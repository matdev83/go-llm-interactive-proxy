package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrRatingInvalid                    = errors.New("billing: invalid rating input")
	ErrRatingSnapshotMismatch           = errors.New("billing: rating snapshot identity mismatch")
	ErrRatingCurrencyMismatch           = errors.New("billing: rating currency mismatch")
	ErrRatingEvidenceMissing            = errors.New("billing: required rating evidence is missing")
	ErrActualChargeExceedsAuthorization = errors.New("billing: actual customer charge exceeds authorization")
	ErrUnreconciledCost                 = errors.New("billing: provider cost is unreconciled")
)

// CustomerPricingSnapshot is the immutable customer pricing value used by both
// conservative admission and post-turn rating.
type CustomerPricingSnapshot = PricingSnapshot

// OperatorRateSnapshot is the exact immutable fallback rate bound to a LUR.
// Operator rates are intentionally separate from customer pricing: provider
// cost and customer revenue are different economic perspectives.
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

// OperatorRateSet is a small immutable-at-call-boundary collection of exact
// operator snapshots. Lookup compares the complete VersionRef, not numeric rate
// values, so equal numbers from another economic version cannot be substituted.
type OperatorRateSet []OperatorRateSnapshot

func (s OperatorRateSet) Resolve(ref VersionRef) (OperatorRateSnapshot, bool) {
	for _, candidate := range s {
		if candidate.Ref == ref {
			return candidate, true
		}
	}
	return OperatorRateSnapshot{}, false
}

// RatingInput contains only sealed values and immutable snapshots. It has no
// runtime, provider, database, or wire dependencies.
type RatingInput struct {
	Record          TurnUsageRecord
	Authorization   Authorization
	CustomerPricing CustomerPricingSnapshot
	// ModelPricing supplies per-backend/model customer cards that share
	// CustomerPricing.Ref. Empty means every billed leg uses CustomerPricing.
	ModelPricing   []ModelCustomerPricing
	CustomerPolicy ChargePolicy
	OperatorRates  OperatorRateSet
}

// ModelCustomerPricing is the immutable customer rate card for one billed
// backend:model. Catalog identity stays on CustomerPricing.Ref / TUR / hold.
type ModelCustomerPricing struct {
	BackendID string
	ModelID   string
	Pricing   PricingSnapshot
}

// OperatorCostResult preserves every LUR's cost outcome. An unreconciled result
// is visible but deliberately has Reconciled=false and must not be posted as a
// zero-cost COGS entry by later settlement.
type OperatorCostResult struct {
	LURKey             string
	Amount             Money
	AmountPresent      bool
	Reconciled         bool
	Authoritative      bool
	UnreconciledReason string
}

// BillingResult keeps customer revenue separate from per-B-leg operator cost.
// UnreconciledLURKeys are explicit processing blockers, never omitted costs.
type BillingResult struct {
	TURKey              string
	CustomerCharge      Money
	OperatorCosts       []OperatorCostResult
	UnreconciledCost    bool
	UnreconciledLURKeys []string
}

// RatingResult and OperatorCost are compatibility aliases for callers that use
// the shorter names in the design document.
type (
	RatingResult = BillingResult
	OperatorCost = OperatorCostResult
)

// ProcessingMarker is the narrow mutable-state seam used when a rating result
// cannot safely proceed to settlement. Implementations must update processing
// metadata only; sealed TUR/LUR evidence is never rewritten.
type ProcessingMarker interface {
	MarkProcessingUnreconciledCost(context.Context, string, string, string) error
}

