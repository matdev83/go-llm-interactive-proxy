package billing

import "slices"

// OVERLAP RELATION. This file owns ONE pure predicate that answers, for any two
// POSITIVE component lines in one reduction scope, what the frozen component
// rating vocabulary says about their relationship: a definite overlap, a proven
// disjointness, or an unknown intersection.
//
// THE PREDICATE IS PRODUCTION. The overlap resolver (component_rater_overlap.go)
// reaches every pair of positive contributors a scope declares a containment
// relation between, and a relationDefiniteOverlap answer IS the structural overlap
// conflict. It previously ran as a shadow beside two ad-hoc special cases -- a
// direct per-edge both-payable check and a transitive payable-descendant walk --
// and was measured against them over 131,934 cases before they were retired. The
// two special cases are gone: the general proof is the single authority for the
// structural overlap verdict, so no walk in the resolver can disagree with the
// conservation proof, the interval solver or the cover authority about whether
// one positive region contains another.
//
// What remains test-only is the CROSS-PRODUCT census and the report rendering
// below (shadowContributingRelations, shadowOverlapPair, shadowOverlapPairKeys and
// the relation constants' name projection), which the audit bridge in
// component_rater_support_test.go drives. Every symbol here is unexported, so the
// compiled package gains no new reachable surface at all.
//
// It deliberately owns NO topology, NO cover resolution, NO payability and NO
// money. It reads the one compiled program and the one cover authority's verdict
// for the same scope, and it answers a RELATION, never a quantity: no cross
// product, no intersection value, no amount, and no implicit surcharge, so a
// containment relation between two positive lines is a conflict, not an adjustment.
// It is commercial-free by construction: it asks no question about cover
// ambiguity, unplaceable members, contributor suppression or dependency signals,
// so the two commercial special cases it is NOT a substitute for stay put.

// componentLineRelation is the predicate's answer for one pair of distinct
// positive component lines in one reduction scope. Exactly one of the three, and
// the three are exhaustive over every pair. It is deliberately NOT the production
// error class vocabulary: production decides which lines to WITHHOLD and how to
// fail a valuation closed, while this only states the structural relation.
type componentLineRelation uint8

const (
	// relationDefiniteOverlap: one region contains the other under the containment
	// closure (the transitive union of complete-coverage and subset edges). Both
	// sides carry a positive effective amount in the same reduction scope, so the
	// same underlying work would be charged twice. The component rating vocabulary
	// has no implicit surcharge to absorb it, so this is a conflict.
	relationDefiniteOverlap componentLineRelation = iota
	// relationProvenDisjoint: the frozen schema's containment forest allocates
	// the two regions separately, and that allocation is backed by a proof. Two
	// proofs exist and either is sufficient. The first is a VALIDATED COMPLETE
	// PARTITION: some cover parent whose coverage the one authority resolved places
	// a and b under two different declared complete children, so the schema's own
	// conservation equation splits them into disjoint branches. The second is FOREST
	// SEPARATION: a and b share no containment ancestor at all, so they sit under
	// two different trees of the same frozen forest. Both are structural, neither
	// is commercial, and neither asserts anything about money.
	relationProvenDisjoint
	// relationUnknownIntersection: the two regions share a containment ancestor,
	// neither contains the other, they are not the same contributor, and no
	// validated complete partition separates them into distinct branches. They
	// MAY overlap and the schema never allocated them, so the overlap is unknown.
	// Non-blocking by construction, and it carries no sentinel.
	relationUnknownIntersection
)

// name renders the relation for the shadow agreement report.
func (r componentLineRelation) name() string {
	switch r {
	case relationDefiniteOverlap:
		return "definite_overlap"
	case relationProvenDisjoint:
		return "proven_disjoint"
	case relationUnknownIntersection:
		return "unknown_intersection"
	default:
		return "unclassified"
	}
}

// shadowOverlapPair is one pair of DISTINCT positive contributors, in ascending
// node id order, with the predicate's relation. It carries a relation and
// nothing else: no amount, no intersection value, no cross product, no verdict.
type shadowOverlapPair struct {
	a, b     int
	relation componentLineRelation
}

// shadowScope is the ONE per-reduction-scope context the predicate reads: the
// frozen compiled program, the shared cover authority's verdict for exactly that
// scope, and the two reachability closures the program already compiles. The
// closures are memoized per node and built LAZILY, so a node only pays for a walk
// once it takes part in a candidate pair -- the same discipline, and for the same
// reason, as containmentUnknownIntersections. CONTRIBUTOR IDENTITY IS THE CANONICAL
// LINE, which IS the node id, so a region reachable by several declared paths is
// one contributor by construction and a redundant path can never manufacture a
// self-pair. There is no contributors set here for that reason: production's
// per-parent one exists only to subtract a charge its own payable-descendant walk
// may reach twice.
type shadowScope struct {
	program     *schemaProgram
	cover       completeCoverVerdict
	descendants []map[int]struct{}
	ancestors   []map[int]struct{}
}

