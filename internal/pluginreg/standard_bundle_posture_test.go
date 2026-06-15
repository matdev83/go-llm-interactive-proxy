package pluginreg

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/localstub"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
)

// bundledBackendFactoryIDs must stay aligned with installBackends in standard_table.go:
// every bundled backend must register an explicit non-unknown credential posture.
var bundledBackendFactoryIDs = []string{
	openairesponses.ID,
	openailegacy.ID,
	anthropic.ID,
	gemini.ID,
	bedrock.ID,
	acp.ID,
	openrouter.ID,
	localstub.ID,
}

func TestInstallStandardBackendsOn_declaresExplicitNonUnknownPosture(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range bundledBackendFactoryIDs {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			p, ok := reg.BackendSecurityProfile(id)
			if !ok {
				t.Fatalf("missing security profile for bundled backend factory %q", id)
			}
			if p.CredentialMode == CredentialUnknown || p.CredentialMode == "" {
				t.Fatalf("bundled backend %q must declare explicit non-unknown posture, got %q", id, p.CredentialMode)
			}
		})
	}
}