// RateTurn deterministically rates one sealed TUR. It accepts an unsealed value
// as a convenience for pure callers and seals a detached copy before use; a
// supplied sealed record is always validated against its stored key/fingerprint.
func RateTurn(in RatingInput) (BillingResult, error) {
	record, err := sealForRating(in.Record)
	if err != nil {
		return BillingResult{}, err
	}
	if err := validateRatingSnapshots(record, in); err != nil {
		return BillingResult{}, err
	}
	customer, err := calculateCustomerCharge(record, in)
	if err != nil {
		return BillingResult{}, err
	}
	if customer.Currency != in.Authorization.Amount.Currency {
		return BillingResult{}, ErrRatingCurrencyMismatch
	}
	if customer.Nano > in.Authorization.Amount.Nano {
		return BillingResult{}, fmt.Errorf("%w: actual=%d authorized=%d", ErrActualChargeExceedsAuthorization, customer.Nano, in.Authorization.Amount.Nano)
	}
	result := BillingResult{TURKey: record.Key, CustomerCharge: customer}
	for _, leg := range record.Legs {
		if authoritativeProviderCost(leg.Evidence) && leg.Evidence.Cost.Currency != in.CustomerPricing.Currency {
			return BillingResult{}, ErrRatingCurrencyMismatch
		}
	}
	result.OperatorCosts = calculateOperatorCosts(record, in.OperatorRates, in.CustomerPricing.Currency, &result.UnreconciledLURKeys)
	result.UnreconciledCost = len(result.UnreconciledLURKeys) > 0
	return result, nil
}

// CalculateBilling is the descriptive alias used by post-turn callers.
func CalculateBilling(in RatingInput) (BillingResult, error) { return RateTurn(in) }

func sealForRating(record TurnUsageRecord) (TurnUsageRecord, error) {
	if strings.TrimSpace(record.Key) == "" {
		return record.Seal()
	}
	if err := CheckReplay(record, record); err != nil {
		return TurnUsageRecord{}, err
	}
	return record, nil
}

func validateRatingSnapshots(record TurnUsageRecord, in RatingInput) error {
	if in.Authorization.ID == "" || in.Authorization.ID != record.AuthorizationID || in.Authorization.AccountID == "" || in.Authorization.AccountID != record.AccountID || in.Authorization.TURKey != record.Key {
		return fmt.Errorf("%w: authorization is not bound to TUR", ErrRatingSnapshotMismatch)
	}
	if err := in.CustomerPolicy.Validate(); err != nil {
		return fmt.Errorf("%w: customer policy: %v", ErrRatingSnapshotMismatch, err)
	}
	if in.CustomerPricing.Ref != record.CustomerPricingRef || in.CustomerPricing.Ref != in.Authorization.PricingRef {
		return fmt.Errorf("%w: customer pricing reference differs from TUR/authorization", ErrRatingSnapshotMismatch)
	}
	if in.CustomerPolicy.Ref != record.ChargePolicyRef || in.CustomerPolicy.Ref != in.Authorization.ChargePolicyRef || in.CustomerPolicy.PricingRef != in.CustomerPricing.Ref {
		return fmt.Errorf("%w: customer policy reference differs from TUR/authorization/pricing", ErrRatingSnapshotMismatch)
	}
	if err := in.CustomerPricing.Validate(in.Authorization.Amount.Currency); err != nil {
		return fmt.Errorf("%w: customer pricing: %v", ErrRatingSnapshotMismatch, err)
	}
	if in.CustomerPricing.Currency != in.Authorization.Amount.Currency {
		return ErrRatingCurrencyMismatch
	}
	for _, card := range in.ModelPricing {
		if strings.TrimSpace(card.BackendID) == "" || strings.TrimSpace(card.ModelID) == "" {
			return fmt.Errorf("%w: model pricing identity is required", ErrRatingInvalid)
		}
		if card.Pricing.Ref != in.CustomerPricing.Ref {
			return fmt.Errorf("%w: model pricing %s/%s reference differs from customer catalog", ErrRatingSnapshotMismatch, card.BackendID, card.ModelID)
		}
		if err := card.Pricing.Validate(in.Authorization.Amount.Currency); err != nil {
			return fmt.Errorf("%w: model pricing %s/%s: %v", ErrRatingSnapshotMismatch, card.BackendID, card.ModelID, err)
		}
	}
	if in.Authorization.Amount.Nano < 0 {
		return fmt.Errorf("%w: authorization amount cannot be negative", ErrRatingInvalid)
	}
	return nil
}

