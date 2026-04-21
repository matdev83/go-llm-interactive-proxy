package policy_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
)

func TestThresholdCircuit_opensAfterFailures(t *testing.T) {
	t.Parallel()
	cb := policy.NewThresholdCircuit(2, time.Hour)
	cb.RecordFailure("a:b")
	cb.RecordFailure("a:b")
	m := cb.UnhealthyCandidateKeys()
	if _, ok := m["a:b"]; !ok {
		t.Fatalf("expected open key, got %#v", m)
	}
	cb.RecordSuccess("a:b")
	if len(cb.UnhealthyCandidateKeys()) != 0 {
		t.Fatalf("expected cleared after success, got %#v", cb.UnhealthyCandidateKeys())
	}
}
