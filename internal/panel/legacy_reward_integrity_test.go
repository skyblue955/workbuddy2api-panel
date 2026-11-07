package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// Duplicate task codes are ambiguous: lookup must fail instead of selecting a
// first row that could be inactive while a later row is claimable.
func TestTaskByCodeRejectsAmbiguousRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{
			"tasks": []map[string]any{
				{"task_code": "chat_5", "locked": true, "target": 5, "current": 5},
				{"task_code": "chat_5", "target": 5, "current": 5},
			},
		}})
	}))
	defer srv.Close()
	p := &Panel{cfg: Config{Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}}}
	if _, err := p.taskByCode(&auth.Auth{AccessToken: "fixture", UID: "fixture"}, "chat_5"); err == nil {
		t.Fatal("ambiguous task code was accepted")
	}
}

func TestTaskSweepLockedAndExpiredReachedProgressDoesNotMutate(t *testing.T) {
	b := &sweepBackend{listFn: func(isMP bool) []map[string]any {
		if isMP {
			return nil
		}
		return []map[string]any{
			sweepTask("locked", 1, 1, "accepted", map[string]any{"locked": true}),
			sweepTask("expired", 1, 1, "accepted", map[string]any{"status": "expired"}),
		}
	}}
	p := newSweepPanel(t, b, &auth.Auth{UID: "u1", AccessToken: "fixture"})
	st := p.RunTaskSweepOnce()
	if st.Accepted != 0 || st.Attempted != 0 || st.Claimed != 0 || b.accepts.Load() != 0 || b.claims.Load() != 0 {
		t.Fatalf("inactive reached tasks mutated: stats=%+v accepts=%d claims=%d", st, b.accepts.Load(), b.claims.Load())
	}
}

func TestTaskSweepDuplicateCodeConflictingRowsDoesNotMutate(t *testing.T) {
	b := &sweepBackend{listFn: func(isMP bool) []map[string]any {
		if isMP {
			return nil
		}
		return []map[string]any{
			sweepTask("dup", 1, 0, "not_accepted", nil),
			sweepTask("dup", 1, 1, "accepted", nil),
		}
	}}
	p := newSweepPanel(t, b, &auth.Auth{UID: "u1", AccessToken: "fixture"})
	st := p.RunTaskSweepOnce()
	if st.Errors != 1 || st.Accepted != 0 || st.Attempted != 0 || st.Claimed != 0 || b.accepts.Load() != 0 || b.claims.Load() != 0 {
		t.Fatalf("duplicate code was mutated: stats=%+v accepts=%d claims=%d", st, b.accepts.Load(), b.claims.Load())
	}
}

func TestTaskSweepPositiveAward(t *testing.T) {
	b := &sweepBackend{
		listFn: func(isMP bool) []map[string]any {
			if isMP {
				return nil
			}
			return []map[string]any{sweepTask("award", 1, 1, "accepted", nil)}
		},
		claimFn: func(string) (int, map[string]any) { return 0, map[string]any{"already_claimed": false, "credit": 100} },
	}
	st := newSweepPanel(t, b, &auth.Auth{UID: "u1", AccessToken: "fixture"}).RunTaskSweepOnce()
	if st.Attempted != 1 || st.Claimed != 1 || st.Credits != 100 || st.AlreadyClaimed != 0 {
		t.Fatalf("positive award stats=%+v", st)
	}
}

func TestTaskSweepAlreadyClaimedAndNoRewardReceipts(t *testing.T) {
	tests := []struct {
		name              string
		data              map[string]any
		already, noReward int
	}{
		{"already", map[string]any{"already_claimed": true}, 1, 0},
		{"zero_false", map[string]any{"already_claimed": false, "credit": 0, "energy": 0}, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &sweepBackend{
				listFn: func(isMP bool) []map[string]any {
					if isMP {
						return nil
					}
					return []map[string]any{sweepTask("receipt", 1, 1, "accepted", nil)}
				},
				claimFn: func(string) (int, map[string]any) { return 0, tc.data },
			}
			st := newSweepPanel(t, b, &auth.Auth{UID: "u1", AccessToken: "fixture"}).RunTaskSweepOnce()
			if st.Attempted != 1 || st.AlreadyClaimed != tc.already || st.NoReward != tc.noReward || st.Claimed != 0 {
				t.Fatalf("stats=%+v", st)
			}
		})
	}
}

func TestTaskSweepMalformedNullAndEmptyReceiptsAreErrorsWithoutRetry(t *testing.T) {
	for _, body := range []string{`null`, `{}`} {
		t.Run(body, func(t *testing.T) {
			var claims atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/activity/growth/tasks" {
					w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("bad", 1, 1, "accepted", nil)})))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/claim") {
					claims.Add(1)
					w.Write([]byte(`{"code":0,"msg":"OK","data":` + body + `}`))
					return
				}
				w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
			}))
			defer srv.Close()
			p := &Panel{cfg: Config{Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}}}
			p.cfg.Pool = pool.New("")
			p.cfg.Pool.Add(&auth.Auth{UID: "u1", AccessToken: "fixture"})
			st := p.RunTaskSweepOnce()
			if st.Errors != 1 || st.Attempted != 1 || st.Claimed != 0 || claims.Load() != 1 {
				t.Fatalf("body=%s stats=%+v claims=%d", body, st, claims.Load())
			}
		})
	}
}

