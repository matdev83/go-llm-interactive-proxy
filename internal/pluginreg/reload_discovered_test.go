package pluginreg_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestReloadDiscovered_NoRescanNoInstallAfterFreeze(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	empty := yaml.Node{Kind: yaml.MappingNode}
	if err := reg.RegisterDiscoveredBackend("discovered-stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			BackendPrefixes: []string{"discovered-stub"},
			ModelInventory: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: []modelinventory.Model{{CanonicalID: "discovered-stub/m", NativeID: "m", DisplayName: "m"}},
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}, pluginreg.BackendReloadPolicy{
		AllowsCandidateOverlap: true,
	}); err != nil {
		t.Fatal(err)
	}
	reg.FreezeDiscovery()

	if err := reg.RescanTrustedDirectories([]string{"/tmp/not-scanned"}); !errors.Is(err, pluginreg.ErrDiscoveryFrozen) {
		t.Fatalf("rescan after freeze: %v", err)
	}
	if got := reg.RescanAttempts(); got != 1 {
		t.Fatalf("rescan attempts=%d want 1", got)
	}
	if err := reg.InstallConnectorArtifact("/tmp/fake-plugin"); !errors.Is(err, pluginreg.ErrDiscoveryFrozen) {
		t.Fatalf("install after freeze: %v", err)
	}
	if got := reg.InstallAttempts(); got != 1 {
		t.Fatalf("install attempts=%d want 1", got)
	}
	// Activation of already discovered kind still works without rescan/install.
	if !reg.HasBackend("discovered-stub") {
		t.Fatal("discovered kind must remain registered")
	}
	if _, err := reg.BuildBackend("discovered-stub", empty, nil, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatalf("activate discovered: %v", err)
	}
	if got := reg.RescanAttempts(); got != 1 {
		t.Fatalf("BuildBackend must not rescan; attempts=%d", got)
	}
	if got := reg.InstallAttempts(); got != 1 {
		t.Fatalf("BuildBackend must not install; attempts=%d", got)
	}
}

func TestReloadDiscovered_OverlapPolicyDefaultsAllow(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("plain", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	pol, ok := reg.BackendReloadPolicy("plain")
	if !ok {
		t.Fatal("expected default reload policy")
	}
	if !pol.AllowsCandidateOverlap {
		t.Fatal("default must allow candidate overlap")
	}
}
