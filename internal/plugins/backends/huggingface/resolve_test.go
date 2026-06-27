package huggingface

import (
	"net/url"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestApplyProviderSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		model string
		cand  routing.AttemptCandidate
		want  string
	}{
		{
			name:  "appends slug when no suffix present",
			model: "openai/gpt-oss-120b",
			cand: routing.AttemptCandidate{Primary: routing.Primary{
				Model:  "openai/gpt-oss-120b",
				Params: url.Values{"provider": []string{"sambanova"}},
			}},
			want: "openai/gpt-oss-120b:sambanova",
		},
		{
			name:  "does not append when model already has suffix",
			model: "openai/gpt-oss-120b:fastest",
			cand: routing.AttemptCandidate{Primary: routing.Primary{
				Model:  "openai/gpt-oss-120b:fastest",
				Params: url.Values{"provider": []string{"sambanova"}},
			}},
			want: "openai/gpt-oss-120b:fastest",
		},
		{
			name:  "whitespace-only provider leaves model unchanged",
			model: "openai/gpt-oss-120b",
			cand: routing.AttemptCandidate{Primary: routing.Primary{
				Model:  "openai/gpt-oss-120b",
				Params: url.Values{"provider": []string{"  "}},
			}},
			want: "openai/gpt-oss-120b",
		},
		{
			name:  "missing provider param leaves model unchanged",
			model: "openai/gpt-oss-120b",
			cand: routing.AttemptCandidate{Primary: routing.Primary{
				Model:  "openai/gpt-oss-120b",
				Params: url.Values{},
			}},
			want: "openai/gpt-oss-120b",
		},
		{
			name:  "nil params leaves model unchanged",
			model: "openai/gpt-oss-120b",
			cand: routing.AttemptCandidate{Primary: routing.Primary{
				Model: "openai/gpt-oss-120b",
			}},
			want: "openai/gpt-oss-120b",
		},
		{
			name:  "trims whitespace around provider",
			model: "openai/gpt-oss-120b",
			cand: routing.AttemptCandidate{Primary: routing.Primary{
				Model:  "openai/gpt-oss-120b",
				Params: url.Values{"provider": []string{"  sambanova  "}},
			}},
			want: "openai/gpt-oss-120b:sambanova",
		},
		{
			name:  "model without slash appends suffix",
			model: "gpt-oss-120b",
			cand: routing.AttemptCandidate{Primary: routing.Primary{
				Model:  "gpt-oss-120b",
				Params: url.Values{"provider": []string{"sambanova"}},
			}},
			want: "gpt-oss-120b:sambanova",
		},
		{
			name:  "empty model unchanged",
			model: "",
			cand: routing.AttemptCandidate{Primary: routing.Primary{
				Model:  "",
				Params: url.Values{"provider": []string{"sambanova"}},
			}},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := applyProviderSuffix(tc.model, tc.cand)
			if got != tc.want {
				t.Fatalf("applyProviderSuffix(%q, %+v) = %q, want %q", tc.model, tc.cand, got, tc.want)
			}
		})
	}
}

func TestResolveModelWithProviderUsesConfiguredPolicy(t *testing.T) {
	t.Parallel()

	cand := routing.AttemptCandidate{Primary: routing.Primary{
		Model:  ID + ":openai/gpt-oss-120b",
		Params: url.Values{"provider": []string{"sambanova"}},
	}}
	got := resolveModelWithProvider(openaifamily.ModelResolutionStripBackendPrefix, cand, lipapi.Call{})
	if got != "openai/gpt-oss-120b:sambanova" {
		t.Fatalf("model = %q, want %q", got, "openai/gpt-oss-120b:sambanova")
	}
}
