package openailegacy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EncodeOptions controls wire identifiers for encoded Chat Completions payloads.
type EncodeOptions struct {
	CompletionID string
	CreatedAt    int64
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
	Content   string           `json:"content,omitempty"`
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
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// WriteErrorJSON writes an OpenAI-shaped JSON error.
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

// WriteNonStreamJSON encodes a completed canonical stream as chat.completion JSON.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		return err
	}
	text := col.Text.String()
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	cid := opts.CompletionID
	if cid == "" {
		cid = "chatcmpl_" + time.Now().UTC().Format("20060102150405")
	}
	ts := opts.CreatedAt
	if ts == 0 {
		ts = time.Now().Unix()
	}
	tools := col.OrderedToolCalls()
	stop := "stop"
	if len(tools) > 0 {
		stop = "tool_calls"
	}
	msg := &wireAssistant{Role: "assistant", Content: text}
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
	if col.InputTokens > 0 || col.OutputTokens > 0 {
		out.Usage = &wireUsageLegacy{
			PromptTokens:     col.InputTokens,
			CompletionTokens: col.OutputTokens,
			TotalTokens:      col.InputTokens + col.OutputTokens,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(out)
}

// WriteStreamSSE emits chat.completion.chunk SSE events incrementally from the canonical stream.
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	defer func() { _ = es.Close() }()
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	cid := opts.CompletionID
	if cid == "" {
		cid = "chatcmpl_" + time.Now().UTC().Format("20060102150405")
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
		return fmt.Errorf("openailegacy: ResponseWriter is not a Flusher")
	}

	roleChunk := wireChatCompletion{
		ID:      cid,
		Object:  "chat.completion.chunk",
		Created: ts,
		Model:   model,
		Choices: []wireChatChoice{{
			Index:        0,
			Delta:        &wireDelta{Role: "assistant"},
			FinishReason: nil,
		}},
	}
	if b, err := json.Marshal(roleChunk); err != nil {
		return err
	} else if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	fl.Flush()

	var inTok, outTok int
	streamToolIndex := make(map[string]int)
	nextToolStreamIndex := 0
	sawTool := false

	for {
		ev, err := es.Recv(ctx)
		if err == io.EOF {
			return fmt.Errorf("openailegacy: stream ended without response_finished")
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
			contChunk := wireChatCompletion{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: ts,
				Model:   model,
				Choices: []wireChatChoice{{
					Index:        0,
					Delta:        &wireDelta{Content: ev.Delta},
					FinishReason: nil,
				}},
			}
			b, err := json.Marshal(contChunk)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return err
			}
			fl.Flush()
		case lipapi.EventToolCallStarted:
			sawTool = true
			idx := nextToolStreamIndex
			nextToolStreamIndex++
			streamToolIndex[ev.ToolCallID] = idx
			fn := &wireLegacyToolDeltaFn{Name: ev.ToolName}
			td := wireLegacyToolDelta{
				Index:    idx,
				ID:       ev.ToolCallID,
				Type:     "function",
				Function: fn,
			}
			contChunk := wireChatCompletion{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: ts,
				Model:   model,
				Choices: []wireChatChoice{{
					Index:        0,
					Delta:        &wireDelta{ToolCalls: []wireLegacyToolDelta{td}},
					FinishReason: nil,
				}},
			}
			b, err := json.Marshal(contChunk)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return err
			}
			fl.Flush()
		case lipapi.EventToolCallArgsDelta:
			sawTool = true
			idx, ok := streamToolIndex[ev.ToolCallID]
			if !ok {
				idx = nextToolStreamIndex
				nextToolStreamIndex++
				streamToolIndex[ev.ToolCallID] = idx
			}
			td := wireLegacyToolDelta{
				Index:    idx,
				Function: &wireLegacyToolDeltaFn{Arguments: ev.Delta},
			}
			contChunk := wireChatCompletion{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: ts,
				Model:   model,
				Choices: []wireChatChoice{{
					Index:        0,
					Delta:        &wireDelta{ToolCalls: []wireLegacyToolDelta{td}},
					FinishReason: nil,
				}},
			}
			b, err := json.Marshal(contChunk)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return err
			}
			fl.Flush()
		case lipapi.EventToolCallFinished:
			sawTool = true
		case lipapi.EventResponseFinished:
			stop := "stop"
			if sawTool {
				stop = "tool_calls"
			}
			finalChunk := wireChatCompletion{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: ts,
				Model:   model,
				Choices: []wireChatChoice{{
					Index:        0,
					Delta:        &wireDelta{},
					FinishReason: &stop,
				}},
			}
			if inTok > 0 || outTok > 0 {
				finalChunk.Usage = &wireUsageLegacy{
					PromptTokens:     inTok,
					CompletionTokens: outTok,
					TotalTokens:      inTok + outTok,
				}
			}
			b, err := json.Marshal(finalChunk)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return err
			}
			fl.Flush()
			if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
				return err
			}
			fl.Flush()
			return nil
		case lipapi.EventError:
			return fmt.Errorf("openailegacy stream error: %s: %s", ev.ErrorCode, ev.ErrorMessage)
		case lipapi.EventWarning, lipapi.EventReasoningDelta:
		default:
		}
	}
}
