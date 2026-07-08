package standardplugins

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/agycliacp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/codexappserver"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorcliacp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/geminicliacp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
)

func TestInstallStandardBackendsOn_declaresExplicitNonUnknownPosture(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range StandardBackendBundle(UpstreamAPIKeys{}).Backends {
		id := entry.ID
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			p, ok := reg.BackendSecurityProfile(id)
			if !ok {
				t.Fatalf("missing security profile for bundled backend factory %q", id)
			}
			if p.CredentialMode == pluginreg.CredentialUnknown || p.CredentialMode == "" {
				t.Fatalf("bundled backend %q must declare explicit non-unknown posture, got %q", id, p.CredentialMode)
			}
		})
	}
}

func TestStandardBackends_localOnlyConnectorsDeclareLocalOnlyScope(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	localOnly := []string{
		acp.ID,
		cursorcliacp.ID,
		geminicliacp.ID,
		agycliacp.ID,
		openaicodex.ID,
		codexappserver.ID,
	}
	for _, id := range localOnly {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			p, ok := reg.BackendSecurityProfile(id)
			if !ok {
				t.Fatalf("missing security profile for bundled backend factory %q", id)
			}
			if p.AccessScope != pluginreg.BackendAccessLocalOnly {
				t.Fatalf("bundled backend %q must be local-only, got %q", id, p.AccessScope)
			}
		})
	}
}
