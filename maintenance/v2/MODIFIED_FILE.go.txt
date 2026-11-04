package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// validateChatRequest checks the fields required to route a chat request while
// leaving the original JSON untouched for the upstream client. Unknown fields
// and nested values are deliberately not decoded into a restrictive struct.
func validateChatRequest(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	var request map[string]json.RawMessage
	if err := dec.Decode(&request); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if request == nil {
		return fmt.Errorf("request body must be a JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain a single JSON value")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}

	model, ok := request["model"]
	if !ok {
		return fmt.Errorf("model is required")
	}
	var modelValue string
	if err := json.Unmarshal(model, &modelValue); err != nil || strings.TrimSpace(modelValue) == "" {
		return fmt.Errorf("model must be a non-empty string")
	}

	if raw, exists := request["stream"]; exists {
		value := strings.TrimSpace(string(raw))
		if value != "true" && value != "false" {
			return fmt.Errorf("stream must be a boolean")
		}
	}

	messages, ok := request["messages"]
	if !ok {
		return fmt.Errorf("messages is required")
	}
	var messageItems []json.RawMessage
	if err := json.Unmarshal(messages, &messageItems); err != nil || messageItems == nil {
		return fmt.Errorf("messages must be an array")
	}
	for _, item := range messageItems {
		var object map[string]json.RawMessage
		if len(item) == 0 || string(item) == "null" || json.Unmarshal(item, &object) != nil || object == nil {
			return fmt.Errorf("messages entries must be objects")
		}
	}
	return nil
}
