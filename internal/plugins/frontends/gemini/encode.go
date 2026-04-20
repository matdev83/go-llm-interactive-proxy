package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EncodeOptions controls optional encoding tweaks.
type EncodeOptions struct{}

// WriteNonStreamJSON encodes a completed canonical stream as a generateContent JSON body.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, _ EncodeOptions) error {
	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		return err
	}
	text := col.Text.String()
	resp := buildGenerateContentResponse(text)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

// WriteStreamSSE drains the canonical stream and emits Gemini stream chunks (data: JSON lines).
// The official genai client parses SSE blocks separated by blank lines; each block should include a `data:` line.
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, _ EncodeOptions) error {
	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		return err
	}
	text := col.Text.String()
	resp := buildGenerateContentResponse(text)
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("gemini: ResponseWriter is not a Flusher")
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	fl.Flush()
	return nil
}

func buildGenerateContentResponse(text string) map[string]any {
	return map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role": "model",
					"parts": []any{
						map[string]any{"text": text},
					},
				},
			},
		},
	}
}
