// Package anthropicmessages is a reference backend emulator for the Anthropic Messages API.
// It serves POST /v1/messages with JSON or SSE bodies compatible with
// github.com/anthropics/anthropic-sdk-go.
package anthropicmessages

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/utils"
	"io"
	"net/http"
	"strings"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
type Config struct {
	// AllowMissingAPIKey, if true, skips the x-api-key header check.
	AllowMissingAPIKey bool
	// OnAuthorizedCredential is invoked after local auth passes with the raw x-api-key value.
	// Do not log this value.
	OnAuthorizedCredential func(secret string)
	// ForcedHTTPStatus, when 401 or 429, returns that status with provider-shaped JSON instead of success.
	ForcedHTTPStatus int
	// ForcedRetryAfter is sent as Retry-After when ForcedHTTPStatus is 429.
	ForcedRetryAfter string
	// ForcedErrorJSON overrides the forced-error JSON body; when empty a minimal default is used.
	ForcedErrorJSON string
	// OnRequestBody is invoked with the full request body after a successful route/auth
	// check and before the response is written.
	OnRequestBody func(body []byte)
	// NonStreamJSON overrides the JSON body for non-streaming responses. When empty, a
	// minimal completed message is returned.
	NonStreamJSON string
	// StreamSSE overrides the full SSE payload for streaming responses. When empty, a
	// minimal message_start plus message_stop stream is returned.
	StreamSSE string
}

// NewHandler returns an http.Handler that emulates POST …/v1/messages for the official SDK.
func NewHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		if !cfg.AllowMissingAPIKey {
			if r.Header.Get("x-api-key") == "" {
				http.Error(w, "missing api key", http.StatusUnauthorized)
				return
			}
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if cfg.OnRequestBody != nil {
			cfg.OnRequestBody(body)
		}

		key := strings.TrimSpace(r.Header.Get("x-api-key"))
		if cfg.OnAuthorizedCredential != nil {
			cfg.OnAuthorizedCredential(key)
		}
		if utils.TryWriteForcedHTTPError(w, cfg.ForcedHTTPStatus, cfg.ForcedRetryAfter, cfg.ForcedErrorJSON, defaultForcedErrorJSON) {
			return
		}

		stream := strings.Contains(string(body), `"stream":true`)
		if stream {
			writeStream(w, cfg)
			return
		}
		writeJSON(w, cfg)
	})
}

func defaultForcedErrorJSON(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	case http.StatusTooManyRequests:
		return `{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`
	default:
		return `{"type":"error","error":{"type":"api_error","message":"error"}}`
	}
}

func writeJSON(w http.ResponseWriter, cfg Config) {
	body := cfg.NonStreamJSON
	if body == "" {
		body = defaultNonStreamJSON
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func writeStream(w http.ResponseWriter, cfg Config) {
	body := cfg.StreamSSE
	if body == "" {
		body = defaultStreamSSE
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

const defaultNonStreamJSON = `{
  "id": "msg_refbackend_1",
  "type": "message",
  "role": "assistant",
  "model": "claude-3-5-haiku-20241022",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`

const defaultStreamSSE = "event: message_start\ndata: " +
	`{"type":"message_start","message":{"id":"msg_refbackend_stream","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
	"\n\n" +
	"event: message_stop\ndata: " +
	`{"type":"message_stop"}` +
	"\n\n"
