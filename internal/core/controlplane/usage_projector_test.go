package controlplane_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

func TestNormalizeObservedUsageExposesDualPlaneFields(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev := usage.Event{
		TraceID:      "trace-dp",
		BLegID:       "bleg-1",
		AttemptSeq:   2,
		BackendID:    "openai",
		InputTokens:  11,
		OutputTokens: 7,
		TotalTokens:  18,
		RecordedAt:   time.Now(),
	}
	got, err := n.FromUsage(ev)
	if err != nil {
		t.Fatalf("FromUsage: %v", err)
	}
	u := got.Usage
	if u == nil {
		t.Fatal("usage detail missing")
	}
	if u.Plane != cp.UsagePlaneObserved || u.Availability != cp.UsageAvailabilityObserved {
		t.Fatalf("legacy projection lost: plane=%q availability=%q", u.Plane, u.Availability)
	}
	if u.Perspective != cp.UsagePerspectiveOperator {
		t.Fatalf("perspective = %q, want operator", u.Perspective)
	}
	if u.Boundary != cp.UsageBoundaryBackendEgress {
		t.Fatalf("boundary = %q, want backend_egress", u.Boundary)
	}
	if u.LifecycleScope != cp.UsageLifecycleBackendAttempt {
		t.Fatalf("lifecycle_scope = %q, want backend_attempt", u.LifecycleScope)
	}
	if u.Provenance != cp.UsageProvenanceDelegated {
		t.Fatalf("provenance = %q, want delegated", u.Provenance)
	}
	if string(u.Perspective) == string(u.Provenance) || string(u.Boundary) == string(u.Provenance) {
		t.Fatalf("perspective, boundary, and provenance must be independent: %#v", u)
	}
	if u.FactKind != cp.UsageFactKindDelta {
		t.Fatalf("fact_kind = %q, want delta", u.FactKind)
	}
	if err := controlplane.ValidateEvent(got); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestProjectUsageRowCopiesDualPlaneFields(t *testing.T) {
	t.Parallel()
	ev := cp.Event{
		Category: cp.CategoryUsage,
		Usage: &cp.UsageDetail{
			Plane:          cp.UsagePlaneObserved,
			Availability:   cp.UsageAvailabilityObserved,
			Perspective:    cp.UsagePerspectiveOperator,
			Boundary:       cp.UsageBoundaryBackendEgress,
			LifecycleScope: cp.UsageLifecycleBackendAttempt,
			Provenance:     cp.UsageProvenanceDelegated,
			FactKind:       cp.UsageFactKindDelta,
			TotalTokens:    9,
		},
		EvidenceState: cp.EvidenceRecorded,
	}
	row := controlplane.UsageRowFromEvent(ev)
	if row.Perspective != cp.UsagePerspectiveOperator || row.Boundary != cp.UsageBoundaryBackendEgress {
		t.Fatalf("usage row projection lost dual-plane fields: %#v", row)
	}
}
