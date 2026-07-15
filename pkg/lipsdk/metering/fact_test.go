package metering_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestFact_Validate_IdentityRequired(t *testing.T) {
	t.Parallel()
	base := validFact()
	if err := base.Validate(); err != nil {
		t.Fatalf("valid fact: %v", err)
	}
	missingID := base
	missingID.FactID = ""
	if err := missingID.Validate(); err == nil {
		t.Fatal("FactID required")
	}
	missingStream := base
	missingStream.StreamID = ""
	if err := missingStream.Validate(); err == nil {
		t.Fatal("StreamID required")
	}
	negSeq := base
	negSeq.Sequence = -1
	if err := negSeq.Validate(); err == nil {
		t.Fatal("negative sequence rejected")
	}
}

func TestFact_SupersedesOnlyForCorrectionKinds(t *testing.T) {
	t.Parallel()
	delta := validFact()
	delta.Kind = metering.FactKindDelta
	delta.Supersedes = []string{"old"}
	if err := delta.Validate(); err == nil {
		t.Fatal("delta must not carry Supersedes")
	}

	correction := validFact()
	correction.Kind = metering.FactKindCorrection
	correction.Supersedes = []string{"old-fact"}
	if err := correction.Validate(); err != nil {
		t.Fatal(err)
	}
	emptySuper := correction
	emptySuper.Supersedes = nil
	if err := emptySuper.Validate(); err == nil {
		t.Fatal("correction requires non-empty Supersedes")
	}

	repl := validFact()
	repl.Kind = metering.FactKindAuthoritativeReplacement
	repl.Supersedes = []string{"old-fact"}
	if err := repl.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFact_IdempotencyKey(t *testing.T) {
	t.Parallel()
	a := validFact()
	b := validFact()
	if a.IdempotencyKey() == "" || a.IdempotencyKey() != b.IdempotencyKey() {
		t.Fatalf("same identity must share key: %q vs %q", a.IdempotencyKey(), b.IdempotencyKey())
	}
	b.FactID = "other"
	if a.IdempotencyKey() == b.IdempotencyKey() {
		t.Fatal("different FactID must differ")
	}
}

func TestSameFactIdentity(t *testing.T) {
	t.Parallel()
	a := validFact()
	b := validFact()
	if !metering.SameFactIdentity(a, b) {
		t.Fatal("expected same identity")
	}
	b.Sequence = 2
	if metering.SameFactIdentity(a, b) {
		t.Fatal("sequence differs")
	}
}

func validFact() metering.Fact {
	return metering.Fact{
		FactID:      "fact-1",
		StreamID:    "stream-1",
		Sequence:    1,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendIngress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "req-1", ALegID: "a-1"},
		Scope:       scope.PrincipalScopeView{},
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
		Surfaced:    metering.SurfacedUnknown,
		RecordedAt:  time.Unix(1, 0).UTC(),
	}
}
