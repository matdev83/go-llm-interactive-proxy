package lipsdk_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestValidateRegistrations_allowsTwoBackendsSameFactoryDifferentInstance(t *testing.T) {
	t.Parallel()

	mandatory := []lipsdk.Requirement{{Kind: lipsdk.PluginKindBackend, ID: "openai-responses"}}
	err := lipsdk.ValidateRegistrations([]lipsdk.Registration{
		{ID: "openai-primary", FactoryKind: "openai-responses", Kind: lipsdk.PluginKindBackend, Enabled: true},
		{ID: "openai-fallback", FactoryKind: "openai-responses", Kind: lipsdk.PluginKindBackend, Enabled: true},
	}, mandatory)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
