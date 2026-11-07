package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestAccountTaskAutoReadyModelChatSkipsActionAndClaims(t *testing.T) {
	var runs, claims, lists atomic.Int32
	p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/activity/growth/tasks":
			lists.Add(1)
			_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{
				sweepTask("Model_chat_GLM5.2", 1, 1, "accepted", nil),
			})))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			claims.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"already_claimed":false,"credit":100,"energy":5}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
		}
	}))
	withStubbedAutoAction(t, "Model_chat_GLM5.2", func(*Panel, *auth.Auth) (string, error) {
		runs.Add(1)
		return "action-ran", nil
	}, func() {
		got := callAccountTaskAuto(t, p, "Model_chat_GLM5.2")
		if runs.Load() != 0 {
			t.Fatalf("ready task replayed action %d times", runs.Load())
		}
		if claims.Load() != 1 {
			t.Fatalf("ready task claim calls=%d, want 1", claims.Load())
		}
		if lists.Load() < 2 || got["claimed"] != true {
			t.Fatalf("response=%v lists=%d, want readback and claim", got, lists.Load())
		}
	})
}

func TestAccountTaskAutoReadyModelChatRouteSkipsRealAction(t *testing.T) {
	oldGap := reportGap
	reportGap = 0
	t.Cleanup(func() { reportGap = oldGap })
	var lists, accepts, chats, reports, claims atomic.Int32
	transport := codeEntryFixtureTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v2/activity/growth/tasks":
			lists.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", sweepTasksBody([]map[string]any{
				sweepTask("Model_chat_GLM5.2", 1, 1, "accepted", nil),
			})), nil
		case "/v2/activity/growth/tasks/accept":
			accepts.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", `{"code":0,"msg":"OK","data":{}}`), nil
		case "/v2/chat/completions":
			chats.Add(1)
			return codeEntryResponse(http.StatusOK, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
		case "/v2/report":
			reports.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", `{"code":0,"msg":"OK","data":{}}`), nil
		case "/activity/growth/tasks/Model_chat_GLM5.2/claim":
			claims.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", `{"code":0,"msg":"OK","data":{"already_claimed":false,"credit":100,"energy":5}}`), nil
		default:
			return nil, fmt.Errorf("unexpected outbound path %s", r.URL.Path)
		}
	})
	p := New(Config{
		Pool:     pool.New(""),
		Upstream: &upstream.Client{HTTP: &http.Client{Transport: transport}, ChatBaseCN: "https://fixture.invalid", BillingBaseCN: "https://fixture.invalid", WebBaseCN: "https://fixture.invalid"},
	})
	t.Cleanup(p.cfg.Pool.Close)
	p.cfg.Pool.Add(&auth.Auth{UID: "u1", AccessToken: "fixture"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/u1/tasks/auto", strings.NewReader(`{"task_code":"Model_chat_GLM5.2"}`))
	p.ServeHTTP(w, r)
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if w.Code != http.StatusOK || got["claimed"] != true {
		t.Fatalf("status=%d response=%v", w.Code, got)
	}
	t.Logf("READY_ROUTE lists=%d accepts=%d chats=%d reports=%d claims=%d", lists.Load(), accepts.Load(), chats.Load(), reports.Load(), claims.Load())
	if lists.Load() != 2 || accepts.Load() != 0 || chats.Load() != 0 || reports.Load() != 0 || claims.Load() != 1 {
		t.Fatalf("outbound counts lists=%d accepts=%d chats=%d reports=%d claims=%d", lists.Load(), accepts.Load(), chats.Load(), reports.Load(), claims.Load())
	}
}

func TestAccountTaskAutoReadyReadbackFailureDoesNotClaim(t *testing.T) {
	old := claimPollAttempts
	claimPollAttempts = 1
	t.Cleanup(func() { claimPollAttempts = old })
	var lists, claims atomic.Int32
	p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/activity/growth/tasks" {
			if lists.Add(1) == 1 {
				_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("Model_chat_GLM5.2", 1, 1, "accepted", nil)})))
				return
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/claim") {
			claims.Add(1)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	withStubbedAutoAction(t, "Model_chat_GLM5.2", func(*Panel, *auth.Auth) (string, error) { return "unused", nil }, func() {
		got := callAccountTaskAuto(t, p, "Model_chat_GLM5.2")
		if claims.Load() != 0 || got["claimed"] == true || got["skipped"] == true {
			t.Fatalf("readback failure response=%v claims=%d", got, claims.Load())
		}
	})
}