// newShadowScope binds one scope's cover verdict to the compiled program.
func newShadowScope(program *schemaProgram, cover completeCoverVerdict) *shadowScope {
	count := len(program.keyOf)
	return &shadowScope{
		program:     program,
		cover:       cover,
		descendants: make([]map[int]struct{}, count),
		ancestors:   make([]map[int]struct{}, count),
	}
}

func (s *shadowScope) descendantsOf(node int) map[int]struct{} {
	if s.descendants[node] == nil {
		s.descendants[node] = s.program.reachable(allInclusionEdges, node)
	}
	return s.descendants[node]
}

func (s *shadowScope) ancestorsOf(node int) map[int]struct{} {
	if s.ancestors[node] == nil {
		s.ancestors[node] = s.program.reachable(reverseInclusionEdges, node)
	}
	return s.ancestors[node]
}

// relation is the predicate. For two DISTINCT positive component lines it returns
// exactly one of the three relations, and it is a pure function of the frozen
// program and the scope's cover verdict: no money, no payability, no quantity, no
// rule, no scope map, and no side effect. The decision order is the containment
// closure first, because it is the only branch that can be a conflict, then the
// schema's own allocation of the two regions.
func (s *shadowScope) relation(a, b int) componentLineRelation {
	relation, _ := classifySupportRelation(a, b, shadowScopeRelationQueries{scope: s})
	return relation
}

// supportRelationQueries supplies the graph facts shared by the legacy conflict
// predicate and the separately budgeted advisory predicate. A query that cannot
// finish returns assessed=false; callers must not turn that into a verdict.
type supportRelationQueries interface {
	containsRegion(container, contained int) (bool, bool)
	sharesAncestor(a, b int) (bool, bool)
	partitionSeparates(a, b int) (bool, bool)
}

type shadowScopeRelationQueries struct {
	scope *shadowScope
}

func (q shadowScopeRelationQueries) containsRegion(container, contained int) (bool, bool) {
	return q.scope.contains(container, contained), true
}

func (q shadowScopeRelationQueries) sharesAncestor(a, b int) (bool, bool) {
	s := q.scope
	ancestorsA, ancestorsB := s.ancestorsOf(a), s.ancestorsOf(b)
	for node := range len(s.program.keyOf) {
		_, inA := ancestorsA[node]
		_, inB := ancestorsB[node]
		if inA && inB {
			return true, true
		}
	}
	return false, true
}

func (q shadowScopeRelationQueries) partitionSeparates(a, b int) (bool, bool) {
	return q.scope.partitionSeparates(a, b), true
}

// classifySupportRelation is the one decision tree for structural support
// relationships. Budgeted queries use separate caches and work accounting while
// preserving the conflict path's containment, forest, partition, unknown order.
func classifySupportRelation(a, b int, queries supportRelationQueries) (componentLineRelation, bool) {
	if a == b {
		// The same contributor, and the branch that keeps a diamond from being read
		// as a self-conflict. Pair enumeration never reaches it; stated for totality.
		return relationProvenDisjoint, true
	}
	// Strict containment, either direction, over the transitive union of the two
	// containment classes. A chain that changes class partway (A subset B, B
	// partition C) is the same relation as a pure chain of one class, because it
	// is the same compiled containment graph.
	contained, assessed := queries.containsRegion(a, b)
	if !assessed {
		return relationUnknownIntersection, false
	}
	if contained {
		return relationDefiniteOverlap, true
	}
	contained, assessed = queries.containsRegion(b, a)
	if !assessed {
		return relationUnknownIntersection, false
	}
	if contained {
		return relationDefiniteOverlap, true
	}
	// Neither contains the other. The two are either under separate trees of the
	// frozen containment forest, which the schema allocates separately, or under a
	// shared ancestor, in which case only a validated complete partition can prove
	// them apart. The shared-ancestor test scans ASCENDING NODE ID rather than
	// ranging a set, so no map iteration order can reach the answer even as work.
	shared, assessed := queries.sharesAncestor(a, b)
	if !assessed {
		return relationUnknownIntersection, false
	}
	if !shared {
		return relationProvenDisjoint, true
	}
	separated, assessed := queries.partitionSeparates(a, b)
	if !assessed {
		return relationUnknownIntersection, false
	}
	if separated {
		return relationProvenDisjoint, true
	}
	return relationUnknownIntersection, true
}

// contains reports whether one region is held inside the other under the
// containment closure. It is the ONE expression relation's definite-overlap branch
// is made of, exposed so a consumer that must ORIENT such an answer -- naming the
// containing line first -- reads this memoized closure, not a walk of its own.
func (s *shadowScope) contains(container, contained int) bool {
	_, held := s.descendantsOf(container)[contained]
	return held
}

