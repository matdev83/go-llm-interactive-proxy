package nvidia

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestResolveModel_fromCandidate(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "nvidia/llama-3.1-nemotron-nano-8b-v1"}}
	call := lipapi.Call{}
	got := resolveModel(cand, call)
	if got != "nvidia/llama-3.1-nemotron-nano-8b-v1" {
		t.Fatalf("resolveModel = %q", got)
	}
}

func TestResolveModel_fallbackToExtension(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{}
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openailegacy.model": json.RawMessage(`"nvidia/model-from-ext"`),
		},
	}
	got := resolveModel(cand, call)
	if got != "nvidia/model-from-ext" {
		t.Fatalf("resolveModel = %q", got)
	}
}

func TestResolveModel_empty(t *testing.T) {
	t.Parallel()
	got := resolveModel(routing.AttemptCandidate{}, lipapi.Call{})
	if got != "" {
		t.Fatalf("resolveModel = %q, want empty", got)
	}
}

func TestResolveFlavor_defaultIsChat(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{}
	got := resolveFlavor(call)
	if got != openrouterwire.FlavorChat {
		t.Fatalf("resolveFlavor = %q, want %q", got, openrouterwire.FlavorChat)
	}
}

func TestResolveFlavor_responsesWhenExtensionSet(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
		},
	}
	got := resolveFlavor(call)
	if got != openrouterwire.FlavorResponses {
		t.Fatalf("resolveFlavor = %q, want %q", got, openrouterwire.FlavorResponses)
	}
}

func TestResolveFlavor_responsesWhenFrontendModelExtension(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openairesponses.model": json.RawMessage(`"nvidia/model"`),
		},
	}
	got := resolveFlavor(call)
	if got != openrouterwire.FlavorResponses {
		t.Fatalf("resolveFlavor = %q, want %q", got, openrouterwire.FlavorResponses)
	}
}

func TestResolveFlavor_chatWhenLegacyModelExtension(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openailegacy.model": json.RawMessage(`"nvidia/model"`),
		},
	}
	got := resolveFlavor(call)
	if got != openrouterwire.FlavorChat {
		t.Fatalf("resolveFlavor = %q, want %q", got, openrouterwire.FlavorChat)
	}
}
