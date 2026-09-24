package billing

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
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

// OperatorCOGSResult is the operator COGS attribution result. It intentionally
// contains a known subtotal and the identities that kept it incomplete rather
// than converting an unknown attempted leg into zero. Subtotals are keyed by
// native currency; no implicit FX conversion is performed. Optional
// source-preserving allocation lines are kept separate from B-leg identities.
type OperatorCOGSResult struct {
	KnownSubtotalByCurrency map[string]Money
	KnownSubtotal           Money
	Completeness            CostCompleteness
	Payable                 bool
	IncludedLegKeys         []string
	UnknownLegKeys          []string
	ExcludedLegKeys         []string
	PendingCoverage         []metering.ChargeCoverageRef
	// AllocatedCostLines are source-preserving resource/account cost lines.
	// They are deliberately separate from B-leg keys and inference evidence;
	// an allocation target never creates a synthetic leg.
	AllocatedCostLines []AllocatedCostLine
	// PendingAllocations keeps unresolved immutable allocation ancestry visible
	// so a known subtotal is never mistaken for a complete payable result.
	PendingAllocations []economics.AllocationRef
}

var (
	// ErrAllocationTargetNotAttributable means an allocation names a target
	// that is not represented by one of the concrete B-legs/calls supplied to
	// the COGS attribution. Failing closed prevents a synthetic B-leg from
	// being introduced solely to carry a shared-resource amount.
	ErrAllocationTargetNotAttributable = errors.New("billing: allocation target is not an attributable executed call or B-leg")
	// ErrAllocationRequestScoped prevents an already request-scoped provider
	// charge from being added a second time through the non-request allocation
	// seam. Provider-charge costs belong to the B-leg charge selector.
	ErrAllocationRequestScoped = errors.New("billing: request-scoped provider charge cannot be allocated into B-leg COGS")
	// ErrAllocationAmountUnavailable keeps a non-payable allocation from being
	// presented as a payable monetary subtotal when its integer ledger amount
	// is absent.
	ErrAllocationAmountUnavailable = errors.New("billing: allocated monetary amount is unavailable")
)

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
// The currency argument selects the requested V1 authoritative-money currency
// for the legacy compatibility subtotal (Task 18.2, Migration Strategy step
// 8). The retired scalar token-to-money fallback no longer consumes it: V2
// charges remain visible in their native currencies in
// KnownSubtotalByCurrency with no implicit FX. Operator migration: request the
// V1 money currency for draining/history; V2 COGS uses native currencies.
// No resource or account-window observation is
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

