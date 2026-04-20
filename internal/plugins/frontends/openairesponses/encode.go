package openairesponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// WriteStreamSSE drains the canonical stream and emits a minimal legal SSE sequence
// understood by the official OpenAI Go SDK (response.completed + [DONE]).
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts EncodeOptions) error {
	resp, err := buildWireResponse(ctx, call, es, opts)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("openairesponses: ResponseWriter is not a Flusher")
	}
	env := wireStreamEnvelope{
		Type:           "response.completed",
		SequenceNumber: 1,
		Response:       resp,
	}
	line, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", env.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
		return err
	}
	fl.Flush()
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	fl.Flush()
	return nil
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
