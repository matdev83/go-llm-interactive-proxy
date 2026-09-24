package billing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// projectCostCoverage derives the bounded full-scope projection of attributable
// executed in-scope cost subjects from authoritative observations (the executed
// B-leg/provider-charge evidence) and classifies each against the singular
// authoritative selected result.
//
// The projection deliberately discovers missing coverage from authoritative
// executed subjects and charge identities, not from already-existing valuations:
// an entire unvalued B-leg or an observation-only charge therefore surfaces as
// an unresolved cost subject instead of vanishing.
//
// Monetary coverage is proven at the exact canonical charge/revision identity
// level. A selected B-leg covers its base execution only. Every independently
// payable charge atom on that B-leg, including a distinct request charge or
// provider-charge event, stays unresolved until the selected result proves it
// through an exact inclusive coverage reference or a validated inclusive edge
// from another covered charge. An inclusive edge is followed only from a
// covered charge to its exact target charge identity, never by promoting a
// whole subject or by summing amounts. A head/valuation input observation, an
// unselected candidate source, a B-leg shared ownership relation, and the mere
// presence of a charge on a selected subject are all participation facts, not
// proof of inclusion.
//
// executionLegs carries the bounded authoritative execution facts the durable
// reader loaded from the same in-scope leg records. A leg with usable
// observation evidence is governed by that evidence; an executed/attempted leg
// with none becomes an explicit execution subject that only canonical
// never-started/nonbillable/known-zero proof (or proven selected coverage)
// exempts. Missing evidence is never turned into zero.
//
// hasExplicit reports whether selection is the caller-supplied full result.
// Coverage seeds only the exact authoritative selected result used for the
// monetary amount: an explicit selection binds its winning candidate
// valuation, a persisted single-head selection binds only that head, and
// ambiguous or missing selections bind nothing. Independent retained heads
// never seed coverage and are never summed.
//
// allocations carries the authoritative effective in-scope allocation lines
// already restricted to the requested scope by the durable reader (or by the
// assembly scope check). Each active attributable monetary allocation is a cost
// unit of its own: it is never inferred included from its target B-leg, its
// complete/payable lineage state or a matching currency. It is proven included
// only when the exact selected valuation names its immutable allocation
// revision and payload hash. Explicit zero is a canonical exemption; a
// redacted, currency-incompatible or unproven positive allocation stays
// unresolved. Allocation amounts are never summed into the selected amount.
func projectCostCoverage(observations []metering.Observation, valuations []economics.Valuation, selectedValuations []economics.Valuation, heads []SelectedCostHead, selection *OperatorCostSelectionResult, hasExplicit bool, allocations []AllocatedCostLine, executionLegs []EconomicDetailExecutionLeg) EconomicDetailCostCoverage {
	units := make(map[string]*costCoverageUnit)
	var unitOrder []string
	atomByKey := make(map[string]*costCoverageChargeAtom)

	// Reduce the in-scope observations through the authoritative supersession
	// reducer and validate the closed effective coverage graph before any Covers
	// edge may act as a completeness proof. An invalid, cyclic, conflicting,
	// unavailable-target or unresolved-supersession graph fails closed: no charge
	// atom is proven included and the projection stays incomplete.
	graphObservations, graphValid := canonicalCoverageGraph(observations)

	// originalPayloadHash pins the immutable payload identity of every original
	// in-scope observation envelope, before supersession reduction. A selected
	// line source reference is only honored when it names the exact retained
	// payload; hashing a reduced graph projection (which intentionally removed
	// superseded items) would let a stale reference masquerade as current.
	originalPayloadHash := make(map[string]string, len(observations))
	for i := range observations {
		observation := observations[i]
		hash, err := observation.ReplayFingerprint()
		if err != nil {
			continue
		}
		originalPayloadHash[costCoverageObservationRevisionKey(observation.Subject.StoreID, observation.ID, observation.Revision)] = hash
	}
	// verifySourceRef proves one selected line source reference against the
	// original persisted/materialized observation payload. A missing observation
	// or an absent/mismatched payload hash fails closed: participation is never
	// inclusion.
	verifySourceRef := func(ref metering.ObservationRef) bool {
		if ref.ObservationID == "" || ref.Revision == 0 || ref.PayloadHash == "" {
			return false
		}
		hash, ok := originalPayloadHash[costCoverageObservationRevisionKey(ref.StoreID, ref.ObservationID, ref.Revision)]
		return ok && hash == ref.PayloadHash
	}

	// allocationLineByKey resolves the exact immutable identity of every active
	// attributable monetary allocation line supplied to this projection. A
	// selected allocation-coverage reference may only prove one of these lines,
	// and only when it names the exact payload hash. Unallocated remainders,
	// informational lines, redacted lines and non-monetary quantity allocations
	// are deliberately absent: they are not live monetary costs.
	allocationLineByKey := make(map[string]AllocatedCostLine, len(allocations))
	for i := range allocations {
		line := allocations[i]
		if line.Unallocated || line.Informational || line.Redacted || line.SourceAmount == nil {
			continue
		}
		key := costCoverageAllocationIdentity(line.SourceSubject.StoreID, line.AllocationID, line.AllocationVersion)
		if key == "" {
			continue
		}
		allocationLineByKey[key] = line
	}
	// includedAllocationTargets is the explicit selected-result inclusion proof:
	// allocation identity plus the B-leg/call ownership the frozen selected
	// valuation covers. Nothing else seeds it.
	includedAllocationTargets := make(map[string]struct{})

	// unitForKey is the single attribution owner: measures and effective charges
	// of the same subject/correlation always fold into one unit.
	unitForKey := func(subject metering.SubjectRef, correlation metering.CorrelationV2) *costCoverageUnit {
		unitKey, ok := costCoverageUnitKey(subject, correlation)
		if !ok {
			return nil
		}
		unit, exists := units[unitKey]
		if !exists {
			unit = &costCoverageUnit{key: unitKey, subject: subject.Clone(), correlation: correlation, allNonCustomerZero: true}
			units[unitKey] = unit
			unitOrder = append(unitOrder, unitKey)
		}
		return unit
	}

	// Base execution and payer attribution follow the full in-scope evidence so
	// an executed-but-unpriced subject never vanishes. Charge atoms follow the
	// canonical effective graph when it is valid; when it is not, raw charge
	// attribution is retained so every attributable charge still surfaces as an
	// unresolved subject instead of disappearing behind a forced-complete margin.
	for i := range observations {
		unit := unitForKey(observations[i].Subject, observations[i].Correlation)
		if unit == nil {
			continue
		}
		for _, measure := range observations[i].Measures {
			if measure.Value != nil {
				unit.hasMeasures = true
			}
		}
	}

	chargeObservations := observations
	if graphValid {
		chargeObservations = graphObservations
	}
	for i := range chargeObservations {
		observation := chargeObservations[i]
		unit := unitForKey(observation.Subject, observation.Correlation)
		if unit == nil {
			continue
		}
		authoritativeMoney := authoritativeProviderChargeObservation(observation)
		for chargeIndex, charge := range observation.Charges {
			unit.hasCharge = true
			unit.chargeItems = append(unit.chargeItems, charge.ChargeItemID)
			atomKey := costCoverageChargeLookupKey(observation.Subject.StoreID, observation.ID, observation.Revision, charge.ChargeItemID)
			unit.chargeKeys = append(unit.chargeKeys, atomKey)
			if graphValid {
				atomByKey[atomKey] = &costCoverageChargeAtom{observation: observation, chargeIndex: chargeIndex}
			}
			switch charge.Payer.Kind {
			case metering.PaymentPartyCustomer:
				unit.hasCustomerCharge = true
			default:
				unit.hasNonCustomerCharge = true
				if charge.Amount == nil {
					unit.allNonCustomerZero = false
					continue
				}
				value, err := charge.Amount.ToRat()
				// Only a canonical authoritative provider zero proves no
				// exposure. A local or estimated exact zero is an advisory
				// claim and must stay unresolved rather than close
				// completeness.
				if err != nil || value.Sign() != 0 || !authoritativeMoney {
					unit.allNonCustomerZero = false
				}
			}
		}
	}

	// baseCovered records a unit whose base execution is proven covered by the
	// selected result/head. atomCovered records exact charge identities proven
	// included. They are deliberately distinct: a selected B-leg proves its base
	// execution, not its independent charge children.
	baseCovered := make(map[string]EconomicDetailCostCoverageState)
	atomCovered := make(map[string]EconomicDetailCostCoverageState)
	var queue []string
	markAtom := func(atomKey string, state EconomicDetailCostCoverageState) {
		if atomByKey[atomKey] == nil {
			return
		}
		if existing, ok := atomCovered[atomKey]; ok {
			// A direct selected proof outranks an inherited inclusive state; an
			// inclusive proof is never downgraded. A promoted atom is already on
			// (or past) the worklist, so its inclusive edges need no re-queue.
			if existing != EconomicDetailCostCoverageInclusive || state != EconomicDetailCostCoverageSelected {
				return
			}
			atomCovered[atomKey] = EconomicDetailCostCoverageSelected
			return
		}
		atomCovered[atomKey] = state
		queue = append(queue, atomKey)
	}
	seedUnit := func(unitKey string, state EconomicDetailCostCoverageState) {
		if _, ok := units[unitKey]; !ok {
			return
		}
		if existing, ok := baseCovered[unitKey]; !ok || (existing != EconomicDetailCostCoverageSelected && state == EconomicDetailCostCoverageSelected) {
			baseCovered[unitKey] = state
		}
	}

	// matchKeyByUnit relates each B-leg observation unit to the concrete B-leg
	// identity an execution fact carries (tenant- and attempt-independent), so a
	// leg whose observations are absent/empty/unusable is never double-counted
	// and a covered leg is never reported unresolved.
	matchKeyByUnit := make(map[string]string, len(units))
	for _, unitKey := range unitOrder {
		unit := units[unitKey]
		if unit.subject.Kind != metering.SubjectBLeg {
			continue
		}
		if matchKey, ok := costCoverageBLegMatchKey(unit.subject, unit.correlation); ok {
			matchKeyByUnit[unitKey] = matchKey
		}
	}
	coveredMatch := make(map[string]EconomicDetailCostCoverageState)
	markCoveredMatch := func(matchKey string, state EconomicDetailCostCoverageState) {
		if matchKey == "" {
			return
		}
		if existing, ok := coveredMatch[matchKey]; ok && existing == EconomicDetailCostCoverageSelected {
			return
		}
		coveredMatch[matchKey] = state
	}
	seedSubject := func(subject metering.SubjectRef, correlation metering.CorrelationV2) {
		if unitKey, ok := costCoverageUnitKey(subject, correlation); ok {
			seedUnit(unitKey, EconomicDetailCostCoverageSelected)
		}
		// Only a B-leg subject proves the base execution identity; a selected
		// provider charge is not proof that the owning B-leg base was priced.
		if subject.Kind == metering.SubjectBLeg {
			if matchKey, ok := costCoverageBLegMatchKey(subject, correlation); ok {
				markCoveredMatch(matchKey, EconomicDetailCostCoverageSelected)
			}
		}
	}
	seedChargeRef := func(ref metering.ChargeRef) {
		markAtom(costCoverageChargeLookupKey(ref.StoreID, ref.ObservationID, ref.Revision, ref.ChargeItemID), EconomicDetailCostCoverageInclusive)
	}
	// resolveValuation resolves the exact frozen valuation named by a selected
	// identity. The bounded SelectedValuations set carries the authoritative
	// exact selected revisions (which latest-per-stream display may not contain);
	// the display Valuations set remains a fallback for callers that supply the
	// selected valuation inline. Identity is never a subject-equality shortcut.
	resolveValuation := func(valuationID string) (economics.Valuation, bool) {
		if valuation, ok := costCoverageValuationByID(selectedValuations, valuationID); ok {
			return valuation, true
		}
		return costCoverageValuationByID(valuations, valuationID)
	}

	// seedValuationCoverage seeds the explicit inclusive coverage contract of one
	// resolved valuation. Base execution is seeded from the valuation subject,
	// but its charge atoms are proven only through exact line source references
	// whose immutable payload identity matches the original observation envelope
	// and through the valuation's own validated inclusive edges.
	seedValuationCoverage := func(valuation economics.Valuation) {
		seedSubject(valuation.Subject, metering.CorrelationV2{StoreID: valuation.Subject.StoreID})
		// A payable selected line names one exact charge atom through its
		// item identity and source observation reference. Only a rated or
		// provider-reported line proves monetary inclusion; unresolved or
		// incomplete lines stay participating evidence.
		for _, line := range valuation.Lines {
			if line.ItemID == "" || !costCoverageLineProvesInclusion(line.Status) {
				continue
			}
			for _, ref := range line.SourceObservationRefs {
				if !verifySourceRef(ref) {
					continue
				}
				markAtom(costCoverageChargeLookupKey(ref.StoreID, ref.ObservationID, ref.Revision, line.ItemID), EconomicDetailCostCoverageSelected)
			}
		}
		for _, edge := range valuation.CoverageRefs {
			if edge.Relation == metering.CoverageInclusive {
				seedChargeRef(edge.Ref)
			}
		}
		// Allocation inclusion is proven only by an exact immutable allocation
		// reference on this frozen valuation. The referenced line must be
		// present, carry the same payload hash, and target the same B-leg/call
		// ownership as the valuation subject; an absent, redacted or
		// differently-targeted line stays unproven. No amount is summed.
		if len(valuation.AllocationCoverageRefs) != 0 {
			targetKeys := costCoverageSubjectAllocationTargetKeys(valuation.Subject, metering.CorrelationV2{StoreID: valuation.Subject.StoreID})
			for _, ref := range valuation.AllocationCoverageRefs {
				identity := costCoverageAllocationIdentity(ref.StoreID, ref.AllocationID, ref.Version)
				if identity == "" {
					continue
				}
				line, ok := allocationLineByKey[identity]
				if !ok || line.PayloadHash == "" || line.PayloadHash != ref.PayloadHash {
					continue
				}
				for _, targetKey := range targetKeys {
					includedAllocationTargets[identity+"\x00"+targetKey] = struct{}{}
				}
			}
		}
	}

	// bindSelectedValuation resolves the frozen valuation named by the selected
	// identity and seeds its explicit inclusive coverage contract. The selected
	// reference must carry its authoritative input-set identity and the resolved
	// valuation must carry the exact same one; equality is required, not
	// optional, so a valuation with an absent input hash can never prove a
	// nonempty frozen selected reference.
	bindSelectedValuation := func(ref SelectedCostValuationRef) {
		if ref.InputSetHash == "" {
			return
		}
		valuation, ok := resolveValuation(ref.ValuationID)
		if !ok {
			return
		}
		if valuation.InputSetHash == "" || valuation.InputSetHash != ref.InputSetHash {
			return
		}
		seedValuationCoverage(valuation)
	}

	// bindValuationByID seeds the explicit inclusive coverage contract of one
	// authoritative valuation by ID. It is the explicit-selection counterpart
	// of bindSelectedValuation: the winning candidate names the valuation
	// whose payable lines prove monetary inclusion. No subject-equality
	// shortcut and no whole-unit promotion are performed; line source
	// references are still verified against the original observation payload.
	bindValuationByID := func(valuationID string) {
		valuation, ok := resolveValuation(valuationID)
		if !ok {
			return
		}
		seedValuationCoverage(valuation)
	}

	// Coverage seeds only the exact authoritative selected result used for the
	// monetary amount. An explicit selection binds its winning candidate
	// valuation; a persisted single-head selection binds only that head.
	// Ambiguous, missing, or multi-head scopes bind nothing, so independent
	// retained heads remain unresolved and are never summed.
	if selection != nil {
		seedSubject(selection.Subject, metering.CorrelationV2{StoreID: selection.Subject.StoreID})
		if hasExplicit {
			for _, candidate := range selection.Candidates {
				if candidate.Basis == selection.Basis {
					bindValuationByID(candidate.ValuationID)
				}
			}
		} else if len(heads) == 1 && heads[0].Selected != nil {
			seedSubject(heads[0].Subject, metering.CorrelationV2{StoreID: heads[0].Subject.StoreID})
			bindSelectedValuation(heads[0].Selected.Ref)
		}
	}

	// Propagate explicit inclusive coverage transitively along exact
	// charge/revision identity edges. The worklist processes each atom once, so
	// traversal is bounded and cycle-safe; an unrelated sibling charge is never
	// promoted, and additive relations are never followed.
	for len(queue) > 0 {
		atomKey := queue[0]
		queue = queue[1:]
		atom, ok := atomByKey[atomKey]
		if !ok || atom.chargeIndex >= len(atom.observation.Charges) {
			continue
		}
		for _, edge := range atom.observation.Charges[atom.chargeIndex].Covers {
			if edge.Relation != metering.CoverageInclusive {
				continue
			}
			seedChargeRef(edge.Ref)
		}
	}

	unitStates := make(map[string]EconomicDetailCostCoverageState, len(unitOrder))
	for _, unitKey := range unitOrder {
		state := costCoverageClassifyUnit(units[unitKey], baseCovered, atomCovered)
		unitStates[unitKey] = state
		if state == EconomicDetailCostCoverageUnresolved {
			continue
		}
		if matchKey, ok := matchKeyByUnit[unitKey]; ok {
			markCoveredMatch(matchKey, state)
		}
	}

	projection := EconomicDetailCostCoverage{}
	usableMatchKeys := make(map[string]struct{}, len(units))
	for _, unitKey := range unitOrder {
		unit := units[unitKey]
		if !unit.hasCharge && !unit.hasMeasures {
			continue
		}
		if matchKey, ok := matchKeyByUnit[unitKey]; ok {
			usableMatchKeys[matchKey] = struct{}{}
		}
	}
	for _, unitKey := range unitOrder {
		unit := units[unitKey]
		if !unit.hasCharge && !unit.hasMeasures {
			// No attributable economic evidence: never invent a cost subject.
			continue
		}
		state := unitStates[unitKey]
		if state == EconomicDetailCostCoverageUnresolved {
			projection.UnresolvedCount++
		}
		subject := EconomicDetailCostSubject{
			Subject:         unit.subject.Clone(),
			State:           state,
			OperatorPayable: unit.hasNonCustomerCharge || (unit.hasMeasures && (!unit.hasCustomerCharge || unit.hasNonCustomerCharge)),
			ChargeItemIDs:   costCoverageCanonicalChargeItems(unit),
		}
		projection.Subjects = append(projection.Subjects, subject)
	}
	// Every authoritative in-scope executed leg either already has usable
	// observation evidence (governed above) or contributes an explicit execution
	// subject. Missing accepted evidence is unresolved; only canonical
	// never-started/nonbillable/known-zero proof, or proven selected/inclusive
	// containment, exempts it. No measurement or amount is invented.
	for _, leg := range executionLegs {
		matchKey, hasMatch := costCoverageBLegMatchKey(leg.Subject, metering.CorrelationV2{StoreID: leg.Subject.StoreID})
		if hasMatch {
			if _, governed := usableMatchKeys[matchKey]; governed {
				continue
			}
		}
		state := leg.coverageState()
		if state == EconomicDetailCostCoverageUnresolved && hasMatch {
			if coveredState, proven := coveredMatch[matchKey]; proven {
				state = coveredState
			}
		}
		if state == EconomicDetailCostCoverageUnresolved {
			projection.UnresolvedCount++
		}
		operatorPayable := state != EconomicDetailCostCoverageNeverStarted &&
			state != EconomicDetailCostCoverageNonbillable &&
			state != EconomicDetailCostCoverageBYOK
		projection.Subjects = append(projection.Subjects, EconomicDetailCostSubject{
			Subject:         leg.Subject.Clone(),
			State:           state,
			OperatorPayable: operatorPayable,
		})
	}
	sort.SliceStable(projection.Subjects, func(i, j int) bool {
		left := economicDetailSubjectSortKey(projection.Subjects[i].Subject)
		right := economicDetailSubjectSortKey(projection.Subjects[j].Subject)
		if left != right {
			return left < right
		}
		return strings.Join(projection.Subjects[i].ChargeItemIDs, "\x00") < strings.Join(projection.Subjects[j].ChargeItemIDs, "\x00")
	})

	selectionCurrency := ""
	if selection != nil {
		selectionCurrency = selection.Currency
	}
	for i := range allocations {
		line := allocations[i]
		// An unallocated remainder and an informational linkage are not live
		// attributed costs, so they never participate in monetary completeness.
		if line.Unallocated || line.Informational {
			continue
		}
		if line.Redacted {
			// The source aggregate was withheld as unauthorized. It can neither
			// prove inclusion nor be exempted as zero, so it fails closed
			// without exposing any amount.
			projection.AllocationCoverage = append(projection.AllocationCoverage, EconomicDetailAllocationCoverage{
				AllocationID: line.AllocationID, AllocationVersion: line.AllocationVersion,
				TargetID: line.TargetID, State: EconomicDetailCostCoverageUnresolved,
				Reason: EconomicDetailAllocationCoverageRedactedSource, Redacted: true,
			})
			projection.AllocationUnresolvedCount++
			continue
		}
		if line.SourceAmount == nil {
			// An exact quantity allocation remains source-preserving accounting
			// evidence; it is not operator money and cannot move a margin.
			continue
		}
		identity := costCoverageAllocationIdentity(line.SourceSubject.StoreID, line.AllocationID, line.AllocationVersion)
		included := false
		for _, targetKey := range costCoverageLineAllocationTargetKeys(line.Target) {
			if _, ok := includedAllocationTargets[identity+"\x00"+targetKey]; ok {
				included = true
				break
			}
		}
		zero := costCoverageAllocationLineIsZero(line)
		currencyMismatch := line.Currency != "" && selectionCurrency != "" && line.Currency != selectionCurrency
		entry := EconomicDetailAllocationCoverage{
			AllocationID: line.AllocationID, AllocationVersion: line.AllocationVersion,
			TargetID: line.TargetID, Currency: line.Currency,
		}
		switch {
		case zero:
			// An exact canonical zero cannot move the margin and is exempt.
			entry.State = EconomicDetailCostCoverageKnownZero
			entry.Reason = EconomicDetailAllocationCoverageKnownZero
		case included && !currencyMismatch:
			entry.State = EconomicDetailCostCoverageSelected
			entry.Reason = EconomicDetailAllocationCoverageIncluded
		case currencyMismatch:
			entry.State = EconomicDetailCostCoverageUnresolved
			entry.Reason = EconomicDetailAllocationCoverageCurrencyMismatch
			projection.AllocationUnresolvedCount++
		default:
			entry.State = EconomicDetailCostCoverageUnresolved
			entry.Reason = EconomicDetailAllocationCoverageNotIncluded
			projection.AllocationUnresolvedCount++
		}
		projection.AllocationCoverage = append(projection.AllocationCoverage, entry)
	}
	sort.SliceStable(projection.AllocationCoverage, func(i, j int) bool {
		left, right := projection.AllocationCoverage[i], projection.AllocationCoverage[j]
		if left.AllocationID != right.AllocationID {
			return left.AllocationID < right.AllocationID
		}
		if left.AllocationVersion != right.AllocationVersion {
			return left.AllocationVersion < right.AllocationVersion
		}
		if left.TargetID != right.TargetID {
			return left.TargetID < right.TargetID
		}
		return left.Reason < right.Reason
	})

	projection.Complete = graphValid && projection.UnresolvedCount == 0 && projection.AllocationUnresolvedCount == 0
	return projection
}

