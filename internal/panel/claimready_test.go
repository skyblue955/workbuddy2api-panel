package panel

import (
	"encoding/json"
	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestParseClaimReadyRequestStrict(t *testing.T) {
	got, err := parseClaimReadyRequest(strings.NewReader(`{"task_codes":["Library_read"],"execute":false}`))
	if err != nil || len(got.TaskCodes) != 1 || got.Execute {
		t.Fatalf("parse valid request: %+v,%v", got, err)
	}
	for _, in := range []string{`{"task_codes":["Library_read"],"extra":true}`, `{"task_codes":["Library_read"]}{"task_codes":["chat_5"]}`} {
		if _, e := parseClaimReadyRequest(strings.NewReader(in)); e == nil {
			t.Errorf("accepted %q", in)
		}
	}
}
func TestClaimReadyAllowlist(t *testing.T) {
	if autoActionFor("Library_read") == nil || isMPTaskCode("Library_read") {
		t.Fatal("ordinary")
	}
	if autoActionFor("Sequential_Tasks_1") == nil || !isMPTaskCode("Sequential_Tasks_1") {
		t.Fatal("mp")
	}
}

type claimFixture struct {
	mu        sync.Mutex
	lists     [][]upstream.Task
	claims    int
	listCalls []string
}

func (f *claimFixture) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/v2/activity/growth/tasks" {
		f.listCalls = append(f.listCalls, r.Method)
		var ts []upstream.Task
		if len(f.lists) > 0 {
			ts = f.lists[0]
			f.lists = f.lists[1:]
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"tasks": ts}})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/claim") {
		f.claims++
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"credit": 10, "energy": 1}})
		return
	}
	http.NotFound(w, r)
}
func task(c string, cur, target int64) upstream.Task {
	return upstream.Task{TaskCode: c, Current: cur, Target: target, Claimable: cur >= target && target > 0}
}
func doClaim(t *testing.T, p *Panel, key, body string) (int, map[string]any) {
	t.Helper()
	q := httptest.NewRequest("POST", "/panel/api/accounts/u1/tasks/claim_ready", strings.NewReader(body))
	q.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, q)
	var out map[string]any
	json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}
