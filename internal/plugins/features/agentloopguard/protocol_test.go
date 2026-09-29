package agentloopguard

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func TestAttemptCompletionStrategyNeverCallsVerifierAndRepromptsOnce(t *testing.T) {
	collector := &providerSemanticCollector{responses: []string{`{"kind":"COMPLETE"}`}}
	in := semanticProviderInput()
	in.Evidence.ExplicitCompletionExpected = true
	in.Evidence.ExplicitCompletion = false
	in.Auxiliary = collector
	provider := NewProvider(Config{Enabled:true, Strategy:StrategyAttemptCompletion})

	first, err := provider.Decide(context.Background(), in)
	if err != nil { t.Fatal(err) }
	if first.Kind != terminaldecision.DecisionContinue || first.Continue == nil || first.ReasonCode != reasonMissingSignalRetry {
		t.Fatalf("first=%+v", first)
	}
	if collector.calls != 0 { t.Fatalf("verifier called %d times", collector.calls) }
	if first.Continue.Instruction != ProtocolRepairInstruction { t.Fatal("repair instruction drift") }

	second := in
	second.Continuation.Attempt = 2
	second.Evidence.Lineage.Attempt = 2
	second.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
	decision, err := provider.Decide(context.Background(), second)
	if err != nil { t.Fatal(err) }
	if decision.Kind != terminaldecision.DecisionAllowStop || decision.ReasonCode != reasonRepromptExhausted {
		t.Fatalf("second=%+v", decision)
	}
	if collector.calls != 0 { t.Fatalf("verifier called %d times", collector.calls) }
}

func TestAttemptCompletionStrategyCompletionAndInactive(t *testing.T) {
	provider := NewProvider(Config{Enabled:true, Strategy:StrategyAttemptCompletion})
	in := semanticProviderInput()
	in.Evidence.ExplicitCompletionExpected = true
	in.Evidence.ExplicitCompletion = true
	got, err := provider.Decide(context.Background(), in)
	if err != nil || got.Kind != terminaldecision.DecisionAllowStop || got.ReasonCode != reasonExplicitComplete {
		t.Fatalf("complete=%+v err=%v", got, err)
	}
	in.Evidence.ExplicitCompletion = false
	in.Evidence.ExplicitCompletionExpected = false
	got, err = provider.Decide(context.Background(), in)
	if err != nil || got.Kind != terminaldecision.DecisionAllowStop || got.ReasonCode != reasonProtocolInactive {
		t.Fatalf("inactive=%+v err=%v", got, err)
	}
}
