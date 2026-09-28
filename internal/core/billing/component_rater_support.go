package billing

// SHADOW OVERLAP RELATION. This file owns ONE pure predicate that answers, for
// any two POSITIVE component lines in one reduction scope, what the frozen
// component rating vocabulary says about their relationship: a definite overlap,
// a proven disjointness, or an unknown intersection.
//
// IT IS A SHADOW. Nothing in production calls it. It is not wired into the
// overlap resolver, the cover authority, the quantity solver, the rating loop or
// any diagnostic, so it can change no verdict, no completeness, no error class
// and no amount. It exists to be MEASURED against the four production special
// cases before any of them is retired, and its only caller is the test-only
// bridge in component_rater_support_test.go. Every symbol here is unexported, so
// the compiled package gains no new reachable surface at all.
//
// It deliberately owns NO topology, NO cover resolution, NO payability and NO
// money. It reads the one compiled program (component_rater_schema_program.go)
// and the one cover authority's verdict for the same scope
// (component_rater_cover.go), and it answers a RELATION, never a quantity: it
// computes no cross product, no intersection value, no amount and no surcharge.
// There is no implicit surcharge anywhere in the component rating vocabulary, so
// a containment relation between two positive lines is a conflict, not an
// adjustment.

// componentLineRelation is the shadow predicate's answer for one pair of
// distinct positive component lines in one reduction scope. Exactly one of the
// three, and the three are exhaustive over every pair.
//
// It is deliberately NOT the production error class vocabulary. Production
// decides which lines to WITHHOLD and how to fail a valuation closed; this only
// states the structural relation, so it can be compared against production
// without importing any of production's commercial rules.
type componentLineRelation uint8

const (
	// relationDefiniteOverlap: one region contains the other under the
	// containment closure (the transitive union of complete-coverage and subset
	// edges). Both sides carry a positive effective amount in the same reduction
	// scope, so the same underlying work would be charged twice. The component
	// rating vocabulary has no implicit surcharge to absorb it, so this is a
	// conflict.
	relationDefiniteOverlap componentLineRelation = iota
	// relationProvenDisjoint: the frozen schema's containment forest allocates
	// the two regions separately, and that allocation is backed by a proof. Two
	// proofs exist and either is sufficient. The first is a VALIDATED COMPLETE
	// PARTITION: some cover parent whose coverage the one authority resolved
	// places a and b under two different declared complete children, so the
	// schema's own conservation equation splits them into disjoint branches. The
	// second is FOREST SEPARATION: a and b share no containment ancestor at all,
	// so they sit under two different trees of the same frozen forest and the
	// schema declares no relation between them. Both are structural, neither is
	// commercial, and neither asserts anything about money.
	relationProvenDisjoint
	// relationUnknownIntersection: the two regions share a containment ancestor,
	// neither contains the other, they are not the same contributor, and no
	// validated complete partition separates them into distinct branches. They
	// MAY overlap and the schema never allocated them, so the overlap is unknown.
	// This answer is non-blocking by construction and carries no sentinel: a
	// future authority would report it and change no money.
	relationUnknownIntersection
)

// name renders the relation for the shadow agreement report. It is a projection
// used only by the test-only bridge.
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
// closures are memoized per node and built LAZILY, so a node only pays for a
// walk once it takes part in a candidate pair -- the same discipline, and for the
// same reason, as containmentUnknownIntersections.
//
// There is no contributors set here, and that is deliberate. Contributor identity
// in this predicate is the canonical component line itself, which IS the node id,
// so a region reachable by several declared paths is one contributor by
// construction and a redundant path can never manufacture a self-pair. The
// production per-parent contributors set exists only because production walks
// PATHS (payableDescendants reports the walk order for its quoted diagnostic) and
// therefore has to subtract a charge it may reach twice; a relation over
// identities never has to.
type shadowScope struct {
	program     *schemaProgram
	cover       completeCoverVerdict
	descendants []map[int]struct{}
	ancestors   []map[int]struct{}
}

// newShadowScope binds one scope's cover verdict to the compiled program. The
// cover verdict is the one authority's; this never re-derives whether a declared
// complete coverage resolves.
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
// rule, no scope map, and no side effect.
//
// The decision order is the containment closure first, because it is the only
// branch that can be a conflict, then the schema's own allocation of the two
// regions.
func (s *shadowScope) relation(a, b int) componentLineRelation {
	if a == b {
		// The same contributor. A region never intersects itself, and a redundant
		// declared path reaches the SAME node id, so this is the branch that keeps
		// a diamond from being read as a self-conflict. Pair enumeration never
		// reaches it; it is stated so the answer is total.
		return relationProvenDisjoint
	}
	// Strict containment, either direction, over the transitive union of the two
	// containment classes. A chain that changes class partway (A subset B, B
	// partition C) is the same relation as a pure chain of one class, because it
	// is the same compiled containment graph.
	if _, contained := s.descendantsOf(a)[b]; contained {
		return relationDefiniteOverlap
	}
	if _, containing := s.descendantsOf(b)[a]; containing {
		return relationDefiniteOverlap
	}
	// Neither contains the other. The two are either under separate trees of the
	// frozen containment forest, which the schema allocates separately, or under a
	// shared ancestor, in which case only a validated complete partition can prove
	// them apart. The shared-ancestor test scans ASCENDING NODE ID rather than
	// ranging a set, so no map iteration order can reach the answer even as work.
	ancestorsA, ancestorsB := s.ancestorsOf(a), s.ancestorsOf(b)
	shared := false
	for node := range len(s.program.keyOf) {
		_, inA := ancestorsA[node]
		_, inB := ancestorsB[node]
		if inA && inB {
			shared = true
			break
		}
	}
	if !shared {
		return relationProvenDisjoint
	}
	if s.partitionSeparates(a, b) {
		return relationProvenDisjoint
	}
	return relationUnknownIntersection
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
// branch. The wider reading is the correct one for a relation ("these two sit in
// distinct branches of a partition that is proven"), and it is the SAFE direction
// for money -- provenDisjoint is non-blocking, so separating two regions the
// evidence does place apart can never suppress a line. Production's
// unknown-intersection diagnostic keeps the narrower reading, and this predicate
// does not touch it.
//
// The cover parents are walked in the compiled ascending node id order, and each
// child list is the sorted-id compile order, so the first separating parent is a
// function of the program alone.
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
// ascending node ids.
//
// Node id IS the canonical component key's position in the compiled program, so
// the returned list is the CONTRIBUTOR IDENTITY set in ASCENDING CANONICAL-KEY
// ORDER: a region reachable by several declared paths is one entry, and the
// ordering is the compiled one rather than a map range. A positive line the
// declared graph never mentions is not a region of this graph at all, so it is
// not returned; and a fixed fee is not a component line and never reaches this
// predicate, because the compiled program holds component relationships only.
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
// nothing at all for fewer than two positive contributors, because a region
// cannot intersect itself.
//
// It reads the compiled program and the scope's cover verdict and nothing else. It
// performs no schema scan, no re-canonicalisation and no pricing.
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
// compiled key table. It exists so the test-only bridge never has to re-derive
// identity from a canonical string, and it keeps the "identity is a slice index"
// rule that the rest of the machine already follows.
func shadowOverlapPairKeys(program *schemaProgram, pair shadowOverlapPair) (string, string) {
	return program.keyStrings[pair.a], program.keyStrings[pair.b]
}
