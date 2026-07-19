package reasoninge2e_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/reasoninge2e"
)

func TestFailtrace_omitsSensitivePayloads(t *testing.T) {
	t.Parallel()
	secretText := "SECRET-REASONING-PAYLOAD"
	secretSig := "sig-SECRET-XYZ"
	secretOpaque := "opaque-SECRET-BYTES"
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   99,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "visible",
			Reasoning: []reasoninge2e.ReasoningBlock{{
				Dialect:   "openai.chat.reasoning_text.v1",
				Text:      secretText,
				Signature: secretSig,
				Opaque:    []byte(secretOpaque),
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := reasoninge2e.FormatFail(plan, plan.Turns()[0].ID, reasoninge2e.ModeDropped, "restoration_incomplete", "count_got=0 count_want=1")
	for _, needle := range []string{secretText, secretSig, secretOpaque, "visible"} {
		if strings.Contains(msg, needle) {
			t.Fatalf("failtrace leaked %q in %q", needle, msg)
		}
	}
	for _, need := range []string{"seed=99", "policy=drop_all", "mode=dropped", "field=restoration_incomplete", "turn="} {
		if !strings.Contains(msg, need) {
			t.Fatalf("failtrace missing %q in %q", need, msg)
		}
	}
}

func TestCheckPrefix_partialHistory(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   3,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a1", Reasoning: sampleReasoning("r1")},
			{VisibleText: "a2", Reasoning: sampleReasoning("r2")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns := plan.Turns()
	obs := reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID:      turns[0].ID,
			VisibleText: turns[0].ExpectedBackend.VisibleText,
			Reasoning:   turns[0].ExpectedBackend.Reasoning,
		}},
	}
	if err := reasoninge2e.CheckPrefix(plan, obs); err != nil {
		t.Fatalf("prefix check: %v", err)
	}
	if err := reasoninge2e.Check(plan, obs); err == nil {
		t.Fatal("full Check must reject incomplete history")
	}
}
