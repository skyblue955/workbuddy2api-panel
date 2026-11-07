package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const claimEnvelopeMaxBody = 1 << 20

// doClaimJSON applies strict envelope validation only to reward claims. The
// shallow http.Client copy keeps redirect policy local to this request and
// leaves the shared client (and its transport) untouched.
func (c *Client) doClaimJSON(req *http.Request) (json.RawMessage, error) {
	if c.HTTP == nil {
		return nil, fmt.Errorf("claim request has no http client")
	}
	hc := *c.HTTP
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, claimEnvelopeMaxBody))
		if readErr != nil {
			return nil, fmt.Errorf("read body: %w", readErr)
		}
		kind := Classify(resp.StatusCode, string(raw))
		return nil, &Error{Kind: kind, Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("claim response status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, claimEnvelopeMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(raw) > claimEnvelopeMaxBody {
		return nil, fmt.Errorf("claim response too large")
	}
	code, msg, data, err := parseClaimEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		kind := Classify(resp.StatusCode, msg)
		if kind == ErrNone {
			kind = ErrClient
		}
		return nil, &Error{Kind: kind, Status: resp.StatusCode, Msg: fmt.Sprintf("code=%d", code)}
	}
	return data, nil
}

func parseClaimEnvelope(raw []byte) (int, string, json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return 0, "", nil, fmt.Errorf("parse claim envelope: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0, "", nil, fmt.Errorf("claim envelope must be an object")
	}
	obj := make(map[string]json.RawMessage)
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return 0, "", nil, fmt.Errorf("parse claim envelope: %w", err)
		}
		key, ok := kt.(string)
		if !ok {
			return 0, "", nil, fmt.Errorf("invalid claim envelope key")
		}
		if _, exists := obj[key]; exists {
			return 0, "", nil, fmt.Errorf("duplicate claim envelope key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return 0, "", nil, fmt.Errorf("parse claim envelope: %w", err)
		}
		obj[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return 0, "", nil, fmt.Errorf("parse claim envelope: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return 0, "", nil, fmt.Errorf("trailing claim envelope data")
	}
	codeRaw, ok := obj["code"]
	if !ok || bytes.Equal(bytes.TrimSpace(codeRaw), []byte("null")) {
		return 0, "", nil, fmt.Errorf("claim envelope code is required")
	}
	var code int
	if err := json.Unmarshal(codeRaw, &code); err != nil {
		return 0, "", nil, fmt.Errorf("claim envelope code must be an integer")
	}
	msg := ""
	if msgRaw, ok := obj["msg"]; ok {
		if err := json.Unmarshal(msgRaw, &msg); err != nil {
			return 0, "", nil, fmt.Errorf("claim envelope msg must be a string")
		}
	}
	data, ok := obj["data"]
	if !ok {
		data = nil
	}
	return code, msg, data, nil
}
