package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestFullLegacyActionFixtures executes every registered legacy action against a
// deterministic failing upstream.  The actions remain the production funcs: a
// request is allowed to reach the real Client, while the fixture prevents any
// account mutation or network access.  This intentionally records the first
// method/path and the returned behavior rather than treating an HTTP 200 mock
// as proof that a reward was earned.
func TestFullLegacyActionFixtures(t *testing.T) {
	oldReport, oldMP, oldPoll, oldGap := reportGap, mpActionGap, mpChatEventGap, claimPollGap
	oldAttempts := claimPollAttempts
	reportGap, mpActionGap, mpChatEventGap, claimPollGap, claimPollAttempts = 0, 0, 0, 0, 1
	t.Cleanup(func() {
		reportGap, mpActionGap, mpChatEventGap, claimPollGap, claimPollAttempts = oldReport, oldMP, oldPoll, oldGap, oldAttempts
	})

	// These are the actions whose production contract is telemetry-only.  The
	// remaining actions attempt a product operation (chat, adoption, theme, or
	// expert flow) before/alongside telemetry.
	eventOnly := map[string]bool{
		"chat_5":        true,
		"RichMeow_Chat": true, "Buddy_App": true, "Buddy_App_QQ": true,
		"automation_1": true, "Library_read": true, "template_5": true,
		"playbook_prompt": true, "create_canvas": true,
		"school_season": true, "Sequential_Tasks_1": true,
		"Sequential_Tasks_2": true, "Sequential_Tasks_3": true,
		"Sequential_Tasks_4": true, "Sequential_Tasks_5": true,
		"Sequential_Tasks_6": true, "Sequential_Tasks_7": true,
	}
	failurePaths := map[string][]string{
		"chat_5": {"GET /v2/activity/growth/tasks"}, "first_buddy": {"POST /v2/report"}, "Model_chat_GLM5.2": {"POST /v2/activity/growth/tasks/accept", "POST /v2/chat/completions"},
		"RichMeow_Chat": {"POST /v2/report"}, "Buddy_App": {"POST /v2/report"}, "Buddy_App_QQ": {"POST /v2/report"}, "automation_1": {"POST /v2/report"}, "Library_read": {"POST /v2/report"}, "template_5": {"POST /v2/report"}, "playbook_prompt": {"POST /v2/report"}, "create_canvas": {"POST /v2/report"},
		"expert_5": {"POST /portal/operation-platform/market/expert/list"}, "Expert_team_use_3": {"POST /portal/operation-platform/market/expert/list"}, "Hp_Appearance": {"POST /v2/user-asset/appearance/set"}, "skill_1": {"POST /v2/chat/completions"}, "Expert_lighthouse": {"POST /portal/operation-platform/market/expert/list", "POST /v2/report"}, "black_cat": {},
		"school_season": {"GET /v2/activity/growth/tasks"}, "Sequential_Tasks_1": {"GET /v2/activity/growth/tasks"}, "Sequential_Tasks_2": {"GET /v2/activity/growth/tasks"}, "Sequential_Tasks_3": {"GET /v2/activity/growth/tasks"}, "Sequential_Tasks_4": {"GET /v2/activity/growth/tasks"}, "Sequential_Tasks_5": {"GET /v2/activity/growth/tasks"}, "Sequential_Tasks_6": {"GET /v2/activity/growth/tasks"}, "Sequential_Tasks_7": {"GET /v2/activity/growth/tasks"},
	}

	if upstream.InNightWindow(time.Now()) {
		failurePaths["black_cat"] = []string{"GET /v2/activity/growth/tasks"}
	}
	if len(autoActions) != 25 {
		t.Fatalf("legacy registry count = %d, want 25", len(autoActions))
	}
	seen := make(map[string]bool, len(autoActions))
	for _, act := range autoActions {
		if seen[act.TaskCode] {
			t.Fatalf("duplicate legacy action code %q", act.TaskCode)
		}
		seen[act.TaskCode] = true

		var mu sync.Mutex
		var paths []string
		rt := fullActionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			paths = append(paths, r.Method+" "+r.URL.Path)
			mu.Unlock()
			// Non-2xx is deliberate: it exercises each real function's observed
			// error/guard behavior without returning a fake completion or reward.
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Status:     "502 Bad Gateway",
				Header:     make(http.Header),
				Body:       http.NoBody,
				Request:    r,
			}, nil
		})
		client := &upstream.Client{HTTP: &http.Client{Transport: rt}, ChatBaseCN: "http://fixture.invalid", BillingBaseCN: "http://fixture.invalid", WebBaseCN: "http://fixture.invalid"}
		p := &Panel{cfg: Config{Upstream: client}}
		msg, err := act.run(p, &auth.Auth{AccessToken: "fixture-token", UID: "fixture-user", Nickname: "fixture"})
		mu.Lock()
		gotPaths := append([]string(nil), paths...)
		mu.Unlock()

		// black_cat is time-gated by design; at daytime it has zero requests and
		// its returned guard message is the expected behavior, not a live success.
		if err == nil && strings.TrimSpace(msg) == "" {
			t.Errorf("%s returned empty result with no error (paths=%v)", act.TaskCode, gotPaths)
		}
		if act.TaskCode != "black_cat" && len(gotPaths) == 0 {
			t.Errorf("%s made no upstream request", act.TaskCode)
		}
		if containsPath(gotPaths, "/claim") {
			t.Errorf("%s attempted reward claim after fixture failure: %v", act.TaskCode, gotPaths)
		}
		if err == nil {
			switch act.TaskCode {
			case "template_5":
				if !strings.Contains(msg, "上报失败") {
					t.Errorf("template failure not explicit: %q", msg)
				}
			case "black_cat":
				if upstream.InNightWindow(time.Now()) || !strings.Contains(msg, "不在") {
					t.Errorf("unexpected blackcat completion: %q", msg)
				}
			default:
				t.Errorf("%s fixture failure lost its error: %q", act.TaskCode, msg)
			}
		}
		if want := failurePaths[act.TaskCode]; !sameStrings(gotPaths, want) {
			t.Errorf("%s failure continued past expected boundary: got=%v want=%v", act.TaskCode, gotPaths, want)
		}
		kind := "product+event"
		if eventOnly[act.TaskCode] {
			kind = "event-only"
		}
		result := "message"
		if err != nil {
			result = "error: " + err.Error()
		}
		t.Logf("%s | %s | requests=%d %v | %s", act.TaskCode, kind, len(gotPaths), gotPaths, result)
	}
}

