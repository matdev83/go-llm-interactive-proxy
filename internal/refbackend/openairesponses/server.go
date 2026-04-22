// Package openairesponses is a reference backend emulator for the OpenAI Responses API.
// It serves POST /v1/responses (or any path suffix /responses) with JSON or SSE bodies
// compatible with github.com/openai/openai-go/v3.
package openairesponses

import (
	"io"
	"net/http"
	"strings"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
type Config struct {
	// AllowMissingBearer, if true, skips the Authorization: Bearer check.
	AllowMissingBearer bool
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

		stream := strings.Contains(string(body), `"stream":true`)
		if stream {
			writeStream(w, cfg)
			return
		}
		writeJSON(w, cfg)
	})
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
	"data: {\"type\":\"response.completed\",\"sequence_number\":1," +
	"\"response\":{\"id\":\"resp_refbackend_stream\",\"object\":\"response\"," +
	"\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"gpt-4o-mini\"," +
	"\"output\":[{\"type\":\"message\",\"id\":\"m1\",\"status\":\"completed\"," +
	"\"role\":\"assistant\",\"content\":[" +
	"{\"type\":\"output_text\",\"text\":\"stream-ok\"}]}]}}}\n\n" +
	"data: [DONE]\n\n"
