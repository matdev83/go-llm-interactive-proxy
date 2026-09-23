package billing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// TestRefinement83 subpass B: explicit cost-pass-through retail and
// non-request resource/account cost allocation over the same subpass A
// call scenario. Subpass A file and assertions are untouched; its fixture
// and tariff helpers are reused read-only.

// ref83PassThroughPolicy freezes an explicit cost-pass-through offer on the
// winner-only retail mode with the given safe bound and missing-cost rule.
func ref83PassThroughPolicy(missing CostPassThroughMissingCostPolicy, allowLate bool) ChargePolicy {
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisCostPassThrough)
	retail := policy.Retail.Clone()
	bound := Money{Nano: 10_000_000_000, Currency: "USD"}
	retail.CostPassThrough = &CostPassThroughPolicy{
		MissingCost: missing, SafeBound: &bound, AllowLateAdjustment: allowLate,
	}
	policy.Retail = &retail
	return policy
}

// ref83ProviderCost is the authoritative, reconciled provider cost accepted
// for pass-through. The 7.50 USD amount intentionally equals the subpass A
// operator COGS subtotal so the settlement can be told apart from both
// independent retail charges (1.97/5.77 USD).
func ref83ProviderCost() CostPassThroughProviderCost {
	return CostPassThroughProviderCost{
		LURKey: "lur-83", ValuationID: "valuation-83", Revision: 3,
		InputHash: strings.Repeat("c", 64), Amount: Money{Nano: 7_500_000_000, Currency: "USD"},
		AmountPresent: true, Reconciled: true, Authoritative: true,
	}
}

func TestRefinement83CostPassThroughSettlesAcceptedProviderCost(t *testing.T) {
	t.Parallel()
	policy := ref83PassThroughPolicy(CostPassThroughMissingCostPending, true)
	call, legs := ref83Fixture(t, policy)

	// Pass-through never silently widens retail inference: selection stays
	// winner-only even though five attributable B-legs carry usage.
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.SelectedBLegs) != 1 || selection.SelectedBLegs[0].BLegID != "b-winner" {
		t.Fatalf("pass-through selection = %+v, want winner only, never all provider usage", selection.SelectedBLegs)
	}
	if selection.Basis != RetailBasisCostPassThrough {
		t.Fatalf("selection basis = %q, want explicit cost pass-through", selection.Basis)
	}

	provider := ref83ProviderCost()
	wantFingerprint, err := provider.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	// No customer tariff is supplied: the explicit basis settles the accepted
	// selected cost, so provider-rate and independent-retail tariff material
	// stay out of the decision entirely.
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: Money{Nano: 10_000_000_000, Currency: "USD"},
		CustomerPolicy: policy, ProviderCost: &provider,
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}
	if result.CustomerCharge != (Money{Nano: 7_500_000_000, Currency: "USD"}) {
		t.Fatalf("customer charge = %+v, want accepted provider cost 7.50 USD", result.CustomerCharge)
	}
	if result.CustomerCharge == (Money{Nano: 1_970_000_000, Currency: "USD"}) ||
		result.CustomerCharge == (Money{Nano: 5_770_000_000, Currency: "USD"}) {
		t.Fatalf("pass-through charge %+v collides with an independent retail meaning", result.CustomerCharge)
	}
	settlement := result.CostPassThrough
	if settlement == nil {
		t.Fatal("pass-through settlement is missing")
	}
	if settlement.Status != CostPassThroughSettlementFinal {
		t.Fatalf("settlement status = %q, want final", settlement.Status)
	}
	if settlement.PolicyRef != policy.Ref || settlement.Policy.MissingCost != CostPassThroughMissingCostPending ||
		!settlement.Policy.AllowLateAdjustment {
		t.Fatalf("settlement lost policy/version/bound identity: %+v", settlement)
	}
	if settlement.SafeBound != (Money{Nano: 10_000_000_000, Currency: "USD"}) ||
		settlement.PostedAmount != result.CustomerCharge {
		t.Fatalf("settlement bound/posted = %+v/%+v, want 10.00 bound with 7.50 posted", settlement.SafeBound, settlement.PostedAmount)
	}
	if settlement.ProviderCost == nil || *settlement.ProviderCost != provider {
		t.Fatalf("settlement lost provider-cost identity: %+v", settlement.ProviderCost)
	}
	gotFingerprint, err := settlement.ProviderCost.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if gotFingerprint != wantFingerprint {
		t.Fatal("settlement provider-cost fingerprint drifted from the accepted input")
	}
	if provider != ref83ProviderCost() {
		t.Fatal("rating mutated the source provider-cost input")
	}

	again, err := RateCall(CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: Money{Nano: 10_000_000_000, Currency: "USD"},
		CustomerPolicy: policy, ProviderCost: &provider,
	})
	if err != nil {
		t.Fatalf("RateCall replay: %v", err)
	}
	againStateFingerprint, err := again.CostPassThrough.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	stateFingerprint, err := settlement.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint != result.Fingerprint || again.CustomerCharge != result.CustomerCharge ||
		againStateFingerprint != stateFingerprint {
		t.Fatalf("replay changed settlement: first=%+v replay=%+v", result, again)
	}
}

