package billing

import (
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
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

// ModelCustomerTariff carries the immutable component tariff selected for a
// backend/model route. It is separate from the legacy scalar pricing card so
// callers cannot accidentally substitute one valuation basis for another.
type ModelCustomerTariff struct {
	BackendID string
	ModelID   string
	Tariff    economics.TariffSnapshot
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
	retailPolicy, err := ResolveRetailSelectionPolicy(policy)
	if err != nil {
		return Money{}, err
	}
	selected, err := selectRetailBLegsForPolicy(legs, retailPolicy, outcome)
	if err != nil {
		return Money{}, err
	}
	var total int64
	// Fixed and resource components are call-scoped commercial charges. They
	// are applied once after per-B-leg usage, so retry/loser/winner selection
	// cannot multiply a request fee by the number of executed legs.
	legPolicy := policy
	legPolicy.IncludeFixedCharges = false
	legPolicy.IncludeResourceCharges = false
	for _, leg := range selected {
		legPricing, err := customerPricingForLeg(leg, pricing, modelPricing)
		if err != nil {
			return Money{}, err
		}
		strictEvidence := outcome == TurnOutcomeCompleted && leg.Surfaced == SurfacedYes
		amount, err := chargeLeg(leg, legPricing, legPolicy, strictEvidence)
		if err != nil {
			return Money{}, err
		}
		total, err = addNonNegative(total, amount)
		if err != nil {
			return Money{}, err
		}
	}
	if len(selected) != 0 {
		amount, err := chargeScopeComponents(pricing, policy)
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

func chargeScopeComponents(pricing PricingSnapshot, policy ChargePolicy) (int64, error) {
	var total int64
	addComponents := func(components []ChargeComponent) error {
		for _, component := range components {
			amount, err := componentAmount(component, pricing.Currency)
			if err != nil {
				return err
			}
			total, err = addNonNegative(total, amount)
			if err != nil {
				return err
			}
		}
		return nil
	}
	if policy.IncludeFixedCharges {
		if err := addComponents(pricing.FixedCharges); err != nil {
			return 0, err
		}
	}
	if policy.IncludeResourceCharges {
		if err := addComponents(pricing.ResourceCharges); err != nil {
			return 0, err
		}
	}
	return total, nil
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
	retailPolicy, err := ResolveRetailSelectionPolicy(ChargePolicy{Scope: scope})
	if err != nil {
		return nil, err
	}
	return selectRetailBLegsForPolicy(legs, retailPolicy, outcome)
}

// SelectRetailBLegs exposes the narrow default retail selector without
// coupling it to supplier COGS attribution. The default surfaced-turn policy
// selects the surfaced B-leg for a completed call; interrupted calls retain
// the existing one-logical-accepted-leg ordering rule.
func SelectRetailBLegs(legs []CallLegUsageRecord, outcome TurnOutcome) ([]CallLegUsageRecord, error) {
	selected, err := selectCustomerLegs(legs, ChargeSurfacedTurn, outcome)
	if err != nil {
		return nil, err
	}
	return append([]CallLegUsageRecord(nil), selected...), nil
}

// SelectRetailLegs is a descriptive alias for SelectRetailBLegs.
func SelectRetailLegs(legs []CallLegUsageRecord, outcome TurnOutcome) ([]CallLegUsageRecord, error) {
	return SelectRetailBLegs(legs, outcome)
}

func acceptedCustomerLegs(legs []CallLegUsageRecord) []CallLegUsageRecord {
	accepted := make([]CallLegUsageRecord, 0, len(legs))
	for _, leg := range legs {
		if leg.Outcome == LegOutcomeNeverStarted || leg.Outcome == LegOutcomeRejected {
			continue
		}
		if providerAcceptedEvidence(leg.Evidence) || selectableV2Evidence(leg) {
			accepted = append(accepted, leg)
		}
	}
	return accepted
}

// selectableV2Evidence lets the narrow retail selector operate on a record
// whose immutable V2 envelope is present even when the compatibility scalar
// has no accepted V1 quantity. Rating the selected quantities remains the
// responsibility of the later V2 rater; this helper only decides ownership.
func selectableV2Evidence(leg CallLegUsageRecord) bool {
	for _, observation := range leg.Observations {
		if len(observation.Measures) != 0 || len(observation.Charges) != 0 {
			return true
		}
	}
	return false
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
