package pluginreg

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"gopkg.in/yaml.v3"
)

func TestBuildBackend_openAIResponses_multiKeyYAML_oneInstance(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{OpenAI: []string{"env-should-not-apply"}}); err != nil {
		t.Fatal(err)
	}
	raw := `api_key: first
api_keys:
  - second
  - third
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(openairesponses.ID, node, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected backend Open")
	}
}

func TestBuildBackend_openAIResponses_envDefaultsWhenYAMLHasNoKeys(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{OpenAI: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	raw := `base_url: https://api.openai.com/v1`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(openairesponses.ID, node, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected backend Open")
	}
}
