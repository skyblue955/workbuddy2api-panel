package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type codeEntryFixtureTransport func(*http.Request) (*http.Response, error)

func (f codeEntryFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func codeEntryResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: &http.Request{}}
}

func newCodeEntryFixtureClient(t *testing.T, experts string) (*upstream.Client, *int) {
	t.Helper()
	var reports, chats int
	c := &upstream.Client{ChatBaseCN: "https://fixture.invalid"}
	c.HTTP = &http.Client{Transport: codeEntryFixtureTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/portal/operation-platform/market/expert/list":
			return codeEntryResponse(200, "application/json", `{"code":0,"data":`+experts+`}`), nil
		case "/v2/chat/completions":
			chats++
			return codeEntryResponse(200, "text/event-stream", "data: {\"id\":\"cmb-0123456789abcdef0123456789abcdef\"}\n\n"), nil
		case "/v2/report":
			reports++
			return codeEntryResponse(200, "application/json", `{"code":0,"data":{}}`), nil
		default:
			return codeEntryResponse(404, "application/json", `{}`), nil
		}
	})}
	return c, &reports
}

func TestRunExpertBatchUsesOriginalCodeEntryChain(t *testing.T) {
	c, reports := newCodeEntryFixtureClient(t, `{"experts":[{"expert_id":"agent-fixture-1","expert_type":"agent","display_name_zh":"测试专家"}]}`)
	p := New(Config{Upstream: c})
	a := &auth.Auth{UID: "uid-fixture", AccessToken: "token-fixture"}
	message, err := runExpertBatch(p, a, "agent", 1)
	if err != nil {
		t.Fatalf("runExpertBatch() error = %v", err)
	}
	if !strings.Contains(message, "1 位真实专家") {
		t.Fatalf("runExpertBatch() message = %q", message)
	}
	if *reports != 2 {
		t.Fatalf("report calls = %d, want summon and actual-use reports", *reports)
	}
	t.Logf("original code entry passed fixture chain: reports=%d", *reports)
}

// This test records the pre-fix behavior: a target of five with only one
// listed expert returns nil error and a success-shaped message. It is kept as
// an explicit regression witness so a later fix can change the assertion to
// require an unconfirmed/partial result without involving GUI automation.
func TestRunExpertBatchPartialEvidence(t *testing.T) {
	c, _ := newCodeEntryFixtureClient(t, `{"experts":[{"expert_id":"agent-fixture-1","expert_type":"agent","display_name_zh":"测试专家"}]}`)
	p := New(Config{Upstream: c})
	a := &auth.Auth{UID: "uid-fixture", AccessToken: "token-fixture"}
	message, err := runExpertBatch(p, a, "agent", 5)
	if err != nil {
		t.Fatalf("runExpertBatch() error = %v", err)
	}
	if !strings.Contains(message, "1 位真实专家") {
		t.Fatalf("partial evidence message = %q", message)
	}
	t.Logf("KNOWN PARTIAL RESULT: target=5 listed=1 returned=%q; no completion/claim is asserted", message)
}

// newChat5RoutePanel serves the real /panel/api/.../tasks/auto handler and the
// real runChat5 action. The fixture advances progress only when the readback
// list is requested; HTTP 200 from /v2/report alone never makes a task done.
func newChat5RoutePanel(t *testing.T, ready, reportFails bool, noopClaim bool) (*Panel, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var lists, reports, claims atomic.Int32
	transport := codeEntryFixtureTransport(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/v2/activity/growth/tasks":
			call := lists.Add(1)
			current := int64(1)
			status := "accepted"
			if ready && call > 2 {
				current = 5
			}
			if !ready && call > 1 {
				current = 1
			}
			if ready && claims.Load() > 0 && !noopClaim {
				// A separate task-list readback confirms claimed status.
				status = "claimed"
			}
			return codeEntryResponse(200, "application/json", sweepTasksBody([]map[string]any{sweepTask("chat_5", 5, current, status, nil)})), nil
		case r.URL.Path == "/v2/billing/meter/get-user-resource":
			remain := int64(500)
			if !noopClaim {
				remain += int64(claims.Load()) * 100
			}
			body := fmt.Sprintf(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacitySize":1000,"CapacityRemain":%d}]}}}}`, remain)
			return codeEntryResponse(200, "application/json", body), nil
		case r.URL.Path == "/v2/report":
			reports.Add(1)
			if reportFails {
				return codeEntryResponse(http.StatusBadGateway, "application/json", `{}`), nil
			}
			return codeEntryResponse(200, "application/json", `{"code":0,"msg":"OK","data":{}}`), nil
		case strings.HasSuffix(r.URL.Path, "/claim"):
			claims.Add(1)
			if noopClaim {
				return codeEntryResponse(200, "application/json", `{"code":0,"msg":"OK","data":{"already_claimed":false,"credit":0,"energy":0}}`), nil
			}
			return codeEntryResponse(200, "application/json", `{"code":0,"msg":"OK","data":{"already_claimed":false,"credit":100,"energy":5}}`), nil
		default:
			return codeEntryResponse(200, "application/json", `{"code":0,"msg":"OK","data":{}}`), nil
		}
	})
	accounts := pool.New("")
	accounts.Add(&auth.Auth{UID: "u1", AccessToken: "fixture"})
	p := New(Config{Pool: accounts, Upstream: &upstream.Client{HTTP: &http.Client{Transport: transport}, ChatBaseCN: "https://fixture.invalid", BillingBaseCN: "https://fixture.invalid", WebBaseCN: "https://fixture.invalid"}})
	return p, &lists, &claims
}

