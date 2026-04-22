package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"google.golang.org/genai"
)

func TestHandleResponse_thoughtPart_emitsReasoningDelta(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					Text:    "step-1",
					Thought: true,
				}},
			},
		}},
	}
	if err := s.handleResponse(resp); err != nil {
		t.Fatal(err)
	}
	var deltas []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventReasoningDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if len(deltas) != 1 || deltas[0] != "step-1" {
		t.Fatalf("reasoning: %v", deltas)
	}
}

func TestHandleResponse_fileDataURI_emitsAssistantImageRef(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					FileData: &genai.FileData{
						FileURI:  "https://storage.example.com/model.png",
						MIMEType: "image/png",
					},
				}},
			},
		}},
	}
	if err := s.handleResponse(resp); err != nil {
		t.Fatal(err)
	}
	var refs []lipapi.Event
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventAssistantImageRef {
			refs = append(refs, ev)
		}
	}
	if len(refs) != 1 || refs[0].AssistantRef != "https://storage.example.com/model.png" {
		t.Fatalf("events: %+v", refs)
	}
}

func TestHandleResponse_fileDataURI_nonImage_emitsAssistantFileRef(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					FileData: &genai.FileData{
						FileURI:  "gs://bucket/reports/out.pdf",
						MIMEType: "application/pdf",
					},
				}},
			},
		}},
	}
	if err := s.handleResponse(resp); err != nil {
		t.Fatal(err)
	}
	var refs []lipapi.Event
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventAssistantFileRef {
			refs = append(refs, ev)
		}
	}
	if len(refs) != 1 || refs[0].AssistantRef != "gs://bucket/reports/out.pdf" {
		t.Fatalf("events: %+v", refs)
	}
}

func TestHandleResponse_functionCall_emitsToolEvents(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call-g1",
						Name: "get_weather",
						Args: map[string]any{"city": "NYC"},
					},
				}},
			},
		}},
	}
	if err := s.handleResponse(resp); err != nil {
		t.Fatal(err)
	}
	evs := stream.DrainPending(&s.pending)
	var kinds []lipapi.EventKind
	var args strings.Builder
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == lipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("args delta: %q", got)
	}
	var sawStart, sawFinish bool
	for _, ev := range evs {
		if ev.Kind == lipapi.EventToolCallStarted && ev.ToolCallID == "call-g1" && ev.ToolName == "get_weather" {
			sawStart = true
		}
		if ev.Kind == lipapi.EventToolCallFinished && ev.ToolCallID == "call-g1" {
			sawFinish = true
		}
	}
	if !sawStart || !sawFinish {
		t.Fatalf("tool lifecycle: start=%v finish=%v events=%v", sawStart, sawFinish, kinds)
	}
}

func TestHandleResponse_functionCall_marshalArgsError_wrapsAndPreservesCause(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call-g1",
						Name: "f",
						Args: map[string]any{"bad": make(chan int)},
					},
				}},
			},
		}},
	}
	err := s.handleResponse(resp)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gemini: marshal tool arguments") {
		t.Fatalf("got %q", err.Error())
	}
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected json.UnsupportedTypeError in chain, got %T", err)
	}
}

func TestGenaiStream_Recv_wrapsIteratorErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	seq := func(yield func(*genai.GenerateContentResponse, error) bool) {
		yield(nil, root)
	}
	es := newGenaiStream(seq)
	_, err := es.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gemini: recv stream") {
		t.Fatalf("got %q", err.Error())
	}
	if !errors.Is(err, root) {
		t.Fatalf("underlying: %v", err)
	}
}

func TestGenaiStream_Recv_afterClose_returnsEOF(t *testing.T) {
	t.Parallel()
	seq := func(yield func(*genai.GenerateContentResponse, error) bool) {
		_ = yield // empty iterator (no responses)
	}
	es := newGenaiStream(seq)
	if err := es.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := es.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("after close: want EOF, got %v", err)
	}
}

func TestGenaiStream_Close_idempotent_race(t *testing.T) {
	t.Parallel()
	seq := func(yield func(*genai.GenerateContentResponse, error) bool) {
		_ = yield(&genai.GenerateContentResponse{}, nil)
	}
	es := newGenaiStream(seq)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_ = es.Close()
		})
	}
	wg.Wait()
}
