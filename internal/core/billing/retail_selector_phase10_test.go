package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func retailSelectionCall(t *testing.T, policy ChargePolicy, ids ...string) CallUsageRecord {
	t.Helper()
	call := testCallUsageRecord(mustBillingCallID(t))
	call.ALegID = "a-1"
	call.ExpectedBLegIDs = append([]string(nil), ids...)
	call.CustomerPricingRef = policy.PricingRef
	call.ChargePolicyRef = policy.Ref
	return call
}

func retailSelectionPolicy(mode RetailSelectionMode, basis RetailCommercialBasis) ChargePolicy {
	return ChargePolicy{
		Ref:                VersionRef{ID: "retail-policy", Version: "v10"},
		PricingRef:         VersionRef{ID: "retail-pricing", Version: "v3"},
		Scope:              ChargeSurfacedTurn,
		IncludeInputTokens: true,
		Retail:             &RetailSelectionPolicy{Mode: mode, Basis: basis},
	}
}

func retailSelectionLeg(t *testing.T, callID BillingCallID, id string, seq int, outcome LegOutcome, surfaced SurfacedState) CallLegUsageRecord {
	t.Helper()
	leg := phase5V2Leg(t, callID, id, phase5ChargeObservation(t, callID, id, "obs-"+id, "usage-"+id, nil, metering.PaymentParty{Kind: metering.PaymentPartyOperator}))
	leg.ALegID = "a-1"
	leg.AttemptSeq = seq
	leg.Outcome = outcome
	leg.Surfaced = surfaced
	leg.OperatorRateRef = VersionRef{}
	return leg
}

func TestPhase10RetailSelectorDefaultSelectsSurfacedWinnerOnly(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-retry", "b-loser", "b-winner")
	legs := []CallLegUsageRecord{
		retailSelectionLeg(t, call.CallID, "b-loser", 2, LegOutcomeLoser, SurfacedNo),
		retailSelectionLeg(t, call.CallID, "b-winner", 3, LegOutcomeWinner, SurfacedYes),
		retailSelectionLeg(t, call.CallID, "b-retry", 1, LegOutcomeFailed, SurfacedNo),
	}
	got, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != RetailSelectionSurfacedWinner || got.Basis != RetailBasisIndependent || len(got.SelectedBLegs) != 1 || got.SelectedBLegs[0].BLegID != "b-winner" {
		t.Fatalf("selection = %+v, want surfaced winner only", got)
	}
	if len(got.ObservationRefs) != 1 || got.ObservationRefs[0].ObservationID != "obs-b-winner" {
		t.Fatalf("observation refs = %+v, want winner observation", got.ObservationRefs)
	}
}

func TestPhase10RetailSelectorMigratesLegacyOfferScope(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy("", "")
	policy.Retail = nil
	policy.Scope = ChargeAllPotentialLegs
	call := retailSelectionCall(t, policy, "b-failed", "b-winner")
	legs := []CallLegUsageRecord{
		retailSelectionLeg(t, call.CallID, "b-winner", 2, LegOutcomeWinner, SurfacedYes),
		retailSelectionLeg(t, call.CallID, "b-failed", 1, LegOutcomeFailed, SurfacedNo),
	}
	got, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != RetailSelectionAllAttributable || got.Basis != RetailBasisIndependent || len(got.SelectedBLegs) != 2 {
		t.Fatalf("legacy scope migration = %+v, want independent all-attributable selection", got)
	}
}

func TestPhase10EstimateHonorsExplicitAllAttributableRetailMode(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisIndependent)
	policy.IncludeOutputTokens = false
	policy.IncludeFixedCharges = false
	policy.IncludeResourceCharges = false
	a := estimateRouteFixture("a", 10, 0)
	b := estimateRouteFixture("b", 20, 0)
	a.Pricing.Ref = policy.PricingRef
	b.Pricing.Ref = policy.PricingRef
	bound, err := EstimateMaxCustomerCharge(MaxChargeInput{
		Currency: "USD", InputTokens: 1_000_000, InputTokensPresent: true,
		Policy: policy, Routes: []ChargeRoute{a, b},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Amount.Nano != 30 {
		t.Fatalf("explicit all-attributable bound = %d, want 30", bound.Amount.Nano)
	}
}

func TestPhase10RetailSelectorNamedSubsetAndAllAttributableAreDeterministic(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionNamedOutcomes, RetailBasisIndependent)
	policy.Retail.OutcomeSubset = []LegOutcome{LegOutcomeWinner, LegOutcomeFailed}
	call := retailSelectionCall(t, policy, "b-winner", "b-failed", "b-loser", "b-shell")
	legs := []CallLegUsageRecord{
		retailSelectionLeg(t, call.CallID, "b-winner", 3, LegOutcomeWinner, SurfacedYes),
		retailSelectionLeg(t, call.CallID, "b-shell", 4, LegOutcomeNeverStarted, SurfacedNo),
		retailSelectionLeg(t, call.CallID, "b-failed", 1, LegOutcomeFailed, SurfacedNo),
		retailSelectionLeg(t, call.CallID, "b-loser", 2, LegOutcomeLoser, SurfacedNo),
	}
	got, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SelectedBLegs) != 2 || got.SelectedBLegs[0].BLegID != "b-failed" || got.SelectedBLegs[1].BLegID != "b-winner" {
		t.Fatalf("named subset = %+v, want attempt order failed,winner", got.SelectedBLegs)
	}

	allPolicy := policy
	allPolicy.Retail = &RetailSelectionPolicy{Mode: RetailSelectionAllAttributable, Basis: RetailBasisIndependent}
	all, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: allPolicy})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.SelectedBLegs) != 3 || all.SelectedBLegs[0].BLegID != "b-failed" || all.SelectedBLegs[1].BLegID != "b-loser" || all.SelectedBLegs[2].BLegID != "b-winner" {
		t.Fatalf("all attributable = %+v, want failed,loser,winner", all.SelectedBLegs)
	}
}