func TestAccountTasksClaimReadyDryRunAndExecute(t *testing.T) {
	f := &claimFixture{lists: [][]upstream.Task{{task("Library_read", 1, 1), task("chat_5", 1, 5), task("Sequential_Tasks_1", 1, 1), {TaskCode: "locked", Current: 2, Target: 1, Claimable: true, Locked: true}, {TaskCode: "zero", Current: 0, Claimable: true}}, {task("Library_read", 1, 1)}, {func() upstream.Task { x := task("Library_read", 1, 1); x.AcceptStatus = "claimed"; return x }()}}}
	s := httptest.NewServer(http.HandlerFunc(f.handler))
	defer s.Close()
	ps := pool.New("")
	ps.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	p := New(Config{Pool: ps, APIKey: "k", Upstream: &upstream.Client{HTTP: s.Client(), ChatBaseCN: s.URL, BillingBaseCN: s.URL, WebBaseCN: s.URL}})
	code, out := doClaim(t, p, "k", `{"task_codes":["Library_read","chat_5","Sequential_Tasks_1","locked","zero"],"execute":false}`)
	if code != 200 || out["selected"] != float64(1) {
		t.Fatalf("dry %d %v", code, out)
	}
	if f.claims != 0 {
		t.Fatal("dry claimed")
	}
	code, out = doClaim(t, p, "k", `{"task_codes":["Library_read"],"execute":true}`)
	if code != 200 || out["ok"] != true || out["complete"] != true || out["claims"] != float64(1) {
		t.Fatalf("exec %d %v", code, out)
	}
	if f.claims != 1 || len(f.listCalls) != 3 {
		t.Fatalf("calls %v/%d", f.listCalls, f.claims)
	}
}
func TestClaimReadyValidationAuthUnsupportedBusyAndNoop(t *testing.T) {
	f := &claimFixture{lists: [][]upstream.Task{{func() upstream.Task { x := task("x", 1, 1); x.AcceptStatus = "claimed"; return x }()}}}
	s := httptest.NewServer(http.HandlerFunc(f.handler))
	defer s.Close()
	ps := pool.New("")
	ps.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	p := New(Config{Pool: ps, APIKey: "k", Upstream: &upstream.Client{HTTP: s.Client(), ChatBaseCN: s.URL, BillingBaseCN: s.URL, WebBaseCN: s.URL}})
	for _, b := range []string{"null", `{"task_codes":[]}`, `{"task_codes":["x","x"]}`, `{"task_codes":[" "]}`, `{"task_codes":["x"],"extra":1}`, `{"task_codes":["x"]}{}`} {
		if c, _ := doClaim(t, p, "k", b); c != 400 {
			t.Errorf("%q=>%d", b, c)
		}
	}
	if c, _ := doClaim(t, p, "k", "{}"); c != 400 {
		t.Errorf("missing=%d", c)
	}
	q := httptest.NewRequest("POST", "/panel/api/accounts/u1/tasks/claim_ready", strings.NewReader(`{"task_codes":["x"]}`))
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, q)
	if rr.Code != 401 {
		t.Fatalf("unauth=%d", rr.Code)
	}
	for _, a := range []*auth.Auth{{UID: "u1", AccessToken: "at", Domain: "api.workbuddy.ai"}, {UID: "u1", AccessToken: "at", EnterpriseID: "ent"}} {
		q := pool.New("")
		q.Add(a)
		pp := New(Config{Pool: q, APIKey: "k", Upstream: p.cfg.Upstream})
		if c, _ := doClaim(t, pp, "k", `{"task_codes":["x"]}`); c != 422 {
			t.Errorf("unsupported=%d", c)
		}
	}
	if !p.tryLockAccount("u1") {
		t.Fatal("lock")
	}
	if c, _ := doClaim(t, p, "k", `{"task_codes":["x"]}`); c != 409 {
		t.Errorf("busy=%d", c)
	}
	p.unlockAccount("u1")
}
func TestClaimReadyFailureAndConfirmationAreUncertain(t *testing.T) {
	f := &claimFixture{lists: [][]upstream.Task{{task("x", 1, 1)}, {func() upstream.Task { x := task("x", 1, 1); x.AcceptStatus = "claimed"; return x }()}}}
	s := httptest.NewServer(http.HandlerFunc(f.handler))
	defer s.Close()
	ps := pool.New("")
	ps.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	p := New(Config{Pool: ps, APIKey: "k", Upstream: &upstream.Client{HTTP: s.Client(), ChatBaseCN: s.URL, BillingBaseCN: s.URL, WebBaseCN: s.URL}})
	c, out := doClaim(t, p, "k", `{"task_codes":["x"],"execute":true}`)
	if c != 200 || out["ok"] != true {
		t.Fatalf("confirm=%d %v", c, out)
	}
	f2 := &claimFixture{lists: [][]upstream.Task{{task("x", 1, 1)}, {func() upstream.Task { x := task("x", 1, 1); x.AcceptStatus = "claimed"; return x }()}}}
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") {
			w.WriteHeader(502)
			return
		}
		f2.handler(w, r)
	}))
	defer s2.Close()
	q := pool.New("")
	q.Add(&auth.Auth{UID: "u1", AccessToken: "at"})
	pp := New(Config{Pool: q, APIKey: "k", Upstream: &upstream.Client{HTTP: s2.Client(), ChatBaseCN: s2.URL, BillingBaseCN: s2.URL, WebBaseCN: s2.URL}})
	_, out = doClaim(t, pp, "k", `{"task_codes":["x"],"execute":true}`)
	if out["ok"] != false || f2.claims != 0 {
		t.Fatalf("failure %v/%d", out, f2.claims)
	}
}
