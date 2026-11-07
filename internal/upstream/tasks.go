// tasks.go growth 域「任务」接口：列表查询 / 接受 / 领取奖励。
//
// 来源：上游 scripts/task_common.py 实测口径（list_tasks/accept_tasks/claim_reward），
// 集成进主程序后不再需要外部脚本。
//
// 端点（chatBase，BillingHeaders）：
//   - GET  /v2/activity/growth/tasks                全量任务列表（含 progress/accept_status）
//   - POST /v2/activity/growth/tasks/accept         {"task_codes":[...]} not_accepted → accepted
//   - POST /v2/activity/growth/tasks/reward/claim   {"task_code":"..."} 完成态领奖
//
// 语义要点：
//   - accept 是"报名"，不产生进度；进度由服务端行为事件点亮（如 chat_request_send 上报、
//     真实对话），故 accept 后可幂等重放。
//   - claim 仅在 progress 达标后可领；重复领返回业务错误（安全，不需要前置状态判断）。
package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// growth 域任务路径（与 scripts/task_common.py 对齐）。
const (
	tasksListPath   = "/v2/activity/growth/tasks"
	tasksAcceptPath = "/v2/activity/growth/tasks/accept"
)

// mpPlatform 小程序口径头值：小程序限定任务（Sequential_Tasks_1 / school_season）
// 的列表下发、accept、claim 全链路要求 X-Client-Platform: miniprogram。
const mpPlatform = "miniprogram"

// Task 单个任务的对外视图（字段名与上游 JSON 对齐，多余字段不透出）。
type Task struct {
	TaskCode     string `json:"task_code"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"` // 操作指引（含跳转说明）
	TaskDesc     string `json:"task_desc,omitempty"`   // 达成条件简述
	Credit       int64  `json:"credit,omitempty"`      // 奖励积分（上游 reward_credit）
	Energy       int64  `json:"energy,omitempty"`      // 奖励能量（上游 reward_energy）
	HasReward    bool   `json:"has_reward,omitempty"`  // 是否带奖励
	RewardBuddy  bool   `json:"reward_buddy,omitempty"`
	TaskType     string `json:"task_type,omitempty"` // single（一次性）/ 累计型
	Tag          string `json:"tag,omitempty"`       // 端标记（PC 等）
	JumpURL      string `json:"jump_url,omitempty"`  // 客户端跳转协议（workbuddy://...）
	Locked       bool   `json:"locked,omitempty"`    // 上游标记未解锁
	Target       int64  `json:"target"`              // 目标次数（恒输出：0 是有效进度值）
	Current      int64  `json:"current"`             // 当前进度（恒输出：0 是有效进度值）
	AcceptStatus string `json:"accept_status,omitempty"`
	Status       string `json:"status,omitempty"`    // 上游任务状态（complete 等）
	Claimable    bool   `json:"claimable,omitempty"` // 进度达标且未领取（本地推算）
	Claimed      bool   `json:"claimed,omitempty"`   // 已领取（accept_status == claimed）
}

// IsInactive reports states in which a task cannot be claimed.
func (t Task) IsInactive() bool {
	if t.Locked {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(t.Status)) {
	case "expired", "ended", "disabled", "cancelled":
		return true
	default:
		return false
	}
}

// CanClaimReward reports whether the locally observed task state is claimable.
func (t Task) CanClaimReward() bool {
	return t.Claimable && !t.Claimed && !t.IsInactive() && t.Target > 0 && t.Current >= t.Target
}

// ClaimReceipt is the normalized result of a reward claim.
type ClaimReceipt struct {
	Credit              int64
	Energy              int64
	AlreadyClaimed      bool
	AlreadyClaimedKnown bool
}

func (r ClaimReceipt) HasReward() bool { return r.Credit > 0 || r.Energy > 0 }

// ListTasks 拉取全量任务列表（默认口径，无端标记头）。
// 响应形如 data.tasks[]，元素字段随任务类型变化（progress 可能是 {current,target} 或平铺），
// 这里做宽松解析：两种形状都尝试。
func (c *Client) ListTasks(a *auth.Auth) ([]Task, error) {
	data, err := c.growthJSON(a, http.MethodGet, tasksListPath, nil)
	if err != nil {
		return nil, err
	}
	return parseGrowthTasks(data)
}

// ListTasksMP 拉取小程序口径的任务列表（X-Client-Platform: miniprogram）。
// 小程序限定任务（Sequential_Tasks_1「小程序首对话」/ school_season「校园日」等）
// 仅在该口径下发——默认列表不出现，accept/claim 同样要求该头（缺头 accept 返回
// task not found，上游 task_runner 实测）。**实测 mp 列表是默认口径的超集**
// （含 RichMeow/Model_chat 等常规任务 + wb_wechat_oa_subscribe_task 等 mp 专属），
// 合并时调用方须按 task_code 去重。
func (c *Client) ListTasksMP(a *auth.Auth) ([]Task, error) {
	data, err := c.growthJSONMP(a, http.MethodGet, tasksListPath, nil)
	if err != nil {
		return nil, err
	}
	return parseGrowthTasks(data)
}

