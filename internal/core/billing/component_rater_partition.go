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
//
// ONE COMPILED PROGRAM IS THE WHOLE SCHEMA AUTHORITY. compileSchemaProgram
// runs once, from NewReferenceRater, over the rater's own canonicalized
// snapshot copy, and derives every projection this file needs: the declared
// edge list, the two containment classes and their reverse, the topological
// order, the propagation depth, the cover parents, and the canonical-key
// member projection. A Rate call therefore performs NO schema scan and NO
// re-canonicalisation of a declared component key: it reads integer node ids
// and the compiled keyStrings table. Anything that needs IDENTITY reaches it
// through program.keyStrings[id] or program.byKey, never by re-marshalling a
// key. Every reachability question is asked of the one walk, edgeClass
// selecting which adjacency it may cross.
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

// schemaProgram is the frozen component-schema inclusion graph compiled ONCE at
// rater construction into integer node ids. Every later Rate call reads the
// compiled program; it never re-walks canonical string keys or rediscovers
// topology. It is built from the rater's private, canonicalized snapshot copy,
// so a caller mutation cannot change compiled semantics.
//
// Aggregate and partition relationships are SYNONYMS here: both declare that the
// named children form a COMPLETE, additive coverage of the parent, so both feed
// the complete-coverage constraint. Subset is a partial containment only, and a
// transform is a separately governed unit derivation that is never traversed.
//
// "Containment" is the union of complete-coverage and subset edges: for every
// containment edge the child is contained in the parent, and the relation is
// transitive. Every edge here already shares economic direction and unit, so all
// traversal is direction- and unit-safe by construction.
type schemaProgram struct {
	byKey map[string]int
	keyOf []metering.ComponentKey
	// keyStrings is keyOf's canonical JSON identity, indexed by the same node id,
	// so a per-id identity is a slice index rather than a re-marshalling.
	// metering.ComponentKey.CanonicalKey is encoding/json.Marshal of the WHOLE
	// key, so deriving one inside a pair loop or a per-parent dedup scan costs a
	// full canonicalisation per iteration and makes both quadratic in the
	// declared graph size.
	keyStrings          []string
	topo                []int
	completeChildren    [][]int
	completeOptional    [][]bool
	subsetChildren      [][]int
	containmentChildren [][]int
	reverseContainment  [][]int
	completeOwner       []int
	// sharedCompleteOwner names the nodes declared as a complete-coverage child
	// by TWO DISTINCT complete parents. It is separate from completeOwner
	// because -1 there means both "no complete parent at all" and "ambiguously
	// shared", and the cover resolver must tell those apart: the first is simply
	// a leaf, the second is a positive ambiguity claim.
	sharedCompleteOwner []bool
	// propagationDepth is the longest containment path in EDGES, computed ONCE
	// here from the frozen graph. See propagateQuantityIntervals for the sweep
	// bound derived from it and why that bound is sufficient.
	propagationDepth int
	// declaredEdges is every inclusion edge the snapshot declares, in canonical
	// DECLARATION order, with both endpoints already resolved to node ids and
	// both canonical identities already derived. A per-call consumer that needs
	// the declared relationships -- the direct overlap check, the excluded-child
	// check -- therefore reads declared topology as data instead of rescanning
	// the snapshot's relationships and re-canonicalising every endpoint on
	// every call. schemaID and kind are carried because the overlap diagnostic
	// quotes them; neither is needed to answer a containment question.
	declaredEdges []programEdge
	// coverParents lists, in ascending node id, the compiled nodes that declare
	// at least one complete-coverage member. Node ids are assigned in sorted
	// canonical-key order, so ascending id IS the order a per-call sort of the
	// canonical parent keys produced, and the first conflict diagnostic stays
	// deterministic without rebuilding or re-sorting that list per scope.
	coverParents []int
	// membersByParent is the ONE projection of the compiled complete-coverage
	// adjacency into the canonical-key shape the string-keyed consumers walk.
	// It is built here, once, so the conservation proof and the overlap resolver
	// cannot disagree about which members a parent declares, in which order, or
	// which of them are optional -- and neither re-reads the snapshot's
	// relationships, so topology is discovered exactly once, at rater
	// construction. A node that declares no complete child is absent from it,
	// which is exactly the set of parents that can carry a cover.
	membersByParent map[string][]partitionMember
}

// programEdge is one declared inclusion edge in compiled form. The zero value is
// never used: every entry comes from compileSchemaProgram, which resolves both
// endpoints to ids and derives both canonical identities.
type programEdge struct {
	schemaID string
	parent   int
	child    int
	kind     metering.RelationshipKind
}

