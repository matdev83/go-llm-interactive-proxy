package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EncodeOptions controls wire identifiers for encoded Messages payloads.
type EncodeOptions struct {
	MessageID string
}

type wireAPIError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type wireMessage struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Model        string             `json:"model"`
	Content      []wireContentBlock `json:"content"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        wireUsage          `json:"usage"`
}

type wireContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// WriteErrorJSON writes an Anthropic-shaped JSON error before any streamed bytes.
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var we wireAPIError
	we.Type = "error"
	we.Error.Type = errType
	if we.Error.Type == "" {
		we.Error.Type = "invalid_request_error"
	}
	we.Error.Message = message
	_ = json.NewEncoder(w).Encode(we)
}

// WriteNonStreamJSON encodes a completed canonical stream as a Messages API JSON body.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		return err
	}
	text := col.Text.String()
	model := ModelFromCall(call)
	if model == "" {
		model = "claude-3-5-haiku-20241022"
	}
	mid := opts.MessageID
	if mid == "" {
		mid = "msg_" + time.Now().UTC().Format("20060102150405")
	}
	out := wireMessage{
		ID:         mid,
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		StopReason: "end_turn",
		Content: []wireContentBlock{{
			Type: "text",
			Text: text,
		}},
		Usage: wireUsage{
			InputTokens:  col.InputTokens,
			OutputTokens: col.OutputTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(out)
}

func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	defer func() { _ = es.Close() }()
	model := ModelFromCall(call)
	if model == "" {
		model = "claude-3-5-haiku-20241022"
	}
	mid := opts.MessageID
	if mid == "" {
		mid = "msg_" + time.Now().UTC().Format("20060102150405")
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("anthropic: ResponseWriter is not a Flusher")
	}

	var inTok, outTok int
	var started bool

	flushStart := func() error {
		if started {
			return nil
		}
		started = true
		startPayload := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            mid,
				"type":          "message",
				"role":          "assistant",
				"content":       []any{},
				"model":         model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]int{
					"input_tokens":  inTok,
					"output_tokens": 0,
				},
			},
		}
		if err := writeSSEEvent(w, fl, "message_start", startPayload); err != nil {
			return err
		}
		cbStart := map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		}
		return writeSSEEvent(w, fl, "content_block_start", cbStart)
	}

	for {
		ev, err := es.Recv(ctx)
		if err == io.EOF {
			return fmt.Errorf("anthropic: stream ended without response_finished")
		}
		if err != nil {
			return err
		}
		switch ev.Kind {
		case lipapi.EventResponseStarted:
		case lipapi.EventMessageStarted:
		case lipapi.EventUsageDelta:
			inTok += ev.InputTokens
			outTok += ev.OutputTokens
		case lipapi.EventTextDelta:
			if !started {
				if err := flushStart(); err != nil {
					return err
				}
			}
			delta := map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": ev.Delta,
				},
			}
			if err := writeSSEEvent(w, fl, "content_block_delta", delta); err != nil {
				return err
			}
		case lipapi.EventResponseFinished:
			if !started {
				if err := flushStart(); err != nil {
					return err
				}
			}
			cbStop := map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			}
			if err := writeSSEEvent(w, fl, "content_block_stop", cbStop); err != nil {
				return err
			}
			msgDelta := map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   "end_turn",
					"stop_sequence": nil,
				},
				"usage": map[string]int{
					"output_tokens": outTok,
				},
			}
			if err := writeSSEEvent(w, fl, "message_delta", msgDelta); err != nil {
				return err
			}
			stopPayload := map[string]any{"type": "message_stop"}
			if err := writeSSEEvent(w, fl, "message_stop", stopPayload); err != nil {
				return err
			}
			return nil
		case lipapi.EventError:
			return fmt.Errorf("anthropic stream error: %s: %s", ev.ErrorCode, ev.ErrorMessage)
		case lipapi.EventWarning, lipapi.EventReasoningDelta, lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished:
		default:
		}
	}
}

func writeSSEEvent(w io.Writer, fl http.Flusher, event string, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	fl.Flush()
	return nil
}
