// Package workbuddyopen contains opt-in connectors for the official WorkBuddy Open API.
package workbuddyopen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	RedemptionsURL  = "https://www.workbuddy.cn/openapi/v2/redemptions"
	maxResponseBody = 1 << 20
	defaultTimeout  = 30 * time.Second
)

// Client calls the official redemption endpoint. The access token is supplied
// to Redeem and is never retained by the client.
type Client struct {
	HTTP    *http.Client
	Timeout time.Duration
}

// NewClient returns a client with the official timeout and transport defaults.
func NewClient() *Client { return &Client{Timeout: defaultTimeout} }

// RedemptionRequest is either a single code or a gift-key/gift-code pair.
type RedemptionRequest struct {
	Code      string `json:"code,omitempty"`
	GiftKey   string `json:"gift_key,omitempty"`
	GiftCode  string `json:"gift_code,omitempty"`
	RequestID string `json:"request_id"`
}

// RedemptionResponse is returned after the API confirms redemption.
type RedemptionResponse struct {
	Status  string `json:"status"`
	FlowNo  string `json:"flow_no"`
	Credits int64  `json:"credits"`
	OpenID  string `json:"open_id"`
}

// Redeem submits one redemption request. It performs no automatic retries.
func (c *Client) Redeem(ctx context.Context, accessToken string, input RedemptionRequest) (*RedemptionResponse, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("redemption: access token is required")
	}
	if err := validateRequest(input); err != nil {
		return nil, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("redemption: encode request: %w", err)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, RedemptionsURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("redemption: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := clientCopy.Do(req)
	if err != nil {
		if requestErr := requestCtx.Err(); requestErr != nil {
			return nil, requestErr
		}
		return nil, errors.New("redemption: request failed")
	}
	if resp.Body == nil {
		return nil, errors.New("redemption: response body is missing")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("redemption: upstream returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		if requestErr := requestCtx.Err(); requestErr != nil {
			return nil, requestErr
		}
		return nil, errors.New("redemption: response unreadable")
	}
	if len(raw) > maxResponseBody {
		return nil, errors.New("redemption: response body exceeds 1 MiB")
	}
	var wire struct {
		Status  string `json:"status"`
		FlowNo  string `json:"flow_no"`
		Credits *int64 `json:"credits"`
		OpenID  string `json:"open_id"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, errors.New("redemption: malformed response")
	}
	if wire.Status != "success" || strings.TrimSpace(wire.FlowNo) == "" || strings.TrimSpace(wire.OpenID) == "" || wire.Credits == nil || *wire.Credits < 0 {
		return nil, errors.New("redemption: response does not confirm success")
	}
	out := &RedemptionResponse{Status: wire.Status, FlowNo: wire.FlowNo, Credits: *wire.Credits, OpenID: wire.OpenID}
	return out, nil
}

func validateRequest(in RedemptionRequest) error {
	if strings.TrimSpace(in.RequestID) == "" || !validLen(in.RequestID, 8, 64) {
		return errors.New("redemption: request_id must be 8-64 characters")
	}
	single := in.Code != ""
	dual := in.GiftKey != "" || in.GiftCode != ""
	if single == dual {
		return errors.New("redemption: specify code or gift_key and gift_code")
	}
	if single && !validLen(in.Code, 1, 64) {
		return errors.New("redemption: code must be 1-64 characters")
	}
	if dual && (!validLen(in.GiftKey, 6, 64) || !validLen(in.GiftCode, 8, 64)) {
		return errors.New("redemption: gift_key/gift_code length is invalid")
	}
	if dual && (in.GiftKey == "" || in.GiftCode == "") {
		return errors.New("redemption: gift_key and gift_code are required together")
	}
	return nil
}

func validLen(s string, min, max int) bool {
	n := utf8.RuneCountInString(s)
	return n >= min && n <= max
}