// growthJSONMP 发 growth 域请求（小程序口径：叠加 X-Client-Platform: miniprogram）
// 并解信封。语义同 growthJSON。
func (c *Client) growthJSONMP(a *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.chatBase(a)+path, rdr)
	if err != nil {
		return nil, err
	}
	c.BillingHeaders(req, a)
	req.Header.Set("X-Client-Platform", mpPlatform)
	return c.doJSON(req)
}

// AcceptTasksMP 接受小程序限定任务（mp 头；缺头实测 task not found）。
// 幂等语义同 AcceptTasks。
func (c *Client) AcceptTasksMP(a *auth.Auth, taskCodes []string) error {
	_, err := c.growthJSONMP(a, http.MethodPost, tasksAcceptPath, map[string]any{"task_codes": taskCodes})
	return err
}

// ClaimRewardMP 领取小程序限定任务奖励：chat 域 /activity/growth/tasks/{code}/claim
// + mp 头（上游 task_runner claim_one(mp=True) 同款）；chat 域 400 时降级 Web 域
// 领奖端点（ClaimReward，x-client-platform: web 形态）。返回 (credit, energy, err)。
func (c *Client) ClaimRewardMP(a *auth.Auth, taskCode string) (credit, energy int64, err error) {
	r, err := c.ClaimRewardMPReceipt(a, taskCode)
	return r.Credit, r.Energy, err
}

// ClaimRewardMPReceipt is the receipt form of ClaimRewardMP.
func (c *Client) ClaimRewardMPReceipt(a *auth.Auth, taskCode string) (ClaimReceipt, error) {
	req, err := http.NewRequest(http.MethodPost,
		c.chatBase(a)+"/activity/growth/tasks/"+url.PathEscape(taskCode)+"/claim", nil)
	if err != nil {
		return ClaimReceipt{}, err
	}
	c.BillingHeaders(req, a)
	req.Header.Set("X-Client-Platform", mpPlatform)
	data, err := c.doJSON(req)
	if err != nil {
		// chat 域对该路径 400（部分任务/租户形态）→ Web 域降级（已实测可领）。
		if ue, ok := err.(*Error); ok && ue.Status == http.StatusBadRequest {
			return c.ClaimRewardReceipt(a, taskCode)
		}
		return ClaimReceipt{}, err
	}
	return parseClaimRewardReceipt(data)
}

