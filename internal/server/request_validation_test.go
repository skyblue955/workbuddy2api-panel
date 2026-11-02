package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestChatValidationRejectsInvalidBodiesBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	up := &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sseOK))}, nil
	})}, ChatBaseCN: "https://fake.example"}
	p := testPoolWith(&auth.Auth{UID: "validation", AccessToken: "fixture", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, PromptMode: "passthrough"})

	for _, body := range []string{
		`{"model":"m","messages":[]} trailing`,
		`null`, `[]`,
		`{"messages":[]}`, `{"model":"  ","messages":[]}`,
		`{"model":7,"messages":[]}`, `{"model":"m"}`,
		`{"model":"m","messages":null}`, `{"model":"m","messages":"text"}`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status=%d, want 400", body, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("body %q: response=%s, want invalid_request", body, rec.Body.String())
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("invalid requests called upstream %d times", got)
	}
}

func TestChatValidationPreservesValidUnknownToolAndMultimodalJSON(t *testing.T) {
	requestBody := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"vendor_extension":{"keep":[1,true,null]}}`
	var got []byte
	up := &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sseOK))}, nil
	})}, ChatBaseCN: "https://fake.example"}
	p := testPoolWith(&auth.Auth{UID: "validation", AccessToken: "fixture", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, PromptMode: "passthrough"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var forwarded map[string]any
	if err := json.Unmarshal(got, &forwarded); err != nil {
		t.Fatalf("upstream body is not JSON: %v", err)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(requestBody), &want); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "messages", "tools", "vendor_extension"} {
		if !jsonEqual(forwarded[key], want[key]) {
			t.Errorf("forwarded %s changed:\n got: %#v\nwant: %#v", key, forwarded[key], want[key])
		}
	}
}

func jsonEqual(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
