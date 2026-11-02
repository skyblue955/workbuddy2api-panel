package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCapabilitiesAuthAndUnsupportedProtocols(t *testing.T) {
	h := NewHandler(Config{APIKey: "fixture"})
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/v1/capabilities", 200}, {"POST", "/v1/responses", 501}, {"POST", "/v1/messages", 501},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unauthorized %s: %d", tc.path, w.Code)
		}
		r = httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer fixture")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}

func TestOversizeChatRejectedBeforeAccountSelection(t *testing.T) {
	h := NewHandler(Config{MaxRequestBodyBytes: 32}) // nil pool would panic if routing ran.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(strings.Repeat("x", 33))))
	if w.Code != 413 || !strings.Contains(w.Body.String(), "request_too_large") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
