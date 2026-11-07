package panel

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type realWorkflowRT struct {
	t                                                       *testing.T
	current, target, balance                                int64
	advance, claimed, locked, mpOnly, conflict, readFailure bool
	stream                                                  string
	chats, claims, reads, balances                          int
	stateAfterChat                                          string
	dir, op                                                 string
	claimMode                                               string
	stopAfter                                               int
	breakClaimJournal                                       bool
}
type contextFixtureBody struct {
	io.Reader
	ctx  context.Context
	fail bool
}

func (b *contextFixtureBody) Read(p []byte) (int, error) {
	if e := b.ctx.Err(); e != nil {
		return 0, e
	}
	n, e := b.Reader.Read(p)
	if e == io.EOF && b.fail {
		return n, errors.New("private-fixture-secret")
	}
	return n, e
}
func (b *contextFixtureBody) Close() error { return nil }
func (x *realWorkflowRT) response(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func (x *realWorkflowRT) reservation(kind string, want int) {
	if x.dir == "" {
		return
	}
	j, e := readWorkflowJournal(workflowJournalPath(x.dir, "u", x.op))
	if e != nil {
		x.t.Fatal(e)
	}
	if j.State != "pending" {
		x.t.Fatal("mutation before pending journal")
	}
	if kind == "chat" && j.Response.ChatCalls != want {
		x.t.Fatalf("chat reservation=%d want%d", j.Response.ChatCalls, want)
	}
	if kind == "claim" && j.Response.ClaimAttempts != want {
		x.t.Fatal("claim not durably reserved")
	}
}
func (x *realWorkflowRT) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Path {
	case "/v2/activity/growth/tasks":
		x.reads++
		mp := r.Header.Get("X-Client-Platform") == "miniprogram"
		rows := []any{}
		if !x.mpOnly || mp {
			cur := x.current
			if x.conflict && mp {
				cur++
			}
			status := "accepted"
			if x.claimed {
				status = "claimed"
			}
			state := ""
			target := x.target
			if x.chats > 0 {
				switch x.stateAfterChat {
				case "locked":
					x.locked = true
				case "expired":
					state = "expired"
				case "target":
					target++
				case "claimed":
					status = "claimed"
				}
			}
			rows = append(rows, map[string]any{"task_code": "chat_5", "progress": map[string]int64{"current": cur, "target": target}, "accept_status": status, "status": state, "locked": x.locked, "reward_credit": 100, "reward_energy": 5, "has_reward": true})
		}
		b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"tasks": rows}})
		return x.response(r, string(b)), nil
	case "/v2/chat/completions":
		x.reservation("chat", x.chats+1)
		x.chats++
		if x.advance && (x.stopAfter == 0 || x.chats <= x.stopAfter) {
			x.current++
		}
		var raw map[string]any
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &raw) != nil || raw["max_tokens"] != float64(16) {
			x.t.Fatal("missing token cap")
		}
		body := x.stream
		if body == "" {
			body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n"
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &contextFixtureBody{Reader: strings.NewReader(body), ctx: r.Context(), fail: x.readFailure}, Request: r}, nil
	case "/activity/growth/tasks/chat_5/claim":
		x.reservation("claim", 1)
		x.claims++
		if x.claimMode == "error" {
			return x.response(r, `{"code":9,"message":"private-fixture-secret"}`), nil
		}
		x.claimed = x.claimMode != "not_claimed"
		if x.claimMode != "no_balance" {
			x.balance += 100
		}
		if x.claimMode == "empty" {
			return x.response(r, `{"code":0,"data":{}}`), nil
		}
		return x.response(r, `{"code":0,"data":{"credit":100,"energy":5}}`), nil
	case "/v2/billing/meter/get-user-resource":
		x.balances++
		b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"Response": map[string]any{"Data": map[string]any{"Accounts": []any{map[string]any{"CapacitySize": 1000, "CapacityRemain": x.balance}}}}}})
		if x.breakClaimJournal {
			path := workflowJournalPath(x.dir, "u", x.op)
			if e := os.Remove(path); e != nil {
				x.t.Fatal(e)
			}
			if e := os.Mkdir(path, 0700); e != nil {
				x.t.Fatal(e)
			}
		}
		return x.response(r, string(b)), nil
	default:
		x.t.Fatalf("forbidden outbound %s %s", r.Method, r.URL.Path)
		return nil, errors.New("forbidden")
	}
}
func newRealWorkflowPanel(t *testing.T, x *realWorkflowRT) *Panel {
	t.Helper()
	old := workflowProgressGap
	workflowProgressGap = 0
	t.Cleanup(func() { workflowProgressGap = old })
	x.t = t
	x.target = 5
	x.balance = 500
	x.op = "operation_001"
	x.dir = t.TempDir()
	q := pool.New("")
	t.Cleanup(q.Close)
	q.Add(&auth.Auth{UID: "u", AccessToken: "fixture-token", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	c := upstream.New()
	c.HTTP = &http.Client{Transport: x}
	c.ChatHTTP = c.HTTP
	c.ChatBaseCN = "https://chat.fixture"
	c.BillingBaseCN = "https://billing.fixture"
	c.WebBaseCN = "https://web.fixture"
	return New(Config{Pool: q, Upstream: c, APIKey: "k", AuthDir: x.dir})
}
func workflowBody(op string) string {
	b, _ := json.Marshal(taskWorkflowRequest{TaskCodes: []string{"chat_5"}, Execute: true, OperationID: op, Model: "deepseek-v4-flash", MaxChatCalls: 5, MaxOutputTokens: 16, ClaimRewards: true})
	return string(b)
}
func callRealWorkflow(t *testing.T, p *Panel, body string) (int, taskWorkflowResponse) {
	t.Helper()
	r := httptest.NewRequest("POST", "/panel/api/accounts/u/tasks/workflow", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer k")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	var out taskWorkflowResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}
func TestRealWorkflowPlanAndValidationHaveNoMutation(t *testing.T) {
	x := &realWorkflowRT{}
	p := newRealWorkflowPanel(t, x)
	code, r := callRealWorkflow(t, p, `{}`)
	if code != 200 || r.Status != "planned" || !r.Tasks[0].CanExecute || x.chats+x.claims != 0 {
		t.Fatalf("plan %d %+v", code, r)
	}
	if files, _ := os.ReadDir(x.dir); len(files) != 0 {
		t.Fatal("plan created journal")
	}
	for _, body := range []string{`null`, `{"execute":null}`, `{"task_codes":["chat_5","chat_5"]}`, `{"execute":false,"execute":true}`, `{"other":1}`, `{"max_chat_calls":6}`, `{"max_output_tokens":null}`, `{"task_codes":[" chat_5"]}`, `{}{}`} {
		before := x.reads
		c, _ := callRealWorkflow(t, p, body)
		if c != 400 || x.reads != before {
			t.Fatalf("invalid request reached upstream %s code%d", body, c)
		}
	}
	r0 := httptest.NewRequest("POST", "/panel/api/accounts/u/tasks/workflow", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r0)
	if w.Code != 401 {
		t.Fatal("missing endpoint auth")
	}
}
func TestRealWorkflowProgressClaimAndReplay(t *testing.T) {
	x := &realWorkflowRT{advance: true}
	p := newRealWorkflowPanel(t, x)
	code, r := callRealWorkflow(t, p, workflowBody(x.op))
	if code != 200 || r.Status != "reward_confirmed" || !r.RewardReceived || !r.UpstreamClaimed || !r.ReceiptRewardReported || !r.BalanceVerified || r.ChatCalls != 5 || r.ClaimAttempts != 1 || r.BalanceDelta != 100 || r.ProgressAfter != 5 || x.chats != 5 || x.claims != 1 {
		t.Fatalf("result %d %+v chats%d claims%d", code, r, x.chats, x.claims)
	}
	before := x.reads + x.chats + x.claims + x.balances
	c, again := callRealWorkflow(t, p, workflowBody(x.op))
	if c != 200 || !again.RewardReceived || x.reads+x.chats+x.claims+x.balances != before {
		t.Fatal("replay repeated outbound")
	}
	c, _ = callRealWorkflow(t, p, strings.Replace(workflowBody(x.op), `"max_chat_calls":5`, `"max_chat_calls":4`, 1))
	if c != 409 {
		t.Fatal("digest mismatch allowed")
	}
}
func TestRealWorkflowStopsAfterUncountedOrFailedChat(t *testing.T) {
	for _, kind := range []string{"no_progress", "error", "read_error", "locked", "expired", "target", "claimed"} {
		t.Run(kind, func(t *testing.T) {
			x := &realWorkflowRT{advance: kind != "no_progress"}
			p := newRealWorkflowPanel(t, x)
			switch kind {
			case "error":
				x.stream = "data: {\"error\":{\"message\":\"private-fixture-secret\"}}\n\n"
			case "read_error":
				x.readFailure = true
			case "locked", "expired", "target", "claimed":
				x.stateAfterChat = kind
			}
			c, r := callRealWorkflow(t, p, workflowBody(x.op))
			if c != 200 || r.OK || r.RewardReceived || x.chats != 1 || x.claims != 0 {
				t.Fatalf("%s result%d %+v chats%d claim%d", kind, c, r, x.chats, x.claims)
			}
			if kind == "no_progress" && (r.Status != "unconfirmed" || x.reads != 6) {
				t.Fatalf("expected two polls %+v reads%d", r, x.reads)
			}
			if strings.Contains(string(mustWorkflowJSON(t, r)), "private-fixture-secret") {
				t.Fatal("error leaked")
			}
			before := x.reads
			c, _ = callRealWorkflow(t, p, workflowBody(x.op))
			if c != 200 || x.chats != 1 || x.reads != before {
				t.Fatal("failed terminal replay repeated activity")
			}
		})
	}
}
func mustWorkflowJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestRealWorkflowBlockedAndReadyDoNotChat(t *testing.T) {
	for _, kind := range []string{"mp_only", "conflict", "locked", "already_claimed", "ready"} {
		t.Run(kind, func(t *testing.T) {
			x := &realWorkflowRT{}
			p := newRealWorkflowPanel(t, x)
			switch kind {
			case "mp_only":
				x.mpOnly = true
			case "conflict":
				x.conflict = true
			case "locked":
				x.locked = true
			case "already_claimed":
				x.claimed = true
				x.current = 5
			case "ready":
				x.current = 5
			}
			_, r := callRealWorkflow(t, p, workflowBody(x.op))
			if x.chats != 0 {
				t.Fatal("blocked/ready task consumed chat")
			}
			if kind == "ready" {
				if x.claims != 1 || !r.RewardReceived {
					t.Fatalf("ready %+v", r)
				}
			} else if x.claims != 0 || r.RewardReceived {
				t.Fatalf("blocked task claimed %+v", r)
			}
		})
	}
}
func TestRealWorkflowPendingPersistenceAndCancelledContext(t *testing.T) {
	x := &realWorkflowRT{}
	p := newRealWorkflowPanel(t, x)
	dir := p.cfg.AuthDir
	p.cfg.AuthDir = dir + "/file"
	if e := os.WriteFile(p.cfg.AuthDir, []byte("x"), 0600); e != nil {
		t.Fatal(e)
	}
	c, _ := callRealWorkflow(t, p, workflowBody(x.op))
	if c != 409 || x.chats+x.claims+x.reads != 0 {
		t.Fatal("unavailable storage reached upstream")
	}
	p.cfg.AuthDir = dir
	path := workflowJournalPath(dir, "u", x.op)
	if e := os.MkdirAll(dir+"/task-workflows", 0700); e != nil {
		t.Fatal(e)
	}
	var req taskWorkflowRequest
	_ = json.Unmarshal([]byte(workflowBody(x.op)), &req)
	if e := writeWorkflowJournal(path, workflowJournal{Digest: workflowDigest(req), State: "pending"}); e != nil {
		t.Fatal(e)
	}
	c, _ = callRealWorkflow(t, p, workflowBody(x.op))
	if c != 409 || x.chats+x.claims+x.reads != 0 {
		t.Fatal("pending op repeated")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", "/panel/api/accounts/u/tasks/workflow", strings.NewReader(workflowBody("operation_002"))).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer k")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if x.chats+x.claims+x.reads != 0 {
		t.Fatal("cancelled request reached transport")
	}
}
func TestRealWorkflowClaimReservationFailureMakesNoClaim(t *testing.T) {
	x := &realWorkflowRT{current: 5, breakClaimJournal: true}
	p := newRealWorkflowPanel(t, x)
	c, r := callRealWorkflow(t, p, workflowBody(x.op))
	if c != 503 || r.Status != "journal_write_failed" || x.claims+x.chats != 0 {
		t.Fatalf("claim on failed reservation code%d %+v", c, r)
	}
}

func TestRealWorkflowBudgetAndPerStepGrowth(t *testing.T) {
	for _, kind := range []string{"budget", "stall_after_growth", "no_claim_opt_in"} {
		t.Run(kind, func(t *testing.T) {
			x := &realWorkflowRT{advance: true}
			p := newRealWorkflowPanel(t, x)
			body := workflowBody(x.op)
			wantChats, wantProgress, wantStatus := 2, int64(2), "budget_exhausted"
			switch kind {
			case "budget":
				body = strings.Replace(body, `"max_chat_calls":5`, `"max_chat_calls":2`, 1)
			case "stall_after_growth":
				x.stopAfter = 1
				wantProgress = 1
				wantStatus = "unconfirmed"
			case "no_claim_opt_in":
				body = strings.Replace(body, `"claim_rewards":true`, `"claim_rewards":false`, 1)
				wantChats = 5
				wantProgress = 5
				wantStatus = "ready_to_claim"
			}
			code, r := callRealWorkflow(t, p, body)
			if code != 200 || r.Status != wantStatus || x.chats != wantChats || x.claims != 0 || r.ProgressAfter != wantProgress || r.RewardReceived {
				t.Fatalf("%d %+v chats%d claims%d", code, r, x.chats, x.claims)
			}
		})
	}
}
func TestRealWorkflowUncertainClaimNeverReportsReward(t *testing.T) {
	for _, kind := range []string{"error", "empty", "not_claimed", "no_balance"} {
		t.Run(kind, func(t *testing.T) {
			x := &realWorkflowRT{current: 5, claimMode: kind}
			p := newRealWorkflowPanel(t, x)
			code, r := callRealWorkflow(t, p, workflowBody(x.op))
			if code != 200 || r.RewardReceived || r.OK || r.Status != "claim_outcome_uncertain" || x.claims != 1 || x.chats != 0 || x.balances != 2 {
				t.Fatalf("%d %+v claims%d balances%d", code, r, x.claims, x.balances)
			}
			before := x.reads + x.balances + x.claims
			callRealWorkflow(t, p, workflowBody(x.op))
			if x.reads+x.balances+x.claims != before {
				t.Fatal("uncertain claim repeated")
			}
		})
	}
}
