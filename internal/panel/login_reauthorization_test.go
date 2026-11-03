package panel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

type loginRoundTripFunc func(*http.Request) (*http.Response, error)

func (f loginRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func enterpriseSwitchToken() string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"token_source":"enterprise_switch"}`))
	return "x." + payload + ".x"
}

func reauthPanel(t *testing.T, accountUID, tokenUID string) (*Panel, *auth.Auth) {
	t.Helper()
	p := pool.New("")
	old := &auth.Auth{UID: accountUID, AccessToken: enterpriseSwitchToken(), Domain: "copilot.tencent.com", FilePath: filepath.Join(t.TempDir(), "old.json")}
	p.Add(old)
	return New(Config{Pool: p, AuthDir: t.TempDir()}), old
}

func withLoginTransport(t *testing.T, uid string, token string) {
	t.Helper()
	previous := loginHTTP
	loginHTTP = &http.Client{Transport: loginRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.Contains(r.URL.Path, "/auth/state"):
			body = `{"code":0,"data":{"state":"state-reauth","authUrl":"https://example.invalid/login"}}`
		case strings.Contains(r.URL.Path, "/auth/token"):
			body = `{"code":0,"data":{"accessToken":"` + token + `","refreshToken":"refresh","expiresIn":3600,"domain":"copilot.tencent.com"}}`
		default:
			body = `{"code":0,"data":{"uid":"` + uid + `","nickname":"personal"}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { loginHTTP = previous })
}

