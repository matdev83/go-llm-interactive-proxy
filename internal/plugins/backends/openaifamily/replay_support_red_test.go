package openaifamily_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestResolveReplaySupport_unprovenModelEmpty_RED(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openailegacy.model": json.RawMessage(`"gpt-4o-mini"`),
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-legacy", Model: "gpt-4o-mini"}}
	got := openaifamily.ResolveReplaySupport(t.Context(), call, cand)
	if len(got.Dialects) != 0 {
		t.Fatalf("RED: unproven model must resolve empty ReplaySupport dialects, got %v", got.Dialects)
	}
}

func TestResolveReplaySupport_kimiChatDialect_RED(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openailegacy.model": json.RawMessage(`"moonshotai/kimi-k2"`),
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openrouter", Model: "moonshotai/kimi-k2"}}
	got := openaifamily.ResolveReplaySupport(t.Context(), call, cand)
	if len(got.Dialects) != 1 || got.Dialects[0] != lipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatalf("RED: kimi chat flavor must resolve chat text dialect, got %v", got.Dialects)
	}
	if openaifamily.ResolveFlavor(call) != openaicompat.FlavorChat {
		t.Fatalf("ResolveFlavor = %q, want chat", openaifamily.ResolveFlavor(call))
	}
}

func TestResolveReplaySupport_kimiResponsesDialect_RED(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openairesponses.model": json.RawMessage(`"moonshotai/kimi-k2"`),
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openrouter", Model: "moonshotai/kimi-k2"}}
	got := openaifamily.ResolveReplaySupport(t.Context(), call, cand)
	if len(got.Dialects) != 1 || got.Dialects[0] != lipapi.ReasoningDialectOpenAIResponsesItemV1 {
		t.Fatalf("RED: kimi responses flavor must resolve responses item dialect, got %v", got.Dialects)
	}
}
