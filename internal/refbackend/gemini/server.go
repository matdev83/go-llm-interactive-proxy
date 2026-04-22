// Package gemini is a reference backend emulator for the Google Gemini generateContent API.
// It serves POST …:generateContent and …:streamGenerateContent?alt=sse with JSON or SSE
// bodies compatible with google.golang.org/genai (Google AI / ML dev backend).
package gemini

import (
	"io"
	"net/http"
	"strings"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
type Config struct {
	// AllowMissingAPIKey, if true, skips the x-goog-api-key header check.
	AllowMissingAPIKey bool
	// OnRequestBody is invoked with the full request body after a successful route/auth
	// check and before the response is written.
	OnRequestBody func(body []byte)
	// NonStreamJSON overrides the JSON body for non-streaming responses. When empty, a
	// minimal completed candidate is returned.
	NonStreamJSON string
	// StreamSSE overrides the full SSE payload for streaming responses. When empty, a
	// minimal single data chunk is returned (double-newline terminated for the genai client).
	StreamSSE string
}

// NewHandler returns an http.Handler that emulates generateContent / streamGenerateContent
// for the official genai SDK (API key backend).
func NewHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		path := r.URL.Path
		switch {
		case strings.Contains(path, "streamGenerateContent"):
			if !routeAuthAndBody(w, r, cfg) {
				return
			}
			writeStream(w, cfg)
		case strings.Contains(path, ":generateContent"):
			if !routeAuthAndBody(w, r, cfg) {
				return
			}
			writeJSON(w, cfg)
		default:
			http.NotFound(w, r)
		}
	})
}

func routeAuthAndBody(w http.ResponseWriter, r *http.Request, cfg Config) bool {
	if !cfg.AllowMissingAPIKey {
		if r.Header.Get("x-goog-api-key") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return false
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return false
	}
	if cfg.OnRequestBody != nil {
		cfg.OnRequestBody(body)
	}
	return true
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
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "ok"}]
      }
    }
  ]
}`

const defaultStreamSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"stream-ok\"}]}}]}\n\n"
