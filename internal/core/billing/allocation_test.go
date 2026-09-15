package billing

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestRollupAllocatedCostsPreservesSourcePlaneAndUnallocatedRemainder(t *testing.T) {
	amount, err := metering.ParseDecimal("10")
	if err != nil {
		t.Fatal(err)
	}
	record := economics.AllocationRecord{
		ID: "shared-cost-1", Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: "store-11", TenantID: "tenant-11",
			AccountID: "supplier-account", ResourceID: "shared", PeriodID: "2026-09",
			StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC(),
		},
		SourceBasis: economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			{TargetID: "b-leg-1", Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-11", TenantID: "tenant-11", BLegID: "b-leg-1"}, Weight: economics.AllocationFraction{Numerator: "3", Denominator: "5"}},
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: "2", Denominator: "5"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	lines, err := RollupAllocatedCosts([]economics.AllocationRecord{record})
	if err != nil {
		t.Fatalf("RollupAllocatedCosts: %v", err)
	}
	if len(lines) != 2 || lines[0].Target.BLegID != "b-leg-1" || !lines[1].Unallocated {
		t.Fatalf("lines = %+v, want deterministic target and unallocated lines", lines)
	}
	for _, line := range lines {
		if line.SourceBasis != economics.BasisAllocatedCost || line.SourceSubject.ResourceID != "shared" || line.SourceSubject.PeriodID != "2026-09" {
			t.Fatalf("line lost source basis/ownership: %+v", line)
		}
		if line.InferenceEligible {
			t.Fatalf("allocated line must not become B-leg inference evidence: %+v", line)
		}
	}
}

func TestRollupAllocatedCostsUsesLatestImmutableCorrectionHead(t *testing.T) {
	amount, err := metering.ParseDecimal("10")
	if err != nil {
		t.Fatal(err)
	}
	base := economics.AllocationRecord{
		ID: "shared-cost-head", Version: 1,
		SourceSubject: metering.SubjectRef{Kind: metering.SubjectResource, StoreID: "store-11", ResourceID: "shared", PeriodID: "2026-09", StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC()},
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets:   []economics.AllocationTarget{{TargetID: "b-leg-1", Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-11", BLegID: "b-leg-1"}, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}}},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	canonical, err := base.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	correction := canonical.Clone()
	correction.Version = 2
	correction.Operation = economics.AllocationOperationCorrection
	correction.Supersedes = []economics.AllocationRef{{StoreID: "store-11", AllocationID: canonical.ID, Version: canonical.Version, PayloadHash: canonical.Fingerprint()}}
	lines, err := RollupAllocatedCosts([]economics.AllocationRecord{canonical, correction})
	if err != nil {
		t.Fatalf("RollupAllocatedCosts correction: %v", err)
	}
	if len(lines) != 1 || lines[0].AllocationVersion != 2 || lines[0].Operation != economics.AllocationOperationCorrection {
		t.Fatalf("correction rollup = %+v, want only latest immutable head", lines)
	}
}

func TestRollupAllocatedCostsDetailedExcludesPendingSuccessor(t *testing.T) {
	amount, err := metering.ParseDecimal("10")
	if err != nil {
		t.Fatal(err)
	}
	base := economics.AllocationRecord{
		ID: "shared-cost-pending-base", Version: 1,
		SourceSubject: metering.SubjectRef{Kind: metering.SubjectResource, StoreID: "store-11", TenantID: "tenant-11", AccountID: "supplier-account", ResourceID: "shared", PeriodID: "2026-09", StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC()},
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets:   []economics.AllocationTarget{{TargetID: "b-leg-1", Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-11", TenantID: "tenant-11", BLegID: "b-leg-1"}, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}}},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	canonical, err := base.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	pending := canonical.Clone()
	pending.ID = "shared-cost-pending-successor"
	pending.Operation = economics.AllocationOperationReplacement
	pending.Supersedes = []economics.AllocationRef{{StoreID: "store-11", AllocationID: "allocation-not-yet-stored", Version: 1, PayloadHash: strings.Repeat("d", 64)}}

	result, err := RollupAllocatedCostsDetailed([]economics.AllocationRecord{pending, canonical})
	if err != nil {
		t.Fatalf("RollupAllocatedCostsDetailed: %v", err)
	}
	if result.Status != economics.AllocationSupersessionPending || result.Complete || result.Payable {
		t.Fatalf("result status=%q complete=%t payable=%t, want pending/incomplete/not payable", result.Status, result.Complete, result.Payable)
	}
	if len(result.Pending) != 1 || result.Pending[0].AllocationID != "allocation-not-yet-stored" {
		t.Fatalf("pending = %+v", result.Pending)
	}
	if len(result.Lines) != 1 || result.Lines[0].AllocationID != canonical.ID {
		t.Fatalf("lines = %+v, want only resolved base", result.Lines)
	}

	legacyLines, legacyErr := RollupAllocatedCosts([]economics.AllocationRecord{pending, canonical})
	if legacyLines != nil {
		t.Fatalf("legacy lines = %+v, want nil when supersession ancestry is pending", legacyLines)
	}
	if !errors.Is(legacyErr, ErrAllocationRollupIncomplete) {
		t.Fatalf("legacy error = %v, want ErrAllocationRollupIncomplete", legacyErr)
	}
	var incompleteErr *AllocationRollupIncompleteError
	if !errors.As(legacyErr, &incompleteErr) {
		t.Fatalf("legacy error = %T, want *AllocationRollupIncompleteError", legacyErr)
	}
	if incompleteErr.Status != economics.AllocationSupersessionPending || incompleteErr.Complete || incompleteErr.Payable {
		t.Fatalf("legacy incomplete error = %+v, want pending/incomplete/non-payable", incompleteErr)
	}
	if len(incompleteErr.Pending) != 1 || incompleteErr.Pending[0] != pending.Supersedes[0] {
		t.Fatalf("legacy pending refs = %+v, want %+v", incompleteErr.Pending, pending.Supersedes)
	}
}

func TestLegacyAllocationLinesRejectsIncompleteAndNonPayableResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		complete bool
		payable  bool
	}{
		{name: "incomplete", complete: false, payable: true},
		{name: "nonpayable", complete: true, payable: false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lines, err := legacyAllocationLines(AllocationRollupResult{
				Lines:    []AllocatedCostLine{{AllocationID: "known-line"}},
				Status:   economics.AllocationSupersessionResolved,
				Complete: tc.complete,
				Payable:  tc.payable,
			})
			if lines != nil {
				t.Fatalf("lines = %+v, want nil for %s result", lines, tc.name)
			}
			if !errors.Is(err, ErrAllocationRollupIncomplete) {
				t.Fatalf("error = %v, want ErrAllocationRollupIncomplete", err)
			}
			var incompleteErr *AllocationRollupIncompleteError
			if !errors.As(err, &incompleteErr) {
				t.Fatalf("error = %T, want *AllocationRollupIncompleteError", err)
			}
			if incompleteErr.Complete != tc.complete || incompleteErr.Payable != tc.payable {
				t.Fatalf("error = %+v, want complete=%t payable=%t", incompleteErr, tc.complete, tc.payable)
			}
		})
	}
}

func TestLegacyAllocationLinesPreservesResolvedCompatibility(t *testing.T) {
	t.Parallel()

	want := []AllocatedCostLine{{AllocationID: "resolved-line"}}
	got, err := legacyAllocationLines(AllocationRollupResult{
		Lines:    want,
		Status:   economics.AllocationSupersessionResolved,
		Complete: true,
		Payable:  true,
	})
	if err != nil {
		t.Fatalf("legacy resolved result: %v", err)
	}
	if len(got) != 1 || got[0].AllocationID != "resolved-line" {
		t.Fatalf("lines = %+v, want resolved line", got)
	}
}
