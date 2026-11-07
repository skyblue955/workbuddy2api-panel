package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

type modelChatRoundTripper struct {
	stream               string
	readErr              bool
	reports              int
	chatBody             []byte
	chatHeader           http.Header
	chatPath, chatMethod string
}
type modelChatBody struct {
	io.Reader
	readErr bool
}

func (b *modelChatBody) Close() error { return nil }
func (b *modelChatBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if b.readErr && err == io.EOF {
		return n, errors.New("fixture-private-read-error-token")
	}
	return n, err
}
func (rt *modelChatRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "chat") {
		rt.chatBody, _ = io.ReadAll(req.Body)
		rt.chatHeader = req.Header.Clone()
		rt.chatPath = req.URL.Path
		rt.chatMethod = req.Method
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &modelChatBody{Reader: strings.NewReader(rt.stream), readErr: rt.readErr}, Request: req}, nil
	}
	if req.URL.Path == "/v2/report" {
		rt.reports++
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{}}`)), Request: req}, nil
}

func TestRunModelChatRequiresCompleteAssistantStream(t *testing.T) {
	oldGap := reportGap
	reportGap = 0
	t.Cleanup(func() { reportGap = oldGap })
	valid := "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"
	cases := []struct {
		name, stream string
		readErr      bool
		wantReport   int
	}{
		{"valid", valid, false, 1}, {"errorframe", "data: {\"error\":{\"message\":\"bad\"}}\n\n", false, 0},
		{"truncated", "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n", false, 0}, {"readerror", valid, true, 0},
		{"idonly", "data: {\"id\":\"x\",\"choices\":[]}\n\ndata: [DONE]\n\n", false, 0},
		{"empty", "data: [DONE]\n\n", false, 0}, {"reasoningonly", "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thought\"}}]}\n\ndata: [DONE]\n\n", false, 0},
		{"oversize", "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", modelChatStreamLimit) + "\"}}]}\n\ndata: [DONE]\n\n", false, 0},
		{"malformed", "data: {not-json}\n\ndata: [DONE]\n\n", false, 0},
		{"event-error", "event: error\ndata: {\"message\":\"bad\"}\n\n", false, 0},
		{"error-after-done", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\ndata: {\"error\":{}}\n\n", false, 0},
		{"nonassistant", "data: {\"choices\":[{\"delta\":{\"role\":\"user\",\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n", false, 0},
		{"no-space", "data:{\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata:[DONE]\n\n", false, 1},
		{"empty-finish", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"\"}]}\n\ndata: [DONE]\n\n", false, 1},
		{"stop-eof", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n", false, 1},
		{"filtered-done", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n", false, 0},
		{"tool-done", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", false, 0},
		{"multi-choice-done", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{\"content\":\"partial\"}}]}\n\ndata: [DONE]\n\n", false, 0},
		{"bare-cr", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\r\rdata: [DONE]\r\r", false, 1},
		{"multiline-data", "data: {\"choices\":[\ndata: {\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", false, 1},
		{"comment-heartbeat", ": heartbeat\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", false, 1},
		{"role-flip", "data: {\"choices\":[{\"delta\":{\"role\":\"user\",\"content\":\"x\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"y\"},\"finish_reason\":\"stop\"}]}\n\n", false, 0},
		{"empty-role", "data: {\"choices\":[{\"delta\":{\"role\":\"\",\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", false, 1},
		{"function-request", "data: {\"choices\":[{\"delta\":{\"content\":\"calling\",\"function_call\":{\"name\":\"fixture\"}}}]}\n\ndata: [DONE]\n\n", false, 0},
		{"empty-finish-eof", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"\"}]}\n\n", false, 0},
		{"tool-finish-only", "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"tool_calls\"}]}\n", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &modelChatRoundTripper{stream: tc.stream, readErr: tc.readErr}
			c := upstream.New()
			c.HTTP = &http.Client{Transport: rt}
			c.ChatHTTP = &http.Client{Transport: rt}
			c.ChatBaseCN = "http://fixture"
			c.BillingBaseCN = "http://fixture"
			c.WebBaseCN = "http://fixture"
			msg, err := runModelChat(New(Config{Upstream: c}), &auth.Auth{UID: "fixture", AccessToken: "fixture"})
			if rt.reports != tc.wantReport {
				t.Fatalf("reports=%d want %d msg=%q err=%v", rt.reports, tc.wantReport, msg, err)
			}
			if tc.wantReport == 0 {
				if err == nil || strings.Contains(msg, "已完成") || strings.Contains(err.Error(), "fixture-private-read-error-token") {
					t.Fatalf("invalid stream claimed completion: msg=%q err=%v", msg, err)
				}
			}
			if tc.wantReport == 1 && err != nil {
				t.Fatalf("valid stream err=%v", err)
			}
		})
	}
}

func TestRunModelChatPreservesChatRequest(t *testing.T) {
	oldGap := reportGap
	reportGap = 0
	t.Cleanup(func() { reportGap = oldGap })
	rt := &modelChatRoundTripper{stream: "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"}
	c := upstream.New()
	c.HTTP = &http.Client{Transport: rt}
	c.ChatHTTP = &http.Client{Transport: rt}
	c.ChatBaseCN = "http://fixture"
	c.BillingBaseCN = "http://fixture"
	c.WebBaseCN = "http://fixture"
	if _, err := runModelChat(New(Config{Upstream: c}), &auth.Auth{UID: "fixture", AccessToken: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rt.chatBody, []byte(`"model":"glm-5.2"`)) || !bytes.Contains(rt.chatBody, []byte(`"stream":true`)) {
		t.Fatalf("chat body changed: %s", rt.chatBody)
	}
}

func TestRunModelChatCompleteRequestContract(t *testing.T) {
	old := reportGap
	reportGap = 0
	t.Cleanup(func() { reportGap = old })
	rt := &modelChatRoundTripper{stream: "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"}
	c := upstream.New()
	c.HTTP = &http.Client{Transport: rt}
	c.ChatHTTP = c.HTTP
	c.ChatBaseCN = "https://fixture.invalid"
	c.BillingBaseCN = "https://fixture.invalid"
	c.WebBaseCN = "https://fixture.invalid"
	c.ClientName = "fixture-product"
	if _, e := runModelChat(New(Config{Upstream: c}), &auth.Auth{UID: "fixture", AccessToken: "fixture"}); e != nil {
		t.Fatal(e)
	}
	if rt.chatMethod != "POST" || rt.chatPath != "/v2/chat/completions" {
		t.Fatal("wrong endpoint")
	}
	for k, want := range map[string]string{"Authorization": "Bearer fixture", "Content-Type": "application/json", "X-Product": "fixture-product", "X-User-Id": "fixture"} {
		if rt.chatHeader.Get(k) != want {
			t.Errorf("header %s mismatch", k)
		}
	}
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(rt.chatBody, &body) != nil || body.Model != "glm-5.2" || !body.Stream || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "hi，请回复一句话" {
		t.Fatal("request body changed")
	}
}

func TestModelChatEmptyFunctionPlaceholderCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, call string
		valid      bool
	}{
		{"empty-fields", `{"name":"","arguments":""}`, true}, {"empty-object", `{}`, true}, {"empty-name", `{"name":""}`, true},
		{"actual-name", `{"name":"tool","arguments":""}`, false}, {"actual-arguments", `{"name":"","arguments":"{}"}`, false},
		{"unknown-key", `{"other":""}`, false}, {"invalid-type", `""`, false}, {"invalid-member", `{"name":0}`, false}, {"null-member", `{"name":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"OK","function_call":null},"finish_reason":""}]}` + "\n\n" +
				`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"","function_call":` + tc.call + `},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
			err := consumeModelChatCompletion(strings.NewReader(stream))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
}
