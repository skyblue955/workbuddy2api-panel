package workbuddyopen

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	OAuthAuthorizeURL = "https://www.workbuddy.cn/openapi/v2/authorize"
	OAuthTokenURL     = "https://www.workbuddy.cn/openapi/v2/token"
)

// OAuthToken is the token response returned by the official Open API.
type OAuthToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	OpenID       string `json:"open_id,omitempty"`
}

// NewState returns a cryptographically random OAuth state value.
func NewState() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", errors.New("oauth: generate state")
	}
	return hex.EncodeToString(b), nil
}

// VerifyState compares OAuth state values without an ordinary early-exit
// comparison. Callers should generate and retain the expected value per flow.
func VerifyState(expected, actual string) bool {
	if strings.TrimSpace(expected) == "" || strings.TrimSpace(actual) == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

// AuthorizeURL builds the fixed official authorization endpoint URL.
func (c *Client) AuthorizeURL(clientID, redirectURI, scope, state string) (string, error) {
	if err := validateOAuthInputs(clientID, redirectURI, state); err != nil {
		return "", err
	}
	u, _ := url.Parse(OAuthAuthorizeURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ExchangeCode exchanges one authorization code. Credentials are supplied per
// call and are not retained by Client.
func (c *Client) ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (*OAuthToken, error) {
	if err := validateOAuthInputs(clientID, redirectURI, "state-not-required"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(clientSecret) == "" || strings.TrimSpace(code) == "" {
		return nil, errors.New("oauth: client secret and authorization code are required")
	}
	return c.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}, "client_id": {clientID}, "client_secret": {clientSecret}})
}

// RefreshToken exchanges one refresh token. The rotated refresh token is
// optional in the response, as permitted by the official contract.
func (c *Client) RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*OAuthToken, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" || strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("oauth: client id, client secret and refresh token are required")
	}
	return c.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {clientID}, "client_secret": {clientSecret}})
}

func validateOAuthInputs(clientID, redirectURI, state string) error {
	if strings.TrimSpace(clientID) == "" {
		return errors.New("oauth: client id is required")
	}
	if strings.TrimSpace(state) == "" {
		return errors.New("oauth: state is required")
	}
	u, err := url.Parse(redirectURI)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("oauth: invalid redirect uri")
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(u.Scheme, "http") && isLoopback(u.Hostname()) {
		return nil
	}
	return errors.New("oauth: redirect uri must use https or loopback http")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) tokenRequest(ctx context.Context, values url.Values) (*OAuthToken, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, OAuthTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, errors.New("oauth: create token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	httpClient := c.httpClient()
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := clientCopy.Do(req)
	if err != nil {
		if requestErr := requestCtx.Err(); requestErr != nil {
			return nil, requestErr
		}
		return nil, errors.New("oauth: token request failed")
	}
	if resp.Body == nil {
		return nil, errors.New("oauth: token response body is missing")
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if readErr != nil {
		if requestErr := requestCtx.Err(); requestErr != nil {
			return nil, requestErr
		}
		return nil, errors.New("oauth: token response unreadable")
	}
	if len(raw) > maxResponseBody {
		return nil, errors.New("oauth: token response exceeds 1 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth: token endpoint returned HTTP %d", resp.StatusCode)
	}
	var token OAuthToken
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, errors.New("oauth: malformed token response")
	}
	if strings.TrimSpace(token.AccessToken) == "" || !strings.EqualFold(strings.TrimSpace(token.TokenType), "bearer") || token.ExpiresIn <= 0 {
		return nil, errors.New("oauth: token response is invalid")
	}
	return &token, nil
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
