package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

const workflowMaxBody = 1 << 20

func workflowUnsupported(a *auth.Auth) error {
	if a == nil || a.IsEnterprise() || a.IsGlobal() || strings.TrimSpace(a.UID) == "" || strings.TrimSpace(a.AccessTokenValue()) == "" || a.NeedsRefresh(0) {
		return errors.New("workflow supports domestic personal accounts only")
	}
	return nil
}
func workflowContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
func workflowPreflight(ctx context.Context, a *auth.Auth) error {
	ctx = workflowContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	return workflowUnsupported(a)
}

// WorkflowTasks is the context-bound, single-request task listing used by the panel.
func (c *Client) WorkflowTasks(ctx context.Context, a *auth.Auth, mp bool) ([]Task, error) {
	ctx = workflowContext(ctx)
	if err := workflowPreflight(ctx, a); err != nil {
		return nil, err
	}
	path := tasksListPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.chatBase(a)+path, nil)
	if err != nil {
		return nil, errors.New("workflow request construction failed")
	}
	c.BillingHeaders(req, a)
	if mp {
		req.Header.Set("X-Client-Platform", mpPlatform)
	}
	data, err := c.workflowJSON(req)
	if err != nil {
		return nil, err
	}
	var top struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	if err := strictUnmarshal(data, &top); err != nil || top.Tasks == nil {
		return nil, errors.New("workflow task schema invalid")
	}
	validProgress := make([]bool, len(top.Tasks))
	seen := make(map[string]struct{}, len(top.Tasks))
	for i, raw := range top.Tasks {
		var obj map[string]json.RawMessage
		if err := strictObject(raw, &obj); err != nil {
			return nil, errors.New("workflow task schema invalid")
		}
		var code string
		v, ok := obj["task_code"]
		if !ok || json.Unmarshal(v, &code) != nil || strings.TrimSpace(code) == "" {
			return nil, errors.New("workflow task schema invalid")
		}
		if _, ok := seen[code]; ok {
			return nil, errors.New("workflow task schema invalid")
		}
		seen[code] = struct{}{}
		for _, key := range []string{"locked", "reward_buddy", "has_reward"} {
			if v, ok := obj[key]; ok {
				var x *bool
				if json.Unmarshal(v, &x) != nil || x == nil {
					return nil, errors.New("workflow task schema invalid")
				}
			}
		}
		if v, ok := obj["status"]; ok {
			var x *string
			if json.Unmarshal(v, &x) != nil || x == nil {
				return nil, errors.New("workflow task schema invalid")
			}
		}
		if nested, ok := obj["progress"]; ok {
			pr, good := parseProgress(nested)
			validProgress[i] = good && len(pr) == 2
		} else {
			var current, target *int64
			ce := json.Unmarshal(obj["current"], &current)
			te := json.Unmarshal(obj["target"], &target)
			validProgress[i] = ce == nil && te == nil && current != nil && target != nil && *current >= 0 && *target >= 0
		}
	}
	tasks, err := parseGrowthTasks(data)
	if err != nil {
		return nil, errors.New("workflow task schema invalid")
	}
	for i := range tasks {
		if !validProgress[i] {
			tasks[i].Current, tasks[i].Target, tasks[i].Claimable = 0, 0, false
		}
	}
	return tasks, nil
}

