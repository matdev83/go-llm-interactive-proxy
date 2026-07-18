package reasoningpreservation_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func classifyTurns(t *testing.T, messages []lipapi.Message, artifacts []reasoningpreservation.TurnArtifact) []reasoningpreservation.ClassifiedTurn {
	t.Helper()
	got, err := reasoningpreservation.ClassifyAssistantTurns(messages, artifacts)
	redNotImplemented(t, err, "ClassifyAssistantTurns must be implemented")
	if err != nil {
		t.Fatalf("ClassifyAssistantTurns: %v", err)
	}
	return got
}

func anchorFor(t *testing.T, parts ...lipapi.Part) [32]byte {
	t.Helper()
	return computeAnchor(t, assistantMsg(parts...))
}

func TestClassifyAssistantTurns_missing(t *testing.T) {
	t.Parallel()
	parts := []lipapi.Part{lipapi.TextPart("visible answer")}
	anchor := anchorFor(t, parts...)
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil))),
	}
	got := classifyTurns(t, []lipapi.Message{assistantMsg(parts...)}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassMissing {
		t.Fatalf("got=%+v want missing", got)
	}
}

func TestClassifyAssistantTurns_preserved(t *testing.T) {
	t.Parallel()
	stored := reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil)
	parts := []lipapi.Part{
		stored,
		lipapi.TextPart("visible answer"),
	}
	anchor := anchorFor(t, lipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0, stored)),
	}
	got := classifyTurns(t, []lipapi.Message{assistantMsg(parts...)}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassPreserved {
		t.Fatalf("got=%+v want preserved", got)
	}
}

func TestClassifyAssistantTurns_conflictingContent(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, lipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil))),
	}
	client := assistantMsg(
		reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "different-thought", "", nil),
		lipapi.TextPart("visible answer"),
	)
	got := classifyTurns(t, []lipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting content", got)
	}
}

func TestClassifyAssistantTurns_conflictingPlacement(t *testing.T) {
	t.Parallel()
	stored := reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil)
	anchor := anchorFor(t, lipapi.TextPart("before"), lipapi.TextPart("after"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor,
			placedReasoning(0, stored),
		),
	}
	client := assistantMsg(
		lipapi.TextPart("before"),
		stored,
		lipapi.TextPart("after"),
	)
	got := classifyTurns(t, []lipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting placement", got)
	}
}

func TestClassifyAssistantTurns_conflictingDialect(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, lipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "same-text", "", nil))),
	}
	client := assistantMsg(
		reasoningPart(lipapi.ReasoningDialectAnthropicThinkingV1, "same-text", "", nil),
		lipapi.TextPart("visible answer"),
	)
	got := classifyTurns(t, []lipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting dialect", got)
	}
}

func TestClassifyAssistantTurns_conflictingSignature(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, lipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectAnthropicThinkingV1, "think", "sig-a", nil))),
	}
	client := assistantMsg(
		reasoningPart(lipapi.ReasoningDialectAnthropicThinkingV1, "think", "sig-b", nil),
		lipapi.TextPart("visible answer"),
	)
	got := classifyTurns(t, []lipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting signature", got)
	}
}

func TestClassifyAssistantTurns_conflictingOpaque(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, lipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", mustOpaqueJSON(t, `{"a":1}`)))),
	}
	client := assistantMsg(
		reasoningPart(lipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", mustOpaqueJSON(t, `{"a":2}`)),
		lipapi.TextPart("visible answer"),
	)
	got := classifyTurns(t, []lipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting opaque", got)
	}
}

func TestClassifyAssistantTurns_ambiguousDuplicateMessages(t *testing.T) {
	t.Parallel()
	parts := []lipapi.Part{lipapi.TextPart("duplicate visible")}
	anchor := anchorFor(t, parts...)
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "thought-1", "", nil))),
		turnArtifact("art-2", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "thought-2", "", nil))),
	}
	messages := []lipapi.Message{
		assistantMsg(parts...),
		assistantMsg(parts...),
	}
	got := classifyTurns(t, messages, artifacts)
	for _, c := range got {
		if c.Classification != reasoningpreservation.ClassAmbiguous {
			t.Fatalf("duplicate association must be ambiguous, got=%+v", got)
		}
	}
}

func TestClassifyAssistantTurns_unmatched(t *testing.T) {
	t.Parallel()
	got := classifyTurns(t, []lipapi.Message{
		assistantMsg(lipapi.TextPart("no artifact for this")),
	}, []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchorFor(t, lipapi.TextPart("other")), placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "x", "", nil))),
	})
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassUnmatched {
		t.Fatalf("got=%+v want unmatched", got)
	}
}

func TestClassifyAssistantTurns_ignoresNonAssistantMessages(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, lipapi.TextPart("assistant visible"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "stored", "", nil))),
	}
	messages := []lipapi.Message{
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("question")}},
		assistantMsg(lipapi.TextPart("assistant visible")),
	}
	got := classifyTurns(t, messages, artifacts)
	if len(got) != 1 {
		t.Fatalf("expected one assistant classification, got=%+v", got)
	}
}
