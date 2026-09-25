package billing

// Frozen component-schema inclusion and complete-partition state machine.
//
// Every helper the reference rater uses to decide whether a declared
// aggregate/partition/subset edge is a payable double charge, and whether a
// declared complete child partition may excuse an unpriced parent, lives
// here. The rating loop in component_rater.go calls into this machine; this
// machine never rates, prices, or emits a line itself. It is one bounded
// state machine with four classifications (covered, contradicted,
// incomparable, incomplete) and one tri-state cover resolution
// (proven, unresolved, ambiguous), and it deliberately owns its own
// traversal so the arithmetic, the ambiguity and the payability signals stay
// readable and reviewable as a unit.

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ErrSchemaOverlapConflict reports a frozen component-schema inclusion or
// partition relationship whose parent aggregate and declared child are both
// priced within the same source/B-leg/work scope and economic direction. The
// rater refuses to emit those overlapping payable lines rather than
// double-charge the same underlying work, while unrelated scopes, unrelated
// components, and independent fixed fees stay payable. Cross-direction,
// transform, and separate-scope edges are not overlaps and remain additive.
var ErrSchemaOverlapConflict = errors.New("billing: frozen schema component overlap is not payable")

// ErrSchemaPartitionContradiction reports a frozen complete child partition
// whose present, complete and rateable declared children do not arithmetically
// account for the present parent's comparable effective reduced quantity in the
// exact same reduction scope. The partition evidence is inconsistent, so the
// rater keeps the independent lines payable but classifies the enclosing
// valuation partial/incomparable instead of suppressing the parent (which would
// make the children-only money look complete) or inventing the residual
// (Requirement 3.5). This classification is independent of the parent's own
// missing-rate diagnostic: a complete zero parent and an informational parent
// are skipped from line emission by the rating loop, yet their contradicted
// partition must still fail the valuation closed.
var ErrSchemaPartitionContradiction = errors.New("billing: frozen schema complete partition contradicts observed quantities")

// ErrSchemaPartitionIncomparable reports an observed aggregate parent whose
// declared complete child partition cannot be proven because at least one
// declared child is also declared under another complete parent (ambiguous
// ownership). The children sum is not comparable in that case, so this is
// deliberately distinct from ErrSchemaPartitionContradiction: the rater exposes
// partial/incomparable evidence (Requirement 3.5) rather than asserting an
// arithmetic contradiction, and never lets the child-only money look complete.
// It is independent of the parent's own rating: a zero or informational parent
// is skipped from line emission, yet its ambiguously shared partition still
// classifies the enclosing valuation partial.
var ErrSchemaPartitionIncomparable = errors.New("billing: frozen schema complete partition has ambiguously shared children")

