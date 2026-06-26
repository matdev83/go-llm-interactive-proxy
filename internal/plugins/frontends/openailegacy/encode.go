package openailegacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// streamChunkState holds reusable wire state for chat.completion.chunk SSE frames.
type streamChunkState struct {
	chunk   wireChatCompletion
	choices [1]wireChatChoice
	delta   wireDelta
	fn      wireLegacyToolDeltaFn
	tools   [1]wireLegacyToolDelta
}

// EncodeOptions controls wire identifiers for encoded Chat Completions payloads.
type EncodeOptions struct {
	CompletionID             string
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

type wireChatCompletion struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []wireChatChoice `json:"choices"`
	Usage   *wireUsageLegacy `json:"usage,omitempty"`
}

type wireChatChoice struct {
	Index        int            `json:"index"`
	Message      *wireAssistant `json:"message,omitempty"`
	Delta        *wireDelta     `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason"`
}

type wireAssistant struct {
	Role      string           `json:"role"`
	Content   json.RawMessage  `json:"content,omitempty"` // JSON string or multimodal part array
	ToolCalls []wireToolCallNS `json:"tool_calls,omitempty"`
}

type wireToolCallNS struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireDelta struct {
	Role      string                `json:"role,omitempty"`
	Content   string                `json:"content,omitempty"`
	ToolCalls []wireLegacyToolDelta `json:"tool_calls,omitempty"`
}

