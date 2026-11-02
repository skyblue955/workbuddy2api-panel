package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/workbuddyopen"
)

type oauthTransport struct {
	calls int
	resp  string
}

func (t *oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(t.resp)), Header: make(http.Header), Request: r}, nil
}

func TestRunAuthorizePersistsStateAndOnlyPrintsURL(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var out strings.Builder
	if err := runAuthorize([]string{"-client-id", "app", "-redirect-uri", "http://127.0.0.1/cb", "-state-file", statePath}, &out, workbuddyopen.NewClient()); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(strings.TrimSpace(out.String()))
	if err != nil || u.Query().Get("scope") != oauthScope || u.Query().Get("client_id") != "app" || u.Query().Get("state") == "" {
		t.Fatalf("authorization URL=%q err=%v", out.String(), err)
	}
	info, _ := os.Stat(statePath)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state mode=%o", info.Mode().Perm())
	}
	if err := runAuthorize([]string{"-client-id", "app", "-redirect-uri", "http://127.0.0.1/cb", "-state-file", statePath}, &strings.Builder{}, workbuddyopen.NewClient()); err == nil {
		t.Fatal("authorize overwrote existing state")
	}
}

func TestRunExchangePersistsTokenAndRemovesState(t *testing.T) {
	dir := t.TempDir()
	statePath, tokenPath := filepath.Join(dir, "state.json"), filepath.Join(dir, "token.json")
	var authOut strings.Builder
	if err := runAuthorize([]string{"-client-id", "app", "-redirect-uri", "http://127.0.0.1/cb", "-state-file", statePath}, &authOut, workbuddyopen.NewClient()); err != nil {
		t.Fatal(err)
	}
	var session oauthSession
	b, _ := os.ReadFile(statePath)
	if err := json.Unmarshal(b, &session); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_OPEN_CLIENT_SECRET", "fixture-secret")
	transport := &oauthTransport{resp: `{"access_token":"fixture-token","token_type":"Bearer","expires_in":3600,"scope":"user.credit.exchange"}`}
	client := &workbuddyopen.Client{HTTP: &http.Client{Transport: transport}}
	var out strings.Builder
	if err := runExchange([]string{"-state-file", statePath, "-token-file", tokenPath}, strings.NewReader(`{"code":"fixture-code","state":"`+session.State+`"}`), &out, client); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 || !strings.Contains(out.String(), "token saved") || strings.Contains(out.String(), "fixture-token") || strings.Contains(out.String(), "fixture-secret") {
		t.Fatalf("calls=%d output=%q", transport.calls, out.String())
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state remains: %v", err)
	}
	if info, err := os.Stat(tokenPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token file=%v info=%v", err, info)
	}
}

func TestRunExchangeRejectsBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	session := oauthSession{ClientID: "app", RedirectURI: "http://127.0.0.1/cb", State: "expected", CreatedAt: time.Now().UTC().Add(-16 * time.Minute)}
	b, _ := json.Marshal(session)
	if err := os.WriteFile(statePath, b, 0600); err != nil {
		t.Fatal(err)
	}
	transport := &oauthTransport{resp: `{}`}
	client := &workbuddyopen.Client{HTTP: &http.Client{Transport: transport}}
	t.Setenv("WORKBUDDY_OPEN_CLIENT_SECRET", "fixture-secret")
	for _, input := range []string{`{"code":"c","state":"wrong"}`, `{"code":"c","state":"expected"}{}`, `{"code":"c","state":"expected","extra":1}`} {
		if err := runExchange([]string{"-state-file", statePath, "-token-file", filepath.Join(dir, "token.json")}, strings.NewReader(input), &strings.Builder{}, client); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if transport.calls != 0 {
		t.Fatalf("network calls=%d", transport.calls)
	}
}

