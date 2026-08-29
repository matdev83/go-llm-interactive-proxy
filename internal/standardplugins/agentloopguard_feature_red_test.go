package standardplugins

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"gopkg.in/yaml.v3"
)

func TestStandardBundle_AgentLoopGuardDisabledContributesNoProvider(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: false"), &node); err != nil {
		t.Fatal(err)
	}
	bundle, err := testRegistryWithStdBundle(t).BuildFeatureBundle(agentloopguard.ID, node)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	if prov := lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); prov != nil {
		t.Fatal("disabled ALG must contribute no terminal decision provider")
	}
}

func TestStandardBundle_AgentLoopGuardEnabledContributesSingularProvider(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nmax_semantic_continuations: 2"), &node); err != nil {
		t.Fatal(err)
	}
	bundle, err := testRegistryWithStdBundle(t).BuildFeatureBundle(agentloopguard.ID, node)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	if prov := lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); prov == nil {
		t.Fatal("enabled ALG must contribute the singular terminal decision provider")
	}
	if id, ok := lipfeature.FrozenIdentity(bundle.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); !ok || id == "" {
		t.Fatalf("provider identity missing or empty on PlaneSet: id=%q ok=%v", id, ok)
	}
}

func TestStandardBundle_AgentLoopGuardInvalidConfigFailsBeforeBundle(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nmax_semantic_continuations: 0"), &node); err != nil {
		t.Fatal(err)
	}
	if _, err := testRegistryWithStdBundle(t).BuildFeatureBundle(agentloopguard.ID, node); err == nil {
		t.Fatal("invalid ALG config must fail before FeatureBundle construction")
	}
}