func TestPhase10RetailSelectorCostPassThroughIsExplicitAndIndependentOfProviderCost(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisCostPassThrough)
	call := retailSelectionCall(t, policy, "b-failed", "b-winner")
	legs := []CallLegUsageRecord{
		retailSelectionLeg(t, call.CallID, "b-winner", 2, LegOutcomeWinner, SurfacedYes),
		retailSelectionLeg(t, call.CallID, "b-failed", 1, LegOutcomeFailed, SurfacedNo),
	}
	got, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if got.Basis != RetailBasisCostPassThrough || len(got.SelectedBLegs) != 2 {
		t.Fatalf("cost pass-through selection = %+v, want explicit all-leg basis", got)
	}
	for _, leg := range legs {
		if leg.Evidence.Cost.Present || leg.OperatorRateRef != (VersionRef{}) {
			t.Fatalf("test fixture unexpectedly supplied provider cost/rate: %+v", leg)
		}
	}
}

func TestPhase10RetailSelectorPreservesInterruptedCallOrderingRule(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-first", "b-latest")
	call.Outcome = TurnOutcomeFailed
	legs := []CallLegUsageRecord{
		retailSelectionLeg(t, call.CallID, "b-first", 1, LegOutcomeFailed, SurfacedNo),
		retailSelectionLeg(t, call.CallID, "b-latest", 2, LegOutcomeCanceled, SurfacedNo),
	}
	got, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SelectedBLegs) != 1 || got.SelectedBLegs[0].BLegID != "b-latest" {
		t.Fatalf("interrupted selection = %+v, want latest accepted attempt", got.SelectedBLegs)
	}
	legs[1].AttemptSeq = 0
	if _, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy}); !errors.Is(err, ErrBillingAttemptSequenceUnknown) {
		t.Fatalf("unknown interrupted attempt ordering = %v, want %v", err, ErrBillingAttemptSequenceUnknown)
	}
}

