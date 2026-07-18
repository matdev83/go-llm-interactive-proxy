package lipapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestOutputCommitted_reasoningSignatureDelta(t *testing.T) {
	t.Parallel()
	if lipapi.OutputCommitted(lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig"}) {
		t.Fatal("reasoning_signature_delta must not commit for failover")
	}
}

func TestValidateEventSequence_acceptsReasoningSignatureDeltaAfterMessageStarted(t *testing.T) {
	t.Parallel()
	err := lipapi.ValidateEventSequence([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "think"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig"},
		{Kind: lipapi.EventResponseFinished},
	})
	if err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestValidateEventSequence_rejectsReasoningSignatureDeltaBeforeMessageStarted(t *testing.T) {
	t.Parallel()
	err := lipapi.ValidateEventSequence([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig"},
		{Kind: lipapi.EventResponseFinished},
	})
	if err == nil {
		t.Fatal("expected reject before message_started")
	}
}

func TestValidateEventEnvelope_reasoningSignatureDeltaOversized(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: strings.Repeat("s", lipapi.MaxRefStringBytes+1)}
	if err := lipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected oversized signature to be rejected")
	}
}

func TestValidateEventEnvelope_reasoningSignatureDeltaWithinBound(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig"}
	if err := lipapi.ValidateEventEnvelope(ev); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestCollect_ignoresReasoningSignatureDelta(t *testing.T) {
	t.Parallel()
	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "think"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig"},
		{Kind: lipapi.EventResponseFinished},
	})
	col, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if col.Reasoning.String() != "think" {
		t.Fatalf("reasoning = %q, want %q", col.Reasoning.String(), "think")
	}
}

func TestOutputCommitted_reasoningOpaqueDelta(t *testing.T) {
	t.Parallel()
	if !lipapi.OutputCommitted(lipapi.Event{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking","data":"x"}`)}) {
		t.Fatal("reasoning_opaque_delta must commit for failover")
	}
}

func TestValidateEventSequence_acceptsReasoningOpaqueDeltaAfterMessageStarted(t *testing.T) {
	t.Parallel()
	err := lipapi.ValidateEventSequence([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking","data":"opaque"}`)},
		{Kind: lipapi.EventResponseFinished},
	})
	if err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestValidateEventEnvelope_reasoningOpaqueDeltaOversized(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(strings.Repeat("o", lipapi.MaxReasoningOpaqueBytes+1))}
	if err := lipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected oversized opaque to be rejected")
	}
}

func TestCollect_ignoresReasoningOpaqueDelta(t *testing.T) {
	t.Parallel()
	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "think"},
		{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking","data":"opaque"}`)},
		{Kind: lipapi.EventResponseFinished},
	})
	col, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if col.Reasoning.String() != "think" {
		t.Fatalf("reasoning = %q, want %q", col.Reasoning.String(), "think")
	}
}