// canonicalCoverageGraph reduces the bounded in-scope observations through the
// authoritative supersession reducer and returns the effective, non-superseded
// charge observations only after the billing closed-graph validator accepts
// them. It returns ok=false when the reducer rejects the batch (cycle,
// contradictory or duplicate edges, ambiguous inclusive parents, ambiguous
// cumulative, identity conflict, supersession error), when an unresolved
// supersession predecessor remains, or when the closed graph has an unavailable
// target or overlapping aggregate/component charge. Callers must fail closed:
// no Covers edge from an unvalidated graph may prove inclusion.
func canonicalCoverageGraph(observations []metering.Observation) ([]metering.Observation, bool) {
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		return nil, false
	}
	if len(snapshot.PendingCoverage) != 0 || len(snapshot.PendingSupersedes) != 0 || len(snapshot.UnusablePredecessors) != 0 {
		return nil, false
	}
	graphObservations := aggregate.EffectiveChargeGraphObservations(snapshot)
	if _, _, err := validateChargeCoverageGraph(graphObservations); err != nil {
		return nil, false
	}
	return graphObservations, true
}

// costCoverageUnit is one attributable ownership unit derived from authoritative
// executed observations: a B-leg root or a provider-charge event beneath one.
// chargeKeys enumerates the exact charge identities whose monetary inclusion is
// tracked independently of the unit's B-leg ownership.
type costCoverageUnit struct {
	key                  string
	subject              metering.SubjectRef
	correlation          metering.CorrelationV2
	hasMeasures          bool
	hasCharge            bool
	hasCustomerCharge    bool
	hasNonCustomerCharge bool
	allNonCustomerZero   bool
	chargeItems          []string
	chargeKeys           []string
}

