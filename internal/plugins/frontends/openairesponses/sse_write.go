package openairesponses

import (
	"io"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
)

// flushSSE writes one Server-Sent Events frame: event line, data line (JSON), blank line.
func flushSSE(w io.Writer, fl http.Flusher, eventName string, payload any) error {
	return stream.FlushSSEEventJSON(w, fl, eventName, payload)
}

// Typed SSE JSON payloads (avoid map[string]any boxing in the hot loop).

type streamOutputTextDelta struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	Delta          string `json:"delta"`
}

type streamFuncArgsDelta struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int64  `json:"output_index"`
	Delta          string `json:"delta"`
}

type streamFuncArgsDone struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	Name           string `json:"name"`
	Arguments      string `json:"arguments"`
	OutputIndex    int64  `json:"output_index"`
}

type streamFuncItemDone struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Status    string `json:"status"`
}

type streamOutputItemDone struct {
	Type           string             `json:"type"`
	SequenceNumber int                `json:"sequence_number"`
	OutputIndex    int64              `json:"output_index"`
	Item           streamFuncItemDone `json:"item"`
}

type streamOutputTextDone struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	Text           string `json:"text"`
}

type streamFuncCallInProgress struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Status    string `json:"status"`
}

type streamOutputItemAddedFunc struct {
	Type           string                   `json:"type"`
	SequenceNumber int                      `json:"sequence_number"`
	OutputIndex    int64                    `json:"output_index"`
	Item           streamFuncCallInProgress `json:"item"`
}

type streamMsgContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	FileName string `json:"filename,omitempty"`
}

type streamMessageItem struct {
	Type    string             `json:"type"`
	ID      string             `json:"id"`
	Status  string             `json:"status"`
	Role    string             `json:"role"`
	Content []streamMsgContent `json:"content"`
}

type streamOutputItemAddedMsg struct {
	Type           string            `json:"type"`
	SequenceNumber int               `json:"sequence_number"`
	OutputIndex    int64             `json:"output_index"`
	Item           streamMessageItem `json:"item"`
}

type streamContentPartAdded struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	OutputIndex    int64  `json:"output_index"`
	Part           struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"part"`
}

type streamCompletedOut struct {
	Type      string             `json:"type"`
	ID        string             `json:"id,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Status    string             `json:"status,omitempty"`
	Role      string             `json:"role,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
	Content   []streamMsgContent `json:"content,omitempty"`
}

type streamCompletedEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	Response       struct {
		ID        string               `json:"id"`
		Object    string               `json:"object"`
		CreatedAt int64                `json:"created_at"`
		Status    string               `json:"status"`
		Model     string               `json:"model"`
		Output    []streamCompletedOut `json:"output"`
		Usage     *wireUsage           `json:"usage,omitempty"`
	} `json:"response"`
}
