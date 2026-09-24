package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ComponentComparisonReconciler is a pure deterministic EconomicJobReconciler
// over frozen revision outputs. It joins every declared dependency valuation
// on component identity, reports exact signed provider-minus-local quantity
// deltas with matched/discrepant/partial/conflict/incomparable status, and
// carries no balance, journal, exposure, or payable port. It performs no
// rating, persistence, or policy selection; those remain the surrounding job
// runner and store seams.
//
// Comparability follows the approved C4/D1 grouping: only genuinely
// independent local measurement (BasisLocalExpected/E) compares against
// provider evidence (BasisProviderQuantityLocal/Q or
// BasisProviderReported/P). Retail/customer-policy (R) may derive from
// policy-selected provider data (requirement 6.4) and never masquerades as
// independent local evidence. Subject/payer, coverage and effective
// measurement context must agree across every dependency valuation;
// mismatches are explicit incomparable outcomes with typed reasons, never
// matches. Dependency Completeness/MissingObservations are preserved: equal
// known subtotals with missing evidence stay partial/incomplete, never
// complete matched. The join reuses the established
// canonicalReconciliationCoverageKey/canonicalReconciliationQualifierKey
// contracts and exact big.Rat arithmetic; it introduces no second reduced
// interpretation.
type ComponentComparisonReconciler struct{}

// comparisonRole is the side one dependency valuation plays in the join.
type comparisonRole string

const (
	comparisonRoleLocal    comparisonRole = "local"
	comparisonRoleProvider comparisonRole = "provider"
)

// comparisonItem is one joined component outcome in the result envelope.
type comparisonItem struct {
	Component          string  `json:"component"`
	Status             string  `json:"status"`
	Reason             string  `json:"reason,omitempty"`
	LocalQuantity      *string `json:"local_quantity,omitempty"`
	ProviderQuantity   *string `json:"provider_quantity,omitempty"`
	SignedDelta        *string `json:"signed_delta,omitempty"`
	AbsoluteDelta      *string `json:"absolute_delta,omitempty"`
	LocalAmountNano    *int64  `json:"local_amount_nano,omitempty"`
	ProviderAmountNano *int64  `json:"provider_amount_nano,omitempty"`
}

// comparisonResult is the canonical result envelope payload.
type comparisonResult struct {
	Status   string           `json:"status"`
	Complete bool             `json:"complete"`
	Reason   string           `json:"reason,omitempty"`
	Items    []comparisonItem `json:"items"`
}

// ReconcileJob resolves every frozen dependency output into local and
// provider quantity planes and compares them component by component. All
// outputs are consumed; a result that silently dropped one dependency would
// be indistinguishable from agreement. Missing quantities stay partial,
// conflicting duplicates stay conflicts, and incompatible subjects stay
// incomparable instead of becoming zeros or matches.
//
// F4 comparability gates run before any quantity join, in stable precedence:
// independent-basis, payer, coverage, then effective measurement context.
// Each returns an explicit incomparable envelope with empty items (no foreign
// or non-independent quantities leaked, no unsafe values echoed) when the
// frozen inputs are not comparable as independent local-versus-provider
// evidence. Completeness/missing evidence is preserved after the join: equal
// known subtotals with missing observations or severe incompleteness stay
// partial/incomplete, never complete matched.
func (ComponentComparisonReconciler) ReconcileJob(ctx context.Context, work EconomicRevisionWork, outputs []EconomicJobDependencyOutput) (*EconomicReconciliation, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if work.Kind != EconomicWorkKindReconciliation {
		return nil, fmt.Errorf("%w: reconciliation requires reconciliation work, got %q", ErrInvalidEconomicRevision, work.Kind)
	}
	if len(work.Dependencies) == 0 {
		return nil, fmt.Errorf("%w: reconciliation work declares no dependencies", ErrInvalidEconomicRevision)
	}
	if len(outputs) != len(work.Dependencies) {
		return nil, fmt.Errorf("%w: outputs %d do not cover %d declared dependencies", ErrInvalidEconomicRevision, len(outputs), len(work.Dependencies))
	}
	_, reason, incompatible := comparisonSubject(work, outputs)
	if incompatible {
		return comparisonEnvelope(work, ReconciliationStatusIncomparable, string(reason), nil, false)
	}
	if reason, incompatible, err := comparisonBasis(outputs); err != nil {
		return nil, err
	} else if incompatible {
		return comparisonEnvelope(work, ReconciliationStatusIncomparable, string(reason), nil, false)
	}
	if reason, incompatible, err := comparisonValuationContext(outputs); err != nil {
		return nil, err
	} else if incompatible {
		return comparisonEnvelope(work, ReconciliationStatusIncomparable, string(reason), nil, false)
	}
	local, provider, err := comparisonPlanes(outputs)
	if err != nil {
		return nil, err
	}
	items := joinComparisonItems(local, provider)
	// Aggregate precedence mirrors the production quantity summary: a
	// conflict beats incomparable and partial results, partial or missing
	// evidence beats a proven discrepancy, and only full agreement is a
	// match. A proven discrepancy is complete evidence; anything less is
	// not. Matched items therefore never mask a separately recorded
	// discrepancy, and an incomplete picture never reports a match.
	status, complete := summarizeComparisonItems(items)
	if comparisonIncomplete(outputs) && (status == ReconciliationStatusMatched || status == ReconciliationStatusDiscrepant) {
		// Preserve dependency completeness: equal known subtotals with
		// missing observations or severe valuation incompleteness stay
		// partial/incomplete. Items retain their exact quantity evidence;
		// only the aggregate claim is downgraded from complete.
		status, complete = ReconciliationStatusPartial, false
	}
	return comparisonEnvelope(work, status, "", items, complete)
}