func TestRefinement83CostPassThroughMissingCostStaysBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("pending posts nothing", func(t *testing.T) {
		t.Parallel()
		policy := ref83PassThroughPolicy(CostPassThroughMissingCostPending, false)
		call, legs := ref83Fixture(t, policy)
		selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		result, err := RateSelectedRetailBLegs(ctx, RetailRatingInput{
			Call: call, Legs: legs, Selection: selection, Policy: policy,
			Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		})
		if err != nil {
			t.Fatalf("RateSelectedRetailBLegs: %v", err)
		}
		if result.CustomerCharge != (Money{Nano: 0, Currency: "USD"}) {
			t.Fatalf("pending charge = %+v, want zero with no monetary posting", result.CustomerCharge)
		}
		if result.CostPassThrough == nil || result.CostPassThrough.Status != CostPassThroughSettlementPending ||
			result.CostPassThrough.PostedAmount.Nano != 0 ||
			result.CostPassThrough.SafeBound != (Money{Nano: 10_000_000_000, Currency: "USD"}) {
			t.Fatalf("pending settlement = %+v, want bounded pending state", result.CostPassThrough)
		}
	})

	t.Run("provisional posts bound", func(t *testing.T) {
		t.Parallel()
		policy := ref83PassThroughPolicy(CostPassThroughMissingCostProvisional, true)
		call, legs := ref83Fixture(t, policy)
		selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		result, err := RateSelectedRetailBLegs(ctx, RetailRatingInput{
			Call: call, Legs: legs, Selection: selection, Policy: policy,
			Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		})
		if err != nil {
			t.Fatalf("RateSelectedRetailBLegs: %v", err)
		}
		if result.CustomerCharge != (Money{Nano: 10_000_000_000, Currency: "USD"}) ||
			result.CostPassThrough.Status != CostPassThroughSettlementProvisional {
			t.Fatalf("provisional = %+v, want bound posted provisionally", result)
		}
	})

	t.Run("untrusted cost fails closed", func(t *testing.T) {
		t.Parallel()
		policy := ref83PassThroughPolicy(CostPassThroughMissingCostPending, true)
		call, legs := ref83Fixture(t, policy)
		selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		provider := ref83ProviderCost()
		provider.Authoritative = false
		_, err = RateSelectedRetailBLegs(ctx, RetailRatingInput{
			Call: call, Legs: legs, Selection: selection, Policy: policy,
			Payer:        metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
			ProviderCost: &provider,
		})
		if !errors.Is(err, ErrCostPassThroughProviderUntrusted) {
			t.Fatalf("untrusted cost error = %v, want %v", err, ErrCostPassThroughProviderUntrusted)
		}
	})

	t.Run("incomparable currency fails closed", func(t *testing.T) {
		t.Parallel()
		policy := ref83PassThroughPolicy(CostPassThroughMissingCostPending, true)
		call, legs := ref83Fixture(t, policy)
		selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		provider := ref83ProviderCost()
		provider.Amount = Money{Nano: 7_500_000_000, Currency: "EUR"}
		_, err = RateSelectedRetailBLegs(ctx, RetailRatingInput{
			Call: call, Legs: legs, Selection: selection, Policy: policy,
			Payer:        metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
			ProviderCost: &provider,
		})
		if !errors.Is(err, ErrCostPassThroughCurrencyMismatch) {
			t.Fatalf("currency mismatch error = %v, want %v", err, ErrCostPassThroughCurrencyMismatch)
		}
	})

	t.Run("over-bound cost fails closed", func(t *testing.T) {
		t.Parallel()
		policy := ref83PassThroughPolicy(CostPassThroughMissingCostPending, true)
		call, legs := ref83Fixture(t, policy)
		selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		provider := ref83ProviderCost()
		provider.Amount = Money{Nano: 11_000_000_000, Currency: "USD"}
		_, err = RateSelectedRetailBLegs(ctx, RetailRatingInput{
			Call: call, Legs: legs, Selection: selection, Policy: policy,
			Payer:        metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
			ProviderCost: &provider,
		})
		if !errors.Is(err, ErrCostPassThroughBoundExceeded) {
			t.Fatalf("bound exceeded error = %v, want %v", err, ErrCostPassThroughBoundExceeded)
		}
	})
}