func TestPhase10RetailSelectorFailsClosedForMissingUnknownDuplicateAndForeignEvidence(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	tests := []struct {
		name string
		make func(t *testing.T, policy ChargePolicy, call CallUsageRecord) (CallUsageRecord, []CallLegUsageRecord)
		want error
	}{
		{
			name: "unknown outcome",
			make: func(t *testing.T, policy ChargePolicy, call CallUsageRecord) (CallUsageRecord, []CallLegUsageRecord) {
				t.Helper()
				return call, []CallLegUsageRecord{retailSelectionLeg(t, call.CallID, "b-unknown", 1, LegOutcomeUnknown, SurfacedYes)}
			},
			want: ErrRetailSelectionOutcomeUnknown,
		},
		{
			name: "missing selected observation",
			make: func(t *testing.T, policy ChargePolicy, call CallUsageRecord) (CallUsageRecord, []CallLegUsageRecord) {
				t.Helper()
				leg := testCallLegUsageRecord(call.CallID, "b-missing")
				leg.ALegID = "a-1"
				leg.AttemptSeq = 1
				return call, []CallLegUsageRecord{leg}
			},
			want: ErrRetailSelectionIncomplete,
		},
		{
			name: "duplicate observation refs",
			make: func(t *testing.T, policy ChargePolicy, call CallUsageRecord) (CallUsageRecord, []CallLegUsageRecord) {
				t.Helper()
				leg := retailSelectionLeg(t, call.CallID, "b-duplicate", 1, LegOutcomeWinner, SurfacedYes)
				ref, err := leg.Observations[0].Ref(leg.Observations[0].Subject.StoreID)
				if err != nil {
					t.Fatal(err)
				}
				leg.ObservationRefs = []metering.ObservationRef{ref, ref}
				return call, []CallLegUsageRecord{leg}
			},
			want: ErrRetailSelectionDuplicate,
		},
		{
			name: "cross-call observation",
			make: func(t *testing.T, policy ChargePolicy, call CallUsageRecord) (CallUsageRecord, []CallLegUsageRecord) {
				t.Helper()
				leg := retailSelectionLeg(t, call.CallID, "b-foreign", 1, LegOutcomeWinner, SurfacedYes)
				foreign := mustBillingCallID(t)
				for i := range leg.Observations {
					leg.Observations[i].Subject.BillingCallID = foreign.String()
					leg.Observations[i].Correlation.BillingCallID = foreign.String()
					leg.Observations[i].Correlation.CallID = foreign.String()
				}
				return call, []CallLegUsageRecord{leg}
			},
			want: ErrRetailSelectionScopeMismatch,
		},
		{
			name: "customer-boundary observation",
			make: func(t *testing.T, policy ChargePolicy, call CallUsageRecord) (CallUsageRecord, []CallLegUsageRecord) {
				t.Helper()
				leg := retailSelectionLeg(t, call.CallID, "b-boundary", 1, LegOutcomeWinner, SurfacedYes)
				observation := &leg.Observations[0]
				observation.Origin = metering.OriginLocal
				observation.Acquisition = metering.AcquisitionLocalTransport
				observation.Subject.Kind = metering.SubjectRequest
				observation.Subject.RequestID = "request-1"
				observation.Subject.BLegID = ""
				observation.Subject.BillingCallID = call.CallID.String()
				observation.Correlation.BLegID = ""
				observation.Correlation.BillingCallID = call.CallID.String()
				observation.Correlation.CallID = call.CallID.String()
				return call, []CallLegUsageRecord{leg}
			},
			want: ErrRetailSelectionScopeMismatch,
		},
		{
			name: "conflicting evidence",
			make: func(t *testing.T, policy ChargePolicy, call CallUsageRecord) (CallUsageRecord, []CallLegUsageRecord) {
				t.Helper()
				leg := retailSelectionLeg(t, call.CallID, "b-conflict", 1, LegOutcomeWinner, SurfacedYes)
				leg.EvidenceConflicts = []EvidenceConflict{{Identity: "obs", ExistingHash: "old", IncomingHash: "new"}}
				return call, []CallLegUsageRecord{leg}
			},
			want: ErrRetailSelectionUntrusted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
			call := testCallUsageRecord(callID)
			call.ALegID = "a-1"
			call.ExpectedBLegIDs = []string{"b-unknown"}
			call.CustomerPricingRef = policy.PricingRef
			call.ChargePolicyRef = policy.Ref
			call, legs := tt.make(t, policy, call)
			call.ExpectedBLegIDs = make([]string, 0, len(legs))
			for _, leg := range legs {
				call.ExpectedBLegIDs = append(call.ExpectedBLegIDs, leg.BLegID)
			}
			_, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestPhase10RetailSelectorRejectsEmptyAndAmbiguousSelection(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-failed")
	empty := retailSelectionLeg(t, call.CallID, "b-failed", 1, LegOutcomeFailed, SurfacedNo)
	if _, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{empty}, Policy: policy}); !errors.Is(err, ErrRetailSelectionEmpty) {
		t.Fatalf("empty selection error = %v, want %v", err, ErrRetailSelectionEmpty)
	}

	call.ExpectedBLegIDs = []string{"b-one", "b-two"}
	first := retailSelectionLeg(t, call.CallID, "b-one", 1, LegOutcomeWinner, SurfacedYes)
	second := retailSelectionLeg(t, call.CallID, "b-two", 2, LegOutcomeWinner, SurfacedYes)
	if _, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{first, second}, Policy: policy}); !errors.Is(err, ErrRetailSelectionAmbiguous) {
		t.Fatalf("ambiguous selection error = %v, want %v", err, ErrRetailSelectionAmbiguous)
	}
}

func TestPhase10RetailSelectorFreezesPolicyAndObservationReferences(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionNamedOutcomes, RetailBasisIndependent)
	policy.Retail.OutcomeSubset = []LegOutcome{LegOutcomeWinner}
	call := retailSelectionCall(t, policy, "b-winner")
	legs := []CallLegUsageRecord{retailSelectionLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes)}
	got, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	policy.Retail.OutcomeSubset[0] = LegOutcomeFailed
	legs[0].Observations[0].ID = "mutated"
	if got.PolicyRef != policy.Ref || got.Mode != RetailSelectionNamedOutcomes || len(got.SelectedBLegs) != 1 || got.SelectedBLegs[0].ObservationRefs[0].ObservationID != "obs-b-winner" {
		t.Fatalf("frozen selection changed after input mutation: %+v", got)
	}
	clone := got.Clone()
	clone.SelectedBLegs[0].ObservationRefs[0].ObservationID = "clone-mutation"
	if got.SelectedBLegs[0].ObservationRefs[0].ObservationID != "obs-b-winner" {
		t.Fatal("selection clone aliases immutable references")
	}
}