// chargeCarryingIncompletePartitions narrows the observed parents whose complete
// partition has a missing required member to the ones for which that
// incompleteness actually governs the money in the scope: parents that do not
// themselves bill a positive effective amount.
//
// This is the only correct place to decide it, because it is a question about
// money rather than about evidence or rules. A parent that rates a positive
// amount already carries its partition's charge on its own line, so an
// unreported unpriced child changes nothing about that charge. A parent that
// bills nothing leaves its children as the only money in the scope, and an
// unknown partition must not be allowed to certify those children as a complete
// valuation. The distinction is deliberately NOT "does a rule resolve": a
// resolving rule can evaluate to zero from a zero quantity, an explicit-free
// unit rate, or any tiered/minimum/block shape that produces no charge.
//
// payableByScope is precisely the positive-amount-without-error signal the
// overlap graph already computes, so no pricing decision is duplicated here.
// The result is the same per-scope map shape, so the caller's diagnostic and
// determinism are unchanged.
func chargeCarryingIncompletePartitions(
	incomplete map[string]map[string]struct{},
	payableByScope map[string]map[string]struct{},
) map[string]map[string]struct{} {
	if len(incomplete) == 0 {
		return nil
	}
	filtered := make(map[string]map[string]struct{}, len(incomplete))
	for scopeKey, parents := range incomplete {
		payable := payableByScope[scopeKey]
		for parentKey := range parents {
			if _, chargesMoney := payable[parentKey]; chargesMoney {
				continue
			}
			if filtered[scopeKey] == nil {
				filtered[scopeKey] = make(map[string]struct{}, len(parents))
			}
			filtered[scopeKey][parentKey] = struct{}{}
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// subsetQuantityContradictions returns, per reduction scope, the canonical keys
// of declared subset children whose effective quantity strictly exceeds their
// declared parent's effective quantity in that exact same scope.
//
// A subset is contained in its parent, so subset > parent is unambiguously
// inconsistent evidence. The check is a property of the reduced quantities
// alone, so it deliberately runs on the same consistency evidence as the
// partition conservation proof and never on effective charge positivity: a
// zero-priced, explicit-free, or wholly unpriced parent is exactly the case
// where a monetary overlap graph has already dropped the parent and could hide
// the inconsistency.
//
// Scope rules mirror the partition proof. Only partition-equivalent containment
// edges with equal economic direction and unit are considered, a transform edge
// is a separately governed unit derivation and never a containment, and both
// operands must be present, complete and comparable in the same scope. A
// missing, unavailable, or otherwise unquantified operand is NOT reported here:
// the arithmetic is simply not comparable, the operand stays missing, and the
// existing quantity/missing-rate diagnostics carry that gap.
func (r *ReferenceRater) subsetQuantityContradictions(aggregates []aggregateMeasure) map[string]map[string]struct{} {
	var contradictions map[string]map[string]struct{}
	if r == nil || len(r.snapshot.Schemas) == 0 || len(aggregates) == 0 {
		return nil
	}
	present := make(map[string]map[string]struct{})
	complete := make(map[string]map[string]struct{})
	quantity := make(map[string]map[string]*big.Rat)
	for _, item := range aggregates {
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		if present[item.scopeKey] == nil {
			present[item.scopeKey] = make(map[string]struct{})
			complete[item.scopeKey] = make(map[string]struct{})
			quantity[item.scopeKey] = make(map[string]*big.Rat)
		}
		canonical := key.CanonicalKey()
		present[item.scopeKey][canonical] = struct{}{}
		if item.complete {
			complete[item.scopeKey][canonical] = struct{}{}
		}
		if item.rat != nil {
			quantity[item.scopeKey][canonical] = item.rat
		}
	}
	if len(quantity) == 0 {
		return nil
	}
	for _, schema := range r.snapshot.Schemas {
		for _, relationship := range schema.Relationships {
			// Only a declared subset is a partial containment. An aggregate or
			// partition edge asserts complete coverage and is already proved by
			// the conservation test above; a transform edge is a separately
			// governed unit derivation, never a containment.
			if relationship.Kind != metering.RelationshipSubset {
				continue
			}
			if relationship.Parent.Direction != relationship.Child.Direction || relationship.Parent.Unit != relationship.Child.Unit {
				continue
			}
			parent, parentErr := relationship.Parent.Normalize()
			child, childErr := relationship.Child.Normalize()
			if parentErr != nil || childErr != nil {
				continue
			}
			parentKey := parent.CanonicalKey()
			childKey := child.CanonicalKey()
			for scopeKey, byKey := range present {
				if _, parentPresent := byKey[parentKey]; !parentPresent {
					continue
				}
				if _, childPresent := byKey[childKey]; !childPresent {
					continue
				}
				if _, parentComplete := complete[scopeKey][parentKey]; !parentComplete {
					continue
				}
				if _, childComplete := complete[scopeKey][childKey]; !childComplete {
					continue
				}
				parentRat, hasParentQuantity := quantity[scopeKey][parentKey]
				if !hasParentQuantity {
					continue
				}
				childRat, hasChildQuantity := quantity[scopeKey][childKey]
				if !hasChildQuantity {
					continue
				}
				if childRat.Cmp(parentRat) <= 0 {
					continue
				}
				if contradictions == nil {
					contradictions = make(map[string]map[string]struct{})
				}
				if contradictions[scopeKey] == nil {
					contradictions[scopeKey] = make(map[string]struct{})
				}
				contradictions[scopeKey][childKey] = struct{}{}
			}
		}
	}
	return contradictions
}

// ErrSchemaPartitionIncomplete reports an observed parent whose declared
// complete child partition cannot be evaluated because a REQUIRED declared
// member is absent from the evidence, unavailable, or has no complete
// comparable quantity. A declared complete partition is usable only when its
// required members are known: missing operands stay missing, so the
// children-only money must never be certified complete from an incomplete
// partition.
//
// The sentinel is raised for the parents where that incompleteness governs the
// money, which is decided against the parent's own effective charge and never
// against rule existence: a parent that rates a positive amount already carries
// its partition's charge on its own line, while a parent that rates nothing
// leaves its children as the only money in the scope. A resolving rule is not a
// commercial basis, because it can evaluate to zero from a zero quantity, an
// explicit-free unit rate, or any tiered/minimum/block shape that produces no
// charge. An absent OPTIONAL member is not this condition; the frozen schema
// declares its own optional zero for it.
var ErrSchemaPartitionIncomplete = errors.New("billing: frozen schema complete partition is missing required members")

// ErrSchemaSubsetContradiction reports a frozen subset containment relationship
// whose child quantity strictly exceeds its parent quantity in the exact same
// reduction scope, economic direction and unit, with both quantities present,
// complete and comparable. A subset is contained in its parent, so
// subset > parent is unambiguously inconsistent evidence, and it is
// inconsistent regardless of what either side costs. The rater therefore keeps
// the independent lines payable but classifies the enclosing valuation partial
// rather than certifying a complete money figure from contradictory
// quantities.
//
// The check is a quantity fact, so it runs on the same reduced evidence as the
// partition conservation proof and never on effective charge positivity: a
// zero-priced parent and an absent priced parent are equally unable to hide the
// inconsistency. A missing, unavailable or otherwise unquantified operand is
// NOT this condition: the arithmetic is simply not comparable, the operand
// stays missing, and the existing quantity/missing-rate diagnostics carry it.
var ErrSchemaSubsetContradiction = errors.New("billing: frozen schema subset quantity exceeds its parent quantity")

// inclusionRelationship reports whether a frozen schema relationship declares
// that the parent aggregate already includes the child. Aggregate, subset and
// partition edges all carry same-unit containment semantics. A transform edge
// is a separately governed unit derivation and never an overlap.
func inclusionRelationship(kind metering.RelationshipKind) bool {
	switch kind {
	case metering.RelationshipAggregate, metering.RelationshipSubset, metering.RelationshipPartition:
		return true
	default:
		return false
	}
}

// completeCoverageRelationship reports whether a frozen schema relationship
// declares that the parent is composed of the declared children as a complete
// coverage. An aggregate parent and a partition of the parent both assert that
// the declared children together account for the parent; a subset edge is a
// partial containment and never proves complete coverage, and a transform is a
// separately governed unit derivation.
func completeCoverageRelationship(kind metering.RelationshipKind) bool {
	switch kind {
	case metering.RelationshipAggregate, metering.RelationshipPartition:
		return true
	default:
		return false
	}
}

// partitionMember is one declared child of a complete aggregate/partition
// coverage. An optional member may be absent; when present it must still be
// complete and rateable, so absence stays distinct from a provider zero.
type partitionMember struct {
	key      metering.ComponentKey
	optional bool
}

// coverResolution is the bounded outcome of resolving one complete-partition
// cover in a single reduction scope. It is deliberately a tri-state: only
// coverAmbiguous carries a positive claim, namely that the parent's paid
// partition exists but is unknown, so nothing declared inside the parent may be
// assumed disjoint from it. The two remaining outcomes are both safe to treat
// as "this parent has no proven cover": neither authorizes any money.
type coverResolution uint8

const (
	// coverUnresolved means the declared members are not all accounted for: a
	// missing required member, an unaccounted present member, a member without
	// a declared complete partition, a cycle, or an all-absent optional
	// declaration.
	coverUnresolved coverResolution = iota
	// coverProven means every declared member carries a share, so the parent's
	// complete partition is authoritative for its own quantity.
	coverProven
	// coverAmbiguous means a declared member is shared with another complete
	// parent, so the true owner cannot be chosen and the cover is unprovable.
	coverAmbiguous
)

// completeChildPartitionCoverage returns, per reduction scope, the set of
// present aggregate parent component keys whose frozen complete child partition
// is fully present and complete in that exact same scope and whose declared
// partition structure is unambiguous. A parent in the returned set may have its
// own missing rule excused by the child-only rating because the declared
// children are the authoritative billers for its share.
//
// The proof is structural and explicit: only partition/aggregate edges with
// equal economic direction and unit are considered; a parent is covered only
// when it declares at least one complete child, every non-optional declared
// child is present and complete in the exact same scope, every present optional
// member is complete and rateable, and no complete child is shared by two
// distinct complete parents anywhere in the frozen schema set (an ambiguous
// overlapping partition stays conservatively uncovered). It is never inferred
// from component names.
//
// Presence and quantity completeness are not enough: every child that proves
// the parent's coverage must itself be an eligible biller for the selected
// rating with a resolving rule under the effective qualifiers. A present but
// unselected or unpriced child (for example an informational input_token_total
// that the rating loop skips for lack of a rule) cannot stand in for the
// parent's share; without this the parent would be excused while nothing
// charges its quantity, yielding a false complete valuation.
//
// Finally, the claim is arithmetically checked: the parent's comparable
// effective reduced quantity in the same scope must equal the exact sum of the
// present and complete declared children. An absent optional member contributes
// zero (the schema's own optional-partition semantics), but any present member
// -- optional or explicit zero -- participates in the sum. The arithmetic is
// deliberately independent of rule resolution: a present, complete child
// contributes its exact quantity whether or not it owns a rule, because an
// unpriced zero child is economically irrelevant yet can still disprove
// conservation. Rule resolution gates only covered status, so a conserved sum
// with an unpriced required child is not covered (the parent keeps its own
// diagnostic) but is never misreported as a contradiction.
//
// A present but unpriced declared member may still be accounted for
// recursively, through its own proven complete child partition: a nested
// child-only tariff (A -> {B, C}, B -> {X, Y} with A, B, C, X and Y observed and
// only X, Y, C priced) conserves exactly and is billed by X + Y + C, so B and
// then A are genuinely covered. The proof is therefore a least fixpoint over
// the same scope, iterated to stability, which is independent of the order in
// which the schema declares its relationships. Only conservation-proven
// members enter the fixpoint, so an observed quantity is never treated as
// covered unless its physical partition was already proven, and a tainted
// parent (whose declared children include a shared child) never enters it.
//
// The function returns four per-scope sets. covered names the parents whose
// conserved, recursively billable partition may excuse an unpriced parent's
// missing rule. contradicted names the parents whose structurally eligible
// partition fails the exact conservation test; that is an independent
// classification, because a complete zero parent or an informational parent is
// skipped from line emission by the rating loop and therefore never produces a
// missing-rate diagnostic that could carry the failure (Requirement 3.5:
// expose partial/incomparable evidence rather than invent a residual).
// incomparable names the OBSERVED parents whose structurally eligible
// partition cannot be trusted because a declared child is shared with another
// complete parent: their sum is not comparable, so the enclosing valuation is
// classified partial/incomparable rather than an arithmetic contradiction.
// incomplete names the OBSERVED parents whose partition is not comparable at
// all because a REQUIRED declared member is absent, unavailable, or has no
// complete comparable quantity. A complete declared partition is usable only
// when its required members are known: missing operands stay missing and the
// residual is never invented. The child's sum is then partial, so this
// classification deliberately precedes every arithmetic one.
//
// Whether that incompleteness is commercially load-bearing is NOT decided here.
// This function sees quantities and rule resolution, not money: a resolving
// rule can still evaluate to a zero amount, so rule existence is not evidence
// that the parent line carries the commercial basis. The caller narrows the set
// to the parents that do not themselves bill a positive effective amount,
// which is the only case where the children are the surviving money and an
// unknown partition would otherwise certify it as complete. An absent OPTIONAL
// member is never incomplete: the frozen schema declares its own zero. A
// contradicted, incomparable or incomplete parent is never covered.
//
// Ambiguity isolation is per parent: a shared child only taints the specific
// parents that declare it, never unrelated cover proofs. An independent
// unambiguous partition elsewhere in the same schema set therefore still
// contributes its bounded arithmetic contradiction, and an unobserved tainted
// parent (whose schema is merely declared but never observed) contributes
// nothing here.
func (r *ReferenceRater) completeChildPartitionCoverage(aggregates []aggregateMeasure, qualifiers []metering.Dimension) (map[string]map[string]struct{}, map[string]map[string]struct{}, map[string]map[string]struct{}, map[string]map[string]struct{}) {
	var covered, contradicted, incomparable, incomplete map[string]map[string]struct{}
	if r == nil || len(r.snapshot.Schemas) == 0 || len(aggregates) == 0 {
		return nil, nil, nil, nil
	}
	childrenByParent := make(map[string][]partitionMember)
	parentByChild := make(map[string]string)
	sharedChild := make(map[string]struct{})
	for _, schema := range r.snapshot.Schemas {
		for _, relationship := range schema.Relationships {
			if !completeCoverageRelationship(relationship.Kind) {
				continue
			}
			if relationship.Parent.Direction != relationship.Child.Direction || relationship.Parent.Unit != relationship.Child.Unit {
				continue
			}
			parent, parentErr := relationship.Parent.Normalize()
			child, childErr := relationship.Child.Normalize()
			if parentErr != nil || childErr != nil {
				continue
			}
			parentKey := parent.CanonicalKey()
			childKey := child.CanonicalKey()
			childrenByParent[parentKey] = append(childrenByParent[parentKey], partitionMember{key: child, optional: relationship.Optional})
			if prior, ok := parentByChild[childKey]; ok && prior != parentKey {
				sharedChild[childKey] = struct{}{}
			}
			parentByChild[childKey] = parentKey
		}
	}
	if len(childrenByParent) == 0 {
		return nil, nil, nil, nil
	}
	// A child shared by two distinct complete parents makes every relationship
	// that declares it untrustworthy: the true owner cannot be chosen, so no
	// coverage may be proven from it. Isolation is per parent, not global:
	// taintedParent names the parents that declare a shared child, so an
	// independent unambiguous partition elsewhere keeps its arithmetic
	// contradiction diagnosis.
	taintedParent := make(map[string]bool)
	for parentKey, children := range childrenByParent {
		for _, child := range children {
			if _, shared := sharedChild[child.key.CanonicalKey()]; shared {
				taintedParent[parentKey] = true
				break
			}
		}
	}
	present := make(map[string]map[string]struct{})
	complete := make(map[string]map[string]struct{})
	quantity := make(map[string]map[string]*big.Rat)
	for _, item := range aggregates {
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		if present[item.scopeKey] == nil {
			present[item.scopeKey] = make(map[string]struct{})
			complete[item.scopeKey] = make(map[string]struct{})
			quantity[item.scopeKey] = make(map[string]*big.Rat)
		}
		canonical := key.CanonicalKey()
		present[item.scopeKey][canonical] = struct{}{}
		if item.complete {
			complete[item.scopeKey][canonical] = struct{}{}
		}
		if item.rat != nil {
			quantity[item.scopeKey][canonical] = item.rat
		}
	}
	// partitionEvidence is the per-(scope, parent) structural evidence one
	// partition classification needs. Arithmetic evidence (comparable,
	// childSum) is deliberately independent of billability (unpriced): every
	// present, complete declared child contributes its exact quantity to the
	// sum whether or not it owns a resolving rule, because conservation is a
	// property of the quantities alone and an unpriced zero child is
	// economically irrelevant yet can still disprove the partition sum.
	// unpricedRequired then names the present, complete, comparable children
	// that have no rule of their own, so each of them can only be accounted
	// for recursively through its own proven complete partition.
	type partitionEvidence struct {
		parentKey        string
		anyMemberPresent bool
		// missingMember is set when a REQUIRED declared member is absent from
		// the evidence or has no comparable complete quantity. The partition
		// is then not evaluable at all, which is neither a contradiction nor
		// an ambiguity but plainly incomplete evidence.
		missingMember bool
		// parentUsable is set when the parent's own quantity is a complete,
		// comparable effective measure in this exact scope; only then can
		// conservation be tested.
		parentUsable bool
		parentRat    *big.Rat
		childSum     *big.Rat
		tainted      bool
		unpriced     []string
	}
	// billable reports whether every declared member of this partition
	// actually bills its share: either it has its own resolving rule, or it is
	// itself a proven complete cover in the same scope.
	billable := func(ev partitionEvidence, scopeCovered map[string]struct{}) bool {
		for _, childKey := range ev.unpriced {
			if _, accounted := scopeCovered[childKey]; !accounted {
				return false
			}
		}
		return true
	}
	for scopeKey, byKey := range present {
		// Sorted parent order keeps the reported classifications and the fixpoint
		// iteration order deterministic across runs and map iteration.
		parentKeys := make([]string, 0, len(childrenByParent))
		for parentKey := range childrenByParent {
			parentKeys = append(parentKeys, parentKey)
		}
		sort.Strings(parentKeys)
		observed := make([]partitionEvidence, 0, len(parentKeys))
		for _, parentKey := range parentKeys {
			if _, ok := byKey[parentKey]; !ok {
				// An unobserved parent has no quantity to conserve and no
				// classification of its own; its declared structure is still
				// available to the overlap resolver as a cover.
				continue
			}
			ev := partitionEvidence{
				parentKey: parentKey,
				childSum:  new(big.Rat),
				tainted:   taintedParent[parentKey],
			}
			childSum := ev.childSum
			for _, child := range childrenByParent[parentKey] {
				childKey := child.key.CanonicalKey()
				if _, childPresent := byKey[childKey]; !childPresent {
					if child.optional {
						// An absent optional member contributes zero, exactly
						// the frozen schema's optional-partition semantics, so
						// it preserves comparability.
						continue
					}
					// An absent required member is missing evidence, not an
					// arithmetic contradiction and not an optional zero.
					ev.missingMember = true
					continue
				}
				ev.anyMemberPresent = true
				childRat, comparableChild := quantity[scopeKey][childKey]
				if _, ok := complete[scopeKey][childKey]; !ok || !comparableChild {
					// A present but incomplete/unavailable child, or one with
					// no comparable quantity, has no comparable share.
					ev.missingMember = true
					continue
				}
				childSum.Add(childSum, childRat)
				if _, ruleErr := r.resolveRule(child.key, qualifiers); ruleErr != nil {
					ev.unpriced = append(ev.unpriced, childKey)
				}
			}
			if parentRat, ok := quantity[scopeKey][parentKey]; ok {
				if _, completeParent := complete[scopeKey][parentKey]; completeParent {
					ev.parentUsable = true
					ev.parentRat = parentRat
				}
			}
			observed = append(observed, ev)
		}
		// Least fixpoint of the recursive cover proof, iterated to stability so
		// the result does not depend on relationship declaration order. Only a
		// structurally complete, conserved, untainted partition can enter it.
		scopeCovered := make(map[string]struct{}, len(observed))
		for grown := true; grown; {
			grown = false
			for _, ev := range observed {
				if _, already := scopeCovered[ev.parentKey]; already {
					continue
				}
				if ev.tainted || ev.missingMember || !ev.anyMemberPresent || !ev.parentUsable {
					continue
				}
				if ev.parentRat.Cmp(ev.childSum) != 0 {
					continue
				}
				if !billable(ev, scopeCovered) {
					continue
				}
				scopeCovered[ev.parentKey] = struct{}{}
				grown = true
			}
		}
		for _, ev := range observed {
			switch {
			case !ev.anyMemberPresent:
				// An all-optional declaration with every member absent proves
				// nothing.
			case ev.missingMember:
				// A declared complete partition whose REQUIRED member is not
				// known is incomplete evidence, full stop. The child's sum is
				// partial, so neither conservation nor shared-child ambiguity
				// can be asserted from it, and a missing operand stays missing
				// rather than being back-filled with a zero.
				//
				// Whether that incompleteness is commercially load-bearing is
				// decided by the caller against the parent's own effective
				// charge, never against rule existence: a resolving rule can
				// still evaluate to a zero amount, and a parent that charges
				// nothing leaves its children as the only money in the scope.
				// Deciding it here would require pricing, and a rule that
				// resolves is not a commercial basis.
				if incomplete == nil {
					incomplete = make(map[string]map[string]struct{})
				}
				if incomplete[scopeKey] == nil {
					incomplete[scopeKey] = make(map[string]struct{})
				}
				incomplete[scopeKey][ev.parentKey] = struct{}{}
			case !ev.parentUsable:
				// The parent's own quantity is not a complete comparable
				// measure, so no conservation claim can be made. Its own
				// quantity-incomplete diagnostic carries the failure.
			case ev.tainted:
				// This observed parent declares a child that another complete
				// parent also declares, so its coverage cannot be proven and
				// its child sum is not comparable against a single unambiguous
				// owner. When the full child arithmetic is nonetheless
				// comparable and disagrees with the parent, pricing must not
				// erase that physical inconsistency: record the typed
				// contradiction even though no child is billable-covered (an
				// unpriced required child previously skipped this parent
				// entirely, letting child-only money look complete). When
				// billable coverage does exist, retain the conservative
				// incomparable classification so a genuine ambiguity is never
				// mislabelled an arithmetic contradiction, and an unobserved
				// tainted parent still contributes nothing.
				if !billable(ev, scopeCovered) {
					if ev.parentRat.Cmp(ev.childSum) != 0 {
						if contradicted == nil {
							contradicted = make(map[string]map[string]struct{})
						}
						if contradicted[scopeKey] == nil {
							contradicted[scopeKey] = make(map[string]struct{})
						}
						contradicted[scopeKey][ev.parentKey] = struct{}{}
					}
					continue
				}
				if incomparable == nil {
					incomparable = make(map[string]map[string]struct{})
				}
				if incomparable[scopeKey] == nil {
					incomparable[scopeKey] = make(map[string]struct{})
				}
				incomparable[scopeKey][ev.parentKey] = struct{}{}
			case ev.parentRat.Cmp(ev.childSum) != 0:
				// The declared complete partition is contradicted by the
				// effective reduced quantities, even when a contributing child
				// is unpriced. Record the bounded contradiction so the
				// enclosing valuation fails closed as partial even when this
				// parent is a zero or informational summary the rating loop
				// would otherwise skip entirely.
				if contradicted == nil {
					contradicted = make(map[string]map[string]struct{})
				}
				if contradicted[scopeKey] == nil {
					contradicted[scopeKey] = make(map[string]struct{})
				}
				contradicted[scopeKey][ev.parentKey] = struct{}{}
			case billable(ev, scopeCovered):
				// Conservation holds and every declared member bills its share,
				// directly or through its own proven complete partition.
				if covered == nil {
					covered = make(map[string]map[string]struct{})
				}
				if covered[scopeKey] == nil {
					covered[scopeKey] = make(map[string]struct{})
				}
				covered[scopeKey][ev.parentKey] = struct{}{}
			}
			// Otherwise not contradictory and not billable-covered: a
			// comparable, conserved sum still fails coverage when a required
			// child has no resolving rule. The parent stays uncovered and keeps
			// its own diagnostic rather than being silently excused.
		}
	}
	if len(covered) == 0 {
		covered = nil
	}
	if len(contradicted) == 0 {
		contradicted = nil
	}
	if len(incomparable) == 0 {
		incomparable = nil
	}
	if len(incomplete) == 0 {
		incomplete = nil
	}
	return covered, contradicted, incomparable, incomplete
}

// schemaPartitionDiagnostic pins one deterministic typed diagnostic for every
// (scope, parent) pair in a partition classification set. Iteration is sorted so
// the message is stable across replay.
func schemaPartitionDiagnostic(kind error, partitions map[string]map[string]struct{}) error {
	if len(partitions) == 0 {
		return nil
	}
	scopes := make([]string, 0, len(partitions))
	for scope := range partitions {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	var parts []string
	for _, scope := range scopes {
		keys := make([]string, 0, len(partitions[scope]))
		for key := range partitions[scope] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s in scope %q", key, scope))
		}
	}
	return fmt.Errorf("%w: %s", kind, strings.Join(parts, "; "))
}

// parentCoveredByCompletePartition reports whether one rated aggregate lands on
// a specific scope/component pair whose complete child partition coverage was
// proven.
func parentCoveredByCompletePartition(covered map[string]map[string]struct{}, item aggregateMeasure) bool {
	if len(covered) == 0 {
		return false
	}
	scope := covered[item.scopeKey]
	if len(scope) == 0 {
		return false
	}
	key, keyErr := item.key.Normalize()
	if keyErr != nil {
		return false
	}
	_, ok := scope[key.CanonicalKey()]
	return ok
}

// suppressedCompletePartitionParent reports whether one present aggregate's own
// absent rate is excused by a proven complete child partition: the parent must
// itself be complete (so it is not a quantity-incomplete diagnostic), belong to
// a proven covered (scope, parent) pair, and have no resolving rule of its own
// under the effective qualifiers. Only such a genuinely unpriced, fully covered
// summary is removed from its siblings' economic context; a priced parent keeps
// contributing its own quantity to its whole-context tier. This mirrors the
// line-emission suppression exactly, so context and emitted lines agree.
func (r *ReferenceRater) suppressedCompletePartitionParent(covered map[string]map[string]struct{}, item aggregateMeasure, qualifiers []metering.Dimension) bool {
	if !item.complete || !parentCoveredByCompletePartition(covered, item) {
		return false
	}
	_, ruleErr := r.resolveRule(item.key, qualifiers)
	return errors.Is(ruleErr, ErrRateMissing)
}

// includedChildExclusions returns the frozen-schema-declared included children
// that must not be independently rated in an aggregate-only tariff. A child is
// excluded from arithmetic only when all of the following hold:
//
//   - an explicit inclusion relationship (aggregate/subset/partition) names it
//     as the child, with the parent and child sharing direction and unit;
//   - the parent is an effective, selected, eligible aggregate that actually
//     participates in rating: it is retained by the retail selection mask (when
//     one applies) so a policy-unselected raw measure can never excuse a
//     selected child, it is not an informational total/reasoning summary, and
//     it has a rule that resolves under the effective qualifiers so the
//     aggregate is genuinely priced rather than silently dropped;
//   - the child has no rule of its own (ErrRateMissing), so it could not be
//     billed independently; and
//   - the parent aggregate is present in the exact same reduction scope as the
//     child, so scopes are never conflated.
//
// Informational totals (input_token_total, total_token) are inclusive evidence
// summaries, not disjoint aggregate billers: a rule that happens to price one
// must never silently drop the child's own billable evidence. The effective
// selection mask is the same allowlist the rating arithmetic applies, so a raw
// parent measure that lost its source competition is not proof of a parent
// rating. The excluded child evidence stays retained on its original
// observation (the observation reference is never rewritten), it simply does
// not require a rate and does not contribute charge. Children with their own
// rule are left to the B1 overlap logic, and unrelated components keep their
// existing behavior.
func (r *ReferenceRater) includedChildExclusions(observations []metering.Observation, selected func(metering.Observation) bool, mask retailComponentMask, qualifiers []metering.Dimension) map[string]map[string]struct{} {
	if r == nil || len(r.snapshot.Schemas) == 0 || len(observations) == 0 {
		return nil
	}
	presentByScope := make(map[string]map[string]struct{})
	for _, observation := range observations {
		if selected != nil && !selected(observation) {
			continue
		}
		scopeKey := aggregate.ScopeFor(observation).Key()
		for _, measure := range observation.Measures {
			key, keyErr := measure.Key.Normalize()
			if keyErr != nil {
				continue
			}
			if mask != nil {
				if _, retained := mask[retailObservationMaskKey(observation, key.CanonicalKey())]; !retained {
					continue
				}
			}
			keys := presentByScope[scopeKey]
			if keys == nil {
				keys = make(map[string]struct{})
				presentByScope[scopeKey] = keys
			}
			keys[key.CanonicalKey()] = struct{}{}
		}
	}
	if len(presentByScope) == 0 {
		return nil
	}
	var exclusions map[string]map[string]struct{}
	for _, schema := range r.snapshot.Schemas {
		for _, relationship := range schema.Relationships {
			if !inclusionRelationship(relationship.Kind) {
				continue
			}
			if relationship.Parent.Direction != relationship.Child.Direction || relationship.Parent.Unit != relationship.Child.Unit {
				continue
			}
			parent, parentErr := relationship.Parent.Normalize()
			child, childErr := relationship.Child.Normalize()
			if parentErr != nil || childErr != nil {
				continue
			}
			if isInformationalMeasure(parent) {
				continue
			}
			if _, priced := r.resolveRule(parent, qualifiers); priced != nil {
				continue
			}
			if _, childRule := r.resolveRule(child, qualifiers); !errors.Is(childRule, ErrRateMissing) {
				continue
			}
			for scopeKey, keys := range presentByScope {
				if _, ok := keys[parent.CanonicalKey()]; !ok {
					continue
				}
				if exclusions == nil {
					exclusions = make(map[string]map[string]struct{})
				}
				scope := exclusions[scopeKey]
				if scope == nil {
					scope = make(map[string]struct{})
					exclusions[scopeKey] = scope
				}
				scope[child.CanonicalKey()] = struct{}{}
			}
		}
	}
	return exclusions
}

// exclusionPredicate turns an inclusion exclusion set into the measure-level
// predicate consumed by aggregateMeasures.
func exclusionPredicate(exclusions map[string]map[string]struct{}) func(scopeKey string, key metering.ComponentKey) bool {
	if len(exclusions) == 0 {
		return nil
	}
	return func(scopeKey string, key metering.ComponentKey) bool {
		scope := exclusions[scopeKey]
		if len(scope) == 0 {
			return false
		}
		_, excluded := scope[key.CanonicalKey()]
		return excluded
	}
}

// overlappingSchemaInclusionConflicts reports every frozen inclusion or
// partition edge whose parent and child are both effectively payable in the
// exact same reduction scope and economic direction, and every priced subset
// child that collides with a payable complete partition of its own parent even
// when that parent aggregate is unpriced. The returned conflict set names the
// specific (scope, component) pairs that must not be emitted as payable lines;
// unrelated scopes and unrelated components remain additive. It additionally
// fails closed for a payable component that transitively includes another
// payable component (A subset B subset C) even when the intermediate edge is
// not itself payable. Cross-direction edges, transform edges, and edges whose
// halves land in different scopes are not conflicts. The error is the typed
// fail-closed classification for the
// enclosing valuation and is returned whenever any conflict exists. This is
// driven only by the explicit frozen relationship, never by component names or
// coincidental numbers.
//
// The subset/partition rule is needed because the parent aggregate may itself be
// unpriced by a child-only tariff: a direct parent-child check cannot see the
// overlap, yet the priced subset tokens are already inside the complete
// partition. The frozen schema declares no allocation of the subset into a
// particular partition member, so the rater fails closed rather than guessing a
// member or dropping the subset.
//
// The two sides use different evidence sets. A complete partition is proven
// from rateableByScope: every declared partition child must be observed and
// have a rule that resolves under the effective qualifiers in the same scope,
// but a child whose effective charge is zero (an explicitly observed zero
// quantity) still completes the partition. The subset side uses
// payableByScope: only a subset with a strictly positive effective amount can
// conflict, so a zero-valued subset stays nonconflicting, exactly as a
// zero-valued partition member stays payable-free. An absent or unavailable
// child is in neither set and therefore cannot complete the partition.
//
// Coverage is resolved recursively so a nested, unpriced partition (A -> B with
// B -> {X, Y} priced) still proves the absent parent's cover: a present but
// unpriced member is accounted for through its own evidence-consistent complete
// partition (completePartitionParents), and only then may its priced children
// stand in for the parent's share. consistencyAggregates supplies the full
// effective reduced evidence so an optional member that is present but unpriced
// is not mistaken for an absent one. A priced subset descendant that is already
// one of the parent's actual paid cover contributors is the same charge reached
// by an alternate path, not an extra one; only a positive payable descendant
// outside that contributor set is an additive double charge, and a conflict
// suppresses the extra hits together with every actual paid contributor so no
// partial winner survives.
func (r *ReferenceRater) overlappingSchemaInclusionConflicts(payableByScope map[string]map[string]struct{}, rateableByScope map[string]map[string]struct{}, consistencyAggregates []aggregateMeasure, completePartitionParents map[string]map[string]struct{}) (map[string]map[string]struct{}, map[string]map[string]struct{}, map[string]map[string]struct{}, error) {
	if r == nil || len(r.snapshot.Schemas) == 0 || len(payableByScope) == 0 {
		return nil, nil, nil, nil
	}
	type schemaEdge struct {
		schemaID string
		kind     metering.RelationshipKind
		parent   metering.ComponentKey
		child    metering.ComponentKey
	}
	var edges []schemaEdge
	completeChildrenByParent := make(map[string][]partitionMember)
	subsetChildrenByParent := make(map[string][]metering.ComponentKey)
	parentByCompleteChild := make(map[string]string)
	sharedCompleteChild := make(map[string]struct{})
	for _, schema := range r.snapshot.Schemas {
		for _, relationship := range schema.Relationships {
			if !inclusionRelationship(relationship.Kind) {
				continue
			}
			if relationship.Parent.Direction != relationship.Child.Direction {
				continue
			}
			if relationship.Parent.Unit != relationship.Child.Unit {
				continue
			}
			parent, parentErr := relationship.Parent.Normalize()
			child, childErr := relationship.Child.Normalize()
			if parentErr != nil || childErr != nil {
				continue
			}
			edges = append(edges, schemaEdge{schemaID: schema.ID, kind: relationship.Kind, parent: parent, child: child})
			parentKey := parent.CanonicalKey()
			if relationship.Kind == metering.RelationshipSubset {
				subsetChildrenByParent[parentKey] = appendUniqueComponentKey(subsetChildrenByParent[parentKey], child)
				continue
			}
			// Aggregate and partition edges assert a complete coverage of the
			// parent, so they form the partition side of the subset conflict.
			completeChildrenByParent[parentKey] = append(completeChildrenByParent[parentKey], partitionMember{key: child, optional: relationship.Optional})
			childKey := child.CanonicalKey()
			if prior, ok := parentByCompleteChild[childKey]; ok && prior != parentKey {
				// Collect the full shared-child set so every declaring parent
				// is tainted, not only the last pair encountered.
				sharedCompleteChild[childKey] = struct{}{}
			}
			parentByCompleteChild[childKey] = parentKey
		}
	}
	// An ambiguous complete partition (a child shared by two distinct parents)
	// proves nothing about the coverage of the parents that declare it, but the
	// ambiguity is LOCAL: only those tainted parents are denied, so an unrelated
	// parent's cover proof still stands. A tainted parent is denied at entry to
	// the recursive cover resolver -- including an absent tainted intermediate
	// reached from an untainted ancestor -- so a cover can never silently use a
	// shared child. Denial is a distinct resolution outcome, not an absence of
	// one: an unprovable cover must never be read as permission to treat
	// everything declared inside the parent as disjoint from it.
	taintedCompleteParent := make(map[string]struct{})
	for parentKey, children := range completeChildrenByParent {
		for _, child := range children {
			if _, shared := sharedCompleteChild[child.key.CanonicalKey()]; shared {
				taintedCompleteParent[parentKey] = struct{}{}
				break
			}
		}
	}
	var conflicts map[string]map[string]struct{}
	// ambiguousCovered names the (scope, parent) pairs whose complete cover
	// could not be proven because a declared child is shared with another
	// complete parent, yet the parent is economically relevant: it declares a
	// subset child with a positive payable descendant in that scope. The
	// system cannot prove that descendant is disjoint from the parent's
	// already-paid partition, so the enclosing valuation is classified
	// partial/incomparable instead of silently summing both sides. The set is
	// reported separately from the overlap error so the caller can merge it
	// into the same typed partition-ambiguity classification an observed
	// tainted parent already receives.
	ambiguousCovered := make(map[string]map[string]struct{})
	// unresolvedCovered names the (scope, parent) pairs whose complete cover
	// could not be proven because a required member is missing or an unaccounted
	// member cannot be placed, while the parent is still economically relevant
	// for the same reason. The partition is not known at all, so the enclosing
	// valuation is classified partial/incomplete rather than silently summing a
	// paid partition member together with a paid subset descendant whose
	// disjointness the schema never allocated. This is deliberately distinct from
	// ambiguousCovered: an unknown partition is missing evidence, an ambiguous
	// one is undecidable evidence, and the two carry different diagnoses.
	unresolvedCovered := make(map[string]map[string]struct{})
	var firstErr error
	record := func(scope string, keys ...metering.ComponentKey) {
		if len(keys) == 0 {
			return
		}
		if conflicts == nil {
			conflicts = make(map[string]map[string]struct{})
		}
		scopeConflicts := conflicts[scope]
		if scopeConflicts == nil {
			scopeConflicts = make(map[string]struct{})
			conflicts[scope] = scopeConflicts
		}
		for _, key := range keys {
			scopeConflicts[key.CanonicalKey()] = struct{}{}
		}
	}
	recordAmbiguous := func(scope, parentKey string) {
		scopeSet := ambiguousCovered[scope]
		if scopeSet == nil {
			scopeSet = make(map[string]struct{})
			ambiguousCovered[scope] = scopeSet
		}
		scopeSet[parentKey] = struct{}{}
	}
	recordUnresolved := func(scope, parentKey string) {
		scopeSet := unresolvedCovered[scope]
		if scopeSet == nil {
			scopeSet = make(map[string]struct{})
			unresolvedCovered[scope] = scopeSet
		}
		scopeSet[parentKey] = struct{}{}
	}
	// recorder picks the right denial ledger for the resolution at hand.
	recorder := func(resolution coverResolution) func(scope, parentKey string) {
		if resolution == coverUnresolved {
			return recordUnresolved
		}
		return recordAmbiguous
	}
	for _, edge := range edges {
		for scope, keys := range payableByScope {
			if _, ok := keys[edge.parent.CanonicalKey()]; !ok {
				continue
			}
			if _, ok := keys[edge.child.CanonicalKey()]; !ok {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: schema %q %s relationship prices aggregate parent %s and included child %s in one %q direction %q unit scope",
					ErrSchemaOverlapConflict, edge.schemaID, edge.kind,
					edge.parent.CanonicalKey(), edge.child.CanonicalKey(), string(edge.parent.Direction), edge.parent.Unit)
			}
			record(scope, edge.parent, edge.child)
		}
	}
	// The direct check above inspects one inclusion edge at a time, so a chain
	// A subset B subset C hides a payable A / payable C overlap behind an
	// unpriced (or absent) middle B: the A->B and B->C pairs are not both
	// payable, yet C is declared inside A. Close it with bounded reachability
	// over exactly these inclusion edges (equal direction/unit), scoped the same
	// way; transform, cross-direction and cross-scope edges are not in the graph.
	kids := make(map[string][]metering.ComponentKey)
	keyOf := make(map[string]metering.ComponentKey)
	for _, edge := range edges {
		parentKey, childKey := edge.parent.CanonicalKey(), edge.child.CanonicalKey()
		if parentKey == "" || childKey == "" {
			continue
		}
		kids[parentKey] = appendUniqueComponentKey(kids[parentKey], edge.child)
		keyOf[parentKey], keyOf[childKey] = edge.parent, edge.child
	}
	for scope, payable := range payableByScope {
		for canonical := range payable {
			start, ok := keyOf[canonical]
			if !ok {
				continue
			}
			seen := map[string]struct{}{canonical: {}}
			queue := append([]metering.ComponentKey(nil), kids[canonical]...)
			for len(queue) != 0 {
				current := queue[0]
				queue = queue[1:]
				currentKey := current.CanonicalKey()
				if _, dup := seen[currentKey]; dup {
					continue
				}
				seen[currentKey] = struct{}{}
				if _, both := payable[currentKey]; both {
					if firstErr == nil {
						firstErr = fmt.Errorf("%w: payable component %s transitively includes payable component %s in one %q direction %q unit scope",
							ErrSchemaOverlapConflict, canonical, currentKey, string(start.Direction), start.Unit)
					}
					record(scope, start, current)
				}
				queue = append(queue, kids[currentKey]...)
			}
		}
	}
	// presenceByScope is the full effective reduced evidence per scope. It lets a
	// genuinely absent optional partition member keep the schema's optional zero
	// while a PRESENT but unpriced member is recognized as a real, unaccounted
	// share rather than silently treated as absent.
	presenceByScope := make(map[string]map[string]struct{})
	for _, item := range consistencyAggregates {
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		presence := presenceByScope[item.scopeKey]
		if presence == nil {
			presence = make(map[string]struct{})
			presenceByScope[item.scopeKey] = presence
		}
		presence[key.CanonicalKey()] = struct{}{}
	}
	// zeroShareByScope names the PRESENT, COMPLETE, EXACT ZERO effective
	// quantities from the same reduced/mask-filtered consistency evidence the
	// pricing path already uses. Such a member is a genuine zero share: it
	// accounts for its partition share without a pricing rule, but it must never
	// be invented as a priced line and never counted as a positive contributor.
	// A positive unpriced member, an absent required member, an
	// incomplete/unavailable member, and an unknown-zero member (no comparable
	// quantity) stay out of this set, so they still fail the cover proof.
	zeroShareByScope := make(map[string]map[string]struct{})
	for _, item := range consistencyAggregates {
		if !item.complete || item.rat == nil || item.rat.Sign() != 0 {
			continue
		}
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		zeroShare := zeroShareByScope[item.scopeKey]
		if zeroShare == nil {
			zeroShare = make(map[string]struct{})
			zeroShareByScope[item.scopeKey] = zeroShare
		}
		zeroShare[key.CanonicalKey()] = struct{}{}
	}
	for scope, keys := range rateableByScope {
		parents := make([]string, 0, len(completeChildrenByParent))
		for parentKey := range completeChildrenByParent {
			parents = append(parents, parentKey)
		}
		// Sorted parent order keeps the first conflict diagnostic deterministic.
		sort.Strings(parents)
		payable := payableByScope[scope]
		presence := presenceByScope[scope]
		zeroShare := zeroShareByScope[scope]
		provenPartitions := completePartitionParents[scope]
		for _, parentKey := range parents {
			children := completeChildrenByParent[parentKey]
			if len(children) == 0 {
				continue
			}
			// A subset is contained in the parent whenever its partition is
			// accounted for here. Coverage is resolved recursively: a declared
			// member satisfies its share directly when its rule resolved, or --
			// when it is present but unpriced -- through its own proven complete
			// partition, so an absent parent A with a nested priced cover
			// B = X + Y still has a complete, billable partition. members holds
			// every accounted member so a rejected conflict suppresses the zero
			// and nested members too; contributors is its positive payable
			// subset, the money the cover actually carries.
			members := make(map[string]struct{})
			resolution := collectCompleteCoverMembers(parentKey, completeChildrenByParent, keys, provenPartitions, presence, zeroShare, taintedCompleteParent, members, make(map[string]struct{}))
			if resolution != coverProven {
				// The parent's complete cover could not be proven. Both denial
				// outcomes are positive statements, not absences:
				//
				//   - coverAmbiguous: a declared child is shared with another
				//     complete parent, so the true owner cannot be chosen.
				//   - coverUnresolved: a required member is missing or an
				//     unaccounted member cannot be placed, so the partition is
				//     not known at all.
				//
				// Either way the parent's paid partition is unknown, so nothing
				// declared inside the parent may be assumed disjoint from it and
				// the denial is reported to the caller instead of silently
				// skipping the overlap analysis. The two denials are
				// distinguished because they carry different diagnoses: an
				// ambiguous partition is incomparable, an unknown one is
				// incomplete.
				if resolution == coverUnresolved && !unprovableCoverNeedsDiagnosis(parentKey, presence, payable, children) {
					// An OBSERVED parent already owns a diagnosis: the
					// conservation proof ran on its own quantity, found the
					// missing required member, and classified the valuation
					// partial, so its additive subset money can never settle.
					// That is the repository's deliberate contract, pinned by the
					// R7 preservation suite. An unobserved parent gets no such
					// proof and therefore no such diagnostic, so the unplaceable
					// case below is the only one that would otherwise certify
					// complete additive money.
					continue
				}
				recordUnplaceableSubsetOverlap(scope, parentKey, subsetChildrenByParent[parentKey], kids, payable, recorder(resolution), record)
				continue
			}
			contributors := make(map[string]struct{}, len(members))
			for memberKey := range members {
				if _, positive := payable[memberKey]; positive {
					contributors[memberKey] = struct{}{}
				}
			}
			for _, subset := range subsetChildrenByParent[parentKey] {
				// A direct subset child is the common case; the same rule
				// must also see every payable component transitively included
				// through absent or unpriced intermediate subset children, so
				// traverse the bounded inclusion graph from the parent's subset
				// child and collect all of them. Traversal never starts at the
				// parent, so the complete partition members are not
				// misreported as overlapping with their own cover.
				hits := payableInclusionDescendants(kids, payable, subset)
				// A hit already carrying the parent's own money through the
				// partition cover is the same charge reached by an alternate,
				// redundant path, not an extra one. Only a positive payable
				// descendant outside the actual paid contributor set is an
				// independent additive double charge.
				extra := make([]metering.ComponentKey, 0, len(hits))
				for _, hit := range hits {
					if _, isContributor := contributors[hit.CanonicalKey()]; isContributor {
						continue
					}
					extra = append(extra, hit)
				}
				if len(extra) == 0 {
					continue
				}
				if firstErr == nil {
					descendants := make([]string, 0, len(extra))
					for _, hit := range extra {
						descendants = append(descendants, hit.CanonicalKey())
					}
					sort.Strings(descendants)
					firstErr = fmt.Errorf("%w: complete partition parent %s has payable partition children and priced included subset child %s (payable descendants %s) in one %q direction %q unit scope",
						ErrSchemaOverlapConflict, parentKey, subset.CanonicalKey(), strings.Join(descendants, ","), string(subset.Direction), subset.Unit)
				}
				// Suppress the extra additive hits together with every actual
				// cover member (including a zero-valued or nested member), so a
				// rejected conflict never leaves a partial winner on either
				// side; contributors is used only to subtract a redundant path
				// from the extra-hit set, not to limit suppression.
				record(scope, extra...)
				for _, memberKey := range sortedCanonicalKeys(members) {
					if member, ok := keyOf[memberKey]; ok {
						record(scope, member)
					}
				}
			}
		}
	}
	if len(ambiguousCovered) == 0 {
		ambiguousCovered = nil
	}
	if len(unresolvedCovered) == 0 {
		unresolvedCovered = nil
	}
	return conflicts, ambiguousCovered, unresolvedCovered, firstErr
}

// unprovableCoverNeedsDiagnosis reports whether an UNPROVABLE complete-partition
// cover still needs a fail-closed classification after the conservation proof
// has had its say, or whether the repository's deliberate additive-subset policy
// already covers the shape.
//
// The conservation proof only ever inspects OBSERVED parents: it needs the
// parent's own comparable quantity to assert conservation, and it is the thing
// that classifies a missing required member as incomplete. An observed parent
// with an unprovable cover therefore already carries a partial diagnosis, so its
// payable subset money can never settle and stays additive, which is exactly the
// contract the R7 preservation suite pins.
//
// An UNOBSERVED parent gets no proof and no diagnostic at all, so the additive
// settlement would otherwise be certified complete. That is the real hole, and
// it is only load-bearing when this scope's money actually spans both sides of
// the unknown partition: a payable declared partition member AND a payable
// descendant of a declared subset child. When the subset's money sits in a
// scope with no payable partition member, there is nothing in that scope for it
// to double-charge, so per-scope isolation is preserved and the subset stays
// payable.
func unprovableCoverNeedsDiagnosis(
	parentKey string,
	presence map[string]struct{},
	payable map[string]struct{},
	children []partitionMember,
) bool {
	if _, parentObserved := presence[parentKey]; parentObserved {
		// The conservation proof already inspected this parent and classified
		// its missing required member, so the shape is diagnosed.
		return false
	}
	for _, child := range children {
		if _, positive := payable[child.key.CanonicalKey()]; positive {
			return true
		}
	}
	return false
}

// recordUnplaceableSubsetOverlap fails closed for a complete-partition parent
// whose cover could not be proven, for either denial reason: an ambiguous
// partition (a declared child shared with another complete parent) or an
// unknown one (a required member missing, or an unaccounted member that cannot
// be placed). In both cases the parent's paid partition exists but is unknown,
// so disjointness between it and the parent's declared subset children cannot be
// proven. Any positive payable descendant of such a subset child is therefore
// suppressed and the parent is recorded through the ledger matching the reason.
// Without this the payer's two sides would settle additively as a false
// complete total: "could not prove cover" is not permission to assume that
// everything declared inside the parent lies outside it.
//
// The denial stays LOCAL: a parent that declares no subset child, or whose
// subset children carry no positive payable descendant in this scope, is not
// economically relevant and is left untouched, so an unrelated ambiguous or
// incomplete fragment can never poison an independent valid cover proof.
func recordUnplaceableSubsetOverlap(
	scope, parentKey string,
	subsets []metering.ComponentKey,
	kids map[string][]metering.ComponentKey,
	payable map[string]struct{},
	recorder func(scope, parentKey string),
	record func(scope string, keys ...metering.ComponentKey),
) {
	for _, subset := range subsets {
		hits := payableInclusionDescendants(kids, payable, subset)
		if len(hits) == 0 {
			continue
		}
		recorder(scope, parentKey)
		// Only the unplaceable side is withheld. The partition children keep
		// their own independently payable lines: the enclosing valuation is
		// classified partial, so it can never settle, and the suppressed side is
		// the one whose disjointness could not be proven.
		record(scope, hits...)
	}
}

// collectCompleteCoverMembers resolves whether every declared member of a
// complete partition is accounted for in one scope, collecting the canonical
// keys of every member that actually carries a share (whether its effective
// charge is positive or exactly zero). A member is accounted for directly when
// its rule resolved under the effective qualifiers (rateable, including an
// explicitly observed zero whose effective charge is zero: a zero-valued member
// still completes the partition without contributing money). A PRESENT member
// that is not directly rateable may be accounted for only through its own
// evidence-consistent complete partition (provenPartitions, the proven set from
// completeChildPartitionCoverage), so an observed quantity is never treated as
// covered unless its physical conservation was already proven. A PRESENT,
// COMPLETE, EXACT ZERO member (zeroShare) is the one exception: its zero share
// needs no pricing rule to be accounted for, so it completes the partition
// without a rule while still being unable to contribute money (it is not in the
// positive payable set). A truly absent optional member contributes the schema's
// optional zero and is skipped. A genuinely absent required member -- an
// intermediate aggregate node whose own quantity is not part of the evidence --
// may still be accounted for recursively through its own frozen complete
// partition when every required descendant share resolves to a rateable line: an
// absent aggregate is covered by its complete priced partition without faking
// its quantity. Any other present-but-unaccounted member, or a member with no
// declared complete partition, fails the proof. A positive unpriced member, an
// incomplete/unavailable member, and an unknown-zero member are never in
// zeroShare and still fail. At least one member must actually account for a
// share, so an all-absent optional declaration proves nothing. The visiting set
// bounds the recursion and guards a malformed cyclic schema even though schema
// validation is expected to reject it first.
//
// The resolution is a tri-state, not a boolean. A tainted parent -- one whose
// declared children include a child shared with another complete parent -- is
// refused at entry and the refusal is reported as coverAmbiguous rather than
// silently unresolved, and an ambiguous result reached recursively is
// propagated to the caller. CoverProven and coverUnresolved are both safe to
// treat as "no cover here": neither authorizes anything. CoverAmbiguous is not:
// it states that the parent's paid partition exists but is unknown, so the
// caller must not treat anything declared inside the parent as disjoint from
// it.
func collectCompleteCoverMembers(
	parentKey string,
	childrenByParent map[string][]partitionMember,
	rateable map[string]struct{},
	provenPartitions map[string]struct{},
	present map[string]struct{},
	zeroShare map[string]struct{},
	tainted map[string]struct{},
	members map[string]struct{},
	visiting map[string]struct{},
) coverResolution {
	if _, isTainted := tainted[parentKey]; isTainted {
		// This parent declares a child shared with another complete parent, so
		// its partition cannot prove coverage. Refusing at entry also blocks an
		// absent tainted intermediate reached recursively from an untainted
		// ancestor, so no cover can silently route through a shared child. The
		// refusal propagates: the ancestor is equally unprovable.
		return coverAmbiguous
	}
	if _, cycle := visiting[parentKey]; cycle {
		return coverUnresolved
	}
	visiting[parentKey] = struct{}{}
	defer delete(visiting, parentKey)
	accounted := false
	for _, child := range childrenByParent[parentKey] {
		childKey := child.key.CanonicalKey()
		if _, ok := rateable[childKey]; ok {
			accounted = true
			members[childKey] = struct{}{}
			continue
		}
		if _, isZeroShare := zeroShare[childKey]; isZeroShare {
			// A present, complete, exactly-zero effective quantity accounts for
			// its share without a pricing rule and contributes no money.
			accounted = true
			members[childKey] = struct{}{}
			continue
		}
		if _, isPresent := present[childKey]; isPresent {
			// Present but not directly rateable: only its own proven complete
			// partition can account for the observed share.
			if _, isProven := provenPartitions[childKey]; !isProven {
				return coverUnresolved
			}
		} else if child.optional {
			// A truly absent optional member contributes the schema's
			// optional zero.
			continue
		}
		// Either a present member with a proven complete partition or a
		// missing required intermediate: both are accounted for through their
		// own declared complete partition, recursively. The absent
		// intermediate has no observed quantity to conserve, so its declared
		// children must themselves account for the share.
		if len(childrenByParent[childKey]) == 0 {
			return coverUnresolved
		}
		switch collectCompleteCoverMembers(childKey, childrenByParent, rateable, provenPartitions, present, zeroShare, tainted, members, visiting) {
		case coverProven:
		case coverAmbiguous:
			// A tainted intermediate makes this ancestor's cover unprovable too.
			return coverAmbiguous
		default:
			return coverUnresolved
		}
		members[childKey] = struct{}{}
		accounted = true
	}
	if !accounted {
		return coverUnresolved
	}
	return coverProven
}

// sortedCanonicalKeys returns the canonical keys of a component key set in
// deterministic order for stable conflict reporting and recording.
func sortedCanonicalKeys(keys map[string]struct{}) []string {
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	return sorted
}

// payableInclusionDescendants collects every component reachable from start
// over the bounded same-direction/same-unit inclusion graph whose effective
// amount in the given scope is strictly positive, including start itself. The
// graph is the same kids graph the direct and declared-transitive checks use, so
// cross-direction, cross-unit (transform) and cross-scope edges are already
// excluded. Callers start from a parent's declared subset children rather than
// from the parent, which keeps the parent's complete partition cover out of the
// traversal. The visited set bounds the walk to each reachable component once.
func payableInclusionDescendants(kids map[string][]metering.ComponentKey, payable map[string]struct{}, start metering.ComponentKey) []metering.ComponentKey {
	seen := make(map[string]struct{})
	queue := []metering.ComponentKey{start}
	var hits []metering.ComponentKey
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		currentKey := current.CanonicalKey()
		if _, dup := seen[currentKey]; dup {
			continue
		}
		seen[currentKey] = struct{}{}
		if _, ok := payable[currentKey]; ok {
			hits = append(hits, current)
		}
		queue = append(queue, kids[currentKey]...)
	}
	return hits
}

// appendUniqueComponentKey adds key to keys unless an equal canonical key is
// already present, so a duplicated frozen edge cannot double-list a component.
func appendUniqueComponentKey(keys []metering.ComponentKey, key metering.ComponentKey) []metering.ComponentKey {
	canonical := key.CanonicalKey()
	for _, prior := range keys {
		if prior.CanonicalKey() == canonical {
			return keys
		}
	}
	return append(keys, key)
}

// schemaConflictSuppressed reports whether one rated aggregate lands on a
// specific scope/component pair identified as a frozen-schema overlap conflict.
func schemaConflictSuppressed(conflicts map[string]map[string]struct{}, item aggregateMeasure) bool {
	if len(conflicts) == 0 {
		return false
	}
	scope := conflicts[item.scopeKey]
	if len(scope) == 0 {
		return false
	}
	key, keyErr := item.key.Normalize()
	if keyErr != nil {
		return false
	}
	_, ok := scope[key.CanonicalKey()]
	return ok
}
