package upstream

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

type desktopFixtureTransport func(*http.Request) (*http.Response, error)

func (f desktopFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func desktopResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    &http.Request{},
	}
}

func TestMarketExpertListRequestContract(t *testing.T) {
	var got *http.Request
	c := &Client{ChatBaseCN: "https://fixture.invalid", HTTP: &http.Client{Transport: desktopFixtureTransport(func(r *http.Request) (*http.Response, error) {
		got = r
		return desktopResponse(200, "application/json", `{"code":0,"data":{"experts":[{"expert_id":"agent-fixture-1","expert_type":"agent","display_name_zh":"测试专家"}]}}`), nil
	})}}
	a := &auth.Auth{UID: "uid-fixture", AccessToken: "token-fixture"}
	experts, err := c.MarketExpertList(a, "agent")
	if err != nil {
		t.Fatalf("MarketExpertList() error = %v", err)
	}
	if len(experts) != 1 || experts[0].ExpertID != "agent-fixture-1" {
		t.Fatalf("unexpected experts: %#v", experts)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/portal/operation-platform/market/expert/list" {
		t.Fatalf("request = %s %s", got.Method, got.URL.Path)
	}
	body, _ := io.ReadAll(got.Body)
	for _, field := range []string{`"page":1`, `"page_size":20`, `"sort_by":"reco_rank"`, `"sort_order":"desc"`, `"expert_type":"agent"`} {
		if !bytes.Contains(body, []byte(field)) {
			t.Errorf("request body missing %s: %s", field, body)
		}
	}
	if got.Header.Get("Authorization") != "Bearer token-fixture" || got.Header.Get("X-User-Id") != "uid-fixture" {
		t.Errorf("auth headers not propagated: authorization=%q uid=%q", got.Header.Get("Authorization"), got.Header.Get("X-User-Id"))
	}
}

func TestDesktopChatWithExpertRequestAndIDOnlySSE(t *testing.T) {
	var got *http.Request
	const serviceID = "cmb-0123456789abcdef0123456789abcdef"
	c := &Client{ChatBaseCN: "https://fixture.invalid", HTTP: &http.Client{Transport: desktopFixtureTransport(func(r *http.Request) (*http.Response, error) {
		got = r
		return desktopResponse(200, "text/event-stream", "data: {\"id\":\""+serviceID+"\"}\n\n"), nil
	})}}
	a := &auth.Auth{UID: "uid-fixture", AccessToken: "token-fixture"}
	conv, requestID, err := c.DesktopChatWithExpert(a, "expert-fixture-1")
	if err != nil {
		t.Fatalf("DesktopChatWithExpert() error = %v", err)
	}
	if conv == "" || requestID != serviceID {
		t.Fatalf("returned conversation/request id = %q/%q", conv, requestID)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/v2/chat/completions" {
		t.Fatalf("request = %s %s", got.Method, got.URL.Path)
	}
	if got.Header.Get("X-Expert-Id") != "expert-fixture-1" || got.Header.Get("Accept") != "text/event-stream" {
		t.Errorf("desktop chat headers = expert %q accept %q", got.Header.Get("X-Expert-Id"), got.Header.Get("Accept"))
	}
	body, _ := io.ReadAll(got.Body)
	for _, field := range []string{`"model":"fast-model"`, `"agent":"cli"`, `"stream":true`, `"include_usage":true`} {
		if !bytes.Contains(body, []byte(field)) {
			t.Errorf("request body missing %s: %s", field, body)
		}
	}
	// The fixture deliberately returns only the service id. This is the current
	// tracking-id contract; it does not claim that a human-readable answer exists.
	t.Logf("id-only SSE accepted as tracking id %s; answer completion is not asserted", requestID)
}
