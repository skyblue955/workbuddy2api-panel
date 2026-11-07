package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

type workflowFixtureRT struct {
	status      int
	body        string
	location    string
	n           atomic.Int32
	last        atomic.Pointer[http.Request]
	requestBody atomic.Pointer[string]
}

func (f *workflowFixtureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	f.n.Add(1)
	f.last.Store(r.Clone(r.Context()))
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		f.requestBody.Store(&s)
	}
	h := make(http.Header)
	if f.location != "" {
		h.Set("Location", f.location)
	}
	return &http.Response{StatusCode: f.status, Header: h, Body: io.NopCloser(strings.NewReader(f.body)), Request: r}, nil
}
func workflowFixtureClient(rt http.RoundTripper) (*Client, *auth.Auth, *http.Client) {
	hc := &http.Client{Transport: rt, Timeout: 3 * time.Second}
	c := &Client{HTTP: hc, ChatHTTP: hc, ChatBaseCN: "https://chat.fixture", BillingBaseCN: "https://billing.fixture", WebBaseCN: "https://web.fixture", UserAgent: "fixture-agent"}
	a := &auth.Auth{AccessToken: "fixture-token", UID: "fixture-uid", Domain: "www.codebuddy.cn", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	return c, a, hc
}
func TestWorkflowTasksFixtureSchemaMPProgressAndTransportPolicy(t *testing.T) {
	rt := &workflowFixtureRT{status: 200, body: `{"code":0,"data":{"tasks":[{"task_code":"chat","task_type":"single","reward_credit":3,"progress":{"current":2,"target":2},"current":0,"target":99},{"task_code":"flat","task_type":"daily","current":1,"target":3}]}}`}
	c, a, hc := workflowFixtureClient(rt)
	sentinel := errors.New("sentinel redirect")
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return sentinel }
	jar := &workflowFixtureJar{}
	hc.Jar = jar
	got, err := c.WorkflowTasks(context.Background(), a, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Current != 2 || got[0].Target != 2 || got[0].TaskType != "single" || !got[0].Claimable {
		t.Fatalf("tasks=%+v", got)
	}
	req := rt.last.Load()
	if req == nil || req.Method != "GET" || req.URL.Path != tasksListPath || req.Header.Get("X-Client-Platform") != mpPlatform || req.Header.Get("Authorization") != "Bearer fixture-token" || req.Header.Get("X-User-Id") != "fixture-uid" {
		t.Fatalf("request=%v headers=%v", req, req.Header)
	}
	if rt.n.Load() != 1 || hc.Transport != rt || hc.Jar != http.CookieJar(jar) || hc.Timeout != 3*time.Second || hc.CheckRedirect(nil, nil) != sentinel {
		t.Fatal("HTTP client mutated or retried")
	}
	for _, body := range []string{`{"code":0,"data":{"tasks":[{"task_code":"x"},{"task_code":"x"}]}}`, `{"code":null,"data":{"tasks":[]}}`, `{"code":"0","data":{"tasks":[]}}`, `{"code":0,"data":{"tasks":null}}`, `{"code":0,"data":{"tasks":[]}} trailing`, `{"code":0,"code":0,"data":{"tasks":[]}}`, `{"code":0,"data":{"tasks":[{"progress":{"current":1}}]}}`, `{"code":0,"data":{"tasks":[{"task_code":"x"}]}}` + strings.Repeat("x", workflowMaxBody)} {

		rt.body = body
		if _, err := c.WorkflowTasks(context.Background(), a, false); err == nil {
			t.Errorf("accepted malformed task fixture %q", body)
		}
	}
	expired := &auth.Auth{AccessToken: a.AccessToken, UID: a.UID, Domain: a.Domain, ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	rt.body = `{"code":0,"data":{"tasks":[]}}`
	if _, err := c.WorkflowTasks(context.Background(), expired, false); err == nil {
		t.Error("expired auth accepted")
	}
	for name, bad := range map[string]*auth.Auth{"missing-token": {UID: a.UID, Domain: a.Domain, ExpiresAt: a.ExpiresAt}, "missing-uid": {AccessToken: a.AccessToken, Domain: a.Domain, ExpiresAt: a.ExpiresAt}, "unknown-expiry": {AccessToken: a.AccessToken, UID: a.UID, Domain: a.Domain}} {
		t.Run(name, func(t *testing.T) {
			rt.n.Store(0)
			if _, err := c.WorkflowTasks(context.Background(), bad, false); err == nil {
				t.Errorf("invalid auth accepted: %s", name)
			}
			if rt.n.Load() != 0 {
				t.Errorf("invalid auth reached transport: %s", name)
			}
		})
	}
}

type workflowFixtureJar struct{}

func (*workflowFixtureJar) Cookies(*url.URL) []*http.Cookie     { return nil }
func (*workflowFixtureJar) SetCookies(*url.URL, []*http.Cookie) {}

func TestWorkflowBalanceFixtureCycleCapacityClampAndOverflow(t *testing.T) {
	rt := &workflowFixtureRT{status: 200, body: `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CycleCapacitySize":10,"CycleCapacityRemain":99,"CycleCapacityUsed":0},{"CapacitySize":8,"CapacityRemain":3,"CapacityUsed":0}]}}}}`}
	c, a, _ := workflowFixtureClient(rt)
	got, err := c.WorkflowBalance(context.Background(), a)
	if err != nil || got != 13 {
		t.Fatalf("balance=%d err=%v", got, err)
	}
	for _, body := range []string{`{"code":0,"data":{"Response":{"Data":{"Accounts":[]}}}}`, `{"code":0,"data":{"Response":{"Data":{"Accounts":null}}}}`, `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CycleCapacitySize":1,"CycleCapacityRemain":null}]}}}}`, `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacityRemain":-1}]}}}}`, `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CycleCapacitySize":1,"CycleCapacityUsed":2}]}}}}`} {
		rt.body = body
		if _, err := c.WorkflowBalance(context.Background(), a); err == nil {
			t.Errorf("accepted invalid balance %q", body)
		}
	}
	rt.body = `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacitySize":9223372036854775807,"CapacityRemain":9223372036854775807},{"CapacitySize":1,"CapacityRemain":1}]}}}}`
	if _, err := c.WorkflowBalance(context.Background(), a); err == nil {
		t.Error("overflow accepted")
	}
}

func TestWorkflowClaimFixtureShapeNoRetryNoRedirectAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int64
		already    bool
	}{{"zero", `{"code":0,"data":{"credit":0,"energy":0}}`, 0, false}, {"positive", `{"code":0,"data":{"credit":7,"energy":2}}`, 7, false}, {"already", `{"code":0,"data":{"already_claimed":true}}`, 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &workflowFixtureRT{status: 200, body: tc.body}
			c, a, _ := workflowFixtureClient(rt)
			r, e := c.WorkflowClaim(context.Background(), a, "fixture-task")
			if e != nil || r.Credit != tc.want || r.AlreadyClaimed != tc.already || rt.n.Load() != 1 {
				t.Fatalf("receipt=%+v err=%v calls=%d", r, e, rt.n.Load())
			}
			q := rt.last.Load()
			if q.URL.Path != "/activity/growth/tasks/fixture-task/claim" || q.Header.Get("x-client-platform") != "web" || q.Header.Get("Authorization") != "Bearer fixture-token" || (func() bool { b := rt.requestBody.Load(); return b != nil && *b != "" })() {
				t.Fatal("claim shape mismatch")
			}
		})
	}
	for _, body := range []string{`{"code":0,"data":{"credit":1},"data":{}}`, `{"code":0,"data":[]}`, `{"code":0,"data":{"credit":null}}`, `{"code":0,"data":{"credit":1}} trailing`} {
		rt := &workflowFixtureRT{status: 200, body: body}
		c, a, _ := workflowFixtureClient(rt)
		if _, err := c.WorkflowClaim(context.Background(), a, "fixture-task"); err == nil {
			t.Errorf("accepted malformed claim fixture %q", body)
		}
	}
	for _, status := range []int{307, 401, 429} {
		rt := &workflowFixtureRT{status: status, location: "https://evil.invalid", body: `{"code":0,"data":{"credit":1}}`}
		c, a, _ := workflowFixtureClient(rt)
		if _, err := c.WorkflowClaim(context.Background(), a, "fixture-task"); err == nil {
			t.Errorf("status %d accepted", status)
		}
		if rt.n.Load() != 1 {
			t.Errorf("status %d calls=%d", status, rt.n.Load())
		}
	}
	rt := &workflowFixtureRT{status: 200, body: `{"code":0,"data":{"credit":1}}`}
	c, a, _ := workflowFixtureClient(rt)
	for _, code := range []string{"../x", "a/b", "a?b", "a#b", " a", "a%2f"} {
		if _, err := c.WorkflowClaim(context.Background(), a, code); err == nil {
			t.Errorf("dangerous code accepted %q", code)
		}
		if rt.n.Load() != 0 {
			t.Fatal("dangerous code reached transport")
		}
	}
}