func TestLegacyClaimMethodsKeepHTTPPaths(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Write([]byte(`{"code":0,"msg":"OK","data":{"credit":100,"energy":5}}`))
	}))
	defer srv.Close()
	c := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}
	a := &auth.Auth{UID: "u1", AccessToken: "fixture"}
	if _, _, err := c.ClaimReward(a, "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ClaimRewardMP(a, "legacy"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "POST /activity/growth/tasks/legacy/claim" || paths[1] != paths[0] {
		t.Fatalf("paths=%v", paths)
	}
}

func withStubbedAutoAction(t *testing.T, code string, run func(*Panel, *auth.Auth) (string, error), f func()) {
	t.Helper()
	for i := range autoActions {
		if autoActions[i].TaskCode == code {
			old := autoActions[i].run
			autoActions[i].run = run
			t.Cleanup(func() { autoActions[i].run = old })
			f()
			return
		}
	}
	t.Fatalf("action %q not found", code)
}

func autoFixturePanel(t *testing.T, handler http.Handler) *Panel {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := &Panel{cfg: Config{Pool: pool.New(""), Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL}}}
	p.cfg.Pool.Add(&auth.Auth{UID: "u1", AccessToken: "fixture"})
	return p
}

func TestRunAutoAllReadyButLockedDoesNotRunOrClaim(t *testing.T) {
	oldGap, oldAttempts := reportGap, claimPollAttempts
	reportGap, claimPollAttempts = 0, 1
	t.Cleanup(func() { reportGap, claimPollAttempts = oldGap, oldAttempts })
	var runs atomic.Int32
	p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/activity/growth/tasks" {
			w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("chat_5", 1, 1, "accepted", map[string]any{"locked": true})})))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/claim") {
			t.Errorf("locked task claimed: %s", r.URL.Path)
		}
		w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	withStubbedAutoAction(t, "chat_5", func(*Panel, *auth.Auth) (string, error) { runs.Add(1); return "stub", nil }, func() {
		out := p.runAutoAll(&auth.Auth{UID: "u1", AccessToken: "fixture"})
		if runs.Load() != 0 {
			t.Fatalf("locked task action ran %d times", runs.Load())
		}
		for _, item := range out {
			if item["task_code"] == "chat_5" && item["status"] != "skipped" {
				t.Fatalf("locked item=%v", item)
			}
		}
	})
}

func TestAccountTaskAutoReadbackNotReadyIsUnconfirmed(t *testing.T) {
	oldAttempts := claimPollAttempts
	claimPollAttempts = 1
	t.Cleanup(func() { claimPollAttempts = oldAttempts })
	var list atomic.Int32
	p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/activity/growth/tasks" {
			list.Add(1)
			w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("chat_5", 1, 0, "accepted", nil)})))
			return
		}
		w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	withStubbedAutoAction(t, "chat_5", func(*Panel, *auth.Auth) (string, error) { return "stub-200", nil }, func() {
		r := httptest.NewRequest(http.MethodPost, "/accounts/u1/tasks/auto", strings.NewReader(`{"task_code":"chat_5"}`))
		r.SetPathValue("uid", "u1")
		w := httptest.NewRecorder()
		p.accountTaskAuto(w, r)
		var got map[string]any
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || got["progress_readback_ok"] != true || got["verified"] != false {
			t.Fatalf("status=%d response=%v lists=%d", w.Code, got, list.Load())
		}
		if got["claimable"] == true || got["claimed"] == true {
			t.Fatalf("not-ready task was treated as done: %v", got)
		}
	})
}

func TestAccountTaskAutoReadbackFailureIsUnknownUnconfirmed(t *testing.T) {
	oldAttempts := claimPollAttempts
	claimPollAttempts = 1
	t.Cleanup(func() { claimPollAttempts = oldAttempts })
	p := autoFixturePanel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/activity/growth/tasks" {
			if r.Header.Get("X-Readback-Fail") == "1" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("chat_5", 1, 0, "accepted", nil)})))
			return
		}
		w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	withStubbedAutoAction(t, "chat_5", func(*Panel, *auth.Auth) (string, error) { return "stub-200", nil }, func() {
		// The fixture flips to an upstream read failure after the initial list.
		// Header injection is not part of production code, so use a client transport
		// that marks the second GET while preserving the same deterministic server.
		var gets atomic.Int32
		base := p.cfg.Upstream.HTTP
		p.cfg.Upstream.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/v2/activity/growth/tasks" && gets.Add(1) > 1 {
				req.Header.Set("X-Readback-Fail", "1")
			}
			return base.Transport.RoundTrip(req)
		})}
		r := httptest.NewRequest(http.MethodPost, "/accounts/u1/tasks/auto", strings.NewReader(`{"task_code":"chat_5"}`))
		r.SetPathValue("uid", "u1")
		w := httptest.NewRecorder()
		p.accountTaskAuto(w, r)
		var got map[string]any
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || got["progress_readback_ok"] != false || got["verified"] != false || got["status"] == "done" {
			t.Fatalf("readback failure response=%v code=%d", got, w.Code)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