// compileSchemaProgram indexes a validated, canonicalized schema set into one
// compiled program. Node ids are assigned in sorted canonical-key order, so the
// program is independent of schema and relationship declaration order.
func compileSchemaProgram(schemas []metering.ComponentSchema) schemaProgram {
	program := schemaProgram{byKey: map[string]int{}}
	if len(schemas) == 0 {
		return program
	}
	type schemaEdge struct {
		schemaID  string
		parent    metering.ComponentKey
		child     metering.ComponentKey
		parentKey string
		childKey  string
		kind      metering.RelationshipKind
		optional  bool
	}
	keys := make(map[string]metering.ComponentKey)
	rawEdges := make([]schemaEdge, 0)
	for _, schema := range schemas {
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
			parentKey, childKey := parent.CanonicalKey(), child.CanonicalKey()
			if parentKey == "" || childKey == "" {
				continue
			}
			keys[parentKey] = parent
			keys[childKey] = child
			// Both canonical identities are derived ONCE per declared edge and
			// carried on it, so the ordering comparator and the dedup pass read
			// them as data; re-deriving them inside the comparator cost four full
			// canonicalisations per comparison and made compilation quadratic.
			rawEdges = append(rawEdges, schemaEdge{
				schemaID: schema.ID,
				parent:   parent, child: child, parentKey: parentKey, childKey: childKey,
				kind: relationship.Kind, optional: relationship.Optional,
			})
		}
	}
	if len(keys) == 0 {
		return program
	}
	canonicalKeys := make([]string, 0, len(keys))
	for canonical := range keys {
		canonicalKeys = append(canonicalKeys, canonical)
	}
	sort.Strings(canonicalKeys)
	count := len(canonicalKeys)
	program.keyOf = make([]metering.ComponentKey, count)
	program.keyStrings = make([]string, count)
	for id, canonical := range canonicalKeys {
		program.keyOf[id] = keys[canonical]
		program.keyStrings[id] = canonical
		program.byKey[canonical] = id
	}
	program.completeChildren = make([][]int, count)
	program.completeOptional = make([][]bool, count)
	program.subsetChildren = make([][]int, count)
	program.containmentChildren = make([][]int, count)
	program.reverseContainment = make([][]int, count)
	program.completeOwner = make([]int, count)
	program.sharedCompleteOwner = make([]bool, count)
	completeParentSeen := make([]bool, count)
	for id := range program.completeOwner {
		program.completeOwner[id] = -1
	}
	// The merged containment view and the declared edge list are compiled BEFORE
	// the sort, in DECLARATION order, because that is the order the payability
	// walks have always visited: the FIRST payable descendant a walk reaches is
	// quoted verbatim in the overlap diagnostic, and those diagnostics are
	// compared byte for byte. Everything else that reads containment (the
	// topological order, the longest path, the unknown-intersection ancestor and
	// descendant sets) reads a SET or a maximum, so the order is invisible there.
	program.declaredEdges = make([]programEdge, 0, len(rawEdges))
	containmentPairs := make(map[[2]int]struct{}, len(rawEdges))
	for _, edge := range rawEdges {
		parent, child := program.byKey[edge.parentKey], program.byKey[edge.childKey]
		program.declaredEdges = append(program.declaredEdges, programEdge{
			schemaID: edge.schemaID, parent: parent, child: child, kind: edge.kind,
		})
		pair := [2]int{parent, child}
		if _, duplicate := containmentPairs[pair]; duplicate {
			continue
		}
		containmentPairs[pair] = struct{}{}
		program.containmentChildren[parent] = append(program.containmentChildren[parent], child)
		program.reverseContainment[child] = append(program.reverseContainment[child], parent)
	}
	sort.SliceStable(rawEdges, func(i, j int) bool {
		pi, ci := program.byKey[rawEdges[i].parentKey], program.byKey[rawEdges[i].childKey]
		pj, cj := program.byKey[rawEdges[j].parentKey], program.byKey[rawEdges[j].childKey]
		if pi != pj {
			return pi < pj
		}
		if ci != cj {
			return ci < cj
		}
		return rawEdges[i].kind < rawEdges[j].kind
	})
	completePairs := make(map[[2]int]struct{})
	subsetPairs := make(map[[2]int]struct{})
	for _, edge := range rawEdges {
		parent := program.byKey[edge.parentKey]
		child := program.byKey[edge.childKey]
		pair := [2]int{parent, child}
		switch {
		case edge.kind == metering.RelationshipSubset:
			if _, duplicate := subsetPairs[pair]; !duplicate {
				subsetPairs[pair] = struct{}{}
				program.subsetChildren[parent] = append(program.subsetChildren[parent], child)
			}
		case completeCoverageRelationship(edge.kind):
			if _, duplicate := completePairs[pair]; !duplicate {
				completePairs[pair] = struct{}{}
				program.completeChildren[parent] = append(program.completeChildren[parent], child)
				program.completeOptional[parent] = append(program.completeOptional[parent], edge.optional)
				if !completeParentSeen[child] {
					completeParentSeen[child] = true
					program.completeOwner[child] = parent
				} else if program.completeOwner[child] != parent {
					// Decision 9: shared complete ownership stays a runtime
					// classification; publication never rejects it and the
					// solver simply carries every declaring parent's constraint.
					program.completeOwner[child] = -1
					program.sharedCompleteOwner[child] = true
				}
			}
		}
	}
	program.coverParents = compileCoverParents(&program)
	program.membersByParent = programCompleteMembers(&program)
	program.topo = containmentOrderNodes(&program)
	program.propagationDepth = longestContainmentPath(&program)
	return program
}

// compileCoverParents lists, in ascending node id, the compiled nodes that
// declare at least one complete-coverage member. Node ids are assigned in sorted
// canonical-key order, so ascending id is exactly the order the per-call
// map-then-sort of the canonical parent keys produced; the fixpoint iteration
// and the first conflict diagnostic both depend on that order being stable.
func compileCoverParents(program *schemaProgram) []int {
	var parents []int
	for id, children := range program.completeChildren {
		if len(children) != 0 {
			parents = append(parents, id)
		}
	}
	return parents
}

