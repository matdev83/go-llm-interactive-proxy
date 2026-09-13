package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// CostCompleteness describes whether an operator subtotal contains every
// attributable payable item. A partial result is useful for reporting, but it
// is never eligible for a complete payable posting.
type CostCompleteness string

const (
	CostCompletenessKnown   CostCompleteness = "known"
	CostCompletenessPartial CostCompleteness = "partial"
)

// OperatorCOGSResult is the small Phase 5 attribution result. It intentionally
// contains a known subtotal and the identities that kept it incomplete rather
// than converting an unknown attempted leg into zero. Subtotals are keyed by
// native currency; no implicit FX conversion is performed.
type OperatorCOGSResult struct {
	KnownSubtotalByCurrency map[string]Money
	KnownSubtotal           Money
	Completeness            CostCompleteness
	Payable                 bool
	IncludedLegKeys         []string
	UnknownLegKeys          []string
	ExcludedLegKeys         []string
	PendingCoverage         []metering.ChargeCoverageRef
}

// Subtotal returns a caller-owned native-currency subtotal. An absent
// currency is represented by a zero amount with that currency and does not
// imply that the cost was known.
func (r OperatorCOGSResult) Subtotal(currency string) Money {
	currency = strings.TrimSpace(currency)
	if amount, ok := r.KnownSubtotalByCurrency[currency]; ok {
		return amount
	}
	return Money{Currency: currency}
}

// AttributeOperatorCOGS selects all executed operator-payable B-leg costs,
// independently of retail leg selection. It supports legacy V1 provider cost
// evidence and V2 reported charges; a V2 charge set takes precedence for its
// leg so the compatibility projection cannot double count it.
//
// The currency argument is the requested legacy/fallback rate currency. V2
// charges remain visible in their native currencies in
// KnownSubtotalByCurrency. No resource or account-window observation is
// accepted here: those subjects require a separately conserved allocation
// before they can be attributed to a call.
func AttributeOperatorCOGS(legs []CallLegUsageRecord, rates OperatorRateSet, currency string) (OperatorCOGSResult, error) {
	currency = strings.TrimSpace(currency)
	if currency == "" {
		return OperatorCOGSResult{}, fmt.Errorf("%w: provider cost currency is required", ErrRatingCurrencyMismatch)
	}
	result := OperatorCOGSResult{
		KnownSubtotalByCurrency: make(map[string]Money),
		Completeness:            CostCompletenessKnown,
		Payable:                 true,
	}
	sealed := make([]CallLegUsageRecord, 0, len(legs))
	for _, source := range legs {
		leg, err := source.Seal()
		if err != nil {
			return OperatorCOGSResult{}, err
		}
		sealed = append(sealed, leg)
	}

	allObservations := make([]metering.Observation, 0)
	for _, leg := range sealed {
		if leg.Outcome == LegOutcomeNeverStarted || leg.Outcome == LegOutcomeRejected {
			continue
		}
		allObservations = append(allObservations, leg.V2Observations()...)
	}
	if len(allObservations) != 0 {
		// The SDK owns graph semantics. Billing only interprets the validated
		// result for operator attribution and does not duplicate a graph engine.
		if err := metering.ValidateCoverageGraph(allObservations); err != nil {
			return OperatorCOGSResult{Completeness: CostCompletenessPartial, Payable: false}, err
		}
		if err := metering.ValidateSupersessionGraph(allObservations); err != nil {
			return OperatorCOGSResult{Completeness: CostCompletenessPartial, Payable: false}, err
		}
		markPendingSupersessionRefs(&result, allObservations)
		if err := attributeV2Charges(&result, allObservations); err != nil {
			return OperatorCOGSResult{Completeness: CostCompletenessPartial, Payable: false}, err
		}
	}

	for _, leg := range sealed {
		key := leg.Key
		if leg.Outcome == LegOutcomeNeverStarted || leg.Outcome == LegOutcomeRejected {
			result.ExcludedLegKeys = append(result.ExcludedLegKeys, key)
			continue
		}
		if len(leg.EvidenceConflicts) != 0 {
			// A changed source payload is durable diagnostic evidence, not a
			// payable observation. Do not let the compatibility scalar turn
			// an unresolved source conflict into a known COGS amount.
			markCostPartial(&result, key)
			continue
		}
		if len(leg.ObservationRefs) != 0 {
			// A reference is intentionally not treated as a known zero. This
			// narrow owner has no cross-store resolver; until the immutable
			// observation is available the leg remains incomplete/non-payable.
			markCostPartial(&result, key)
		}
		if hasV2Charge(leg) {
			// V2 charges, including an explicit unknown payer, were accounted for
			// in the graph pass. Do not rate the V1 projection again.
			continue
		}
		cost, err := RateProviderCost(leg, rates, currency)
		if err == nil {
			if cost.AmountPresent && cost.Reconciled {
				if err := addOperatorSubtotal(&result, cost.Amount); err != nil {
					return OperatorCOGSResult{}, err
				}
				result.IncludedLegKeys = append(result.IncludedLegKeys, key)
			} else {
				// A resolver may report an incomplete result without returning an
				// error. Preserve that state as an unknown attempted cost rather
				// than treating the absent amount as a known zero.
				markCostPartial(&result, key)
			}
			continue
		}
		if errors.Is(err, ErrUnreconciledCost) {
			markCostPartial(&result, key)
			continue
		}
		return OperatorCOGSResult{}, err
	}

	sort.Strings(result.IncludedLegKeys)
	sort.Strings(result.UnknownLegKeys)
	sort.Strings(result.ExcludedLegKeys)
	sort.Slice(result.PendingCoverage, func(i, j int) bool {
		left, right := result.PendingCoverage[i], result.PendingCoverage[j]
		if left.Ref.StoreID != right.Ref.StoreID {
			return left.Ref.StoreID < right.Ref.StoreID
		}
		if left.Ref.ObservationID != right.Ref.ObservationID {
			return left.Ref.ObservationID < right.Ref.ObservationID
		}
		if left.Ref.Revision != right.Ref.Revision {
			return left.Ref.Revision < right.Ref.Revision
		}
		return left.Ref.ChargeItemID < right.Ref.ChargeItemID
	})
	result.KnownSubtotal = result.Subtotal(currency)
	if result.Completeness == CostCompletenessPartial {
		result.Payable = false
	}
	return result, nil
}

