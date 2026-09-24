package billing

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func phase7ResourceAllocation(t *testing.T, callID BillingCallID, id, bLegID string, weight string, unallocatedWeight string) economics.AllocationRecord {
	t.Helper()
	amount, err := metering.ParseDecimal("10")
	if err != nil {
		t.Fatal(err)
	}
	return economics.AllocationRecord{
		ID: id, Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: "store-7", TenantID: "tenant-7", AccountID: "supplier-account",
			ResourceID: "prompt-cache-storage", PeriodID: "2026-09",
			StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC(),
		},
		SourceBasis: economics.BasisStatementReported, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "prompt-cache-weighted", Version: "v3", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			{
				TargetID: "real-target", Weight: economics.AllocationFraction{Numerator: weight, Denominator: "10"},
				Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-7", TenantID: "tenant-7", BillingCallID: callID.String(), BLegID: bLegID},
			},
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: unallocatedWeight, Denominator: "10"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
}

func TestPhase7ResourceAllocationAddsPayableCOGSWithoutInferenceLeg(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	providerNanos := int64(2_000_000_000)
	leg := phase5CostLeg(t, callID, "b-real", 1, LegOutcomeWinner, SurfacedYes, &providerNanos)
	leg.Evidence.InputTokens = Quantity{Value: 11, Present: true}

	base, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	allocation := phase7ResourceAllocation(t, callID, "resource-cost-1", "b-real", "5", "5")
	got, err := AttributeOperatorCOGSWithAllocations([]CallLegUsageRecord{leg}, []economics.AllocationRecord{allocation}, nil, "USD")
	if err != nil {
		t.Fatalf("AttributeOperatorCOGSWithAllocations: %v", err)
	}
	if got.KnownSubtotal.Nano != 7_000_000_000 || !got.Payable || got.Completeness != CostCompletenessKnown {
		t.Fatalf("COGS = %+v, want payable provider 2 + allocated 5", got)
	}
	if len(got.IncludedLegKeys) != len(base.IncludedLegKeys) || got.IncludedLegKeys[0] != base.IncludedLegKeys[0] {
		t.Fatalf("included B-legs = %v, want provider leg only %v", got.IncludedLegKeys, base.IncludedLegKeys)
	}
	if len(got.AllocatedCostLines) != 2 {
		t.Fatalf("allocated lines = %+v, want target and unallocated remainder", got.AllocatedCostLines)
	}
	var allocated, remainder *AllocatedCostLine
	for i := range got.AllocatedCostLines {
		line := &got.AllocatedCostLines[i]
		if line.Unallocated {
			remainder = line
		} else {
			allocated = line
		}
	}
	if allocated == nil || allocated.SourceSubject.ResourceID != "prompt-cache-storage" || allocated.SourceSubject.AccountID != "supplier-account" || allocated.SourceSubject.PeriodID != "2026-09" {
		t.Fatalf("allocated line lost source resource/account/period: %+v", allocated)
	}
	if allocated.AllocationRevision != 1 || allocated.Policy.Method != "prompt-cache-weighted" || allocated.Policy.Version != "v3" || allocated.Policy.Hash != strings.Repeat("a", 64) {
		t.Fatalf("allocated line lost policy identity: %+v", allocated)
	}
	if allocated.Share != (economics.AllocationFraction{Numerator: "1", Denominator: "2"}) || allocated.RoundedAmount == nil || allocated.RoundedAmount.NanoUnits != 5_000_000_000 {
		t.Fatalf("allocated line share/amount = %+v, want exact half of source", allocated)
	}
	if remainder == nil || remainder.RoundedAmount == nil || remainder.RoundedAmount.NanoUnits != 5_000_000_000 || remainder.InferenceEligible {
		t.Fatalf("unallocated remainder = %+v, want payable resource remainder outside inference", remainder)
	}
	if allocated.InferenceEligible {
		t.Fatalf("allocated resource cost must not become B-leg inference evidence: %+v", allocated)
	}

	selected, err := SelectRetailBLegs([]CallLegUsageRecord{leg}, TurnOutcomeCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].BLegID != "b-real" || selected[0].Evidence.InputTokens != leg.Evidence.InputTokens {
		t.Fatalf("retail inference selection changed with resource allocation: %+v", selected)
	}
}

func TestPhase7ResourceAllocationRejectsSyntheticBLegTarget(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	amount := int64(0)
	leg := phase5CostLeg(t, callID, "b-real", 1, LegOutcomeWinner, SurfacedYes, &amount)
	allocation := phase7ResourceAllocation(t, callID, "resource-cost-synthetic", "b-synthetic", "10", "0")
	_, err := AttributeOperatorCOGSWithAllocations([]CallLegUsageRecord{leg}, []economics.AllocationRecord{allocation}, nil, "USD")
	if !errors.Is(err, ErrAllocationTargetNotAttributable) {
		t.Fatalf("error = %v, want ErrAllocationTargetNotAttributable", err)
	}
}

func TestPhase7InformationalResourceAllocationRejectsSyntheticBLegTarget(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	amount := int64(0)
	leg := phase5CostLeg(t, callID, "b-real", 1, LegOutcomeWinner, SurfacedYes, &amount)
	allocation := phase7ResourceAllocation(t, callID, "resource-cost-informational-synthetic", "b-synthetic", "10", "0")
	allocation.Targets[0].Informational = true

	_, err := AttributeOperatorCOGSWithAllocations([]CallLegUsageRecord{leg}, []economics.AllocationRecord{allocation}, nil, "USD")
	if !errors.Is(err, ErrAllocationTargetNotAttributable) {
		t.Fatalf("error = %v, want ErrAllocationTargetNotAttributable", err)
	}
}