func calculateCustomerCharge(record TurnUsageRecord, in RatingInput) (Money, error) {
	// OpenRouter-style cost recovery: turn outcome never grants a free ride.
	// Customer charge follows observed provider-accepted usage under policy.
	selected := selectCustomerLegs(record.Legs, in.CustomerPolicy.Scope, record.Outcome)
	var total int64
	for _, leg := range selected {
		pricing, err := in.customerPricingForLeg(leg)
		if err != nil {
			return Money{}, err
		}
		// Surfaced legs on a completed TUR still fail closed on missing required
		// quantities. Failed/unsurfaced accepted siblings skip missing optional
		// dimensions (Req 12.13 at leg granularity) instead of failing the A-leg.
		strictEvidence := record.Outcome == TurnOutcomeCompleted && leg.Surfaced == SurfacedYes
		amount, err := chargeLeg(leg, pricing, in.CustomerPolicy, strictEvidence)
		if err != nil {
			return Money{}, err
		}
		total, err = addNonNegative(total, amount)
		if err != nil {
			return Money{}, err
		}
	}
	return Money{Nano: total, Currency: in.CustomerPricing.Currency}, nil
}

func (in RatingInput) customerPricingForLeg(leg LegUsageRecord) (PricingSnapshot, error) {
	if len(in.ModelPricing) == 0 {
		return in.CustomerPricing, nil
	}
	for _, card := range in.ModelPricing {
		if card.BackendID == leg.BackendID && card.ModelID == leg.ModelID {
			return card.Pricing, nil
		}
	}
	return PricingSnapshot{}, fmt.Errorf("%w: customer pricing for %s/%s", ErrRatingEvidenceMissing, leg.BackendID, leg.ModelID)
}

func selectCustomerLegs(legs []LegUsageRecord, scope ChargePolicyScope, outcome TurnOutcome) []LegUsageRecord {
	accepted := acceptedCustomerLegs(legs)
	if scope == ChargeAllPotentialLegs {
		// Pass-through charges every provider-accepted leg. Never-started /
		// rejected siblings stay $0 rather than failing the winning TUR.
		return accepted
	}
	if outcome != TurnOutcomeCompleted {
		// ChargeSurfacedTurn admission holds max(routes). Interrupt settlement is
		// one logical turn: surfaced accepted legs if any, else the latest Seq.
		return oneLogicalAcceptedTurn(accepted)
	}
	selected := make([]LegUsageRecord, 0, len(legs))
	for _, leg := range legs {
		if leg.Surfaced == SurfacedYes {
			selected = append(selected, leg)
		}
	}
	return selected
}

func acceptedCustomerLegs(legs []LegUsageRecord) []LegUsageRecord {
	accepted := make([]LegUsageRecord, 0, len(legs))
	for _, leg := range legs {
		if providerAcceptedEvidence(leg.Evidence) {
			accepted = append(accepted, leg)
		}
	}
	return accepted
}

func oneLogicalAcceptedTurn(accepted []LegUsageRecord) []LegUsageRecord {
	if len(accepted) == 0 {
		return accepted
	}
	surfaced := make([]LegUsageRecord, 0, 1)
	for _, leg := range accepted {
		if leg.Surfaced == SurfacedYes {
			surfaced = append(surfaced, leg)
		}
	}
	if len(surfaced) > 0 {
		return surfaced
	}
	best := accepted[0]
	for _, leg := range accepted[1:] {
		if leg.Seq > best.Seq {
			best = leg
		}
	}
	return []LegUsageRecord{best}
}

// providerAcceptedEvidence is true when the downstream provider accepted work for
// this leg. Absent all quantities and authoritative cost means rejection / never
// started — those legs stay $0 for the customer.
func providerAcceptedEvidence(e FinalBillingEvidence) bool {
	if authoritativeProviderCost(e) {
		return true
	}
	return e.InputTokens.Present || e.OutputTokens.Present || e.CacheReadTokens.Present ||
		e.CacheWriteTokens.Present || e.ReasoningTokens.Present || e.TotalTokens.Present
}

