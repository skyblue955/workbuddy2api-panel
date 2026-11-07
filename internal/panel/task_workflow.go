package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type taskWorkflowRequest struct {
	TaskCodes       []string `json:"task_codes"`
	Execute         bool     `json:"execute"`
	OperationID     string   `json:"operation_id"`
	Model           string   `json:"model"`
	MaxChatCalls    int      `json:"max_chat_calls"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	ClaimRewards    bool     `json:"claim_rewards"`
}
type taskWorkflowTask struct {
	TaskCode   string `json:"task_code"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Current    int64  `json:"current"`
	Target     int64  `json:"target"`
	CanExecute bool   `json:"can_execute"`
	CanClaim   bool   `json:"can_claim"`
	Channel    string `json:"channel"`
}
type taskWorkflowResponse struct {
	OK                    bool               `json:"ok"`
	Support               string             `json:"support"`
	Execute               bool               `json:"execute"`
	OperationID           string             `json:"operation_id,omitempty"`
	Status                string             `json:"status"`
	ChatCalls             int                `json:"chat_calls"`
	ClaimAttempts         int                `json:"claim_attempts"`
	ProgressBefore        int64              `json:"progress_before"`
	ProgressAfter         int64              `json:"progress_after"`
	RewardReceived        bool               `json:"reward_received"`
	ReceiptRewardReported bool               `json:"receipt_reward_reported"`
	UpstreamClaimed       bool               `json:"upstream_claimed"`
	BalanceVerified       bool               `json:"balance_verified"`
	Credit                int64              `json:"credit"`
	Energy                int64              `json:"energy"`
	BalanceDelta          int64              `json:"balance_delta"`
	Tasks                 []taskWorkflowTask `json:"tasks"`
}
type workflowJournal struct {
	Digest   string               `json:"digest"`
	State    string               `json:"state"`
	Response taskWorkflowResponse `json:"response"`
}

var workflowOpRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,64}$`)

func decodeTaskWorkflow(r *http.Request) (taskWorkflowRequest, error) {
	if r.Body == nil {
		return taskWorkflowRequest{}, errors.New("invalid request")
	}
	q := taskWorkflowRequest{MaxOutputTokens: 16}
	b, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(b) > 8192 {
		return q, errors.New("invalid request")
	}
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return q, errors.New("object required")
	}
	raw = map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return q, errors.New("invalid request")
		}
		ks, ok := key.(string)
		if !ok {
			return q, errors.New("invalid request")
		}
		if _, exists := raw[ks]; exists {
			return q, errors.New("duplicate field")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return q, errors.New("invalid request")
		}
		raw[ks] = v
	}
	if _, err := dec.Token(); err != nil {
		return q, errors.New("invalid request")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return q, errors.New("trailing data")
	}
	allowed := map[string]bool{"task_codes": true, "execute": true, "operation_id": true, "model": true, "max_chat_calls": true, "max_output_tokens": true, "claim_rewards": true}
	for k := range raw {
		if !allowed[k] {
			return q, errors.New("unknown field")
		}
	}
	if v, ok := raw["task_codes"]; ok {
		if string(bytes.TrimSpace(v)) == "null" || json.Unmarshal(v, &q.TaskCodes) != nil {
			return q, errors.New("invalid task_codes")
		}
		for _, s := range q.TaskCodes {
			if strings.TrimSpace(s) == "" {
				return q, errors.New("invalid task_codes")
			}
		}
		if len(q.TaskCodes) > 10 {
			return q, errors.New("invalid task_codes")
		}
		seen := map[string]bool{}
		for _, s := range q.TaskCodes {
			if seen[s] {
				return q, errors.New("duplicate task_codes")
			}
			seen[s] = true
		}
	}
	if v, ok := raw["execute"]; ok {
		if string(bytes.TrimSpace(v)) == "null" {
			return q, errors.New("invalid execute")
		}
		if json.Unmarshal(v, &q.Execute) != nil {
			return q, errors.New("invalid execute")
		}
	}
	if v, ok := raw["operation_id"]; ok {
		if string(bytes.TrimSpace(v)) == "null" {
			return q, errors.New("invalid operation_id")
		}
		if json.Unmarshal(v, &q.OperationID) != nil {
			return q, errors.New("invalid operation_id")
		}
	}
	if v, ok := raw["model"]; ok {
		if string(bytes.TrimSpace(v)) == "null" {
			return q, errors.New("invalid model")
		}
		if json.Unmarshal(v, &q.Model) != nil {
			return q, errors.New("invalid model")
		}
	}
	if v, ok := raw["max_chat_calls"]; ok {
		if string(bytes.TrimSpace(v)) == "null" {
			return q, errors.New("invalid max_chat_calls")
		}
		if json.Unmarshal(v, &q.MaxChatCalls) != nil {
			return q, errors.New("invalid max_chat_calls")
		}
	}
	if v, ok := raw["max_output_tokens"]; ok {
		if string(bytes.TrimSpace(v)) == "null" {
			return q, errors.New("invalid max_output_tokens")
		}
		if json.Unmarshal(v, &q.MaxOutputTokens) != nil {
			return q, errors.New("invalid max_output_tokens")
		}
	}
	if v, ok := raw["claim_rewards"]; ok {
		if string(bytes.TrimSpace(v)) == "null" {
			return q, errors.New("invalid claim_rewards")
		}
		if json.Unmarshal(v, &q.ClaimRewards) != nil {
			return q, errors.New("invalid claim_rewards")
		}
	}
	if q.MaxChatCalls < 0 || q.MaxChatCalls > 5 || q.MaxOutputTokens < 1 || q.MaxOutputTokens > 64 {
		return q, errors.New("invalid caps")
	}
	if _, ok := raw["max_output_tokens"]; !ok {
		q.MaxOutputTokens = 16
	}
	seen := map[string]bool{}
	for _, code := range q.TaskCodes {
		if strings.TrimSpace(code) != code || code == "" || seen[code] {
			return q, errors.New("invalid task_codes")
		}
		seen[code] = true
	}
	if len(q.TaskCodes) > 10 {
		return q, errors.New("too many task codes")
	}
	if _, provided := raw["task_codes"]; provided && len(q.TaskCodes) == 0 {
		return q, errors.New("empty task codes")
	}
	return q, nil
}

var workflowProgressGap = time.Second

func workflowJournalPath(dir, uid, op string) string {
	h := sha256.Sum256([]byte(uid + "\x00" + op))
	return filepath.Join(dir, "task-workflows", hex.EncodeToString(h[:])+".json")
}
func workflowDigest(q taskWorkflowRequest) string {
	b, _ := json.Marshal(q)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func syncWorkflowDir(dir string) error {
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func writeWorkflowJournal(path string, j workflowJournal) error {
	b, e := json.Marshal(j)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return syncWorkflowDir(filepath.Dir(path))
}
func updateWorkflowJournal(path string, j workflowJournal) error {
	b, e := json.Marshal(j)
	if e != nil {
		return e
	}
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() {
		return errors.New("journal unavailable")
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".workflow-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	return syncWorkflowDir(filepath.Dir(path))
}
func readWorkflowJournal(path string) (workflowJournal, error) {
	var j workflowJournal
	st, e := os.Lstat(path)
	if e != nil {
		return j, e
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return j, errors.New("invalid journal")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return j, e
	}
	e = json.Unmarshal(b, &j)
	if e == nil && (j.Digest == "" || (j.State != "complete" && j.State != "pending")) {
		e = errors.New("invalid journal")
	}
	return j, e
}
func (p *Panel) accountTaskWorkflow(w http.ResponseWriter, r *http.Request) {
	req, e := decodeTaskWorkflow(r)
	if e != nil {
		writeErr(w, 400, "invalid workflow request")
		return
	}
	if req.Execute && (!workflowOpRE.MatchString(req.OperationID) || strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Model) != req.Model || len(req.Model) > 128 || req.MaxChatCalls < 1 || len(req.TaskCodes) != 1 || req.TaskCodes[0] != "chat_5") {
		writeErr(w, 400, "invalid execute request")
		return
	}
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	if a.IsEnterprise() || a.IsGlobal() || a.NeedsRefresh(0) || strings.TrimSpace(a.UID) == "" || strings.TrimSpace(a.AccessTokenValue()) == "" {
		writeErr(w, 422, "account unavailable")
		return
	}
	if !p.tryLockAccount(uid) {
		writeErr(w, 409, "account task operation is busy")
		return
	}
	defer p.unlockAccount(uid)
	if !req.Execute {
		p.workflowPlan(w, r, a, req)
		return
	}
	if strings.TrimSpace(p.cfg.AuthDir) == "" {
		writeErr(w, 409, "workflow persistence unavailable")
		return
	}
	dir := filepath.Join(p.cfg.AuthDir, "task-workflows")
	if e = os.MkdirAll(dir, 0700); e != nil {
		writeErr(w, 409, "workflow persistence unavailable")
		return
	}
	if e = syncWorkflowDir(p.cfg.AuthDir); e != nil {
		writeErr(w, 409, "workflow persistence unavailable")
		return
	}
	path := workflowJournalPath(p.cfg.AuthDir, uid, req.OperationID)
	digest := workflowDigest(req)
	j, e := readWorkflowJournal(path)
	if e == nil {
		if j.Digest != digest {
			writeErr(w, 409, "operation conflict")
			return
		}
		if j.State != "complete" {
			writeErr(w, 409, "operation pending")
			return
		}
		writeJSON(w, 200, j.Response)
		return
	} else if !os.IsNotExist(e) {
		writeErr(w, 409, "operation journal invalid")
		return
	}
	resp := taskWorkflowResponse{Support: "legacy_unverified", Execute: true, OperationID: req.OperationID, Status: "starting", Tasks: []taskWorkflowTask{}}
	j = workflowJournal{Digest: digest, State: "pending", Response: resp}
	if e = writeWorkflowJournal(path, j); e != nil {
		writeErr(w, 409, "workflow persistence unavailable")
		return
	}
	// Every network mutation is preceded by a durable reservation. A failure of
	// the final commit leaves the old pending reservation, never a replayable run.
	finish := func() {
		j.State = "complete"
		j.Response = resp
		if updateWorkflowJournal(path, j) != nil {
			resp.OK = false
			resp.Status = "journal_write_failed"
			writeJSON(w, 503, resp)
			return
		}
		writeJSON(w, 200, resp)
	}
	reserve := func() bool { j.Response = resp; return updateWorkflowJournal(path, j) == nil }
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	tasks, defaults, e := p.workflowTasks(ctx, a)
	if e != nil {
		resp.Status = "task_read_failed"
		finish()
		return
	}
	resp.Tasks = workflowTaskRows(tasks, req.TaskCodes, defaults)
	t := findWorkflowTask(tasks, "chat_5")
	if !defaults["chat_5"] || t == nil || t.Target <= 0 || t.Current < 0 || t.RewardBuddy || t.IsInactive() || isMPTaskCode(t.TaskCode) {
		resp.Status = "blocked"
		finish()
		return
	}
	resp.ProgressBefore = t.Current
	resp.ProgressAfter = t.Current
	if t.Claimed {
		resp.Status = "already_claimed"
		resp.UpstreamClaimed = true
		resp.OK = true
		finish()
		return
	}
	if t.AcceptStatus != "accepted" && t.AcceptStatus != "in_progress" && t.AcceptStatus != "completed" {
		resp.Status = "requires_enrollment"
		finish()
		return
	}
	target := t.Target
	for t.Current < target && resp.ChatCalls < req.MaxChatCalls {
		if ctx.Err() != nil {
			resp.Status = "cancelled"
			finish()
			return
		}
		previous := t.Current
		resp.ChatCalls++
		if !reserve() {
			resp.Status = "journal_write_failed"
			writeJSON(w, 503, resp)
			return
		}
		cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
		rc, status, ce := p.cfg.Upstream.WorkflowChat(cctx, a, req.Model, req.MaxOutputTokens)
		if ce == nil {
			if status < 200 || status >= 300 || rc == nil {
				ce = errors.New("invalid chat response")
			} else {
				ce = consumeModelChatCompletion(rc)
			}
		}
		if rc != nil {
			rc.Close()
		}
		ccancel()
		if ce != nil {
			resp.Status = "chat_failed"
			finish()
			return
		}
		progressed := false
		for poll := 0; poll < 2; poll++ {
			if poll > 0 {
				timer := time.NewTimer(workflowProgressGap)
				select {
				case <-ctx.Done():
					timer.Stop()
					resp.Status = "cancelled"
					finish()
					return
				case <-timer.C:
				}
			}
			tasks, defaults, e = p.workflowTasks(ctx, a)
			if e != nil {
				resp.Status = "task_read_failed"
				finish()
				return
			}
			resp.Tasks = workflowTaskRows(tasks, req.TaskCodes, defaults)
			nt := findWorkflowTask(tasks, "chat_5")
			if !defaults["chat_5"] || nt == nil || nt.Target != target || nt.IsInactive() || nt.RewardBuddy || nt.Claimed || nt.Current < previous || (nt.AcceptStatus != "accepted" && nt.AcceptStatus != "in_progress" && nt.AcceptStatus != "completed") {
				resp.Status = "task_state_changed"
				if nt != nil {
					resp.ProgressAfter = nt.Current
					resp.UpstreamClaimed = nt.Claimed
				}
				finish()
				return
			}
			t = nt
			resp.ProgressAfter = t.Current
			if t.Current > previous {
				progressed = true
				break
			}
		}
		if !progressed {
			resp.Status = "unconfirmed"
			finish()
			return
		}
	}
	if t.Current < target {
		resp.Status = "budget_exhausted"
		finish()
		return
	}
	if !t.CanClaimReward() {
		resp.Status = "qualification_unconfirmed"
		finish()
		return
	}
	if !req.ClaimRewards {
		resp.Status = "ready_to_claim"
		resp.OK = true
		finish()
		return
	}
	bctx, bcancel := context.WithTimeout(ctx, 10*time.Second)
	before, be := p.cfg.Upstream.WorkflowBalance(bctx, a)
	bcancel()
	if be != nil {
		resp.Status = "balance_unavailable"
		finish()
		return
	}
	if ctx.Err() != nil {
		resp.Status = "cancelled"
		finish()
		return
	}
	resp.ClaimAttempts = 1
	if !reserve() {
		resp.Status = "journal_write_failed"
		writeJSON(w, 503, resp)
		return
	}
	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	receipt, claimErr := p.cfg.Upstream.WorkflowClaim(cctx, a, "chat_5")
	ccancel()
	if claimErr == nil {
		resp.Credit = receipt.Credit
		resp.Energy = receipt.Energy
		resp.ReceiptRewardReported = receipt.HasReward() && !receipt.AlreadyClaimed
	}
	// Both post-write facts are observed even after an uncertain claim response.
	tasks, postDefaults, readErr := p.workflowTasks(ctx, a)
	if readErr == nil {
		defaults = postDefaults
		resp.Tasks = workflowTaskRows(tasks, req.TaskCodes, defaults)
		if done := findWorkflowTask(tasks, "chat_5"); done != nil && defaults["chat_5"] {
			resp.UpstreamClaimed = done.Claimed
			resp.ProgressAfter = done.Current
		}
	}
	bctx, bcancel = context.WithTimeout(ctx, 10*time.Second)
	after, afterErr := p.cfg.Upstream.WorkflowBalance(bctx, a)
	bcancel()
	if afterErr == nil {
		resp.BalanceVerified = true
		resp.BalanceDelta = after - before
	}
	resp.RewardReceived = claimErr == nil && readErr == nil && resp.ReceiptRewardReported && resp.UpstreamClaimed && resp.BalanceVerified && resp.BalanceDelta == receipt.Credit
	if resp.RewardReceived {
		resp.Status = "reward_confirmed"
		resp.OK = true
	} else {
		resp.Status = "claim_outcome_uncertain"
	}
	finish()
}
func (p *Panel) workflowTasks(ctx context.Context, a *auth.Auth) ([]upstream.Task, map[string]bool, error) {
	read := func(mp bool) ([]upstream.Task, error) {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return p.cfg.Upstream.WorkflowTasks(c, a, mp)
	}
	d, e := read(false)
	if e != nil {
		return nil, nil, e
	}
	m, e := read(true)
	if e != nil {
		return nil, nil, e
	}
	seen := map[string]upstream.Task{}
	defaults := map[string]bool{}
	for _, t := range d {
		if _, exists := seen[t.TaskCode]; exists {
			return nil, nil, errors.New("ambiguous tasks")
		}
		seen[t.TaskCode] = t
		defaults[t.TaskCode] = true
	}
	for _, t := range m {
		if old, exists := seen[t.TaskCode]; exists {
			if old.Current != t.Current || old.Target != t.Target || old.Claimed != t.Claimed || old.Locked != t.Locked || old.Status != t.Status || old.AcceptStatus != t.AcceptStatus || old.Claimable != t.Claimable || old.RewardBuddy != t.RewardBuddy {
				return nil, nil, errors.New("qualification conflict")
			}
		} else {
			d = append(d, t)
			seen[t.TaskCode] = t
		}
	}
	return d, defaults, nil
}
func findWorkflowTask(ts []upstream.Task, code string) *upstream.Task {
	for i := range ts {
		if ts[i].TaskCode == code {
			return &ts[i]
		}
	}
	return nil
}
func workflowTaskRows(ts []upstream.Task, codes []string, defaults map[string]bool) []taskWorkflowTask {
	want := map[string]bool{}
	for _, code := range codes {
		want[code] = true
	}
	out := []taskWorkflowTask{}
	for _, t := range ts {
		if len(codes) > 0 && !want[t.TaskCode] {
			continue
		}
		row := taskWorkflowTask{TaskCode: t.TaskCode, Current: t.Current, Target: t.Target, CanClaim: defaults[t.TaskCode] && t.CanClaimReward() && !isMPTaskCode(t.TaskCode) && !t.RewardBuddy}
		row.Channel = "mp"
		if defaults[t.TaskCode] {
			row.Channel = "default"
		}
		switch {
		case t.Claimed:
			row.Status = "already_claimed"
			row.Reason = "already_claimed"
		case t.IsInactive():
			row.Status = "blocked"
			row.Reason = "inactive_or_locked"
		case t.RewardBuddy:
			row.Status = "blocked"
			row.Reason = "buddy_side_effect"
		case t.Target <= 0 || t.Current < 0:
			row.Status = "blocked"
			row.Reason = "unknown_progress"
		case !defaults[t.TaskCode] || isMPTaskCode(t.TaskCode):
			row.Status = "blocked"
			row.Reason = "channel_not_verified"
		case t.TaskCode != "chat_5":
			row.Status = "blocked"
			row.Reason = "no_verified_executor"
		case t.AcceptStatus != "accepted" && t.AcceptStatus != "in_progress" && t.AcceptStatus != "completed":
			row.Status = "blocked"
			row.Reason = "requires_enrollment"
		case t.CanClaimReward():
			row.Status = "ready_to_claim"
			row.CanExecute = true
		case t.Current >= t.Target:
			row.Status = "blocked"
			row.Reason = "qualification_unconfirmed"
		default:
			row.Status = "requires_paid_opt_in"
			row.CanExecute = true
		}
		out = append(out, row)
		delete(want, t.TaskCode)
	}
	for code := range want {
		out = append(out, taskWorkflowTask{TaskCode: code, Status: "blocked", Reason: "missing_task"})
	}
	return out
}
func (p *Panel) workflowPlan(w http.ResponseWriter, r *http.Request, a *auth.Auth, q taskWorkflowRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ts, defaults, e := p.workflowTasks(ctx, a)
	if e != nil {
		writeJSON(w, 200, taskWorkflowResponse{Support: "legacy_unverified", Status: "task_read_failed", Tasks: []taskWorkflowTask{}})
		return
	}
	writeJSON(w, 200, taskWorkflowResponse{OK: true, Support: "legacy_unverified", Status: "planned", Tasks: workflowTaskRows(ts, q.TaskCodes, defaults)})
}
