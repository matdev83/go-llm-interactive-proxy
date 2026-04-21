package acp

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func messageIDForCall(call *lipapi.Call) string {
	if call == nil || call.Extensions == nil {
		return "msg_" + diag.StableCallToken(call)
	}
	raw, ok := call.Extensions[extMessageIDJSONKey]
	if !ok || len(raw) == 0 {
		return "msg_" + diag.StableCallToken(call)
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
		return "msg_" + diag.StableCallToken(call)
	}
	return strings.TrimSpace(s)
}

func buildPromptParams(sessionID string, blocks []map[string]any, messageID string) map[string]any {
	p := map[string]any{
		"sessionId": sessionID,
		"prompt":    blocks,
	}
	if strings.TrimSpace(messageID) != "" {
		p["messageId"] = messageID
	}
	return p
}
