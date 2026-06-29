package anthropic

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

// EncodeOptions controls wire identifiers for encoded Messages payloads.
type EncodeOptions struct {
	MessageID                string
	ExposeLipUsageExtensions bool
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

type wireImageSource struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	MediaType string `json:"media_type,omitempty"`
}

type wireContentBlock struct {
	Type   string           `json:"type"`
	Text   string           `json:"text,omitempty"`
	ID     string           `json:"id,omitempty"`
	Name   string           `json:"name,omitempty"`
	Input  json.RawMessage  `json:"input,omitempty"`
	Source *wireImageSource `json:"source,omitempty"`
}

type wireUsage struct {
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens,omitempty"`
	CostNanoUnits            int64  `json:"x_lip_cost_nano_units,omitempty"`
	Currency                 string `json:"x_lip_currency,omitempty"`
	CostSource               string `json:"x_lip_cost_source,omitempty"`
	UncachedInputTokens      int    `json:"x_lip_uncached_tokens,omitempty"`
}

func wireAnthropicUsage(col lipapi.Collected, exposeLipExtensions bool) wireUsage {
	u := wireUsage{
		InputTokens:  col.InputTokens,
		OutputTokens: col.OutputTokens,
	}
	if col.CacheReadTokens > 0 {
		u.CacheReadInputTokens = col.CacheReadTokens
	}
	if col.CacheWriteTokens > 0 {
		u.CacheCreationInputTokens = col.CacheWriteTokens
	}
	if exposeLipExtensions {
		u.CostNanoUnits = col.CostNanoUnits
		u.Currency = col.Currency
		u.CostSource = col.CostSource
		if uncached := col.UncachedInputTokens(); uncached > 0 {
			u.UncachedInputTokens = uncached
		}
	}
	return u
}

// Streaming SSE wire shapes (typed JSON; avoids map[string]any in the hot loop).
var (
	anthropicJSONNull     = json.RawMessage("null")
	anthropicJSONEmptyArr = json.RawMessage("[]")
	anthropicJSONEmptyObj = json.RawMessage("{}")
)

type anthropicSSEMessageStart struct {
	Type    string                       `json:"type"`
	Message anthropicSSEMessageStartBody `json:"message"`
}

type anthropicSSEMessageStartBody struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	Model        string          `json:"model"`
	StopReason   json.RawMessage `json:"stop_reason"`
	StopSequence json.RawMessage `json:"stop_sequence"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicSSEContentBlockStartText struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index"`
	ContentBlock anthropicSSETextBlock `json:"content_block"`
}

type anthropicSSETextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicSSEContentBlockStartTool struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index"`
	ContentBlock anthropicSSEToolBlock `json:"content_block"`
}

type anthropicSSEContentBlockStartMedia struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type   string           `json:"type"`
		Source *wireImageSource `json:"source"`
	} `json:"content_block"`
}

type anthropicSSEToolBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicSSEDeltaJSON struct {
	Type  string                       `json:"type"`
	Index int                          `json:"index"`
	Delta anthropicSSEPartialJSONDelta `json:"delta"`
}

type anthropicSSEPartialJSONDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

type anthropicSSEDeltaText struct {
	Type  string                     `json:"type"`
	Index int                        `json:"index"`
	Delta anthropicSSETextDeltaInner `json:"delta"`
}

type anthropicSSETextDeltaInner struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicSSEContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type anthropicSSEMessageDelta struct {
	Type  string                        `json:"type"`
	Delta anthropicSSEMessageDeltaInner `json:"delta"`
	Usage wireUsage                     `json:"usage"`
}

type anthropicSSEMessageDeltaInner struct {
	StopReason   string          `json:"stop_reason"`
	StopSequence json.RawMessage `json:"stop_sequence"`
}

type anthropicSSEMessageStop struct {
	Type string `json:"type"`
}

func defaultEncodeOptions(call *lipapi.Call, opts EncodeOptions) EncodeOptions {
	if opts.MessageID == "" {
		opts.MessageID = "msg_" + diag.StableCallToken(call)
	}
	return opts
}