// ref83CacheAllocation splits one genuine $12.00 prompt-cache resource cost
// across the winner (1/2) and the retry (1/4) with an explicit 1/4
// unallocated remainder. Weights are exactly conserved.
func ref83CacheAllocation(t *testing.T, callID BillingCallID) economics.AllocationRecord {
	t.Helper()
	amount, err := metering.ParseDecimal("12")
	if err != nil {
		t.Fatal(err)
	}
	target := func(id, bLegID string, numerator string) economics.AllocationTarget {
		return economics.AllocationTarget{
			TargetID: id, Weight: economics.AllocationFraction{Numerator: numerator, Denominator: "4"},
			Target: metering.SubjectRef{
				Kind: metering.SubjectBLeg, StoreID: "store-83", TenantID: "tenant-83",
				BillingCallID: callID.String(), ALegID: "a-83", BLegID: bLegID,
			},
		}
	}
	return economics.AllocationRecord{
		ID: "ref83-cache-tier-cost", Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: "store-83", TenantID: "tenant-83", AccountID: "supplier-account-83",
			ResourceID: "ref83-cache-tier", PeriodID: "2026-09",
			StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC(),
		},
		SourceBasis: economics.BasisStatementReported, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "ref83-cache-weighted", Version: "v1", Hash: strings.Repeat("d", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			target("b-winner-share", "b-winner", "2"),
			target("b-retry-share", "b-retry", "1"),
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "4"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
}

func TestRefinement83NonRequestAllocationConservesAndStaysOffRetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := ref83WinnerPolicy()
	call, legs := ref83Fixture(t, policy)

	allocation := ref83CacheAllocation(t, call.CallID)
	canonical, err := economics.ConserveAllocation(allocation)
	if err != nil {
		t.Fatalf("ConserveAllocation: %v", err)
	}
	if canonical.Fingerprint() == "" {
		t.Fatal("conserved allocation carries no fingerprint")
	}
	cloned := allocation.Clone()
	if again, err := economics.ConserveAllocation(cloned); err != nil {
		t.Fatalf("ConserveAllocation replay: %v", err)
	} else if again.Fingerprint() != canonical.Fingerprint() {
		t.Fatal("conserve replay changed the allocation fingerprint")
	}
	wantShares := map[string]economics.AllocationFraction{
		"b-winner-share": {Numerator: "1", Denominator: "2"},
		"b-retry-share":  {Numerator: "1", Denominator: "4"},
		"unallocated":    {Numerator: "1", Denominator: "4"},
	}
	if len(canonical.Targets) != len(wantShares) {
		t.Fatalf("targets = %+v, want two attributions plus remainder", canonical.Targets)
	}
	for _, target := range canonical.Targets {
		if wantShares[target.TargetID] != target.Share {
			t.Fatalf("target %q share = %+v, want %+v", target.TargetID, target.Share, wantShares[target.TargetID])
		}
	}

	got, err := AttributeOperatorCOGSWithAllocations(legs, []economics.AllocationRecord{canonical}, nil, "USD")
	if err != nil {
		t.Fatalf("AttributeOperatorCOGSWithAllocations: %v", err)
	}
	// Payable B-leg 7.50 plus conserved winner 6.00 plus retry 3.00; the 3.00
	// remainder is retained but never posted into the subtotal.
	if got.KnownSubtotal != (Money{Nano: 16_500_000_000, Currency: "USD"}) {
		t.Fatalf("operator subtotal = %+v, want 16.50 USD", got.KnownSubtotal)
	}
	if !got.Payable || got.Completeness != CostCompletenessKnown {
		t.Fatalf("attribution = %+v, want known payable result", got)
	}
	base, err := AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.IncludedLegKeys) != len(base.IncludedLegKeys) {
		t.Fatalf("included legs changed with allocation: %v vs %v", got.IncludedLegKeys, base.IncludedLegKeys)
	}
	for i := range base.IncludedLegKeys {
		if got.IncludedLegKeys[i] != base.IncludedLegKeys[i] {
			t.Fatalf("included legs changed with allocation: %v vs %v", got.IncludedLegKeys, base.IncludedLegKeys)
		}
	}
	if len(got.AllocatedCostLines) != 3 {
		t.Fatalf("allocated lines = %+v, want two attributions plus remainder", got.AllocatedCostLines)
	}
	byTarget := make(map[string]AllocatedCostLine)
	for _, line := range got.AllocatedCostLines {
		byTarget[line.TargetID] = line
	}
	winner := byTarget["b-winner-share"]
	if winner.SourceSubject.ResourceID != "ref83-cache-tier" || winner.SourceSubject.AccountID != "supplier-account-83" ||
		winner.SourceSubject.PeriodID != "2026-09" || winner.SourceSubject.Kind != metering.SubjectResource {
		t.Fatalf("allocated line lost source resource/account/period: %+v", winner)
	}
	if winner.AllocationID != "ref83-cache-tier-cost" || winner.Policy.Method != "ref83-cache-weighted" ||
		winner.Policy.Version != "v1" || winner.Target.BLegID != "b-winner" {
		t.Fatalf("allocated line lost allocation/policy/target identity: %+v", winner)
	}
	if winner.RoundedAmount == nil || winner.RoundedAmount.NanoUnits != 6_000_000_000 {
		t.Fatalf("winner allocation = %+v, want exactly 6.00 USD", winner.RoundedAmount)
	}
	if retry := byTarget["b-retry-share"]; retry.RoundedAmount == nil || retry.RoundedAmount.NanoUnits != 3_000_000_000 {
		t.Fatalf("retry allocation = %+v, want exactly 3.00 USD", retry.RoundedAmount)
	}
	remainder := byTarget["unallocated"]
	if !remainder.Unallocated || remainder.RoundedAmount == nil || remainder.RoundedAmount.NanoUnits != 3_000_000_000 {
		t.Fatalf("remainder = %+v, want explicit 3.00 USD remainder", remainder)
	}
	for _, line := range got.AllocatedCostLines {
		if line.InferenceEligible {
			t.Fatalf("allocated line became inference evidence: %+v", line)
		}
	}

	// The same allocation attributed over a smaller leg set keeps identical
	// lines: one allocation is never multiplied by B-leg count.
	subset := []CallLegUsageRecord{legs[3], legs[0]}
	narrow, err := AttributeOperatorCOGSWithAllocations(subset, []economics.AllocationRecord{canonical}, nil, "USD")
	if err != nil {
		t.Fatalf("subset attribution: %v", err)
	}
	if len(narrow.AllocatedCostLines) != len(got.AllocatedCostLines) {
		t.Fatalf("subset lines = %+v, want identical allocation lines", narrow.AllocatedCostLines)
	}
	for i := range got.AllocatedCostLines {
		want, have := got.AllocatedCostLines[i], narrow.AllocatedCostLines[i]
		if want.TargetID != have.TargetID || want.AllocationID != have.AllocationID ||
			want.Share != have.Share || want.Unallocated != have.Unallocated ||
			(want.RoundedAmount == nil) != (have.RoundedAmount == nil) ||
			(want.RoundedAmount != nil && want.RoundedAmount.NanoUnits != have.RoundedAmount.NanoUnits) {
			t.Fatalf("subset line %d differs: %+v vs %+v", i, want, have)
		}
	}

	// Retail inference selection and rating do not absorb the allocation:
	// winner-only selection and the 1.97 USD charge from subpass A hold.
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.SelectedBLegs) != 1 || selection.SelectedBLegs[0].BLegID != "b-winner" {
		t.Fatalf("selection changed with allocation present: %+v", selection.SelectedBLegs)
	}
	rated := ref83Rate(t, ctx, call, legs, selection, policy, false)
	if rated.CustomerCharge != (Money{Nano: 1_970_000_000, Currency: "USD"}) {
		t.Fatalf("retail charge = %+v, want unchanged 1.97 USD", rated.CustomerCharge)
	}
}

