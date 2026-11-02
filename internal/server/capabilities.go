package server

import "net/http"

func (h *Handler) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"chat_completions": true, "streaming": true,
		"responses": false, "anthropic_messages": false,
		"search_messages_only":  "/v1/search/messages",
		"upstream_support":      "legacy_unverified",
		"real_account_verified": false,
	})
}

func (h *Handler) unsupportedProtocol(w http.ResponseWriter, r *http.Request) {
	writeOpenAIError(w, http.StatusNotImplemented, "unsupported_protocol", "this gateway supports /v1/chat/completions; this protocol is not implemented")
}