func TestWorkflowChatFixturePreparedBodyHeadersAndSSE(t *testing.T) {
	rt := &workflowFixtureRT{status: 200, body: "data: hello\\n\\n data: [DONE]\\n\\n"}
	c, a, hc := workflowFixtureClient(rt)
	body, status, err := c.WorkflowChat(context.Background(), a, "fixture-model", 16)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	raw, _ := io.ReadAll(body)
	body.Close()
	if !strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("sse=%q", raw)
	}
	req := rt.last.Load()
	s := rt.requestBody.Load()
	if req.Header.Get("Authorization") != "Bearer fixture-token" || req.Header.Get("Accept") != "application/json, text/event-stream" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers=%v", req.Header)
	}
	for _, want := range []string{`"stream":true`, `"max_tokens":16`, `"include_usage":true`, `"enable_thinking":false`, `Reply only OK.`} {
		if s == nil || !strings.Contains(*s, want) {
			t.Fatalf("prepared body missing %q: %v", want, s)
		}
	}
	if rt.n.Load() != 1 || hc.CheckRedirect != nil {
		t.Fatal("chat retries/mutates client")
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.WorkflowChat(cancelCtx, a, "m", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled chat err=%v", err)
	}
	for _, n := range []int{0, 65} {
		rt.body = `data: x`
		if _, _, err := c.WorkflowChat(context.Background(), a, "m", n); err == nil {
			t.Errorf("max_tokens=%d accepted", n)
		}
	}
	rt.status = 502
	rt.body = `secret token=fixture-token uid=fixture-uid https://private.invalid`
	if _, _, err := c.WorkflowChat(context.Background(), a, "m", 1); err == nil || strings.Contains(err.Error(), "fixture-token") || strings.Contains(err.Error(), "fixture-uid") || strings.Contains(err.Error(), "private.invalid") {
		t.Fatalf("leaky error: %v", err)
	}
}

func TestWorkflowUnknownProgressCannotTriggerPaidExecution(t *testing.T) {
	for _, row := range []string{`"current":null,"target":5`, `"target":5`, `"current":5,"target":5,"progress":null`, `"progress":{"current":null,"target":5}`} {
		rt := &workflowFixtureRT{status: 200, body: `{"code":0,"data":{"tasks":[{"task_code":"chat_5",` + row + `}]}}`}
		c, a, _ := workflowFixtureClient(rt)
		tasks, err := c.WorkflowTasks(context.Background(), a, false)
		if err != nil || len(tasks) != 1 || tasks[0].Target != 0 || tasks[0].Claimable {
			t.Fatalf("unknown progress accepted %s: %+v %v", row, tasks, err)
		}
	}
}