func newOAuthSessionFixture(t *testing.T) (dir, statePath string, session oauthSession) {
	t.Helper()
	dir = t.TempDir()
	statePath = filepath.Join(dir, "state.json")
	var out strings.Builder
	if err := run([]string{"authorize", "-client-id", "app", "-redirect-uri", "http://127.0.0.1/cb", "-state-file", statePath}, strings.NewReader(""), &out, workbuddyopen.NewClient()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &session); err != nil {
		t.Fatal(err)
	}
	return dir, statePath, session
}

func TestRunDispatchesAuthorizeAndExchange(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	var out strings.Builder
	if err := run([]string{"authorize", "-client-id", "app", "-redirect-uri", "http://127.0.0.1/cb", "-state-file", statePath}, strings.NewReader(""), &out, workbuddyopen.NewClient()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), workbuddyopen.OAuthAuthorizeURL+"?") {
		t.Fatalf("dispatch output=%q", out.String())
	}
}

func TestRunExchangeMissingSecretDoesNotCallNetwork(t *testing.T) {
	dir, statePath, session := newOAuthSessionFixture(t)
	t.Setenv("WORKBUDDY_OPEN_CLIENT_SECRET", "")
	transport := &oauthTransport{resp: `{}`}
	client := &workbuddyopen.Client{HTTP: &http.Client{Transport: transport}}
	err := run([]string{"exchange", "-state-file", statePath, "-token-file", filepath.Join(dir, "token.json")}, strings.NewReader(`{"code":"c","state":"`+session.State+`"}`), &strings.Builder{}, client)
	if err == nil || transport.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, transport.calls)
	}
}

func TestRunExchangeFreshWrongStateDoesNotCallNetwork(t *testing.T) {
	dir, statePath, _ := newOAuthSessionFixture(t)
	t.Setenv("WORKBUDDY_OPEN_CLIENT_SECRET", "fixture-secret")
	transport := &oauthTransport{resp: `{}`}
	client := &workbuddyopen.Client{HTTP: &http.Client{Transport: transport}}
	err := run([]string{"exchange", "-state-file", statePath, "-token-file", filepath.Join(dir, "token.json")}, strings.NewReader(`{"code":"c","state":"wrong"}`), &strings.Builder{}, client)
	if err == nil || transport.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, transport.calls)
	}
}

func TestRunExchangeUsedSessionAndExistingDestinationDoNotCallNetwork(t *testing.T) {
	t.Run("used", func(t *testing.T) {
		dir, statePath, session := newOAuthSessionFixture(t)
		if err := os.WriteFile(statePath+".used", []byte(`{"submitted":true}`), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WORKBUDDY_OPEN_CLIENT_SECRET", "fixture-secret")
		transport := &oauthTransport{resp: `{}`}
		client := &workbuddyopen.Client{HTTP: &http.Client{Transport: transport}}
		err := run([]string{"exchange", "-state-file", statePath, "-token-file", filepath.Join(dir, "token.json")}, strings.NewReader(`{"code":"c","state":"`+session.State+`"}`), &strings.Builder{}, client)
		if err == nil || transport.calls != 0 {
			t.Fatalf("err=%v calls=%d", err, transport.calls)
		}
	})
	t.Run("existing destination", func(t *testing.T) {
		dir, statePath, session := newOAuthSessionFixture(t)
		t.Setenv("WORKBUDDY_OPEN_CLIENT_SECRET", "fixture-secret")
		tokenPath := filepath.Join(dir, "token.json")
		if err := os.WriteFile(tokenPath, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		transport := &oauthTransport{resp: `{}`}
		client := &workbuddyopen.Client{HTTP: &http.Client{Transport: transport}}
		err := run([]string{"exchange", "-state-file", statePath, "-token-file", tokenPath}, strings.NewReader(`{"code":"c","state":"`+session.State+`"}`), &strings.Builder{}, client)
		if err == nil || transport.calls != 0 {
			t.Fatalf("err=%v calls=%d", err, transport.calls)
		}
	})
}
