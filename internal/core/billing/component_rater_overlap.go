package billing

// OVERLAP, SUPPRESSION AND DIAGNOSTICS. This file owns the typed sentinels and
// the deterministic formatters that quote them, the payable-double-charge
// detection, the suppression and contributor sets that keep a rejected conflict
// from leaving a partial winner, the declared included-child exclusions, and
// the non-blocking unknown-intersection diagnostic.
//
// It deliberately owns NO compile-time topology, NO quantity solving and NO cover
// verdict: every walk here is answered by the compiled program in
// component_rater_schema_program.go and every cover question by
// component_rater_cover.go, so this file decides only which lines are withheld
// and how the failure is reported.

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
// against rule existence. An absent OPTIONAL member is not this condition; the
// frozen schema declares its own optional zero for it.
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
// partition conservation proof and never on effective charge positivity. A
// missing, unavailable or otherwise unquantified operand is NOT this condition:
// the arithmetic is simply not comparable and the existing quantity/missing-rate
// diagnostics carry it. It is raised by the compiled constraint solver for any
// containment violation over the transitive union of subset and
// complete-coverage edges, so a chain that changes edge class partway (A subset
// B, B partition C) is bounded exactly like a pure subset chain.
var (
	ErrSchemaSubsetContradiction = errors.New("billing: frozen schema subset quantity exceeds its parent quantity")

	// ErrSchemaQuantityContradiction reports an effective reduced ordinary-usage
	// component quantity strictly below zero. Every nonzero deviation from the
	// evidence is a structural contradiction: pkg/lipsdk/metering exposes no
	// component-level correction or credit marker on Measure values (only
	// metering.Fact carries FactKindCorrection, which never reaches the reduced
	// component aggregates the rater sees), so the rater cannot distinguish a
	// legitimate credit from corrupt arithmetic. It fails the valuation closed and
	// never silently clamps the quantity to zero. This is the narrow rule named by
	// review decision 7; a future SDK credit marker would be honoured here.
	ErrSchemaQuantityContradiction = errors.New("billing: frozen schema effective quantity is negative")
)

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
// must never silently drop the child's own billable evidence. The excluded child
// evidence stays retained on its original observation; it simply does not require
// a rate and does not contribute charge. Children with their own rule are left to
// the B1 overlap logic.
func (r *ReferenceRater) includedChildExclusions(observations []metering.Observation, selected func(metering.Observation) bool, mask retailComponentMask, qualifiers []metering.Dimension) map[string]map[string]struct{} {
	if r == nil || len(r.program.declaredEdges) == 0 || len(observations) == 0 {
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
	// The declared inclusion edges are read off the COMPILED program: it was
	// built from this rater's own canonicalized snapshot copy, filtered exactly as
	// a per-call scan of the relationships would filter them, with both endpoints
	// already resolved to node ids and both keys already normalized. Only the two
	// per-call questions -- is the parent an informational summary, and does each
	// side resolve a rule under the effective qualifiers -- are asked here, so
	// this loop performs no schema discovery of its own. The result is a set per
	// scope, so the declared edge order is invisible to it.
	var exclusions map[string]map[string]struct{}
	for _, edge := range r.program.declaredEdges {
		parent, child := r.program.keyOf[edge.parent], r.program.keyOf[edge.child]
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
			if _, ok := keys[r.program.keyStrings[edge.parent]]; !ok {
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
			scope[r.program.keyStrings[edge.child]] = struct{}{}
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

// containmentUnknownIntersections reports the decision-8 unknown containment
// intersections in one scope: pairs of strictly positive regions that share a
// containment ancestor, where neither is the ancestor of the other and the
// schema does not place them in distinct branches of a validated complete
// partition. These regions may overlap, but the frozen schema never allocated
// them, so the overlap is unknown.
//
// This is deliberately NON-BLOCKING per the ratified decision: the pairs are
// reported as an auxiliary diagnostic, never used to downgrade completeness and
// never used to change any money. There is no standalone non-monetary diagnostic
// channel on economics.Valuation today; adding one is the named follow-up
// decision. The report is deterministic: the caller visits scopes in sorted order
// and the pair strings are sorted here.
//
// OUTPUT IS PINNED, INCLUDING ITS BYTES. The acceptance vectors quote these pair
// strings exactly, so three things are load-bearing and must not drift with a
// traversal refactor: the SET of pairs (a walk is a set, never a path, so it is
// order independent), the FORMAT (keyStrings[a] + " ~ " + keyStrings[b], with a
// and b in the ascending-node-id order of the positive list, which is ascending
// canonical-key order), and the trailing sort.Strings over the whole list. The
// ancestor and descendant questions are therefore answered by the same
// int-indexed walk as every other question, through the edgeClass knob, and the
// pair SET it returns is unchanged by that consolidation.
func containmentUnknownIntersections(program *schemaProgram, lower []*big.Rat, represented []*big.Rat) []string {
	count := len(program.keyOf)
	var positive []int
	for node := range count {
		if lower[node] != nil && lower[node].Sign() > 0 {
			positive = append(positive, node)
		}
	}
	if len(positive) < 2 {
		return nil
	}
	// Reachability is paid for LAZILY and memoized per node: a set is built only when
	// a node takes part in a candidate pair, so a graph where almost nothing is
	// positive never builds the sets it would discard.
	ancestors := make([]map[int]struct{}, count)
	descendants := make([]map[int]struct{}, count)
	ancestorsOf := func(node int) map[int]struct{} {
		if ancestors[node] == nil {
			ancestors[node] = program.reachable(reverseInclusionEdges, node)
		}
		return ancestors[node]
	}
	descendantsOf := func(node int) map[int]struct{} {
		if descendants[node] == nil {
			descendants[node] = program.reachable(allInclusionEdges, node)
		}
		return descendants[node]
	}
	var pairs []string
	for i := 0; i < len(positive); i++ {
		for j := i + 1; j < len(positive); j++ {
			a, b := positive[i], positive[j]
			ancestorsB := ancestorsOf(b)
			if _, ancestor := ancestorsB[a]; ancestor {
				continue
			}
			if _, descendant := descendantsOf(b)[a]; descendant {
				continue
			}
			sharesAncestor := false
			for ancestor := range ancestorsOf(a) {
				if _, shared := ancestorsB[ancestor]; shared {
					sharesAncestor = true
					break
				}
			}
			if !sharesAncestor {
				continue
			}
			if separatedByValidatedPartition(program, descendantsOf, represented, a, b) {
				continue
			}
			pairs = append(pairs, program.keyStrings[a]+" ~ "+program.keyStrings[b])
		}
	}
	sort.Strings(pairs)
	return pairs
}

// separatedByValidatedPartition reports whether a and b fall into distinct
// complete children of at least one COMPLETE parent whose coverage is exactly
// represented (decision 10), which allocates the two regions. The descendant sets
// come from the caller's LAZY memo, so a branch is walked only once a pair reaches it.
func separatedByValidatedPartition(program *schemaProgram, descendants func(int) map[int]struct{}, represented []*big.Rat, a, b int) bool {
	for parent := 0; parent < len(program.keyOf); parent++ {
		if represented[parent] == nil {
			continue
		}
		children := program.completeChildren[parent]
		if len(children) < 2 {
			continue
		}
		branchA, branchB := -1, -1
		for _, child := range children {
			if _, ok := descendants(child)[a]; ok && branchA == -1 {
				branchA = child
			}
			if _, ok := descendants(child)[b]; ok && branchB == -1 {
				branchB = child
			}
		}
		if branchA != -1 && branchB != -1 && branchA != branchB {
			return true
		}
	}
	return false
}

// unknownContainmentDiagnostic formats the decision-8 unknown-intersection
// report deterministically. It carries no sentinel because it is advisory only:
// it is attached to an already-failing valuation and never changes money.
func unknownContainmentDiagnostic(unknown map[string][]string) error {
	if len(unknown) == 0 {
		return nil
	}
	scopes := make([]string, 0, len(unknown))
	for scope := range unknown {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	var parts []string
	for _, scope := range scopes {
		for _, pair := range unknown[scope] {
			parts = append(parts, fmt.Sprintf("%s in scope %q", pair, scope))
		}
	}
	return fmt.Errorf("billing: unknown containment intersection: %s", strings.Join(parts, "; "))
}

// overlappingSchemaInclusionConflicts reports every pair of effectively payable
// component lines in one reduction scope where one contains the other -- directly
// on a declared inclusion edge or transitively through an unpriced or absent
// middle (A subset B subset C) -- and every priced subset child that collides
// with a payable complete partition of its own parent even when that parent
// aggregate is unpriced. The returned conflict set names the specific
// (scope, component) pairs that must not be emitted as payable lines; unrelated
// scopes and unrelated components remain additive. Cross-direction edges,
// transform edges, and edges whose halves land in different scopes are not
// conflicts. The error is the typed fail-closed classification for the enclosing
// valuation and is returned whenever any conflict exists. This is driven only by
// the explicit frozen relationship, never by component names or coincidental
// numbers. The two sides use different evidence sets. A complete partition is
// proven from rateableByScope: every declared partition child must be observed and
// have a rule that resolves under the effective qualifiers in the same scope, but
// a child whose effective charge is zero still completes the partition. The subset
// side uses payableByScope: only a subset with a strictly positive effective
// amount can conflict, so a zero-valued subset stays nonconflicting, exactly as a
// zero-valued partition member stays payable-free. An absent or unavailable child
// is in neither set and therefore cannot complete the partition.
//
// dependencies is the RULE-DERIVED commercial signal this analysis unions with the
// amount-based one wherever a cover's money has to be located, and is consulted
// only where the amount signal is structurally blind. The function returns five
// sets; hiddenDependencyCovers names the (scope, parent) pairs whose own positive
// line does not demonstrably stand in for the cover because a REQUIRED member of
// that cover could itself have billed money and was never accounted for in this
// scope. The caller uses it to keep such a parent inside the incomplete-partition
// classification instead of excusing it.
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
//
// EVERYTHING DECLARED IS READ FROM THE COMPILED PROGRAM. This function used to
// rebuild, on every call, its own string-keyed copies of the declared edges, the
// subset children, the complete members, the merged kids graph and a canonical-key
// table, and to walk two further ad-hoc adjacency maps of its own. The structural
// half is now decided by the general relation proof in component_rater_support.go,
// which owns the containment closure; only the two COMMERCIAL halves below remain
// local special cases, because they ask questions that proof deliberately cannot.
func (r *ReferenceRater) overlappingSchemaInclusionConflicts(
	payableByScope map[string]map[string]struct{},
	dependencies *commercialDependencySet,
	rateableByScope map[string]map[string]struct{},
	consistencyAggregates []aggregateMeasure,
	completePartitionParents map[string]map[string]struct{}) (
	map[string]map[string]struct{},
	map[string]map[string]struct{},
	map[string]map[string]struct{},
	map[string]map[string]struct{},
	error,
) {
	// The fast path requires there to be no reduced evidence at all, not merely no
	// positive amount: every commercial question this analysis answers used to be an
	// AMOUNT question, but the rule-derived dependency signal is not, so a scope can
	// hold only zero-quantity or explicit-free lines and still hide a REQUIRED
	// member of a declared complete cover that the provider never reported and whose
	// own rule would have billed. A call with nothing rated has no cover to resolve.
	if r == nil || len(r.snapshot.Schemas) == 0 || (len(payableByScope) == 0 && len(rateableByScope) == 0) {
		return nil, nil, nil, nil, nil
	}
	// Everything this analysis knows about DECLARED topology comes from the frozen
	// program: the declared edges in declaration order, the complete-coverage
	// member lists, the subset children, the cover parents and the compiled
	// adjacency every reachability question below is answered from. None of it is
	// rediscovered here, so no walk in this function can disagree with the
	// conservation proof or the interval solver about who contains whom.
	program := &r.program
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
	// hiddenDependencyCovers names the (scope, parent) pairs that bill a positive
	// amount of their own AND still hide a commercial dependency behind their
	// declared complete cover: a REQUIRED member that was never reported with a
	// complete quantity anywhere in this call and whose own rule could have billed
	// money. Such a parent's own line cannot stand in for the cover, so the
	// caller's commercial-relevance narrowing must keep it inside the incomplete
	// classification. It is per scope and per parent, like the other ledgers here,
	// so the caller's determinism is unchanged.
	hiddenDependencyCovers := make(map[string]map[string]struct{})
	recordHiddenDependency := func(scope, parentKey string) {
		parents := hiddenDependencyCovers[scope]
		if parents == nil {
			parents = make(map[string]struct{})
			hiddenDependencyCovers[scope] = parents
		}
		parents[parentKey] = struct{}{}
	}
	var firstErr error
	record := func(scope string, keys ...string) {
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
			scopeConflicts[key] = struct{}{}
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
	// The two scope sets are the per-call evidence, so they arrive as MAPS, and
	// every walk below that records a FIRST error used to range one directly:
	// which scope, and inside a scope which payable component, was quoted first
	// was then decided by Go's randomized map iteration. The three projections
	// below are the deterministic replacements, built once per call. They change
	// no reported SET -- every candidate is still visited and recorded -- they
	// only fix which candidate wins, and the ledgers here are sets, so nothing
	// downstream can observe the difference but the quoted diagnostic.
	payableScopes := sortedScopeKeys(payableByScope)
	rateableScopes := sortedScopeKeys(rateableByScope)
	// evidenceByScope groups the full effective reduced evidence per scope, so the
	// cover resolver is handed exactly the scope it is reasoning about and never
	// evidence from another one.
	evidenceByScope := make(map[string][]aggregateMeasure)
	for _, item := range consistencyAggregates {
		evidenceByScope[item.scopeKey] = append(evidenceByScope[item.scopeKey], item)
	}
	// The cover authority is resolved AT MOST ONCE per scope and shared by the two
	// passes below. A payable line is a rateable line by construction, so every
	// scope the structural pass asks about is one the commercial pass would have
	// resolved anyway: the share removes a duplicated resolution rather than adding
	// one, and it is what stops the two passes from reading the same physical fact
	// twice.
	coverByScope := make(map[string]completeCoverVerdict, len(rateableScopes))
	coverFor := func(scope string) completeCoverVerdict {
		if verdict, resolved := coverByScope[scope]; resolved {
			return verdict
		}
		verdict := resolveCompleteCovers(program, evidenceByScope[scope])
		coverByScope[scope] = verdict
		return verdict
	}
	// STRUCTURAL OVERLAP, decided by the one general relation proof. A pair of
	// positive component lines is a conflict exactly when one contains the other
	// under the compiled containment closure: both then carry money for the same
	// underlying work, and the rating vocabulary has no surcharge to absorb it.
	// This replaces BOTH former special cases, and neither is kept alongside. The
	// direct per-edge both-payable check is SUBSUMED: a declared edge whose two ends
	// are both positive is exactly a definite overlap between two contributors. The
	// transitive payable walk is subsumed too, and is what this pass GENERALIZES: a
	// relation is a statement about two REGIONS, so a chain that changes edge class
	// partway (A subset B, B partition C) behind an unpriced or absent middle is
	// answered by the closure that answers a single declared edge.
	//
	// CANDIDATES COME FROM THE CLOSURE, THE VERDICT COMES FROM THE PREDICATE. The
	// walk reaches every pair the containment closure relates, which is exactly the
	// set the predicate can answer with a conflict, and decides nothing itself; a
	// pair the predicate calls a proven disjointness or an unknown intersection
	// records nothing, both being non-blocking. That is what keeps the enumeration
	// from reintroducing the quadratic cross product it replaced: reachability, not
	// the verdict, decides how many pairs are asked about. The order is the result
	// too -- ascending node id contributors, an ascending candidate list, and only
	// the higher-index side of a pair, the same i < j discipline the cross product
	// used, so each unordered pair is emitted once in the order that decides which
	// conflict is quoted first. candidates is hoisted out of the scope loop so one
	// buffer serves every scope.
	var candidates []int
	for _, scope := range payableScopes {
		positive := shadowPositiveContributors(program, payableByScope[scope])
		if len(positive) < 2 {
			continue
		}
		relations := newShadowScope(program, coverFor(scope))
		for index := 0; index+1 < len(positive); index++ {
			candidates = relations.overlapCandidates(positive, index, candidates[:0])
			head := positive[index]
			for _, tail := range candidates {
				if relations.relation(head, tail) != relationDefiniteOverlap {
					continue
				}
				// The diagnostic names the CONTAINING line first, so the pair is
				// oriented through the predicate's own memoized closure.
				container, contained := head, tail
				if !relations.contains(container, contained) {
					container, contained = contained, container
				}
				containerKey, containedKey := program.keyStrings[container], program.keyStrings[contained]
				if firstErr == nil {
					containerIdentity := program.keyOf[container]
					firstErr = fmt.Errorf("%w: payable component %s transitively includes payable component %s in one %q direction %q unit scope",
						ErrSchemaOverlapConflict, containerKey, containedKey, string(containerIdentity.Direction), containerIdentity.Unit)
				}
				record(scope, containerKey, containedKey)
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
	// knownAnywhere is the call-wide set of components that have a COMPLETE
	// comparable quantity in at least one reduction scope. It is what separates
	// "never reported" from "reported in a different scope", which the per-scope
	// sets above cannot express: a component absent from THIS scope is ambiguous
	// between the two, and reading it as never reported would drag a scope that
	// settled the component elsewhere into a fail-closed diagnosis it does not
	// owe. The rule-derived dependency signal is gated on this set for exactly
	// that reason; the amount-based signal stays per-scope, because an amount is
	// only ever money in the scope that carries it.
	knownAnywhere := make(map[string]struct{})
	for _, item := range consistencyAggregates {
		if !item.complete {
			continue
		}
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		knownAnywhere[key.CanonicalKey()] = struct{}{}
	}
	for _, scope := range rateableScopes {
		keys := rateableByScope[scope]
		payable := payableByScope[scope]
		presence := presenceByScope[scope]
		zeroShare := zeroShareByScope[scope]
		provenPartitions := completePartitionParents[scope]
		// The one shared cover authority for this scope, taken from the per-scope
		// memo the structural pass already filled for every payable scope. It
		// answers the structural question -- is this declared complete coverage
		// exactly resolved, and is it denied for a shared member -- so the overlap
		// resolver cannot reach a different answer from the conservation proof
		// or the interval solver about the same physical fact. An ambiguous
		// partition (a child shared with another complete parent) still proves
		// nothing about the parents that declare it, and the ambiguity stays
		// LOCAL: only those parents are denied, so an unrelated parent's cover
		// proof still stands.
		cover := coverFor(scope)
		// The compiled cover parents are already in sorted canonical-key order,
		// which is what keeps the first conflict diagnostic deterministic; no
		// per-scope rebuild and re-sort is needed to obtain it.
		for _, parentID := range program.coverParents {
			parentKey := program.keyStrings[parentID]
			children := program.membersByParent[parentKey]
			if len(children) == 0 {
				continue
			}
			// A parent that bills a positive amount is normally excused from the
			// incomplete-cover classification, because its own line already
			// carries the partition's money. That premise is only true when the
			// partition is actually accounted for, so check the one case where it
			// is not: a REQUIRED member of the declared cover was never reported
			// with a complete quantity anywhere in this call, and its own rule
			// could have billed money. The rule-derived signal is unioned with the
			// amount-based one and can only add the parent back, and it is asked of
			// the MISSING member rather than of the parent merely having a rule, so
			// an aggregate-only tariff with an unreported unpriced child is
			// untouched.
			if _, chargesMoney := payable[parentKey]; chargesMoney {
				if missingRequiredMemberDependency(program, parentKey, knownAnywhere, dependencies) {
					recordHiddenDependency(scope, parentKey)
				}
			}
			// A subset is contained in the parent whenever its partition is
			// accounted for here. Coverage is resolved recursively: a declared
			// member satisfies its share directly when its rule resolved, or --
			// when it is present but unpriced, or absent altogether -- through
			// its own complete coverage, which the shared authority has already
			// resolved, so an absent parent A with a nested priced cover
			// B = X + Y still has a complete, billable partition. members holds
			// every accounted member; contributors is the RECURSIVE set of
			// actual paid contributors, the money the cover really carries.
			members := make(map[string]struct{})
			resolution := billCompleteCover(parentKey, keys, provenPartitions, presence, zeroShare, cover, members, make(map[string]struct{}))
			if resolution != coverProven {
				// The parent's complete cover could not be proven, and both denial
				// outcomes are positive statements rather than absences: an
				// AMBIGUOUS cover is a declared child shared with another complete
				// parent, so the true owner cannot be chosen, and an UNRESOLVED one
				// is a missing required member or an unplaceable unaccounted member,
				// so the partition is not known at all. Either way the parent's paid
				// partition is unknown, so nothing declared inside the parent may be
				// assumed disjoint from it and the denial is reported to the caller
				// instead of silently skipping the overlap analysis. The two denials
				// are distinguished because they carry different diagnoses: an
				// ambiguous partition is incomparable, an unknown one is incomplete.
				//
				// TWO commercial questions are asked here, deliberately not the same
				// one, because they are diagnosed on different evidence and pinned by
				// different contracts. HIDE MONEY is the rule-derived signal below,
				// and it is unconditional for an unobserved parent: a REQUIRED member
				// the provider never reported at all, carrying a rule that could have
				// billed, is missing money whether or not this parent also declares a
				// subset child, and whether the cover is unresolved or merely
				// ambiguous. A DOUBLE-CHARGE RISK IN THIS SCOPE is the amount-based
				// walk, and it stays tied to the subset suppression: a parent whose
				// partition money is fully represented here and which has no payable
				// subset descendant here has nothing unplaceable to fail closed, and
				// the 63c cross-scope control pins that.
				denial := recorder(resolution)
				if _, parentObserved := presence[parentKey]; !parentObserved &&
					missingRequiredMemberDependency(program, parentKey, knownAnywhere, dependencies) {
					// An UNOBSERVED parent gets no conservation proof and no
					// diagnostic from anywhere else, so an unaccounted REQUIRED
					// member that could have billed is a hole no other branch
					// closes. Recording here is what stops a declared complete
					// coverage from certifying complete on money the schema never
					// accounted for, and it is idempotent with the suppression path
					// below, so the two can both fire.
					denial(scope, parentKey)
				}
				if resolution == coverUnresolved && !unprovableCoverNeedsDiagnosis(program, parentKey, presence, payable) {
					// An OBSERVED parent already owns a diagnosis: the
					// conservation proof ran on its own quantity, found the
					// missing required member, and classified the valuation
					// partial, so its additive subset money can never settle.
					// That is the repository's deliberate contract, pinned by the
					// R7 preservation suite. An unobserved parent with no money
					// on either side of the unknown partition has nothing to
					// settle either, and per-scope isolation depends on leaving
					// it untouched.
					continue
				}
				recordUnplaceableSubsetOverlap(program, scope, parentID, payable, denial, record)
				continue
			}
			// contributors is the RECURSIVE set of actual paid contributors: the
			// money this cover really carries. Reading it off the DIRECT members
			// alone is what let an explicitly-free intermediate leave the deeper
			// payable leaves outside both the redundancy test and the
			// suppression, so a nested declaration settled different money from
			// its own flattened equivalent. A declared member that does not
			// itself bill a positive amount hands its share to whatever bills
			// beneath it, and that is the same recursive resolution the cover
			// proof uses.
			contributors := recursivePaidContributors(parentKey, payable, cover)
			for _, subsetID := range program.subsetChildren[parentID] {
				// A direct subset child is the common case; the same rule
				// must also see every payable component transitively included
				// through absent or unpriced intermediate subset children, so
				// traverse the bounded inclusion graph from the parent's subset
				// child and collect all of them. Traversal never starts at the
				// parent, so the complete partition members are not
				// misreported as overlapping with their own cover.
				hits := program.payableDescendants(allInclusionEdges, subsetID, payable)
				// A hit already carrying the parent's own money through the
				// partition cover is the same charge reached by an alternate,
				// redundant path, not an extra one. Only a positive payable
				// descendant outside the actual paid contributor set is an
				// independent additive double charge.
				extra := make([]string, 0, len(hits))
				for _, hit := range hits {
					if _, isContributor := contributors[hit]; isContributor {
						continue
					}
					extra = append(extra, hit)
				}
				if len(extra) == 0 {
					continue
				}
				if firstErr == nil {
					subset := program.keyOf[subsetID]
					descendants := append([]string(nil), extra...)
					sort.Strings(descendants)
					firstErr = fmt.Errorf("%w: complete partition parent %s has payable partition children and priced included subset child %s (payable descendants %s) in one %q direction %q unit scope",
						ErrSchemaOverlapConflict, parentKey, program.keyStrings[subsetID], strings.Join(descendants, ","), string(subset.Direction), subset.Unit)
				}
				// Suppress the extra additive hits together with every actual
				// cover member AND every actual paid contributor beneath it
				// (including a zero-valued, explicitly-free or nested member),
				// so a rejected conflict never leaves a partial winner on
				// either side; contributors is otherwise used only to subtract
				// a redundant path from the extra-hit set, not to limit
				// suppression.
				suppressed := make(map[string]struct{}, len(members)+len(contributors))
				for memberKey := range members {
					suppressed[memberKey] = struct{}{}
				}
				for contributorKey := range contributors {
					suppressed[contributorKey] = struct{}{}
				}
				record(scope, extra...)
				for _, memberKey := range sortedCanonicalKeys(suppressed) {
					if _, ok := program.byKey[memberKey]; ok {
						record(scope, memberKey)
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
	if len(hiddenDependencyCovers) == 0 {
		hiddenDependencyCovers = nil
	}
	return conflicts, ambiguousCovered, unresolvedCovered, hiddenDependencyCovers, firstErr
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
// everything declared inside the parent lies outside it. The denial stays LOCAL: a
// parent that declares no subset child, or whose subset children carry no positive
// payable descendant in this scope, is not economically relevant and is left
// untouched.
//
// The parent's declared subset children and the walk that prices them come from
// the compiled program, so this path and the proven-cover path above ask the
// inclusion question through the same adjacency.
func recordUnplaceableSubsetOverlap(
	program *schemaProgram,
	scope string,
	parentID int,
	payable map[string]struct{},
	recorder func(scope, parentKey string),
	record func(scope string, keys ...string),
) {
	for _, subsetID := range program.subsetChildren[parentID] {
		hits := program.payableDescendants(allInclusionEdges, subsetID, payable)
		if len(hits) == 0 {
			continue
		}
		recorder(scope, program.keyStrings[parentID])
		// Only the unplaceable side is withheld. The partition children keep
		// their own independently payable lines: the enclosing valuation is
		// classified partial, so it can never settle, and the suppressed side is
		// the one whose disjointness could not be proven.
		record(scope, hits...)
	}
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

// sortedScopeKeys returns the reduction scopes of a per-scope projection in
// deterministic order, for the same reason: a walk that records a first
// diagnostic while ranging a scope map would otherwise have Go's randomized map
// iteration choose which scope is quoted. The scope key is the reducer's own
// length-prefixed identity, so its lexicographic order is total and stable.
func sortedScopeKeys(byScope map[string]map[string]struct{}) []string {
	scopes := make([]string, 0, len(byScope))
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
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
