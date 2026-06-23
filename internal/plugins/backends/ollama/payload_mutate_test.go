package ollama

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRequestOptions_noMutationWhenMaxOutputTokensNil(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{}
	opts := requestOptions(call)
	if len(opts) != 0 {
		t.Fatalf("expected 0 options, got %d", len(opts))
	}
}

func TestRequestOptions_addsMaxTokensWhenSet(t *testing.T) {
	t.Parallel()
	maxTokens := 1024
	call := lipapi.Call{
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTokens},
	}
	opts := requestOptions(call)
	if len(opts) != 2 {
		t.Fatalf("expected 2 options, got %d", len(opts))
	}
}

func TestRequestOptions_noMaxTokensWhenZero(t *testing.T) {
	t.Parallel()
	maxTokens := 0
	call := lipapi.Call{
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTokens},
	}
	opts := requestOptions(call)
	if len(opts) != 0 {
		t.Fatalf("expected 0 options, got %d", len(opts))
	}
}