// WriteErrorJSON writes an Anthropic-shaped JSON error before any streamed bytes.
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var we wireAPIError
	we.Type = "error"
	we.Error.Type = errType
	if we.Error.Type == "" {
		we.Error.Type = "invalid_request_error"
	}
	we.Error.Message = message
	return json.NewEncoder(w).Encode(we)
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
	opts = defaultEncodeOptions(call, opts)
	mid := opts.MessageID
	tools := col.OrderedToolCalls()
	stop := "end_turn"
	if len(tools) > 0 {
		stop = "tool_use"
	}
	blocksCap := len(tools) + len(col.AssistantMedia)
	if text != "" {
		blocksCap++
	}
	blocks := make([]wireContentBlock, 0, blocksCap)
	if text != "" {
		blocks = append(blocks, wireContentBlock{Type: "text", Text: text})
	}
	for _, tc := range tools {
		raw := strings.TrimSpace(tc.Arguments)
		if raw == "" {
			raw = "{}"
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return fmt.Errorf("anthropic: tool arguments json: %w", err)
		}
		input, err := json.Marshal(v)
		if err != nil {
			return err
		}
		blocks = append(blocks, wireContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: input,
		})
	}
	for _, p := range col.AssistantMedia {
		switch p.Kind {
		case lipapi.PartImageRef:
			src := &wireImageSource{Type: "url", URL: p.ImageRef}
			if strings.TrimSpace(p.ImageMIME) != "" {
				src.MediaType = p.ImageMIME
			}
			blocks = append(blocks, wireContentBlock{Type: "image", Source: src})
		case lipapi.PartFileRef:
			// Non-stream assistant document output (URL-style ref only in v1 subset).
			src := &wireImageSource{Type: "url", URL: p.FileRef}
			if strings.TrimSpace(p.FileMIME) != "" {
				src.MediaType = p.FileMIME
			}
			blocks = append(blocks, wireContentBlock{Type: "document", Source: src})
		}
	}
	if len(blocks) == 0 {
		blocks = []wireContentBlock{{Type: "text", Text: ""}}
	}
	out := wireMessage{
		ID:         mid,
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		StopReason: stop,
		Content:    blocks,
		Usage:      wireAnthropicUsage(col, opts.ExposeLipUsageExtensions),
	}
	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(out)
}

