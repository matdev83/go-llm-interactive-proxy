package billing

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
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
	// CustomerTariff and ModelTariffs carry the immutable generic customer
	// tariff material for V2 B-leg retail rating. V1 rows continue through the
	// legacy scalar adapter when no V2 quantity evidence is present.
	CustomerTariff economics.TariffSnapshot
	ModelTariffs   []ModelCustomerTariff
	// ProviderCost is used only by an explicit cost-pass-through customer
	// policy. It is never consulted by independent retail rating.
	ProviderCost *CostPassThroughProviderCost
}
type CallRatingResult struct {
	CallID            BillingCallID
	CustomerCharge    Money
	Fingerprint       string
	CustomerValuation economics.Valuation
	// CustomerUnitOperation is an optional customer-owned allowance debit or
	// reservation plan. When supplied, the durable settlement adapter applies it
	// in the same transaction as the monetary customer posting.
	CustomerUnitOperation *CustomerUnitOperation
	// CustomerUnitFallbackCharge is the concrete charge for uncovered units. It
	// is required when the atomic unit result reports a bounded fallback and is
	// checked against that operation's bound before settlement commits.
	CustomerUnitFallbackCharge *Money
	CostPassThrough            *CostPassThroughSettlement
}
type ApplyCallBillingInput struct {
	Call          CallUsageRecord
	Exposure      CallExposure
	Result        CallRatingResult
	OperationKind string
}
type CallSettlement struct {
	CallID             BillingCallID
	Customer           Posting
	CustomerUnitResult *CustomerUnitOperationResult
	CostPassThrough    *CostPassThroughSettlement
	Replayed           bool
}

func RateCall(in CallRatingInput) (CallRatingResult, error) {
	call, err := in.Call.Seal()
	if err != nil {
		return CallRatingResult{}, err
	}
	if err := in.MaxCustomerCharge.Validate(); err != nil {
		return CallRatingResult{}, err
	}
	policy := in.CustomerPolicy.Clone()
	// The already admitted maximum is an acceptable compatibility source for a
	// legacy explicit pass-through policy that omitted its serialized bound.
	if policy.Retail != nil && policy.Retail.Basis == RetailBasisCostPassThrough && policy.Retail.CostPassThrough != nil && policy.Retail.CostPassThrough.SafeBound == nil {
		retail := policy.Retail.Clone()
		pass := retail.CostPassThrough.Clone()
		bound := in.MaxCustomerCharge
		pass.SafeBound = &bound
		retail.CostPassThrough = &pass
		policy.Retail = &retail
	}
	retailPolicy, policyErr := ResolveRetailSelectionPolicy(policy)
	if policyErr != nil {
		return CallRatingResult{}, policyErr
	}
	passThrough := retailPolicy.Basis == RetailBasisCostPassThrough
	if !passThrough && in.MaxCustomerCharge.Currency != in.CustomerPricing.Currency {
		return CallRatingResult{}, ErrRatingCurrencyMismatch
	}
	if in.CustomerPolicy.Ref != call.ChargePolicyRef || in.CustomerPolicy.PricingRef != call.CustomerPricingRef {
		return CallRatingResult{}, ErrRatingSnapshotMismatch
	}
	if !passThrough && in.CustomerPricing.Ref != call.CustomerPricingRef {
		return CallRatingResult{}, ErrRatingSnapshotMismatch
	}
	if err := policy.Validate(); err != nil {
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
	if passThrough {
		retail, retailErr := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
			Call: call, Legs: sealedLegs, Policy: policy, ProviderCost: in.ProviderCost,
			Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		})
		if retailErr != nil {
			return CallRatingResult{CallID: call.CallID, CustomerValuation: retail.Valuation, CostPassThrough: retail.CostPassThrough}, retailErr
		}
		if retail.CustomerCharge.Currency != in.MaxCustomerCharge.Currency {
			return CallRatingResult{}, ErrRatingCurrencyMismatch
		}
		return CallRatingResult{
			CallID: call.CallID, CustomerCharge: retail.CustomerCharge,
			Fingerprint: retail.Fingerprint, CustomerValuation: retail.Valuation,
			CostPassThrough: retail.CostPassThrough,
		}, nil
	}
	if hasV2RetailQuantityEvidence(sealedLegs) {
		if in.CustomerTariff.Ref.ID == "" {
			return CallRatingResult{}, fmt.Errorf("%w: generic customer tariff is required for V2 B-leg evidence", ErrRetailRateIncomplete)
		}
		retail, retailErr := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
			Call: call, Legs: sealedLegs, Policy: policy,
			Tariff: in.CustomerTariff, ModelTariffs: in.ModelTariffs,
			Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		})
		if retailErr != nil {
			return CallRatingResult{CallID: call.CallID, CustomerValuation: retail.Valuation}, retailErr
		}
		if err := in.MaxCustomerCharge.Validate(); err != nil {
			return CallRatingResult{}, err
		}
		if retail.CustomerCharge.Currency != in.MaxCustomerCharge.Currency {
			return CallRatingResult{}, ErrRatingCurrencyMismatch
		}
		return CallRatingResult{
			CallID: call.CallID, CustomerCharge: retail.CustomerCharge,
			Fingerprint: retail.Fingerprint, CustomerValuation: retail.Valuation,
		}, nil
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

func hasV2RetailQuantityEvidence(legs []CallLegUsageRecord) bool {
	for _, leg := range legs {
		if leg.EvidenceVersion < EvidenceFormatVersionV2 {
			continue
		}
		for _, observation := range leg.Observations {
			if isRetailBLegObservation(observation) {
				return true
			}
		}
	}
	return false
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
