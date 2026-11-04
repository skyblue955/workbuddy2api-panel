package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type claimRT struct {
	mu          sync.Mutex
	lists       [][]upstream.Task
	claimStatus int
	negative    bool
	calls       []string
}

func (x *claimRT) RoundTrip(r *http.Request) (*http.Response, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.calls = append(x.calls, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/v2/activity/growth/tasks" {
		if len(x.lists) == 0 {
			return nil, errors.New("unexpected list")
		}
		ts := x.lists[0]
		x.lists = x.lists[1:]
		b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"tasks": ts}})
		return resp(200, b), nil
	}
	if strings.HasSuffix(r.URL.Path, "/claim") {
		if x.claimStatus != 0 {
			return resp(x.claimStatus, []byte(`{"code":1,"msg":"failed"}`)), nil
		}
		if x.negative {
			return resp(200, []byte(`{"code":0,"data":{"credit":-1,"energy":1}}`)), nil
		}
		return resp(200, []byte(`{"code":0,"data":{"credit":10,"energy":1}}`)), nil
	}
	return nil, errors.New("unexpected endpoint")
}
func resp(code int, b []byte) *http.Response {
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}
}
func claimSafetyPanel(rt *claimRT, a *auth.Auth) *Panel {
	q := pool.New("")
	q.Add(a)
	cl := &upstream.Client{HTTP: &http.Client{Transport: rt}, ChatBaseCN: "http://fixture", BillingBaseCN: "http://fixture", WebBaseCN: "http://fixture"}
	return New(Config{Pool: q, APIKey: "k", Upstream: cl})
}
func safetyCall(p *Panel, method, path, body string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer k")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}
func st(c string, cur, target int64) upstream.Task {
	return upstream.Task{TaskCode: c, Current: cur, Target: target, Claimable: cur >= target && target > 0}
}
func TestClaimReadySafetyParserAndZeroOutbound(t *testing.T) {
	cases := []string{`null`, `[]`, `{"task_codes":null}`, `{"task_codes":["x"],"execute":null}`, `{"task_codes":["x"],"execute":1}`, `{"task_codes":["x"]}{}`, `{"task_codes":["x"],"task_codes":["y"]}`, `{"task_codes":["x"],"execute":false,"execute":true}`, `{"task_codes":["x"],"extra":1}`, `{"task_codes":["x"]}` + strings.Repeat(" ", 8193)}
	for _, body := range cases {
		rt := &claimRT{}
		p := claimSafetyPanel(rt, &auth.Auth{UID: "u1", AccessToken: "fixture"})
		code, _ := safetyCall(p, "POST", "/panel/api/accounts/u1/tasks/claim_ready", body)
		if code != 400 || len(rt.calls) != 0 {
			t.Fatalf("invalid body length%d code%d calls%v", len(body), code, rt.calls)
		}
	}
}
func TestClaimReadySafetyFiltersAndSingleClaim(t *testing.T) {
	rt := &claimRT{lists: [][]upstream.Task{{st("new_code", 1, 1), st("other", 1, 1), {TaskCode: "buddy", Current: 1, Target: 1, Claimable: true, RewardBuddy: true}, {TaskCode: "expired", Current: 1, Target: 1, Claimable: true, Status: "expired"}, {TaskCode: "Sequential_Tasks_1", Current: 1, Target: 1, Claimable: true}, {TaskCode: "zero", Current: 0, Target: 0, Claimable: true}, {TaskCode: "dup", Current: 1, Target: 1, Claimable: true}, {TaskCode: "dup", Current: 1, Target: 1, Claimable: true}}, {func() upstream.Task { x := st("new_code", 1, 1); x.AcceptStatus = "claimed"; return x }()}}}
	p := claimSafetyPanel(rt, &auth.Auth{UID: "u1", AccessToken: "a"})
	c, m := safetyCall(p, "POST", "/panel/api/accounts/u1/tasks/claim_ready", `{"task_codes":["new_code","buddy","expired","Sequential_Tasks_1","zero","dup","missing"],"execute":true}`)
	if c != 200 || m["claims"] != float64(1) || m["ok"] != true {
		t.Fatalf("%d %v", c, m)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.calls) != 3 || rt.calls[1] != "POST /activity/growth/tasks/new_code/claim" {
		t.Fatalf("calls=%v", rt.calls)
	}
}
func TestClaimReadySafetyReceiptAndReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lists  [][]upstream.Task
		status int
		wantOK bool
	}{{"duplicate confirmation", [][]upstream.Task{{st("x", 1, 1)}, {st("x", 1, 1), st("x", 1, 1)}}, 0, false}, {"unclaimed confirmation", [][]upstream.Task{{st("x", 1, 1)}, {st("x", 1, 1)}}, 0, false}, {"negative receipt", [][]upstream.Task{{st("x", 1, 1)}, {func() upstream.Task { x := st("x", 1, 1); x.AcceptStatus = "claimed"; return x }()}}, 0, false}, {"post-read failure", [][]upstream.Task{{st("x", 1, 1)}}, 0, false}} {
		rt := &claimRT{lists: tc.lists, claimStatus: tc.status, negative: tc.name == "negative receipt"}
		if tc.name == "post-read failure" {
			rt.lists = [][]upstream.Task{{st("x", 1, 1)}}
		}
		p := claimSafetyPanel(rt, &auth.Auth{UID: "u1", AccessToken: "a"})
		_, m := safetyCall(p, "POST", "/panel/api/accounts/u1/tasks/claim_ready", `{"task_codes":["x"],"execute":true}`)
		if m["ok"] == true && !tc.wantOK {
			t.Errorf("%s unexpectedly ok: %v", tc.name, m)
		}
		if m["ok"] == false && tc.wantOK {
			t.Errorf("%s unexpectedly failed: %v", tc.name, m)
		}
	}
}
func TestClaimReadySafetySourceHintAndSharedLock(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"accessToken": enterpriseSwitchToken(), "uid": "u1", "domain": "copilot.tencent.com"})
	a, e := auth.Parse(raw)
	if e != nil || a.IsEnterprise() || !a.HasEnterpriseSwitchSource() {
		t.Fatalf("source hint fixture: %v %v", a, e)
	}
	rt := &claimRT{lists: [][]upstream.Task{{st("new_code", 1, 1)}}}
	p := claimSafetyPanel(rt, a)
	p.tryLockAccount("u1")
	for _, path := range []string{"/panel/api/accounts/u1/tasks/accept", "/panel/api/accounts/u1/tasks/accept_all", "/panel/api/accounts/u1/tasks/claim"} {
		c, _ := safetyCall(p, "POST", path, `{"task_codes":["x"],"task_code":"x"}`)
		if c != 409 {
			t.Errorf("%s=%d", path, c)
		}
	}
	rt.mu.Lock()
	if len(rt.calls) != 0 {
		t.Errorf("locked outbound=%v", rt.calls)
	}
	rt.mu.Unlock()
	p.unlockAccount("u1")
	code, m := safetyCall(p, "POST", "/panel/api/accounts/u1/tasks/claim_ready", `{"task_codes":["new_code"]}`)
	if code != 200 || m["selected"] != float64(1) || m["claims"] != float64(0) || len(rt.calls) != 1 {
		t.Fatalf("sourcehint dryrun code%d result%v calls%v", code, m, rt.calls)
	}
}