func TestReauthorizationSuccessPreservesPathAndSkipsRewards(t *testing.T) {
	p, old := reauthPanel(t, "uid-1", "uid-1")
	withLoginTransport(t, "uid-1", "personal-token")
	start := httptest.NewRecorder()
	p.loginStart(start, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"reauthorize_uid":"uid-1"}`)))
	if start.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body)
	}
	var started struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(start.Body.Bytes(), &started)
	poll := httptest.NewRecorder()
	p.loginPoll(poll, httptest.NewRequest(http.MethodGet, "/?state="+started.State, nil))
	if poll.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", poll.Code, poll.Body)
	}
	if got := p.cfg.Pool.AuthByUID("uid-1"); got == nil || got.FilePath != old.FilePath || got.AccessToken != "personal-token" {
		t.Fatalf("reauth did not preserve account path/state: %#v", got)
	}
	if strings.Contains(poll.Body.String(), "reward") || !strings.Contains(poll.Body.String(), "personal_reauthorization") {
		t.Fatalf("unexpected reauth response: %s", poll.Body)
	}
}

func TestReauthorizationRejectsMismatchAndExpiredWithoutNetwork(t *testing.T) {
	p, _ := reauthPanel(t, "uid-1", "uid-1")
	called := false
	oldHTTP := loginHTTP
	loginHTTP = &http.Client{Transport: loginRoundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}
	t.Cleanup(func() { loginHTTP = oldHTTP })
	p.loginMu.Lock()
	p.logins["expired"] = loginSession{created: time.Now().Add(-loginTTL - time.Second), realm: "cn", reauthorizeUID: "uid-1"}
	p.loginMu.Unlock()
	rec := httptest.NewRecorder()
	p.loginPoll(rec, httptest.NewRequest(http.MethodGet, "/?state=expired", nil))
	if rec.Code != http.StatusNotFound || called {
		t.Fatalf("expired poll code=%d called=%v", rec.Code, called)
	}
	p.loginMu.Lock()
	p.logins["missing"] = loginSession{created: time.Now(), realm: "cn", reauthorizeUID: "missing"}
	p.loginMu.Unlock()
	rec = httptest.NewRecorder()
	p.loginPoll(rec, httptest.NewRequest(http.MethodGet, "/?state=missing", nil))
	if rec.Code != http.StatusBadRequest || called {
		t.Fatalf("missing account code=%d called=%v", rec.Code, called)
	}
}

// Candidate validation must precede persistence: every failure preserves exact bytes.
func TestReauthorizationRejectsInvalidCandidateWithoutOverwrite(t *testing.T) {
	cases := []struct {
		name, uid, token, enterprise, domain, refresh string
		expires                                       int64
	}{
		{"wrong_uid", "other", "personal", "", "copilot.tencent.com", "refresh", 3600},
		{"enterprise_id", "uid-1", "personal", "organization", "copilot.tencent.com", "refresh", 3600},
		{"wrong_realm", "uid-1", "personal", "", "www.workbuddy.ai", "refresh", 3600},
		{"zero_expiry", "uid-1", "personal", "", "copilot.tencent.com", "refresh", 0},
		{"negative_expiry", "uid-1", "personal", "", "copilot.tencent.com", "refresh", -1},
		{"overflow_expiry", "uid-1", "personal", "", "copilot.tencent.com", "refresh", 1<<63 - 1},
		{"no_refresh", "uid-1", "personal", "", "copilot.tencent.com", "", 3600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, old := reauthPanel(t, "uid-1", "uid-1")
			if err := old.SaveAtomic(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(old.FilePath)
			if err != nil {
				t.Fatal(err)
			}
			previous := loginHTTP
			calls := 0
			loginHTTP = &http.Client{Transport: loginRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var data any
				switch r.URL.Path {
				case "/v2/plugin/auth/token":
					data = map[string]any{"accessToken": tc.token, "refreshToken": tc.refresh, "expiresIn": tc.expires, "domain": tc.domain}
				case "/v2/plugin/login/account":
					data = map[string]any{"uid": tc.uid, "enterpriseId": tc.enterprise}
				default:
					t.Errorf("unexpected upstream operation %s", r.URL.Path)
				}
				b, _ := json.Marshal(map[string]any{"code": 0, "data": data})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{}}, nil
			})}
			t.Cleanup(func() { loginHTTP = previous })
			p.logins["candidate"] = loginSession{created: time.Now(), realm: "cn", reauthorizeUID: old.UID}
			rec := httptest.NewRecorder()
			p.loginPoll(rec, httptest.NewRequest("GET", "/?state=candidate", nil))
			if rec.Code < 400 {
				t.Fatalf("invalid candidate accepted: %d", rec.Code)
			}
			after, err := os.ReadFile(old.FilePath)
			if err != nil || !bytes.Equal(before, after) || p.cfg.Pool.AuthByUID(old.UID) != old {
				t.Fatal("invalid candidate changed credential")
			}
			if calls != 2 {
				t.Fatalf("unexpected network calls: %d", calls)
			}
			if _, ok := p.logins["candidate"]; ok {
				t.Fatal("rejected authorization should be discarded")
			}
		})
	}
}

func TestReauthorizationPreservesDisabledPausedAndDeviceState(t *testing.T) {
	p, old := reauthPanel(t, "uid-1", "uid-1")
	old.DeviceToken = "fixture-device"
	if err := old.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	p.cfg.Pool.Disable(old.UID, "fixture disabled")
	p.cfg.Pool.Pause(old.UID)
	p.cfg.Pool.SetCredits(old.UID, 100, 200)
	before, _ := p.cfg.Pool.Status(old.UID)
	withLoginTransport(t, old.UID, "new-personal")
	p.logins["preserve"] = loginSession{created: time.Now(), realm: "cn", reauthorizeUID: old.UID}
	rec := httptest.NewRecorder()
	p.loginPoll(rec, httptest.NewRequest("GET", "/?state=preserve", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	after, _ := p.cfg.Pool.Status(old.UID)
	got := p.cfg.Pool.AuthByUID(old.UID)
	if !after.Disabled || !after.Paused || after.Reason != before.Reason || after.Credits != before.Credits || got.DeviceToken != old.DeviceToken {
		t.Fatal("reauthorization reset runtime/device state")
	}
	if _, ok := p.logins["preserve"]; ok {
		t.Fatal("completed state not consumed")
	}
	rec = httptest.NewRecorder()
	p.loginPoll(rec, httptest.NewRequest("GET", "/?state=preserve", nil))
	if rec.Code != 404 {
		t.Fatalf("replay status=%d", rec.Code)
	}
}

func TestReauthorizationSaveFailurePreservesAccountAndReleasesLease(t *testing.T) {
	p, old := reauthPanel(t, "uid-1", "uid-1")
	// An existing directory as the atomic temp-file path makes saving fail deterministically.
	if err := os.Mkdir(old.FilePath+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	withLoginTransport(t, old.UID, "personal")
	p.logins["failure"] = loginSession{created: time.Now(), realm: "cn", reauthorizeUID: old.UID}
	rec := httptest.NewRecorder()
	p.loginPoll(rec, httptest.NewRequest("GET", "/?state=failure", nil))
	if rec.Code != 500 || p.cfg.Pool.AuthByUID(old.UID) != old {
		t.Fatalf("failed save altered account: status=%d", rec.Code)
	}
	if s, ok := p.logins["failure"]; !ok || s.processing {
		t.Fatal("save failure lost state or retained processing lease")
	}
	if err := os.Remove(old.FilePath + ".tmp"); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	p.loginPoll(rec, httptest.NewRequest("GET", "/?state=failure", nil))
	if rec.Code != 200 {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestReauthorizationConcurrentPollUsesOneCompletion(t *testing.T) {
	p, old := reauthPanel(t, "uid-1", "uid-1")
	started, release := make(chan struct{}), make(chan struct{})
	previous := loginHTTP
	loginHTTP = &http.Client{Transport: loginRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		if r.URL.Path == "/v2/plugin/auth/token" {
			close(started)
			<-release
			body = `{"code":0,"data":{"accessToken":"personal","refreshToken":"fixture","expiresIn":3600,"domain":"copilot.tencent.com"}}`
		} else if r.URL.Path == "/v2/plugin/login/account" {
			body = `{"code":0,"data":{"uid":"uid-1"}}`
		} else {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	t.Cleanup(func() { loginHTTP = previous })
	p.logins["concurrent"] = loginSession{created: time.Now(), realm: "cn", reauthorizeUID: old.UID}
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); p.loginPoll(first, httptest.NewRequest("GET", "/?state=concurrent", nil)) }()
	<-started
	second := httptest.NewRecorder()
	p.loginPoll(second, httptest.NewRequest("GET", "/?state=concurrent", nil))
	close(release)
	<-done
	if first.Code != 200 || second.Code != 409 {
		t.Fatalf("statuses first=%d second=%d", first.Code, second.Code)
	}
}

func TestReauthorizationStartRejectsInvalidInputBeforeNetwork(t *testing.T) {
	p, _ := reauthPanel(t, "uid-1", "uid-1")
	previous := loginHTTP
	loginHTTP = &http.Client{Transport: loginRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid input triggered network")
		return nil, fmt.Errorf("unexpected network")
	})}
	t.Cleanup(func() { loginHTTP = previous })
	for _, body := range []string{`{"reauthorize_uid":`, `{"reauthorize_uid":"missing"}`, `{"reauthorize_uid":"uid-1","realm":"global"}`} {
		rec := httptest.NewRecorder()
		p.loginStart(rec, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if rec.Code != 400 {
			t.Fatalf("input %s: status=%d", body, rec.Code)
		}
	}
}

func TestReauthorizationOverviewReportsContextWithoutCredentials(t *testing.T) {
	p, old := reauthPanel(t, "uid-1", "uid-1")
	rec := httptest.NewRecorder()
	p.overview(rec, httptest.NewRequest("GET", "/", nil))
	var data struct {
		Accounts []struct {
			Enterprise bool `json:"enterprise"`
			Session    bool `json:"enterprise_session"`
			SourceHint bool `json:"enterprise_switch_source"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Accounts) != 1 || data.Accounts[0].Enterprise || !data.Accounts[0].Session || !data.Accounts[0].SourceHint {
		t.Fatalf("incorrect context: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), old.AccessToken) {
		t.Fatal("overview leaked credential")
	}
}

func TestReauthorizationGlobalUsesMatchingEndpointsAndDomain(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p, old := reauthPanel(t, "uid-1", "uid-1")
	old.Domain = "www.workbuddy.ai"
	if _, err := auth.BackfillRealmFor(old, "global"); err != nil {
		t.Fatal(err)
	}
	previous := loginHTTP
	calls := 0
	loginHTTP = &http.Client{Transport: loginRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "www.workbuddy.ai" {
			t.Errorf("wrong realm host: %s", r.URL.Host)
		}
		var body string
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			body = `{"code":0,"data":{"state":"global-state","authUrl":"https://www.workbuddy.ai/login"}}`
		case "/v2/plugin/auth/token":
			body = `{"code":0,"data":{"accessToken":"personal","refreshToken":"fixture","expiresIn":3600,"domain":"www.workbuddy.ai"}}`
		case "/v2/plugin/login/account":
			body = `{"code":0,"data":{"uid":"uid-1"}}`
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	t.Cleanup(func() { loginHTTP = previous })
	rec := httptest.NewRecorder()
	p.loginStart(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"realm":"global","reauthorize_uid":"uid-1"}`)))
	if rec.Code != 200 {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	p.loginPoll(rec, httptest.NewRequest("GET", "/?state=global-state", nil))
	if rec.Code != 200 || calls != 3 || p.cfg.Pool.AuthByUID(old.UID).Realm() != "global" {
		t.Fatalf("global recovery failed status=%d calls=%d", rec.Code, calls)
	}
}

// A personal-login token may retain this source hint. It must not cause an
// endless reject-and-retain-old-credential loop when account identity matches.
func TestReauthorizationAcceptsSourceHintWithoutAssertingEnterprise(t *testing.T) {
	p, old := reauthPanel(t, "uid-1", "uid-1")
	old.DeviceToken = "fixture-device"
	if err := old.SaveAtomic(); err != nil {
		t.Fatal(err)
	}
	p.cfg.Pool.Disable(old.UID, "preserve-disabled")
	p.cfg.Pool.SetCredits(old.UID, 70, 100)
	fresh := "hdr." + base64.RawURLEncoding.EncodeToString([]byte(`{"token_source":"enterprise_switch","fixture":"fresh-login"}`)) + ".sig"
	withLoginTransport(t, old.UID, fresh)
	p.logins["source-hint"] = loginSession{created: time.Now(), realm: "cn", reauthorizeUID: old.UID}
	rec := httptest.NewRecorder()
	p.loginPoll(rec, httptest.NewRequest("GET", "/?state=source-hint", nil))
	if rec.Code != 200 {
		t.Fatalf("source-hint login rejected: status=%d body=%s", rec.Code, rec.Body)
	}
	got := p.cfg.Pool.AuthByUID(old.UID)
	if got == old || got.AccessTokenValue() != fresh || got.AccessTokenValue() == old.AccessTokenValue() || got.FilePath != old.FilePath || got.DeviceToken != old.DeviceToken {
		t.Fatal("fresh source-hint credential not persisted with original account path/device")
	}
	b, err := os.ReadFile(old.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := auth.Parse(b)
	if err != nil || saved.AccessTokenValue() != fresh {
		t.Fatal("credential file did not contain new source-hint token")
	}
	if got.IsEnterprise() || !got.HasEnterpriseSwitchSource() || !got.IsEnterpriseSession() {
		t.Fatal("source hint must not assert enterprise identity or remove existing automation gate")
	}
	st, _ := p.cfg.Pool.Status(old.UID)
	if !st.Disabled || st.Credits != 70 {
		t.Fatal("source-hint reauthorization reset runtime/balance state")
	}
	var response struct {
		Done       bool   `json:"done"`
		Enterprise bool   `json:"enterprise"`
		Source     bool   `json:"enterprise_switch_source"`
		Message    string `json:"checkin_message"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &response) != nil || !response.Done || response.Enterprise || !response.Source || !strings.Contains(response.Message, "不代表企业账号") {
		t.Fatalf("misleading response: %s", rec.Body)
	}
	if _, ok := p.logins["source-hint"]; ok {
		t.Fatal("successful state not consumed")
	}
	// No Upstream configured: any automatic checkin/reward/balance call would panic.
}