// markPendingSupersessionRefs closes the SDK validator's intentional late-
// evidence allowance for this call-local attribution result. A correction
// that names an observation outside the current immutable batch cannot be
// treated as a settled charge until its prior revision is available; keeping
// the owning B-leg unknown preserves that distinction without inventing a
// zero or resolving across stores.
func markPendingSupersessionRefs(result *OperatorCOGSResult, observations []metering.Observation) {
	if result == nil || len(observations) == 0 {
		return
	}
	type observationKey struct {
		storeID  string
		id       string
		revision uint64
	}
	known := make(map[observationKey]struct{}, len(observations))
	for _, observation := range observations {
		known[observationKey{storeID: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}] = struct{}{}
	}
	for _, observation := range observations {
		for _, ref := range observation.Supersedes {
			if _, ok := known[observationKey{storeID: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}]; ok {
				continue
			}
			identity := observation.Correlation.BillingCallID + ":" + observation.Subject.BLegID
			if strings.TrimSpace(observation.Correlation.BillingCallID) == "" {
				identity = observation.Subject.BLegID
			}
			markCostPartial(result, identity)
		}
	}
}

// AttributeOperatorCost is a descriptive compatibility alias for callers
// that use the existing provider-cost vocabulary.
func AttributeOperatorCost(legs []CallLegUsageRecord, rates OperatorRateSet, currency string) (OperatorCOGSResult, error) {
	return AttributeOperatorCOGS(legs, rates, currency)
}

func hasV2Charge(leg CallLegUsageRecord) bool {
	for _, observation := range leg.Observations {
		if len(observation.Charges) != 0 {
			return true
		}
	}
	return false
}

type operatorChargeKey struct {
	storeID       string
	observationID string
	revision      uint64
	itemID        string
}

func chargeKey(observation metering.Observation, charge metering.ReportedCharge) operatorChargeKey {
	return operatorChargeKey{storeID: observation.Subject.StoreID, observationID: observation.ID, revision: observation.Revision, itemID: charge.ChargeItemID}
}