// longestContainmentPath derives, ONCE at compile time, the longest containment
// path in edges. It is a plain longest-path over the acyclic containment graph walked in
// topological order, so every parent is resolved before its child is visited.
// Deriving it beside the compile that produced the graph makes the sweep bound a
// fact about the FROZEN program rather than a per-call guess.
func longestContainmentPath(program *schemaProgram) int {
	deepest := 0
	byNode := make([]int, len(program.keyOf))
	for _, node := range program.topo {
		longest := 0
		for _, parent := range program.reverseContainment[node] {
			if candidate := byNode[parent] + 1; candidate > longest {
				longest = candidate
			}
		}
		byNode[node] = longest
		if longest > deepest {
			deepest = longest
		}
	}
	return deepest
}

// containmentOrderNodes returns node ids with every containment parent before
// its children. Publication validation has already rejected cycles, so Kahn's
// algorithm always completes; a malformed cyclic graph simply leaves the
// remaining nodes out of the order instead of looping.
func containmentOrderNodes(program *schemaProgram) []int {
	count := len(program.keyOf)
	indegree := make([]int, count)
	for parent := range count {
		for _, child := range program.containmentChildren[parent] {
			indegree[child]++
		}
	}
	ordered := make([]int, 0, count)
	emitted := make([]bool, count)
	for len(ordered) < count {
		next := -1
		for node := range count {
			if !emitted[node] && indegree[node] == 0 {
				next = node
				break
			}
		}
		if next == -1 {
			break
		}
		emitted[next] = true
		ordered = append(ordered, next)
		for _, child := range program.containmentChildren[next] {
			indegree[child]--
		}
	}
	return ordered
}

// schemaQuantityVerdict is the quantity-only classification produced by the
// compiled constraint solver for one reduction. It maps each reduction scope to
// the canonical component keys that violate that rule.
//
// subset names nodes whose containment bound is violated: the enforced lower
// bound (from themselves or a contained descendant, across the transitive union
// of subset and complete-coverage edges) exceeds the enforced upper bound (from
// themselves or a containing ancestor). negative names nodes whose effective
// reduced ordinary-usage quantity is strictly below zero. Both are quantity
// facts, so they never depend on which side happens to be priced. unknown carries
// the decision-8 unknown containment intersections.
type schemaQuantityVerdict struct {
	subset   map[string]map[string]struct{}
	negative map[string]map[string]struct{}
	// unknown carries the decision-8 unknown containment intersections. They are
	// deliberately NON-BLOCKING: they never downgrade completeness and never
	// change money. They are reported as an auxiliary diagnostic only when the
	// valuation is already non-complete.
	unknown map[string][]string
}

// schemaQuantityContradictions is the single quantity authority for the rater.
// It compiles nothing at call time: the schema program is already frozen on the
// rater. Only the FULL, exclusion-free reduction (consistencyAggregates) is
// passed here. The pricing projection never feeds the constraint solver, because
// a covered, unpriced child that is excluded from pricing is still a physical
// quantity fact for the containment arithmetic.
func (r *ReferenceRater) schemaQuantityContradictions(aggregates []aggregateMeasure, diagnosed map[string]map[string]struct{}) schemaQuantityVerdict {
	if r == nil || len(r.program.keyOf) == 0 || len(aggregates) == 0 {
		return schemaQuantityVerdict{}
	}
	byScope := make(map[string][]aggregateMeasure)
	for _, item := range aggregates {
		byScope[item.scopeKey] = append(byScope[item.scopeKey], item)
	}
	scopes := make([]string, 0, len(byScope))
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	var verdict schemaQuantityVerdict
	for _, scope := range scopes {
		subset, negative, unknown := solveScopeQuantityConstraints(&r.program, byScope[scope], diagnosed[scope])
		mergeSchemaQuantitySet(&verdict.subset, scope, subset)
		mergeSchemaQuantitySet(&verdict.negative, scope, negative)
		if len(unknown) != 0 {
			if verdict.unknown == nil {
				verdict.unknown = make(map[string][]string)
			}
			verdict.unknown[scope] = unknown
		}
	}
	return verdict
}

func mergeSchemaQuantitySet(target *map[string]map[string]struct{}, scope string, keys map[string]struct{}) {
	if len(keys) == 0 {
		return
	}
	if *target == nil {
		*target = make(map[string]map[string]struct{})
	}
	scopeSet := (*target)[scope]
	if scopeSet == nil {
		scopeSet = make(map[string]struct{}, len(keys))
		(*target)[scope] = scopeSet
	}
	for key := range keys {
		scopeSet[key] = struct{}{}
	}
}

// quantityEvidenceState is the stored state of one node's evidence in one
// reduction scope. The three states are exhaustive and mutually exclusive, and
// the distinction is deliberate: Exact(0) is a genuine observed zero while
// Absent and Unavailable are unknown and must never be read as zero.
type quantityEvidenceState uint8

const (
	quantityAbsent quantityEvidenceState = iota
	quantityExact
	quantityUnavailable
) // solveScopeQuantityConstraints runs one deterministic, monotone interval

