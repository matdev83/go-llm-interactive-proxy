package standardplugins

import (
	"bytes"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"gopkg.in/yaml.v3"
)

func TestExpandProviderProfileRows_bindsCatalogDataWithoutChangingCustomRows(t *testing.T) {
	var profileNode, customNode yaml.Node
	if err := yaml.Unmarshal([]byte("profile: example-openai-responses\n"), &profileNode); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte("backend_prefix: private\nbase_url: https://private.example/v1\n"), &customNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{ID: "profile-instance", Kind: ProviderProfileKind, Enabled: true, Config: profileNode},
		{ID: "custom-instance", Kind: CustomOpenAIResponsesCompatibleID, Enabled: true, Config: customNode},
	}}}
	before := cfg.Plugins.Backends[1].Config.Value
	profileBefore := cfg.Plugins.Backends[0].Config.Value
	prepared, err := ExpandProviderProfileRows(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Plugins.Backends[0].Kind != ProviderProfileKind {
		t.Fatalf("source profile kind=%q", cfg.Plugins.Backends[0].Kind)
	}
	if cfg.Plugins.Backends[0].Config.Value != profileBefore {
		t.Fatal("source profile config changed")
	}
	if prepared.Plugins.Backends[0].Kind != CustomOpenAIResponsesCompatibleID {
		t.Fatalf("profile family kind=%q", prepared.Plugins.Backends[0].Kind)
	}
	if prepared.Plugins.Backends[1].Kind != CustomOpenAIResponsesCompatibleID {
		t.Fatal("custom row changed kind")
	}
	if !bytes.Equal([]byte(before), []byte(cfg.Plugins.Backends[1].Config.Value)) {
		t.Fatal("custom row config changed")
	}
}

func TestExpandProviderProfileRows_rejectsUnknownProfileBeforeActivation(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile_id: does-not-exist\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "missing", Kind: ProviderProfileKind, Enabled: true, Config: node}}}}
	if _, err := ExpandProviderProfileRows(cfg); err == nil {
		t.Fatal("unknown profile accepted")
	}
}
