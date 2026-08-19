package runtime

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func TestAttemptSessionConstructsItsTerminalOnce(t *testing.T) {
	first := newAttemptSession(attemptSessionInput{})
	if first == nil || first.terminal == nil {
		t.Fatal("expected attempt session to own an attempt terminal")
	}
	if first.terminal.Owner().Scope() != sdkterminal.ScopeAttempt {
		t.Fatalf("attempt terminal scope = %v, want %v", first.terminal.Owner().Scope(), sdkterminal.ScopeAttempt)
	}
}

func TestAttemptSessionsHaveDistinctAttemptTerminals(t *testing.T) {
	first := newAttemptSession(attemptSessionInput{})
	second := newAttemptSession(attemptSessionInput{})
	if first.terminal == second.terminal {
		t.Fatal("each B-leg attempt must own a fresh terminal")
	}
	if first.terminal.Owner().Scope() != sdkterminal.ScopeAttempt || second.terminal.Owner().Scope() != sdkterminal.ScopeAttempt {
		t.Fatal("attempt terminals must both be ScopeAttempt")
	}
}

func TestAttemptSlotSnapshotsAndSwapsPointers(t *testing.T) {
	first := newAttemptSession(attemptSessionInput{})
	second := newAttemptSession(attemptSessionInput{})
	var slot attemptSlot
	slot.install(first)
	if slot.snapshot() != first {
		t.Fatal("snapshot did not return installed attempt")
	}
	if slot.swap(second) != first || slot.snapshot() != second {
		t.Fatal("swap did not publish replacement atomically")
	}
}

func TestAttemptSessionTakeInnerIsIdempotent(t *testing.T) {
	inner := lipapi.NewFixedEventStream(nil)
	session := newAttemptSession(attemptSessionInput{inner: inner})
	if got := session.takeInner(); got != inner {
		t.Fatalf("first take = %T, want installed stream", got)
	}
	if got := session.takeInner(); got != nil {
		t.Fatalf("second take = %T, want nil after ownership transfer", got)
	}
}
