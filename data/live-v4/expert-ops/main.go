package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var codes = []string{"expert_5", "Expert_team_use_3"}

type gate struct {
	base          http.RoundTripper
	mode, code    string
	reads, writes int
	statuses      []int
}

func (g *gate) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, errors.New("https required")
	}
	switch {
	case r.Method == "GET" && r.URL.Host == "copilot.tencent.com" && r.URL.Path == "/v2/activity/growth/tasks":
		if g.reads >= 2 {
			return nil, errors.New("read cap")
		}
		g.reads++
	case g.mode == "accept" && r.Method == "POST" && r.URL.Host == "copilot.tencent.com" && r.URL.Path == "/v2/activity/growth/tasks/accept":
		if g.writes != 0 {
			return nil, errors.New("no retry")
		}
		b, e := io.ReadAll(io.LimitReader(r.Body, 1024))
		r.Body.Close()
		var v struct {
			Codes []string `json:"task_codes"`
		}
		if e != nil || json.Unmarshal(b, &v) != nil || len(v.Codes) == 0 || len(v.Codes) > 2 {
			return nil, errors.New("unapproved accept")
		}
		seen := map[string]bool{}
		for _, c := range v.Codes {
			if (c != codes[0] && c != codes[1]) || seen[c] {
				return nil, errors.New("unapproved code")
			}
			seen[c] = true
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		g.writes++
	case g.mode == "claim" && r.Method == "POST" && r.URL.Host == "www.workbuddy.cn" && r.URL.Path == "/activity/growth/tasks/"+g.code+"/claim":
		if g.writes != 0 {
			return nil, errors.New("no retry")
		}
		g.writes++
	default:
		return nil, errors.New("operation not approved")
	}
	resp, e := g.base.RoundTrip(r)
	if resp != nil {
		g.statuses = append(g.statuses, resp.StatusCode)
	}
	return resp, e
}
func markerAt(dir, name string) error {
	f, e := os.OpenFile(filepath.Join(dir, name+".started.private"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	return f.Close()
}
func marker(name string) {
	if e := markerAt("data/live-v4/expert-ops", name); e != nil {
		panic("oneoff marker already exists")
	}
}
func selected(ts []upstream.Task, code string) *upstream.Task {
	var out *upstream.Task
	for i := range ts {
		if ts[i].TaskCode == code {
			if out != nil {
				return nil
			}
			out = &ts[i]
		}
	}
	return out
}

type rewardClient interface {
	ListTasks(*auth.Auth) ([]upstream.Task, error)
	UserResource(*auth.Auth) (int64, int64, error)
	ClaimRewardReceipt(*auth.Auth, string) (upstream.ClaimReceipt, error)
}
type claimSummary struct {
	Receipt             upstream.ClaimReceipt
	ClaimErr            error
	PreValid, PostValid bool
	Delta               int64
	PreTasks, PostTasks []upstream.Task
}

// claimOne is the deliberately single-shot claim protocol.  It performs one
// fresh task read and one resource read before the marker/POST, then one task
// and resource read after it.  Resource values are never returned, only their
// validity and delta are reported by callers.
func claimOne(c rewardClient, a *auth.Auth, code, markerDir string) (claimSummary, error) {
	var s claimSummary
	ts, e := c.ListTasks(a)
	if e != nil {
		return s, fmt.Errorf("pre task read: %w", e)
	}
	s.PreTasks = ts
	t := selected(ts, code)
	if t == nil || !t.CanClaimReward() || t.RewardBuddy {
		return s, fmt.Errorf("task not qualified")
	}
	pre, _, e := c.UserResource(a)
	if e != nil {
		// Resource lookup is a gate, not a retryable claim failure.  Normalize
		// failures so callers cannot accidentally route it through claim retry.
		return s, &upstream.Error{Kind: upstream.ErrClient, Msg: "pre resource gate failed"}
	}
	s.PreValid = true
	if e = markerAt(markerDir, "claim-"+code); e != nil {
		return s, fmt.Errorf("claim marker: %w", e)
	}
	s.Receipt, s.ClaimErr = c.ClaimRewardReceipt(a, code)
	post, postTotal, e2 := c.UserResource(a)
	_ = postTotal
	if e2 == nil {
		s.PostValid = true
		s.Delta = post - pre
	}
	s.PostTasks, e = c.ListTasks(a)
	if e != nil && s.ClaimErr == nil {
		s.ClaimErr = fmt.Errorf("post task read: %w", e)
	}
	return s, s.ClaimErr
}
func main() {
	log.SetOutput(io.Discard)
	if len(os.Args) < 2 {
		fmt.Println("DRY_RUN no call")
		return
	}
	mode := os.Args[1]
	code := ""
	if mode != "read" && mode != "accept" && mode != "claim" {
		panic("mode")
	}
	if mode == "claim" {
		if len(os.Args) != 4 || (os.Args[3] != "-official-ui-ready-confirmed" && os.Args[3] != "-upstream-ready-confirmed") {
			panic("explicit UI/upstream readiness required")
		}
		code = os.Args[2]
		if code != codes[0] && code != codes[1] {
			panic("code")
		}
	}
	raw, e := os.ReadFile("data/local-dev/config.json")
	if e != nil {
		panic("config")
	}
	var cfg struct {
		AuthDir string `json:"auth_dir"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		panic("config")
	}
	files, _ := filepath.Glob(filepath.Join(cfg.AuthDir, "*.json"))
	if len(files) != 1 {
		panic("one account")
	}
	raw, e = os.ReadFile(files[0])
	if e != nil {
		panic("credential")
	}
	a, e := auth.Parse(raw)
	if e != nil || a.IsGlobal() || a.IsEnterprise() || a.ExpiresAt <= time.Now().Unix() {
		panic("valid domestic personal account required")
	}
	ledgerRaw, e := os.ReadFile("data/live-v4/gui-expert/ledger.private.json")
	if e != nil {
		panic("ledger")
	}
	var ledger struct {
		Account string `json:"account_sha256"`
	}
	json.Unmarshal(ledgerRaw, &ledger)
	h := sha256.Sum256([]byte(a.UID))
	if hex.EncodeToString(h[:]) != ledger.Account {
		panic("account mismatch")
	}
	c := upstream.New()
	g := &gate{base: c.HTTP.Transport, mode: mode, code: code}
	if g.base == nil {
		g.base = http.DefaultTransport
	}
	c.HTTP.Transport = g
	c.HTTP.Timeout = 20 * time.Second
	c.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	report := map[string]any{"mode": mode, "chat_calls": 0, "event_reports": 0, "automatic_retries": 0}
	var ts []upstream.Task
	if mode != "claim" {
		ts, e = c.ListTasks(a)
		if e != nil {
			panic("task read failed")
		}
	}
	if mode == "accept" {
		pending := []string{}
		for _, code := range codes {
			t := selected(ts, code)
			if t == nil || t.IsInactive() || t.Claimed || t.RewardBuddy {
				panic("task not eligible for approved registration")
			}
			if t.AcceptStatus != "accepted" && t.AcceptStatus != "completed" {
				pending = append(pending, code)
			}
		}
		marker("accept-two")
		if len(pending) > 0 {
			e = c.AcceptTasks(a, pending)
		}
		report["accept_response_ok"] = e == nil
		var re error
		ts, re = c.ListTasks(a)
		if re != nil {
			panic("postread failed")
		}
	}
	if mode == "claim" {
		s, se := claimOne(c, a, code, "data/live-v4/expert-ops")
		e = se
		ts = s.PostTasks
		report["receipt_ok"] = s.ClaimErr == nil
		report["reward_reported"] = s.Receipt.HasReward()
		report["already_claimed"] = s.Receipt.AlreadyClaimed
		report["pre_resource_valid"] = s.PreValid
		report["post_resource_valid"] = s.PostValid
		report["resource_delta"] = s.Delta
		report["postread_ok"] = len(s.PostTasks) > 0
		report["claim_http_status"] = 0
		report["claim_business_code"] = 0
		if ue, ok := s.ClaimErr.(*upstream.Error); ok {
			report["claim_http_status"] = ue.Status
		}
	}
	rows := []map[string]any{}
	for _, code := range codes {
		t := selected(ts, code)
		r := map[string]any{"task_code": code, "unique_present": t != nil}
		if t != nil {
			r["current"] = t.Current
			r["target"] = t.Target
			r["accept_status"] = t.AcceptStatus
			r["inactive"] = t.IsInactive()
			r["claimable"] = t.CanClaimReward()
			r["claimed"] = t.Claimed
		}
		rows = append(rows, r)
	}
	report["tasks"] = rows
	report["reads"] = g.reads
	report["writes"] = g.writes
	report["statuses"] = g.statuses
	report["operation_ok"] = e == nil
	b, _ := json.MarshalIndent(report, "", "  ")
	p := "data/live-v4/expert-ops/" + mode + "-" + code + "-" + time.Now().Format("150405") + ".private.json"
	os.WriteFile(p, b, 0600)
	fmt.Println(string(b))
	if e != nil {
		os.Exit(1)
	}
}
