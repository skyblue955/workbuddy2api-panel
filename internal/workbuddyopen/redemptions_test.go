package workbuddyopen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string, headers http.Header) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func validSingle() RedemptionRequest {
	return RedemptionRequest{Code: "code", RequestID: "request-1"}
}

func TestRedeemKeepsIdempotencyKeyAcrossCallerRetries(t *testing.T) {
	const body = `{"status":"success","flow_no":"same-flow","credits":1,"open_id":"fixture"}`
	calls := 0
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"request_id":"request-1"`) {
			t.Error("caller idempotency key changed")
		}
		return response(200, body, nil), nil
	})}}
	for i := 0; i < 2; i++ {
		if _, err := c.Redeem(context.Background(), "fixture", validSingle()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d; no implicit retry expected", calls)
	}
}

func validDual() RedemptionRequest {
	return RedemptionRequest{GiftKey: "giftkey", GiftCode: "giftcode", RequestID: "request-2"}
}

func TestRedeemSingleAndDual(t *testing.T) {
	calls := 0
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != RedemptionsURL || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("request=%s %s auth=%q", r.Method, r.URL, r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("content-type=%q accept=%q", r.Header.Get("Content-Type"), r.Header.Get("Accept"))
		}
		var sent map[string]string
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("request body: %v", err)
		}
		if calls == 1 {
			if sent["code"] != "code" || sent["request_id"] != "request-1" || len(sent) != 2 {
				t.Errorf("single body=%v", sent)
			}
			return response(200, `{"status":"success","flow_no":"flow-1","credits":12,"open_id":"open-1"}`, nil), nil
		}
		if sent["gift_key"] != "giftkey" || sent["gift_code"] != "giftcode" || sent["request_id"] != "request-2" || len(sent) != 3 {
			t.Errorf("dual body=%v", sent)
		}
		return response(200, `{"status":"success","flow_no":"flow-2","credits":0,"open_id":"open-2"}`, nil), nil
	})}}
	for _, in := range []RedemptionRequest{validSingle(), validDual()} {
		out, err := c.Redeem(context.Background(), "test-token", in)
		if err != nil || out == nil || out.Status != "success" || out.FlowNo == "" || out.OpenID == "" {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d want 2", calls)
	}
}

func TestRedeemRejectsInvalidRequestsWithoutTransport(t *testing.T) {
	calls := 0
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected") })}}
	cases := []RedemptionRequest{
		{Code: "", RequestID: "request-1"},
		{Code: strings.Repeat("x", 65), RequestID: "request-1"},
		{Code: "code", RequestID: "short"},
		{Code: "code", RequestID: "        "},
		{Code: "code", GiftKey: "giftkey", RequestID: "request-1"},
		{GiftKey: "giftkey", RequestID: "request-1"},
		{GiftCode: "giftcode", RequestID: "request-1"},
		{GiftKey: "short", GiftCode: "giftcode", RequestID: "request-1"},
	}
	for _, in := range cases {
		if _, err := c.Redeem(context.Background(), "test-token", in); err == nil {
			t.Errorf("input=%+v accepted", in)
		}
	}
	if calls != 0 {
		t.Fatalf("transport calls=%d want 0", calls)
	}
}

func TestRedeemStatusesAreRedactedAndNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(status, `{"token":"secret","code":"private"}`, nil), nil
			})}}
			_, err := c.Redeem(context.Background(), "test-token", validSingle())
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("err=%v", err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d want 1", calls)
			}
		})
	}
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestRedeemClosesResponseBodyOnEveryResponseShape(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "400", status: http.StatusBadRequest, body: `{"error":"bad request"}`},
		{name: "401", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
		{name: "403", status: http.StatusForbidden, body: `{"error":"forbidden"}`},
		{name: "429", status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`},
		{name: "500", status: http.StatusInternalServerError, body: `{"error":"server error"}`},
		{name: "307", status: http.StatusTemporaryRedirect, body: `{"error":"redirect"}`},
		{name: "success", status: http.StatusOK, body: `{"status":"success","flow_no":"flow","credits":1,"open_id":"open"}`},
		{name: "malformed", status: http.StatusOK, body: `{`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &closeTrackingBody{Reader: strings.NewReader(tc.body)}
			c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Status:     http.StatusText(tc.status),
					Header:     make(http.Header),
					Body:       body,
				}, nil
			})}}
			_, _ = c.Redeem(context.Background(), "test-token", validSingle())
			if !body.closed {
				t.Fatalf("response body was not closed for status=%d", tc.status)
			}
		})
	}
}

func TestRedeemRejectsMalformedTruncatedOversizeAndUnconfirmed(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":       `{`,
		"truncated":       `{"status":"success","flow_no":"flow"`,
		"wrong status":    `{"status":"pending","flow_no":"flow","credits":1,"open_id":"open"}`,
		"missing ids":     `{"status":"success","flow_no":"","credits":1,"open_id":"open"}`,
		"missing credits": `{"status":"success","flow_no":"flow","open_id":"open"}`,
		"null credits":    `{"status":"success","flow_no":"flow","credits":null,"open_id":"open"}`,
		"whitespace ids":  `{"status":"success","flow_no":"  ","credits":1,"open_id":"open"}`,
		"negative":        `{"status":"success","flow_no":"flow","credits":-1,"open_id":"open"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body, nil), nil })}}
			if out, err := c.Redeem(context.Background(), "test-token", validSingle()); err == nil || out != nil {
				t.Fatalf("out=%+v err=%v", out, err)
			}
		})
	}
	t.Run("oversize", func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, strings.Repeat("x", maxResponseBody+1), nil), nil
		})}}
		if _, err := c.Redeem(context.Background(), "test-token", validSingle()); err == nil {
			t.Fatal("oversize response accepted")
		}
	})
}

func TestRedeemDoesNotLeakTransportOrBodyErrors(t *testing.T) {
	for name, transportErr := range map[string]error{
		"transport": errors.New("token=secret code=private"),
	} {
		t.Run(name, func(t *testing.T) {
			c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, transportErr })}}
			_, err := c.Redeem(context.Background(), "test-token", validSingle())
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	t.Run("body", func(t *testing.T) {
		body := &failingBody{}
		c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: body}, nil
		})}}
		_, err := c.Redeem(context.Background(), "test-token", validSingle())
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
			t.Fatalf("err=%v", err)
		}
	})
}

type failingBody struct{}

func (*failingBody) Read([]byte) (int, error) { return 0, errors.New("body token=secret code=private") }
func (*failingBody) Close() error             { return nil }

func TestRedeemTimeoutPreservesDeadline(t *testing.T) {
	c := &Client{Timeout: time.Nanosecond, HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	_, err := c.Redeem(context.Background(), "test-token", validSingle())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline exceeded", err)
	}
}

func TestRedeemCancellationAndRedirect(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}}
		if _, err := c.Redeem(ctx, "test-token", validSingle()); err == nil {
			t.Fatal("cancelled request succeeded")
		}
	})
	t.Run("redirect is not followed", func(t *testing.T) {
		calls := 0
		c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return response(307, "", http.Header{"Location": []string{RedemptionsURL}}), nil
			}
			return response(200, `{"status":"success","flow_no":"flow","credits":1,"open_id":"open"}`, nil), nil
		})}, Timeout: time.Second}
		if _, err := c.Redeem(context.Background(), "test-token", validSingle()); err == nil {
			t.Fatal("redirect response accepted")
		}
		if calls != 1 {
			t.Fatalf("calls=%d want 1", calls)
		}
	})
}
