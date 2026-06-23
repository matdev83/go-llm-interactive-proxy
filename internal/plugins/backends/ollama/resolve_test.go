package ollama

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testResolveCandidate(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: model}}
}

func TestResolveModel_localStripsBackendAndVendorPrefix(t *testing.T) {
	t.Parallel()
	resolve := resolveModelForMode(backendModeLocal)
	cases := []struct {
		in, want string
	}{
		{"ollama/llama3:latest", "llama3:latest"},
		{"ollama:llama3:latest", "llama3:latest"},
		{"ollama:google/gemma3:4b", "gemma3:4b"},
		{"llama3:latest", "llama3:latest"},
		{"google/gemma3:4b", "gemma3:4b"},
		{"unknown/llama3:latest", "llama3:latest"},
		{"gemma3:4b-cloud", "gemma3:4b-cloud"},
	}
	for _, tc := range cases {
		got := resolve(testResolveCandidate(tc.in), lipapi.Call{})
		if got != tc.want {
			t.Fatalf("local resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveModel_localStripsUnexpectedBackendPrefixAsVendorSegment(t *testing.T) {
	t.Parallel()
	resolve := resolveModelForMode(backendModeLocal)
	got := resolve(testResolveCandidate("ollama-cloud/gemma3:4b"), lipapi.Call{})
	if got != "gemma3:4b" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveModel_cloudStripsPrefixAndAppendsCloudSuffix(t *testing.T) {
	t.Parallel()
	resolve := resolveModelForMode(backendModeCloud)
	cases := []struct {
		in, want string
	}{
		{"ollama-cloud/gemma3:4b", "gemma3:4b-cloud"},
		{"ollama-cloud:gemma3:4b", "gemma3:4b-cloud"},
		{"google/gemma3:4b", "gemma3:4b-cloud"},
		{"gemma3:4b", "gemma3:4b-cloud"},
		{"gemma3:4b-cloud", "gemma3:4b-cloud"},
		{"ollama-cloud/deepseek-v3.2-cloud", "deepseek-v3.2-cloud"},
	}
	for _, tc := range cases {
		got := resolve(routing.AttemptCandidate{Primary: routing.Primary{Model: tc.in}}, lipapi.Call{})
		if got != tc.want {
			t.Fatalf("cloud resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