func TestPhase7InformationalResourceAllocationDoesNotChangeCOGS(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	providerNanos := int64(2_000_000_000)
	leg := phase5CostLeg(t, callID, "b-real", 1, LegOutcomeWinner, SurfacedYes, &providerNanos)
	base, err := AttributeOperatorCOGS([]CallLegUsageRecord{leg}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	allocation := phase7ResourceAllocation(t, callID, "resource-cost-informational", "b-real", "10", "0")
	allocation.Targets[0].Informational = true

	got, err := AttributeOperatorCOGSWithAllocations([]CallLegUsageRecord{leg}, []economics.AllocationRecord{allocation}, nil, "USD")
	if err != nil {
		t.Fatalf("AttributeOperatorCOGSWithAllocations: %v", err)
	}
	if got.KnownSubtotal != base.KnownSubtotal || !got.Payable || got.Completeness != CostCompletenessKnown {
		t.Fatalf("COGS = %+v, want unchanged payable provider subtotal %+v", got, base)
	}
	if len(got.IncludedLegKeys) != len(base.IncludedLegKeys) || got.IncludedLegKeys[0] != base.IncludedLegKeys[0] {
		t.Fatalf("included B-legs = %v, want provider leg only %v", got.IncludedLegKeys, base.IncludedLegKeys)
	}
	var informational *AllocatedCostLine
	for i := range got.AllocatedCostLines {
		line := &got.AllocatedCostLines[i]
		if !line.Unallocated {
			informational = line
		}
	}
	if informational == nil || !informational.Informational || informational.InferenceEligible {
		t.Fatalf("informational allocation line = %+v, want non-inference evidence", informational)
	}

	selected, err := SelectRetailBLegs([]CallLegUsageRecord{leg}, TurnOutcomeCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].BLegID != "b-real" {
		t.Fatalf("retail inference selection changed with informational allocation: %+v", selected)
	}
}

func TestPhase7ResourceAllocationRejectsNonConservedWeights(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	amount := int64(0)
	leg := phase5CostLeg(t, callID, "b-real", 1, LegOutcomeWinner, SurfacedYes, &amount)
	allocation := phase7ResourceAllocation(t, callID, "resource-cost-not-conserved", "b-real", "5", "4")
	_, err := AttributeOperatorCOGSWithAllocations([]CallLegUsageRecord{leg}, []economics.AllocationRecord{allocation}, nil, "USD")
	if !errors.Is(err, economics.ErrAllocationNotConserved) {
		t.Fatalf("error = %v, want ErrAllocationNotConserved", err)
	}
}

func TestPhase7StatementAllocationRetainsAccountSubjectAndRollsUp(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	providerNanos := int64(0)
	leg := phase5CostLeg(t, callID, "b-real", 1, LegOutcomeWinner, SurfacedYes, &providerNanos)
	allocation := phase7ResourceAllocation(t, callID, "account-period-cost", "b-real", "10", "0")
	allocation.SourceSubject = metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: "store-7", TenantID: "tenant-7", AccountID: "supplier-account",
		ProviderAccountKey: "provider-account", PeriodID: "2026-09", StatementID: "statement-2026-09", StatementLineID: "line-1",
	}

	got, err := AttributeOperatorCOGSWithAllocations([]CallLegUsageRecord{leg}, []economics.AllocationRecord{allocation}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != 10_000_000_000 || !got.Payable || len(got.AllocatedCostLines) != 2 {
		t.Fatalf("COGS = %+v, want payable statement line allocation", got)
	}
	line := got.AllocatedCostLines[0]
	if line.Unallocated {
		line = got.AllocatedCostLines[1]
	}
	if line.SourceSubject.Kind != metering.SubjectStatementLine || line.SourceSubject.ProviderAccountKey != "provider-account" || line.SourceSubject.StatementID != "statement-2026-09" || line.SourceSubject.StatementLineID != "line-1" {
		t.Fatalf("statement source subject = %+v, want account-period statement identity", line.SourceSubject)
	}
}

func TestPhase7PendingResourceAllocationIsNotPayable(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	amount := int64(2_000_000_000)
	leg := phase5CostLeg(t, callID, "b-real", 1, LegOutcomeWinner, SurfacedYes, &amount)
	pending := phase7ResourceAllocation(t, callID, "resource-cost-pending", "b-real", "10", "0")
	pending.Operation = economics.AllocationOperationCorrection
	pending.Supersedes = []economics.AllocationRef{{
		StoreID: "store-7", AllocationID: "resource-cost-not-yet-stored", Version: 1, PayloadHash: strings.Repeat("b", 64),
	}}

	got, err := AttributeOperatorCOGSWithAllocations([]CallLegUsageRecord{leg}, []economics.AllocationRecord{pending}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.KnownSubtotal.Nano != amount || got.Completeness != CostCompletenessPartial || got.Payable {
		t.Fatalf("COGS = %+v, want provider subtotal only and pending allocation non-payable", got)
	}
	if len(got.PendingAllocations) != 1 || got.PendingAllocations[0].AllocationID != "resource-cost-not-yet-stored" {
		t.Fatalf("pending allocations = %+v, want unresolved predecessor", got.PendingAllocations)
	}
}