// WorkflowBalance returns the SDK-normalized sum of personal resource remains.
func (c *Client) WorkflowBalance(ctx context.Context, a *auth.Auth) (int64, error) {
	ctx = workflowContext(ctx)
	if err := workflowPreflight(ctx, a); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.billingBase(a)+billingMeterPathV2, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return 0, errors.New("workflow request construction failed")
	}
	c.BillingHeaders(req, a)
	data, err := c.workflowJSON(req)
	if err != nil {
		return 0, err
	}
	var root map[string]json.RawMessage
	if err := strictObject(data, &root); err != nil {
		return 0, errors.New("workflow balance schema invalid")
	}
	respRaw, ok := root["Response"]
	if !ok {
		return 0, errors.New("workflow balance schema invalid")
	}
	var resp map[string]json.RawMessage
	if err := strictObject(respRaw, &resp); err != nil {
		return 0, errors.New("workflow balance schema invalid")
	}
	dRaw, ok := resp["Data"]
	if !ok {
		return 0, errors.New("workflow balance schema invalid")
	}
	var dat map[string]json.RawMessage
	if err := strictObject(dRaw, &dat); err != nil {
		return 0, errors.New("workflow balance schema invalid")
	}
	aRaw, ok := dat["Accounts"]
	if !ok || string(aRaw) == "null" {
		return 0, errors.New("workflow balance schema invalid")
	}
	var accounts []json.RawMessage
	if err := strictUnmarshal(aRaw, &accounts); err != nil || len(accounts) == 0 {
		return 0, errors.New("workflow balance schema invalid")
	}
	var total int64
	for _, ar := range accounts {
		var m map[string]json.RawMessage
		if err := strictObject(ar, &m); err != nil {
			return 0, errors.New("workflow balance schema invalid")
		}
		vals := make(map[string]int64)
		for _, k := range []string{"CycleCapacitySize", "CycleCapacityRemain", "CycleCapacityUsed", "CapacitySize", "CapacityRemain", "CapacityUsed"} {
			raw, ok := m[k]
			if !ok {
				continue
			}
			if string(raw) == "null" {
				return 0, errors.New("workflow balance schema invalid")
			}
			var n int64
			if err := json.Unmarshal(raw, &n); err != nil || n < 0 {
				return 0, errors.New("workflow balance schema invalid")
			}
			vals[k] = n
		}
		var acc respAccount
		acc.CycleCapacitySize, acc.CycleCapacityRemain, acc.CycleCapacityUsed = vals["CycleCapacitySize"], vals["CycleCapacityRemain"], vals["CycleCapacityUsed"]
		acc.CapacitySize, acc.CapacityRemain, acc.CapacityUsed = vals["CapacitySize"], vals["CapacityRemain"], vals["CapacityUsed"]
		if n, ok := vals["CycleCapacitySize"]; ok && n > 0 {
			if _, present := vals["CycleCapacityRemain"]; !present {
				return 0, errors.New("workflow balance schema invalid")
			}
		} else if _, ok := vals["CapacityRemain"]; !ok {
			return 0, errors.New("workflow balance schema invalid")
		}
		r, _, size := packageRemainUsed(acc)
		if r < 0 {
			r = 0
		}
		if size < r {
			size = r
		}
		if r > math.MaxInt64-total {
			return 0, errors.New("workflow balance overflow")
		}
		total += r
		_ = size
	}
	return total, nil
}