func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) (err error) {
	ka, err := stream.WrapRecoveryKeepalive(es)
	if err != nil {
		return err
	}
	es = ka
	defer func() {
		if cerr := es.Close(); cerr != nil {
			closeErr := fmt.Errorf("anthropic: close event stream: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()
	model := ModelFromCall(call)
	if model == "" {
		model = "claude-3-5-haiku-20241022"
	}
	opts = defaultEncodeOptions(call, opts)
	mid := opts.MessageID

	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("anthropic: ResponseWriter is not a Flusher")
	}

	var usageCol lipapi.Collected
	var msgStarted bool
	nextBlockIdx := 0
	textBlockIdx := -1
	toolBlockIdx := make(map[string]int)
	sawTool := false

	flushMessageStart := func() error {
		if msgStarted {
			return nil
		}
		msgStarted = true
		var p anthropicSSEMessageStart
		p.Type = "message_start"
		p.Message.ID = mid
		p.Message.Type = "message"
		p.Message.Role = "assistant"
		p.Message.Content = anthropicJSONEmptyArr
		p.Message.Model = model
		p.Message.StopReason = anthropicJSONNull
		p.Message.StopSequence = anthropicJSONNull
		p.Message.Usage.InputTokens = usageCol.InputTokens
		p.Message.Usage.OutputTokens = 0
		return stream.FlushSSEEventJSON(w, fl, "message_start", &p)
	}

	openTextBlock := func() error {
		if textBlockIdx >= 0 {
			return nil
		}
		if err := flushMessageStart(); err != nil {
			return err
		}
		textBlockIdx = nextBlockIdx
		nextBlockIdx++
		cb := anthropicSSEContentBlockStartText{
			Type:         "content_block_start",
			Index:        textBlockIdx,
			ContentBlock: anthropicSSETextBlock{Type: "text", Text: ""},
		}
		return stream.FlushSSEEventJSON(w, fl, "content_block_start", &cb)
	}

	openToolBlock := func(callID, name string) error {
		if _, ok := toolBlockIdx[callID]; ok {
			return nil
		}
		if err := flushMessageStart(); err != nil {
			return err
		}
		idx := nextBlockIdx
		nextBlockIdx++
		toolBlockIdx[callID] = idx
		sawTool = true
		cb := anthropicSSEContentBlockStartTool{
			Type:  "content_block_start",
			Index: idx,
			ContentBlock: anthropicSSEToolBlock{
				Type:  "tool_use",
				ID:    callID,
				Name:  name,
				Input: anthropicJSONEmptyObj,
			},
		}
		return stream.FlushSSEEventJSON(w, fl, "content_block_start", &cb)
	}

	var ev lipapi.Event
	for {
		ev, err = es.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("anthropic: stream ended without response_finished")
		}
		if err != nil {
			return err
		}
		switch ev.Kind {
		case lipapi.EventResponseStarted:
		case lipapi.EventMessageStarted:
		case lipapi.EventUsageDelta:
			usageCol.AccumulateUsage(ev)
		case lipapi.EventToolCallStarted:
			if err := openToolBlock(ev.ToolCallID, ev.ToolName); err != nil {
				return err
			}
		case lipapi.EventToolCallArgsDelta:
			if err := openToolBlock(ev.ToolCallID, ""); err != nil {
				return err
			}
			idx := toolBlockIdx[ev.ToolCallID]
			d := anthropicSSEDeltaJSON{
				Type:  "content_block_delta",
				Index: idx,
				Delta: anthropicSSEPartialJSONDelta{
					Type:        "input_json_delta",
					PartialJSON: ev.Delta,
				},
			}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_delta", &d); err != nil {
				return err
			}
		case lipapi.EventToolCallFinished:
			idx, ok := toolBlockIdx[ev.ToolCallID]
			if !ok {
				continue
			}
			cbStop := anthropicSSEContentBlockStop{Type: "content_block_stop", Index: idx}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_stop", &cbStop); err != nil {
				return err
			}
		case lipapi.EventTextDelta:
			if err := openTextBlock(); err != nil {
				return err
			}
			d := anthropicSSEDeltaText{
				Type:  "content_block_delta",
				Index: textBlockIdx,
				Delta: anthropicSSETextDeltaInner{Type: "text_delta", Text: ev.Delta},
			}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_delta", &d); err != nil {
				return err
			}
		case lipapi.EventAssistantImageRef, lipapi.EventAssistantFileRef:
			idx := nextBlockIdx
			nextBlockIdx++
			src := &wireImageSource{Type: "url", URL: ev.AssistantRef}
			if mt := strings.TrimSpace(ev.AssistantMIME); mt != "" {
				src.MediaType = mt
			}
			var cb anthropicSSEContentBlockStartMedia
			cb.Type = "content_block_start"
			cb.Index = idx
			if ev.Kind == lipapi.EventAssistantImageRef {
				cb.ContentBlock.Type = "image"
			} else {
				cb.ContentBlock.Type = "document"
			}
			cb.ContentBlock.Source = src
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_start", &cb); err != nil {
				return err
			}
			cbStop := anthropicSSEContentBlockStop{Type: "content_block_stop", Index: idx}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_stop", &cbStop); err != nil {
				return err
			}
		case lipapi.EventResponseFinished:
			if !msgStarted {
				if err := openTextBlock(); err != nil {
					return err
				}
			}
			if textBlockIdx >= 0 {
				cbStop := anthropicSSEContentBlockStop{Type: "content_block_stop", Index: textBlockIdx}
				if err := stream.FlushSSEEventJSON(w, fl, "content_block_stop", &cbStop); err != nil {
					return err
				}
			}
			stop := "end_turn"
			if sawTool {
				stop = "tool_use"
			}
			var msgDelta anthropicSSEMessageDelta
			msgDelta.Type = "message_delta"
			msgDelta.Delta.StopReason = stop
			msgDelta.Delta.StopSequence = anthropicJSONNull
			msgDelta.Usage = wireAnthropicUsage(usageCol, opts.ExposeLipUsageExtensions)
			if err := stream.FlushSSEEventJSON(w, fl, "message_delta", &msgDelta); err != nil {
				return err
			}
			stopPayload := anthropicSSEMessageStop{Type: "message_stop"}
			if err := stream.FlushSSEEventJSON(w, fl, "message_stop", &stopPayload); err != nil {
				return err
			}
			return nil
		case lipapi.EventError:
			return lipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage)
		case lipapi.EventWarning:
			if ev.WarningCode == stream.KeepaliveEventCode {
				if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
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