// costCoverageChargeAtom is one exact canonical charge identity: the
// observation revision and charge item that monetary inclusion is proven over.
type costCoverageChargeAtom struct {
	observation metering.Observation
	chargeIndex int
}

// costCoverageUnitKey maps one in-scope observation subject to its coverage
// ownership unit. Request-scoped costs are rooted in the B-leg (Requirement
// 6.2); a provider charge remains its own exact-identity unit and never inherits
// coverage from the B-leg that owns it.
func costCoverageUnitKey(subject metering.SubjectRef, correlation metering.CorrelationV2) (unitKey string, ok bool) {
	switch subject.Kind {
	case metering.SubjectBLeg:
		if bLegKey, hasBLeg := costCoverageBLegOwnershipKey(subject, correlation); hasBLeg {
			return "bleg:" + bLegKey, true
		}
		return "subj:" + costCoverageSubjectIdentity(subject), true
	case metering.SubjectProviderCharge:
		return "charge:" + costCoverageSubjectIdentity(subject), true
	default:
		return "", false
	}
}

// costCoverageBLegOwnershipKey is the stable ownership key of the B-leg that an
// observable request-scoped cost belongs to. It folds in every lineage field
// that distinguishes one executed B-leg from another.
func costCoverageBLegOwnershipKey(subject metering.SubjectRef, correlation metering.CorrelationV2) (string, bool) {
	bLegID := subject.BLegID
	if bLegID == "" {
		bLegID = correlation.BLegID
	}
	if bLegID == "" {
		return "", false
	}
	var builder strings.Builder
	writeEconomicStreamPart(&builder, subject.StoreID)
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.TenantID, correlation.TenantID))
	writeEconomicStreamPart(&builder, subject.AccountID)
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.ALegID, correlation.ALegID))
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.BillingCallID, correlation.BillingCallID))
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.CallID, correlation.CallID))
	writeEconomicStreamPart(&builder, bLegID)
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.AttemptID, correlation.AttemptID))
	writeEconomicStreamPart(&builder, fmt.Sprint(costCoverageFirstNonZero(subject.AttemptSeq, correlation.AttemptSeq)))
	return builder.String(), true
}

