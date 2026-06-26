package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EncodeOptions controls wire identifiers for encoded Responses payloads.
type EncodeOptions struct {
	ResponseID               string
	MessageID                string
	CreatedAt                int64
	ExposeLipUsageExtensions bool
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
	InputTokens         int                      `json:"input_tokens"`
	OutputTokens        int                      `json:"output_tokens"`
	TotalTokens         int                      `json:"total_tokens,omitempty"`
	InputTokensDetails  *wireInputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *wireOutputTokensDetails `json:"output_tokens_details,omitempty"`
	CostNanoUnits       int64                    `json:"x_lip_cost_nano_units,omitempty"`
	Currency            string                   `json:"x_lip_currency,omitempty"`
	CostSource          string                   `json:"x_lip_cost_source,omitempty"`
}

type wireInputTokensDetails struct {
	CachedTokens   int `json:"cached_tokens,omitempty"`
	UncachedTokens int `json:"x_lip_uncached_tokens,omitempty"`
	CacheWrite     int `json:"x_lip_cache_write_tokens,omitempty"`
}

type wireOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

func wireResponsesUsage(col lipapi.Collected, exposeExt bool) *wireUsage {
	if col.InputTokens == 0 && col.OutputTokens == 0 && col.CacheReadTokens == 0 && col.CacheWriteTokens == 0 && col.ReasoningTokens == 0 && col.TotalTokens == 0 && col.CostNanoUnits == 0 {
		return nil
	}
	u := &wireUsage{
		InputTokens:  col.InputTokens,
		OutputTokens: col.OutputTokens,
		TotalTokens:  col.TotalOrDerived(),
	}
	if col.CacheReadTokens > 0 || col.CacheWriteTokens > 0 {
		u.InputTokensDetails = &wireInputTokensDetails{CachedTokens: col.CacheReadTokens}
		if exposeExt {
			u.InputTokensDetails.UncachedTokens = col.UncachedInputTokens()
			u.InputTokensDetails.CacheWrite = col.CacheWriteTokens
		}
	}
	if col.ReasoningTokens > 0 {
		u.OutputTokensDetails = &wireOutputTokensDetails{ReasoningTokens: col.ReasoningTokens}
	}
	if exposeExt {
		u.CostNanoUnits = col.CostNanoUnits
		u.Currency = col.Currency
		u.CostSource = col.CostSource
	}
	return u
}

func fcItemID(callID string) string {
	return "fc_" + strings.ReplaceAll(callID, ":", "_")
}

// wireMessageContentParts builds Responses API message.content items (output_text plus optional input_image / input_file refs).
func wireMessageContentParts(text string, media []lipapi.Part) []any {
	out := []any{map[string]any{"type": "output_text", "text": text}}
	for _, p := range media {
		switch p.Kind {
		case lipapi.PartImageRef:
			out = append(out, map[string]any{"type": "input_image", "image_url": p.ImageRef})
		case lipapi.PartFileRef:
			m := map[string]any{"type": "input_file", "file_id": p.FileRef}
			if p.FileName != "" {
				m["filename"] = p.FileName
			}
			out = append(out, m)
		}
	}
	return out
}

type wireStreamEnvelope struct {
	Type           string       `json:"type"`
	SequenceNumber int          `json:"sequence_number"`
	Response       wireResponse `json:"response"`
}

func defaultEncodeOptions(call *lipapi.Call, opts EncodeOptions) EncodeOptions {
	if opts.ResponseID == "" {
		opts.ResponseID = "resp_" + diag.StableCallToken(call)
	}
	if opts.MessageID == "" {
		opts.MessageID = "msg_" + opts.ResponseID
	}
	if opts.CreatedAt == 0 {
		opts.CreatedAt = diag.StableUnix(call)
	}
	return opts
}

// WriteErrorJSON writes an OpenAI-shaped JSON error before any streamed bytes.
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType, code string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var we wireAPIError
	we.Error.Message = message
	we.Error.Type = errType
	we.Error.Param = nil
	if code != "" {
		we.Error.Code = &code
	}
	return json.NewEncoder(w).Encode(we)
}