// comparisonSubject requires every dependency valuation to exactly match the
// reconciliation work comparison subject and scope before any item is
// generated. Outputs rooted at a foreign subject, or at the right subject
// with a different scope, cannot be meaningfully joined under the work
// identity; callers must scope reconciliation work per subject/scope instead.
// Binding is at B-leg ownership granularity: attempt lineage (AttemptID,
// AttemptSeq) is child execution detail within the same B-leg and, per
// requirement 6.2, is not an alternative accounting subject, so it is cleared
// before comparison. Every other subject field (store, account, call, A-leg,
// B-leg, resource, allocation identity via subject kind) must match exactly.
// This comparer supports no cross-subject allocation join (design D1/C4:
// resource costs contribute only through explicit conserved allocation, never
// as a direct cross-subject quantity comparison), so binding is otherwise
// exact. A mismatch yields an explicit incomparable envelope with empty items,
// never a matched result stamped with the work subject and never foreign
// quantities in result JSON.
func comparisonSubject(work EconomicRevisionWork, outputs []EconomicJobDependencyOutput) (string, ReconciliationComparisonReason, bool) {
	workOwnership := comparisonOwnershipSubject(work.Subject)
	for _, output := range outputs {
		if !sameSubject(comparisonOwnershipSubject(output.Valuation.Subject), workOwnership) {
			return "", ReconciliationReasonSubjectMismatch, true
		}
	}
	for _, output := range outputs {
		if output.Valuation.Scope != work.Input.Scope {
			return "", ReconciliationReasonScopeMismatch, true
		}
	}
	encoded, err := json.Marshal(work.Subject)
	if err != nil {
		return "", ReconciliationReasonSubjectMismatch, true
	}
	return string(encoded), "", false
}

// comparisonOwnershipSubject clears attempt lineage so reconciliation binds at
// B-leg ownership granularity. AttemptSeq/AttemptID identify ordered child
// execution within the same B-leg/call context and are not standalone
// ownership keys.
func comparisonOwnershipSubject(subject metering.SubjectRef) metering.SubjectRef {
	out := subject
	out.AttemptID = ""
	out.AttemptSeq = 0
	return out
}

// comparisonBasis restricts the join to genuinely independent evidence
// pairs: local_expected (E) against provider_quantity_local (Q) or
// provider_reported (P), per requirements 1.1/1.2/7.2/12.1 and design C4
// (E/Q/P without substitution). Retail/customer-policy (R) may select
// provider data as its quantity source, so it never counts as independent
// local corroboration; statement, allocated-cost and unit-debit bases are
// likewise not independent quantity evidence. Known but non-comparable bases
// yield an explicit incomparable envelope with empty items (no quantities
// leaked, no unsafe values echoed). Truly unknown bases fail closed as
// invalid input via comparisonRoleFor.
func comparisonBasis(outputs []EconomicJobDependencyOutput) (ReconciliationComparisonReason, bool, error) {
	for _, output := range outputs {
		basis := output.Valuation.Basis
		if !basis.IsKnown() {
			return "", false, fmt.Errorf("%w: unsupported comparison basis %q", ErrInvalidEconomicRevision, basis)
		}
		switch basis {
		case economics.BasisLocalExpected, economics.BasisProviderQuantityLocal, economics.BasisProviderReported:
		default:
			return ReconciliationReasonBasisMismatch, true, nil
		}
	}
	return "", false, nil
}

