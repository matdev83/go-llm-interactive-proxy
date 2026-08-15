package manifest_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
)

func TestManifestValidate_ExecutionClass(t *testing.T) {
	t.Parallel()
	validBase := func(exec lipsdk.BackendExecutionClass) manifest.Manifest {
		return manifest.Manifest{
			Schema: manifest.SchemaV1, PluginID: "io.x.y", Version: "1", BuildID: "b",
			Executable: "bin/x", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ProtocolMajor: 1, Platforms: []manifest.Platform{{OS: "linux", Arch: "amd64"}},
			Exports: []manifest.Export{{
				Kind: "k", CredentialMode: backendplugin.CredentialModeNone,
				AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
				ExecutionClass: exec,
			}},
		}
	}

	if err := validBase(lipsdk.BackendExecutionUnknown).Validate(); err != nil {
		t.Fatalf("omitted/unknown execution class should be valid, got: %v", err)
	}
	if err := validBase(lipsdk.BackendExecutionInference).Validate(); err != nil {
		t.Fatalf("inference execution class should be valid, got: %v", err)
	}
	if err := validBase(lipsdk.BackendExecutionAgentRuntime).Validate(); err != nil {
		t.Fatalf("agent_runtime execution class should be valid, got: %v", err)
	}
	if err := validBase("invalid_class").Validate(); err == nil {
		t.Fatal("invalid execution class should fail validation, got nil")
	}
}