// AttributeOperatorCOGSWithAllocations attributes all operator-payable
// provider costs and then adds explicit, conserved monetary allocations from
// non-request resource or statement subjects. The supplied legs are the only
// concrete call/B-leg targets eligible for attribution; allocation lines are
// retained in full (including unallocated remainders), but never become
// inference evidence or entries in IncludedLegKeys.
//
// Allocation records must already be scoped by the caller to the relevant
// reporting set. A target that is neither an unallocated remainder nor a real
// BillingCallID/B-leg in legs fails closed. Pending allocation supersession is
// retained in PendingAllocations and makes the result non-payable while known
// resolved lines remain visible for reporting.
func AttributeOperatorCOGSWithAllocations(legs []CallLegUsageRecord, allocations []economics.AllocationRecord, rates OperatorRateSet, currency string) (OperatorCOGSResult, error) {
	result, err := AttributeOperatorCOGS(legs, rates, currency)
	if err != nil || len(allocations) == 0 {
		return result, err
	}

	rolled, err := RollupAllocatedCostsDetailed(allocations)
	if err != nil {
		return OperatorCOGSResult{}, err
	}
	result.AllocatedCostLines = append([]AllocatedCostLine(nil), rolled.Lines...)
	result.PendingAllocations = append([]economics.AllocationRef(nil), rolled.Pending...)
	for _, pending := range rolled.PendingSupersedes {
		if !containsAllocationRef(result.PendingAllocations, pending) {
			result.PendingAllocations = append(result.PendingAllocations, pending)
		}
	}
	if !rolled.Complete || !rolled.Payable {
		result.Completeness = CostCompletenessPartial
		result.Payable = false
	}

	for _, line := range rolled.Lines {
		if line.Unallocated {
			continue
		}
		if line.SourceSubject.Kind == metering.SubjectProviderCharge {
			return OperatorCOGSResult{}, fmt.Errorf("%w: allocation %s", ErrAllocationRequestScoped, line.AllocationID)
		}
		if line.SourceSubject.Kind != metering.SubjectResource && line.SourceSubject.Kind != metering.SubjectStatementLine {
			return OperatorCOGSResult{}, fmt.Errorf("%w: allocation %s source kind %q", ErrAllocationRequestScoped, line.AllocationID, line.SourceSubject.Kind)
		}
		if !allocationTargetMatchesLegs(line.Target, legs) {
			return OperatorCOGSResult{}, fmt.Errorf("%w: allocation %s target %q", ErrAllocationTargetNotAttributable, line.AllocationID, line.TargetID)
		}
		if line.Informational {
			continue
		}
		if line.SourceAmount == nil {
			// Exact resource quantities remain source-preserving accounting
			// evidence. They cannot become operator money without a separate
			// valuation, so they do not change the COGS subtotal.
			continue
		}
		if line.RoundedAmount == nil || !line.RoundedAmount.Present {
			return OperatorCOGSResult{}, fmt.Errorf("%w: allocation %s", ErrAllocationAmountUnavailable, line.AllocationID)
		}
		if err := addOperatorSubtotal(&result, Money{Nano: line.RoundedAmount.NanoUnits, Currency: line.RoundedAmount.Currency}); err != nil {
			return OperatorCOGSResult{}, err
		}
	}

	result.KnownSubtotal = result.Subtotal(strings.TrimSpace(currency))
	if result.Completeness == CostCompletenessPartial {
		result.Payable = false
	}
	return result, nil
}

func allocationTargetMatchesLegs(target metering.SubjectRef, legs []CallLegUsageRecord) bool {
	switch target.Kind {
	case metering.SubjectBillingCall:
		for _, leg := range legs {
			if target.BillingCallID == leg.CallID.String() &&
				(target.CallID == "" || target.CallID == leg.CallID.String()) &&
				(target.ALegID == "" || target.ALegID == leg.ALegID) &&
				(target.SubmissionID == "" || target.SubmissionID == leg.SubmissionID) &&
				target.RequestID == "" && target.ProviderAccountKey == "" && target.ProviderRequestID == "" {
				return true
			}
		}
		return false
	case metering.SubjectBLeg:
		matches := 0
		for _, leg := range legs {
			if strings.TrimSpace(target.BLegID) != strings.TrimSpace(leg.BLegID) ||
				(target.BillingCallID != "" && target.BillingCallID != leg.CallID.String()) ||
				(target.CallID != "" && target.CallID != leg.CallID.String()) ||
				(target.ALegID != "" && target.ALegID != leg.ALegID) ||
				(target.SubmissionID != "" && target.SubmissionID != leg.SubmissionID) ||
				(target.AttemptSeq != 0 && (leg.AttemptSeq <= 0 || target.AttemptSeq != uint64(leg.AttemptSeq))) {
				continue
			}
			if target.RequestID != "" || target.AttemptID != "" || target.ProviderAccountKey != "" || target.ProviderRequestID != "" || target.ProviderChargeID != "" {
				continue
			}
			matches++
		}
		return matches == 1
	default:
		return false
	}
}

func containsAllocationRef(refs []economics.AllocationRef, wanted economics.AllocationRef) bool {
	return slices.Contains(refs, wanted)
}

func hasV2Charge(leg CallLegUsageRecord) bool {
	for _, observation := range leg.Observations {
		if len(observation.Charges) != 0 {
			return true
		}
	}
	return false
}

