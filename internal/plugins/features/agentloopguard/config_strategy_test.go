package agentloopguard

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStrategyOmissionPreservesSemanticVerifier(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\n"), &node); err != nil { t.Fatal(err) }
	cfg, err := DecodeConfig(node)
	if err != nil { t.Fatal(err) }
	if cfg.Strategy != StrategySemanticVerifier || cfg.VerifierRole != DefaultVerifierRole || cfg.MaxProtocolReprompts != 0 {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestAttemptCompletionConfigMutualExclusion(t *testing.T) {
	for _, raw := range []string{
		"enabled: true\nstrategy: attempt_completion\nverifier_role: loop_guard\n",
		"enabled: true\nstrategy: attempt_completion\nverifier_timeout_seconds: 4\n",
		"enabled: true\nstrategy: attempt_completion\nmax_semantic_continuations: 3\n",
		"enabled: true\nstrategy: attempt_completion\nexplicit_completion_policy: trust\n",
		"enabled: true\nstrategy: semantic_verifier\nmax_protocol_reprompts: 1\n",
	} {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &node); err != nil { t.Fatal(err) }
		if _, err := DecodeConfig(node); err == nil {
			t.Fatalf("DecodeConfig(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestAttemptCompletionDefaultsAndBounds(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nstrategy: attempt_completion\n"), &node); err != nil { t.Fatal(err) }
	cfg, err := DecodeConfig(node)
	if err != nil { t.Fatal(err) }
	if cfg.Strategy != StrategyAttemptCompletion || cfg.MaxProtocolReprompts != 1 || cfg.NoProgressLimit != DefaultNoProgressLimit {
		t.Fatalf("cfg=%+v", cfg)
	}
	for _, n := range []int{0,4} {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte("enabled: true\nstrategy: attempt_completion\nmax_protocol_reprompts: "+strings.TrimSpace(strings.Repeat(" ",0))+fmtInt(n)+"\n"), &node); err != nil { t.Fatal(err) }
		if _, err := DecodeConfig(node); err == nil { t.Fatalf("max_protocol_reprompts=%d accepted", n) }
	}
}

func fmtInt(v int) string {
	if v == 0 { return "0" }
	if v == 4 { return "4" }
	return ""
}
