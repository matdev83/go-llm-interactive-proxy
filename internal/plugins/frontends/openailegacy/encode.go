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
}

type wireChatChoice struct {
	Index        int            `json:"index"`
	Message      *wireAssistant `json:"message,omitempty"`
	Delta        *wireDelta     `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason"`
}

type wireAssistant struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wireDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
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
	stop := "stop"
	out := wireChatCompletion{
		ID:      cid,
		Object:  "chat.completion",
		Created: ts,
		Model:   model,
		Choices: []wireChatChoice{{
			Index: 0,
			Message: &wireAssistant{
				Role:    "assistant",
				Content: text,
			},
			FinishReason: &stop,
		}},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(out)
}

// WriteStreamSSE drains the canonical stream and emits chat.completion.chunk SSE
// sequences terminated with data: [DONE] (compatible with github.com/openai/openai-go/v3 streaming).
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
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

	if text != "" {
		contChunk := wireChatCompletion{
			ID:      cid,
			Object:  "chat.completion.chunk",
			Created: ts,
			Model:   model,
			Choices: []wireChatChoice{{
				Index:        0,
				Delta:        &wireDelta{Content: text},
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
	}

	stop := "stop"
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
}
