package billing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAuxiliaryWorkloadIdentityIsBoundedAndContentFree(t *testing.T) {
	t.Parallel()
	identity, err := WorkloadIdentityFromAuxiliaryRole(WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if identity.Class != WorkloadClassAuxiliary || identity.Role != WorkloadRoleCompactionContinuityExtractor {
		t.Fatalf("identity = %+v", identity)
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "prompt") || strings.Contains(strings.ToLower(string(raw)), "content") {
		t.Fatalf("workload identity must remain content-free: %s", raw)
	}
	for _, role := range []string{"", "user supplied prompt", "compaction_continuity_extractor:secret"} {
		if _, err := WorkloadIdentityFromAuxiliaryRole(role); !errors.Is(err, ErrInvalidWorkloadIdentity) {
			t.Fatalf("role %q error = %v, want ErrInvalidWorkloadIdentity", role, err)
		}
	}
}

func TestDedupeKeyForBLegIsScopedToBillingCallAndBLeg(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	key, err := DedupeKeyForBLeg(callID, "b-aux")
	if err != nil {
		t.Fatal(err)
	}
	if key != "lip-b-leg:"+callID.String()+":b-aux" {
		t.Fatalf("dedupe key = %q", key)
	}
	if _, err := DedupeKeyForBLeg(callID, ""); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("empty B-leg dedupe key error = %v, want ErrInvalidRecord", err)
	}
}

