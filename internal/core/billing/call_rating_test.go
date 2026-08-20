package billing

import (
	"strings"
	"testing"
)

func TestRateCallUsesCallAndLegEvidenceWithoutAuthorizationHold(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	call := testCallUsageRecord(callID)
	pricing := PricingSnapshot{
		Ref: VersionRef{ID: "prices", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
	}
	policy := ChargePolicy{Ref: VersionRef{ID: "policy", Version: "v2"}, PricingRef: pricing.Ref, Scope: ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true}
	failLeg := testCallLegUsageRecord(callID, "b-fail")
	failLeg.Surfaced = SurfacedNo
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{testCallLegUsageRecord(callID, "b-win"), failLeg},
		MaxCustomerCharge: Money{Nano: 100, Currency: "USD"}, CustomerPricing: pricing, CustomerPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CallID != callID || result.CustomerCharge.Nano != 13 {
		t.Fatalf("result = %+v, want call %s and 13 USD", result, callID)
	}
	if result.Fingerprint == "" || !strings.HasPrefix(result.Fingerprint, "call-rating:v2:") {
		t.Fatalf("fingerprint = %q, want call-rating:v2: prefix", result.Fingerprint)
	}
}

func TestRateCallReturnsActualEvenWhenExceedsAdmittedMax(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	call := testCallUsageRecord(callID)
	pricing := PricingSnapshot{
		Ref: VersionRef{ID: "prices", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
	}
	policy := ChargePolicy{Ref: VersionRef{ID: "policy", Version: "v2"}, PricingRef: pricing.Ref, Scope: ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true}
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{testCallLegUsageRecord(callID, "b-win")},
		MaxCustomerCharge: Money{Nano: 1, Currency: "USD"}, CustomerPricing: pricing, CustomerPolicy: policy,
	})
	if err != nil {
		t.Fatalf("RateCall must return actual charge for settlement to enforce max: %v", err)
	}
	if result.CustomerCharge.Nano <= 1 {
		t.Fatalf("customer charge = %d, want greater than admitted max 1", result.CustomerCharge.Nano)
	}
}

func TestRateCallFingerprintChangesWhenPolicyOrLegsDifferAtSameAmount(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	call := testCallUsageRecord(callID)
	pricing := PricingSnapshot{
		Ref: VersionRef{ID: "prices", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
	}
	policyA := ChargePolicy{Ref: VersionRef{ID: "policy", Version: "v2"}, PricingRef: pricing.Ref, Scope: ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true}
	policyB := policyA
	policyB.Ref = VersionRef{ID: "policy", Version: "v3"}
	callB := call
	callB.ChargePolicyRef = policyB.Ref

	leg := testCallLegUsageRecord(callID, "b-win")
	first, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg},
		MaxCustomerCharge: Money{Nano: 100, Currency: "USD"}, CustomerPricing: pricing, CustomerPolicy: policyA,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RateCall(CallRatingInput{
		Call: callB, Legs: []CallLegUsageRecord{leg},
		MaxCustomerCharge: Money{Nano: 100, Currency: "USD"}, CustomerPricing: pricing, CustomerPolicy: policyB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.CustomerCharge.Nano != second.CustomerCharge.Nano {
		t.Fatalf("expected same amount, got %d vs %d", first.CustomerCharge.Nano, second.CustomerCharge.Nano)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatalf("fingerprint must change when policy ref differs: %q", first.Fingerprint)
	}
}
