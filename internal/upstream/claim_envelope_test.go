package upstream

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

type claimFixtureTransport struct {
	status   int
	body     string
	location string
	n        atomic.Int32
}

func (f *claimFixtureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.n.Add(1)
	resp := &http.Response{
		StatusCode: f.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}
	if f.location != "" {
		resp.Header.Set("Location", f.location)
	}
	return resp, nil
}

func claimFixtureClient(f http.RoundTripper) (*Client, *auth.Auth) {
	return &Client{HTTP: &http.Client{Transport: f}}, &auth.Auth{AccessToken: "fixture", UID: "fixture"}
}

func TestClaimEnvelopeAcceptsValidAndAlreadyClaimedReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		credit, energy int64
		already        bool
	}{
		{"valid", `{"code":0,"data":{"credit":100,"energy":5}}`, 100, 5, false},
		{"already", `{"code":0,"data":{"credit":0,"energy":0,"already_claimed":true}}`, 0, 0, true},
		{"zero", `{"code":0,"data":{"credit":0,"energy":0}}`, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &claimFixtureTransport{status: http.StatusOK, body: tc.body}
			c, a := claimFixtureClient(f)
			got, err := c.ClaimRewardReceipt(a, "fixture-task")
			if err != nil || got.Credit != tc.credit || got.Energy != tc.energy || got.AlreadyClaimed != tc.already {
				t.Fatalf("receipt=%+v err=%v", got, err)
			}
		})
	}
}

func TestClaimEnvelopeRejectsMalformedSuccess(t *testing.T) {
	for _, body := range []string{
		`{"data":{"credit":100}}`,
		`{"code":null,"data":{"credit":100}}`,
		`{"code":0,"code":0,"data":{"credit":100}}`,
		`{"code":"0","data":{"credit":100}}`,
		`{"code":0,"data":{"credit":100}} trailing`,
	} {
		f := &claimFixtureTransport{status: http.StatusOK, body: body}
		c, a := claimFixtureClient(f)
		if got, err := c.ClaimRewardReceipt(a, "fixture-task"); err == nil || got.HasReward() {
			t.Fatalf("body %q got=%+v err=%v", body, got, err)
		}
	}
	oversized := `{"code":0,"data":{"padding":"` + strings.Repeat("x", claimEnvelopeMaxBody) + `"}}`
	f := &claimFixtureTransport{status: http.StatusOK, body: oversized}
	c, a := claimFixtureClient(f)
	if _, err := c.ClaimRewardReceipt(a, "fixture-task"); err == nil {
		t.Fatal("oversized envelope accepted")
	}
}

func TestClaimEnvelopeRejectsRedirectWithoutFollowing(t *testing.T) {
	f := &claimFixtureTransport{status: http.StatusFound, location: "https://redirect.invalid", body: `{"code":0,"data":{"credit":100}}`}
	c, a := claimFixtureClient(f)
	if _, err := c.ClaimRewardReceipt(a, "fixture-task"); err == nil {
		t.Fatal("redirect accepted")
	}
	if got := f.n.Load(); got != 1 {
		t.Fatalf("redirect hops=%d", got)
	}
}

func TestClaimEnvelopePreservesHTTPErrorClassification(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		f := &claimFixtureTransport{status: status, body: `{"code":1,"msg":"fixture"}`}
		c, a := claimFixtureClient(f)
		_, err := c.ClaimRewardReceipt(a, "fixture-task")
		ue, ok := err.(*Error)
		if !ok || ue.Status != status {
			t.Fatalf("status=%d err=%T %+v", status, err, err)
		}
	}
}

func TestClaimEnvelopeDoesNotMutateHTTPClient(t *testing.T) {
	f := &claimFixtureTransport{status: http.StatusOK, body: `{"code":0,"data":{"credit":1}}`}
	sentinel := errors.New("fixture redirect")
	checkRedirect := func(*http.Request, []*http.Request) error { return sentinel }
	jar := &fixtureCookieJar{}
	hc := &http.Client{Transport: f, CheckRedirect: checkRedirect, Jar: jar, Timeout: 3 * time.Second}
	c := &Client{HTTP: hc}
	a := &auth.Auth{AccessToken: "fixture"}
	for i := 0; i < 8; i++ {
		if _, err := c.ClaimRewardReceipt(a, "fixture-task"); err != nil {
			t.Fatal(err)
		}
	}
	if hc.Transport != f || hc.Jar != (http.CookieJar)(jar) || hc.Timeout != 3*time.Second || hc.CheckRedirect == nil || hc.CheckRedirect(nil, nil) != sentinel {
		t.Fatal("shared http client was mutated")
	}
}

type fixtureCookieJar struct{}

func (*fixtureCookieJar) Cookies(*url.URL) []*http.Cookie     { return nil }
func (*fixtureCookieJar) SetCookies(*url.URL, []*http.Cookie) {}

func TestClaimEnvelopePreservesNonZeroCodeClassification(t *testing.T) {
	for _, msg := range []string{"session expired 12153", "queue.full.title 6021"} {
		body := `{"code":1,"msg":"` + msg + `","data":null}`
		f1 := &claimFixtureTransport{status: http.StatusOK, body: body}
		c1, _ := claimFixtureClient(f1)
		req1, _ := http.NewRequest(http.MethodPost, "https://fixture.invalid/claim", nil)
		_, oldErr := c1.doJSON(req1)
		f2 := &claimFixtureTransport{status: http.StatusOK, body: body}
		c2, a2 := claimFixtureClient(f2)
		_, newErr := c2.ClaimRewardReceipt(a2, "fixture-task")
		old, oldOK := oldErr.(*Error)
		got, gotOK := newErr.(*Error)
		if !oldOK || !gotOK || old.Kind != got.Kind {
			t.Fatalf("msg=%q old=%T/%v new=%T/%v", msg, oldErr, oldErr, newErr, newErr)
		}
	}
}

func TestClaimEnvelopeConcurrentCallsDoNotMutateHTTPClient(t *testing.T) {
	f := &claimFixtureTransport{status: http.StatusOK, body: `{"code":0,"data":{"credit":1}}`}
	hc := &http.Client{Transport: f}
	c := &Client{HTTP: hc}
	a := &auth.Auth{AccessToken: "fixture"}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ClaimRewardReceipt(a, "fixture-task"); err != nil {
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if hc.Transport != f || hc.CheckRedirect != nil {
		t.Fatal("shared http client was mutated")
	}
}

func TestClaimRewardMPFallsBackOnceOnHTTP400(t *testing.T) {
	rt := &mpFixtureTransport{}
	c, a := claimFixtureClient(rt)
	got, err := c.ClaimRewardMPReceipt(a, "fixture-task")
	if err != nil || got.Credit != 7 || got.Energy != 2 || rt.n.Load() != 2 {
		t.Fatalf("receipt=%+v err=%v calls=%d", got, err, rt.n.Load())
	}
}

type mpFixtureTransport struct{ n atomic.Int32 }

func (f *mpFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.n.Add(1)
	if req.Header.Get("X-Client-Platform") == mpPlatform {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":1}`))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"credit":7,"energy":2}}`))}, nil
}
