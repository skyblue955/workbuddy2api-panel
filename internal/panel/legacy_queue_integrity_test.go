package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestGrowthPendingRejectsInactiveAndAlreadyEligible(t *testing.T) {
	cases := []struct {
		name string
		task upstream.Task
		want bool
	}{
		{"locked", upstream.Task{TaskCode: "chat_5", Locked: true, Target: 1, Current: 0}, false},
		{"expired", upstream.Task{TaskCode: "chat_5", Status: "expired", Target: 1, Current: 0}, false},
		{"claimed", upstream.Task{TaskCode: "chat_5", Claimed: true, Target: 1, Current: 1}, false},
		{"eligible", upstream.Task{TaskCode: "chat_5", Target: 5, Current: 5, Claimable: true}, true},
		{"pending", upstream.Task{TaskCode: "chat_5", Target: 5, Current: 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := growthPending(tc.task); got != tc.want {
				t.Fatalf("growthPending(%+v)=%v, want %v", tc.task, got, tc.want)
			}
		})
	}
}

func TestGrowthPendingUnknownActionIsNotQueued(t *testing.T) {
	if growthPending(upstream.Task{TaskCode: "unknown", Target: 1, Current: 0}) {
		t.Fatal("unknown task action must not enter queue")
	}
}

func TestLegacyQueueReadyClaimDoesNotRunAction(t *testing.T) {
	for _, tc := range []struct {
		name, receipt string
		wantErr       bool
		wantMsg       string
	}{
		{"positive", `{"credit":100,"energy":5}`, false, "已自动领奖"},
		{"already", `{"already_claimed":true}`, false, "奖励此前已领取"},
		{"zero", `{"credit":0,"energy":0,"already_claimed":false}`, true, ""},
		{"malformed", `null`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var claims, runs atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/activity/growth/tasks" {
					_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("chat_5", 1, 1, "accepted", nil)})))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/claim") {
					claims.Add(1)
					_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":` + tc.receipt + `}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
			}))
			defer srv.Close()
			p := &Panel{cfg: Config{Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, WebBaseCN: srv.URL}}}
			withStubbedAutoAction(t, "chat_5", func(*Panel, *auth.Auth) (string, error) { runs.Add(1); return "stub", nil }, func() {
				msg, err := p.runGrowthQueued(&auth.Auth{UID: "fixture", AccessToken: "fixture"}, "chat_5")
				if (err != nil) != tc.wantErr || runs.Load() != 0 || claims.Load() != 1 {
					t.Fatalf("msg=%q err=%v runs=%d claims=%d", msg, err, runs.Load(), claims.Load())
				}
				if !tc.wantErr && !strings.Contains(msg, tc.wantMsg) {
					t.Fatalf("msg=%q missing %q", msg, tc.wantMsg)
				}
			})
		})
	}
}

func TestLegacyQueueInactiveAndUnreadyDoNotClaim(t *testing.T) {
	oldAttempts := claimPollAttempts
	claimPollAttempts = 1
	t.Cleanup(func() { claimPollAttempts = oldAttempts })
	for _, tc := range []struct {
		name  string
		extra map[string]any
		cur   int
	}{
		{"locked", map[string]any{"locked": true}, 1},
		{"expired", map[string]any{"status": "expired"}, 1},
		{"unready", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var claims, runs atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/activity/growth/tasks" {
					_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("chat_5", 1, int64(tc.cur), "accepted", tc.extra)})))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/claim") {
					claims.Add(1)
				}
				_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
			}))
			defer srv.Close()
			p := &Panel{cfg: Config{Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, WebBaseCN: srv.URL}}}
			withStubbedAutoAction(t, "chat_5", func(*Panel, *auth.Auth) (string, error) { runs.Add(1); return "stub", nil }, func() {
				_, err := p.runGrowthQueued(&auth.Auth{UID: "fixture", AccessToken: "fixture"}, "chat_5")
				wantRuns := int32(0)
				if tc.name == "unready" {
					wantRuns = 1
				}
				if runs.Load() != wantRuns || claims.Load() != 0 || err == nil {
					t.Fatalf("err=%v runs=%d claims=%d", err, runs.Load(), claims.Load())
				}
			})
		})
	}
}

func TestLegacyQueueReadbackFailureDoesNotConfirm(t *testing.T) {
	oldAttempts := claimPollAttempts
	claimPollAttempts = 1
	t.Cleanup(func() { claimPollAttempts = oldAttempts })
	var reads, runs, claims atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/activity/growth/tasks" {
			if reads.Add(1) == 1 {
				_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{sweepTask("chat_5", 1, 0, "accepted", nil)})))
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		claims.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	p := &Panel{cfg: Config{Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, WebBaseCN: srv.URL}}}
	withStubbedAutoAction(t, "chat_5", func(*Panel, *auth.Auth) (string, error) { runs.Add(1); return "stub", nil }, func() {
		_, err := p.runGrowthQueued(&auth.Auth{UID: "fixture", AccessToken: "fixture"}, "chat_5")
		if err == nil || runs.Load() != 1 || claims.Load() != 0 {
			t.Fatalf("err=%v runs=%d claims=%d", err, runs.Load(), claims.Load())
		}
	})
}

func TestLegacyQueueAcceptSkipsEmptyDuplicateAndInactive(t *testing.T) {
	var accepted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/activity/growth/tasks" {
			_, _ = w.Write([]byte(sweepTasksBody([]map[string]any{
				sweepTask("", 1, 0, "not_accepted", nil),
				sweepTask("dup", 1, 0, "not_accepted", nil),
				sweepTask("dup", 1, 0, "not_accepted", nil),
				sweepTask("expired", 1, 0, "not_accepted", map[string]any{"status": "expired"}),
				sweepTask("valid", 1, 0, "not_accepted", nil),
			})))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/accept") {
			var body struct {
				TaskCodes []string `json:"task_codes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			accepted = append(accepted, body.TaskCodes...)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"OK","data":{}}`))
	}))
	defer srv.Close()
	p := &Panel{cfg: Config{Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, WebBaseCN: srv.URL}}}
	if got := p.acceptPendingTasks(&auth.Auth{UID: "fixture", AccessToken: "fixture"}); got != 1 || len(accepted) != 1 || accepted[0] != "valid" {
		t.Fatalf("accepted=%d codes=%v", got, accepted)
	}
}
