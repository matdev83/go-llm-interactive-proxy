package lipapi_test

import (
	"net/url"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestMergeRouteQueryIntoGenerationOptions_emptyQuery(t *testing.T) {
	t.Parallel()
	base := lipapi.GenerationOptions{}
	got, err := lipapi.MergeRouteQueryIntoGenerationOptions(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Temperature != nil || got.TopP != nil {
		t.Fatalf("unexpected merge: %#v", got)
	}
}

func TestMergeRouteQueryIntoGenerationOptions_fillsFromRoute(t *testing.T) {
	t.Parallel()
	q := url.Values{}
	q.Set("temperature", "0.2")
	q.Set("reasoning_effort", "high")
	q.Set("max_output_tokens", "4096")
	q.Set("top_p", "0.9")
	q.Set("parallel_tool_calls", "false")

	got, err := lipapi.MergeRouteQueryIntoGenerationOptions(lipapi.GenerationOptions{}, q)
	if err != nil {
		t.Fatal(err)
	}
	if got.Temperature == nil || *got.Temperature != 0.2 {
		t.Fatalf("temperature: %#v", got.Temperature)
	}
	if got.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort: %q", got.ReasoningEffort)
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 4096 {
		t.Fatalf("max_output_tokens: %#v", got.MaxOutputTokens)
	}
	if got.TopP == nil || *got.TopP != 0.9 {
		t.Fatalf("top_p: %#v", got.TopP)
	}
	if got.ParallelToolCalls == nil || *got.ParallelToolCalls != false {
		t.Fatalf("parallel_tool_calls: %#v", got.ParallelToolCalls)
	}
}

func TestMergeRouteQueryIntoGenerationOptions_callWinsOverRoute(t *testing.T) {
	t.Parallel()
	temp := 0.1
	base := lipapi.GenerationOptions{Temperature: &temp}
	q := url.Values{}
	q.Set("temperature", "0.99")

	got, err := lipapi.MergeRouteQueryIntoGenerationOptions(base, q)
	if err != nil {
		t.Fatal(err)
	}
	if got.Temperature == nil || *got.Temperature != 0.1 {
		t.Fatalf("call temperature should win, got %#v", got.Temperature)
	}
}

func TestMergeRouteQueryIntoGenerationOptions_maxTokensAlias(t *testing.T) {
	t.Parallel()
	q := url.Values{}
	q.Set("max_tokens", "128")
	got, err := lipapi.MergeRouteQueryIntoGenerationOptions(lipapi.GenerationOptions{}, q)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 128 {
		t.Fatalf("max_tokens alias: %#v", got.MaxOutputTokens)
	}
}

func TestMergeRouteQueryIntoGenerationOptions_invalidTemperature(t *testing.T) {
	t.Parallel()
	q := url.Values{}
	q.Set("temperature", "not-a-float")
	_, err := lipapi.MergeRouteQueryIntoGenerationOptions(lipapi.GenerationOptions{}, q)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMergeRouteQueryIntoGenerationOptions_invalidRange(t *testing.T) {
	t.Parallel()
	q := url.Values{}
	q.Set("temperature", "99")
	_, err := lipapi.MergeRouteQueryIntoGenerationOptions(lipapi.GenerationOptions{}, q)
	if err == nil {
		t.Fatal("expected validation error from GenerationOptions.validate")
	}
}
