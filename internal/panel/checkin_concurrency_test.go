package panel

import (
	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManualCheckinConcurrentCallsMakeOneClaim(t *testing.T) {
	accounts := pool.New("")
	accounts.Add(&auth.Auth{UID: "checkin-fixture", AccessToken: "fixture", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	entered, release := make(chan struct{}), make(chan struct{})
	var claims atomic.Int32
	c := upstream.New()
	c.HTTP = &http.Client{Transport: loginRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "daily-checkin") {
			if claims.Add(1) == 1 {
				close(entered)
				<-release
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{}}`))}, nil
		}
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":1,"msg":"fixture balance failure"}`))}, nil
	})}
	p := New(Config{Pool: accounts, Upstream: c, APIKey: "fixture-key"})
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/panel/api/accounts/checkin-fixture/checkin", nil)
		r.Header.Set("Authorization", "Bearer fixture-key")
		return r
	}
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); p.ServeHTTP(first, request()) }()
	<-entered
	second := httptest.NewRecorder()
	p.ServeHTTP(second, request())
	close(release)
	<-done
	if second.Code != 409 || first.Code != 200 || claims.Load() != 1 {
		t.Fatalf("status first=%d second=%d claims=%d", first.Code, second.Code, claims.Load())
	}
	st, _ := accounts.Status("checkin-fixture")
	if !st.CheckinDone {
		t.Fatal("successful signin was not retained after balance error")
	}
	if !p.tryLockAccount("checkin-fixture") {
		t.Fatal("checkin lock not released")
	}
	p.unlockAccount("checkin-fixture")
}
