package lmstudio

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testResolveCandidate(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: model}}
}

func TestResolveModel_stripsBackendPrefixOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"lmstudio/llama-3", "llama-3"},
		{"lmstudio:llama-3", "llama-3"},
		{"llama-3", "llama-3"},
		{"meta/llama-3", "meta/llama-3"},
		{"openai/gpt-4o", "openai/gpt-4o"},
	}
	for _, tc := range cases {
		got := resolveModel(testResolveCandidate(tc.in), lipapi.Call{})
		if got != tc.want {
			t.Fatalf("resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
