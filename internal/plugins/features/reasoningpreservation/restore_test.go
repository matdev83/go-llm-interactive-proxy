package reasoningpreservation_test

import (
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func restoreMissing(t *testing.T, in reasoningpreservation.RestoreInput) reasoningpreservation.RestoreResult {
	t.Helper()
	got, err := reasoningpreservation.RestoreMissingReasoning(in)
	redNotImplemented(t, err, "RestoreMissingReasoning must be implemented")
	if err != nil {
		t.Fatalf("RestoreMissingReasoning: %v", err)
	}
	return got
}

func cloneCall(t *testing.T, c lipapi.Call) lipapi.Call {
	t.Helper()
	return lipapi.CloneCall(c)
}

func missingRestoreFixture(t *testing.T) (lipapi.Call, []reasoningpreservation.TurnArtifact) {
	t.Helper()
	visible := []lipapi.Part{lipapi.TextPart("visible answer")}
	anchor := anchorFor(t, visible...)
	stored := reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil)
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0, stored)),
	}
	call := lipapi.Call{Messages: []lipapi.Message{assistantMsg(visible...)}}
	return call, artifacts
}

func TestRestoreMissingReasoning_observeNeverMutates(t *testing.T) {
	t.Parallel()
	call, artifacts := missingRestoreFixture(t)
	before := cloneCall(t, call)
	got := restoreMissing(t, reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionObserve,
		Call:          &call,
		Artifacts:     artifacts,
		Eligible:      true,
		ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
	})
	if got.Mutated {
		t.Fatal("observe must not mutate call")
	}
	if !reflect.DeepEqual(before.Messages, call.Messages) {
		t.Fatal("observe must leave messages unchanged")
	}
}

func TestRestoreMissingReasoning_restoresOnlyMissingEligibleDialects(t *testing.T) {
	t.Parallel()
	call, artifacts := missingRestoreFixture(t)
	got := restoreMissing(t, reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     artifacts,
		Eligible:      true,
		ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
	})
	if !got.Mutated || got.RestoredCount != 1 {
		t.Fatalf("expected one restored turn, got=%+v", got)
	}
	parts := call.Messages[0].Parts
	if len(parts) < 2 || parts[0].Kind != lipapi.PartReasoning {
		t.Fatalf("restored reasoning must be prepended at recorded placement, parts=%+v", parts)
	}
}

func TestRestoreMissingReasoning_skipsIneligibleCandidate(t *testing.T) {
	t.Parallel()
	call, artifacts := missingRestoreFixture(t)
	got := restoreMissing(t, reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     artifacts,
		Eligible:      false,
		ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
	})
	if got.Mutated || got.RestoredCount != 0 {
		t.Fatalf("ineligible candidate must not restore, got=%+v", got)
	}
	if len(got.Outcomes) != 0 {
		t.Fatalf("ineligible candidate must emit no feature outcomes, got %v", got.Outcomes)
	}
}

func TestRestoreMissingReasoning_idempotent(t *testing.T) {
	t.Parallel()
	call, artifacts := missingRestoreFixture(t)
	in := reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     artifacts,
		Eligible:      true,
		ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
	}
	first := restoreMissing(t, in)
	afterFirst := cloneCall(t, call)
	second := restoreMissing(t, in)
	if !first.Mutated {
		t.Fatal("first restore must mutate")
	}
	if second.Mutated || second.RestoredCount != 0 {
		t.Fatalf("second restore must be idempotent, got=%+v", second)
	}
	if !reflect.DeepEqual(afterFirst.Messages, call.Messages) {
		t.Fatal("idempotent second pass must not change messages further")
	}
}

