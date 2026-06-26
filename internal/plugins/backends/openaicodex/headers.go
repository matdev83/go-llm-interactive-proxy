package openaicodex

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	codexBetaHeader     = "responses=experimental"
	codexOriginator     = "codex_cli_rs"
	codexVersionHeader  = "0.0.0"
	codexTaskTypeHeader = "standard"
)

func responsesEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/responses") {
		return base
	}
	return base + "/responses"
}

func applyCodexHeaders(req *http.Request, cfg Config, conversationID string) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AccessToken))
	req.Header.Set("OpenAI-Beta", codexBetaHeader)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("version", codexVersionHeader)
	req.Header.Set("originator", codexOriginator)
	req.Header.Set("User-Agent", codexUserAgent())
	req.Header.Set("conversation_id", conversationID)
	req.Header.Set("session_id", conversationID)
	req.Header.Set("Codex-Task-Type", codexTaskTypeHeader)
	if id := strings.TrimSpace(cfg.AccountID); id != "" {
		req.Header.Set("chatgpt-account-id", id)
	}
}

func codexUserAgent() string {
	return fmt.Sprintf("%s/%s (%s; %s)", codexOriginator, codexVersionHeader, runtime.GOOS, runtime.GOARCH)
}

func conversationID(call lipapi.Call, model string) string {
	if id := strings.TrimSpace(call.Session.CorrelationID()); id != "" {
		return id
	}
	if id := strings.TrimSpace(call.ID); id != "" {
		return id
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "codex"
	}
	return "lip-" + model
}
