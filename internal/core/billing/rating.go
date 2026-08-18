package billing

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrRatingInvalid          = errors.New("billing: invalid rating input")
	ErrRatingSnapshotMismatch = errors.New("billing: rating snapshot identity mismatch")
	ErrRatingCurrencyMismatch = errors.New("billing: rating currency mismatch")
	ErrRatingEvidenceMissing  = errors.New("billing: required rating evidence is missing")
	ErrUnreconciledCost       = errors.New("billing: provider cost is unreconciled")
	// ErrBillingAttemptSequenceUnknown fails closed when customer leg
	// selection requires the persisted B2BUA attempt sequence but a legacy
	// pre-fix leg row carries none. The call must be retried/reconciled rather
	// than guessing order from IDs or timestamps.
	ErrBillingAttemptSequenceUnknown = errors.New("billing: customer leg selection requires unknown attempt sequence")
)

type OperatorRateSnapshot struct {
	Ref                      VersionRef
	Currency                 string
	InputPerMillionNano      int64
	OutputPerMillionNano     int64
	CacheReadPerMillionNano  int64
	CacheWritePerMillionNano int64
	ReasoningPerMillionNano  int64
	InputRatePresent         bool
	OutputRatePresent        bool
	CacheReadRatePresent     bool
	CacheWriteRatePresent    bool
	ReasoningRatePresent     bool
}

func (r OperatorRateSnapshot) Validate() error {
	if strings.TrimSpace(r.Ref.ID) == "" || strings.TrimSpace(r.Ref.Version) == "" {
		return fmt.Errorf("%w: operator rate reference is required", ErrRatingInvalid)
	}
	if strings.TrimSpace(r.Currency) == "" {
		return fmt.Errorf("%w: operator rate currency is required", ErrRatingInvalid)
	}
	if r.InputRatePresent && r.InputPerMillionNano < 0 || r.OutputRatePresent && r.OutputPerMillionNano < 0 ||
		r.CacheReadRatePresent && r.CacheReadPerMillionNano < 0 || r.CacheWriteRatePresent && r.CacheWritePerMillionNano < 0 ||
		r.ReasoningRatePresent && r.ReasoningPerMillionNano < 0 {
		return fmt.Errorf("%w: operator rates cannot be negative", ErrRatingInvalid)
	}
	return nil
}

type OperatorRateSet []OperatorRateSnapshot

func (s OperatorRateSet) Resolve(ref VersionRef) (OperatorRateSnapshot, bool) {
	for _, candidate := range s {
		if candidate.Ref == ref {
			return candidate, true
		}
	}
	return OperatorRateSnapshot{}, false
}

type ModelCustomerPricing struct {
	BackendID string
	ModelID   string
	Pricing   PricingSnapshot
}
type OperatorCostResult struct {
	LURKey             string
	Amount             Money
	AmountPresent      bool
	Reconciled         bool
	Authoritative      bool
	UnreconciledReason string
}

func rateCustomerCharge(legs []CallLegUsageRecord, outcome TurnOutcome, pricing PricingSnapshot, policy ChargePolicy, modelPricing []ModelCustomerPricing) (Money, error) {
	selected, err := selectCustomerLegs(legs, policy.Scope, outcome)
	if err != nil {
		return Money{}, err
	}
	var total int64
	for _, leg := range selected {
		legPricing, err := customerPricingForLeg(leg, pricing, modelPricing)
		if err != nil {
			return Money{}, err
		}
		strictEvidence := outcome == TurnOutcomeCompleted && leg.Surfaced == SurfacedYes
		amount, err := chargeLeg(leg, legPricing, policy, strictEvidence)
		if err != nil {
			return Money{}, err
		}
		total, err = addNonNegative(total, amount)
		if err != nil {
			return Money{}, err
		}
	}
	return Money{Nano: total, Currency: pricing.Currency}, nil
}

func customerPricingForLeg(leg CallLegUsageRecord, defaultPricing PricingSnapshot, modelPricing []ModelCustomerPricing) (PricingSnapshot, error) {
	if len(modelPricing) == 0 {
		return defaultPricing, nil
	}
	for _, card := range modelPricing {
		if card.BackendID == leg.BackendID && card.ModelID == leg.ModelID {
			return card.Pricing, nil
		}
	}
	return PricingSnapshot{}, fmt.Errorf("%w: customer pricing for %s/%s", ErrRatingEvidenceMissing, leg.BackendID, leg.ModelID)
}

// selectCustomerLegs filters provider-accepted customer evidence before scope
// selection. In particular, all-potential means every accepted evidence leg,
// never a planned, rejected, never-started, or evidence-unavailable leg.
func selectCustomerLegs(legs []CallLegUsageRecord, scope ChargePolicyScope, outcome TurnOutcome) ([]CallLegUsageRecord, error) {
	accepted := acceptedCustomerLegs(legs)
	if scope == ChargeAllPotentialLegs {
		return accepted, nil
	}
	if outcome != TurnOutcomeCompleted {
		return oneLogicalAcceptedTurn(accepted)
	}
	selected := make([]CallLegUsageRecord, 0, len(accepted))
	for _, leg := range accepted {
		if leg.Surfaced == SurfacedYes {
			selected = append(selected, leg)
		}
	}
	return selected, nil
}

func acceptedCustomerLegs(legs []CallLegUsageRecord) []CallLegUsageRecord {
	accepted := make([]CallLegUsageRecord, 0, len(legs))
	for _, leg := range legs {
		if providerAcceptedEvidence(leg.Evidence) {
			accepted = append(accepted, leg)
		}
	}
	return accepted
}