func attributeV2Charges(result *OperatorCOGSResult, observations []metering.Observation) error {
	// Keep effective charge selection aligned with the Phase 3 reducer. The
	// immutable observations remain on the sealed leg, while a correction or
	// replacement can replace one effective charge item without double-counting
	// its superseded predecessor here. Graph validation remains owned by the
	// SDK immediately before this attribution pass.
	reducedCharges := reduceEffectiveV2Charges(observations)
	known := make(map[operatorChargeKey]reducedV2Charge, len(reducedCharges))
	for _, reduced := range reducedCharges {
		known[chargeKey(reduced.observation, reduced.charge)] = reduced
	}
	inclusiveCovered := make(map[operatorChargeKey]struct{})
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			for _, coverage := range charge.Covers {
				child := operatorChargeKey{
					storeID: coverage.Ref.StoreID, observationID: coverage.Ref.ObservationID,
					revision: coverage.Ref.Revision, itemID: coverage.Ref.ChargeItemID,
				}
				if _, exists := known[child]; !exists {
					appendUniquePendingCoverage(&result.PendingCoverage, coverage)
					continue
				}
				if coverage.Relation == metering.CoverageInclusive {
					inclusiveCovered[child] = struct{}{}
				}
			}
		}
	}
	if len(result.PendingCoverage) != 0 {
		result.Completeness = CostCompletenessPartial
		result.Payable = false
	}
	for _, observation := range observations {
		if observation.Authority == metering.AuthorityUnavailableClaim {
			markObservationPartial(result, observation.ID)
		}
	}
	for key, reduced := range known {
		observation, charge := reduced.observation, reduced.charge
		if _, covered := inclusiveCovered[key]; covered {
			continue
		}
		legKey := observation.Correlation.BillingCallID + ":" + observation.Subject.BLegID
		if strings.TrimSpace(observation.Correlation.BillingCallID) == "" {
			legKey = observation.Subject.BLegID
		}
		switch charge.Payer.Kind {
		case metering.PaymentPartyOperator:
			if charge.Amount == nil {
				markCostPartial(result, legKey)
				continue
			}
			amount, err := charge.Amount.ToNanoUnits()
			if err != nil {
				markCostPartial(result, legKey)
				continue
			}
			if err := addOperatorSubtotal(result, Money{Nano: amount, Currency: charge.Currency}); err != nil {
				return err
			}
			appendUniqueString(&result.IncludedLegKeys, legKey)
		case metering.PaymentPartyCustomer:
			appendUniqueString(&result.ExcludedLegKeys, legKey)
		default:
			// Absent/unknown/unallocated payer is not silently promoted to
			// operator payable. Preserve its B-leg owner as incomplete.
			markCostPartial(result, legKey)
		}
	}
	return nil
}

type reducedV2Charge struct {
	observation metering.Observation
	charge      metering.ReportedCharge
}

// reduceEffectiveV2Charges mirrors only the Phase 3 reducer's effective
// reported-charge rule. It is deliberately not a second graph validator: the
// SDK validators above remain the authority for graph shape and references.
func reduceEffectiveV2Charges(observations []metering.Observation) []reducedV2Charge {
	ordered := append([]metering.Observation(nil), observations...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Sequence != ordered[j].Sequence {
			return ordered[i].Sequence < ordered[j].Sequence
		}
		if ordered[i].Revision != ordered[j].Revision {
			return ordered[i].Revision < ordered[j].Revision
		}
		if ordered[i].SourceEventKey != ordered[j].SourceEventKey {
			return ordered[i].SourceEventKey < ordered[j].SourceEventKey
		}
		return ordered[i].ID < ordered[j].ID
	})
	type entry struct {
		scope       string
		observation metering.Observation
		charge      metering.ReportedCharge
	}
	entries := make(map[string]entry)
	for _, observation := range ordered {
		scope := v2ChargeScopeKey(observation)
		for _, charge := range observation.Charges {
			if (observation.Semantics == metering.SemanticsCorrection || observation.Semantics == metering.SemanticsReplacement) && !v2ChargeIsAdjustment(observation, charge) {
				for key, prior := range entries {
					if prior.scope != scope || prior.charge.ChargeItemID != charge.ChargeItemID {
						continue
					}
					if v2SupersedesObservation(observation.Supersedes, prior.observation) {
						delete(entries, key)
					}
				}
			}
			key := strings.Join([]string{scope, observation.ID, fmt.Sprint(observation.Revision), charge.ChargeItemID}, "\x00")
			entries[key] = entry{scope: scope, observation: observation, charge: charge.Clone()}
		}
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]reducedV2Charge, 0, len(keys))
	for _, key := range keys {
		item := entries[key]
		out = append(out, reducedV2Charge{observation: item.observation, charge: item.charge})
	}
	return out
}

