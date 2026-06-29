// Package openaichat is a reference backend emulator for the OpenAI Chat Completions API.
// It serves POST …/chat/completions with JSON or SSE bodies compatible with
// github.com/openai/openai-go/v3.
package openaichat

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/utils"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
type Config struct {
	// AllowMissingBearer, if true, skips the Authorization: Bearer check.
	AllowMissingBearer bool
	// OnAuthorizedCredential is invoked after local auth passes with the raw bearer
	// secret (Authorization without the "Bearer " prefix). Do not log this value.
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
	// minimal chat.completion is returned.
	NonStreamJSON string
	// StreamSSE overrides the full SSE payload for streaming responses. When empty, a
	// minimal chat.completion.chunk stream ending with [DONE] is returned.
	StreamSSE string
}

// NewHandler returns an http.Handler that emulates POST …/chat/completions for the official SDK.
func NewHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		if !cfg.AllowMissingBearer {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
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

		secret := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
		secret = strings.TrimSpace(secret)
		if cfg.OnAuthorizedCredential != nil {
			cfg.OnAuthorizedCredential(secret)
		}
		if utils.TryWriteForcedHTTPError(w, cfg.ForcedHTTPStatus, cfg.ForcedRetryAfter, cfg.ForcedErrorJSON, defaultForcedErrorJSON) {
			return
		}

		stream := strings.Contains(string(body), `"stream":true`)
		if stream {
			writeStream(w, cfg, body)
			return
		}
		writeJSON(w, cfg)
	})
}

func defaultForcedErrorJSON(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return `{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`
	case http.StatusTooManyRequests:
		return `{"error":{"message":"rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`
	default:
		return `{"error":{"message":"error","type":"invalid_request_error"}}`
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

func writeStream(w http.ResponseWriter, cfg Config, requestBody []byte) {
	if cfg.StreamSSE != "" {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(cfg.StreamSSE))
		return
	}
	body := defaultStreamSSE
	if bytes.Contains(requestBody, []byte(`"include_usage":true`)) {
		body = streamWithUsageSSE
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

const defaultNonStreamJSON = `{
  "id": "chatcmpl_refbackend_1",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
}`

const defaultStreamSSE = "data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

// streamWithUsageSSE is returned when the client sets stream_options.include_usage,
// matching OpenAI's final usage chunk before [DONE].
const streamWithUsageSSE = "data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream-ok\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refbackend_stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7,\"total_tokens\":10}}\n\n" +
	"data: [DONE]\n\n"