func TestClaimReadySafetyNoopAndClaimFailureBudget(t *testing.T) {
	for _, ready := range []bool{false, true} {
		var lists [][]upstream.Task
		if ready {
			lists = [][]upstream.Task{{st("x", 1, 1)}, {st("x", 1, 1)}}
		} else {
			lists = [][]upstream.Task{{st("x", 0, 1)}}
		}
		rt := &claimRT{lists: lists, claimStatus: 502}
		p := claimSafetyPanel(rt, &auth.Auth{UID: "u1", AccessToken: "fixture"})
		code, m := safetyCall(p, "POST", "/panel/api/accounts/u1/tasks/claim_ready", `{"task_codes":["x"],"execute":true}`)
		if code != 200 {
			t.Fatalf("http%d", code)
		}
		writes := 0
		for _, c := range rt.calls {
			if strings.HasPrefix(c, "POST ") {
				writes++
			}
		}
		if !ready {
			if m["no_op"] != true || writes != 0 || len(rt.calls) != 1 {
				t.Fatalf("notready mutated %v calls%v", m, rt.calls)
			}
		} else {
			if m["ok"] != false || m["complete"] != false || m["claims"] != float64(1) || writes != 1 || len(rt.calls) != 3 {
				t.Fatalf("failed claim/retry %v calls%v", m, rt.calls)
			}
		}
	}
}
func TestClaimReadySafetyRewardConfirmationFields(t *testing.T) {
	for _, kind := range []string{"confirmed", "notclaimed", "duplicate", "readerror", "negative"} {
		done := st("x", 1, 1)
		done.AcceptStatus = "claimed"
		lists := [][]upstream.Task{{st("x", 1, 1)}}
		switch kind {
		case "confirmed", "negative":
			lists = append(lists, []upstream.Task{done})
		case "notclaimed":
			lists = append(lists, []upstream.Task{st("x", 1, 1)})
		case "duplicate":
			lists = append(lists, []upstream.Task{done, done})
		}
		rt := &claimRT{lists: lists, negative: kind == "negative"}
		p := claimSafetyPanel(rt, &auth.Auth{UID: "u1", AccessToken: "fixture"})
		_, m := safetyCall(p, "POST", "/panel/api/accounts/u1/tasks/claim_ready", `{"task_codes":["x"],"execute":true}`)
		r := m["results"].([]any)[0].(map[string]any)
		if kind == "confirmed" {
			if r["reward_received"] != true || r["upstream_claimed"] != true || r["credit"] != float64(10) || m["complete"] != true {
				t.Fatalf("confirmed%v", m)
			}
		} else {
			if r["reward_received"] != false || m["complete"] != false {
				t.Fatalf("false reward confirmation %s %v", kind, m)
			}
		}
		if r["receipt_reward_reported"] != (kind != "negative") {
			t.Fatalf("receiptflag%s %v", kind, m)
		}
		if len(rt.calls) != 3 {
			t.Fatalf("unexpectedcalls %v", rt.calls)
		}
	}
}