// WriteNonStreamJSON encodes a completed canonical stream as a non-streaming Responses JSON body.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	resp, err := buildWireResponse(ctx, call, es, opts)
	if err != nil {
		return err
	}
	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) (err error) {
	ka, err := stream.WrapRecoveryKeepalive(es)
	if err != nil {
		return err
	}
	es = ka
	defer func() {
		if cerr := es.Close(); cerr != nil {
			closeErr := fmt.Errorf("openairesponses: close event stream: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	opts = defaultEncodeOptions(call, opts)
	rid := opts.ResponseID
	mid := opts.MessageID
	ts := opts.CreatedAt

	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("openairesponses: ResponseWriter is not a Flusher")
	}

	var seq int
	nextSeq := func() int { seq++; return seq }

	var usageCol lipapi.Collected
	var fullText strings.Builder
	var assistantMedia []lipapi.Part

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
		if err := flushSSE(w, fl, "response.output_item.added", streamOutputItemAddedFunc{
			Type:           "response.output_item.added",
			SequenceNumber: nextSeq(),
			OutputIndex:    st.OutputIndex,
			Item: streamFuncCallInProgress{
				Type:      "function_call",
				ID:        st.ItemID,
				CallID:    st.CallID,
				Name:      st.Name,
				Arguments: "",
				Status:    "in_progress",
			},
		}); err != nil {
			return nil, err
		}
		return st, nil
	}

	createdResponse := wireResponse{
		ID:        rid,
		Object:    "response",
		CreatedAt: ts,
		Status:    "in_progress",
		Model:     model,
		Output:    []any{},
	}
	if err := flushSSE(w, fl, "response.created", wireStreamEnvelope{
		Type:           "response.created",
		SequenceNumber: nextSeq(),
		Response:       createdResponse,
	}); err != nil {
		return err
	}
	if err := flushSSE(w, fl, "response.in_progress", wireStreamEnvelope{
		Type:           "response.in_progress",
		SequenceNumber: nextSeq(),
		Response:       createdResponse,
	}); err != nil {
		return err
	}
	msgItem := streamMessageItem{
		Type:    "message",
		ID:      mid,
		Status:  "in_progress",
		Role:    "assistant",
		Content: []streamMsgContent{},
	}
	if err := flushSSE(w, fl, "response.output_item.added", streamOutputItemAddedMsg{
		Type:           "response.output_item.added",
		SequenceNumber: nextSeq(),
		OutputIndex:    0,
		Item:           msgItem,
	}); err != nil {
		return err
	}
	var partAdded streamContentPartAdded
	partAdded.Type = "response.content_part.added"
	partAdded.SequenceNumber = nextSeq()
	partAdded.OutputIndex = 0
	partAdded.Part.Type = "output_text"
	partAdded.Part.Text = ""
	if err := flushSSE(w, fl, "response.content_part.added", partAdded); err != nil {
		return err
	}

	var ev lipapi.Event
	for {
		ev, err = es.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("openairesponses: stream ended without response_finished")
		}
		if err != nil {
			return err
		}
		switch ev.Kind {
		case lipapi.EventResponseStarted, lipapi.EventMessageStarted:
		case lipapi.EventUsageDelta:
			usageCol.AccumulateUsage(ev)
		case lipapi.EventTextDelta:
			fullText.WriteString(ev.Delta)
			if err := flushSSE(w, fl, "response.output_text.delta", streamOutputTextDelta{
				Type:           "response.output_text.delta",
				SequenceNumber: nextSeq(),
				Delta:          ev.Delta,
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
			if err := flushSSE(w, fl, "response.output_item.added", streamOutputItemAddedFunc{
				Type:           "response.output_item.added",
				SequenceNumber: nextSeq(),
				OutputIndex:    st.OutputIndex,
				Item: streamFuncCallInProgress{
					Type:      "function_call",
					ID:        st.ItemID,
					CallID:    st.CallID,
					Name:      st.Name,
					Arguments: "",
					Status:    "in_progress",
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
			if err := flushSSE(w, fl, "response.function_call_arguments.delta", streamFuncArgsDelta{
				Type:           "response.function_call_arguments.delta",
				SequenceNumber: nextSeq(),
				ItemID:         st.ItemID,
				OutputIndex:    st.OutputIndex,
				Delta:          ev.Delta,
			}); err != nil {
				return err
			}
		case lipapi.EventAssistantImageRef:
			assistantMedia = append(assistantMedia, lipapi.Part{
				Kind: lipapi.PartImageRef, ImageRef: ev.AssistantRef, ImageMIME: ev.AssistantMIME,
			})
		case lipapi.EventAssistantFileRef:
			assistantMedia = append(assistantMedia, lipapi.Part{
				Kind: lipapi.PartFileRef, FileRef: ev.AssistantRef, FileMIME: ev.AssistantMIME, FileName: ev.AssistantName,
			})
		case lipapi.EventToolCallFinished:
			st := toolByCallID[ev.ToolCallID]
			if st == nil {
				continue
			}
			args := st.Args.String()
			if err := flushSSE(w, fl, "response.function_call_arguments.done", streamFuncArgsDone{
				Type:           "response.function_call_arguments.done",
				SequenceNumber: nextSeq(),
				ItemID:         st.ItemID,
				Name:           st.Name,
				Arguments:      args,
				OutputIndex:    st.OutputIndex,
			}); err != nil {
				return err
			}
			if err := flushSSE(w, fl, "response.output_item.done", streamOutputItemDone{
				Type:           "response.output_item.done",
				SequenceNumber: nextSeq(),
				OutputIndex:    st.OutputIndex,
				Item: streamFuncItemDone{
					Type:      "function_call",
					ID:        st.ItemID,
					CallID:    st.CallID,
					Name:      st.Name,
					Arguments: args,
					Status:    "completed",
				},
			}); err != nil {
				return err
			}
		case lipapi.EventResponseFinished:
			text := fullText.String()
			if err := flushSSE(w, fl, "response.output_text.done", streamOutputTextDone{
				Type:           "response.output_text.done",
				SequenceNumber: nextSeq(),
				Text:           text,
			}); err != nil {
				return err
			}

			msgParts := []streamMsgContent{{Type: "output_text", Text: text}}
			for _, p := range assistantMedia {
				switch p.Kind {
				case lipapi.PartImageRef:
					msgParts = append(msgParts, streamMsgContent{Type: "input_image", ImageURL: p.ImageRef})
				case lipapi.PartFileRef:
					msgParts = append(msgParts, streamMsgContent{Type: "input_file", FileID: p.FileRef, FileName: p.FileName})
				}
			}
			out := make([]streamCompletedOut, 0, 1+len(toolOrder))
			out = append(out, streamCompletedOut{
				Type:    "message",
				ID:      mid,
				Status:  "completed",
				Role:    "assistant",
				Content: msgParts,
			})
			for _, st := range toolOrder {
				out = append(out, streamCompletedOut{
					Type:      "function_call",
					ID:        st.ItemID,
					CallID:    st.CallID,
					Name:      st.Name,
					Arguments: st.Args.String(),
					Status:    "completed",
				})
			}

			var completed streamCompletedEvent
			completed.Type = "response.completed"
			completed.SequenceNumber = nextSeq()
			completed.Response.ID = rid
			completed.Response.Object = "response"
			completed.Response.CreatedAt = ts
			completed.Response.Status = "completed"
			completed.Response.Model = model
			completed.Response.Output = out
			completed.Response.Usage = wireResponsesUsage(usageCol, opts.ExposeLipUsageExtensions)
			if err := flushSSE(w, fl, "response.completed", completed); err != nil {
				return err
			}
			if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
				return err
			}
			fl.Flush()
			return nil
		case lipapi.EventError:
			return lipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage)
		case lipapi.EventWarning:
			if ev.WarningCode == stream.KeepaliveEventCode {
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					return err
				}
				fl.Flush()
				continue
			}
		case lipapi.EventReasoningDelta:
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
	opts = defaultEncodeOptions(call, opts)
	rid := opts.ResponseID
	mid := opts.MessageID
	ts := opts.CreatedAt
	text := col.Text.String()
	msgOut := map[string]any{
		"type":    "message",
		"id":      mid,
		"status":  "completed",
		"role":    "assistant",
		"content": wireMessageContentParts(text, col.AssistantMedia),
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
	resp.Usage = wireResponsesUsage(col, opts.ExposeLipUsageExtensions)
	return resp, nil
}
