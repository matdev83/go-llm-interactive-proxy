package openairesponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EncodeOptions controls wire identifiers for encoded Responses payloads.
type EncodeOptions struct {
	ResponseID string
	MessageID  string
	CreatedAt  int64
}

type wireAPIError struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   any     `json:"param"`
		Code    *string `json:"code,omitempty"`
	} `json:"error"`
}

type wireResponse struct {
	ID        string       `json:"id"`
	Object    string       `json:"object"`
	CreatedAt int64        `json:"created_at"`
	Status    string       `json:"status"`
	Model     string       `json:"model"`
	Output    []wireOutput `json:"output"`
}

type wireOutput struct {
	Type    string        `json:"type"`
	ID      string        `json:"id"`
	Status  string        `json:"status"`
	Role    string        `json:"role"`
	Content []wireOutPart `json:"content"`
}

type wireOutPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type wireStreamEnvelope struct {
	Type           string       `json:"type"`
	SequenceNumber int          `json:"sequence_number"`
	Response       wireResponse `json:"response"`
}

// WriteErrorJSON writes an OpenAI-shaped JSON error before any streamed bytes.
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var we wireAPIError
	we.Error.Message = message
	we.Error.Type = errType
	we.Error.Param = nil
	if code != "" {
		we.Error.Code = &code
	}
	_ = json.NewEncoder(w).Encode(we)
}

// WriteNonStreamJSON encodes a completed canonical stream as a non-streaming Responses JSON body.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	resp, err := buildWireResponse(ctx, call, es, opts)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	defer func() { _ = es.Close() }()
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	rid := opts.ResponseID
	if rid == "" {
		rid = "resp_" + time.Now().UTC().Format("20060102150405")
	}
	mid := opts.MessageID
	if mid == "" {
		mid = "msg_" + rid
	}
	ts := opts.CreatedAt
	if ts == 0 {
		ts = time.Now().Unix()
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("openairesponses: ResponseWriter is not a Flusher")
	}

	var seq int
	nextSeq := func() int { seq++; return seq }

	var inTok, outTok int
	var fullText strings.Builder

	writeStreamEvent := func(evName string, payload map[string]any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evName, b); err != nil {
			return err
		}
		fl.Flush()
		return nil
	}

	createdResponse := map[string]any{
		"id":         rid,
		"object":     "response",
		"created_at": ts,
		"status":     "in_progress",
		"model":      model,
		"output":     []any{},
	}
	if err := writeStreamEvent("response.created", map[string]any{
		"type":            "response.created",
		"sequence_number": nextSeq(),
		"response":        createdResponse,
	}); err != nil {
		return err
	}
	if err := writeStreamEvent("response.in_progress", map[string]any{
		"type":            "response.in_progress",
		"sequence_number": nextSeq(),
		"response":        createdResponse,
	}); err != nil {
		return err
	}
	if err := writeStreamEvent("response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": nextSeq(),
		"item": map[string]any{
			"type":    "message",
			"id":      mid,
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	}); err != nil {
		return err
	}
	if err := writeStreamEvent("response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": nextSeq(),
		"part": map[string]any{
			"type": "output_text",
			"text": "",
		},
	}); err != nil {
		return err
	}

	for {
		ev, err := es.Recv(ctx)
		if err == io.EOF {
			return fmt.Errorf("openairesponses: stream ended without response_finished")
		}
		if err != nil {
			return err
		}
		switch ev.Kind {
		case lipapi.EventResponseStarted, lipapi.EventMessageStarted:
		case lipapi.EventUsageDelta:
			inTok += ev.InputTokens
			outTok += ev.OutputTokens
		case lipapi.EventTextDelta:
			fullText.WriteString(ev.Delta)
			if err := writeStreamEvent("response.output_text.delta", map[string]any{
				"type":            "response.output_text.delta",
				"sequence_number": nextSeq(),
				"delta":           ev.Delta,
			}); err != nil {
				return err
			}
		case lipapi.EventResponseFinished:
			text := fullText.String()
			if err := writeStreamEvent("response.output_text.done", map[string]any{
				"type":            "response.output_text.done",
				"sequence_number": nextSeq(),
				"text":            text,
			}); err != nil {
				return err
			}

			completedResp := map[string]any{
				"id":         rid,
				"object":     "response",
				"created_at": ts,
				"status":     "completed",
				"model":      model,
				"output": []any{
					map[string]any{
						"type":   "message",
						"id":     mid,
						"status": "completed",
						"role":   "assistant",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": text,
							},
						},
					},
				},
			}
			if inTok > 0 || outTok > 0 {
				completedResp["usage"] = map[string]any{
					"input_tokens":  inTok,
					"output_tokens": outTok,
				}
			}
			if err := writeStreamEvent("response.completed", map[string]any{
				"type":            "response.completed",
				"sequence_number": nextSeq(),
				"response":        completedResp,
			}); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
				return err
			}
			fl.Flush()
			return nil
		case lipapi.EventError:
			return fmt.Errorf("openairesponses stream error: %s: %s", ev.ErrorCode, ev.ErrorMessage)
		case lipapi.EventWarning, lipapi.EventReasoningDelta, lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished:
		default:
		}
	}
}

func buildWireResponse(ctx context.Context, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) (wireResponse, error) {
	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		return wireResponse{}, err
	}
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	rid := opts.ResponseID
	if rid == "" {
		rid = "resp_" + time.Now().UTC().Format("20060102150405")
	}
	mid := opts.MessageID
	if mid == "" {
		mid = "msg_" + rid
	}
	ts := opts.CreatedAt
	if ts == 0 {
		ts = time.Now().Unix()
	}
	text := col.Text.String()
	out := wireOutput{
		Type:   "message",
		ID:     mid,
		Status: "completed",
		Role:   "assistant",
		Content: []wireOutPart{{
			Type: "output_text",
			Text: text,
		}},
	}
	return wireResponse{
		ID:        rid,
		Object:    "response",
		CreatedAt: ts,
		Status:    "completed",
		Model:     model,
		Output:    []wireOutput{out},
	}, nil
}
