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
	ID        string     `json:"id"`
	Object    string     `json:"object"`
	CreatedAt int64      `json:"created_at"`
	Status    string     `json:"status"`
	Model     string     `json:"model"`
	Output    []any      `json:"output"`
	Usage     *wireUsage `json:"usage,omitempty"`
}

type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func fcItemID(callID string) string {
	return "fc_" + strings.ReplaceAll(callID, ":", "_")
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

	type toolStream struct {
		CallID      string
		ItemID      string
		OutputIndex int64
		Name        string
		Args        strings.Builder
	}
	toolByCallID := make(map[string]*toolStream)
	var toolOrder []*toolStream
	nextOutIdx := int64(1)

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

	ensureToolStream := func(callID string) (*toolStream, error) {
		if st := toolByCallID[callID]; st != nil {
			return st, nil
		}
		st := &toolStream{
			CallID:      callID,
			ItemID:      fcItemID(callID),
			OutputIndex: nextOutIdx,
			Name:        "",
		}
		nextOutIdx++
		toolByCallID[callID] = st
		toolOrder = append(toolOrder, st)
		if err := writeStreamEvent("response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": nextSeq(),
			"output_index":    st.OutputIndex,
			"item": map[string]any{
				"type":      "function_call",
				"id":        st.ItemID,
				"call_id":   st.CallID,
				"name":      st.Name,
				"arguments": "",
				"status":    "in_progress",
			},
		}); err != nil {
			return nil, err
		}
		return st, nil
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
		"output_index":    int64(0),
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
		"output_index":    int64(0),
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
		case lipapi.EventToolCallStarted:
			if st, ok := toolByCallID[ev.ToolCallID]; ok {
				if ev.ToolName != "" {
					st.Name = ev.ToolName
				}
				break
			}
			st := &toolStream{
				CallID:      ev.ToolCallID,
				ItemID:      fcItemID(ev.ToolCallID),
				OutputIndex: nextOutIdx,
				Name:        ev.ToolName,
			}
			nextOutIdx++
			toolByCallID[ev.ToolCallID] = st
			toolOrder = append(toolOrder, st)
			if err := writeStreamEvent("response.output_item.added", map[string]any{
				"type":            "response.output_item.added",
				"sequence_number": nextSeq(),
				"output_index":    st.OutputIndex,
				"item": map[string]any{
					"type":      "function_call",
					"id":        st.ItemID,
					"call_id":   st.CallID,
					"name":      st.Name,
					"arguments": "",
					"status":    "in_progress",
				},
			}); err != nil {
				return err
			}
		case lipapi.EventToolCallArgsDelta:
			st, err := ensureToolStream(ev.ToolCallID)
			if err != nil {
				return err
			}
			st.Args.WriteString(ev.Delta)
			if err := writeStreamEvent("response.function_call_arguments.delta", map[string]any{
				"type":            "response.function_call_arguments.delta",
				"sequence_number": nextSeq(),
				"item_id":         st.ItemID,
				"output_index":    st.OutputIndex,
				"delta":           ev.Delta,
			}); err != nil {
				return err
			}
		case lipapi.EventToolCallFinished:
			st := toolByCallID[ev.ToolCallID]
			if st == nil {
				continue
			}
			args := st.Args.String()
			if err := writeStreamEvent("response.function_call_arguments.done", map[string]any{
				"type":            "response.function_call_arguments.done",
				"sequence_number": nextSeq(),
				"item_id":         st.ItemID,
				"name":            st.Name,
				"arguments":       args,
				"output_index":    st.OutputIndex,
			}); err != nil {
				return err
			}
			if err := writeStreamEvent("response.output_item.done", map[string]any{
				"type":            "response.output_item.done",
				"sequence_number": nextSeq(),
				"output_index":    st.OutputIndex,
				"item": map[string]any{
					"type":      "function_call",
					"id":        st.ItemID,
					"call_id":   st.CallID,
					"name":      st.Name,
					"arguments": args,
					"status":    "completed",
				},
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

			out := []any{
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
			}
			for _, st := range toolOrder {
				out = append(out, map[string]any{
					"type":      "function_call",
					"id":        st.ItemID,
					"call_id":   st.CallID,
					"name":      st.Name,
					"arguments": st.Args.String(),
					"status":    "completed",
				})
			}

			completedResp := map[string]any{
				"id":         rid,
				"object":     "response",
				"created_at": ts,
				"status":     "completed",
				"model":      model,
				"output":     out,
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
		case lipapi.EventWarning, lipapi.EventReasoningDelta:
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
	msgOut := map[string]any{
		"type":    "message",
		"id":      mid,
		"status":  "completed",
		"role":    "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
	out := []any{msgOut}
	for _, tc := range col.OrderedToolCalls() {
		out = append(out, map[string]any{
			"type":      "function_call",
			"id":        fcItemID(tc.ID),
			"call_id":   tc.ID,
			"name":      tc.Name,
			"arguments": tc.Arguments,
			"status":    "completed",
		})
	}
	resp := wireResponse{
		ID:        rid,
		Object:    "response",
		CreatedAt: ts,
		Status:    "completed",
		Model:     model,
		Output:    out,
	}
	if col.InputTokens > 0 || col.OutputTokens > 0 {
		resp.Usage = &wireUsage{
			InputTokens:  col.InputTokens,
			OutputTokens: col.OutputTokens,
		}
	}
	return resp, nil
}
