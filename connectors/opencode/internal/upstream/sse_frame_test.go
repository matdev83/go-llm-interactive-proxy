package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestDecodeSSEFrameRejectsOversizedPayload(t *testing.T) {
	t.Parallel()

	var got map[string]any
	err := decodeSSEFrame([]byte(strings.Repeat("a", maxSSEFrameBytes+1)), &got)
	if !errors.Is(err, errSSEFrameTooLarge) {
		t.Fatalf("error=%v, want errSSEFrameTooLarge", err)
	}
	if len(got) != 0 {
		t.Fatalf("destination was materialized despite frame cap: %+v", got)
	}
}

func TestDecodeSSEFrameAdmitsExactCapPayload(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("a", maxSSEFrameBytes-8)
	body := `{"x":"` + text + `"}`
	if len(body) != maxSSEFrameBytes {
		t.Fatalf("body=%d bytes, want exact cap %d", len(body), maxSSEFrameBytes)
	}
	var got map[string]any
	if err := decodeSSEFrame([]byte(body), &got); err != nil {
		t.Fatalf("exact-cap frame rejected: %v", err)
	}
	if got["x"] != text {
		t.Fatalf("decoded value=%v, want exact payload", got["x"])
	}
}

func TestAnthropicSSEStreamRejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	body := "data: " + strings.Repeat("x", maxSSEFrameBytes+1) + "\n\n"
	s := newAnthropicSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer s.Close()
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errSSEFrameTooLarge) {
		t.Fatalf("error=%v, want errSSEFrameTooLarge", err)
	}
}

func TestGeminiSSEStreamRejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	body := "data: " + strings.Repeat("x", maxSSEFrameBytes+1) + "\n\n"
	s := newGeminiSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer s.Close()
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errSSEFrameTooLarge) {
		t.Fatalf("error=%v, want errSSEFrameTooLarge", err)
	}
}

func TestAnthropicSSEStreamEmitsTextFrame(t *testing.T) {
	t.Parallel()

	body := "event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"data: [DONE]\n\n"
	s := newAnthropicSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer s.Close()
	ev, err := s.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != "hi" {
		t.Fatalf("event=%+v, want text delta %q", ev, "hi")
	}
}

func TestGeminiSSEStreamEmitsTextFrame(t *testing.T) {
	t.Parallel()

	body := `data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}` + "\n\n"
	s := newGeminiSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer s.Close()
	ev, err := s.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != "hi" {
		t.Fatalf("event=%+v, want text delta %q", ev, "hi")
	}
}
