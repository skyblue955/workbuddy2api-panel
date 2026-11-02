package workbuddyopen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthorizeURLAndState(t *testing.T) {
	state, err := NewState()
	if err != nil || len(state) != 64 || !VerifyState(state, state) || VerifyState(state, state+"x") || VerifyState("", "") || VerifyState(" ", state) || VerifyState(state, " ") {
		t.Fatalf("state=%q err=%v", state, err)
	}
	c := NewClient()
	got, err := c.AuthorizeURL("client id", "http://127.0.0.1:4312/cb", "openid profile", "a&b")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	if u.String() == "" || q.Get("response_type") != "code" || q.Get("client_id") != "client id" || q.Get("redirect_uri") != "http://127.0.0.1:4312/cb" || q.Get("scope") != "openid profile" || q.Get("state") != "a&b" {
		t.Fatalf("authorize URL=%s", got)
	}
	if u.Scheme != "https" || u.Host != "www.workbuddy.cn" || u.Path != "/openapi/v2/authorize" {
		t.Fatalf("unexpected authorize endpoint: %s", got)
	}
}

func TestOAuthRejectsInvalidRedirectAndState(t *testing.T) {
	c := NewClient()
	for _, redirect := range []string{"", "http://example.com/cb", "javascript:alert(1)", "http://[::2]", "https://client.example/cb#fragment"} {
		if _, err := c.AuthorizeURL("client", redirect, "openid", "state"); err == nil {
			t.Errorf("redirect %q accepted", redirect)
		}
	}
	for _, clientID := range []string{"", "  "} {
		if _, err := c.AuthorizeURL(clientID, "https://client.example/cb", "openid", "state"); err == nil {
			t.Errorf("client id %q accepted", clientID)
		}
	}
	if _, err := c.AuthorizeURL("client", "https://client.example/cb", "openid", ""); err == nil {
		t.Error("empty state accepted")
	}
}

func TestOAuthTokenFormsAndValidation(t *testing.T) {
	var forms []url.Values
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != OAuthTokenURL || r.Method != http.MethodPost {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		forms = append(forms, form)
		return response(200, `{"access_token":"access","token_type":"Bearer","expires_in":3600,"scope":"openid","open_id":"id"}`, nil), nil
	})}}
	first, err := c.ExchangeCode(context.Background(), "id", "secret", "code+value", "https://client.example/cb")
	if err != nil || first.AccessToken != "access" || first.ExpiresIn != 3600 {
		t.Fatalf("exchange=%+v err=%v", first, err)
	}
	second, err := c.RefreshToken(context.Background(), "id", "secret", "refresh")
	if err != nil || second.RefreshToken != "" {
		t.Fatalf("refresh=%+v err=%v", second, err)
	}
	if len(forms) != 2 || forms[0].Get("grant_type") != "authorization_code" || forms[0].Get("code") != "code+value" || forms[0].Get("redirect_uri") == "" || forms[1].Get("grant_type") != "refresh_token" || forms[1].Get("refresh_token") != "refresh" {
		t.Fatalf("forms=%v", forms)
	}
}

func TestOAuthErrorsRedactedAndNoRedirect(t *testing.T) {
	for _, status := range []int{302, 400, 500} {
		c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(status, `{"secret":"do-not-leak"}`, http.Header{"Location": []string{"https://other.example"}}), nil
		})}}
		_, err := c.ExchangeCode(context.Background(), "id", "secret", "code", "https://client.example/cb")
		if err == nil || !strings.Contains(err.Error(), "HTTP "+http.StatusText(status)) && status != 302 && status != 400 && status != 500 || strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "secret") {
			t.Errorf("status=%d err=%v", status, err)
		}
	}
}

func TestOAuthTimeoutPreservesContextError(t *testing.T) {
	c := &Client{Timeout: time.Millisecond, HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		<-time.After(100 * time.Millisecond)
		return nil, errors.New("late")
	})}}
	_, err := c.RefreshToken(context.Background(), "id", "secret", "refresh")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}