func v2ChargeIsAdjustment(observation metering.Observation, charge metering.ReportedCharge) bool {
	if observation.Semantics != metering.SemanticsCorrection {
		return charge.Kind == metering.ChargeKindAdjustment || charge.Kind == metering.ChargeKindCredit
	}
	if charge.Kind == metering.ChargeKindAdjustment || charge.Kind == metering.ChargeKindCredit || observation.MappingRef == metering.LegacyV1MappingRef {
		return true
	}
	if charge.Amount == nil {
		return false
	}
	value, err := charge.Amount.Normalize()
	return err == nil && strings.HasPrefix(value.Coefficient, "-")
}

func v2SupersedesObservation(refs []metering.ObservationRef, prior metering.Observation) bool {
	for _, ref := range refs {
		if ref.StoreID == prior.Subject.StoreID && ref.ObservationID == prior.ID && ref.Revision == prior.Revision {
			return true
		}
	}
	return false
}

func v2ChargeScopeKey(observation metering.Observation) string {
	account := strings.TrimSpace(observation.Correlation.ProviderAccountKey)
	if account == "" {
		account = strings.TrimSpace(observation.Subject.ProviderAccountKey)
	}
	chargeScope := strings.TrimSpace(observation.Correlation.ProviderChargeID)
	if chargeScope == "" {
		chargeScope = strings.TrimSpace(observation.Subject.ProviderChargeID)
	}
	if chargeScope == "" && len(observation.Charges) == 1 {
		chargeScope = strings.TrimSpace(observation.Charges[0].ChargeItemID)
	}
	subject, _ := json.Marshal(observation.Subject)
	var c canonicalWriter
	for _, value := range []string{
		observation.Correlation.StoreID, observation.Correlation.TenantID, account,
		observation.Origin, observation.Acquisition, string(observation.Perspective),
		string(observation.Boundary), string(observation.Lifecycle), string(subject),
		observation.StreamID, chargeScope,
	} {
		c.string(value)
	}
	return string(c.bytes())
}

func appendUniquePendingCoverage(pending *[]metering.ChargeCoverageRef, coverage metering.ChargeCoverageRef) {
	if pending == nil {
		return
	}
	for _, prior := range *pending {
		if prior.Ref.StoreID == coverage.Ref.StoreID && prior.Ref.ObservationID == coverage.Ref.ObservationID &&
			prior.Ref.Revision == coverage.Ref.Revision && prior.Ref.ChargeItemID == coverage.Ref.ChargeItemID &&
			prior.Relation == coverage.Relation {
			return
		}
	}
	*pending = append(*pending, coverage)
}

func appendUniqueString(values *[]string, value string) {
	if values == nil || value == "" {
		return
	}
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}

func markObservationPartial(result *OperatorCOGSResult, identity string) {
	result.Completeness = CostCompletenessPartial
	result.Payable = false
	if identity != "" {
		markCostPartial(result, identity)
	}
}

func markCostPartial(result *OperatorCOGSResult, identity string) {
	result.Completeness = CostCompletenessPartial
	result.Payable = false
	if identity == "" {
		return
	}
	for _, existing := range result.UnknownLegKeys {
		if existing == identity {
			return
		}
	}
	result.UnknownLegKeys = append(result.UnknownLegKeys, identity)
}

func addOperatorSubtotal(result *OperatorCOGSResult, amount Money) error {
	if err := amount.Validate(); err != nil {
		return err
	}
	prior := result.KnownSubtotalByCurrency[amount.Currency]
	if prior.Currency == "" {
		prior.Currency = amount.Currency
	}
	updated, err := prior.Add(amount)
	if err != nil {
		return err
	}
	result.KnownSubtotalByCurrency[amount.Currency] = updated
	return nil
}
