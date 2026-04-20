package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// WriteStreamSSE emits Gemini stream chunks incrementally from the canonical stream.
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, _ EncodeOptions) error {
	defer func() { _ = es.Close() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("gemini: ResponseWriter is not a Flusher")
	}

	var inTok, outTok int

	for {
		ev, err := es.Recv(ctx)
		if err == io.EOF {
			return fmt.Errorf("gemini: stream ended without response_finished")
		}
		if err != nil {
			return err
		}
		switch ev.Kind {
		case lipapi.EventTextDelta:
			chunk := buildGenerateContentResponse(ev.Delta)
			b, err := json.Marshal(chunk)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return err
			}
			fl.Flush()
		case lipapi.EventUsageDelta:
			inTok += ev.InputTokens
			outTok += ev.OutputTokens
		case lipapi.EventResponseFinished:
			if inTok > 0 || outTok > 0 {
				usageFrame := map[string]any{
					"usageMetadata": map[string]any{
						"promptTokenCount":     inTok,
						"candidatesTokenCount": outTok,
						"totalTokenCount":      inTok + outTok,
					},
				}
				b, err := json.Marshal(usageFrame)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
					return err
				}
				fl.Flush()
			}
			return nil
		case lipapi.EventError:
			return fmt.Errorf("gemini stream error: %s: %s", ev.ErrorCode, ev.ErrorMessage)
		case lipapi.EventResponseStarted, lipapi.EventMessageStarted:
		case lipapi.EventWarning, lipapi.EventReasoningDelta, lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished:
		default:
		}
	}
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