// comparisonValuationContext enforces the C4 grouping across every dependency
// valuation: payer, coverage and effective measurement context must agree,
// otherwise equal quantities are incomparable, never matched. It reuses the
// established canonicalReconciliationCoverageKey and
// canonicalReconciliationQualifierKey contracts so this comparer cannot drift
// into a second reduced interpretation. Checks run in stable precedence
// (payer, coverage, context) and return typed reasons without echoing raw
// payer IDs, coverage hashes or qualifier values. Empty-items envelopes
// prevent any non-comparable quantity from being presented as a comparison.
func comparisonValuationContext(outputs []EconomicJobDependencyOutput) (ReconciliationComparisonReason, bool, error) {
	if len(outputs) == 0 {
		return "", false, nil
	}
	payer := outputs[0].Valuation.Payer
	for _, output := range outputs[1:] {
		if output.Valuation.Payer != payer {
			return ReconciliationReasonPayerMismatch, true, nil
		}
	}
	coverage, err := comparisonCoverageKey(outputs[0].Valuation)
	if err != nil {
		return "", false, err
	}
	for _, output := range outputs[1:] {
		key, err := comparisonCoverageKey(output.Valuation)
		if err != nil {
			return "", false, err
		}
		if key != coverage {
			return ReconciliationReasonCoverageMismatch, true, nil
		}
	}
	context, err := comparisonMeasurementContextKey(outputs[0].Valuation)
	if err != nil {
		return "", false, err
	}
	for _, output := range outputs[1:] {
		key, err := comparisonMeasurementContextKey(output.Valuation)
		if err != nil {
			return "", false, err
		}
		if key != context {
			return ReconciliationReasonContextMismatch, true, nil
		}
	}
	return "", false, nil
}

// comparisonCoverageKey canonicalizes both charge coverage and allocation
// coverage into one deterministic comparability identity. Either graph
// differing across valuations is a material coverage difference per C4.
func comparisonCoverageKey(valuation economics.Valuation) (string, error) {
	coverageKey, err := canonicalReconciliationCoverageKey(valuation.CoverageRefs)
	if err != nil {
		return "", fmt.Errorf("%w: coverage: %v", ErrInvalidEconomicRevision, err)
	}
	allocations, err := economics.CanonicalAllocationCoverageRefs(valuation.AllocationCoverageRefs)
	if err != nil {
		return "", fmt.Errorf("%w: allocation coverage: %v", ErrInvalidEconomicRevision, err)
	}
	encodedAllocations, err := json.Marshal(allocations)
	if err != nil {
		return "", fmt.Errorf("%w: allocation coverage: %v", ErrInvalidEconomicRevision, err)
	}
	return coverageKey + "\x00" + string(encodedAllocations), nil
}

// comparisonMeasurementContextKey canonicalizes the effective measurement
// context: effective qualifiers plus qualifier snapshot identity. Scope is
// already bound by comparisonSubject; tariff/rater frozen identity governs
// monetary decomposition, not quantity comparability, so only measurement
// qualifiers participate here.
func comparisonMeasurementContextKey(valuation economics.Valuation) (string, error) {
	qualifierKey, err := canonicalReconciliationQualifierKey(valuation.EffectiveQualifiers)
	if err != nil {
		return "", fmt.Errorf("%w: measurement context: %v", ErrInvalidEconomicRevision, err)
	}
	snapshotRef := ""
	if valuation.QualifierSnapshotRef != nil {
		encoded, err := json.Marshal(*valuation.QualifierSnapshotRef)
		if err != nil {
			return "", fmt.Errorf("%w: measurement context: %v", ErrInvalidEconomicRevision, err)
		}
		snapshotRef = string(encoded)
	}
	return qualifierKey + "\x00" + valuation.QualifierSnapshot + "\x00" + snapshotRef, nil
}

// comparisonIncomplete preserves dependency completeness into the aggregate:
// any missing observations or any non-complete valuation completeness
// (partial, unknown, conflict, unavailable) keeps equal known subtotals
// partial/incomplete. An explicit partial is incomplete even without
// enumerated missing refs: partial can represent a failed rule or
// classification without a missing observation ref, so the aggregate must not
// report complete agreement while a dependency reports partial.
func comparisonIncomplete(outputs []EconomicJobDependencyOutput) bool {
	for _, output := range outputs {
		if len(output.Valuation.MissingObservations) != 0 {
			return true
		}
		switch output.Valuation.Completeness {
		case economics.CompletenessPartial, economics.CompletenessUnknown, economics.CompletenessConflict, economics.CompletenessUnavailable:
			return true
		}
	}
	return false
}

