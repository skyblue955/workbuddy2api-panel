package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type claimReadyRequest struct {
	TaskCodes []string `json:"task_codes"`
	Execute   bool     `json:"execute"`
}

type claimReadyResult struct {
	TaskCode              string `json:"task_code"`
	Status                string `json:"status"`
	Reason                string `json:"reason,omitempty"`
	RewardReceived        bool   `json:"reward_received"`
	ReceiptRewardReported bool   `json:"receipt_reward_reported"`
	UpstreamClaimed       bool   `json:"upstream_claimed"`
	Credit                int64  `json:"credit,omitempty"`
	Energy                int64  `json:"energy,omitempty"`
}

func parseClaimReadyRequest(r io.Reader) (claimReadyRequest, error) {
	var req claimReadyRequest
	b, err := io.ReadAll(io.LimitReader(r, 8*1024+1))
	if err != nil || len(b) > 8*1024 {
		return req, errors.New("request too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return req, errors.New("invalid shape")
	}
	raw := map[string]json.RawMessage{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return req, errors.New("invalid field")
		}
		key, ok := token.(string)
		if !ok || (key != "task_codes" && key != "execute") {
			return req, errors.New("unknown field")
		}
		if _, duplicate := raw[key]; duplicate {
			return req, errors.New("duplicate field")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return req, errors.New("invalid field")
		}
		raw[key] = value
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return req, errors.New("invalid shape")
	}
	v, ok := raw["task_codes"]
	if !ok || string(v) == "null" || json.Unmarshal(v, &req.TaskCodes) != nil {
		return req, errors.New("task_codes")
	}
	if v, ok = raw["execute"]; ok {
		if string(v) == "null" || json.Unmarshal(v, &req.Execute) != nil {
			return req, errors.New("execute")
		}
	}
	var extra any
	if err := dec.Decode(&extra); err == nil || !errors.Is(err, io.EOF) {
		return req, io.ErrUnexpectedEOF
	}
	return req, nil
}

// accountTasksClaimReady claims only explicitly requested, already-ready tasks.
// It deliberately uses the default task list and never performs task actions.
func (p *Panel) accountTasksClaimReady(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.cfg.Pool.AuthByUID(uid)
	if a == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	req, err := parseClaimReadyRequest(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.TaskCodes) < 1 || len(req.TaskCodes) > 10 {
		writeErr(w, http.StatusBadRequest, "task_codes must contain 1..10 codes")
		return
	}
	seen := make(map[string]bool, len(req.TaskCodes))
	for _, code := range req.TaskCodes {
		if strings.TrimSpace(code) == "" || seen[code] {
			writeErr(w, http.StatusBadRequest, "task_codes contains invalid code")
			return
		}
		seen[code] = true
	}
	if !p.tryLockAccount(uid) {
		writeErr(w, http.StatusConflict, "account task operation is busy")
		return
	}
	defer p.unlockAccount(uid)

	results := make([]claimReadyResult, 0, len(req.TaskCodes))
	if a.IsGlobal() || a.IsEnterprise() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "support": "legacy_unverified", "error": "unsupported account"})
		return
	}
	tasks, err := p.cfg.Upstream.ListTasks(a)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list tasks failed")
		return
	}
	byCode := make(map[string][]upstream.Task)
	for _, t := range tasks {
		byCode[t.TaskCode] = append(byCode[t.TaskCode], t)
	}
	eligible := make(map[string]bool)
	for _, code := range req.TaskCodes {
		entries := byCode[code]
		if len(entries) != 1 {
			reason := "missing_task"
			if len(entries) > 1 {
				reason = "ambiguous_task"
			}
			results = append(results, claimReadyResult{TaskCode: code, Status: "skipped", Reason: reason})
			continue
		}
		t := entries[0]
		status := strings.ToLower(strings.TrimSpace(t.Status))
		reason := ""
		switch {
		case isMPTaskCode(code):
			reason = "unsupported_channel"
		case t.Claimed:
			reason = "already_claimed"
		case t.Locked:
			reason = "locked"
		case t.RewardBuddy:
			reason = "buddy_side_effect_not_authorized"
		case status == "expired" || status == "ended" || status == "disabled" || status == "cancelled":
			reason = "inactive_task"
		case t.Target <= 0:
			reason = "progress_unknown"
		case t.Current < t.Target || !t.Claimable:
			reason = "not_ready"
		}
		if reason != "" {
			results = append(results, claimReadyResult{TaskCode: code, Status: "skipped", Reason: reason})
			continue
		}
		eligible[code] = true
		results = append(results, claimReadyResult{TaskCode: code, Status: "selected"})
	}
	if !req.Execute {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "support": "legacy_unverified", "selected": len(eligible), "readiness": len(eligible), "claims": 0, "results": results})
		return
	}
	claims := 0
	if len(eligible) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "complete": true, "no_op": true, "support": "legacy_unverified", "selected": 0, "readiness": 0, "claims": 0, "results": results})
		return
	}
	uncertain := false
	for i := range results {
		if !eligible[results[i].TaskCode] {
			continue
		}
		claims++
		credit, energy, claimErr := p.cfg.Upstream.ClaimReward(a, results[i].TaskCode)
		if claimErr == nil {
			if credit < 0 || energy < 0 {
				results[i].Status = "uncertain"
				uncertain = true
			} else {
				results[i].Status = "submitted"
				results[i].ReceiptRewardReported = credit > 0 || energy > 0
				results[i].Credit, results[i].Energy = credit, energy
			}
		} else {
			results[i].Status = "uncertain"
			uncertain = true
		}
	}
	// One and only one post-write default-list confirmation.
	if confirmed, listErr := p.cfg.Upstream.ListTasks(a); listErr == nil {
		for i := range results {
			if results[i].Status != "submitted" && results[i].Status != "uncertain" {
				continue
			}
			matches := 0
			claimed := false
			for _, t := range confirmed {
				if t.TaskCode == results[i].TaskCode {
					matches++
					claimed = claimed || t.Claimed
				}
			}
			if matches == 1 && claimed {
				results[i].UpstreamClaimed = true
				if results[i].Status == "submitted" {
					results[i].Status = "confirmed"
					results[i].RewardReceived = results[i].ReceiptRewardReported
				} else {
					results[i].Status = "claimed_outcome_uncertain"
				}
			} else {
				if results[i].Status == "submitted" {
					results[i].Status = "submitted_unverified"
				}
				uncertain = true
			}
		}
	} else {
		uncertain = true
		for i := range results {
			if results[i].Status == "submitted" {
				results[i].Status = "submitted_unverified"
			}
		}
	}
	complete := !uncertain
	writeJSON(w, http.StatusOK, map[string]any{"ok": complete, "complete": complete, "support": "legacy_unverified", "selected": len(eligible), "readiness": len(eligible), "claims": claims, "results": results})
}