// costCoverageBLegMatchKey is the tenant-, account- and attempt-independent
// identity of one concrete B-leg. It is used only to relate an authoritative
// execution fact to the observation-derived unit of the same B-leg: BLegID is
// the accounting ownership root (requirement 6.2), and tenant/account are
// validated separately at the trusted scope boundary while AttemptSeq is
// ordered lineage, not an ownership key. It is deliberately distinct from
// costCoverageBLegOwnershipKey so execution facts never weaken charge-inclusion
// identity.
func costCoverageBLegMatchKey(subject metering.SubjectRef, correlation metering.CorrelationV2) (string, bool) {
	bLegID := subject.BLegID
	if bLegID == "" {
		bLegID = correlation.BLegID
	}
	if bLegID == "" {
		return "", false
	}
	var builder strings.Builder
	writeEconomicStreamPart(&builder, subject.StoreID)
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.ALegID, correlation.ALegID))
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.BillingCallID, correlation.BillingCallID))
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.CallID, correlation.CallID))
	writeEconomicStreamPart(&builder, bLegID)
	return builder.String(), true
}

// authoritativeProviderChargeObservation reports whether an observation's
// charges carry canonical provider-authoritative money. The provider-cost
// boundary accepts a provider-origin, provider-acquired observed claim; local,
// estimated, unavailable and statement claims are advisory or separately
// allocated and can never prove an exact provider zero. This is the same
// authority vocabulary RateProviderCost applies before any monetary
// classification, so a weaker zero can never stand in for provider money.
func authoritativeProviderChargeObservation(observation metering.Observation) bool {
	return observation.Origin == metering.OriginProvider &&
		isProviderEvidenceAcquisition(observation.Acquisition) &&
		observation.Authority == metering.AuthorityObservedClaim
}