// parseClaimRewardReceipt strictly parses the claim data object while allowing
// unknown extension fields.
func parseClaimRewardReceipt(data json.RawMessage) (ClaimReceipt, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return ClaimReceipt{}, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return ClaimReceipt{}, fmt.Errorf("claim receipt must be an object")
	}
	obj := make(map[string]json.RawMessage)
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return ClaimReceipt{}, err
		}
		key, ok := kt.(string)
		if !ok {
			return ClaimReceipt{}, fmt.Errorf("invalid claim receipt key")
		}
		if _, exists := obj[key]; exists {
			return ClaimReceipt{}, fmt.Errorf("duplicate claim receipt key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return ClaimReceipt{}, err
		}
		obj[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return ClaimReceipt{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ClaimReceipt{}, fmt.Errorf("trailing claim receipt data")
	}
	var r ClaimReceipt
	knownAmount := false
	for key, raw := range obj {
		switch key {
		case "credit", "energy":
			var n *int64
			if err := json.Unmarshal(raw, &n); err != nil || n == nil || *n < 0 {
				return ClaimReceipt{}, fmt.Errorf("invalid %s", key)
			}
			knownAmount = true
			v := *n
			if key == "credit" {
				r.Credit = v
			} else {
				r.Energy = v
			}
		case "already_claimed":
			var b *bool
			if err := json.Unmarshal(raw, &b); err != nil || b == nil {
				return ClaimReceipt{}, fmt.Errorf("invalid already_claimed")
			}
			r.AlreadyClaimed, r.AlreadyClaimedKnown = *b, true
		}
	}
	if r.AlreadyClaimed && r.HasReward() {
		return ClaimReceipt{}, fmt.Errorf("already claimed receipt has reward")
	}
	if !knownAmount && !r.AlreadyClaimed {
		return ClaimReceipt{}, fmt.Errorf("empty claim receipt")
	}
	return r, nil
}

// parseClaimReward preserves the legacy tuple API.
func parseClaimReward(data json.RawMessage) (credit, energy int64, err error) {
	r, err := parseClaimRewardReceipt(data)
	return r.Credit, r.Energy, err
}

// parseGrowthTasks 解析 growth 任务列表 data.tasks[]（默认与 mp 口径共用）。
func parseGrowthTasks(data json.RawMessage) ([]Task, error) {
	var resp struct {
		Tasks []struct {
			TaskCode     string          `json:"task_code"`
			Title        string          `json:"title"`
			Description  string          `json:"description"`
			TaskDesc     string          `json:"task_desc"`
			RewardCredit int64           `json:"reward_credit"` // 上游实际字段名（reward_ 前缀）
			RewardEnergy int64           `json:"reward_energy"`
			HasReward    bool            `json:"has_reward"`
			RewardBuddy  bool            `json:"reward_buddy"`
			TaskType     string          `json:"task_type"`
			Tag          string          `json:"tag"`
			JumpURL      string          `json:"jump_url"`
			Locked       bool            `json:"locked"`
			AcceptStatus string          `json:"accept_status"`
			Status       string          `json:"status"`
			Target       int64           `json:"target"`
			Current      int64           `json:"current"`
			Progress     json.RawMessage `json:"progress"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		cur, tgt := t.Current, t.Target
		// progress 可能是 {current,target} 对象（实测口径），覆盖平铺字段。
		if len(t.Progress) > 0 && string(t.Progress) != "null" {
			pr, ok := parseProgress(t.Progress)
			// A nested progress object is treated as an atomic snapshot.  Do not
			// combine one nested field with stale flat fields: a partial snapshot
			// is not sufficient evidence for claiming a reward.
			if ok && len(pr) == 2 {
				cur = pr["current"]
				tgt = pr["target"]
			} else {
				// A supplied but malformed/non-object progress must not leave
				// stale flat progress eligible.
				cur, tgt = 0, 0
			}
		}
		claimed := t.AcceptStatus == "claimed"
		out = append(out, Task{
			TaskCode:     t.TaskCode,
			Title:        t.Title,
			Description:  t.Description,
			TaskDesc:     t.TaskDesc,
			Credit:       t.RewardCredit,
			Energy:       t.RewardEnergy,
			HasReward:    t.HasReward,
			RewardBuddy:  t.RewardBuddy,
			TaskType:     t.TaskType,
			Tag:          t.Tag,
			JumpURL:      t.JumpURL,
			Locked:       t.Locked,
			Target:       tgt,
			Current:      cur,
			AcceptStatus: t.AcceptStatus,
			Status:       t.Status,
			Claimable:    !claimed && !t.Locked && !strings.EqualFold(strings.TrimSpace(t.Status), "expired") && !strings.EqualFold(strings.TrimSpace(t.Status), "ended") && !strings.EqualFold(strings.TrimSpace(t.Status), "disabled") && !strings.EqualFold(strings.TrimSpace(t.Status), "cancelled") && tgt > 0 && cur >= tgt,
			Claimed:      claimed,
		})
	}
	return out, nil
}

func parseProgress(raw json.RawMessage) (map[string]int64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '{' {
		return nil, false
	}
	out := make(map[string]int64)
	seen := make(map[string]bool)
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := kt.(string)
		if !ok || seen[key] {
			return nil, false
		}
		seen[key] = true
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		if key != "current" && key != "target" {
			continue
		}
		var n *int64
		if err := json.Unmarshal(v, &n); err != nil || n == nil || *n < 0 {
			return nil, false
		}
		out[key] = *n
	}
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return out, true
}

// AcceptTasks 接受任务（幂等：已 accepted 时上游返回成功或业务提示，均不视为致命错误）。
func (c *Client) AcceptTasks(a *auth.Auth, taskCodes []string) error {
	_, err := c.growthJSON(a, http.MethodPost, tasksAcceptPath, map[string]any{"task_codes": taskCodes})
	return err
}

// ClaimReward 领取单个任务奖励。
//
// 端点来源（实测）：Web 成长中心的领奖请求 ——
//
//	POST https://www.workbuddy.cn/activity/growth/tasks/<task_code>/claim
//	（任务码在**路径**里，无 body；带 x-client-platform: web 头，Bearer 鉴权）
//
// 关键区别：此前误用 CLI 域 copilot.tencent.com 的
// /v2/activity/growth/tasks/reward/claim（task_code 放 body），该路径**不存在**，
// 一直返回 400 "task not completed"，是此前领奖失败的真实原因。
// 本实现返回 (credit, energy, err)：credit/energy 为本次到账奖励（已领取过时为 0）。
func (c *Client) ClaimReward(a *auth.Auth, taskCode string) (credit, energy int64, err error) {
	r, err := c.ClaimRewardReceipt(a, taskCode)
	return r.Credit, r.Energy, err
}

// ClaimRewardReceipt is the receipt form of ClaimReward.
func (c *Client) ClaimRewardReceipt(a *auth.Auth, taskCode string) (ClaimReceipt, error) {
	req, err := http.NewRequest(http.MethodPost,
		c.webBase(a)+"/activity/growth/tasks/"+url.PathEscape(taskCode)+"/claim", nil)
	if err != nil {
		return ClaimReceipt{}, err
	}
	// Web 端请求头形状（对照浏览器实际请求）：Origin/Referer 指向 workbuddy.cn 成长中心，
	// 带 x-client-platform: web 标记来源端。
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

	data, err := c.doJSON(req)
	if err != nil {
		return ClaimReceipt{}, err
	}
	return parseClaimRewardReceipt(data)
}