// oneLogicalAcceptedTurn selects a single billable accepted leg for an
// interrupted (failed/canceled) call. A surfaced leg is unambiguous and needs
// no order. Without a surfaced leg the latest accepted attempt is chosen using
// the persisted B2BUA sequence; when the sequence is unknown for more than one
// accepted leg the selection is indeterminate and fails closed.
func oneLogicalAcceptedTurn(accepted []CallLegUsageRecord) ([]CallLegUsageRecord, error) {
	if len(accepted) == 0 {
		return accepted, nil
	}
	surfaced := make([]CallLegUsageRecord, 0, 1)
	for _, leg := range accepted {
		if leg.Surfaced == SurfacedYes {
			surfaced = append(surfaced, leg)
		}
	}
	if len(surfaced) > 0 {
		return surfaced, nil
	}
	if len(accepted) == 1 {
		return accepted, nil
	}
	for _, leg := range accepted {
		if leg.AttemptSeq <= 0 {
			return nil, fmt.Errorf("%w: interrupted call has %d accepted legs and requires the latest accepted attempt", ErrBillingAttemptSequenceUnknown, len(accepted))
		}
	}
	best := accepted[0]
	for _, leg := range accepted[1:] {
		if leg.AttemptSeq > best.AttemptSeq {
			best = leg
		}
	}
	return []CallLegUsageRecord{best}, nil
}

func providerAcceptedEvidence(e FinalBillingEvidence) bool {
	if authoritativeProviderCost(e) {
		return true
	}
	return e.InputTokens.Present || e.OutputTokens.Present || e.CacheReadTokens.Present ||
		e.CacheWriteTokens.Present || e.ReasoningTokens.Present || e.TotalTokens.Present
}

func chargeLeg(leg CallLegUsageRecord, pricing PricingSnapshot, policy ChargePolicy, strictEvidence bool) (int64, error) {
	var total int64
	add := func(amount int64) error {
		var err error
		total, err = addNonNegative(total, amount)
		return err
	}
	chargeQuantity := func(include bool, qty Quantity, ratePresent bool, rateNano int64, dim string) error {
		if !include {
			return nil
		}
		if !qty.Present {
			if strictEvidence {
				return fmt.Errorf("%w: %s tokens for call leg %q", ErrRatingEvidenceMissing, dim, leg.BLegID)
			}
			return nil
		}
		if !ratePresent {
			return fmt.Errorf("%w: %s rate for LUR %q", ErrRatingEvidenceMissing, dim, leg.BLegID)
		}
		amount, err := exactTokensAtRate(qty.Value, rateNano)
		if err != nil {
			return err
		}
		return add(amount)
	}
	if err := chargeQuantity(policy.IncludeInputTokens, leg.Evidence.InputTokens, pricing.InputRatePresent, pricing.InputPerMillionNano, "input"); err != nil {
		return 0, err
	}
	if err := chargeQuantity(policy.IncludeOutputTokens, leg.Evidence.OutputTokens, pricing.OutputRatePresent, pricing.OutputPerMillionNano, "output"); err != nil {
		return 0, err
	}
	if policy.IncludeFixedCharges {
		for _, component := range pricing.FixedCharges {
			amount, err := componentAmount(component, pricing.Currency)
			if err != nil {
				return 0, err
			}
			if err := add(amount); err != nil {
				return 0, err
			}
		}
	}
	if policy.IncludeResourceCharges {
		for _, component := range pricing.ResourceCharges {
			amount, err := componentAmount(component, pricing.Currency)
			if err != nil {
				return 0, err
			}
			if err := add(amount); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func authoritativeProviderCost(e FinalBillingEvidence) bool {
	return e.Cost.Present && e.Authority == EvidenceAuthorityAuthoritative
}

func fallbackOperatorCost(leg CallLegUsageRecord, rate OperatorRateSnapshot, found bool, currency string) (int64, string, bool) {
	if !found || rate.Validate() != nil || rate.Currency != currency {
		return 0, "exact_operator_rate_unavailable", false
	}
	type dimension struct {
		quantity Quantity
		rate     int64
		present  bool
	}
	dimensions := []dimension{
		{leg.Evidence.InputTokens, rate.InputPerMillionNano, rate.InputRatePresent},
		{leg.Evidence.OutputTokens, rate.OutputPerMillionNano, rate.OutputRatePresent},
		{leg.Evidence.CacheReadTokens, rate.CacheReadPerMillionNano, rate.CacheReadRatePresent},
		{leg.Evidence.CacheWriteTokens, rate.CacheWritePerMillionNano, rate.CacheWriteRatePresent},
		{leg.Evidence.ReasoningTokens, rate.ReasoningPerMillionNano, rate.ReasoningRatePresent},
	}
	var total int64
	matched := false
	for _, d := range dimensions {
		if !d.quantity.Present {
			continue
		}
		if !d.present {
			return 0, "operator_rate_or_quantity_incomplete", false
		}
		matched = true
		value, err := exactTokensAtRate(d.quantity.Value, d.rate)
		if err != nil {
			return 0, "operator_rate_arithmetic_overflow", false
		}
		total, err = addNonNegative(total, value)
		if err != nil {
			return 0, "operator_rate_arithmetic_overflow", false
		}
	}
	if !matched {
		return 0, "operator_rate_or_quantity_incomplete", false
	}
	return total, "", true
}