// costCoverageClassifyUnit resolves the bounded coverage state of one unit. A
// unit is covered only when every exact charge identity it owns is proven
// included, or, for a unit with no charge identity at all, its base execution
// is proven covered. Base-execution coverage never stands in for a charge atom:
// a selected subject covers base execution only, and one proven charge never
// promotes an independent sibling. Explicit known-zero or customer-BYOK
// evidence still exempts a unit without a selected-coverage proof.
func costCoverageClassifyUnit(unit *costCoverageUnit, baseCovered, atomCovered map[string]EconomicDetailCostCoverageState) EconomicDetailCostCoverageState {
	if unit.hasCustomerCharge && !unit.hasNonCustomerCharge {
		return EconomicDetailCostCoverageBYOK
	}
	if unit.hasNonCustomerCharge && unit.allNonCustomerZero {
		return EconomicDetailCostCoverageKnownZero
	}
	if len(unit.chargeKeys) > 0 {
		coveredCount := 0
		strongest := EconomicDetailCostCoverageState("")
		for _, atomKey := range unit.chargeKeys {
			state, ok := atomCovered[atomKey]
			if !ok {
				continue
			}
			coveredCount++
			if strongest == "" || state == EconomicDetailCostCoverageSelected {
				strongest = state
			}
		}
		if coveredCount == len(unit.chargeKeys) && strongest != "" {
			return strongest
		}
		return EconomicDetailCostCoverageUnresolved
	}
	if state, ok := baseCovered[unit.key]; ok {
		return state
	}
	return EconomicDetailCostCoverageUnresolved
}

