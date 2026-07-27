package manifest_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
)

func TestManifestValidate_Minimal(t *testing.T) {
	t.Parallel()
	m := manifest.Manifest{
		Schema: manifest.SchemaV1, PluginID: "io.x.y", Version: "1", BuildID: "b",
		Executable: "bin/x", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProtocolMajor: 1, Platforms: []manifest.Platform{{OS: "linux", Arch: "amd64"}},
		Exports: []manifest.Export{{
			Kind: "k", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
}