// overlapCandidates appends to into, in ASCENDING node id order, every positive
// contributor the containment closure relates to positive[index] in EITHER
// direction at a HIGHER index than it. The result is a CANDIDATE SET, never a
// verdict, and it is an EFFICIENT ENUMERATOR rather than a second authority: it
// reads the two memoized closures relation already reads, and whether a candidate
// pair is a conflict, a proven disjointness or an unknown intersection is still
// relation's answer alone.
//
// COVERAGE IS A PROOF, NOT A HOPE. relation answers definiteOverlap exactly when
// one side lies in the other's descendants closure, and descendantsOf and
// ancestorsOf are the two directions of that ONE closure over the ONE compiled
// adjacency, so the set of pairs relation calls a definite overlap IS the set of
// pairs this enumerates. The higher-index filter emits each unordered pair once
// from its lower endpoint, the same i < j discipline the cross product used, so
// the emission order -- and the first quoted conflict -- is unchanged. The two
// answers this walk never reaches are the two non-blocking ones, so no unreached
// pair can withhold a line.
func (s *shadowScope) overlapCandidates(positive []int, index int, into []int) []int {
	lower := positive[index]
	into = into[:0]
	for _, closure := range [...]map[int]struct{}{s.descendantsOf(lower), s.ancestorsOf(lower)} {
		for candidate := range closure {
			// The one filter the cross product's i < j loop applied. The positive list
			// is ascending node id, so membership is a binary search, not a set.
			if candidate <= lower {
				continue
			}
			if _, carriesMoney := slices.BinarySearch(positive, candidate); carriesMoney {
				into = append(into, candidate)
			}
		}
	}
	slices.Sort(into)
	return into
}

// partitionSeparates reports whether some cover parent whose coverage the ONE
// authority RESOLVED places a and b under two different declared complete
// children. Resolved is the authority's PROOF verdict -- summed AND
// unambiguously owned -- not merely "the parent reported a quantity", because a
// parent with its own exact number proves nothing about how its declared children
// divide it. That is the conservative direction: the predicate claims
// provenDisjoint only when it can, so a future authority could never suppress
// money on a claim the evidence does not carry.
//
// A region belongs to the branch of the child that CONTAINS it, and that includes
// the declared child ITSELF. This is deliberately one step wider than
// separatedByValidatedPartition, which asks only whether the region is a STRICT
// descendant of the child and therefore never places a declared child in its own
// branch. The wider reading is the correct one for a relation, and it is the SAFE
// direction for money -- provenDisjoint is non-blocking, so separating two regions
// the evidence does place apart can never suppress a line. Production's
// unknown-intersection diagnostic keeps the narrower reading. The cover parents
// are walked in ascending node id and each child list is the sorted-id compile
// order, so the first separating parent is a function of the program alone.
func (s *shadowScope) partitionSeparates(a, b int) bool {
	for _, parent := range s.program.coverParents {
		if !s.cover.resolved[parent] {
			continue
		}
		children := s.program.completeChildren[parent]
		if len(children) < 2 {
			continue
		}
		branchA, branchB := -1, -1
		for _, child := range children {
			if branchA == -1 && s.inBranch(child, a) {
				branchA = child
			}
			if branchB == -1 && s.inBranch(child, b) {
				branchB = child
			}
		}
		if branchA != -1 && branchB != -1 && branchA != branchB {
			return true
		}
	}
	return false
}

// inBranch reports whether region is the declared child itself or one of its
// contained descendants.
func (s *shadowScope) inBranch(child, region int) bool {
	if child == region {
		return true
	}
	_, contained := s.descendantsOf(child)[region]
	return contained
}

// shadowPositiveContributors lists one scope's positive component lines as
// ascending node ids, which IS ascending canonical-key order and the compiled
// order rather than a map range. A positive line the declared graph never mentions
// is not a region of this graph at all, so it is not returned; and a fixed fee is
// not a component line and never reaches this predicate, because the compiled
// program holds component relationships only.
func shadowPositiveContributors(program *schemaProgram, payable map[string]struct{}) []int {
	count := len(program.keyOf)
	positive := make([]int, 0, len(payable))
	for node := range count {
		if _, carriesMoney := payable[program.keyStrings[node]]; carriesMoney {
			positive = append(positive, node)
		}
	}
	return positive
}

// shadowContributingRelations is the whole shadow answer for one reduction scope:
// the relation of every DISTINCT pair of positive contributors, in ascending node
// id order, so the pair list is a function of the population alone. It returns
// nothing at all for fewer than two positive contributors, and it reads the
// compiled program and the scope's cover verdict and nothing else.
func shadowContributingRelations(
	program *schemaProgram,
	cover completeCoverVerdict,
	payable map[string]struct{},
) []shadowOverlapPair {
	positive := shadowPositiveContributors(program, payable)
	if len(positive) < 2 {
		return nil
	}
	scope := newShadowScope(program, cover)
	pairs := make([]shadowOverlapPair, 0, len(positive)*(len(positive)-1)/2)
	for i := 0; i < len(positive); i++ {
		for j := i + 1; j < len(positive); j++ {
			pairs = append(pairs, shadowOverlapPair{
				a:        positive[i],
				b:        positive[j],
				relation: scope.relation(positive[i], positive[j]),
			})
		}
	}
	return pairs
}

// shadowOverlapPairKeys renders a pair's identities through the program's
// compiled key table, so the test-only bridge never re-derives identity from a
// canonical string and the "identity is a slice index" rule still holds.
func shadowOverlapPairKeys(program *schemaProgram, pair shadowOverlapPair) (string, string) {
	return program.keyStrings[pair.a], program.keyStrings[pair.b]
}
