package upstream

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSETerminalConditions(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		truncated bool
	}{
		{"content_eof", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", true},
		{"one_choice_unfinished", "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{\"content\":\"partial\"}}]}\n\n", true},
		{"all_choices_finished", "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n", false},
		{"explicit_done", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Aggregate(strings.NewReader(tc.raw))
			if IsTruncatedStreamError(err) != tc.truncated {
				t.Fatalf("aggregate truncated=%v err=%v", tc.truncated, err)
			}
			rec := httptest.NewRecorder()
			err = Stream(rec, strings.NewReader(tc.raw))
			if IsTruncatedStreamError(err) != tc.truncated {
				t.Fatalf("stream truncated=%v err=%v", tc.truncated, err)
			}
			if tc.truncated && strings.Contains(rec.Body.String(), "data: [DONE]") {
				t.Fatal("truncated stream synthesized DONE")
			}
			if !tc.truncated && strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
				t.Fatal("completed stream missing uniqueDONE")
			}
		})
	}
}

func TestAggregateRejectsErrorEventInsteadOfEmptySuccess(t *testing.T) {
	for _, prefix := range []string{"", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"} {
		got, err := Aggregate(strings.NewReader(prefix + "data: {\"error\":{\"message\":\"fixture upstream failure\"}}\n\ndata: [DONE]\n\n"))
		if err == nil || got != nil {
			t.Fatalf("error event became a successful response: got=%v err=%v", got, err)
		}
	}
}