type comparisonQuantity struct {
	value    *big.Rat
	literal  string
	amount   *int64
	conflict bool
	missing  bool
}

type comparisonPlane map[string]*comparisonQuantity

// comparisonPlanes indexes every dependency valuation line by component
// identity within its local or provider role. Duplicate keys with different
// values stay conflicts; identical duplicates merge silently; any missing
// quantity keeps the component explicitly missing so later known data never
// silently erases missing evidence. The merge is total, nil-safe, and
// order-independent: missing and conflict flags are sticky, every conflict
// (quantity or rounded-amount) clears to a canonical empty value/literal/
// amount, and equal quantities with equal amounts retain their shared value.
// The union cardinality is bounded before any result is rendered.
func comparisonPlanes(outputs []EconomicJobDependencyOutput) (comparisonPlane, comparisonPlane, error) {
	local := make(comparisonPlane)
	provider := make(comparisonPlane)
	seen := make(map[string]struct{})
	for _, output := range outputs {
		role, err := comparisonRoleFor(output.Valuation.Basis)
		if err != nil {
			return nil, nil, err
		}
		plane := local
		if role == comparisonRoleProvider {
			plane = provider
		}
		for _, line := range output.Valuation.Lines {
			if line.Component == nil {
				continue
			}
			key := line.Component.CanonicalKey()
			if _, ok := seen[key]; !ok {
				if len(seen) >= MaxReconciliationItems {
					return nil, nil, fmt.Errorf("%w: comparison cardinality exceeds %d", ErrReconciliationBoundExceeded, MaxReconciliationItems)
				}
				seen[key] = struct{}{}
			}
			quantity, amount, err := comparisonLineValues(line)
			if err != nil {
				return nil, nil, err
			}
			literal := ""
			if line.Quantity != nil {
				if normalized, err := line.Quantity.Normalize(); err == nil {
					literal = ratDecimalValue(normalized)
				} else {
					return nil, nil, fmt.Errorf("%w: line quantity: %v", ErrInvalidEconomicRevision, err)
				}
			}
			prior, exists := plane[key]
			if !exists || prior == nil {
				plane[key] = &comparisonQuantity{value: quantity, literal: literal, amount: amount, missing: quantity == nil}
				continue
			}
			if quantity == nil {
				prior.missing = true
				continue
			}
			if prior.conflict {
				prior.value = nil
				prior.literal = ""
				prior.amount = nil
				continue
			}
			if prior.value == nil {
				prior.value = quantity
				prior.literal = literal
				prior.amount = amount
				continue
			}
			if prior.value.Cmp(quantity) != 0 {
				prior.conflict = true
				prior.value = nil
				prior.literal = ""
				prior.amount = nil
				continue
			}
			if amount != nil {
				if prior.amount == nil {
					prior.amount = amount
				} else if *prior.amount != *amount {
					prior.conflict = true
					prior.value = nil
					prior.literal = ""
					prior.amount = nil
				}
			}
		}
	}
	return local, provider, nil
}

// comparisonRoleFor assigns the comparison side from the valuation basis.
// Only genuinely independent local measurement (E) compares against provider
// evidence (Q/P). Retail/customer-policy (R) may derive from policy-selected
// provider data and never masquerades as independent local evidence; known
// but non-comparable bases fail as incomparable via comparisonBasis, while
// truly unknown bases fail closed here as invalid input.
func comparisonRoleFor(basis economics.ValuationBasis) (comparisonRole, error) {
	switch basis {
	case economics.BasisLocalExpected:
		return comparisonRoleLocal, nil
	case economics.BasisProviderQuantityLocal, economics.BasisProviderReported:
		return comparisonRoleProvider, nil
	default:
		return "", fmt.Errorf("%w: unsupported comparison basis %q", ErrInvalidEconomicRevision, basis)
	}
}

// comparisonLineValues extracts the exact quantity and optional rounded money
// of one valuation line. A missing quantity stays explicitly missing; it is
// never defaulted to zero.
func comparisonLineValues(line economics.LineItem) (*big.Rat, *int64, error) {
	if line.Quantity == nil {
		return nil, nil, nil
	}
	value, err := decimalRat(*line.Quantity)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: line quantity: %v", ErrInvalidEconomicRevision, err)
	}
	var amount *int64
	if line.RoundedAmount != nil && line.RoundedAmount.Present {
		amount = &line.RoundedAmount.NanoUnits
	}
	return value, amount, nil
}

