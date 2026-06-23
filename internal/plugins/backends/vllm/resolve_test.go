package vllm

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
		{"vllm/meta-llama/Llama-3-8B-Instruct", "meta-llama/Llama-3-8B-Instruct"},
		{"vllm:meta-llama/Llama-3-8B-Instruct", "meta-llama/Llama-3-8B-Instruct"},
		{"meta-llama/Llama-3-8B-Instruct", "meta-llama/Llama-3-8B-Instruct"},
		{"other-backend/model", "other-backend/model"},
	}
	for _, tc := range cases {
		got := resolveModel(testResolveCandidate(tc.in), lipapi.Call{})
		if got != tc.want {
			t.Fatalf("resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