// WorkflowClaim performs only the default web claim endpoint; it never falls back.
func (c *Client) WorkflowClaim(ctx context.Context, a *auth.Auth, taskCode string) (ClaimReceipt, error) {
	ctx = workflowContext(ctx)
	if err := workflowPreflight(ctx, a); err != nil {
		return ClaimReceipt{}, err
	}
	if taskCode == "" || strings.TrimSpace(taskCode) != taskCode || strings.Contains(taskCode, "%") || strings.ContainsAny(taskCode, "/?#") {
		return ClaimReceipt{}, errors.New("invalid task code")
	}
	for _, r := range taskCode {
		if unicode.IsControl(r) {
			return ClaimReceipt{}, errors.New("invalid task code")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webBase(a)+"/activity/growth/tasks/"+url.PathEscape(taskCode)+"/claim", nil)
	if err != nil {
		return ClaimReceipt{}, errors.New("workflow request construction failed")
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.workbuddy.cn")
	req.Header.Set("Referer", "https://www.workbuddy.cn/profile/growth-center")
	req.Header.Set("x-client-platform", "web")
	if ua := c.userAgent(a); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if d := a.DomainValue(); d != "" {
		req.Header.Set("X-Domain", d)
	}
	data, err := c.doClaimJSON(req)
	if err != nil {
		return ClaimReceipt{}, sanitizeWorkflowErr(ctx, err)
	}
	return parseClaimRewardReceipt(data)
}

func (c *Client) workflowJSON(req *http.Request) (json.RawMessage, error) {
	if c.HTTP == nil {
		return nil, errors.New("workflow http client unavailable")
	}
	hc := *c.HTTP
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return nil, sanitizeWorkflowErr(req.Context(), err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, workflowMaxBody+1))
	if err != nil {
		return nil, errors.New("workflow response unreadable")
	}
	if len(raw) > workflowMaxBody {
		return nil, errors.New("workflow response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Kind: Classify(resp.StatusCode, string(raw)), Status: resp.StatusCode, Msg: "workflow upstream request failed"}
	}
	var env map[string]json.RawMessage
	if err := strictObject(raw, &env); err != nil {
		return nil, errors.New("workflow response schema invalid")
	}
	cr, ok := env["code"]
	if !ok {
		return nil, errors.New("workflow response schema invalid")
	}
	var code *int64
	if json.Unmarshal(cr, &code) != nil || code == nil {
		return nil, errors.New("workflow response schema invalid")
	}
	if *code != 0 {
		return nil, &Error{Kind: ErrClient, Status: resp.StatusCode, Msg: "workflow upstream request failed"}
	}
	d, ok := env["data"]
	if !ok {
		return nil, errors.New("workflow response schema invalid")
	}
	return d, nil
}

func strictObject(raw []byte, out *map[string]json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("object required")
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := k.(string)
		if !ok {
			return errors.New("key required")
		}
		if _, ok := m[key]; ok {
			return errors.New("duplicate key")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		m[key] = v
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing data")
	}
	*out = m
	return nil
}
func strictUnmarshal(raw []byte, v any) error {
	if err := validateStrictJSON(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func validateStrictJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := validateStrictValue(dec); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func validateStrictValue(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok {
				return errors.New("invalid key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate key")
			}
			seen[key] = struct{}{}
			if err := validateStrictValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := validateStrictValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return errors.New("invalid json delimiter")
	}
}
func sanitizeWorkflowErr(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	var ue *Error
	if errors.As(err, &ue) {
		return &Error{Kind: ue.Kind, Status: ue.Status, Msg: "workflow upstream request failed"}
	}
	return errors.New("workflow upstream request failed")
}

// WorkflowChat performs the bounded coordinator probe without sharing redirect
// policy or mutating the ordinary chat client.
func (c *Client) WorkflowChat(ctx context.Context, a *auth.Auth, model string, maxTokens int) (io.ReadCloser, int, error) {
	ctx = workflowContext(ctx)
	if err := workflowPreflight(ctx, a); err != nil {
		return nil, 0, err
	}
	if maxTokens < 1 || maxTokens > 64 || strings.TrimSpace(model) == "" {
		return nil, 0, errors.New("invalid workflow chat parameters")
	}
	body, err := json.Marshal(map[string]any{
		"model": model, "messages": []map[string]string{{"role": "user", "content": "Reply only OK."}},
		"stream": true, "max_tokens": maxTokens, "thinking": map[string]string{"type": "disabled"},
		"enable_thinking": false, "stream_options": map[string]bool{"include_usage": true},
	})
	if err != nil {
		return nil, 0, errors.New("workflow request construction failed")
	}
	prepared := c.prepareBody(body, a.Realm(), a.UID, "")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatBase(a)+chatCompletionsPath, bytes.NewReader(prepared))
	if err != nil {
		return nil, 0, errors.New("workflow request construction failed")
	}
	c.ChatHeaders(req, a, "", ChatMeta{})
	httpClient := c.chatHTTP()
	if httpClient == nil {
		return nil, 0, errors.New("workflow http client unavailable")
	}
	hc := *httpClient
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, sanitizeWorkflowErr(req.Context(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, resp.StatusCode, &Error{Kind: Classify(resp.StatusCode, ""), Status: resp.StatusCode, Msg: "workflow upstream request failed"}
	}
	return resp.Body, resp.StatusCode, nil
}