// joinComparisonItems joins both planes on component identity in stable key
// order. Every key from either plane appears exactly once.
func joinComparisonItems(local, provider comparisonPlane) []comparisonItem {
	keys := make(map[string]struct{}, len(local)+len(provider))
	for key := range local {
		keys[key] = struct{}{}
	}
	for key := range provider {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	items := make([]comparisonItem, 0, len(ordered))
	for _, key := range ordered {
		left, hasLeft := local[key]
		right, hasRight := provider[key]
		items = append(items, joinComparisonKey(key, left, hasLeft, right, hasRight))
	}
	return items
}

// summarizeComparisonItems derives the aggregate outcome mirroring the
// production quantity summary: conflict, then incomparable, then partial or
// missing evidence, then proven discrepancy, and only then agreement. A
// proven discrepancy is complete evidence; an empty comparison is partial.
func summarizeComparisonItems(items []comparisonItem) (ReconciliationComparisonStatus, bool) {
	if len(items) == 0 {
		return ReconciliationStatusPartial, false
	}
	var hasConflict, hasIncomparable, hasPartial, hasMissing, hasDiscrepant bool
	for _, item := range items {
		switch ReconciliationComparisonStatus(item.Status) {
		case ReconciliationStatusConflict:
			hasConflict = true
		case ReconciliationStatusIncomparable:
			hasIncomparable = true
		case ReconciliationStatusPartial:
			hasPartial = true
		case ReconciliationStatusMissingLocal, ReconciliationStatusMissingProvider:
			hasMissing = true
		case ReconciliationStatusDiscrepant:
			hasDiscrepant = true
		}
	}
	switch {
	case hasConflict:
		return ReconciliationStatusConflict, false
	case hasIncomparable:
		return ReconciliationStatusIncomparable, false
	case hasPartial || hasMissing:
		return ReconciliationStatusPartial, false
	case hasDiscrepant:
		return ReconciliationStatusDiscrepant, true
	default:
		return ReconciliationStatusMatched, true
	}
}

// joinComparisonKey classifies one component following the production pair
// semantics: exact matches, exact provider-minus-local deltas, partial
// unavailable values, missing-side markers, and conflicting duplicates.
// Nothing is coerced to zero. Conflicting sides never expose
// quantity/literal/amount remnants: comparisonSide suppresses conflicted
// entries, so only the clean side (if any) contributes quantities to a
// conflict envelope. The persisted envelope is therefore canonical
// regardless of the encounter order that produced the conflict.
func joinComparisonKey(key string, left *comparisonQuantity, hasLeft bool, right *comparisonQuantity, hasRight bool) comparisonItem {
	item := comparisonItem{Component: key}
	leftValue, leftLiteral, leftAmount := comparisonSide(left, hasLeft)
	rightValue, rightLiteral, rightAmount := comparisonSide(right, hasRight)
	if leftValue != nil {
		item.LocalQuantity = &leftLiteral
		item.LocalAmountNano = leftAmount
	}
	if rightValue != nil {
		item.ProviderQuantity = &rightLiteral
		item.ProviderAmountNano = rightAmount
	}
	leftConflict := hasLeft && left != nil && left.conflict
	rightConflict := hasRight && right != nil && right.conflict
	if leftConflict || rightConflict {
		// Defensive canonicalization: comparisonSide already suppresses
		// conflicted sides, but never let a remnant leak if a future merge
		// path retains one. Clean sides stay visible.
		if leftConflict {
			item.LocalQuantity = nil
			item.LocalAmountNano = nil
		}
		if rightConflict {
			item.ProviderQuantity = nil
			item.ProviderAmountNano = nil
		}
		item.SignedDelta = nil
		item.AbsoluteDelta = nil
		item.Status = string(ReconciliationStatusConflict)
		if leftConflict && rightConflict {
			item.Reason = string(ReconciliationReasonConflictingLocal)
		} else if leftConflict {
			item.Reason = string(ReconciliationReasonConflictingLocal)
		} else {
			item.Reason = string(ReconciliationReasonConflictingProvider)
		}
		return item
	}
	switch {
	case hasLeft && hasRight && leftValue != nil && rightValue != nil && leftValue.Cmp(rightValue) == 0:
		item.Status = string(ReconciliationStatusMatched)
	case hasLeft && hasRight && leftValue != nil && rightValue != nil:
		delta := new(big.Rat).Sub(rightValue, leftValue)
		absolute := new(big.Rat).Abs(delta)
		signed, absoluteLiteral := ratDecimal(delta), ratDecimal(absolute)
		item.Status = string(ReconciliationStatusDiscrepant)
		item.SignedDelta = &signed
		item.AbsoluteDelta = &absoluteLiteral
	case hasLeft && hasRight:
		item.Status = string(ReconciliationStatusPartial)
		item.Reason = string(ReconciliationReasonValueUnavailableBoth)
	case hasLeft:
		if leftValue != nil {
			item.Status = string(ReconciliationStatusMissingProvider)
		} else {
			item.Status = string(ReconciliationStatusPartial)
			item.Reason = string(ReconciliationReasonValueUnavailableLocal)
		}
	default:
		if rightValue != nil {
			item.Status = string(ReconciliationStatusMissingLocal)
		} else {
			item.Status = string(ReconciliationStatusPartial)
			item.Reason = string(ReconciliationReasonValueUnavailableProvider)
		}
	}
	return item
}

// comparisonSide normalizes one plane entry: absent keys, missing values,
// conflicting duplicates and empty values all yield no value, distinguished
// by presence and conflict flags for reason selection. Suppressing conflicts
// here guarantees no order-dependent quantity/literal/amount remnant can
// reach the persisted envelope even if a future merge path retained one.
func comparisonSide(entry *comparisonQuantity, present bool) (*big.Rat, string, *int64) {
	if !present || entry == nil || entry.missing || entry.conflict || entry.value == nil {
		return nil, "", nil
	}
	return entry.value, entry.literal, entry.amount
}

// comparisonEnvelope renders the canonical result envelope for one revision
// work item. Envelope identity (ID, version, input hash, time) is assigned by
// the surrounding worker normalization, not invented here.
func comparisonEnvelope(work EconomicRevisionWork, status ReconciliationComparisonStatus, reason string, items []comparisonItem, complete bool) (*EconomicReconciliation, error) {
	if items == nil {
		items = []comparisonItem{}
	}
	payload, err := json.Marshal(comparisonResult{
		Status: string(status), Complete: complete, Reason: reason, Items: items,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: comparison result: %v", ErrInvalidEconomicRevision, err)
	}
	return &EconomicReconciliation{
		Subject: work.Subject, Scope: work.Input.Scope, Basis: work.Input.Basis,
		InputSetHash: work.InputSetHash, ResultJSON: payload,
	}, nil
}

// decimalRat converts an exact bounded decimal to a rational without float
// intermediaries. Unparseable values fail closed.
func decimalRat(value metering.Decimal) (*big.Rat, error) {
	normalized, err := value.Normalize()
	if err != nil {
		return nil, err
	}
	coefficient := new(big.Int)
	if _, ok := coefficient.SetString(normalized.Coefficient, 10); !ok {
		return nil, fmt.Errorf("invalid coefficient %q", normalized.Coefficient)
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(normalized.Scale)), nil)
	return new(big.Rat).SetFrac(coefficient, denominator), nil
}

// ratDecimalValue renders an already-normalized exact decimal in canonical
// value form used for stable comparison literals.
func ratDecimalValue(normalized metering.Decimal) string {
	coefficient := new(big.Int)
	if _, ok := coefficient.SetString(normalized.Coefficient, 10); !ok {
		return normalized.CanonicalString()
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(normalized.Scale)), nil)
	return ratDecimal(new(big.Rat).SetFrac(coefficient, denominator))
}

// ratDecimal renders an exact rational in canonical decimal form: integers
// without a denominator, exact finite decimals expanded, and repeating
// rationals as numerator/denominator. No float rounding is ever applied.
func ratDecimal(value *big.Rat) string {
	if value.IsInt() {
		return value.Num().String()
	}
	denominator := new(big.Int).Set(value.Denom())
	factors := map[int64]int{}
	for _, prime := range []int64{2, 5} {
		zero := new(big.Int)
		for {
			quotient, remainder := new(big.Int).QuoRem(denominator, big.NewInt(prime), new(big.Int))
			if remainder.Cmp(zero) != 0 {
				break
			}
			denominator.Set(quotient)
			factors[prime]++
		}
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return value.RatString()
	}
	places := max(factors[5], factors[2])
	return value.FloatString(int(places))
}
