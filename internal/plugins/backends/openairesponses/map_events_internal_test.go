package openairesponses

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/responses"
)

func TestHandleUnion_textDeltaThenCompleted_noDuplicateText(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}

	s.handleUnion(responses.ResponseStreamEventUnion{
		Type:  "response.output_text.delta",
		Delta: "hel",
	})
	s.handleUnion(responses.ResponseStreamEventUnion{
		Type:  "response.output_text.delta",
		Delta: "lo",
	})

	resp := responses.Response{
		ID:     "r1",
		Status: "completed",
		Output: []responses.ResponseOutputItemUnion{
			{
				Type: "message",
				Content: []responses.ResponseOutputMessageContentUnion{
					{Type: "output_text", Text: "hello"},
				},
			},
		},
	}
	s.handleUnion(responses.ResponseStreamEventUnion{
		Type:     "response.completed",
		Response: resp,
	})

	var texts []string
	for _, ev := range s.pending {
		if ev.Kind == lipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
	}
	if got := len(texts); got != 2 {
		t.Fatalf("expected 2 text deltas (from incremental only), got %d: %v", got, texts)
	}
	combined := texts[0] + texts[1]
	if combined != "hello" {
		t.Fatalf("combined text: %q", combined)
	}
}

func TestHandleUnion_completedOnly_emitsFullText(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}

	resp := responses.Response{
		ID:     "r2",
		Status: "completed",
		Output: []responses.ResponseOutputItemUnion{
			{
				Type: "message",
				Content: []responses.ResponseOutputMessageContentUnion{
					{Type: "output_text", Text: "done"},
				},
			},
		},
	}
	s.handleUnion(responses.ResponseStreamEventUnion{
		Type:     "response.completed",
		Response: resp,
	})

	var texts []string
	for _, ev := range s.pending {
		if ev.Kind == lipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
	}
	if got := len(texts); got != 1 {
		t.Fatalf("expected 1 text delta (from completed), got %d: %v", got, texts)
	}
	if texts[0] != "done" {
		t.Fatalf("text: %q", texts[0])
	}
}
