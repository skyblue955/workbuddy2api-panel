package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

const modelChatStreamLimit = 1 << 20

func consumeModelChatCompletion(r io.Reader) error {
	b, err := io.ReadAll(io.LimitReader(r, modelChatStreamLimit+1))
	if err != nil {
		return fmt.Errorf("stream read failed")
	}
	if len(b) > modelChatStreamLimit {
		return fmt.Errorf("stream exceeds %d bytes", modelChatStreamLimit)
	}
	var sawDone, sawEvent, sawFinish bool
	var canonical strings.Builder
	// SSE joins data lines within an event and accepts LF, CRLF, or CR.
	text := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n")
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.TrimSpace(strings.Join(data, "\n"))
		data = nil
		if payload == "" {
			return nil
		}
		if sawDone {
			return fmt.Errorf("SSE data after DONE")
		}
		if payload == "[DONE]" {
			sawDone = true
			canonical.WriteString("data: [DONE]\n\n")
			return nil
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil || chunk == nil {
			return fmt.Errorf("malformed SSE data")
		}
		if _, exists := chunk["error"]; exists {
			return fmt.Errorf("SSE error event")
		}
		sawEvent = true
		if choices, ok := chunk["choices"].([]any); ok {
			// This task requests one answer (n defaults to 1), never teams of
			// alternative choices. Do not merge an unexpected partial choice.
			if len(choices) > 1 {
				return fmt.Errorf("unexpected multiple choices")
			}
			for _, raw := range choices {
				choice, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid choice")
				}
				for _, field := range []string{"delta", "message"} {
					if node, exists := choice[field]; exists && node != nil {
						part, ok := node.(map[string]any)
						if !ok {
							return fmt.Errorf("invalid assistant message")
						}
						if rawRole, exists := part["role"]; exists && rawRole != nil {
							role, ok := rawRole.(string)
							if !ok || (role != "" && role != "assistant") {
								return fmt.Errorf("non-assistant stream role")
							}
						}
						if call, exists := part["function_call"]; exists && call != nil && !emptyModelChatFunctionPlaceholder(call) {
							return fmt.Errorf("completion requires function execution")
						}
						if tools, ok := part["tool_calls"].([]any); ok && len(tools) > 0 {
							return fmt.Errorf("completion requires tool execution")
						}
					}
				}
				if index, exists := choice["index"]; exists {
					if v, ok := index.(float64); !ok || v != 0 {
						return fmt.Errorf("unexpected choice index")
					}
				}
				if reason, exists := choice["finish_reason"]; exists && reason != nil {
					v, ok := reason.(string)
					if !ok || (v != "" && !validModelChatFinishReason(v)) {
						return fmt.Errorf("SSE did not finish a plain-text answer")
					}
					if v != "" {
						sawFinish = true
					}
				}
			}
		}
		compact, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("invalid SSE data")
		}
		canonical.WriteString("data: ")
		canonical.Write(compact)
		canonical.WriteString("\n\n")
		return nil
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if !hasColon {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		if field == "event" && strings.TrimSpace(value) == "error" {
			return fmt.Errorf("SSE error event")
		}
		if field == "data" {
			data = append(data, value)
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if !sawEvent || (!sawDone && !sawFinish) {
		return fmt.Errorf("incomplete SSE stream")
	}
	completion, err := upstream.Aggregate(strings.NewReader(canonical.String()))
	if err != nil {
		return fmt.Errorf("aggregate stream: %w", err)
	}
	choices, _ := completion["choices"].([]any)
	if len(choices) == 0 {
		return fmt.Errorf("completion has no choices")
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if role, _ := message["role"].(string); role != "assistant" {
		return fmt.Errorf("completion role is not assistant")
	}
	if tools, ok := message["tool_calls"].([]any); ok && len(tools) > 0 {
		return fmt.Errorf("completion requires tool execution")
	}
	content, _ := message["content"].(string)
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("completion has no assistant content")
	}
	return nil
}

func validModelChatFinishReason(reason string) bool {
	switch reason {
	case "stop", "length":
		return true
	default:
		return false
	}
}

// Some domestic streams serialize an unused function_call on the final text
// frame. Only the known all-empty placeholder is noise; never accept actual
// tool/function instructions, unknown fields, or malformed values here.
func emptyModelChatFunctionPlaceholder(value any) bool {
	fields, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, raw := range fields {
		if key != "name" && key != "arguments" {
			return false
		}
		v, ok := raw.(string)
		if !ok || v != "" {
			return false
		}
	}
	return true
}