func TestAccountTaskAutoReadyClaimReceiptDoesNotGreenwash(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{name: "zero", body: `{"code":0,"msg":"OK","data":{"already_claimed":false,"credit":0,"energy":0}}`, code: http.StatusOK},
		{name: "failure", body: `{"code":1,"msg":"claim failed","data":{}}`, code: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var claims atomic.Int32
			p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/activity/growth/tasks" {
					_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("Model_chat_GLM5.2", 1, 1, "accepted", nil)})))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/claim") {
					claims.Add(1)
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
			}))
			withStubbedAutoAction(t, "Model_chat_GLM5.2", func(*Panel, *auth.Auth) (string, error) { return "unused", nil }, func() {
				got := callAccountTaskAuto(t, p, "Model_chat_GLM5.2")
				if claims.Load() != 1 || got["claimed"] == true || got["skipped"] == true {
					t.Fatalf("receipt=%s response=%v claims=%d", tc.name, got, claims.Load())
				}
				if tc.name == "zero" && got["no_reward"] != true {
					t.Fatalf("zero receipt response=%v, want no_reward=true", got)
				}
				if tc.name == "failure" {
					if _, ok := got["claim_error"].(string); !ok {
						t.Fatalf("failed receipt response=%v, want claim_error", got)
					}
				}
			})
		})
	}
}

