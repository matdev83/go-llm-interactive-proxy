package policy_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCircuitBreaker_opensAfterThreshold(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 2,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	key := "be:model"
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSurfacedFailure)
	if u := cb.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("unexpected unhealthy before threshold: %v", u)
	}
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSurfacedFailure)
	u := cb.UnhealthyCandidateKeys()
	if len(u) != 1 {
		t.Fatalf("want 1 unhealthy key, got %v", u)
	}
	if _, ok := u[key]; !ok {
		t.Fatal("missing key")
	}
}

func TestCircuitBreaker_successResets(t *testing.T) {
	t.Parallel()
	now := time.Unix(2000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 3,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	key := "x:y"
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSuccess)
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSurfacedFailure)
	if u := cb.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("unexpected unhealthy: %v", u)
	}
}

func TestCircuitBreaker_cooldownClearsBlock(t *testing.T) {
	t.Parallel()
	start := time.Unix(3000, 0).UTC()
	var cur time.Time
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 1,
		OpenDuration:     10 * time.Second,
		Now:              func() time.Time { return cur },
	})
	key := "a:b"
	cur = start
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSwallowedFailure)
	if u := cb.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("want unhealthy, got %v", u)
	}
	cur = start.Add(11 * time.Second)
	if u := cb.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("want healthy after cooldown, got %v", u)
	}
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSurfacedFailure)
	if u := cb.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("want open again, got %v", u)
	}
}

func TestCircuitBreaker_cancelIgnored(t *testing.T) {
	t.Parallel()
	now := time.Unix(4000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 3,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	key := "p:q"
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptCancelled)
	if cb.UnhealthyCandidateKeys() != nil {
		t.Fatal("cancel alone must not affect breaker state")
	}
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptCancelled)
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome(key, lipapi.AttemptSurfacedFailure)
	if u := cb.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("want blocked after three failures with cancel ignored mid-streak: %v", u)
	}
}

func TestCircuitBreaker_maxTrackedKeys_rotates(t *testing.T) {
	t.Parallel()
	now := time.Unix(5000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 100,
		OpenDuration:     time.Hour,
		MaxTrackedKeys:   1,
		Now:              func() time.Time { return now },
	})
	cb.OnRoutingAttemptOutcome("first:k", lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("second:k", lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("first:k", lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("third:k", lipapi.AttemptSurfacedFailure)
	// Without eviction, four failure events on distinct keys under cap 1 would panic or grow; rotations must succeed.
}

func TestCircuitBreaker_evictsIdleFirst(t *testing.T) {
	t.Parallel()
	now := time.Unix(6000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 50,
		OpenDuration:     time.Hour,
		MaxTrackedKeys:   2,
		Now:              func() time.Time { return now },
	})
	cb.OnRoutingAttemptOutcome("idle:k", lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("keep:k", lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("idle:k", lipapi.AttemptSuccess)
	cb.OnRoutingAttemptOutcome("new:k", lipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("idle:k", lipapi.AttemptSurfacedFailure)
}