func TestAuxiliaryBillingRecordsCarryWorkloadIdentityAndProviderEvidence(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	identity, err := WorkloadIdentityFromAuxiliaryRole(WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	call := testCallUsageRecord(callID)
	call.Workload = identity
	call.ExpectedBLegIDs = []string{"b-fail", "b-win"}
	sealedCall, err := call.Seal()
	if err != nil {
		t.Fatalf("seal call: %v", err)
	}
	for _, legID := range []string{"b-fail", "b-win"} {
		leg := testCallLegUsageRecord(callID, legID)
		leg.AttemptSeq = 1
		leg.Workload = identity
		if legID == "b-fail" {
			leg.AttemptSeq = 2
			leg.Outcome = LegOutcomeLoser
			leg.Surfaced = SurfacedNo
		}
		sealedLeg, err := leg.Seal()
		if err != nil {
			t.Fatalf("seal %s: %v", legID, err)
		}
		if err := ValidateIndependentLeg(sealedLeg); err != nil {
			t.Fatalf("validate independent %s: %v", legID, err)
		}
		if sealedLeg.CallID != sealedCall.CallID || sealedLeg.Workload != sealedCall.Workload {
			t.Fatalf("leg/call identity mismatch: call=%+v leg=%+v", sealedCall.Workload, sealedLeg.Workload)
		}
		if sealedLeg.Evidence.Source != EvidenceSourceProviderReported || sealedLeg.Evidence.Authority != EvidenceAuthorityAuthoritative || sealedLeg.Evidence.DedupeKey == "" {
			t.Fatalf("provider evidence was not preserved: %+v", sealedLeg.Evidence)
		}
		cost, err := RateProviderCost(sealedLeg, nil, "USD")
		if err != nil {
			t.Fatalf("provider cost %s: %v", legID, err)
		}
		if !cost.AmountPresent || cost.Amount.Nano != sealedLeg.Evidence.Cost.NanoUnits {
			t.Fatalf("provider cost %s = %+v, evidence=%+v", legID, cost, sealedLeg.Evidence)
		}
	}
}

func TestAuxiliaryBillingIdentityRejectsNonPositiveAttemptSequence(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	identity, err := WorkloadIdentityFromAuxiliaryRole(WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int{0, -1} {
		leg := testCallLegUsageRecord(callID, "b-invalid")
		leg.AttemptSeq = seq
		leg.Workload = identity
		sealed, sealErr := leg.Seal()
		if seq < 0 {
			if !errors.Is(sealErr, ErrInvalidRecord) {
				t.Fatalf("seq=%d seal = %v, want ErrInvalidRecord", seq, sealErr)
			}
			continue
		}
		if sealErr != nil {
			t.Fatalf("legacy seal must remain readable for seq=%d: %v", seq, sealErr)
		}
		if err := ValidateIndependentLeg(sealed); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("seq=%d validation = %v, want ErrInvalidRecord", seq, err)
		}
	}
}

func TestValidateIndependentLegRequiresEvidenceIdentity(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	identity, err := WorkloadIdentityFromAuxiliaryRole(WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	base := testCallLegUsageRecord(callID, "b-evidence")
	base.AttemptSeq = 1
	base.Workload = identity
	cases := []struct {
		name string
		edit func(*CallLegUsageRecord)
	}{
		{name: "source", edit: func(leg *CallLegUsageRecord) { leg.Evidence.Source = EvidenceSourceUnknown }},
		{name: "authority", edit: func(leg *CallLegUsageRecord) { leg.Evidence.Authority = EvidenceAuthorityUnknown }},
		{name: "dedupe", edit: func(leg *CallLegUsageRecord) { leg.Evidence.DedupeKey = "" }},
		{name: "provider_presence", edit: func(leg *CallLegUsageRecord) {
			leg.Evidence.InputTokens = Quantity{}
			leg.Evidence.OutputTokens = Quantity{}
			leg.Evidence.Cost = MoneyEvidence{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leg := base
			tc.edit(&leg)
			sealed, sealErr := leg.Seal()
			if sealErr != nil {
				t.Fatal(sealErr)
			}
			if err := ValidateIndependentLeg(sealed); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("validation = %v, want ErrInvalidRecord", err)
			}
		})
	}
}

func TestAuxiliaryWorkloadDoesNotChangeCustomerPricing(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	identity, err := WorkloadIdentityFromAuxiliaryRole(WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	pricing := PricingSnapshot{
		Ref:                  VersionRef{ID: "prices", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  10,
		OutputPerMillionNano: 20,
		InputRatePresent:     true,
		OutputRatePresent:    true,
	}
	policy := ChargePolicy{
		Ref:                 VersionRef{ID: "policy", Version: "v1"},
		PricingRef:          pricing.Ref,
		Scope:               ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
	}
	baseCall := testCallUsageRecord(callID)
	baseCall.CustomerPricingRef = pricing.Ref
	baseCall.ChargePolicyRef = policy.Ref
	baseLeg := testCallLegUsageRecord(callID, "b-win")
	baseLeg.AttemptSeq = 1
	baseLeg.Surfaced = SurfacedYes
	baseCall.ExpectedBLegIDs = []string{"b-win"}
	auxCall := baseCall
	auxCall.Workload = identity
	auxLeg := baseLeg
	auxLeg.Workload = identity
	base, err := RateCall(CallRatingInput{Call: baseCall, Legs: []CallLegUsageRecord{baseLeg}, MaxCustomerCharge: Money{Nano: 1000, Currency: "USD"}, CustomerPricing: pricing, CustomerPolicy: policy})
	if err != nil {
		t.Fatalf("base rating: %v", err)
	}
	aux, err := RateCall(CallRatingInput{Call: auxCall, Legs: []CallLegUsageRecord{auxLeg}, MaxCustomerCharge: Money{Nano: 1000, Currency: "USD"}, CustomerPricing: pricing, CustomerPolicy: policy})
	if err != nil {
		t.Fatalf("aux rating: %v", err)
	}
	if aux.CustomerCharge != base.CustomerCharge {
		t.Fatalf("workload classification changed pricing: base=%+v aux=%+v", base.CustomerCharge, aux.CustomerCharge)
	}
}

func TestAuxiliaryCompleteCallRequiresLegWorkloadCorrelation(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	identity, err := WorkloadIdentityFromAuxiliaryRole(WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	call := testCallUsageRecord(callID)
	call.Workload = identity
	call.ExpectedBLegIDs = []string{"b-win"}
	closure, err := call.Seal()
	if err != nil {
		t.Fatal(err)
	}
	leg := testCallLegUsageRecord(callID, "b-win")
	leg.AttemptSeq = 1
	if _, err := leg.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := JoinCompleteCall(closure, []CallLegUsageRecord{leg}); !errors.Is(err, ErrCallIncomplete) {
		t.Fatalf("missing auxiliary leg workload = %v, want ErrCallIncomplete", err)
	}
	leg.Workload = identity
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := JoinCompleteCall(closure, []CallLegUsageRecord{sealedLeg}); err != nil {
		t.Fatalf("matching auxiliary workload = %v", err)
	}
}

func TestSubmittedButDiscardedAuxiliaryResultStillHasProviderCostIdentity(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	identity, err := WorkloadIdentityFromAuxiliaryRole(WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	leg := testCallLegUsageRecord(callID, "b-discarded")
	leg.AttemptSeq = 1
	leg.Workload = identity
	leg.Surfaced = SurfacedNo
	leg.Outcome = LegOutcomeLoser
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateIndependentLeg(sealed); err != nil {
		t.Fatal(err)
	}
	// A stale/discarded semantic result does not erase provider evidence or
	// alter the normal provider-cost path.
	result, err := RateProviderCost(sealed, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reconciled || !result.Authoritative || result.LURKey != sealed.Key {
		t.Fatalf("discarded result provider accounting = %+v", result)
	}
}

func TestAuxiliaryPreSubmitCreditRejectionDoesNotCreateBillingRecord(t *testing.T) {
	t.Parallel()
	store := &creditScreenAccountStub{account: Account{ID: "acct-aux", Currency: "USD", Mode: AccountPrepaid, BalanceNano: 0, State: AccountReady, Version: 1}}
	screen := CheapCreditScreen{Store: store, Currency: "USD", MinPreRouteHeadroomNano: 1}
	if err := screen.Check(t.Context(), "acct-aux"); !errors.Is(err, ErrCreditScreenDenied) {
		t.Fatalf("credit rejection = %v, want ErrCreditScreenDenied", err)
	}
	if store.lookups != 1 {
		t.Fatalf("credit lookups=%d, want one pre-submit lookup", store.lookups)
	}
}

type creditScreenAccountStub struct {
	account Account
	lookups int
}

func (s *creditScreenAccountStub) GetAccount(_ context.Context, _ string) (Account, error) {
	s.lookups++
	return s.account, nil
}