func TestRestoreMissingReasoning_atomicNoPartialOnValidationFailure(t *testing.T) {
	t.Parallel()
	visible := []lipapi.Part{lipapi.TextPart("visible answer")}
	anchor := anchorFor(t, visible...)
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-ok", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "ok-thought", "", nil))),
		turnArtifact("art-bad", anchorFor(t, lipapi.TextPart("other turn")), placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectAnthropicThinkingV1, "needs-signature", "", nil))),
	}
	call := lipapi.Call{Messages: []lipapi.Message{
		assistantMsg(visible...),
		assistantMsg(lipapi.TextPart("other turn")),
	}}
	before := cloneCall(t, call)
	got := restoreMissing(t, reasoningpreservation.RestoreInput{
		Action:            reasoningpreservation.ActionRestore,
		OnUnrepresentable: reasoningpreservation.PolicyReject,
		Call:              &call,
		Artifacts:         artifacts,
		Eligible:          true,
		ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{
			lipapi.ReasoningDialectOpenAIChatTextV1,
		}},
	})
	if got.Mutated {
		t.Fatal("reject policy with unrepresentable dialect must not partially mutate")
	}
	if !reflect.DeepEqual(before.Messages, call.Messages) {
		t.Fatal("partial restoration is forbidden")
	}
	if !got.Exclude {
		t.Fatal("expected candidate exclusion on unrepresentable reject policy")
	}
}

func TestRestoreMissingReasoning_unrepresentableLogSkipContinuesWithoutRestore(t *testing.T) {
	t.Parallel()
	call, artifacts := missingRestoreFixture(t)
	// Force unrepresentable by advertising no supported dialects while restore requested.
	got := restoreMissing(t, reasoningpreservation.RestoreInput{
		Action:            reasoningpreservation.ActionRestore,
		OnUnrepresentable: reasoningpreservation.PolicyLogSkip,
		Call:              &call,
		Artifacts:         artifacts,
		Eligible:          true,
		ReplaySupport:     lipapi.ReasoningReplaySupport{Dialects: nil},
	})
	if got.Mutated || got.RestoredCount != 0 {
		t.Fatalf("log_skip unrepresentable must not restore, got=%+v", got)
	}
	if got.Exclude {
		t.Fatal("log_skip must continue without exclude")
	}
}

func TestRestoreMissingReasoning_stateErrorPolicies(t *testing.T) {
	t.Parallel()
	corruptArtifacts := []reasoningpreservation.TurnArtifact{
		{
			ID:             "corrupt",
			ReasoningBytes: -1,
			Reasoning:      nil,
			SourceBackend:  "backend",
			SourceModel:    "model",
		},
	}
	t.Run("reject", func(t *testing.T) {
		t.Parallel()
		call, _ := missingRestoreFixture(t)
		got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
			Action:        reasoningpreservation.ActionRestore,
			OnStateError:  reasoningpreservation.PolicyReject,
			Call:          &call,
			Artifacts:     corruptArtifacts,
			Eligible:      true,
			ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
		})
		redNotImplemented(t, err, "state error handling must be implemented")
		if err == nil {
			if got.Mutated {
				t.Fatal("state error reject must not mutate")
			}
			if !got.Exclude {
				t.Fatalf("state error reject must exclude candidate, got=%+v", got)
			}
		}
	})
	t.Run("log_skip", func(t *testing.T) {
		t.Parallel()
		call, _ := missingRestoreFixture(t)
		before := cloneCall(t, call)
		got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
			Action:        reasoningpreservation.ActionRestore,
			OnStateError:  reasoningpreservation.PolicyLogSkip,
			Call:          &call,
			Artifacts:     corruptArtifacts,
			Eligible:      true,
			ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
		})
		redNotImplemented(t, err, "state error handling must be implemented")
		if err == nil {
			if got.Mutated {
				t.Fatalf("state error log_skip must not mutate, got=%+v", got)
			}
			if !reflect.DeepEqual(before.Messages, call.Messages) {
				t.Fatal("state error log_skip must leave call unchanged")
			}
		}
	})
}

func TestRestoreMissingReasoning_conflictingNeverOverwritten(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, lipapi.TextPart("visible answer"))
	stored := reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil)
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0, stored)),
	}
	clientReasoning := reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "client-thought", "", nil)
	call := lipapi.Call{Messages: []lipapi.Message{assistantMsg(clientReasoning, lipapi.TextPart("visible answer"))}}
	before := cloneCall(t, call)
	got := restoreMissing(t, reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     artifacts,
		Eligible:      true,
		ReplaySupport: lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
	})
	if got.Mutated || got.RestoredCount != 0 {
		t.Fatalf("conflicting reasoning must not be overwritten, got=%+v", got)
	}
	if !reflect.DeepEqual(before.Messages[0].Parts[0].Reasoning.Text, call.Messages[0].Parts[0].Reasoning.Text) {
		t.Fatal("client reasoning text changed")
	}
}