func TestAccountTaskAutoReadyMPRouteSkipsActionAndUsesMPClaim(t *testing.T) {
	var defaultLists, mpLists, accepts, chats, reports, claims atomic.Int32
	transport := codeEntryFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "chat.fixture" {
			return nil, fmt.Errorf("unexpected non-chat outbound host %s", r.URL.Host)
		}
		switch {
		case r.URL.Path == "/v2/activity/growth/tasks" && r.Header.Get("X-Client-Platform") == "miniprogram":
			mpLists.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", sweepTasksBody([]map[string]any{
				sweepTask("Sequential_Tasks_1", 1, 1, "accepted", nil),
			})), nil
		case r.URL.Path == "/v2/activity/growth/tasks":
			defaultLists.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", sweepTasksBody(nil)), nil
		case r.URL.Path == "/activity/growth/tasks/Sequential_Tasks_1/claim":
			if r.Header.Get("X-Client-Platform") != "miniprogram" {
				return nil, fmt.Errorf("mp claim missing miniprogram header")
			}
			claims.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", `{"code":0,"msg":"OK","data":{"already_claimed":false,"credit":100,"energy":5}}`), nil
		case r.URL.Path == "/v2/activity/growth/tasks/accept":
			accepts.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", `{"code":0,"msg":"OK","data":{}}`), nil
		case r.URL.Path == "/v2/chat/completions":
			chats.Add(1)
			return codeEntryResponse(http.StatusOK, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
		case r.URL.Path == "/v2/report":
			reports.Add(1)
			return codeEntryResponse(http.StatusOK, "application/json", `{"code":0,"msg":"OK","data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected outbound path %s headers=%v", r.URL.Path, r.Header)
		}
	})
	p := New(Config{
		Pool:     pool.New(""),
		Upstream: &upstream.Client{HTTP: &http.Client{Transport: transport}, ChatBaseCN: "https://chat.fixture", BillingBaseCN: "https://web.fixture", WebBaseCN: "https://web.fixture"},
	})
	p.cfg.Pool.Add(&auth.Auth{UID: "u1", AccessToken: "fixture"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/u1/tasks/auto", strings.NewReader(`{"task_code":"Sequential_Tasks_1"}`))
	p.ServeHTTP(w, r)
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if w.Code != http.StatusOK || got["claimed"] != true {
		t.Fatalf("status=%d response=%v", w.Code, got)
	}
	if defaultLists.Load() != 1 || mpLists.Load() != 2 || accepts.Load() != 0 || chats.Load() != 0 || reports.Load() != 0 || claims.Load() != 1 {
		t.Fatalf("outbound counts default=%d mp=%d accepts=%d chats=%d reports=%d claims=%d", defaultLists.Load(), mpLists.Load(), accepts.Load(), chats.Load(), reports.Load(), claims.Load())
	}
}

func TestAccountTaskAutoReadyReadbackRevokedDoesNotClaim(t *testing.T) {
	old := claimPollAttempts
	claimPollAttempts = 1
	t.Cleanup(func() { claimPollAttempts = old })
	var runs, claims, lists atomic.Int32
	p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/activity/growth/tasks":
			if lists.Add(1) == 1 {
				_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{
					sweepTask("Model_chat_GLM5.2", 1, 1, "accepted", nil),
				})))
			} else {
				_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{
					sweepTask("Model_chat_GLM5.2", 1, 0, "accepted", nil),
				})))
			}
		case strings.HasSuffix(r.URL.Path, "/claim"):
			claims.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{"credit":100}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
		}
	}))
	withStubbedAutoAction(t, "Model_chat_GLM5.2", func(*Panel, *auth.Auth) (string, error) {
		runs.Add(1)
		return "action-ran", nil
	}, func() {
		got := callAccountTaskAuto(t, p, "Model_chat_GLM5.2")
		if runs.Load() != 0 || claims.Load() != 0 {
			t.Fatalf("revoked ready task mutated: runs=%d claims=%d response=%v", runs.Load(), claims.Load(), got)
		}
		if got["claimable"] == true || got["claimed"] == true {
			t.Fatalf("revoked task remained claimable: %v", got)
		}
	})
}

func TestAccountTaskAutoClaimedOrInactiveSkipsAction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
		state string
	}{
		{name: "claimed", state: "claimed"},
		{name: "inactive", state: "accepted", extra: map[string]any{"locked": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var runs, claims atomic.Int32
			p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/activity/growth/tasks" {
					_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{
						sweepTask("Model_chat_GLM5.2", 1, 1, tc.state, tc.extra),
					})))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/claim") {
					claims.Add(1)
				}
				_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
			}))
			withStubbedAutoAction(t, "Model_chat_GLM5.2", func(*Panel, *auth.Auth) (string, error) {
				runs.Add(1)
				return "action-ran", nil
			}, func() {
				callAccountTaskAuto(t, p, "Model_chat_GLM5.2")
				if runs.Load() != 0 || claims.Load() != 0 {
					t.Fatalf("%s task mutated: runs=%d claims=%d", tc.name, runs.Load(), claims.Load())
				}
			})
		})
	}
}

func TestAccountTaskAutoNotReadyStillRunsAction(t *testing.T) {
	oldAttempts := claimPollAttempts
	claimPollAttempts = 1
	t.Cleanup(func() { claimPollAttempts = oldAttempts })
	var runs atomic.Int32
	p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/activity/growth/tasks" {
			_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{
				sweepTask("Model_chat_GLM5.2", 1, 0, "accepted", nil),
			})))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	withStubbedAutoAction(t, "Model_chat_GLM5.2", func(*Panel, *auth.Auth) (string, error) {
		runs.Add(1)
		return "action-ran", nil
	}, func() {
		callAccountTaskAuto(t, p, "Model_chat_GLM5.2")
		if runs.Load() != 1 {
			t.Fatalf("not-ready task action calls=%d, want 1", runs.Load())
		}
	})
}

func callAccountTaskAuto(t *testing.T, p *Panel, code string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/accounts/u1/tasks/auto", strings.NewReader(`{"task_code":"`+code+`"}`))
	r.SetPathValue("uid", "u1")
	w := httptest.NewRecorder()
	p.accountTaskAuto(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}