// fixed point over the compiled program using only evidence from a single
// reduction scope. Evidence never crosses scopes.
//
// Each node starts at lower=0, upper=+infinity. Exact(q) pins both bounds to q
// (a negative q clamps lower to 0 and is then reported as a structural
// contradiction, never silently treated as zero). Absent and Unavailable keep
// lower=0, upper=+infinity but remain distinct in the stored state.
//
// The constraints are:
//
//	subset   P -> C : lower(P) = max(lower(P), lower(C)); upper(C) = min(upper(C), upper(P))
//
//	complete P -> {Ci} : lower(P) >= sum(lower(Ci)); upper(P) <= sum(upper(Ci)) when all finite
//
// plus the child-directed reverse rules. Bound propagation is INFERENCE, not
// observation: the subset upper bound reaches an absent or unavailable child
// exactly like any other, and a reverse rule inverts a conserved equation only for
// a child with a known exact representation. Which members have one, the
// absent-optional edge-local zero, the all-finite guard, and the rule that only
// resolveCompleteCovers may create a represented value, are stated on that
// authority, which is the one the conservation proof and the overlap resolver read.
//
// "complete" here is the synonym of aggregate and partition. A node whose enforced
// lower bound exceeds its upper bound is a quantity contradiction, and the solver
// stops propagating from an already-contradicted node so one inconsistent subtree
// cannot manufacture a second, unrelated report. Bounds are integers of the input
// rationals only. The sweep bound and the reason it suffices live on
// propagateQuantityIntervals. Node order is the sorted-id topological order and edge
// order is the sorted-id compile order, so no map iteration order can affect the
// result.
func solveScopeQuantityConstraints(program *schemaProgram, items []aggregateMeasure, diagnosed map[string]struct{}) (map[string]struct{}, map[string]struct{}, []string) {
	count := len(program.keyOf)
	// The scope's evidence reading AND its recursive complete-cover
	// representation come from the one shared authority, so the reverse
	// complete-coverage inversion below can never disagree with the conservation
	// proof or the overlap resolver about which member has a known share.
	cover := resolveCompleteCovers(program, items)
	lower, _, contradicted := propagateQuantityIntervals(program, cover.state, cover.exact, cover.represented)
	// A node the partition proof already classified -- and any node contained
	// beneath it -- is left to that more precise diagnosis: the containment
	// violation below it is the same physical inconsistency expressed less
	// specifically, so restating it here would only add a duplicate. Parents are
	// visited before children in topo order, so one pass propagates the flag.
	diagnosedBelow := make([]bool, count)
	for _, node := range program.topo {
		flagged := false
		if diagnosed != nil {
			if _, ok := diagnosed[program.keyStrings[node]]; ok {
				flagged = true
			}
		}
		if !flagged {
			for _, parent := range program.reverseContainment[node] {
				if diagnosedBelow[parent] {
					flagged = true
					break
				}
			}
		}
		diagnosedBelow[node] = flagged
	}
	subset := make(map[string]struct{})
	negativeKeys := make(map[string]struct{})
	for node := range count {
		if diagnosedBelow[node] {
			continue
		}
		key := program.keyStrings[node]
		if cover.negative[node] {
			negativeKeys[key] = struct{}{}
		}
		if contradicted[node] {
			subset[key] = struct{}{}
		}
	}
	return subset, negativeKeys, containmentUnknownIntersections(program, lower, cover.represented)
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
// int-indexed walk as every other question in this file, through the edgeClass
// knob, and the pair SET it returns is unchanged by that consolidation.
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

// reachable returns the set of nodes reachable from start over one edge class,
// excluding start itself. It is the ONE reachability walk in this file: the
// monetary overlap rules, the cover-relevance test and the unknown-intersection
// diagnostic all ask this question, so they all read this one result instead of
// each deriving a traversal of its own.
//
// The walk visits each node once, so it is bounded even under a malformed graph,
// and it collects a set rather than a path, so it is order independent.
//
// The work list is consumed by INDEX and never resliced, so a pop does not strip
// the capacity the next append has to grow: a resliced head reallocated the whole
// list once per visited node. A node already in the set is not enqueued at all,
// which bounds the list by the node count and makes the single up-front capacity
// exact. Both changes are invisible to the result: an enqueued duplicate used to
// be popped and discarded, and every consumer reads the set by membership.
func (p *schemaProgram) reachable(class edgeClass, start int) map[int]struct{} {
	edges := p.adjacency(class)
	seen := make(map[int]struct{})
	queue := append(make([]int, 0, len(edges)), edges[start]...)
	for at := 0; at < len(queue); at++ {
		current := queue[at]
		if _, duplicate := seen[current]; duplicate {
			continue
		}
		seen[current] = struct{}{}
		for _, next := range edges[current] {
			if _, duplicate := seen[next]; !duplicate {
				queue = append(queue, next)
			}
		}
	}
	return seen
}

// payableDescendants reports the CANONICAL IDENTITY of every node reachable from
// start over one edge class whose effective amount in the given scope is
// strictly positive, INCLUDING start itself, in the order the walk reached them.
//
// Reach matters, not adjacency: a member represented two complete-coverage hops
// below an unobserved parent is still money on that parent's side, and it is
// exactly the case a direct-child test cannot see. It reports identities rather
// than node ids because every consumer wants the identity: the payable and
// contributor sets are identity-keyed and the diagnostic quotes it, and the
// identity is the compiled keyStrings entry rather than a re-marshalled key.
//
// It reports the WALK ORDER rather than a set, because the first hit is quoted
// verbatim in the overlap diagnostic. The compiled adjacency is therefore built
// in declared order for this reason alone; see compileSchemaProgram.
func (p *schemaProgram) payableDescendants(class edgeClass, start int, payable map[string]struct{}) []string {
	edges := p.adjacency(class)
	seen := make(map[int]struct{}, len(edges))
	queue := append(make([]int, 0, len(edges)+1), start)
	var hits []string
	for at := 0; at < len(queue); at++ {
		current := queue[at]
		if _, duplicate := seen[current]; duplicate {
			continue
		}
		seen[current] = struct{}{}
		canonical := p.keyStrings[current]
		if _, positive := payable[canonical]; positive {
			hits = append(hits, canonical)
		}
		queue = append(queue, edges[current]...)
	}
	return hits
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

// propagateQuantityIntervals runs the monotone interval fixed point described on
// solveScopeQuantityConstraints and returns the final lower/upper bounds plus the
// per-node contradiction flags.
//
// SWEEP BOUND. The loop is bounded by a NO-CHANGE sweep, not by the counter: a
// sweep that changes no bound has reached the fixed point, because the transfer
// functions are monotone and a monotone operator applied to its own fixed point is a
// fixed point. The counter is a backstop, co-bounded by the program's PRECOMPUTED
// longest containment path rather than by the node count alone:
//
//	sweeps = min(len(topo)+1, 2*propagationDepth+3)
//
// 2*propagationDepth+3 is a PROVEN sufficient height, not an assumption: a bound's
// influence travels DOWN or UP the acyclic containment DAG, never both at once, and
// every edge is one hop, so a propagation phase needs at most propagationDepth
// sweeps to carry every derived bound everywhere it can reach, plus one sweep to
// observe that nothing changed. The lower bounds are a function of the lower bounds
// alone; the upper bounds read both, so they settle in the same window once the
// lower ones are final. Contradiction is monotone (a node is only ever ADDED) and
// only ever removes a bound, so it settles with the phase that sets it and cannot
// restart the other. Three is the slack for both settle windows plus the confirming
// sweep, so the fixed point is reached strictly inside the cap for every acyclic
// program. The cap is only ever the MINIMUM of that and len(topo)+1, the historical
// node-count bound, so no graph that converged under the old cap can fail to
// converge here.
//
// ORDER IS PART OF THE RESULT, NOT AN IMPLEMENTATION DETAIL. The DOWN pass reads a
// parent and writes its children, so topological order already carries it the length
// of its path; the UP pass reads children and writes the parent that absorbs them,
// so topological order carries it ONE HOP per sweep and a deep chain needs
// Theta(depth) sweeps. Running the UP pass in reverse topological order would settle
// both directions in one sweep, and that is tempting to read as a pure speedup, but
// it is NOT result-preserving: a contradicted node stops contributing (boundLower
// becomes zero, boundUpper becomes nil), which makes the operator ANTITONE in the
// contradiction set, so the terminal state depends on the visiting order and not
// only on the constraint system. Acceptance vector 06 is the minimal witness: with
// ancestor 30 over middle over leaves 20/20 and a third absent required leaf, this
// order flags MIDDLE (the down pass caps it at 30 and 20+20 > 30 collapses it) while
// the reverse order has already lifted the ancestor's own lower bound to 40 and flags
// the ANCESTOR. Both fail closed; the reported class changes, so the order is pinned.
func propagateQuantityIntervals(program *schemaProgram, state []quantityEvidenceState, exact []*big.Rat, represented []*big.Rat) ([]*big.Rat, []*big.Rat, []bool) {
	count := len(program.keyOf)
	lower := make([]*big.Rat, count)
	upper := make([]*big.Rat, count)
	contradicted := make([]bool, count)
	for node := range count {
		if state[node] == quantityExact {
			lower[node] = new(big.Rat).Set(exact[node])
			upper[node] = new(big.Rat).Set(exact[node])
			continue
		}
		lower[node] = new(big.Rat)
	}
	boundLower := func(node int) *big.Rat {
		if contradicted[node] {
			return new(big.Rat)
		}
		return lower[node]
	}
	boundUpper := func(node int) *big.Rat {
		if contradicted[node] {
			return nil
		}
		return upper[node]
	}
	// The two cover-equation accumulators live for the whole solve. They are reset
	// at the top of every equation and COPIED into lower/upper only when a bound
	// moves, so no stored bound can be mutated by a later sweep.
	sumLower, sumUpper := new(big.Rat), new(big.Rat)
	changed := false
	// A node's body in EITHER pass is a pure function of its own bounds and of its
	// children's effective bounds, so re-running it when none of those has moved can
	// only reproduce the value the node already holds. settled carries one flag per
	// node per pass (the second half is the down pass), which makes the solve's
	// big-rational work proportional to the number of bound MOVES instead of to the
	// node count times the sweep count. Every move clears the node's flag and each
	// containment ancestor's flag in BOTH passes, and only a completed body sets it,
	// so no move is dropped. That closure is exactly each body's read set: the up
	// body reads the node and its children, the down body reads the node and its
	// children too, and a node is a child of nothing but its own parents' bodies, so
	// no reader is ever left settled after a move. Each body writes only to itself
	// (up) or only to its own children (down), so a write ALWAYS clears the writer's
	// own flag: a set flag therefore means the last execution wrote nothing AND left
	// every value it read untouched, so a skipped body would have written nothing
	// either. The pruned trajectory is the unpruned one term for term.
	settled := make([]bool, 2*count)
	markMoved := func(node int) {
		changed = true
		settled[node] = false
		settled[count+node] = false
		for _, ancestor := range program.reverseContainment[node] {
			settled[ancestor] = false
			settled[count+ancestor] = false
		}
	}
	maxSweeps := min(len(program.topo)+1, 2*program.propagationDepth+3)
	for range maxSweeps {
		for _, parent := range program.topo {
			if contradicted[parent] || settled[parent] {
				continue
			}
			if children := program.completeChildren[parent]; len(children) != 0 {
				sumLower.SetInt64(0)
				sumUpper.SetInt64(0)
				allFinite := true
				for index, child := range children {
					if state[child] == quantityAbsent && program.completeOptional[parent][index] {
						// The schema's declared edge-local zero for an absent
						// optional member contributes zero to THIS cover equation
						// and never becomes a global zero. Its zero upper is
						// still finite, so a cover whose every member is either
						// present-with-a-known-upper or absent-optional sums to a
						// determined value and may bound the parent, and through
						// it the parent's contained descendants. An absent
						// REQUIRED member has no finite upper, so the parent's
						// own value stays unknown and it bounds nothing.
						continue
					}
					sumLower.Add(sumLower, boundLower(child))
					childUpper := boundUpper(child)
					if childUpper == nil {
						allFinite = false
						continue
					}
					sumUpper.Add(sumUpper, childUpper)
				}
				if sumLower.Cmp(lower[parent]) > 0 {
					lower[parent] = new(big.Rat).Set(sumLower)
					markMoved(parent)
				}
				if allFinite && (upper[parent] == nil || sumUpper.Cmp(upper[parent]) < 0) {
					upper[parent] = new(big.Rat).Set(sumUpper)
					markMoved(parent)
				}
			}
			for _, child := range program.subsetChildren[parent] {
				childLower := boundLower(child)
				if childLower.Cmp(lower[parent]) > 0 {
					lower[parent] = childLower
					markMoved(parent)
				}
			}
			if upper[parent] != nil && lower[parent].Cmp(upper[parent]) > 0 {
				contradicted[parent] = true
				markMoved(parent)
			}
			settled[parent] = true
		}
		for _, parent := range program.topo {
			if contradicted[parent] || settled[count+parent] {
				continue
			}
			children := program.completeChildren[parent]
			// The two sibling sums are re-read from the state THIS loop has already
			// mutated, so they are carried as one running total per direction and
			// this member's own term is removed from it instead of re-summing every
			// sibling per member. total-minus-own is the same exact rational the
			// re-summation produces, and a member whose term moved rebuilds the
			// total before the next member reads it, so every read is the value the
			// quadratic form read at that index. This is a regrouping, not a reorder:
			// big.Rat is canonical, so the regrouped sum is bit-identical.
			totalLower, totalUpper, infiniteUppers, stale := new(big.Rat), new(big.Rat), 0, true
			for index, child := range children {
				if contradicted[child] || represented[child] == nil {
					// A child with no exact representation has no conserved
					// share to invert: applying the reverse rule to it would
					// synthesize a global value out of absence. An absent
					// OPTIONAL member that IS represented through its own
					// evidence still takes the inferred bound normally, which is
					// what catches a cover whose edge-local zero contradicts the
					// member's separately represented quantity.
					continue
				}
				if stale {
					stale = false
					totalLower.SetInt64(0)
					totalUpper.SetInt64(0)
					infiniteUppers = 0
					for member, node := range children {
						if state[node] == quantityAbsent && program.completeOptional[parent][member] {
							continue
						}
						totalLower.Add(totalLower, boundLower(node))
						if value := boundUpper(node); value != nil {
							totalUpper.Add(totalUpper, value)
						} else {
							infiniteUppers++
						}
					}
				}
				ownLower, ownUpper := lower[child], upper[child]
				counted := state[child] != quantityAbsent || !program.completeOptional[parent][index]
				if upper[parent] != nil {
					sumLower.Set(totalLower)
					if counted {
						sumLower.Sub(sumLower, ownLower)
					}
					candidate := new(big.Rat).Sub(upper[parent], sumLower)
					if candidate.Sign() < 0 {
						candidate.SetInt64(0)
					}
					if upper[child] == nil || candidate.Cmp(upper[child]) < 0 {
						upper[child] = candidate
						markMoved(child)
					}
				}
				remaining := infiniteUppers
				if counted && ownUpper == nil {
					remaining--
				}
				if remaining == 0 {
					sumUpper.Set(totalUpper)
					if counted && ownUpper != nil {
						sumUpper.Sub(sumUpper, ownUpper)
					}
					candidate := new(big.Rat).Sub(lower[parent], sumUpper)
					if candidate.Sign() < 0 {
						candidate.SetInt64(0)
					}
					if candidate.Cmp(lower[child]) > 0 {
						lower[child] = candidate
						markMoved(child)
					}
				}
				if upper[child] != nil && lower[child].Cmp(upper[child]) > 0 {
					contradicted[child] = true
					markMoved(child)
				}
				stale = stale || lower[child] != ownLower || upper[child] != ownUpper || contradicted[child]
			}
			for _, child := range program.subsetChildren[parent] {
				if contradicted[child] || upper[parent] == nil {
					// An upper bound is INFERENCE, not an observation: it
					// propagates into an absent or unavailable child normally.
					// Only a nil (infinite) parent upper carries no information.
					continue
				}
				if upper[child] == nil || upper[parent].Cmp(upper[child]) < 0 {
					upper[child] = new(big.Rat).Set(upper[parent])
					markMoved(child)
				}
				if upper[child] != nil && lower[child].Cmp(upper[child]) > 0 {
					contradicted[child] = true
					markMoved(child)
				}
			}
			settled[count+parent] = true
		}
		if !changed {
			break
		}
	}
	return lower, upper, contradicted
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
// against rule existence. An absent OPTIONAL member is not this condition; the
// frozen schema declares its own optional zero for it.
var ErrSchemaPartitionIncomplete = errors.New("billing: frozen schema complete partition is missing required members")

// edgeClass is the single knob that separates every reachability walk in this
// file: the frozen component-schema inclusion graph, split by edge class, with
// all reachability expressed as a bounded, scope-aware traversal over integer
// node ids compiled once at rater construction.
//
// The rater's monetary overlap rules already treat inclusion TRANSITIVELY: a
// payable component is reachable from a parent through any chain of declared
// inclusion edges, however deep. A quantity or cover check that inspects only a
// node's immediate neighbours is therefore unsound, because the money or the
// quantity on the far side of a nested declaration is invisible to it. Keeping
// the two edge classes in one place is what makes the containment and cover
// semantics agree with the overlap semantics instead of drifting apart.
//
// Both edge classes are already restricted to equal economic direction and unit,
// so every traversal here is direction- and unit-safe by construction. A
// transform edge is a separately governed unit derivation and is never a
// containment, so it is not in either class.
type edgeClass uint8

const (
	// allInclusionEdges is the union of complete-coverage and subset edges, and
	// the correct view for any question about whether one quantity is INSIDE
	// another. A declared aggregate, partition or subset edge all mean the same
	// thing about containment and differ only in the extra CONSERVATION claim a
	// complete-coverage edge makes, so a merged view is what stops a quantity
	// check from being blind to a chain that changes class partway, such as
	// A subset B with B partition C.
	allInclusionEdges edgeClass = iota
	// completeInclusionEdges is the complete-coverage class alone: a parent
	// composed of these children as complete coverage, whose money is what pays
	// for the parent's own quantity, so cover-specific logic uses this class and
	// never the merged one.
	completeInclusionEdges
	// reverseInclusionEdges is allInclusionEdges walked upward, from a node to
	// the nodes that contain it: the same relation read from the other side, not
	// a fourth relation.
	reverseInclusionEdges
)

// adjacency returns the compiled adjacency one edge class walks. Node ids are
// dense and every class is indexed by the same ids, so a snapshot declaring no
// edge of a class is simply an empty list and a walk from any node terminates
// immediately.
func (p *schemaProgram) adjacency(class edgeClass) [][]int {
	switch class {
	case completeInclusionEdges:
		return p.completeChildren
	case reverseInclusionEdges:
		return p.reverseContainment
	default:
		return p.containmentChildren
	}
}

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
	ErrSchemaSubsetContradiction = errors.New("billing: frozen schema subset quantity exceeds its parent quantity") // ErrSchemaQuantityContradiction reports an effective reduced ordinary-usage
	// component quantity strictly below zero. Every nonzero deviation from the
	// evidence is a structural contradiction: pkg/lipsdk/metering exposes no
	// component-level correction or credit marker on Measure values (only
	// metering.Fact carries FactKindCorrection, which never reaches the reduced
	// component aggregates the rater sees), so the rater cannot distinguish a
	// legitimate credit from corrupt arithmetic. It fails the valuation closed and
	// never silently clamps the quantity to zero. This is the narrow rule named by
	// review decision 7; a future SDK credit marker would be honoured here.
	ErrSchemaQuantityContradiction = errors.New("billing: frozen schema effective quantity is negative") // inclusionRelationship reports whether a frozen schema relationship declares
)

// that the parent aggregate already includes the child. Aggregate, subset and
// partition edges all carry same-unit containment semantics; a transform edge is a
// separately governed unit derivation and never an overlap.
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
// the declared children together account for the parent; a subset edge is a partial
// containment and a transform is a separately governed unit derivation.
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
//
// id is the member's compiled node id. A consumer that only needs IDENTITY
// reads program.keyStrings[id] rather than re-canonicalising key, and a consumer
// that needs the key itself for rule resolution reads key unchanged: both views
// are the same declared member, so they cannot drift.
type partitionMember struct {
	id       int
	key      metering.ComponentKey
	optional bool
}

// programCompleteMembers projects the COMPILED program's complete-coverage
// adjacency into the canonical-key shape the string-keyed consumers walk. It is
// the single projection of that adjacency: the conservation proof and the
// overlap resolver both read the one map it produces, so they cannot disagree
// about which members a parent declares, in which order, or which of them are
// optional -- and neither re-reads the snapshot's relationships. A node that
// declares no complete child is absent from the result, which is exactly the
// set of parents that can carry a cover.
//
// It runs ONCE, from compileSchemaProgram. The result is frozen and read-only
// for the life of the program, so a per-call projection was pure duplicated
// work: the same members, rebuilt and re-allocated on every rated call.
func programCompleteMembers(program *schemaProgram) map[string][]partitionMember {
	childrenByParent := make(map[string][]partitionMember, len(program.coverParents))
	for _, id := range program.coverParents {
		members := make([]partitionMember, 0, len(program.completeChildren[id]))
		for memberIndex, child := range program.completeChildren[id] {
			members = append(members, partitionMember{id: child, key: program.keyOf[child], optional: program.completeOptional[id][memberIndex]})
		}
		childrenByParent[program.keyStrings[id]] = members
	}
	return childrenByParent
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
) // completeChildPartitionCoverage returns, per reduction scope, the set of

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
// fail-closed classification for the enclosing valuation and is returned whenever
// any conflict exists. This is driven only by the explicit frozen relationship,
// never by component names or coincidental numbers.
// The two sides use different evidence sets. A complete partition is proven
// from rateableByScope: every declared partition child must be observed and have a
// rule that resolves under the effective qualifiers in the same scope, but a child
// whose effective charge is zero still completes the partition. The subset side
// uses payableByScope: only a subset with a strictly positive effective amount
// can conflict, so a zero-valued subset stays nonconflicting, exactly as a
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
// EVERYTHING DECLARED IS READ FROM THE COMPILED PROGRAM, and the walk order is
// part of the output. This function used to rebuild, on every call, its own
// string-keyed copies of the declared edges, the subset children, the complete
// members, the merged kids graph and a canonical-key table, and to walk two
// further ad-hoc adjacency maps of its own. It now reads the frozen
// schemaProgram: the declared edges in DECLARATION order, the two containment
// classes, the cover parents and the single member projection, and it answers
// every reachability question with the one compiled walk. DECLARATION order is
// preserved deliberately, because the first payable descendant a walk reaches
// is quoted verbatim in the diagnostics below; see compileSchemaProgram.
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
	for _, edge := range program.declaredEdges {
		parentKey, childKey := program.keyStrings[edge.parent], program.keyStrings[edge.child]
		for _, scope := range payableScopes {
			if _, ok := payableByScope[scope][parentKey]; !ok {
				continue
			}
			if _, ok := payableByScope[scope][childKey]; !ok {
				continue
			}
			if firstErr == nil {
				parent := program.keyOf[edge.parent]
				firstErr = fmt.Errorf("%w: schema %q %s relationship prices aggregate parent %s and included child %s in one %q direction %q unit scope",
					ErrSchemaOverlapConflict, edge.schemaID, edge.kind,
					parentKey, childKey, string(parent.Direction), parent.Unit)
			}
			record(scope, parentKey, childKey)
		}
	}
	// The direct check above inspects one inclusion edge at a time, so a chain
	// A subset B subset C hides a payable A / payable C overlap behind an
	// unpriced (or absent) middle B: the A->B and B->C pairs are not both
	// payable, yet C is declared inside A. Close it with bounded reachability
	// over exactly these inclusion edges (equal direction/unit), scoped the same
	// way; transform, cross-direction and cross-scope edges are not in the graph.
	//
	// payableDescendants reports the walk's own start too, because the start is
	// drawn from the payable set; hits[0] is therefore canonical itself and every
	// later hit is a declared descendant, in the order the walk reached them. That
	// order is what the quoted descendant is, and the compiled adjacency is built
	// in declared order for exactly that reason. The START is sorted rather than
	// declared or map-ordered: node ids are assigned in sorted canonical-key
	// order, so sortedCanonicalKeys of a payable set IS ascending node id, and
	// the quoted ancestor is a function of the input alone.
	for _, scope := range payableScopes {
		payable := payableByScope[scope]
		for _, canonical := range sortedCanonicalKeys(payable) {
			start, ok := program.byKey[canonical]
			if !ok {
				continue
			}
			hits := program.payableDescendants(allInclusionEdges, start, payable)
			if len(hits) < 2 {
				continue
			}
			direction, unit := program.keyOf[start].Direction, program.keyOf[start].Unit
			for _, descendant := range hits[1:] {
				if firstErr == nil {
					firstErr = fmt.Errorf("%w: payable component %s transitively includes payable component %s in one %q direction %q unit scope",
						ErrSchemaOverlapConflict, canonical, descendant, string(direction), unit)
				}
				record(scope, canonical, descendant)
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
	// evidenceByScope groups the full effective reduced evidence per scope, so
	// the cover resolver below is handed exactly the scope it is reasoning about
	// and never evidence from another one.
	evidenceByScope := make(map[string][]aggregateMeasure)
	for _, item := range consistencyAggregates {
		evidenceByScope[item.scopeKey] = append(evidenceByScope[item.scopeKey], item)
	}
	for _, scope := range rateableScopes {
		keys := rateableByScope[scope]
		payable := payableByScope[scope]
		presence := presenceByScope[scope]
		zeroShare := zeroShareByScope[scope]
		provenPartitions := completePartitionParents[scope]
		// The one shared cover authority for this scope. It answers the
		// structural question -- is this declared complete coverage exactly
		// resolved, and is it denied for a shared member -- so the overlap
		// resolver cannot reach a different answer from the conservation proof
		// or the interval solver about the same physical fact. An ambiguous
		// partition (a child shared with another complete parent) still proves
		// nothing about the parents that declare it, and the ambiguity stays
		// LOCAL: only those parents are denied, so an unrelated parent's cover
		// proof still stands.
		cover := resolveCompleteCovers(program, evidenceByScope[scope])
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