// costCoverageLineProvesInclusion reports whether a selected valuation line is a
// settled payable line whose identity can prove monetary inclusion. Only rated,
// explicit-free and provider-reported lines are authoritative; incomplete or
// unsupported lines remain participation and never grant coverage.
func costCoverageLineProvesInclusion(status economics.RatingLineStatus) bool {
	switch status {
	case economics.RatingLineRated, economics.RatingLineExplicitFree, economics.RatingLineProviderReported:
		return true
	default:
		return false
	}
}

func costCoverageCanonicalChargeItems(unit *costCoverageUnit) []string {
	if len(unit.chargeItems) == 0 {
		return nil
	}
	out := make([]string, 0, len(unit.chargeItems))
	seen := make(map[string]struct{}, len(unit.chargeItems))
	for _, item := range unit.chargeItems {
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func costCoverageValuationByID(valuations []economics.Valuation, id string) (economics.Valuation, bool) {
	for i := range valuations {
		if valuations[i].ID == id {
			return valuations[i], true
		}
	}
	return economics.Valuation{}, false
}

// costCoverageObservationRevisionKey is the exact immutable identity of one
// observation revision: store, observation id and revision. It is the lookup
// key for verifying a selected source reference against the original retained
// payload hash.
func costCoverageObservationRevisionKey(storeID, observationID string, revision uint64) string {
	var builder strings.Builder
	writeEconomicStreamPart(&builder, storeID)
	writeEconomicStreamPart(&builder, observationID)
	writeEconomicStreamPart(&builder, fmt.Sprint(revision))
	return builder.String()
}

func costCoverageChargeLookupKey(storeID, observationID string, revision uint64, chargeItemID string) string {
	var builder strings.Builder
	writeEconomicStreamPart(&builder, storeID)
	writeEconomicStreamPart(&builder, observationID)
	writeEconomicStreamPart(&builder, fmt.Sprint(revision))
	writeEconomicStreamPart(&builder, chargeItemID)
	return builder.String()
}

func costCoverageSubjectIdentity(subject metering.SubjectRef) string {
	var builder strings.Builder
	writeEconomicSubjectIdentity(&builder, subject)
	return builder.String()
}

func costCoverageFirstNonEmpty(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func costCoverageFirstNonZero(primary, fallback uint64) uint64 {
	if primary != 0 {
		return primary
	}
	return fallback
}

// costCoverageAllocationIdentity is the exact immutable identity of one
// allocation revision: store, allocation id and version. It is deliberately
// independent of target or share, because allocation inclusion is tracked per
// revision and only then related to a target ownership.
func costCoverageAllocationIdentity(storeID, allocationID string, version uint64) string {
	if allocationID == "" || version == 0 {
		return ""
	}
	var builder strings.Builder
	writeEconomicStreamPart(&builder, storeID)
	writeEconomicStreamPart(&builder, allocationID)
	writeEconomicStreamPart(&builder, fmt.Sprint(version))
	return builder.String()
}

// costCoverageAllocationLineIsZero reports whether one active allocation
// target's exact attributable contribution is a canonical zero that cannot move
// the margin. It classifies the target's own effective amount, never the shared
// source aggregate: a conserved, legal zero-weight target of a nonzero source
// incurs exactly zero attributable cost, while a nonzero exact share stays live
// even when its integer projection floors to zero.
//
// Both authoritative halves of the canonical rollup line are honored: the exact
// contribution (SourceAmount * Share) must be exactly zero, and the canonical
// integer ledger projection must not have attributed money to this target. A
// declared residual can land on a zero-weight target and a nonzero exact share
// can round to a zero projection; neither may be exempted. A missing/unreadable
// amount or share, or an absent source, fails closed. No amount is summed and no
// share is recomputed from a raw weight.
func costCoverageAllocationLineIsZero(line AllocatedCostLine) bool {
	// The exact product is zero exactly when either factor is exactly zero.
	// Neither factor is inferred from the other or from a rounded projection.
	exactZero := false
	if line.SourceAmount != nil {
		if value, err := line.SourceAmount.ToRat(); err == nil && value.Sign() == 0 {
			exactZero = true
		}
	}
	if !exactZero {
		if share, err := line.Share.Rat(); err == nil && share.Sign() == 0 {
			exactZero = true
		}
	}
	if !exactZero {
		return false
	}
	// The canonical rounded projection is authoritative for attributed money: a
	// residual assigned to this exact target is live cost even when its raw
	// share is zero.
	if line.RoundedAmount != nil && line.RoundedAmount.Present && line.RoundedAmount.NanoUnits != 0 {
		return false
	}
	return true
}

// costCoverageSubjectAllocationTargetKeys returns the allocation-target
// ownership keys a frozen valuation subject can prove: its B-leg match key
// (when it names a leg) and its call match key. Tenant, account and attempt are
// deliberately excluded, exactly as for execution coverage, so a representation
// difference never weakens or widens ownership.
func costCoverageSubjectAllocationTargetKeys(subject metering.SubjectRef, correlation metering.CorrelationV2) []string {
	keys := make([]string, 0, 2)
	if legKey, ok := costCoverageBLegMatchKey(subject, correlation); ok {
		keys = append(keys, "leg:"+legKey)
	}
	if callKey, ok := costCoverageCallMatchKey(subject, correlation); ok {
		keys = append(keys, "call:"+callKey)
	}
	return keys
}

// costCoverageLineAllocationTargetKeys returns the ownership keys an
// allocation line's target can be matched by. A B-leg target is proven only by
// the same B-leg ownership; a call target only by the same call ownership.
func costCoverageLineAllocationTargetKeys(target metering.SubjectRef) []string {
	switch target.Kind {
	case metering.SubjectBLeg:
		if legKey, ok := costCoverageBLegMatchKey(target, metering.CorrelationV2{StoreID: target.StoreID}); ok {
			return []string{"leg:" + legKey}
		}
	case metering.SubjectBillingCall:
		if callKey, ok := costCoverageCallMatchKey(target, metering.CorrelationV2{StoreID: target.StoreID}); ok {
			return []string{"call:" + callKey}
		}
	}
	return nil
}

// costCoverageCallMatchKey is the tenant-, account- and attempt-independent
// identity of one concrete BillingCall. It mirrors costCoverageBLegMatchKey at
// call granularity so a whole-call allocation can be proven by the selected
// valuation of a leg owned by that call.
func costCoverageCallMatchKey(subject metering.SubjectRef, correlation metering.CorrelationV2) (string, bool) {
	callID := subject.BillingCallID
	if callID == "" {
		callID = subject.CallID
	}
	if callID == "" {
		callID = correlation.BillingCallID
	}
	if callID == "" {
		callID = correlation.CallID
	}
	if callID == "" {
		return "", false
	}
	var builder strings.Builder
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.StoreID, correlation.StoreID))
	writeEconomicStreamPart(&builder, costCoverageFirstNonEmpty(subject.ALegID, correlation.ALegID))
	writeEconomicStreamPart(&builder, callID)
	return builder.String(), true
}