// TestFullLegacyActionNormalFixtures lets every action traverse its normal
// success/event path with a non-complete task (0/1).  The fixture deliberately
// never advances progress, so a 200 response cannot be mistaken for a reward:
// no action may call a claim endpoint.
func TestFullLegacyActionNormalFixtures(t *testing.T) {
	oldReport, oldMP, oldPoll, oldGap := reportGap, mpActionGap, mpChatEventGap, claimPollGap
	oldAttempts := claimPollAttempts
	// Jitter is in [0,10s), so -10s + jitter is ALWAYS negative;
	// time.Sleep returns immediately. This changes fixture timing only.
	reportGap, mpActionGap, mpChatEventGap, claimPollGap, claimPollAttempts = 0, 0, -10*time.Second, 0, 1
	t.Cleanup(func() {
		reportGap, mpActionGap, mpChatEventGap, claimPollGap, claimPollAttempts = oldReport, oldMP, oldPoll, oldGap, oldAttempts
	})

	first := map[string]string{
		"chat_5": "/v2/activity/growth/tasks", "first_buddy": "/v2/report", "Model_chat_GLM5.2": "/v2/activity/growth/tasks/accept",
		"RichMeow_Chat": "/v2/report", "Buddy_App": "/v2/report", "Buddy_App_QQ": "/v2/report", "automation_1": "/v2/report", "Library_read": "/v2/report",
		"template_5": "/v2/report", "playbook_prompt": "/v2/report", "create_canvas": "/v2/report", "expert_5": "/portal/operation-platform/market/expert/list",
		"Expert_team_use_3": "/portal/operation-platform/market/expert/list", "Hp_Appearance": "/v2/user-asset/appearance/set", "skill_1": "/v2/chat/completions",
		"Expert_lighthouse": "/portal/operation-platform/market/expert/list", "black_cat": "", "school_season": "/v2/activity/growth/tasks",
		"Sequential_Tasks_1": "/v2/activity/growth/tasks", "Sequential_Tasks_2": "/v2/activity/growth/tasks", "Sequential_Tasks_3": "/v2/activity/growth/tasks",
		"Sequential_Tasks_4": "/v2/activity/growth/tasks", "Sequential_Tasks_5": "/v2/activity/growth/tasks", "Sequential_Tasks_6": "/v2/activity/growth/tasks", "Sequential_Tasks_7": "/v2/activity/growth/tasks",
	}
	night := upstream.InNightWindow(time.Now())
	if night {
		first["black_cat"] = "/v2/activity/growth/tasks"
	}
	required := map[string][]string{
		"first_buddy":       {"/activity/growth/buddy/agreement", "/activity/growth/buddy/first"},
		"Model_chat_GLM5.2": {"/v2/chat/completions", "/v2/report"},
		"expert_5":          {"/v2/chat/completions", "/v2/report"}, "Expert_team_use_3": {"/v2/chat/completions", "/v2/report"},
		"Hp_Appearance": {"/v2/user-asset/appearance/set", "/v2/report"},
		"skill_1":       {"/v2/chat/completions", "/v2/report"}, "Expert_lighthouse": {"/v2/chat/completions", "/v2/report"},
	}
	for _, act := range autoActions {
		var mu sync.Mutex
		var paths []string
		rt := fullActionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			paths = append(paths, r.Method+" "+r.URL.Path)
			mu.Unlock()
			if r.Method == http.MethodPost && r.Body != nil {
				raw, e := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(raw))
				if e != nil || !json.Valid(raw) {
					t.Errorf("%s invalid JSON request at %s", act.TaskCode, r.URL.Path)
				}
				if r.URL.Path == "/portal/operation-platform/market/expert/list" {
					var fields map[string]any
					_ = json.Unmarshal(raw, &fields)
					if act.TaskCode == "Expert_team_use_3" && fields["expert_type"] != "team" {
						t.Error("team request lost expert_type")
					}
				}
			}
			if (strings.HasPrefix(act.TaskCode, "Sequential_Tasks_") || act.TaskCode == "school_season") && r.URL.Path == "/v2/activity/growth/tasks" && r.Header.Get("X-Client-Platform") != "miniprogram" {
				t.Errorf("%s lost MP header", act.TaskCode)
			}
			path := r.URL.Path
			body := `{"code":0,"msg":"OK","data":{}}`
			header := make(http.Header)
			header.Set("Content-Type", "application/json")
			if path == "/v2/activity/growth/tasks" {
				body = fmt.Sprintf(`{"code":0,"msg":"OK","data":{"tasks":[{"task_code":%q,"target":1,"current":0,"accept_status":"accepted"}]}}`, act.TaskCode)
			}
			if path == "/portal/operation-platform/market/expert/list" {
				body = `{"code":0,"msg":"OK","data":{"experts":[{"expert_id":"ex_fixture","expert_type":"agent","display_name_zh":"Fixture专家","profession_zh":"Fixture专家","version":"1.0"}]}}`
			}
			if path == "/v2/chat/completions" {
				header.Set("Content-Type", "text/event-stream")
				return &http.Response{StatusCode: 200, Status: "200 OK", Header: header, Body: io.NopCloser(strings.NewReader("data: {\"id\":\"cmb-0123456789abcdef0123456789abcdef\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")), Request: r}, nil
			}
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
		client := &upstream.Client{HTTP: &http.Client{Transport: rt}, ChatBaseCN: "http://fixture.invalid", BillingBaseCN: "http://fixture.invalid", WebBaseCN: "http://fixture.invalid"}
		msg, err := act.run(&Panel{cfg: Config{Upstream: client}}, &auth.Auth{AccessToken: "fixture-token", UID: "fixture-user", Nickname: "fixture"})
		mu.Lock()
		got := append([]string(nil), paths...)
		mu.Unlock()
		if err != nil {
			t.Errorf("%s normal fixture error: %v paths=%v", act.TaskCode, err, got)
		}
		if strings.TrimSpace(msg) == "" {
			t.Errorf("%s normal fixture empty result", act.TaskCode)
		}
		if want := first[act.TaskCode]; want != "" && (len(got) == 0 || strings.TrimPrefix(strings.TrimPrefix(got[0], "GET "), "POST ") != want) {
			t.Errorf("%s missing expected path %s: %v", act.TaskCode, want, got)
		}
		if act.TaskCode == "black_cat" && !night && len(got) != 0 {
			t.Errorf("daytime blackcat requests=%v", got)
		}
		for _, path := range required[act.TaskCode] {
			if !containsPath(got, path) {
				t.Errorf("%s missing required product request %s: %v", act.TaskCode, path, got)
			}
		}
		if containsPath(got, "/claim") {
			t.Errorf("%s claimed non-complete task: %v", act.TaskCode, got)
		}
		t.Logf("%s | normal non-complete | first=%s requests=%d paths=%v", act.TaskCode, first[act.TaskCode], len(got), got)
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if strings.Contains(path, want) {
			return true
		}
	}
	return false
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type fullActionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f fullActionRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
