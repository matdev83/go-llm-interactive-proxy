package secretsguard_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestEnabledRegistrations_GenerationFeatureUniquenessRejectsDuplicates(t *testing.T) {
	t.Parallel()
	_, err := secretsguard.EnabledRegistrations([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "sg-a", FactoryKind: "secrets-guard", Enabled: true},
		{Kind: lipsdk.PluginKindFeature, ID: "sg-b", FactoryKind: "secrets-guard", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected uniqueness violation")
	}
	if !strings.Contains(err.Error(), "secrets-guard") {
		t.Fatalf("err=%v", err)
	}
}

func TestComposeRuntimeConfig_GenerationFeatureUniquenessBeforeDecode(t *testing.T) {
	t.Parallel()
	_, err := secretsguard.ComposeRuntimeConfig("single_user", []lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "sg-a", FactoryKind: "secrets-guard", Enabled: true},
		{Kind: lipsdk.PluginKindFeature, ID: "sg-b", FactoryKind: "secrets-guard", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected uniqueness violation")
	}
}
