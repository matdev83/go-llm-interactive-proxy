package billing

// ONE COMPILED PROGRAM IS THE WHOLE SCHEMA AUTHORITY. This file owns PROGRAM
// CONSTRUCTION and nothing else: the integer node ids, the canonical keyStrings
// table, the two containment classes and their reverse, the declared edge list,
// the topological order, the propagation depth, the cover parents, the single
// canonical-key member projection, and the one int-indexed reachability walk
// (reachable, payableDescendants) the rest of the machine reads.
// compileSchemaProgram runs once, from NewReferenceRater, over the rater's own
// canonicalized snapshot copy, so a Rate call performs NO schema scan and NO
// re-canonicalisation of a declared component key: it reads integer node ids and
// the compiled keyStrings table, and anything that needs IDENTITY reaches it
// through program.keyStrings[id] or program.byKey. Every reachability question
// is asked of the one walk here, edgeClass selecting which adjacency it may
// cross.
//
// This file deliberately owns NO evidence, money or verdict logic. The quantity
// solver, the cover authority and the overlap resolver are the other three files
// of this machine; each reads this program instead of rediscovering declared
// topology, and none of them compiles anything.

import (
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// schemaProgram is the frozen component-schema inclusion graph compiled ONCE at
// rater construction into integer node ids. Every later Rate call reads the
// compiled program; it never re-walks canonical string keys or rediscovers
// topology. It is built from the rater's private, canonicalized snapshot copy,
// so a caller mutation cannot change compiled semantics.
//
// "Containment" is the union of complete-coverage and subset edges: for every
// containment edge the child is contained in the parent, and the relation is
// transitive. Every edge here already shares economic direction and unit, so all
// traversal is direction- and unit-safe by construction.
type schemaProgram struct {
	byKey map[string]int
	keyOf []metering.ComponentKey
	// keyStrings is keyOf's canonical JSON identity, indexed by the same node id,
	// so a per-id identity is a slice index rather than a re-marshalling. A
	// canonical key is a json.Marshal of the WHOLE key, so deriving one inside a
	// pair loop or a per-parent dedup scan costs a full canonicalisation per
	// iteration and makes both quadratic in the declared graph size.
	keyStrings          []string
	topo                []int
	completeChildren    [][]int
	completeOptional    [][]bool
	subsetChildren      [][]int
	containmentChildren [][]int
	reverseContainment  [][]int
	completeOwner       []int
	// sharedCompleteOwner names the nodes declared as a complete-coverage child
	// by TWO DISTINCT complete parents. It is separate from completeOwner because
	// -1 there means both "no complete parent at all" and "ambiguously shared", and
	// the cover resolver must tell those apart: a leaf against a positive ambiguity
	// claim.
	sharedCompleteOwner []bool
	// propagationDepth is the longest containment path in EDGES, computed ONCE
	// here; see propagateQuantityIntervals for the sweep bound derived from it.
	propagationDepth int
	// declaredEdges is every inclusion edge the snapshot declares, in canonical
	// DECLARATION order, with both endpoints already resolved to node ids. A
	// per-call consumer that needs the declared relationships -- the excluded-child
	// check -- therefore reads declared topology as data instead of rescanning
	// the snapshot's relationships and re-canonicalising every endpoint on every
	// call. It carries no per-edge schema identity or relationship kind, because
	// the overlap resolver's structural verdict is a relation between two
	// component lines and quotes no per-edge attribution.
	declaredEdges []programEdge
	// coverParents lists, in ascending node id, the compiled nodes that declare
	// at least one complete-coverage member. Ascending id IS the order a per-call
	// sort of the canonical parent keys produced, so the first conflict diagnostic
	// stays deterministic without rebuilding that list per scope.
	coverParents []int
	// membersByParent is the ONE projection of the compiled complete-coverage
	// adjacency into the canonical-key shape the string-keyed consumers walk.
	// It is built here, once, so the conservation proof and the overlap resolver
	// cannot disagree about which members a parent declares, in which order, or
	// which of them are optional -- and neither re-reads the snapshot's
	// relationships, so topology is discovered exactly once, at construction.
	membersByParent map[string][]partitionMember
}

// programEdge is one declared inclusion edge in compiled form. The zero value is
// never used: every entry comes from compileSchemaProgram, which resolves both
// endpoints to ids.
type programEdge struct {
	parent int
	child  int
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
				parent: parent, child: child, parentKey: parentKey, childKey: childKey,
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
	// the sort, in DECLARATION order, which is the order the payability walks
	// have always visited. Every consumer of containment reads a SET or a
	// maximum -- the topological order, the longest path, the relation proof's
	// closures and the unknown-intersection pairs -- so the order is invisible in
	// the answer, and is retained only so no walk's traversal can drift.
	program.declaredEdges = make([]programEdge, 0, len(rawEdges))
	containmentPairs := make(map[[2]int]struct{}, len(rawEdges))
	for _, edge := range rawEdges {
		parent, child := program.byKey[edge.parentKey], program.byKey[edge.childKey]
		program.declaredEdges = append(program.declaredEdges, programEdge{parent: parent, child: child})
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
// It reports the WALK ORDER rather than a set, so a caller that quotes a hit
// quotes a deterministic one; every consumer folds the result into a set or checks
// only its emptiness, so the order is not load-bearing today.
//
// Reach matters, not adjacency: a member represented two complete-coverage hops
// below an unobserved parent is still money on that parent's side, and it is
// exactly the case a direct-child test cannot see. It reports identities rather
// than node ids because every consumer wants the identity: the payable and
// contributor sets are identity-keyed and the diagnostic quotes it, and the
// identity is the compiled keyStrings entry rather than a re-marshalled key.
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

// inclusionRelationship reports whether a frozen schema relationship declares
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
