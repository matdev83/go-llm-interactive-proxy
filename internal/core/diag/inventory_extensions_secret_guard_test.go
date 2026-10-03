package diag

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestBuildInventoryExtensions_secretGuardMetadataNoSyntheticSecrets(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "sg-main", Kind: "secrets-guard", Enabled: true},
			},
		},
	}
	ext := buildInventoryExtensions(t.Context(), cfg, &InventoryExtras{
		SecretGuardCatalogEntryCount: 3,
		SecretGuardSourceCategories:  []string{"proxy_env", "popular_env"},
		SecretGuardAccessMode:        "single_user",
		SecretGuardAction:            "block",
		Registrations: []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "sg-main", Enabled: true, FactoryKind: "secrets-guard"},
		},
	})
	if len(ext.Features) != 1 {
		t.Fatalf("features %d", len(ext.Features))
	}
	sg := ext.Features[0].SecretGuard
	if sg == nil {
		t.Fatal("missing secret_guard inventory")
	}
	if sg.InstanceID != "sg-main" || sg.Action != "block" || sg.AccessMode != "single_user" {
		t.Fatalf("secret_guard=%#v", sg)
	}
	if sg.CatalogEntryCount != 3 {
		t.Fatalf("catalog_entry_count=%d", sg.CatalogEntryCount)
	}
	if len(sg.SourceCategories) != 2 {
		t.Fatalf("source_categories=%#v", sg.SourceCategories)
	}

	raw, err := json.Marshal(ext)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		"sk-test-openai-secretguard-fixture-001",
		"sk-or-test-secretguard-fixture-002",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("inventory JSON must not contain %q", forbidden)
		}
	}
}

func TestBuildInventoryExtensions_secretGuardEnabledZeroFactsRetainPresence(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "sg-zero", Kind: "secrets-guard", Enabled: true}},
		},
	}
	ext := buildInventoryExtensions(t.Context(), cfg, &InventoryExtras{
		SecretGuardAccessMode:             "single_user",
		SecretGuardAction:                 "block",
		SecretGuardBetterLeaksEnabled:     true,
		SecretGuardBetterLeaksVersion:     "v2.0.0-rc.1",
		SecretGuardBetterLeaksConfigHash:  "config-hash",
		SecretGuardBetterLeaksRuleCount:   0,
		SecretGuardBetterLeaksConfidence:  "medium",
		SecretGuardBetterLeaksDecodeDepth: 0,
		SecretGuardBetterLeaksWorkers:     1,
		SecretGuardDiscoveryDetectorCount: 1,
	})
	if len(ext.Features) != 1 || ext.Features[0].SecretGuard == nil {
		t.Fatalf("secret_guard inventory missing: %#v", ext.Features)
	}
	sg := ext.Features[0].SecretGuard
	if sg.BetterLeaksRuleCount == nil || *sg.BetterLeaksRuleCount != 0 {
		t.Fatalf("enabled zero BetterLeaks rule count lost presence: %#v", sg.BetterLeaksRuleCount)
	}
	if sg.BetterLeaksDecodeDepth == nil || *sg.BetterLeaksDecodeDepth != 0 {
		t.Fatalf("enabled zero BetterLeaks decode depth lost presence: %#v", sg.BetterLeaksDecodeDepth)
	}

	raw, err := json.Marshal(sg)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"betterleaks_active_rule_count", "betterleaks_max_decode_depth"} {
		value, present := fields[key]
		if !present || value != float64(0) {
			t.Fatalf("enabled zero-valued %s must remain present as 0, present=%t value=%v", key, present, value)
		}
	}
}
