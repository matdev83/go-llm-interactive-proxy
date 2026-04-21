package gemini

import (
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
	s.handleResponse(resp)
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
	s.handleResponse(resp)
	evs := stream.DrainPending(&s.pending)
	var kinds []lipapi.EventKind
	var args string
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == lipapi.EventToolCallArgsDelta {
			args += ev.Delta
		}
	}
	if args != `{"city":"NYC"}` {
		t.Fatalf("args delta: %q", args)
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
