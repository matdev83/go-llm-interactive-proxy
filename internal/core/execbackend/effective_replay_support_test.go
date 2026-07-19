package execbackend_test

import (
	"context"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestEffectiveReplaySupport_normalizesDedupesSortsOmitsEmpty(t *testing.T) {
	t.Parallel()
	be := execbackend.Backend{
		ReplaySupport: lipapi.ReasoningReplaySupport{
			Dialects: []lipapi.ReasoningDialect{
				"  OpenAI.Chat.Reasoning_Text.V1 ",
				"",
				"   ",
				lipapi.ReasoningDialectAnthropicThinkingV1,
				lipapi.ReasoningDialectOpenAIChatTextV1,
				lipapi.ReasoningDialectAnthropicThinkingV1,
			},
		},
	}
	got := execbackend.EffectiveReplaySupport(context.Background(), be, lipapi.Call{}, routing.AttemptCandidate{})
	want := []lipapi.ReasoningDialect{
		lipapi.ReasoningDialectAnthropicThinkingV1,
		lipapi.ReasoningDialectOpenAIChatTextV1,
	}
	if !slices.Equal(got.Dialects, want) {
		t.Fatalf("got %#v want %#v", got.Dialects, want)
	}
}

func TestEffectiveReplaySupport_resolvePathNormalized(t *testing.T) {
	t.Parallel()
	be := execbackend.Backend{
		ResolveReplaySupport: func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.ReasoningReplaySupport {
			return lipapi.ReasoningReplaySupport{
				Dialects: []lipapi.ReasoningDialect{" Anthropic.Thinking.V1 ", ""},
			}
		},
	}
	got := execbackend.EffectiveReplaySupport(context.Background(), be, lipapi.Call{}, routing.AttemptCandidate{})
	if len(got.Dialects) != 1 || got.Dialects[0] != lipapi.ReasoningDialectAnthropicThinkingV1 {
		t.Fatalf("got %#v", got.Dialects)
	}
}
