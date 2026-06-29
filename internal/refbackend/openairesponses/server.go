// Package openairesponses is a reference backend emulator for the OpenAI Responses API.
// It serves POST /v1/responses (or any path suffix /responses) with JSON or SSE bodies
// compatible with github.com/openai/openai-go/v3.
package openairesponses

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/utils"
)

const maxBodyBytes = 10 << 20

// Used with [bytes.Contains] on each POST body — package-level to avoid a per-request []byte allocation.
var jsonBodyMarkerStreamTrue = []byte(`"stream":true`)

// Config tunes the emulator handler.
type Config struct {
	// AllowMissingBearer, if true, skips the Authorization: Bearer check.
	AllowMissingBearer bool
	// OnAuthorizedCredential is invoked after local auth passes with the raw bearer
	// secret (Authorization without the "Bearer " prefix). Do not log this value.
	OnAuthorizedCredential func(secret string)
	// ForcedHTTPStatus, when http.StatusUnauthorized or http.StatusTooManyRequests, returns
	// that status with a provider-shaped JSON error instead of success (stream or JSON).
	ForcedHTTPStatus int
	// ForcedRetryAfter is sent as Retry-After when ForcedHTTPStatus is 429.
	ForcedRetryAfter string
	// ForcedErrorJSON overrides the forced-error JSON body; when empty a minimal default is used.
	ForcedErrorJSON string
	// OnRequestBody is invoked with the full request body after a successful route/auth
	// check and before the response is written.
	OnRequestBody func(body []byte)
	// NonStreamJSON overrides the JSON body for non-streaming responses. When empty, a
	// minimal completed response is returned.
	NonStreamJSON string
	// StreamSSE overrides the full SSE payload for streaming responses. When empty, a
	// minimal response.completed plus [DONE] stream is returned.
	StreamSSE string
}

// NewHandler returns an http.Handler that emulates POST …/responses for the official SDK.
func NewHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
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

		stream := bytes.Contains(body, jsonBodyMarkerStreamTrue)
		if stream {
			writeStream(r.Context(), w, cfg)
			return
		}
		writeJSON(r.Context(), w, cfg)
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

func writeJSON(ctx context.Context, w http.ResponseWriter, cfg Config) {
	body := cfg.NonStreamJSON
	if body == "" {
		body = defaultNonStreamJSON
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, body); err != nil {
		slog.ErrorContext(ctx, "refbackend openairesponses: write json body", "error", err)
	}
}

func writeStream(ctx context.Context, w http.ResponseWriter, cfg Config) {
	body := cfg.StreamSSE
	if body == "" {
		body = defaultStreamSSE
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, body); err != nil {
		slog.ErrorContext(ctx, "refbackend openairesponses: write sse body", "error", err)
	}
}

const defaultNonStreamJSON = `{
  "id": "resp_refbackend_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "ok"}
      ]
    }
  ]
}`

const defaultStreamSSE = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"stream-ok\"}]}]}}\n\n" +
	"data: [DONE]\n\n"
