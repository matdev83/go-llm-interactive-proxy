package billing

import (
	"fmt"
	"sort"
	"strings"
)

type CallRatingInput struct {
	Call              CallUsageRecord
	Legs              []CallLegUsageRecord
	MaxCustomerCharge Money
	CustomerPricing   PricingSnapshot
	CustomerPolicy    ChargePolicy
	// ModelPricing carries the effective per backend/model customer pricing
	// cards resolved for the call legs. An empty set means no route/model
	// override exists and the configured default pricing applies to every
	// selected leg. When overrides exist, each selected leg must resolve its
	// own card; a missing applicable card fails rating explicitly rather than
	// silently substituting an unrelated model or the default price.
	//
	// Operator-rate data is deliberately absent from this customer type: it
	// belongs to provider COGS processing only, so provider-cost readiness can
	// never couple into customer settlement.
	ModelPricing []ModelCustomerPricing
}
type CallRatingResult struct {
	CallID         BillingCallID
	CustomerCharge Money
	Fingerprint    string
}
type ApplyCallBillingInput struct {
	Call          CallUsageRecord
	Exposure      CallExposure
	Result        CallRatingResult
	OperationKind string
}
type CallSettlement struct {
	CallID   BillingCallID
	Customer Posting
	Replayed bool
}

func RateCall(in CallRatingInput) (CallRatingResult, error) {
	call, err := in.Call.Seal()
	if err != nil {
		return CallRatingResult{}, err
	}
	if err := in.MaxCustomerCharge.Validate(); err != nil {
		return CallRatingResult{}, err
	}
	if in.MaxCustomerCharge.Currency != in.CustomerPricing.Currency {
		return CallRatingResult{}, ErrRatingCurrencyMismatch
	}
	if in.CustomerPricing.Ref != call.CustomerPricingRef || in.CustomerPolicy.Ref != call.ChargePolicyRef || in.CustomerPolicy.PricingRef != call.CustomerPricingRef {
		return CallRatingResult{}, ErrRatingSnapshotMismatch
	}
	if err := in.CustomerPolicy.Validate(); err != nil {
		return CallRatingResult{}, err
	}
	sealedLegs := make([]CallLegUsageRecord, 0, len(in.Legs))
	legFingerprints := make([]string, 0, len(in.Legs))
	for _, source := range in.Legs {
		leg, sealErr := source.Seal()
		if sealErr != nil {
			return CallRatingResult{}, sealErr
		}
		if leg.CallID != call.CallID || !containsExpectedLeg(call.ExpectedBLegIDs, leg.BLegID) {
			return CallRatingResult{}, fmt.Errorf("%w: leg %q is not expected for call", ErrRatingSnapshotMismatch, leg.BLegID)
		}
		sealedLegs = append(sealedLegs, leg)
		legFingerprints = append(legFingerprints, leg.Fingerprint)
	}
	if err := in.CustomerPricing.Validate(in.MaxCustomerCharge.Currency); err != nil {
		return CallRatingResult{}, err
	}
	customer, err := rateCustomerCharge(sealedLegs, call.Outcome, in.CustomerPricing, in.CustomerPolicy, in.ModelPricing)
	if err != nil {
		return CallRatingResult{}, err
	}
	fp, err := callRatingFingerprint(call, customer, in.MaxCustomerCharge, legFingerprints)
	if err != nil {
		return CallRatingResult{}, err
	}
	return CallRatingResult{CallID: call.CallID, CustomerCharge: customer, Fingerprint: fp}, nil
}

func containsExpectedLeg(ids []string, id string) bool {
	id = strings.TrimSpace(id)
	for _, candidate := range ids {
		if strings.TrimSpace(candidate) == id {
			return true
		}
	}
	return false
}

func callRatingFingerprint(call CallUsageRecord, amount Money, max Money, legFingerprints []string) (string, error) {
	sorted := append([]string(nil), legFingerprints...)
	sort.Strings(sorted)
	return fmt.Sprintf(
		"call-rating:v2:%s:%d:%s:max=%d:%s:pricing=%s@%s:policy=%s@%s:legs=%s",
		call.CallID.String(),
		amount.Nano,
		amount.Currency,
		max.Nano,
		max.Currency,
		call.CustomerPricingRef.ID,
		call.CustomerPricingRef.Version,
		call.ChargePolicyRef.ID,
		call.ChargePolicyRef.Version,
		strings.Join(sorted, ","),
	), nil
}
