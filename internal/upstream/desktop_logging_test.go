package upstream

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

func TestDesktopDebugDoesNotLogCredentials(t *testing.T) {
	t.Setenv("WB2A_DEBUG_CHAT", "1")
	const secret = "dummy-upstream-access-token"
	c := &Client{ChatBaseCN: "https://mock.invalid", HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("outbound authentication was lost")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`data: {"id":"cmb-0123456789abcdef0123456789abcdef","content":"` + secret + `"}`))}, nil
	})}}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original; w.Close() }()
	_, _, callErr := c.DesktopChatWithExpert(&auth.Auth{UID: "dummy-user", AccessToken: secret}, "")
	w.Close()
	os.Stdout = original
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if callErr != nil {
		t.Fatal(callErr)
	}
	if strings.Contains(string(out), secret) || strings.Contains(string(out), "Authorization") {
		t.Fatalf("debug output exposes credentials")
	}
}