func invokeChat5Route(t *testing.T, p *Panel) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/u1/tasks/auto", strings.NewReader(`{"task_code":"chat_5"}`))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	var out map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode route response: %v; body=%s", err, w.Body.String())
	}
	return w.Code, out
}

func TestCodeEntryPanelAutoReadbackClaimAndIdempotence(t *testing.T) {
	oldGap, oldAttempts, oldPollGap := reportGap, claimPollAttempts, claimPollGap
	reportGap, claimPollAttempts, claimPollGap = 0, 1, 0
	t.Cleanup(func() { reportGap, claimPollAttempts, claimPollGap = oldGap, oldAttempts, oldPollGap })
	p, lists, claims := newChat5RoutePanel(t, true, false, false)
	before := codeEntryFixtureBalance(t, p)
	code, first := invokeChat5Route(t, p)
	if code != http.StatusOK || first["verified"] != true || first["claimed"] != true || first["credit"] != float64(100) {
		t.Fatalf("ready route response code=%d body=%v", code, first)
	}
	if claims.Load() != 1 || lists.Load() < 2 {
		t.Fatalf("ready route calls lists=%d claims=%d, want readback and one claim", lists.Load(), claims.Load())
	}
	after := codeEntryFixtureBalance(t, p)
	if after-before != 100 {
		t.Fatalf("balance delta=%d want100", after-before)
	}
	code, second := invokeChat5Route(t, p)
	if code != http.StatusOK || second["skipped"] != true || claims.Load() != 1 {
		t.Fatalf("claimed rerun code=%d body=%v claims=%d", code, second, claims.Load())
	}
	if codeEntryFixtureBalance(t, p) != after {
		t.Fatal("repeat claim changed balance")
	}
	t.Log("FIXTURE progress=5/5 claimed=true receipt=100/5 balance_delta=100 repeat_delta=0")
}

func TestCodeEntryPanelAutoPartialAndNoopNeverInventReward(t *testing.T) {
	oldGap, oldAttempts, oldPollGap := reportGap, claimPollAttempts, claimPollGap
	reportGap, claimPollAttempts, claimPollGap = 0, 1, 0
	t.Cleanup(func() { reportGap, claimPollAttempts, claimPollGap = oldGap, oldAttempts, oldPollGap })
	p, _, claims := newChat5RoutePanel(t, false, false, false)
	before := codeEntryFixtureBalance(t, p)
	code, out := invokeChat5Route(t, p)
	if code != http.StatusOK || out["verified"] != false || out["claimable"] == true || claims.Load() != 0 {
		t.Fatalf("partial route code=%d body=%v claims=%d", code, out, claims.Load())
	}

	if codeEntryFixtureBalance(t, p) != before {
		t.Fatal("partial changed balance")
	}
	t.Log("FIXTURE progress=1/5 verified=false claim_requests=0 balance_delta=0")
	p, _, claims = newChat5RoutePanel(t, true, false, true)
	before = codeEntryFixtureBalance(t, p)
	code, out = invokeChat5Route(t, p)
	if code != http.StatusOK || out["verified"] != true || out["claimed"] == true || out["no_reward"] != true || claims.Load() != 1 {
		t.Fatalf("noop claim route code=%d body=%v claims=%d", code, out, claims.Load())
	}
	if codeEntryFixtureBalance(t, p) != before {
		t.Fatal("noop changed balance")
	}
	t.Log("FIXTURE noop_receipt=0/0 claimed=false no_reward=true balance_delta=0")
}

func TestCodeEntryPanelAutoActionFailureIsNotCompletion(t *testing.T) {
	oldGap, oldAttempts, oldPollGap := reportGap, claimPollAttempts, claimPollGap
	reportGap, claimPollAttempts, claimPollGap = 0, 1, 0
	t.Cleanup(func() { reportGap, claimPollAttempts, claimPollGap = oldGap, oldAttempts, oldPollGap })
	p, _, claims := newChat5RoutePanel(t, false, true, false)
	code, out := invokeChat5Route(t, p)
	if code != http.StatusOK || out["verified"] == true || out["claimed"] == true || claims.Load() != 0 {
		t.Fatalf("failed action route code=%d body=%v claims=%d", code, out, claims.Load())
	}
}

func codeEntryFixtureBalance(t *testing.T, p *Panel) int64 {
	t.Helper()
	n, _, e := p.cfg.Upstream.UserResource(p.cfg.Pool.AuthByUID("u1"))
	if e != nil {
		t.Fatal(e)
	}
	return n
}
