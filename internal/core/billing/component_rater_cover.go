package billing

// COMPLETE-COVER RESOLUTION. This file owns the four verdicts on one declared
// complete coverage (represented, summed, resolved, ambiguous), the recursive
// least-fixpoint cover proof and its four classifications, the tri-state
// restatement the overlap resolver reads, the commercial overlay that decides
// whether a cover actually BILLS its share, the recursive paid-contributor
// resolution, and the narrowing that keeps an incomplete partition inside the
// classification whenever that incompleteness governs the money.
//
// It deliberately owns NO declared topology (it reads the compiled program), NO
// quantity solving and NO line suppression: it answers what a declared complete
// coverage RESOLVES to, while the quantity solver answers what the evidence
// forces and the overlap resolver decides which lines are withheld.

import (
	"errors"
	"math/big"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// chargeCarryingIncompletePartitions narrows the observed parents whose complete
// partition has a missing required member to the ones for which that
// incompleteness actually governs the money in the scope: parents that do not
// themselves bill a positive effective amount.
//
// It is decided here because it is a question about money rather than about
// evidence or rules. payableByScope is precisely the positive-amount-without-error
// signal the overlap graph already computes, so no pricing decision is duplicated
// here and the caller's determinism is unchanged. hiddenDependencyCovers is the
// RULE-DERIVED signal unioned with that amount-based one, never a replacement for
// it: a hidden dependency can only add a parent back into the classification, never
// excuse one, and it is about the missing member rather than about the parent
// merely having a rule, so the deliberate aggregate-only boundary (an OBSERVED
// priced parent, an unreported child with no rule of its own) still rates complete.
func chargeCarryingIncompletePartitions(
	incomplete map[string]map[string]struct{},
	payableByScope map[string]map[string]struct{},
	hiddenDependencyCovers map[string]map[string]struct{},
) map[string]map[string]struct{} {
	if len(incomplete) == 0 {
		return nil
	}
	filtered := make(map[string]map[string]struct{}, len(incomplete))
	for scopeKey, parents := range incomplete {
		payable := payableByScope[scopeKey]
		hidden := hiddenDependencyCovers[scopeKey]
		for parentKey := range parents {
			if _, chargesMoney := payable[parentKey]; chargesMoney {
				if _, hidesMoney := hidden[parentKey]; !hidesMoney {
					continue
				}
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

// completeCoverVerdict is the SINGLE authority on recursive complete-cover
// representation for one reduction scope. Every analysis that needs to know
// whether a declared complete coverage is resolved, and what exact quantity it
// licenses, reads this one value: the interval solver, the conservation proof
// and the overlap resolver. Three analyses answering one physical fact
// independently is how they came to disagree, so there is now one place to
// disagree in.
//
// state, exact and negative are the scope's evidence reading and are shared with
// the solver verbatim. represented is the exact quantity the evidence REPRESENTS
// for a node (decision 10): its own exact evidence, or the exact sum of a
// resolved complete coverage. coverSum is that sum, kept separately so the
// conservation proof can test it against the parent's own reported quantity even
// for a node whose own evidence already determines the value.
type completeCoverVerdict struct {
	// program is the frozen compiled program this reading was taken from. It is
	// carried so every consumer of the verdict reaches node identity through the
	// program's precomputed keyStrings table and its own adjacency, instead of
	// re-deriving either from a canonical string.
	program     *schemaProgram
	state       []quantityEvidenceState
	exact       []*big.Rat
	negative    []bool
	represented []*big.Rat
	coverSum    []*big.Rat
	// summed reports that the node's declared coverage has a computable exact
	// share sum: every REQUIRED member is exactly represented, every absent
	// OPTIONAL member is the schema's edge-local zero, and at least one member
	// actually carries a share. It is a pure arithmetic fact and is deliberately
	// independent of ownership, so a coverage whose member is shared still has a
	// sum and the conservation arithmetic against it is still decided.
	summed []bool
	// resolved is summed AND unambiguous: the coverage may be used to PROVE
	// something, either as an excused parent or as the representation of an
	// absent required member. A shared complete owner withholds the proof and
	// nothing else, which is why the two are separate facts rather than one.
	// ambiguous means the refusal is specifically a shared complete owner, which
	// is a positive claim rather than an absence of one.
	resolved  []bool
	ambiguous []bool
}

// node returns the compiled node id for one canonical key, or -1 when the key
// declares no inclusion relationship and therefore has no cover to resolve.
// Callers use -1 as "this is not a cover participant", never as a verdict.
func (v completeCoverVerdict) node(canonicalKey string) int {
	if id, ok := v.program.byKey[canonicalKey]; ok {
		return id
	}
	return -1
}

// resolutionFor restates one node's cover verdict in the overlap resolver's
// tri-state, so a consumer asking by canonical key cannot re-derive the
// ambiguity question from its own schema scan.
func (v completeCoverVerdict) resolutionFor(canonicalKey string) coverResolution {
	id := v.node(canonicalKey)
	if id < 0 {
		return coverUnresolved
	}
	if v.ambiguous[id] {
		return coverAmbiguous
	}
	if !v.resolved[id] {
		return coverUnresolved
	}
	return coverProven
}

// resolveCompleteCovers reads one reduction scope's reduced evidence and, for
// every compiled node, separates the four verdicts documented on
// completeCoverVerdict -- represented, summed, resolved and ambiguous. Ambiguity is
// SPECIFICALLY a shared complete owner, directly or through one of the node's own
// members, and it propagates upward so a tainted intermediate is never silently
// downgraded to a missing operand at its ancestor. It is also what lets the
// conservation proof, the interval solver and the overlap resolver agree, because
// all three ask this one function instead of deriving the fact three times.
//
// Resolving an absent member through its own coverage is what makes an
// unobserved intermediate behave exactly like the priced partition standing in
// for it: with A -> {B} and B -> {C} where only A and C are reported, B's
// quantity is neither invented nor missing, and A's coverage is decided on the
// same equation either way.
//
// A member the provider reported with UNUSABLE evidence is deliberately NOT
// resolved through its own coverage: the provider said something about that node
// and the reduction could not use it, which is a different fact from never having
// reported it, and it stays unknown. An absent OPTIONAL member never becomes a
// global zero: it contributes zero to THIS cover equation only, and it is not a
// member of the resolved set, so an all-absent-optional cover proves nothing.
// A subset inequality or an interval collapse NEVER creates a represented exact
// value. Only this function does, and only from evidence or from a complete
// coverage whose shares are all known.
//
// Representation flows from complete children to their parent and the compiled
// containment graph is acyclic, so one reverse-topological pass computes every
// node. Node order is the sorted-id topological order and member order is the
// sorted-id compile order, so no map iteration order can reach the result.
func resolveCompleteCovers(program *schemaProgram, items []aggregateMeasure) completeCoverVerdict {
	count := len(program.keyOf)
	verdict := completeCoverVerdict{
		program:     program,
		state:       make([]quantityEvidenceState, count),
		exact:       make([]*big.Rat, count),
		negative:    make([]bool, count),
		represented: make([]*big.Rat, count),
		coverSum:    make([]*big.Rat, count),
		resolved:    make([]bool, count),
		ambiguous:   make([]bool, count),
		summed:      make([]bool, count),
	}
	for _, item := range items {
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		id, ok := program.byKey[key.CanonicalKey()]
		if !ok {
			continue
		}
		if item.complete && item.rat != nil {
			if verdict.state[id] == quantityExact {
				continue
			}
			verdict.state[id] = quantityExact
			if item.rat.Sign() < 0 {
				// Decision 7: an effective reduced ordinary-usage quantity below
				// zero is a structural contradiction. pkg/lipsdk/metering exposes
				// no component-level correction/credit marker for Measure values
				// (only metering.Fact carries FactKindCorrection, which never
				// reaches this reduction), so every negative here is a
				// contradiction; the SDK gap is recorded rather than papered over.
				// The clamped zero is what the cover arithmetic then sees, so a
				// negative member can disprove a parent's conservation exactly
				// as any other wrong share does.
				verdict.negative[id] = true
				verdict.exact[id] = new(big.Rat)
			} else {
				verdict.exact[id] = new(big.Rat).Set(item.rat)
			}
			verdict.represented[id] = new(big.Rat).Set(verdict.exact[id])
			continue
		}
		if verdict.state[id] != quantityExact {
			verdict.state[id] = quantityUnavailable
		}
	}
	for index := len(program.topo) - 1; index >= 0; index-- {
		node := program.topo[index]
		children := program.completeChildren[node]
		if len(children) == 0 {
			continue
		}
		ambiguous := false
		for _, child := range children {
			if program.sharedCompleteOwner[child] || verdict.ambiguous[child] {
				ambiguous = true
				break
			}
		}
		if ambiguous {
			// A declared member shared with another complete parent -- or a
			// member whose OWN coverage is unprovable for that same reason --
			// makes this coverage unPROVABLE: the true owner cannot be chosen.
			// That is a POSITIVE claim about the evidence rather than a missing
			// operand, and it is recorded separately because the two carry
			// different diagnoses -- an ambiguous partition is incomparable, an
			// unknown one is incomplete. It is deliberately NOT a reason to
			// refuse the ARITHMETIC below: the declared shares are physical
			// quantities whoever owns them, so whether they sum to the parent is
			// decided and reported either way, and only the proof of coverage is
			// withheld. The refusal propagates upward so a tainted intermediate
			// reached from an untainted ancestor denies THAT ancestor as
			// ambiguous too, rather than silently downgrading it to a missing
			// operand and losing the distinction.
			verdict.ambiguous[node] = true
		}
		sum, carrying := coverShareSum(program, &verdict, node)
		if !carrying {
			continue
		}
		verdict.summed[node] = true
		verdict.coverSum[node] = sum
		if verdict.state[node] != quantityExact {
			verdict.represented[node] = sum
		}
		if !ambiguous {
			verdict.resolved[node] = true
		}
	}
	return verdict
}

// coverShareSum adds up the declared shares under one complete coverage, and
// reports whether the coverage has an exact share sum at all. The two are one
// function because the sum is only meaningful when it is exact: a REQUIRED
// member with no exactly known share makes the whole equation undecidable, and
// an all-absent-OPTIONAL coverage carries no share to sum. Members are visited
// in compiled (sorted-id) order and the accumulation is a plain rational sum, so
// the result cannot depend on map iteration order.
func coverShareSum(program *schemaProgram, verdict *completeCoverVerdict, node int) (*big.Rat, bool) {
	sum := new(big.Rat)
	carrying := false
	for childIndex, child := range program.completeChildren[node] {
		switch verdict.state[child] {
		case quantityAbsent:
			if program.completeOptional[node][childIndex] {
				// The schema's declared edge-local zero for an absent optional
				// member. It is not a member of this coverage for any other
				// purpose and never becomes a global zero.
				continue
			}
			if !verdict.resolved[child] {
				// An absent REQUIRED member is resolved through its own complete
				// coverage, and through nothing else. A member that declares no
				// coverage of its own, or one the authority refused, has no
				// share: a missing operand stays missing.
				return nil, false
			}
			// The share is derived from that coverage, never invented, and it is
			// the same value the conservation proof and the overlap resolver read.
			carrying = true
		case quantityExact:
			carrying = true
		default:
			// Present but unusable: no comparable share, and deliberately not
			// derivable from its own coverage either. The provider said
			// something about this node and the reduction could not use it, which
			// is a different fact from never having reported it.
			return nil, false
		}
		sum.Add(sum, verdict.represented[child])
	}
	return sum, carrying
}

// coverResolution is the bounded outcome of resolving one complete-partition
// cover in a single reduction scope. It is deliberately a tri-state: only
// coverAmbiguous carries a positive claim, namely that the parent's paid partition
// exists but is unknown, so nothing declared inside the parent may be assumed
// disjoint from it. The other two are safe to treat as "no proven cover".
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
// The proof is structural and explicit, never inferred from component names:
// only partition/aggregate edges with equal economic direction and unit are
// considered, and an ambiguous overlapping partition (a complete child shared by
// two distinct complete parents anywhere in the frozen schema set) stays
// conservatively uncovered.
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
// A present but unpriced declared member may still be accounted for recursively,
// through its own proven complete child partition: a nested child-only tariff
// (A -> {B, C}, B -> {X, Y} with A, B, C, X and Y observed and only X, Y, C
// priced) conserves exactly and is billed by X + Y + C, so B and then A are
// genuinely covered. The proof is therefore a least fixpoint over the same scope,
// iterated to stability, which is independent of the order in which the schema
// declares its relationships. Only conservation-proven members enter the
// fixpoint, so an observed quantity is never treated as covered unless its
// physical partition was already proven, and a tainted parent never enters it.
//
// That recursion runs through DECLARED structure, which is why the absent
// intermediate above is accounted for through the coverage the shared authority
// resolved for it, and refused when it resolved none. The function returns four
// per-scope sets. covered names the parents whose conserved, recursively billable
// partition may excuse an unpriced parent's missing rule. contradicted names the
// parents whose structurally eligible partition fails the exact conservation test;
// that is an independent classification, because a complete zero parent or an
// informational parent is skipped from line emission by the rating loop and
// therefore never produces a missing-rate diagnostic that could carry the failure
// (Requirement 3.5: expose partial/incomparable evidence rather than invent a
// residual). incomparable names the OBSERVED parents whose structurally eligible
// partition cannot be trusted because a declared child is shared with another
// complete parent: their sum is not comparable, so the enclosing valuation is
// classified partial/incomparable rather than an arithmetic contradiction.
// incomplete names the OBSERVED parents whose partition is not comparable at all
// because a REQUIRED declared member has no exactly known share: it is absent and
// has no resolved coverage of its own, or it was reported with unusable evidence.
// A complete declared partition is usable only when its required members are
// known: a missing operand stays missing and the residual is never invented. An
// absent REQUIRED member that the shared cover authority DID resolve is NOT
// missing -- it is represented by its own coverage, so the parent's conservation
// is decided on the same equation the un-subdivided declaration would use. The
// child's sum is then partial, so this classification deliberately precedes every
// arithmetic one.
//
// Whether that incompleteness is commercially load-bearing is NOT decided here.
// This function sees quantities and rule resolution, not money: a resolving
// rule can still evaluate to a zero amount, so rule existence is not evidence
// that the parent line carries the commercial basis. The caller narrows the set
// to the parents that do not themselves bill a positive effective amount, which
// is the only case where the children are the surviving money and an unknown
// partition would otherwise certify it as complete. An absent OPTIONAL member is
// never incomplete: the frozen schema declares its own zero. A contradicted,
// incomparable or incomplete parent is never covered.
//
// Ambiguity isolation is per parent: a shared child only taints the specific
// parents that declare it, never unrelated cover proofs, and an unreported tainted
// parent contributes nothing here. The structural and the commercial questions
// are kept apart throughout, so one declared world cannot produce two different
// structural diagnoses under two different tariffs.
func (r *ReferenceRater) completeChildPartitionCoverage(aggregates []aggregateMeasure, qualifiers []metering.Dimension) (map[string]map[string]struct{}, map[string]map[string]struct{}, map[string]map[string]struct{}, map[string]map[string]struct{}) {
	var covered, contradicted, incomparable, incomplete map[string]map[string]struct{}
	if r == nil || len(r.program.keyOf) == 0 || len(aggregates) == 0 {
		return nil, nil, nil, nil
	}
	// The declared complete-coverage members are read off the COMPILED program,
	// not off a second scan of the snapshot's relationships, so the conservation
	// proof, the interval solver and the overlap resolver all see exactly the
	// same declared members, deduplicated and restricted to equal direction and
	// unit. The compiled coverParents list is already in sorted canonical-key
	// order, so the reported classifications and the fixpoint iteration order are
	// deterministic without a per-scope rebuild and re-sort.
	program := &r.program
	if len(program.coverParents) == 0 {
		return nil, nil, nil, nil
	}
	byScope := make(map[string][]aggregateMeasure)
	present := make(map[string]map[string]struct{})
	for _, item := range aggregates {
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		byScope[item.scopeKey] = append(byScope[item.scopeKey], item)
		if present[item.scopeKey] == nil {
			present[item.scopeKey] = make(map[string]struct{})
		}
		present[item.scopeKey][key.CanonicalKey()] = struct{}{}
	}
	// partitionEvidence is the per-(scope, parent) structural evidence one
	// partition classification needs. Arithmetic evidence (comparable,
	// childSum) is deliberately independent of billability (unpriced): every
	// represented declared child contributes its exact quantity to the
	// sum whether or not it owns a resolving rule, because conservation is a
	// property of the quantities alone and an unpriced zero child is
	// economically irrelevant yet can still disprove the partition sum.
	// unpriced then names the declared children that have no rule of their own,
	// so each of them can only be accounted for recursively through its own
	// proven complete partition.
	type partitionEvidence struct {
		parentKey        string
		anyMemberPresent bool
		// reported is set when the provider reported this parent in this exact
		// scope. Only a reported parent receives a classification of its own; an
		// unreported one contributes its declared coverage to its ancestors.
		reported bool
		// coverSummed is the shared authority's answer for this parent's own
		// declared coverage, and childSum is the exact share sum that answer
		// licenses. A false coverSummed means neither is meaningful, so
		// childSum stays a zero rational and no arithmetic reads it. It is
		// deliberately the ARITHMETIC half, not the proof half: a coverage whose
		// member is shared still has a computable sum, and that sum is still
		// compared against the parent's own quantity, because whether declared
		// shares add up is a physical fact and not a commercial one.
		coverSummed bool
		// coverProven is the PROOF half: the coverage is summed AND no declared
		// member is ambiguously owned, so it may excuse a parent and may stand
		// in for an absent required member.
		coverProven bool
		// missingMember is set when the shared cover authority refused this
		// parent's declared coverage: a REQUIRED declared member with no
		// exactly known share, or a member the provider reported with unusable
		// evidence. The partition is then not evaluable at all, which is
		// neither a contradiction nor an ambiguity but plainly incomplete
		// evidence. An ABSENT REQUIRED member whose own complete coverage the
		// authority resolved is NOT missing: it is represented by that coverage.
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
	// Sorted scope order keeps the reported classifications and the fixpoint
	// iteration order deterministic across runs and map iteration.
	scopes := make([]string, 0, len(present))
	for scope := range present {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	for _, scopeKey := range scopes {
		cover := resolveCompleteCovers(program, byScope[scopeKey])
		evidence := make([]partitionEvidence, 0, len(program.coverParents))
		for _, parentID := range program.coverParents {
			parentKey := program.keyStrings[parentID]
			ev := partitionEvidence{
				parentKey:   parentKey,
				childSum:    new(big.Rat),
				reported:    cover.state[parentID] != quantityAbsent,
				tainted:     cover.ambiguous[parentID],
				coverSummed: cover.summed[parentID],
				coverProven: cover.resolved[parentID],
			}
			// allOptionalZero is the schema's own declared set of zeros: every
			// declared member is absent AND optional. Such a cover proves
			// nothing, which is deliberately different from an absent REQUIRED
			// member, and the authority refuses both.
			allOptionalZero := true
			for _, child := range program.membersByParent[parentKey] {
				absent := cover.state[child.id] == quantityAbsent
				if !absent {
					ev.anyMemberPresent = true
				}
				if !absent || !child.optional {
					allOptionalZero = false
				}
				if absent && child.optional {
					// The declared edge-local zero needs no biller and is not a
					// member of the cover for any other purpose.
					continue
				}
				if _, ruleErr := r.resolveRule(child.key, qualifiers); ruleErr != nil {
					ev.unpriced = append(ev.unpriced, program.keyStrings[child.id])
				}
			}
			ev.missingMember = !allOptionalZero && !ev.coverSummed
			if ev.coverSummed {
				ev.childSum = cover.coverSum[parentID]
			}
			if ev.parentUsable = cover.state[parentID] == quantityExact; ev.parentUsable {
				// The parent's own exact, comparable effective measure in this
				// scope: the only quantity conservation can be tested against.
				ev.parentRat = cover.exact[parentID]
			}
			evidence = append(evidence, ev)
		}
		// Least fixpoint of the recursive cover proof, iterated to stability so
		// the result does not depend on relationship declaration order. A
		// declared parent enters it when the shared authority resolved its
		// coverage, it declares no shared member, and every member bills its
		// share directly or through its own proven complete cover. Conservation
		// is additionally required for an OBSERVED parent, which is the only one
		// with a reported quantity to conserve; an unobserved parent has nothing
		// to conserve and contributes only its own billable coverage, which is
		// exactly what lets a nested child-only tariff (A -> {B, C},
		// B -> {X, Y}, only X, Y, C priced and B never reported) cover A.
		scopeCovered := make(map[string]struct{}, len(evidence))
		for grown := true; grown; {
			grown = false
			for _, ev := range evidence {
				if _, already := scopeCovered[ev.parentKey]; already {
					continue
				}
				if ev.tainted || ev.missingMember || !ev.coverProven {
					continue
				}
				if ev.parentUsable && ev.parentRat.Cmp(ev.childSum) != 0 {
					continue
				}
				if !billable(ev, scopeCovered) {
					continue
				}
				scopeCovered[ev.parentKey] = struct{}{}
				grown = true
			}
		}
		markPartition := func(set *map[string]map[string]struct{}, scope, parentKey string) {
			if *set == nil {
				*set = make(map[string]map[string]struct{})
			}
			if (*set)[scope] == nil {
				(*set)[scope] = make(map[string]struct{})
			}
			(*set)[scope][parentKey] = struct{}{}
		}
		for _, ev := range evidence {
			if !ev.reported {
				// An unreported parent has no quantity to conserve and no
				// classification of its own; its declared structure is still
				// available to the overlap resolver and to its own ancestors as
				// a cover.
				continue
			}
			switch {
			case ev.missingMember:
				// The shared cover authority refused this declared complete
				// coverage: a REQUIRED member has no exactly known share, or a
				// member the provider reported is unusable. That is incomplete
				// evidence, full stop. The child's sum is partial, so neither
				// conservation nor shared-child ambiguity can be asserted from
				// it, and a missing operand stays missing rather than being
				// back-filled with a zero.
				//
				// This deliberately precedes every other case, including the
				// no-member-present one: a partition whose members are ALL absent
				// proves nothing only when every absent member is OPTIONAL and so
				// carries the schema's declared zero. An all-absent REQUIRED member
				// whose own coverage the authority could NOT resolve is missing
				// evidence like any other, and skipping it would let a partition
				// whose only declared child was never reported certify
				// children-only money as complete.
				//
				// Whether that incompleteness is commercially load-bearing is
				// decided by the caller against the parent's own effective
				// charge, never against rule existence: a resolving rule can
				// still evaluate to a zero amount, and a parent that charges
				// nothing leaves its children as the only money in the scope.
				// Deciding it here would require pricing, and a rule that
				// resolves is not a commercial basis.
				markPartition(&incomplete, scopeKey, ev.parentKey)
			case !ev.anyMemberPresent && !ev.coverSummed:
				// Every declared member is absent and OPTIONAL, so the partition
				// is the schema's declared set of zeros and proves nothing. The
				// shared authority refused this coverage, which is what makes
				// this case the declared-zero one rather than an absent REQUIRED
				// member that the authority DID resolve: a cover carried by an
				// unreported intermediate reaches the classification below like
				// any other conserved coverage.
			case !ev.parentUsable:
				// The parent's own quantity is not a complete comparable
				// measure, so no conservation claim can be made. Its own
				// quantity-incomplete diagnostic carries the failure.
			case ev.tainted:
				// This observed parent declares a member that another complete
				// parent also declares. That is the STRUCTURAL verdict, and it
				// is decided here, once, from the declared ownership alone:
				// the coverage's membership cannot be attributed to a single
				// unambiguous owner, so its child sum is not comparable and no
				// arithmetic verdict may be asserted from it. The valuation is
				// partial and the independent member lines stay payable.
				//
				// Nothing commercial is consulted here, which is the whole
				// point: the structural diagnosis is price-independent, so one
				// declared world cannot yield partition_incomparable under a
				// paid tariff and partition_contradiction under an unpriced
				// one. A genuine ambiguity is still never filed as an
				// arithmetic contradiction, an unpriced required child still
				// never lets child-only money look complete, and an
				// unreported tainted parent still contributes nothing.
				markPartition(&incomparable, scopeKey, ev.parentKey)
			case ev.parentRat.Cmp(ev.childSum) != 0:
				// The declared complete partition is contradicted by the
				// effective reduced quantities, even when a contributing child
				// is unpriced. Record the bounded contradiction so the
				// enclosing valuation fails closed as partial even when this
				// parent is a zero or informational summary the rating loop
				// would otherwise skip entirely.
				markPartition(&contradicted, scopeKey, ev.parentKey)
			case billable(ev, scopeCovered):
				// Conservation holds and every declared member bills its share,
				// directly or through its own proven complete partition.
				markPartition(&covered, scopeKey, ev.parentKey)
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

// unprovableCoverNeedsDiagnosis reports whether an UNPROVABLE complete-partition
// cover still needs a fail-closed classification after the conservation proof
// has had its say, or whether the repository's deliberate additive-subset policy
// already covers the shape.
//
// An OBSERVED parent with an unprovable cover already carries a partial
// diagnosis, because the conservation proof is the thing that inspects OBSERVED
// parents and classifies a missing required member as incomplete: its payable
// subset money can never settle and stays additive, which is exactly the contract
// the R7 preservation suite pins. An UNOBSERVED parent gets no proof and no
// diagnostic at all, so the additive settlement would otherwise be certified
// complete. That is the real hole, and it is load-bearing exactly when this
// scope's money spans both sides of the unknown partition: some component on the
// parent's COMPLETE-COVERAGE side bills a positive amount, reachable however
// deeply nested, AND a payable descendant of a declared subset child. Reach
// rather than adjacency is what closes it: with A --partition--> {B, C} and
// B --partition--> {X} and A absent, the partition money is X, not B, so a
// direct-child test would find nothing to price. When the subset's money sits in
// a scope with no payable partition-side money at all, there is nothing to
// double-charge, so per-scope isolation is preserved and the subset stays payable.
//
// The partition-side reachability answer is the UNION of two signals that answer
// DIFFERENT questions. The AMOUNT-based one is this function: is the money on the
// unknown partition side of this scope represented here, transitively? It walks
// the compiled program's complete-coverage class, so it is the same relation the
// conservation proof walks. It is blind by construction to an ABSENT member,
// which has no measure, so no line, so no amount, whatever its tariff says, and
// the RULE-DERIVED answer to that blind spot lives in
// missingRequiredMemberDependency. Neither replaces the other: a member that
// already bills stays visible here, and a member this call never rated becomes
// visible through its rule. An EXPLICIT FREE rule, and a member with no rule at
// all, stay invisible on purpose, because a component declared free cannot be the
// money a cover is hiding.
func unprovableCoverNeedsDiagnosis(
	program *schemaProgram,
	parentKey string,
	presence map[string]struct{},
	payable map[string]struct{},
) bool {
	if _, parentObserved := presence[parentKey]; parentObserved {
		// The conservation proof already inspected this parent and classified
		// its missing required member, so the shape is diagnosed.
		return false
	}
	parentID, declared := program.byKey[parentKey]
	if !declared {
		return false
	}
	// The AMOUNT-based, per-scope, transitive half of the cover commercial-
	// relevance question, answered by the one compiled walk over the complete-
	// coverage class. Its blind spot is an unobserved member, which has no line
	// and therefore no amount; that blind spot is covered by the rule-derived
	// direct-member signal unioned with it at every call site rather than by
	// widening this walk, which would reach across scopes.
	for node := range program.reachable(completeInclusionEdges, parentID) {
		if _, positive := payable[program.keyStrings[node]]; positive {
			return true
		}
	}
	return false
}

// missingRequiredMemberDependency reports whether one of parent's DIRECTLY
// declared REQUIRED complete-coverage members was never reported with a complete
// comparable quantity anywhere in this call AND could itself have billed money.
// That is the condition under which a parent's own line does NOT demonstrably
// stand in for its cover, so the incompleteness still governs the money.
//
// It is deliberately asked of the DIRECT declared members and gated on a CALL-WIDE
// absence, which is what keeps per-scope isolation honest. A component that is not
// declared inside THIS parent is not part of THIS parent's cover, and a member
// another scope settled is not missing money from this one: reading "absent here"
// as "absent everywhere" would collapse two independent B-leg scopes into one
// false complete-versus-partial argument. Transitive reach of money remains the
// amount signal's job, and it already does it. Only REQUIRED members qualify: an
// absent OPTIONAL member declares the schema's own edge-local zero, which is a
// proven share and never missing money. An absent or unavailable member is
// exactly the case the amount-based signal cannot see, because it never produced
// a line.
//
// Identity comes from the compiled keyStrings table indexed by the member's node
// id, so this per-member question costs a slice index rather than a full
// canonicalisation of the key.
func missingRequiredMemberDependency(
	program *schemaProgram,
	parentKey string,
	knownAnywhere map[string]struct{},
	dependencies *commercialDependencySet,
) bool {
	if dependencies == nil {
		return false
	}
	for _, member := range program.membersByParent[parentKey] {
		if member.optional {
			continue
		}
		if _, known := knownAnywhere[program.keyStrings[member.id]]; known {
			continue
		}
		if dependencies.has(member.key) {
			return true
		}
	}
	return false
}

// billCompleteCover is the COMMERCIAL overlay on the shared cover authority: it
// answers whether every declared member of a complete coverage actually BILLS its
// share in one scope, and collects the canonical keys of every member that does
// (whether its effective charge is positive or exactly zero). A member bills
// directly when its rule resolved under the effective qualifiers (rateable,
// including an explicitly observed zero whose effective charge is zero: a
// zero-valued member still completes the partition without contributing money),
// or through its own proven complete partition (provenPartitions, the proven set
// from completeChildPartitionCoverage), so an observed quantity is never treated
// as covered unless its physical conservation was already proven. A PRESENT,
// COMPLETE, EXACT ZERO member (zeroShare) is the one member that needs no
// pricing rule at all. A present-but-unaccounted member, and a member with no
// usable biller anywhere beneath it, fail the proof.
//
// The QUANTITY half of the question is NOT answered here: whether a declared coverage
// is resolved at all, whether an absent REQUIRED member is represented by its own
// coverage, and whether the refusal is specifically a shared complete owner all come
// from resolveCompleteCovers, the one authority. That split is the whole point, so
// this function can never promote a cover the authority refused, and never refuse one
// for a structural reason the authority accepted. The resolution is a tri-state, not
// a boolean, and it is the authority's:
// coverAmbiguous is a positive claim that the parent's paid partition exists but
// its owner cannot be chosen, so the caller must not treat anything declared
// inside the parent as disjoint from it, while coverUnresolved and coverProven
// are both safe to read as "no cover here". The visiting set bounds the
// recursion and guards a malformed cyclic schema. The declared members and their
// identities both come off the compiled program the verdict carries, so no level
// of the recursion re-reads the snapshot.
func billCompleteCover(
	parentKey string,
	rateable map[string]struct{},
	provenPartitions map[string]struct{},
	present map[string]struct{},
	zeroShare map[string]struct{},
	cover completeCoverVerdict,
	members map[string]struct{},
	visiting map[string]struct{},
) coverResolution {
	program := cover.program
	if resolution := cover.resolutionFor(parentKey); resolution != coverProven {
		return resolution
	}
	if _, cycle := visiting[parentKey]; cycle {
		return coverUnresolved
	}
	visiting[parentKey] = struct{}{}
	defer delete(visiting, parentKey)
	accounted := false
	for _, child := range program.membersByParent[parentKey] {
		childKey := program.keyStrings[child.id]
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
		childPresent := false
		if _, isPresent := present[childKey]; isPresent {
			childPresent = true
		}
		if !childPresent && child.optional {
			// A truly absent optional member contributes the schema's optional
			// zero, which needs no biller.
			continue
		}
		// Either a present member with a proven complete partition, or an
		// absent REQUIRED intermediate whose own coverage the authority already
		// resolved. Both are accounted for through their own declared complete
		// partition, recursively; the absent intermediate has no reported
		// quantity of its own, so its declared members must bill the share.
		if childPresent {
			if _, isProven := provenPartitions[childKey]; !isProven {
				return coverUnresolved
			}
		}
		if len(program.membersByParent[childKey]) == 0 {
			return coverUnresolved
		}
		if resolution := billCompleteCover(childKey, rateable, provenPartitions, present, zeroShare, cover, members, visiting); resolution != coverProven {
			// An ambiguous intermediate makes this ancestor's cover unprovable
			// too, and the caller must learn that it was ambiguity rather than a
			// missing member.
			return resolution
		}
		members[childKey] = struct{}{}
		accounted = true
	}
	if !accounted {
		return coverUnresolved
	}
	return coverProven
}

// recursivePaidContributors collects every component that actually carries a
// POSITIVE amount of one declared complete coverage's money, following the
// coverage downward. A declared member that bills a positive amount of its own
// is a contributor and is not descended through: its own line already carries
// that share, and anything declared inside it is a separate decomposition that
// the direct and transitive payable-overlap checks already govern. A member that
// does NOT bill -- an explicit-free rate, a zero or minimum-charge result, or an
// absent intermediate -- hands its share to whatever bills beneath it, so the
// walk descends through it, and only through a coverage the shared authority has
// resolved. The descent is bounded by the visiting set and every declared member
// list is finite, so a malformed cyclic schema terminates.
//
// This is the same recursive resolution billCompleteCover uses, asked for the
// money instead of the billing: it is what makes a priced descendant reached
// through an explicitly-free intermediate read as the same charge by a redundant
// path rather than an extra one, and what puts that descendant inside the
// suppression set so a rejected conflict never leaves a partial winner behind.
func recursivePaidContributors(
	parentKey string,
	payable map[string]struct{},
	cover completeCoverVerdict,
) map[string]struct{} {
	contributors := make(map[string]struct{})
	collectPaidContributors(parentKey, payable, cover, contributors, map[string]struct{}{})
	return contributors
}

func collectPaidContributors(
	parentKey string,
	payable map[string]struct{},
	cover completeCoverVerdict,
	contributors map[string]struct{},
	visiting map[string]struct{},
) {
	if _, cycle := visiting[parentKey]; cycle {
		return
	}
	visiting[parentKey] = struct{}{}
	defer delete(visiting, parentKey)
	program := cover.program
	for _, child := range program.membersByParent[parentKey] {
		childKey := program.keyStrings[child.id]
		if cover.state[child.id] == quantityAbsent && child.optional {
			// The schema's declared edge-local zero carries no money.
			continue
		}
		if _, positive := payable[childKey]; positive {
			contributors[childKey] = struct{}{}
			continue
		}
		if !cover.resolved[child.id] {
			// Nothing beneath an unresolved coverage can be trusted to be this
			// member's share, so it stays outside both the redundancy test and
			// the suppression rather than being guessed either way.
			continue
		}
		collectPaidContributors(childKey, payable, cover, contributors, visiting)
	}
}