func TestRefinement83CrossAuthorityUsesAcceptedCostOnly(t *testing.T) {
	t.Parallel()
	policy := ref83WinnerPolicy()
	call, legs := ref83Fixture(t, policy)

	pristine := append([]CallLegUsageRecord(nil), legs...)
	for i := range pristine {
		pristine[i] = pristine[i].Clone()
	}
	allocation, err := economics.ConserveAllocation(ref83CacheAllocation(t, call.CallID))
	if err != nil {
		t.Fatal(err)
	}
	operator, err := AttributeOperatorCOGSWithAllocations(legs, []economics.AllocationRecord{allocation}, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if operator.KnownSubtotal != (Money{Nano: 16_500_000_000, Currency: "USD"}) {
		t.Fatalf("operator subtotal = %+v, want payable B-leg 7.50 plus conserved allocation 9.00", operator.KnownSubtotal)
	}
	for i := range legs {
		before, err := pristine[i].SemanticFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		after, err := legs[i].SemanticFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("attribution mutated source leg %q inputs", legs[i].BLegID)
		}
	}

	passPolicy := ref83PassThroughPolicy(CostPassThroughMissingCostPending, true)
	passCall, passLegs := ref83Fixture(t, passPolicy)
	provider := ref83ProviderCost()
	settled, err := RateCall(CallRatingInput{
		Call: passCall, Legs: passLegs, MaxCustomerCharge: Money{Nano: 10_000_000_000, Currency: "USD"},
		CustomerPolicy: passPolicy, ProviderCost: &provider,
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}
	if settled.CustomerCharge != (Money{Nano: 7_500_000_000, Currency: "USD"}) {
		t.Fatalf("pass-through charge = %+v, want accepted selected cost only", settled.CustomerCharge)
	}
	if settled.CustomerCharge == operator.KnownSubtotal {
		t.Fatal("pass-through swept the resource allocation into the customer charge")
	}
	if settled.CostPassThrough == nil || settled.CostPassThrough.ProviderCost == nil ||
		*settled.CostPassThrough.ProviderCost != provider {
		t.Fatalf("settlement lost accepted provider-cost identity: %+v", settled.CostPassThrough)
	}
	for _, amount := range []Money{operator.KnownSubtotal, settled.CustomerCharge, settled.CostPassThrough.PostedAmount} {
		if amount.Currency != "USD" {
			t.Fatalf("cross-authority amount %+v is not USD", amount)
		}
	}
}