func chargeLeg(leg LegUsageRecord, pricing PricingSnapshot, policy ChargePolicy, strictEvidence bool) (int64, error) {
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
				return fmt.Errorf("%w: %s tokens for LUR %q", ErrRatingEvidenceMissing, dim, leg.BLegID)
			}
			// Interrupted turns skip missing quantities, not missing bound rates.
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

func calculateOperatorCosts(record TurnUsageRecord, rates OperatorRateSet, currency string, unresolved *[]string) []OperatorCostResult {
	out := make([]OperatorCostResult, 0, len(record.Legs))
	for _, leg := range record.Legs {
		if authoritativeProviderCost(leg.Evidence) {
			out = append(out, OperatorCostResult{LURKey: leg.Key, Amount: Money{Nano: leg.Evidence.Cost.NanoUnits, Currency: currency}, AmountPresent: true, Reconciled: true, Authoritative: true})
			continue
		}
		if !providerAcceptedEvidence(leg.Evidence) {
			// Rejected / never-started legs have no billable work. Recording
			// reconciled $0 is not a silent COGS cover-up; unreconciled_cost
			// would strand the whole TUR including any winning B-leg.
			out = append(out, OperatorCostResult{LURKey: leg.Key, Amount: Money{Currency: currency}, AmountPresent: true, Reconciled: true})
			continue
		}
		rate, found := rates.Resolve(leg.OperatorRateRef)
		amount, reason, ok := fallbackOperatorCost(leg, rate, found, currency)
		if !ok {
			out = append(out, OperatorCostResult{LURKey: leg.Key, Amount: Money{Currency: currency}, UnreconciledReason: reason})
			*unresolved = append(*unresolved, leg.Key)
			continue
		}
		out = append(out, OperatorCostResult{LURKey: leg.Key, Amount: Money{Nano: amount, Currency: currency}, AmountPresent: true, Reconciled: true})
	}
	return out
}

// authoritativeProviderCost is the only sealed monetary signal that may become
// posted COGS without operator-rate fallback. Cost.Present alone is insufficient:
// estimated/unknown presence must not silently substitute for authoritative cost.
func authoritativeProviderCost(e FinalBillingEvidence) bool {
	return e.Cost.Present && e.Authority == EvidenceAuthorityAuthoritative
}

func fallbackOperatorCost(leg LegUsageRecord, rate OperatorRateSnapshot, found bool, currency string) (int64, string, bool) {
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
			// Rate cards commonly publish optional dimensions; absent quantity is zero.
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

// MarkUnreconciledCost moves only mutable processing metadata to the explicit
// unreconciled state. It refuses to mark a fully reconciled result this way.
func MarkUnreconciledCost(ctx context.Context, marker ProcessingMarker, record TurnUsageRecord, result BillingResult) error {
	if marker == nil || !result.UnreconciledCost {
		return fmt.Errorf("%w: unreconciled result and processing marker are required", ErrUnreconciledCost)
	}
	sealed, err := sealForRating(record)
	if err != nil {
		return err
	}
	if result.TURKey != sealed.Key {
		return fmt.Errorf("%w: result TUR does not match processing record", ErrRatingInvalid)
	}
	return marker.MarkProcessingUnreconciledCost(ctx, sealed.Key, sealed.Fingerprint, "cost_unresolved")
}

// RateTurnAndMarkProcessing is the post-turn boundary for the Phase 5
// unreconciled-cost path. It rates first, then changes only mutable processing
// metadata when operator cost cannot be proven. Settlement remains Phase 6.
func RateTurnAndMarkProcessing(ctx context.Context, marker ProcessingMarker, in RatingInput) (BillingResult, error) {
	result, err := RateTurn(in)
	if err != nil {
		return BillingResult{}, err
	}
	if result.UnreconciledCost {
		if err := MarkUnreconciledCost(ctx, marker, in.Record, result); err != nil {
			return BillingResult{}, err
		}
	}
	return result, nil
}