func markSnapshotIncomplete(result *OperatorCOGSResult, snapshot aggregate.SnapshotV2) {
	for _, observation := range snapshot.Observations {
		for _, unavailableID := range snapshot.Unavailable {
			if observation.ID == unavailableID {
				markObservationPartial(result, observationLegKey(observation))
			}
		}
	}
	for _, reduced := range snapshot.Charges {
		if reduced.Complete {
			continue
		}
		for _, observation := range snapshot.Observations {
			if observation.ID == reduced.ObservationID && observation.Revision == reduced.Revision {
				markObservationPartial(result, observationLegKey(observation))
				break
			}
		}
	}
	for _, predecessor := range snapshot.UnusablePredecessors {
		for _, observation := range snapshot.Observations {
			for _, ref := range observation.Supersedes {
				if ref == predecessor {
					markObservationPartial(result, observationLegKey(observation))
				}
			}
		}
	}
	for _, pending := range snapshot.PendingSupersedes {
		for _, observation := range snapshot.Observations {
			for _, ref := range observation.Supersedes {
				if ref != pending {
					continue
				}
				markObservationPartial(result, observationLegKey(observation))
				break
			}
		}
	}
	for _, pending := range snapshot.PendingCoverage {
		for _, observation := range snapshot.Observations {
			for _, charge := range observation.Charges {
				for _, coverage := range charge.Covers {
					if coverage.Ref == pending.Ref && coverage.Relation == pending.Relation {
						markObservationPartial(result, observationLegKey(observation))
					}
				}
			}
		}
	}
}

func observationLegKey(observation metering.Observation) string {
	callID := strings.TrimSpace(observation.Correlation.BillingCallID)
	if callID == "" {
		callID = strings.TrimSpace(observation.Subject.BillingCallID)
	}
	if callID == "" {
		callID = strings.TrimSpace(observation.Correlation.CallID)
	}
	if callID == "" {
		callID = strings.TrimSpace(observation.Subject.CallID)
	}
	bLegID := strings.TrimSpace(observation.Correlation.BLegID)
	if bLegID == "" {
		bLegID = strings.TrimSpace(observation.Subject.BLegID)
	}
	if callID == "" {
		return bLegID
	}
	if bLegID == "" {
		return callID
	}
	return callID + ":" + bLegID
}

func attributeV2Charges(result *OperatorCOGSResult, observations []metering.Observation) error {
	// The reducer first derives the effective revision state and its coverage
	// diagnostics. Build the strict coverage graph only from that projected
	// state so superseded historical edges remain audit-only.
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		return err
	}
	for _, coverage := range snapshot.PendingCoverage {
		result.PendingCoverage = appendUniquePendingCoverage(result.PendingCoverage, coverage)
	}
	markSnapshotIncomplete(result, snapshot)

	effective := aggregate.EffectiveChargeObservationsForCOGS(snapshot)
	if len(effective) == 0 {
		return nil
	}
	nodes, _, err := validateChargeCoverageGraph(effective)
	if err != nil {
		// Preserve both the billing classification and the canonical metering
		// sentinel for callers that consume this COGS boundary directly.
		return fmt.Errorf("%w: %w: %v", metering.ErrInvalidCoverage, err, err)
	}
	inclusiveCovered := make(map[string]struct{})
	for _, node := range nodes {
		for _, edge := range node.charge.Covers {
			if edge.Relation == metering.CoverageInclusive {
				inclusiveCovered[chargeRefKey(edge.Ref)] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(nodes))
	for key := range nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, covered := inclusiveCovered[key]; covered {
			continue
		}
		node := nodes[key]
		legKey := observationLegKey(node.observation)
		switch node.charge.Payer.Kind {
		case metering.PaymentPartyOperator:
			if node.charge.Amount == nil {
				markCostPartial(result, legKey)
				continue
			}
			amount, err := node.charge.Amount.ToNanoUnits()
			if err != nil {
				markCostPartial(result, legKey)
				continue
			}
			if err := addOperatorSubtotal(result, Money{Nano: amount, Currency: node.charge.Currency}); err != nil {
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

func appendUniquePendingCoverage(pending []metering.ChargeCoverageRef, coverage metering.ChargeCoverageRef) []metering.ChargeCoverageRef {
	for _, prior := range pending {
		if prior.Ref == coverage.Ref && prior.Relation == coverage.Relation {
			return pending
		}
	}
	return append(pending, coverage)
}

func appendUniqueString(values *[]string, value string) {
	if values == nil || value == "" {
		return
	}
	if slices.Contains(*values, value) {
		return
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
	if slices.Contains(result.UnknownLegKeys, identity) {
		return
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
