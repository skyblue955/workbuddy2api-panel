package server

import (
	"errors"
	"fmt"
	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryAfterGatewayErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		upStatus, want int
	}{
		{"rate_limit", `{"code":6004,"msg":"rate limit"}`, 429, 429},
		{"capacity", `{"code":6021,"msg":"queue.full.title"}`, 429, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			up := &upstream.Client{ChatBaseCN: "https://fixture.invalid", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: tc.upStatus, Header: http.Header{"Retry-After": []string{"17"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			p := testPoolWith(&auth.Auth{UID: "retry", AccessToken: "fixture-secret", ExpiresAt: 9999999999})
			h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 1})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`)))
			if rec.Code != tc.want || rec.Header().Get("Retry-After") != "17" || calls.Load() != 1 {
				t.Fatalf("status=%d retry=%q calls=%d body=%s", rec.Code, rec.Header().Get("Retry-After"), calls.Load(), rec.Body)
			}
			if strings.Contains(rec.Body.String(), "fixture-secret") {
				t.Fatal("credential leaked")
			}
		})
	}
}
func TestRetryAfterBoundsAndCeiling(t *testing.T) {
	for _, tc := range []struct {
		wait time.Duration
		want string
	}{{0, ""}, {-1, ""}, {time.Nanosecond, "1"}, {time.Second + 1, "2"}, {time.Duration(1<<63 - 1), "86400"}} {
		rec := httptest.NewRecorder()
		setRetryAfter(rec, tc.wait)
		if rec.Header().Get("Retry-After") != tc.want {
			t.Fatalf("wait=%v got=%s", tc.wait, rec.Header().Get("Retry-After"))
		}
	}
}
func TestTruncatedStreamIsNotSuccessOrRetried(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var calls atomic.Int32
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				calls.Add(1)
				return 200, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", true
			})
			p := testPoolWith(&auth.Auth{UID: "truncated", AccessToken: "fixture", ExpiresAt: 9999999999})
			metrics := reqlog.New(reqlog.Config{})
			sess := session.New(session.Config{TTL: time.Hour})
			h := NewHandler(Config{Pool: p, Upstream: up, RequestLog: metrics, Session: sess, MaxRotate: 3})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"m","stream":%t,"metadata":{"conversation_id":"truncated-fixture"},"messages":[{"role":"user","content":"fixture"}]}`, stream))))
			st, _ := p.Status("truncated")
			if st.SuccessCount != 0 || !st.LastSuccessTime.IsZero() || calls.Load() != 1 || sess.Count() != 0 {
				t.Fatalf("partial stream recorded success/retried: success=%d calls=%d", st.SuccessCount, calls.Load())
			}
			if strings.Contains(rec.Body.String(), "data: [DONE]") {
				t.Fatal("truncation falsely completed")
			}
			if !strings.Contains(rec.Body.String(), "upstream_parse") {
				t.Fatalf("missing parse error: %s", rec.Body)
			}
			entries := metrics.Snapshot().Recent
			if len(entries) != 1 || entries[0].OK {
				t.Fatalf("false-success metrics: %+v", entries)
			}
			if stream && entries[0].Outcome != reqlog.OutcomeStreamError {
				t.Fatalf("wrong stream outcome: %+v", entries)
			}
		})
	}
}

type partialReadFailure struct{ data []byte }

func (r *partialReadFailure) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errors.New("fixture upstream reset")
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, errors.New("fixture upstream reset")
}

type disconnectedWriter struct{ *httptest.ResponseRecorder }

func (w disconnectedWriter) Write(p []byte) (int, error) {
	return 0, errors.New("fixture client disconnected")
}
func TestStreamReadFailureVsClientDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			var calls atomic.Int32
			up := &upstream.Client{ChatBaseCN: "https://fixture.invalid", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				var reader io.Reader = strings.NewReader(sseOK)
				if !disconnect {
					reader = &partialReadFailure{data: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(reader)}, nil
			})}}
			p := testPoolWith(&auth.Auth{UID: "read-fail", AccessToken: "fixture", ExpiresAt: 9999999999})
			metrics := reqlog.New(reqlog.Config{})
			sticky := session.New(session.Config{TTL: time.Hour})
			h := NewHandler(Config{Pool: p, Upstream: up, RequestLog: metrics, Session: sticky, MaxRotate: 3})
			rec := httptest.NewRecorder()
			var writer http.ResponseWriter = rec
			if disconnect {
				writer = disconnectedWriter{rec}
			}
			h.ServeHTTP(writer, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"metadata":{"conversation_id":"read-fixture"},"messages":[]}`)))
			entries := metrics.Snapshot().Recent
			if len(entries) != 1 || calls.Load() != 1 {
				t.Fatal("missingmetrics or implicitretry")
			}
			if disconnect {
				if entries[0].Outcome != reqlog.OutcomeInterrupted {
					t.Fatalf("clientdisconnect misclassified: %+v", entries)
				}
			} else {
				st, _ := p.Status("read-fail")
				if st.SuccessCount != 0 || sticky.Count() != 0 || entries[0].Outcome != reqlog.OutcomeStreamError || !strings.Contains(rec.Body.String(), "upstream_parse") {
					t.Fatalf("upstreamreadfailure misclassified: %+v", entries)
				}
			}
		})
	}
}