type wireLegacyToolDelta struct {
	Index    int                    `json:"index"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function *wireLegacyToolDeltaFn `json:"function,omitempty"`
}

type wireLegacyToolDeltaFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type wireUsageLegacy struct {
	PromptTokens            int                          `json:"prompt_tokens"`
	CompletionTokens        int                          `json:"completion_tokens"`
	TotalTokens             int                          `json:"total_tokens"`
	PromptTokensDetails     *wirePromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *wireCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	CostNanoUnits           int64                        `json:"x_lip_cost_nano_units,omitempty"`
	Currency                string                       `json:"x_lip_currency,omitempty"`
	CostSource              string                       `json:"x_lip_cost_source,omitempty"`
}

type wirePromptTokensDetails struct {
	CachedTokens   int `json:"cached_tokens,omitempty"`
	UncachedTokens int `json:"x_lip_uncached_tokens,omitempty"`
	CacheWrite     int `json:"x_lip_cache_write_tokens,omitempty"`
}

type wireCompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

func wireLegacyUsage(col lipapi.Collected, exposeExt bool) *wireUsageLegacy {
	if col.InputTokens == 0 && col.OutputTokens == 0 && col.CacheReadTokens == 0 && col.CacheWriteTokens == 0 && col.ReasoningTokens == 0 && col.TotalTokens == 0 && col.CostNanoUnits == 0 {
		return nil
	}
	u := &wireUsageLegacy{
		PromptTokens:     col.InputTokens,
		CompletionTokens: col.OutputTokens,
		TotalTokens:      col.TotalOrDerived(),
	}
	if col.CacheReadTokens > 0 || col.CacheWriteTokens > 0 {
		u.PromptTokensDetails = &wirePromptTokensDetails{CachedTokens: col.CacheReadTokens}
		if exposeExt {
			u.PromptTokensDetails.UncachedTokens = col.UncachedInputTokens()
			u.PromptTokensDetails.CacheWrite = col.CacheWriteTokens
		}
	}
	if col.ReasoningTokens > 0 {
		u.CompletionTokensDetails = &wireCompletionTokensDetails{ReasoningTokens: col.ReasoningTokens}
	}
	if exposeExt {
		u.CostNanoUnits = col.CostNanoUnits
		u.Currency = col.Currency
		u.CostSource = col.CostSource
	}
	return u
}

// marshalChatAssistantContent returns JSON for Chat Completions assistant message content:
// a JSON string when there is text-only output, otherwise a parts array (text + image_url / file).
func marshalChatAssistantContent(col lipapi.Collected) (json.RawMessage, error) {
	if len(col.AssistantMedia) == 0 {
		return json.Marshal(col.Text.String())
	}
	parts := []map[string]any{}
	if col.Text.String() != "" {
		parts = append(parts, map[string]any{"type": "text", "text": col.Text.String()})
	}
	for _, p := range col.AssistantMedia {
		switch p.Kind {
		case lipapi.PartImageRef:
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": p.ImageRef},
			})
		case lipapi.PartFileRef:
			parts = append(parts, map[string]any{
				"type": "file",
				"file": map[string]any{"file_id": p.FileRef},
			})
		}
	}
	return json.Marshal(parts)
}

func defaultEncodeOptions(call *lipapi.Call, opts EncodeOptions) EncodeOptions {
	if opts.CompletionID == "" {
		opts.CompletionID = "chatcmpl_" + diag.StableCallToken(call)
	}
	if opts.CreatedAt == 0 {
		opts.CreatedAt = diag.StableUnix(call)
	}
	return opts
}

// WriteErrorJSON writes an OpenAI-shaped JSON error.
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

// WriteNonStreamJSON encodes a completed canonical stream as chat.completion JSON.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		return err
	}
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	opts = defaultEncodeOptions(call, opts)
	cid := opts.CompletionID
	ts := opts.CreatedAt
	tools := col.OrderedToolCalls()
	stop := "stop"
	if len(tools) > 0 {
		stop = "tool_calls"
	}
	contentJSON, err := marshalChatAssistantContent(col)
	if err != nil {
		return err
	}
	msg := &wireAssistant{Role: "assistant", Content: contentJSON}
	for _, tc := range tools {
		var wtc wireToolCallNS
		wtc.ID = tc.ID
		wtc.Type = "function"
		wtc.Function.Name = tc.Name
		wtc.Function.Arguments = tc.Arguments
		msg.ToolCalls = append(msg.ToolCalls, wtc)
	}
	out := wireChatCompletion{
		ID:      cid,
		Object:  "chat.completion",
		Created: ts,
		Model:   model,
		Choices: []wireChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: &stop,
		}},
	}
	out.Usage = wireLegacyUsage(col, opts.ExposeLipUsageExtensions)
	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(out)
}

// WriteStreamSSE emits chat.completion.chunk SSE events incrementally from the canonical stream.
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) (err error) {
	ka, err := stream.WrapRecoveryKeepalive(es)
	if err != nil {
		return err
	}
	es = ka
	defer func() {
		if cerr := es.Close(); cerr != nil {
			closeErr := fmt.Errorf("openailegacy: close event stream: %w", cerr)
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
	cid := opts.CompletionID
	ts := opts.CreatedAt

	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("openailegacy: ResponseWriter is not a Flusher")
	}

	var st streamChunkState
	st.chunk.ID = cid
	st.chunk.Object = "chat.completion.chunk"
	st.chunk.Created = ts
	st.chunk.Model = model
	st.choices[0].Index = 0
	st.choices[0].FinishReason = nil
	st.delta.Role = "assistant"
	st.choices[0].Delta = &st.delta
	st.chunk.Choices = st.choices[:]
	if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
		return err
	}

	var usageCol lipapi.Collected
	streamToolIndex := make(map[string]int)
	nextToolStreamIndex := 0
	sawTool := false

	var ev lipapi.Event
	for {
		ev, err = es.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("openailegacy: stream ended without response_finished")
		}
		if err != nil {
			return err
		}
		switch ev.Kind {
		case lipapi.EventResponseStarted, lipapi.EventMessageStarted:
		case lipapi.EventUsageDelta:
			usageCol.AccumulateUsage(ev)
		case lipapi.EventTextDelta:
			st.choices[0].FinishReason = nil
			st.delta = wireDelta{Content: ev.Delta}
			st.choices[0].Delta = &st.delta
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
				return err
			}
		case lipapi.EventToolCallStarted:
			sawTool = true
			idx := nextToolStreamIndex
			nextToolStreamIndex++
			streamToolIndex[ev.ToolCallID] = idx
			st.choices[0].FinishReason = nil
			st.fn = wireLegacyToolDeltaFn{Name: ev.ToolName}
			st.tools[0] = wireLegacyToolDelta{
				Index:    idx,
				ID:       ev.ToolCallID,
				Type:     "function",
				Function: &st.fn,
			}
			st.delta = wireDelta{ToolCalls: st.tools[:]}
			st.choices[0].Delta = &st.delta
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
				return err
			}
		case lipapi.EventToolCallArgsDelta:
			sawTool = true
			idx, ok := streamToolIndex[ev.ToolCallID]
			if !ok {
				idx = nextToolStreamIndex
				nextToolStreamIndex++
				streamToolIndex[ev.ToolCallID] = idx
			}
			st.choices[0].FinishReason = nil
			st.fn = wireLegacyToolDeltaFn{Arguments: ev.Delta}
			st.tools[0] = wireLegacyToolDelta{
				Index:    idx,
				Function: &st.fn,
			}
			st.delta = wireDelta{ToolCalls: st.tools[:]}
			st.choices[0].Delta = &st.delta
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
				return err
			}
		case lipapi.EventToolCallFinished:
			sawTool = true
		case lipapi.EventResponseFinished:
			stop := "stop"
			if sawTool {
				stop = "tool_calls"
			}
			st.delta = wireDelta{}
			st.choices[0].Delta = &st.delta
			st.choices[0].FinishReason = &stop
			st.chunk.Usage = wireLegacyUsage(usageCol, opts.ExposeLipUsageExtensions)
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
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
