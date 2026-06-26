package openaiusage

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func TestChatUsageEvent_cacheAndProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{
  "prompt_tokens": 11,
  "completion_tokens": 8,
  "total_tokens": 19,
  "prompt_tokens_details": {
    "cached_tokens": 3,
    "x_lip_cache_write_tokens": 2
  },
  "completion_tokens_details": {"reasoning_tokens": 5},
  "cost": 0.00014
}`
	var usage openai.CompletionUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}

	ev := ChatUsageEvent(usage)
	if ev.InputTokens != 11 || ev.OutputTokens != 8 {
		t.Fatalf("tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	if ev.CacheReadTokens != 3 || ev.CacheWriteTokens != 2 {
		t.Fatalf("cache: read=%d write=%d", ev.CacheReadTokens, ev.CacheWriteTokens)
	}
	if ev.ReasoningTokens != 5 || ev.TotalTokens != 19 {
		t.Fatalf("reasoning=%d total=%d", ev.ReasoningTokens, ev.TotalTokens)
	}
	if ev.CostNanoUnits != 140_000 {
		t.Fatalf("CostNanoUnits = %d, want 140000", ev.CostNanoUnits)
	}
	if ev.Currency != "USD" || ev.CostSource != accounting.CostSourceProviderReported {
		t.Fatalf("cost meta: currency=%q source=%q", ev.Currency, ev.CostSource)
	}
}

func TestResponsesUsageEvent_cacheAndProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{
  "input_tokens": 11,
  "output_tokens": 8,
  "total_tokens": 19,
  "input_tokens_details": {
    "cached_tokens": 3,
    "x_lip_cache_write_tokens": 2
  },
  "output_tokens_details": {"reasoning_tokens": 5},
  "cost": 0.00014
}`
	var usage responses.ResponseUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}

	ev := ResponsesUsageEvent(usage)
	if ev.CacheReadTokens != 3 || ev.CacheWriteTokens != 2 {
		t.Fatalf("cache: read=%d write=%d", ev.CacheReadTokens, ev.CacheWriteTokens)
	}
	if ev.CostNanoUnits != 140_000 {
		t.Fatalf("CostNanoUnits = %d, want 140000", ev.CostNanoUnits)
	}
	if ev.CostSource != accounting.CostSourceProviderReported {
		t.Fatalf("CostSource = %q", ev.CostSource)
	}
}

func TestChatUsageEvent_ignoresMissingProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}`
	var usage openai.CompletionUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}

	ev := ChatUsageEvent(usage)
	if ev.CostNanoUnits != 0 || ev.CostSource != "" {
		t.Fatalf("unexpected cost fields: %+v", ev)
	}
}

func TestProviderCostNanoUnits_roundsRationalExactly(t *testing.T) {
	t.Parallel()
	nano, ok := providerCostNanoUnits("0.0000000015")
	if !ok {
		t.Fatal("providerCostNanoUnits returned !ok")
	}
	if nano != 2 {
		t.Fatalf("nano = %d, want 2", nano)
	}
}
