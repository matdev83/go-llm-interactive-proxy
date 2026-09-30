//go:build integration

package billing_test

// GENERATED model-based extension of the three-node exhaustive schema sweep to
// FOUR and FIVE nodes.
//
// WHY THIS FILE EXISTS. billing_schema_model_test.go enumerates the complete
// 7^3 = 343 three-node topology space against the independent oracle in
// internal/testkit/billsem. Three nodes can only express a contradiction class
// whose deepest load-bearing path is two edges long, so every contradiction that
// first becomes reachable at depth three (a bound that has to propagate through
// two complete covers before it meets an absent member) or at width three (three
// complete owners of one quantity) is INVISIBLE to it. This file extends the same
// differential contract to those depths and widths so such a class is found by
// generation rather than by the next reviewer.
//
// Node order is fixed (n1 < n2 < ... < nn) and every declared edge goes from a
// lower index to a higher one, so every generated graph is forward and therefore
// ACYCLIC BY CONSTRUCTION. Every graph is still run through the public
// metering.ValidateComponentSchemas validator, and a graph it rejects is SKIPPED
// and COUNTED rather than failed: a publication-rejected graph is a real
// production decision, not a defect in this file's generator.
//
// SHAPE OVER-REPRESENTATION. Uniform sampling over 7 shapes per slot already
// over-represents dense graphs (six of the seven shapes are non-empty), but it
// would almost never produce the specific structures that carry the interesting
// arithmetic. So the population is built in two declared parts:
//
//   - CURATED: a hand-declared, fully enumerable set of graph families, one
//     variant per line, over-representing nested complete covers, mixed
//     complete/subset chains, diamonds, fan-out, fan-in, optional leaves,
//     redundant duplicate containment paths, shared descendants under sibling
//     branches, disconnected regions and transform-only regions.
//   - SEEDED: a uniform draw over the same 7 shapes per edge slot, from a FIXED,
//     hard-coded seed. No time-based, environment-based or per-run seeding, so
//     the population is byte-identical on every run and on every machine.
//
// Both parts are REPORTED SEPARATELY, per family, with the drawn / accepted /
// validator-skipped counts, so the structural coverage claim is checkable
// rather than asserted.
//
// EDGE CONSTRAINT (the deliberate bound that keeps the space finite and the
// runtime sane). The edge slot set is every ordered pair (p, c) with p < c:
//
//	n=4 ->  6 slots  (n1n2 n1n3 n1n4 n2n3 n2n4 n3n4)
//	n=5 -> 10 slots  (n1n2 n1n3 n1n4 n1n5 n2n3 n2n4 n2n5 n3n4 n3n5 n4n5)
//
// Each slot independently takes one of the same seven shapes the three-node
// sweep uses: none, subset, partition-required, partition-optional,
// aggregate-required, aggregate-optional, transform. The bound is:
//
//	n=4: at most  6 non-empty edges, i.e. all 6 slots -- UNCONSTRAINED
//	n=5: at most  6 non-empty edges, i.e. at least four node pairs UNDECLARED
//
// The 7^6 = 117,649 four-node space and the 7^10 = 282,475,249 five-node space
// are both enumerated far beyond any wall-time budget, so the finite part is the
// DRAW BUDGET below, not the shape space. The n=5 bound is the only shape
// restriction; it is applied by REJECTION SAMPLING (a draw is kept only when it
// satisfies the bound, and the stream is advanced until the draw budget is met),
// so it does not bias any surviving shape toward sparse or dense. The bound is
// six rather than larger because the enforced complete-sibling topology rules
// reject a dense random five-node graph almost always: measured over 60 uniform
// draws from this seed, 42/60 candidates were publishable at five edges, 30/60 at
// six, 18/60 at seven, 12/60 at eight and 0/10 at nine. The measured yield of
// every bound is reported per run by the FAMILY seeded_uniform line.
//
// DRAW BUDGET (all of it reported in every summary line):
//
//	structure pass, n=4:  every curated 4-node family + 24 seeded candidate draws
//	structure pass, n=5:  every curated 5-node family + 24 seeded candidate draws
//	order pass, n=4/n=5:  the same populations as the structure pass
//	commercial pass:       the curated 4-node and 5-node families with ONE
//	                       accepted graph per family, then an EVEN STRIDE over
//	                       that family list: all 10 families at n=4 and 5 of the
//	                       10 at n=5. This is a DELIBERATE, REPORTED graph-count
//	                       reduction and is the only reduction applied anywhere.
//	                       The evidence product is NEVER subsampled: every graph
//	                       is driven through the FULL 5^n evidence space in every
//	                       pass.
//	A4 order pass:          four NAMED evidence assignments per node count, not
//	                       the full product. A4 is a transformation property over
//	                       representative evidence, and the exhaustive evidence
//	                       coverage belongs to the structure and commercial passes.
//
// EVIDENCE. Per node, one of five states: absent, exact 0, exact 1, exact 2,
// unavailable. The structure pass uses the FULL 5^n product (625 at n=4, 3125
// at n=5) with an EXPLICIT-FREE (unit price "0") rule on every node, so nothing
// is ever payable and STRUCTURE is isolated from MONEY. The commercial pass
// drives the SAME full evidence product under the same three tariff states the
// metamorphic suite uses: paid, explicit free, and no rule at all.
//
// EVIDENCE IS BUILT ONCE PER NODE COUNT. billsem.Evidence and
// metering.Observation depend only on the node keys and the evidence states, not
// on the graph, so the whole 5^n product is materialised once per node count and
// shared by every graph. That removes the per-case map and rational allocation
// from the inner loop without changing what is executed: one billsem.Solve and
// one production rating per (graph, evidence, seam).
//
// ASSERTIONS (the same contract the three-node sweep holds at A1 = 0 and
// A2_illegitimate = 0):
//
//	A1  safety.     oracle quantity contradiction => production must NOT return
//	                Complete.
//	A2  clause 1.   an absent or unavailable node never emits a positive rated
//	                line.
//	A2  clause 2.   TARIFF-AWARE, exactly as in the three-node sweep: the oracle
//	                is deliberately commercial-free, so "oracle Incomplete AND
//	                production Complete" is CLASSIFIED, not failed. It is
//	                legitimate when no declared REQUIRED complete member that is
//	                missing or unavailable carries an applicable non-free rule
//	                (nothing money-bearing could have been hidden); it is
//	                ILLEGITIMATE, and a hard failure, when at least one such
//	                member does carry one.
//	A3             a transform edge never contributes containment: on a
//	                transform-only graph a child quantity greater than its
//	                parent's is NOT a quantity contradiction, in production AND
//	                in the oracle. Paired with a same-shape subset control that
//	                MUST contradict in both, so the check has teeth.
//	A4             declaration-order invariance: reversing the relationship
//	                slice (and appending an empty second schema) changes neither
//	                the typed error class, nor the completeness, nor the emitted
//	                component identities, nor the payable total.
//	A5             a cross-direction containment edge and a cross-unit
//	                transform edge are INERT IDENTICALLY IN BOTH: production and
//	                the oracle each return, for the graph carrying the odd edge,
//	                exactly the verdict they return for the same graph with that
//	                one edge removed. Paired with a same-direction same-unit
//	                control that MUST contradict in both.
//
// A6 is reported rather than failed, and it is the STRICTNESS accounting:
// production-stricter (production not Complete while the oracle reports no
// contradiction) is ACCEPTABLE, because production carries commercial rules the
// structural oracle deliberately has none of. Oracle-stricter is a real finding
// and is exactly A1 failures plus A2 illegitimate cases; both are counted
// separately and reported.
//
// SEAMS. Every assertion in this file runs through BOTH seams of
// review5beSeams -- the operator/local E seam and the customer-policy R seam --
// and A1, A2 clause 1, A2 clause 2 and A3 also run on every graph, every
// evidence assignment and every tariff, not on a sample. A4 and A5 are the two
// properties that compare two production outcomes or a production outcome
// against a stripped one, so there is nothing for a second seam to change; they
// are still driven on both, and a cross-seam disagreement is not silently
// tolerated: the tuples are compared seam-for-seam against the same declaration.
//
// DETERMINISM. Every shared counter is an atomic, every diagnostic map is behind
// a mutex, no counter is derived from wall time, map iteration or completion
// order, and each pass emits one canonical TALLY line built from sorted names.
// Running the file three times must produce three byte-identical sets of eight
// TALLY lines.

import (
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/billsem"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ---------------------------------------------------------------------------
// Configuration. Every number here is reported by the summary line of the pass
// it governs, so the cost of this file is stated by the file itself.
// ---------------------------------------------------------------------------

const (
	// gmxSeed is the single FIXED seed. It is deliberately a literal, never a
	// time.Now(), a hash of the clock, or an environment value, so the seeded
	// half of the population is byte-identical on every run and every machine.
	// Each node count is drawn from its OWN stream seeded with this same
	// constant, so the four-node population does not depend on how many draws the
	// five-node population consumed.
	gmxSeed int64 = 20260927

	// gmxMaxNodes is the widest graph this file generates.
	gmxMaxNodes = 5

	// gmxMaxEdges4 / gmxMaxEdges5 are the EDGE CONSTRAINT, in non-empty edges,
	// applied by rejection sampling over the raw uniform draw.
	//
	//	n=4: at most 6 of 6 slots -- UNCONSTRAINED (every four-node graph is
	//	     eligible; the only bound that keeps this finite is the draw budget).
	//	n=5: at most 6 of 10 slots -- at least four node pairs UNDECLARED.
	//
	// The n=5 bound is not arbitrary. Measured over 60 uniform draws from this
	// seed, the fraction of drawn candidates the publication validator ACCEPTS
	// falls steeply as the bound rises: 42/60 at five edges, 30/60 at six, 18/60
	// at seven, 12/60 at eight, and 0/10 at nine. The complete-sibling topology
	// rules (enforced at publication) reject a dense random five-node graph almost
	// always, so an unconstrained draw contributes nothing at all. Six is a bound
	// that still yields half the candidates, and it is reported on every summary
	// line rather than buried here.
	gmxMaxEdges4 = 6
	gmxMaxEdges5 = 6

	// Seeded draw budgets per node count, per pass. Curated graphs are never
	// budgeted: every curated family is generated in full. The budget is a budget
	// of DRAWN candidates, not of accepted graphs, so a run that loses more
	// candidates to the publication validator executes a smaller accepted
	// population; the accepted number is reported alongside the drawn one.
	gmxSeededStructure4 = 24
	gmxSeededStructure5 = 24

	// The commercial pass is a DELIBERATE, REPORTED graph-count reduction: it
	// keeps one accepted graph per family and then selects `budget` of those
	// families by even stride across the family list. The evidence product is
	// never subsampled.
	gmxCommercialFamilies4 = 10
	gmxCommercialFamilies5 = 5
)

// gmxMaxEdgesFor is the edge constraint for a node count, in one place so the
// summary line and the generator can never disagree about it.
func gmxMaxEdgesFor(nodes int) int {
	if nodes >= 5 {
		return gmxMaxEdges5
	}
	return gmxMaxEdges4
}

// gmxNodeKeys is the per-node component key table. Node i owns key i, so a
// generated edge slot (p, c) with p < c is always a forward edge.
var gmxNodeKeys = [gmxMaxNodes]metering.ComponentKey{
	r7Key("vendor:gmx_n1"),
	r7Key("vendor:gmx_n2"),
	r7Key("vendor:gmx_n3"),
	r7Key("vendor:gmx_n4"),
	r7Key("vendor:gmx_n5"),
}

// ---------------------------------------------------------------------------
// Edge slots and graph construction.
// ---------------------------------------------------------------------------

// gmxSlotCount is the number of forward slots among n nodes: every ordered pair
// (p, c) with p < c, which is what makes acyclicity structural.
func gmxSlotCount(n int) int { return n * (n - 1) / 2 }

// gmxSlotAt returns the i-th forward slot of an n-node graph in lexicographic
// (parent, then child) order, so a slot index is a stable identity for a draw.
func gmxSlotAt(n, i int) [2]int {
	for parent := range n {
		for child := parent + 1; child < n; child++ {
			if i == 0 {
				return [2]int{parent, child}
			}
			i--
		}
	}
	panic(fmt.Sprintf("gmxSlotAt: index out of range for n=%d", n))
}

// gmxGraph is one generated declaration: a per-slot shape vector plus the family
// it was drawn from. kinds is indexed by slot, so two graphs are equal exactly
// when their shape vectors are equal, which is what makes deduplication a plain
// string compare.
type gmxGraph struct {
	name   string
	family string
	origin string
	nodes  int
	kinds  []smEdgeKind
}

func (g gmxGraph) edges() int {
	total := 0
	for _, kind := range g.kinds {
		if kind != smNone {
			total++
		}
	}
	return total
}

func (g gmxGraph) code() string {
	var b strings.Builder
	for _, kind := range g.kinds {
		fmt.Fprintf(&b, "%d", int(kind))
	}
	return b.String()
}

// gmxShapes renders the shape vector in the same vocabulary the three-node sweep
// uses, so a RED message is readable without the shape code.
func (g gmxGraph) shapes() string {
	names := make([]string, 0, len(g.kinds))
	for i, kind := range g.kinds {
		if kind == smNone {
			continue
		}
		slot := gmxSlotAt(g.nodes, i)
		names = append(names, fmt.Sprintf("%s->%s:%s", gmxNodeName(slot[0]), gmxNodeName(slot[1]), kind))
	}
	if len(names) == 0 {
		return "<no-edges>"
	}
	return strings.Join(names, " ")
}

func gmxNodeName(index int) string { return fmt.Sprintf("n%d", index+1) }

func (g gmxGraph) String() string { return g.family + "/" + g.name + "{" + g.shapes() + "}" }

// gmxEdge is one declared edge over node INDICES, 0-based, always parent < child.
type gmxEdge struct {
	parent int
	child  int
	kind   smEdgeKind
}

// gmxMake materialises a graph from a declared edge list. Every slot the list
// does not mention is smNone, so two variants of one family differ in exactly the
// edges they declare.
func gmxMake(nodes int, family, name string, edges ...gmxEdge) gmxGraph {
	graph := gmxGraph{
		name:   name,
		family: family,
		origin: "curated",
		nodes:  nodes,
		kinds:  make([]smEdgeKind, gmxSlotCount(nodes)),
	}
	for _, edge := range edges {
		if edge.parent >= edge.child {
			panic(fmt.Sprintf("gmxMake: edge %d->%d is not forward", edge.parent, edge.child))
		}
		index := 0
		found := false
		for parent := 0; parent < nodes && !found; parent++ {
			for child := parent + 1; child < nodes; child++ {
				if parent == edge.parent && child == edge.child {
					found = true
					break
				}
				index++
			}
		}
		if !found {
			panic(fmt.Sprintf("gmxMake: no slot for %d->%d in an n=%d graph", edge.parent, edge.child, nodes))
		}
		graph.kinds[index] = edge.kind
	}
	return graph
}

// gmxRelationships materialises a graph's declared edges. smRelationship is the
// three-node sweep's own shape-to-edge builder, so the seven shapes mean exactly
// the same thing here.
func gmxRelationships(g gmxGraph) []metering.ComponentRelationship {
	rels := make([]metering.ComponentRelationship, 0, len(g.kinds))
	for i, kind := range g.kinds {
		slot := gmxSlotAt(g.nodes, i)
		if rel, ok := smRelationship(kind, gmxNodeKeys[slot[0]], gmxNodeKeys[slot[1]]); ok {
			rels = append(rels, rel)
		}
	}
	return rels
}

// ---------------------------------------------------------------------------
// Curated graph families.
//
// Every line is one VARIANT of the family, not one graph: a family with five
// lines yields five graphs. A line that the publication validator rejects is kept
// in the list on purpose, so the rejected population is enumerated and COUNTED
// rather than being designed away.
// ---------------------------------------------------------------------------

type gmxFamily struct {
	name  string
	edges [][]gmxEdge
}

func (f gmxFamily) graphs(nodes int) []gmxGraph {
	out := make([]gmxGraph, 0, len(f.edges))
	for i, edges := range f.edges {
		out = append(out, gmxMake(nodes, f.name, fmt.Sprintf("%s%02d", shortFamily(f.name), i), edges...))
	}
	return out
}

// shortFamily shortens a family name for a variant label so a subtest name stays
// readable at five nodes.
func shortFamily(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "complete_", "c"), "_", "")
}

func gmxCuratedFamilies(nodes int) []gmxFamily {
	if nodes == 4 {
		return gmxCuratedFamilies4()
	}
	return gmxCuratedFamilies5()
}

func gmxCuratedFamilies4() []gmxFamily {
	// Node roles in these declarations, stated once so the variants read as the
	// shapes they are: n1 is the root, n2/n3/n4 its declared members.
	return []gmxFamily{
		{name: "nested_complete_cover", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smAggregateRequired}},
			{{0, 1, smAggregateRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionRequired}},
			{{0, 1, smAggregateRequired}, {1, 2, smAggregateRequired}, {1, 3, smAggregateRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {0, 2, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionOptional}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionOptional}, {1, 3, smPartitionOptional}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {0, 3, smSubset}},
		}},
		{name: "mixed_complete_subset_chain", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {2, 3, smSubset}},
			{{0, 1, smAggregateRequired}, {1, 2, smSubset}, {2, 3, smPartitionRequired}},
			{{0, 1, smSubset}, {1, 2, smPartitionRequired}, {2, 3, smSubset}},
			{{0, 1, smSubset}, {1, 2, smSubset}, {2, 3, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {2, 3, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {1, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {1, 3, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smTransform}, {2, 3, smSubset}},
			{{0, 1, smSubset}, {1, 2, smSubset}, {2, 3, smSubset}},
		}},
		{name: "diamond", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smPartitionRequired}},
			{{0, 1, smSubset}, {0, 2, smSubset}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smSubset}, {1, 3, smPartitionRequired}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smSubset}, {2, 3, smPartitionRequired}},
			{{0, 1, smSubset}, {0, 2, smPartitionRequired}, {1, 3, smSubset}, {2, 3, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smPartitionOptional}, {2, 3, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smSubset}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {1, 3, smSubset}},
		}},
		{name: "fan_out", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}},
			{{0, 1, smSubset}, {0, 2, smSubset}, {0, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionOptional}, {0, 3, smPartitionOptional}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smSubset}, {0, 3, smSubset}},
			{{0, 1, smAggregateRequired}, {0, 2, smAggregateRequired}, {0, 3, smAggregateRequired}},
			{{0, 1, smTransform}, {0, 2, smTransform}, {0, 3, smTransform}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {1, 2, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {2, 3, smSubset}},
		}},
		{name: "fan_in", edges: [][]gmxEdge{
			{{0, 3, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smPartitionRequired}},
			{{0, 3, smSubset}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 3, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smPartitionOptional}},
			{{0, 3, smPartitionRequired}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 3, smPartitionRequired}, {0, 1, smSubset}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 3, smPartitionRequired}, {0, 1, smPartitionRequired}, {1, 2, smSubset}, {2, 3, smSubset}},
			{{0, 3, smAggregateRequired}, {1, 3, smAggregateRequired}, {2, 3, smAggregateRequired}},
			{{0, 3, smTransform}, {1, 3, smTransform}, {2, 3, smTransform}},
		}},
		{name: "optional_leaf", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionOptional}, {0, 3, smSubset}},
			{{0, 1, smPartitionOptional}, {0, 2, smSubset}, {1, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionOptional}, {1, 3, smPartitionOptional}},
			{{0, 1, smPartitionOptional}, {0, 2, smSubset}, {0, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionOptional}, {0, 3, smPartitionOptional}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionOptional}, {0, 3, smSubset}},
		}},
		{name: "redundant_duplicate_path", edges: [][]gmxEdge{
			{{0, 1, smSubset}, {0, 3, smSubset}, {1, 2, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {0, 3, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 2, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {2, 3, smSubset}},
			{{0, 1, smSubset}, {1, 2, smSubset}, {0, 2, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {2, 3, smSubset}, {0, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {0, 2, smSubset}, {2, 3, smSubset}},
			{{0, 1, smSubset}, {1, 2, smPartitionRequired}, {0, 2, smPartitionRequired}, {2, 3, smSubset}},
		}},
		{name: "shared_descendant_sibling_branch", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smSubset}},
			{{0, 1, smSubset}, {0, 2, smSubset}, {1, 3, smSubset}, {2, 3, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {0, 2, smSubset}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionOptional}, {1, 3, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smAggregateRequired}, {1, 3, smSubset}, {2, 3, smSubset}},
		}},
		{name: "disconnected_region", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {2, 3, smPartitionRequired}},
			{{0, 1, smSubset}, {2, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smTransform}, {2, 3, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {2, 3, smPartitionRequired}, {1, 3, smTransform}},
			{{0, 1, smPartitionRequired}},
			{{0, 1, smSubset}},
			{{0, 1, smTransform}},
			{{0, 1, smPartitionRequired}, {2, 3, smPartitionOptional}},
			{{0, 1, smPartitionOptional}, {2, 3, smSubset}},
		}},
		{name: "transform_only", edges: [][]gmxEdge{
			{{0, 1, smTransform}, {1, 2, smTransform}, {2, 3, smTransform}},
			{{0, 1, smTransform}, {1, 2, smTransform}, {0, 3, smTransform}},
			{{0, 1, smTransform}, {0, 2, smTransform}, {0, 3, smTransform}},
			{{0, 1, smTransform}, {1, 2, smTransform}, {2, 3, smTransform}, {0, 3, smTransform}},
			{{0, 1, smTransform}, {2, 3, smTransform}},
		}},
	}
}

func gmxCuratedFamilies5() []gmxFamily {
	return []gmxFamily{
		{name: "nested_complete_cover", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {2, 3, smPartitionRequired}, {3, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {2, 3, smPartitionRequired}, {2, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {2, 3, smPartitionRequired}, {2, 4, smPartitionOptional}},
			{{0, 1, smAggregateRequired}, {1, 2, smPartitionRequired}, {2, 3, smPartitionRequired}, {2, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {1, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {2, 3, smPartitionRequired}, {0, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionOptional}, {2, 3, smPartitionRequired}, {2, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {2, 3, smPartitionRequired}, {2, 4, smPartitionRequired}, {0, 3, smSubset}},
		}},
		{name: "mixed_complete_subset_chain", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {2, 3, smPartitionRequired}, {3, 4, smSubset}},
			{{0, 1, smSubset}, {1, 2, smPartitionRequired}, {2, 3, smSubset}, {3, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {2, 3, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smTransform}, {2, 3, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {1, 3, smSubset}, {2, 4, smSubset}},
			{{0, 1, smAggregateRequired}, {1, 2, smPartitionRequired}, {2, 3, smSubset}, {3, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {2, 3, smPartitionRequired}, {3, 4, smPartitionRequired}},
			{{0, 1, smSubset}, {1, 2, smSubset}, {2, 3, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {2, 3, smTransform}, {3, 4, smSubset}},
		}},
		{name: "diamond", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smPartitionRequired}, {3, 4, smPartitionRequired}},
			{{0, 1, smSubset}, {0, 2, smSubset}, {1, 3, smSubset}, {2, 3, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smPartitionRequired}, {0, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smSubset}, {2, 3, smSubset}, {3, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {1, 4, smPartitionRequired}, {2, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {0, 2, smSubset}, {1, 3, smPartitionRequired}, {2, 3, smSubset}, {3, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smPartitionRequired}, {3, 4, smPartitionOptional}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {2, 3, smPartitionRequired}, {3, 4, smSubset}},
		}},
		{name: "fan_out", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {0, 4, smPartitionRequired}},
			{{0, 1, smSubset}, {0, 2, smSubset}, {0, 3, smSubset}, {0, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionOptional}, {0, 3, smPartitionOptional}, {0, 4, smPartitionOptional}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {0, 4, smSubset}},
			{{0, 1, smAggregateRequired}, {0, 2, smAggregateRequired}, {0, 3, smAggregateRequired}, {0, 4, smAggregateRequired}},
			{{0, 1, smTransform}, {0, 2, smTransform}, {0, 3, smTransform}, {0, 4, smTransform}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {0, 4, smPartitionRequired}, {1, 2, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {0, 4, smPartitionRequired}, {2, 4, smSubset}},
		}},
		{name: "fan_in", edges: [][]gmxEdge{
			{{0, 4, smPartitionRequired}, {1, 4, smPartitionRequired}, {2, 4, smPartitionRequired}, {3, 4, smPartitionRequired}},
			{{0, 4, smSubset}, {1, 4, smSubset}, {2, 4, smSubset}, {3, 4, smSubset}},
			{{0, 4, smPartitionRequired}, {1, 4, smPartitionRequired}, {2, 4, smPartitionRequired}, {3, 4, smPartitionOptional}},
			{{0, 4, smPartitionRequired}, {0, 1, smSubset}, {1, 4, smSubset}, {2, 4, smSubset}, {3, 4, smSubset}},
			{{0, 4, smAggregateRequired}, {1, 4, smAggregateRequired}, {2, 4, smAggregateRequired}, {3, 4, smAggregateRequired}},
			{{0, 4, smTransform}, {1, 4, smTransform}, {2, 4, smTransform}, {3, 4, smTransform}},
			{{0, 4, smPartitionRequired}, {0, 1, smPartitionRequired}, {1, 4, smSubset}, {2, 4, smSubset}, {3, 4, smSubset}},
		}},
		{name: "optional_leaf", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {0, 4, smPartitionOptional}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionRequired}, {1, 4, smPartitionOptional}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {1, 3, smPartitionOptional}, {1, 4, smPartitionOptional}},
			{{0, 1, smPartitionOptional}, {0, 2, smSubset}, {1, 3, smSubset}, {2, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionOptional}, {0, 3, smPartitionRequired}, {1, 3, smPartitionRequired}, {0, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smSubset}, {0, 3, smPartitionRequired}, {0, 4, smSubset}},
		}},
		{name: "redundant_duplicate_path", edges: [][]gmxEdge{
			{{0, 1, smSubset}, {0, 4, smSubset}, {1, 2, smSubset}, {2, 3, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {0, 3, smPartitionRequired}, {2, 3, smSubset}, {3, 4, smSubset}},
			{{0, 1, smSubset}, {1, 2, smSubset}, {0, 2, smSubset}, {2, 3, smSubset}, {0, 3, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {0, 2, smSubset}, {2, 3, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {2, 3, smPartitionRequired}, {0, 2, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smSubset}, {0, 2, smSubset}, {0, 3, smPartitionRequired}, {2, 3, smSubset}},
		}},
		{name: "shared_descendant_sibling_branch", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 4, smSubset}, {2, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smSubset}, {1, 4, smSubset}, {2, 4, smSubset}},
			{{0, 1, smSubset}, {0, 2, smSubset}, {1, 4, smSubset}, {2, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionOptional}, {1, 3, smSubset}, {2, 3, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {0, 2, smPartitionRequired}, {1, 3, smSubset}, {2, 3, smSubset}, {0, 4, smSubset}},
		}},
		{name: "disconnected_region", edges: [][]gmxEdge{
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {3, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {2, 3, smPartitionRequired}, {0, 4, smTransform}},
			{{0, 1, smSubset}, {3, 4, smSubset}},
			{{0, 1, smPartitionRequired}, {1, 2, smPartitionRequired}, {2, 4, smTransform}, {3, 4, smPartitionRequired}},
			{{0, 1, smPartitionRequired}, {0, 2, smTransform}, {3, 4, smPartitionRequired}, {2, 3, smTransform}},
			{{0, 1, smPartitionRequired}},
		}},
		{name: "transform_only", edges: [][]gmxEdge{
			{{0, 1, smTransform}, {1, 2, smTransform}, {2, 3, smTransform}, {3, 4, smTransform}},
			{{0, 1, smTransform}, {1, 2, smTransform}, {0, 3, smTransform}, {0, 4, smTransform}},
			{{0, 1, smTransform}, {0, 2, smTransform}, {0, 3, smTransform}, {0, 4, smTransform}},
			{{0, 1, smTransform}, {2, 3, smTransform}, {3, 4, smTransform}},
			{{0, 1, smTransform}, {1, 2, smTransform}, {2, 3, smTransform}, {3, 4, smTransform}, {0, 4, smTransform}},
		}},
	}
}

func gmxCuratedGraphs(nodes int) []gmxGraph {
	out := make([]gmxGraph, 0, 128)
	for _, family := range gmxCuratedFamilies(nodes) {
		out = append(out, family.graphs(nodes)...)
	}
	return out
}

// ---------------------------------------------------------------------------
// Seeded uniform draw over the same seven shapes.
// ---------------------------------------------------------------------------

// gmxSeededGraphs draws `count` graphs uniformly from the 7^slotCount shape space
// of an n-node graph, keeping only draws that satisfy the edge bound, and
// deduplicating against `existing` so a seeded draw never silently re-runs a
// curated graph. It returns the accepted graphs and the number of raw draws the
// stream consumed, both of which are reported.
func gmxSeededGraphs(n, count, maxEdges int, existing []gmxGraph) ([]gmxGraph, int) {
	slots := gmxSlotCount(n)
	random := rand.New(rand.NewSource(gmxSeed)) //nolint:gosec // a fixed literal seed for a reproducible test population, never a credential.
	seen := make(map[string]struct{}, len(existing))
	for _, graph := range existing {
		seen[graph.code()] = struct{}{}
	}
	out := make([]gmxGraph, 0, count)
	rawDraws := 0
	for len(out) < count {
		kinds := make([]smEdgeKind, slots)
		edges := 0
		for i := range kinds {
			kind := smEdgeKind(random.Intn(int(smEdgeKindCount)))
			kinds[i] = kind
			if kind != smNone {
				edges++
			}
		}
		rawDraws++
		if edges > maxEdges {
			continue
		}
		graph := gmxGraph{
			name:   fmt.Sprintf("seed%03d", len(out)),
			family: "seeded_uniform",
			origin: "seeded",
			nodes:  n,
			kinds:  kinds,
		}
		if _, duplicate := seen[graph.code()]; duplicate {
			continue
		}
		seen[graph.code()] = struct{}{}
		out = append(out, graph)
	}
	return out, rawDraws
}

// ---------------------------------------------------------------------------
// Selection and reporting of the population.
// ---------------------------------------------------------------------------

// gmxSelection is one pass's population plus the accounting the coverage claim
// needs: how many candidate graphs were drawn, how many the publication
// validator rejected, and the same split per family and per edge shape.
type gmxSelection struct {
	graphs   []gmxGraph
	rejected int
	rawDraws int

	// familyDrawn and familyAccepted are per family, in family-declaration order
	// for the curated half and as a single seeded entry otherwise.
	families     []string
	familyDrawn  map[string]int
	familyAccept map[string]int

	// shapeSlots counts, over the ACCEPTED graphs, how many edge slots carry each
	// of the seven shapes. shapeGraphs counts how many accepted graphs contain at
	// least one slot of each shape. Together they are the per-shape coverage
	// claim.
	shapeSlots  map[string]int
	shapeGraphs map[string]int

	// edgeHist counts accepted graphs by their non-empty edge count.
	edgeHist map[int]int

	// originAccept splits the accepted graphs into the two declared halves of
	// the population, so the "curated plus seeded" claim is checkable.
	originAccept map[string]int
}

func newGMXSelection() *gmxSelection {
	return &gmxSelection{
		familyDrawn:  map[string]int{},
		familyAccept: map[string]int{},
		shapeSlots:   map[string]int{},
		shapeGraphs:  map[string]int{},
		edgeHist:     map[int]int{},
		originAccept: map[string]int{},
	}
}

// gmxSelect filters a candidate population through the public publication
// validator, keeping the accepted graphs in declaration order and accounting for
// every rejected one. Filtering happens here, before any parallel subtest starts,
// so the shared counters are never written concurrently.
func gmxSelect(t *testing.T, candidates []gmxGraph) *gmxSelection {
	t.Helper()
	selection := newGMXSelection()
	for _, graph := range candidates {
		if _, seen := selection.familyDrawn[graph.family]; !seen {
			selection.families = append(selection.families, graph.family)
		}
		selection.familyDrawn[graph.family]++
		if err := metering.ValidateComponentSchemas(smSchemas(gmxRelationships(graph))); err != nil {
			selection.rejected++
			if selection.rejected <= 12 {
				t.Logf("GENERATED-SWEEP validator rejected n=%d graph %s: %v", graph.nodes, graph, err)
			}
			continue
		}
		selection.familyAccept[graph.family]++
		selection.originAccept[graph.origin]++
		selection.graphs = append(selection.graphs, graph)
		selection.edgeHist[graph.edges()]++
		// shapeSlots counts EVERY slot, including the empty ones, so the seven
		// numbers sum to slots x accepted_graphs and the coverage claim is
		// checkable by addition. shapeGraphs counts only the shapes a graph
		// actually declares, so "none" is necessarily absent from it.
		present := map[smEdgeKind]struct{}{}
		for _, kind := range graph.kinds {
			selection.shapeSlots[kind.String()]++
			if kind != smNone {
				present[kind] = struct{}{}
			}
		}
		for kind := range present {
			selection.shapeGraphs[kind.String()]++
		}
	}
	return selection
}

// gmxTakeSpreadPerFamily reduces a selection to at most one accepted graph per
// family and then picks `budget` of those families by EVEN STRIDE across the
// family list, so a reduced commercial pass covers the whole declared family
// space rather than a prefix of it. It is the commercial pass's deliberate,
// reported graph-count reduction. The stride is integer arithmetic on the family
// index, so the selection is deterministic and independent of how many graphs a
// family contributed.
func gmxTakeSpreadPerFamily(selection *gmxSelection, budget int) []gmxGraph {
	representatives := make([]gmxGraph, 0, len(selection.families))
	taken := map[string]struct{}{}
	for _, graph := range selection.graphs {
		if _, already := taken[graph.family]; already {
			continue
		}
		taken[graph.family] = struct{}{}
		representatives = append(representatives, graph)
	}
	if budget <= 0 || len(representatives) == 0 {
		return nil
	}
	if budget >= len(representatives) {
		return representatives
	}
	out := make([]gmxGraph, 0, budget)
	for i := range budget {
		out = append(out, representatives[i*len(representatives)/budget])
	}
	return out
}

func (s *gmxSelection) report(t *testing.T, label string, maxEdges int) {
	t.Helper()
	t.Logf("GENERATED-SWEEP[%s] seed=%d nodes=%d slots=%d edge_constraint=at_most_%d_non_empty_edges space=%d accepted_graphs=%d validator_rejected=%d seeded_raw_draws=%d",
		label, gmxSeed, s.nodeCount(), gmxSlotCount(s.nodeCount()), maxEdges,
		intPow(int(smEdgeKindCount), gmxSlotCount(s.nodeCount())), len(s.graphs), s.rejected, s.rawDraws)
	origins := make([]string, 0, len(s.originAccept))
	for origin := range s.originAccept {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	originParts := make([]string, 0, len(origins))
	for _, origin := range origins {
		originParts = append(originParts, fmt.Sprintf("%s=%d", origin, s.originAccept[origin]))
	}
	t.Logf("GENERATED-SWEEP[%s] POPULATION_HALF_SPLIT %s", label, strings.Join(originParts, " "))
	for _, family := range s.families {
		t.Logf("GENERATED-SWEEP[%s] FAMILY %-34s drawn=%3d accepted=%3d rejected=%3d",
			label, family, s.familyDrawn[family], s.familyAccept[family], s.familyDrawn[family]-s.familyAccept[family])
	}
	shapes := make([]string, 0, int(smEdgeKindCount))
	for kind := range smEdgeKindCount {
		shapes = append(shapes, kind.String())
	}
	shapeLines := make([]string, 0, len(shapes))
	for _, shape := range shapes {
		shapeLines = append(shapeLines, fmt.Sprintf("%s=slots:%d/graphs:%d", shape, s.shapeSlots[shape], s.shapeGraphs[shape]))
	}
	t.Logf("GENERATED-SWEEP[%s] PER_SHAPE %s", label, strings.Join(shapeLines, " "))
	edges := make([]int, 0, len(s.edgeHist))
	for count := range s.edgeHist {
		edges = append(edges, count)
	}
	sort.Ints(edges)
	hist := make([]string, 0, len(edges))
	for _, count := range edges {
		hist = append(hist, fmt.Sprintf("edges=%d:%d", count, s.edgeHist[count]))
	}
	t.Logf("GENERATED-SWEEP[%s] EDGE_HISTOGRAM %s", label, strings.Join(hist, " "))
}

func (s *gmxSelection) nodeCount() int {
	if len(s.graphs) > 0 {
		return s.graphs[0].nodes
	}
	return 0
}

func intPow(base, exponent int) int {
	total := 1
	for range exponent {
		total *= base
	}
	return total
}

// ---------------------------------------------------------------------------
// Tariff states.
// ---------------------------------------------------------------------------

type gmxTariff int

const (
	// gmxTariffPaid prices every node at a distinct positive rate, so any money
	// a graph moves is visible.
	gmxTariffPaid gmxTariff = iota
	// gmxTariffExplicitFree prices every node "0": an explicit free declaration.
	gmxTariffExplicitFree
	// gmxTariffNoRule declares NO rule at all, which production treats as a
	// different thing from an explicit free one.
	gmxTariffNoRule
)

var gmxTariffNames = [...]string{"paid", "explicit_free", "no_rule"}

func (t gmxTariff) String() string { return gmxTariffNames[t] }

func gmxTariffStates() []gmxTariff {
	return []gmxTariff{gmxTariffPaid, gmxTariffExplicitFree, gmxTariffNoRule}
}

// gmxNonFree reports whether this tariff can bill money for any non-negative
// quantity. It is the single input to the TARIFF-AWARE A2 clause 2 split, and it
// is passed explicitly rather than inferred from a sweep label so the
// classification cannot silently follow a renamed pass.
func (t gmxTariff) nonFree() bool { return t == gmxTariffPaid }

func (tariff gmxTariff) rules(t *testing.T, nodes int, tag string) []economics.RatingRule {
	t.Helper()
	switch tariff {
	case gmxTariffNoRule:
		return nil
	case gmxTariffExplicitFree:
		out := make([]economics.RatingRule, 0, nodes)
		for i := range nodes {
			out = append(out, b1Rule(t, fmt.Sprintf("gmx-%s-free-n%d", tag, i+1), gmxNodeKeys[i], "0"))
		}
		return out
	default:
		// Distinct positive rates per node: a graph that moved money between two
		// identities would otherwise be invisible.
		out := make([]economics.RatingRule, 0, nodes)
		for i := range nodes {
			out = append(out, b1Rule(t, fmt.Sprintf("gmx-%s-paid-n%d", tag, i+1), gmxNodeKeys[i], fmt.Sprintf("%d", i+1)))
		}
		return out
	}
}

// ---------------------------------------------------------------------------
// Evidence. Built once per node count and shared by every graph.
// ---------------------------------------------------------------------------

// gmxEvidenceSpace enumerates the FULL product of the five evidence states over
// n nodes, in lexicographic order, which is a total order independent of any
// completion order.
func gmxEvidenceSpace(n int) [][]smEv {
	space := [][]smEv{{}}
	for range n {
		next := make([][]smEv, 0, len(space)*int(smEvCount))
		for _, prefix := range space {
			for state := range smEvCount {
				extended := make([]smEv, len(prefix), len(prefix)+1)
				copy(extended, prefix)
				next = append(next, append(extended, state))
			}
		}
		space = next
	}
	return space
}

// gmxCase is one fully materialised evidence assignment: the states themselves,
// the production observation, and the oracle evidence. None of them depends on
// the graph, so this is built once per node count.
type gmxCase struct {
	states []smEv
	code   string
	obs    metering.Observation
	oracle billsem.Evidence
	// unknown is the set of node indices whose evidence is absent or
	// unavailable, i.e. the nodes A2 clause 1 guards.
	unknown []int
}

func gmxBuildCases(t *testing.T, n int) []gmxCase {
	t.Helper()
	space := gmxEvidenceSpace(n)
	cases := make([]gmxCase, 0, len(space))
	for index, states := range space {
		entries := make([]smEvidenceEntry, 0, n)
		measures := make([]metering.Measure, 0, n)
		unknown := make([]int, 0, n)
		for i, state := range states {
			entries = append(entries, smEvidenceEntry{key: gmxNodeKeys[i], ev: state})
			if measure, ok := smMeasureFor(t, gmxNodeKeys[i], state); ok {
				measures = append(measures, measure)
			}
			if state == smAbsent || state == smUnavailable {
				unknown = append(unknown, i)
			}
		}
		cases = append(cases, gmxCase{
			states:  append([]smEv(nil), states...),
			code:    fmt.Sprintf("%04d", index),
			obs:     f3Observation(t, fmt.Sprintf("gmx-ev-%d-%d-%s", n, index, gmxEvName(states)), measures...),
			oracle:  smEvidenceEntries(smScope, entries...),
			unknown: unknown,
		})
	}
	return cases
}

func gmxEvName(states []smEv) string {
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, state.String())
	}
	return strings.Join(names, "|")
}

// gmxMissingRequiredMembers returns the node indices of every declared REQUIRED
// complete-coverage member whose quantity is unknown in this evidence
// assignment: absent (no measure at all) or unavailable (a measure carrying no
// comparable value). Either one makes the cover equation unknowable.
//
// An absent OPTIONAL member is deliberately excluded: the frozen schema declares
// its own edge-local zero, so it is a proven share rather than missing money and
// must never be what a cover is said to be hiding.
func gmxMissingRequiredMembers(graph gmxGraph, states []smEv) []int {
	members := make([]int, 0, graph.nodes)
	for i, kind := range graph.kinds {
		if kind != smPartitionRequired && kind != smAggregateRequired {
			continue
		}
		child := gmxSlotAt(graph.nodes, i)[1]
		if states[child] == smAbsent || states[child] == smUnavailable {
			members = append(members, child)
		}
	}
	return members
}

// gmxObservedParentWithUnknownRequiredMember reports whether an OBSERVED node is
// the parent of a required complete-coverage edge whose child is absent or
// unavailable. Only that shape is a production-diagnosable cover equation.
func gmxObservedParentWithUnknownRequiredMember(graph gmxGraph, states []smEv) bool {
	for i, kind := range graph.kinds {
		if kind != smPartitionRequired && kind != smAggregateRequired {
			continue
		}
		slot := gmxSlotAt(graph.nodes, i)
		if states[slot[0]] == smAbsent || states[slot[0]] == smUnavailable {
			continue
		}
		if states[slot[1]] == smAbsent || states[slot[1]] == smUnavailable {
			return true
		}
	}
	return false
}

func gmxNodeNames(indices []int) []string {
	names := make([]string, 0, len(indices))
	for _, index := range indices {
		names = append(names, gmxNodeName(index))
	}
	return names
}

// ---------------------------------------------------------------------------
// Tally.
// ---------------------------------------------------------------------------

// gmxAssertionKinds is the closed set of hard-failure kinds this file can raise,
// in report order.
var gmxAssertionKinds = []string{"A1", "A2", "A3", "A4", "A5"}

// gmxMaxExamplesPerAssertion bounds how many reproducers of one kind are quoted,
// so a systemic disagreement cannot flood the log. The COUNTER is unbounded.
const gmxMaxExamplesPerAssertion = 20

// gmxTally holds one pass's counters. Every plain counter is an atomic and every
// diagnostic map sits behind the mutex, so it is safe for parallel subtests and
// its totals do not depend on completion order.
type gmxTally struct {
	label string
	start time.Time

	cases       atomic.Int64
	oracleEvals atomic.Int64
	seamCalls   atomic.Int64

	// Guard counts: how many cases each assertion was actually applied to. A
	// zero failure count is only meaningful next to a non-zero guard count, so
	// both are reported and both are in the TALLY line.
	guardA1   atomic.Int64
	guardA2c1 atomic.Int64
	guardA2c2 atomic.Int64
	guardA3   atomic.Int64
	guardA4   atomic.Int64
	guardA5   atomic.Int64

	// Strictness accounting (A6).
	oracleContradicted  atomic.Int64
	oracleIncomplete    atomic.Int64
	agreeIncomplete     atomic.Int64
	prodStricter        atomic.Int64
	prodStricterBeyond  atomic.Int64
	oracleIncompProdCmp atomic.Int64
	coverObserved       atomic.Int64
	coverUnobserved     atomic.Int64
	coverLegitimate     atomic.Int64
	coverIllegitimate   atomic.Int64

	mu                sync.Mutex
	stricterBeyond    map[string]int
	counts            map[string]int
	examples          map[string][]string
	posErrClassCounts map[string]int
}

func newGMXTally(label string) *gmxTally {
	return &gmxTally{
		label:             label,
		start:             time.Now(),
		stricterBeyond:    map[string]int{},
		counts:            map[string]int{},
		examples:          map[string][]string{},
		posErrClassCounts: map[string]int{},
	}
}

func (p *gmxTally) add(kind, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts[kind]++
	if len(p.examples[kind]) < gmxMaxExamplesPerAssertion {
		p.examples[kind] = append(p.examples[kind], message)
	}
}

func (p *gmxTally) noteErrClass(class string) {
	p.mu.Lock()
	p.posErrClassCounts[class]++
	p.mu.Unlock()
}

// gmxSnapshot renders the counters as an ordered name=value list. It is the
// DETERMINISM contract: sorted names, no wall time, no map iteration, so three
// runs of this file must print three byte-identical lines.
func (p *gmxTally) gmxSnapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	fields := map[string]int64{
		"cases":                       p.cases.Load(),
		"oracle_evaluations":          p.oracleEvals.Load(),
		"seam_production_ratings":     p.seamCalls.Load(),
		"guard_A1":                    p.guardA1.Load(),
		"guard_A2_clause1":            p.guardA2c1.Load(),
		"guard_A2_clause2":            p.guardA2c2.Load(),
		"guard_A3":                    p.guardA3.Load(),
		"guard_A4":                    p.guardA4.Load(),
		"guard_A5":                    p.guardA5.Load(),
		"oracle_contradicted":         p.oracleContradicted.Load(),
		"oracle_incomplete":           p.oracleIncomplete.Load(),
		"agree_incomplete":            p.agreeIncomplete.Load(),
		"production_stricter":         p.prodStricter.Load(),
		"production_stricter_beyond":  p.prodStricterBeyond.Load(),
		"oracle_incomplete_prod_comp": p.oracleIncompProdCmp.Load(),
		"cover_observed":              p.coverObserved.Load(),
		"cover_unobserved":            p.coverUnobserved.Load(),
		"cover_legitimate":            p.coverLegitimate.Load(),
		"cover_illegitimate":          p.coverIllegitimate.Load(),
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, fields[name]))
	}
	for _, kind := range gmxAssertionKinds {
		parts = append(parts, fmt.Sprintf("%s_failures=%d", kind, p.counts[kind]))
	}
	for _, class := range billsem.SortedKeys(p.posErrClassCounts) {
		parts = append(parts, fmt.Sprintf("prod_err_class[%s]=%d", class, p.posErrClassCounts[class]))
	}
	for _, class := range billsem.SortedKeys(p.stricterBeyond) {
		parts = append(parts, fmt.Sprintf("stricter_beyond[%s]=%d", class, p.stricterBeyond[class]))
	}
	return strings.Join(parts, " ")
}

func (p *gmxTally) finish(t *testing.T) {
	t.Helper()
	// oracle-stricter is the FINDING count: a case the model says must not be
	// certified and production certified anyway. It is exactly A1 failures plus
	// A2 clause 2 illegitimate cases, and it is reported as its own number so the
	// strictness claim is a measurement rather than an assertion.
	p.mu.Lock()
	a1 := p.counts["A1"]
	a2 := p.counts["A2"]
	guard := map[string]int64{
		"A1": p.guardA1.Load(), "A2_c1": p.guardA2c1.Load(), "A2_c2": p.guardA2c2.Load(),
		"A3": p.guardA3.Load(), "A4": p.guardA4.Load(), "A5": p.guardA5.Load(),
	}
	p.mu.Unlock()
	t.Logf("GENERATED-SWEEP[%s] CASES=%d ORACLE_EVALUATIONS=%d PRODUCTION_RATINGS=%d wall=%s (all five top-level tests of this file run concurrently, so this wall is measured under CPU contention; the process wall is the sum-bound by the slowest pass, the five-node structure pass)",
		p.label, p.cases.Load(), p.oracleEvals.Load(), p.seamCalls.Load(), time.Since(p.start).Round(time.Millisecond))
	t.Logf("GENERATED-SWEEP[%s] A6_STRICTNESS production_stricter=%d production_stricter_beyond_oracle=%d agree_incomplete=%d oracle_stricter_FINDINGS=%d",
		p.label, p.prodStricter.Load(), p.prodStricterBeyond.Load(), p.agreeIncomplete.Load(), a1+a2)
	t.Logf("GENERATED-SWEEP[%s] A2_CLAUSE2_SPLIT population=%d observed_parent=%d unobserved_or_other=%d legitimate=%d illegitimate=%d",
		p.label, p.oracleIncompProdCmp.Load(), p.coverObserved.Load(), p.coverUnobserved.Load(),
		p.coverLegitimate.Load(), p.coverIllegitimate.Load())
	guardNames := make([]string, 0, len(guard))
	for name := range guard {
		guardNames = append(guardNames, name)
	}
	sort.Strings(guardNames)
	guardParts := make([]string, 0, len(guardNames))
	for _, name := range guardNames {
		guardParts = append(guardParts, fmt.Sprintf("%s=%d", name, guard[name]))
	}
	t.Logf("GENERATED-SWEEP[%s] GUARDS %s", p.label, strings.Join(guardParts, " "))
	t.Logf("GENERATED-SWEEP[%s] TALLY %s", p.label, p.gmxSnapshot())
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, kind := range gmxAssertionKinds {
		t.Logf("GENERATED-SWEEP[%s] ASSERTION %s failures=%d", p.label, kind, p.counts[kind])
	}
	total := 0
	for _, kind := range gmxAssertionKinds {
		total += p.counts[kind]
	}
	if total == 0 {
		t.Logf("GENERATED-SWEEP[%s] no mismatches found", p.label)
		return
	}
	for _, kind := range gmxAssertionKinds {
		for _, example := range p.examples[kind] {
			t.Errorf("GENERATED-SWEEP[%s] %s MISMATCH: %s", p.label, kind, example)
		}
	}
}

// ---------------------------------------------------------------------------
// Core per-case differential evaluation.
// ---------------------------------------------------------------------------

// gmxEvaluate drives one (graph, evidence, tariff, seam) case through BOTH
// implementations and applies A1, A2 and the A6 strictness counters.
//
// The oracle result is passed in rather than recomputed, because it depends only
// on the relationship set and the evidence, never on the tariff or the seam.
func gmxEvaluate(
	t *testing.T,
	tally *gmxTally,
	graph gmxGraph,
	seam review5beSeam,
	tariff gmxTariff,
	resolved economics.TariffSnapshot,
	rels []metering.ComponentRelationship,
	oracle billsem.ScopeResult,
	evidence gmxCase,
) {
	t.Helper()
	caseID := fmt.Sprintf("n=%d graph=%s tariff=%s seam=%s evidence=%s",
		graph.nodes, graph, tariff, seam.name, gmxEvName(evidence.states))

	oracleContradicted := len(oracle.Contradicted) > 0
	oracleIncomplete := len(oracle.Incomplete) > 0

	val, err := seam.rate(t, resolved, evidence.obs)
	out := smInspect(val)
	out.errClass = smErrorClass(err)
	prodComplete := out.completeness == economics.CompletenessComplete

	tally.cases.Add(1)
	tally.seamCalls.Add(1)
	if oracleContradicted {
		tally.oracleContradicted.Add(1)
	}
	if oracleIncomplete {
		tally.oracleIncomplete.Add(1)
	}
	if !prodComplete && !oracleContradicted {
		tally.prodStricter.Add(1)
	}
	if !prodComplete && !oracleContradicted && !oracleIncomplete {
		tally.prodStricterBeyond.Add(1)
		tally.noteErrClass(out.errClass)
		tally.mu.Lock()
		tally.stricterBeyond[out.errClass]++
		tally.mu.Unlock()
	}
	if oracleIncomplete && !prodComplete {
		tally.agreeIncomplete.Add(1)
	}

	// A1: an oracle quantity contradiction must forbid a Complete production
	// result. This is the safety property and the only hard failure whose cause
	// is the model being right about the arithmetic.
	if oracleContradicted {
		tally.guardA1.Add(1)
	}
	if oracleContradicted && prodComplete {
		tally.add("A1", fmt.Sprintf("%s oracle_contradicted=[%s] production=Complete total=%s err=%s components=%v",
			caseID, strings.Join(oracle.Contradicted, ","), out.total, out.errClass, out.components))
	}

	// A2 clause 1: an Absent or Unavailable node must never emit a positive line.
	if len(evidence.unknown) > 0 {
		tally.guardA2c1.Add(1)
		for _, index := range evidence.unknown {
			if !out.positive[gmxNodeKeys[index].CanonicalKey()] {
				continue
			}
			tally.add("A2", fmt.Sprintf("%s node=%s evidence=%s emitted_positive_line total=%s err=%s components=%v",
				caseID, gmxNodeName(index), evidence.states[index], out.total, out.errClass, out.components))
		}
	}

	// A2 clause 2, TARIFF-AWARE, exactly as in the three-node sweep. The oracle
	// is deliberately commercial-free, so "oracle Incomplete AND production
	// Complete" is CLASSIFIED rather than failed: the cover can be structurally
	// incomplete while nothing money-bearing is hidden behind it.
	if !oracleIncomplete || !prodComplete {
		return
	}
	tally.guardA2c2.Add(1)
	tally.oracleIncompProdCmp.Add(1)
	observed := gmxObservedParentWithUnknownRequiredMember(graph, evidence.states)
	if observed {
		tally.coverObserved.Add(1)
	} else {
		tally.coverUnobserved.Add(1)
	}
	if tariff.nonFree() && len(gmxMissingRequiredMembers(graph, evidence.states)) != 0 {
		tally.coverIllegitimate.Add(1)
		tally.add("A2", fmt.Sprintf("%s production=Complete although the required complete member(s) %s are absent/unavailable and carry a non-free rule; total=%s components=%v oracle_incomplete=%v observed_parent=%v",
			caseID, strings.Join(gmxNodeNames(gmxMissingRequiredMembers(graph, evidence.states)), ","),
			out.total, out.components, oracle.Incomplete, observed))
		return
	}
	tally.coverLegitimate.Add(1)
}

// gmxSolveOnce runs the independent oracle for one evidence assignment and counts
// the evaluation. The result is shared by every tariff and every seam, which is
// exact: the oracle takes no tariff and no observation set.
func gmxSolveOnce(t *testing.T, tally *gmxTally, rels []metering.ComponentRelationship, evidence gmxCase) billsem.ScopeResult {
	t.Helper()
	tally.oracleEvals.Add(1)
	return smSolveOracle(t, smSchemas(rels), evidence.oracle)
}

// ---------------------------------------------------------------------------
// PASS 1 -- structure, tariff irrelevant, full evidence, both seams.
// ---------------------------------------------------------------------------

func TestGeneratedSchemaStructureSweep(t *testing.T) {
	// The whole file is one measurement, so every top-level test runs concurrently with
	// the others: the five-node structure pass alone dominates the wall time, and the
	// four-node, order, transform and isolation passes are pure additions to it.
	t.Parallel()
	for _, nodes := range []int{4, 5} {
		t.Run(fmt.Sprintf("n%d", nodes), func(t *testing.T) {
			// The two node counts run CONCURRENTLY: the five-node pass costs
			// several times the four-node pass, so serialising them would leave
			// half the machine idle for the whole of the cheaper one.
			t.Parallel()
			maxEdges := gmxMaxEdgesFor(nodes)
			seededBudget := gmxSeededStructure4
			if nodes == 5 {
				seededBudget = gmxSeededStructure5
			}
			curated := gmxCuratedGraphs(nodes)
			seeded, rawDraws := gmxSeededGraphs(nodes, seededBudget, maxEdges, curated)
			selection := gmxSelect(t, append(append([]gmxGraph(nil), curated...), seeded...))
			selection.rawDraws = rawDraws
			selection.report(t, fmt.Sprintf("structure/n%d", nodes), maxEdges)

			tally := newGMXTally(fmt.Sprintf("structure/n%d", nodes))
			cases := gmxBuildCases(t, nodes)
			seams := review5beSeams()
			t.Logf("GENERATED-SWEEP[structure/n%d] evidence_space=%d (full 5^%d product) seams=%d tariffs=explicit_free", nodes, len(cases), nodes, len(seams))
			t.Cleanup(func() { tally.finish(t) })

			for index, graph := range selection.graphs {
				t.Run(fmt.Sprintf("g%03d/%s", index, graph.family), func(t *testing.T) {
					t.Parallel()
					rels := gmxRelationships(graph)
					// The structure pass is TARIFF IRRELEVANT: every node is
					// declared explicit free, so nothing is ever payable and
					// STRUCTURE is isolated from MONEY.
					rules := gmxTariffExplicitFree.rules(t, nodes, fmt.Sprintf("structure-n%d", nodes))
					resolved := f356Schema(t, fmt.Sprintf("gmx-structure-n%d-%d", nodes, index), rules, rels)
					// The oracle result is the same for every seam, so it is
					// solved once per evidence assignment and shared.
					oracles := make([]billsem.ScopeResult, len(cases))
					for i := range cases {
						oracles[i] = gmxSolveOnce(t, tally, rels, cases[i])
					}
					for i := range cases {
						for _, seam := range seams {
							gmxEvaluate(t, tally, graph, seam, gmxTariffExplicitFree, resolved, rels, oracles[i], cases[i])
						}
					}
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PASS 2 -- commercial, three tariff states, both seams.
//
// The graph count is reduced DELIBERATELY (the first accepted graph of each
// family, up to the reported budget) while the evidence product is NOT reduced.
// ---------------------------------------------------------------------------

func TestGeneratedSchemaCommercialSweep(t *testing.T) {
	// The whole file is one measurement, so every top-level test runs concurrently with
	// the others: the five-node structure pass alone dominates the wall time, and the
	// four-node, order, transform and isolation passes are pure additions to it.
	t.Parallel()
	for _, nodes := range []int{4, 5} {
		t.Run(fmt.Sprintf("n%d", nodes), func(t *testing.T) {
			t.Parallel()
			familyBudget := gmxCommercialFamilies4
			if nodes == 5 {
				familyBudget = gmxCommercialFamilies5
			}
			curated := gmxCuratedGraphs(nodes)
			selection := gmxSelect(t, curated)
			selection.rawDraws = 0
			selection.report(t, fmt.Sprintf("commercial-curated-all/n%d", nodes), gmxMaxEdgesFor(nodes))

			population := gmxTakeSpreadPerFamily(selection, familyBudget)
			families := make([]string, 0, len(population))
			for _, graph := range population {
				families = append(families, graph.family)
			}
			t.Logf("GENERATED-SWEEP[commercial/n%d] DELIBERATE_GRAPH_REDUCTION accepted_curated=%d selected=%d family_budget=%d selection=one_accepted_graph_per_family_then_even_stride_over_the_family_list evidence_space=full_5^%d families=%v",
				nodes, len(selection.graphs), len(population), familyBudget, nodes, families)

			tally := newGMXTally(fmt.Sprintf("commercial/n%d", nodes))
			cases := gmxBuildCases(t, nodes)
			seams := review5beSeams()
			tariffs := gmxTariffStates()
			t.Logf("GENERATED-SWEEP[commercial/n%d] evidence_space=%d (full 5^%d product) seams=%d tariffs=%s",
				nodes, len(cases), nodes, len(seams), strings.Join(gmxTariffNames[:], "/"))
			t.Cleanup(func() { tally.finish(t) })

			for index, graph := range population {
				t.Run(fmt.Sprintf("g%03d/%s", index, graph.family), func(t *testing.T) {
					t.Parallel()
					rels := gmxRelationships(graph)
					oracles := make([]billsem.ScopeResult, len(cases))
					for i := range cases {
						oracles[i] = gmxSolveOnce(t, tally, rels, cases[i])
					}
					for _, tariff := range tariffs {
						rules := tariff.rules(t, nodes, fmt.Sprintf("commercial-n%d-%d-%s", nodes, index, tariff))
						resolved := f356Schema(t, fmt.Sprintf("gmx-commercial-n%d-%d-%s", nodes, index, tariff), rules, rels)
						for i := range cases {
							for _, seam := range seams {
								gmxEvaluate(t, tally, graph, seam, tariff, resolved, rels, oracles[i], cases[i])
							}
						}
					}
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A4: declaration-order invariance.
// ---------------------------------------------------------------------------

// gmxOrderEvidence is the small, named evidence set the order-invariance pass
// drives. It is deliberately NOT the full 5^n product: A4 is a transformation
// property over a few representative evidence assignments, and the exhaustive
// evidence coverage belongs to the structure and commercial passes.
func gmxOrderEvidence(n int) [][]smEv {
	patterns := [][]smEv{
		{smOne, smOne, smOne, smOne, smOne},
		{smTwo, smOne, smZero, smTwo, smZero},
		{smAbsent, smOne, smUnavailable, smZero, smOne},
		{smOne, smAbsent, smTwo, smUnavailable, smZero},
	}
	out := make([][]smEv, 0, len(patterns))
	for _, pattern := range patterns {
		out = append(out, append([]smEv(nil), pattern[:n]...))
	}
	return out
}

func TestGeneratedSchemaOrderInvariance(t *testing.T) {
	// The whole file is one measurement, so every top-level test runs concurrently with
	// the others: the five-node structure pass alone dominates the wall time, and the
	// four-node, order, transform and isolation passes are pure additions to it.
	t.Parallel()
	for _, nodes := range []int{4, 5} {
		t.Run(fmt.Sprintf("n%d", nodes), func(t *testing.T) {
			t.Parallel()
			maxEdges := gmxMaxEdgesFor(nodes)
			seededBudget := gmxSeededStructure4
			if nodes == 5 {
				seededBudget = gmxSeededStructure5
			}
			curated := gmxCuratedGraphs(nodes)
			seeded, rawDraws := gmxSeededGraphs(nodes, seededBudget, maxEdges, curated)
			selection := gmxSelect(t, append(append([]gmxGraph(nil), curated...), seeded...))
			selection.rawDraws = rawDraws
			selection.report(t, fmt.Sprintf("order/n%d", nodes), maxEdges)

			tally := newGMXTally(fmt.Sprintf("order/n%d", nodes))
			evidenceSets := gmxOrderEvidence(nodes)
			seams := review5beSeams()
			// The same prebuilt cases the sweeps use, looked up by rendered state,
			// so the order pass measures the transformation and not a different
			// evidence materialisation.
			byState := gmxCaseIndex(gmxBuildCases(t, nodes))
			t.Logf("GENERATED-SWEEP[order/n%d] evidence_sets=%d (named, not the full product) seams=%d", nodes, len(evidenceSets), len(seams))
			t.Cleanup(func() { tally.finish(t) })

			for index, graph := range selection.graphs {
				t.Run(fmt.Sprintf("g%03d/%s", index, graph.family), func(t *testing.T) {
					t.Parallel()
					rels := gmxRelationships(graph)
					rules := gmxTariffExplicitFree.rules(t, nodes, fmt.Sprintf("order-n%d", nodes))
					resolvedForward := f356Schema(t, fmt.Sprintf("gmx-order-a-n%d-%d", nodes, index), rules, rels)
					// The reversed form also appends an empty second schema, so a
					// single-schema-only implementation could not pass by
					// construction.
					resolvedReversed := f3Resolved(t, fmt.Sprintf("gmx-order-b-n%d-%d", nodes, index), rules, []metering.ComponentSchema{
						{ID: b1SchemaID, Version: "1", Relationships: smReverse(rels)},
						{ID: b1SchemaID + "_empty", Version: "1"},
					})
					for _, states := range evidenceSets {
						evidence := gmxFindCase(byState, states)
						oracleForward := gmxSolveOnce(t, tally, rels, evidence)
						oracleReversed := gmxSolveOnce(t, tally, smReverse(rels), evidence)
						if gmxOracleVerdict(oracleForward) != gmxOracleVerdict(oracleReversed) {
							tally.guardA4.Add(1)
							tally.add("A4", fmt.Sprintf("n=%d graph=%s evidence=%s the ORACLE's own verdict changed under a declaration reversal: forward=%s reversed=%s",
								nodes, graph, gmxEvName(states), gmxOracleVerdict(oracleForward), gmxOracleVerdict(oracleReversed)))
						}
						for _, seam := range seams {
							caseID := fmt.Sprintf("n=%d graph=%s seam=%s evidence=%s", nodes, graph, seam.name, gmxEvName(states))
							valForward, errForward := seam.rate(t, resolvedForward, evidence.obs)
							valReversed, errReversed := seam.rate(t, resolvedReversed, evidence.obs)
							outForward, outReversed := smInspect(valForward), smInspect(valReversed)
							outForward.errClass, outReversed.errClass = smErrorClass(errForward), smErrorClass(errReversed)
							tally.cases.Add(1)
							tally.seamCalls.Add(2)
							tally.guardA4.Add(1)
							if outForward.errClass != outReversed.errClass ||
								outForward.completeness != outReversed.completeness ||
								!slices.Equal(outForward.components, outReversed.components) ||
								outForward.total != outReversed.total {
								tally.add("A4", fmt.Sprintf("%s forward{err=%s compl=%s total=%s components=%v} reversed+empty{err=%s compl=%s total=%s components=%v}",
									caseID, outForward.errClass, outForward.completeness, outForward.total, outForward.components,
									outReversed.errClass, outReversed.completeness, outReversed.total, outReversed.components))
							}
						}
					}
				})
			}
		})
	}
}

func gmxFindCase(index map[string]gmxCase, states []smEv) gmxCase {
	want := gmxEvName(states)
	evidence, ok := index[want]
	if !ok {
		panic("gmxFindCase: the prebuilt evidence space does not contain " + want)
	}
	return evidence
}

// gmxCaseIndex keys the prebuilt evidence space by its rendered state name, so
// the order pass can look a named evidence assignment up in constant time instead
// of scanning the whole 5^n product once per graph.
func gmxCaseIndex(cases []gmxCase) map[string]gmxCase {
	index := make(map[string]gmxCase, len(cases))
	for _, candidate := range cases {
		index[gmxEvName(candidate.states)] = candidate
	}
	return index
}

// gmxOracleVerdict renders the oracle's set-level verdict. A4 asserts it is
// invariant under a declaration reversal, which is the oracle's own determinism
// claim and is checked here rather than assumed.
func gmxOracleVerdict(result billsem.ScopeResult) string {
	return fmt.Sprintf("contradicted=%v incomplete=%v ambiguous=%v",
		sortedStrings(result.Contradicted), sortedStrings(result.Incomplete), sortedStrings(result.Ambiguous))
}

func sortedStrings(in []string) string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return "[" + strings.Join(out, ",") + "]"
}

// ---------------------------------------------------------------------------
// A3: a transform edge is never containment.
//
// Each row is a PAIR of declarations that differ in exactly one edge's kind: the
// transform form, on which child > parent must NOT be a quantity contradiction in
// either implementation, and the subset control, on which the very same
// quantities MUST be a contradiction in both. The control is what gives the
// property teeth: without it, "no contradiction" would be satisfied by any
// implementation that ignored every edge.
// ---------------------------------------------------------------------------

func TestGeneratedSchemaTransformIsNotContainment(t *testing.T) {
	// The whole file is one measurement, so every top-level test runs concurrently with
	// the others: the five-node structure pass alone dominates the wall time, and the
	// four-node, order, transform and isolation passes are pure additions to it.
	t.Parallel()
	tally := newGMXTally("transform")
	t.Cleanup(func() { tally.finish(t) })

	type row struct {
		name  string
		graph gmxGraph
		slot  int
	}
	var rows []row
	for _, nodes := range []int{4, 5} {
		for _, graph := range gmxCuratedGraphs(nodes) {
			if graph.family != "transform_only" {
				continue
			}
			for slot, kind := range graph.kinds {
				if kind != smTransform {
					continue
				}
				rows = append(rows, row{
					name:  fmt.Sprintf("n%d/%s/slot%d", nodes, graph.name, slot),
					graph: graph,
					slot:  slot,
				})
			}
		}
	}
	t.Logf("GENERATED-SWEEP[transform] transform_only_rows=%d (every transform edge of every curated transform-only graph at n=4 and n=5)", len(rows))

	for _, entry := range rows {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()
			graph := entry.graph
			slot := gmxSlotAt(graph.nodes, entry.slot)
			// Evidence: every node exact 1 except the child of the edge under
			// test, which is exact 2. If the edge were containment, the parent's
			// 1 could not contain the child's 2.
			states := make([]smEv, graph.nodes)
			for i := range states {
				states[i] = smOne
			}
			states[slot[1]] = smTwo

			entries := make([]smEvidenceEntry, 0, graph.nodes)
			measures := make([]metering.Measure, 0, graph.nodes)
			for i, state := range states {
				entries = append(entries, smEvidenceEntry{key: gmxNodeKeys[i], ev: state})
				measure, ok := smMeasureFor(t, gmxNodeKeys[i], state)
				if !ok {
					t.Fatalf("every node is present in this row; node %d is absent", i)
				}
				measures = append(measures, measure)
			}
			evidence := gmxCase{
				states: states,
				obs:    f3Observation(t, "gmx-transform-"+entry.name, measures...),
				oracle: smEvidenceEntries(smScope, entries...),
			}

			control := gmxGraph{name: graph.name + "/subset", family: "transform_control", nodes: graph.nodes, kinds: append([]smEdgeKind(nil), graph.kinds...)}
			control.kinds[entry.slot] = smSubset

			transformRels := gmxRelationships(graph)
			controlRels := gmxRelationships(control)
			transformOracle := gmxSolveOnce(t, tally, transformRels, evidence)
			controlOracle := gmxSolveOnce(t, tally, controlRels, evidence)

			tally.cases.Add(1)
			if len(transformOracle.Contradicted) != 0 {
				tally.add("A3", fmt.Sprintf("%s a transform-only graph reported an ORACLE contradiction %v; a transform is never containment",
					entry.name, transformOracle.Contradicted))
			}
			// The control: same quantities, one subset edge. Both implementations
			// must see the contradiction, or the numbers above prove nothing.
			tally.cases.Add(1)
			tally.guardA3.Add(1)
			if len(controlOracle.Contradicted) == 0 {
				tally.add("A3", fmt.Sprintf("%s the subset CONTROL reported no ORACLE contradiction (oracle class %q), so this row's quantities are not load-bearing and the property is untested here",
					entry.name, smErrorClassFromOracle(controlOracle)))
			}

			transformResolved := f356Schema(t, "gmx-transform-"+entry.name, gmxTariffExplicitFree.rules(t, graph.nodes, "transform"), transformRels)
			controlResolved := f356Schema(t, "gmx-transform-control-"+entry.name, gmxTariffExplicitFree.rules(t, graph.nodes, "transform"), controlRels)

			for _, seam := range review5beSeams() {
				val, err := seam.rate(t, transformResolved, evidence.obs)
				class := smErrorClass(err)
				tally.seamCalls.Add(1)
				if smIsQuantityContradiction(class) {
					out := smInspect(val)
					tally.add("A3", fmt.Sprintf("%s/%s transform-only production reported %s (a transform must not be containment); completeness=%s components=%v oracle_contradicted=%v",
						entry.name, seam.name, class, out.completeness, out.components, transformOracle.Contradicted))
				}
				controlVal, controlErr := seam.rate(t, controlResolved, evidence.obs)
				controlClass := smErrorClass(controlErr)
				tally.seamCalls.Add(1)
				if !smIsQuantityContradiction(controlClass) {
					out := smInspect(controlVal)
					tally.add("A3", fmt.Sprintf("%s/%s the subset CONTROL reported %q (completeness=%s components=%v) where the oracle reports %v; the control is meant to be a contradiction, so its silence would make the transform check vacuous",
						entry.name, seam.name, controlClass, out.completeness, out.components, controlOracle.Contradicted))
				}
			}
		})
	}
}

// smErrorClassFromOracle is not a production classification and must never be
// confused with one: the oracle has no typed errors, only classifications. It is
// spelled out so no reader mistakes the A3 control for a production claim.
func smErrorClassFromOracle(result billsem.ScopeResult) string {
	if len(result.Contradicted) > 0 {
		return "quantity_contradiction"
	}
	if len(result.Incomplete) > 0 {
		return "incomplete"
	}
	return "clean"
}

// ---------------------------------------------------------------------------
// A5: a cross-direction containment edge and a cross-unit transform edge are
// inert IDENTICALLY in both implementations.
//
// Each row is a triple of declarations over the same node universe: the graph
// carrying the odd edge, the same graph with that one edge REMOVED, and a
// same-direction same-unit CONTROL in which the same quantities really are
// contradictory. Inertness is then the exact statement "the graph carrying the
// odd edge is indistinguishable from the graph without it", measured on the
// production tuple AND on the oracle's verdict.
// ---------------------------------------------------------------------------

func TestGeneratedSchemaDirectionUnitIsolation(t *testing.T) {
	// The whole file is one measurement, so every top-level test runs concurrently with
	// the others: the five-node structure pass alone dominates the wall time, and the
	// four-node, order, transform and isolation passes are pure additions to it.
	t.Parallel()
	tally := newGMXTally("isolation")
	t.Cleanup(func() { tally.finish(t) })

	// gmxIsoNode is one declared identity of an isolation row, with the evidence
	// it is reported under. Rows list the gmx nodes, the odd edge's two
	// identities and the control edge's two identities; identities that coincide
	// (the control shares whichever endpoint the direction/unit fix did not
	// change) are deduplicated by canonical key, so a row never declares two
	// rules for the same component.
	type gmxIsoNode struct {
		key     metering.ComponentKey
		ev      smEv
		measure string
	}
	type gmxIsoRow struct {
		name       string
		nodes      int
		rels       []metering.ComponentRelationship
		oddParent  metering.ComponentKey
		oddChild   metering.ComponentKey
		oddRel     metering.ComponentRelationship
		controlRel metering.ComponentRelationship
		controlP   metering.ComponentKey
		controlC   metering.ComponentKey
	}

	// gmxIsoWorld materialises one row: the distinct identities, one explicit-free
	// rule each, and one evidence set. Every gmx node is exact 1; the odd edge's
	// parent is exact 1 and its child is exact 2, so the odd edge WOULD be a
	// contradiction if it were containment. The control edge is given the same
	// two quantities, so the control is contradictory for the same reason the
	// isolated form must not be.
	buildWorld := func(t *testing.T, row gmxIsoRow) ([]economics.RatingRule, gmxCase) {
		t.Helper()
		declared := make([]gmxIsoNode, 0, row.nodes+4)
		for i := range row.nodes {
			declared = append(declared, gmxIsoNode{key: gmxNodeKeys[i], ev: smOne, measure: "1"})
		}
		declared = append(
			declared,
			gmxIsoNode{key: row.oddParent, ev: smOne, measure: "1"},
			gmxIsoNode{key: row.oddChild, ev: smTwo, measure: "2"},
			gmxIsoNode{key: row.controlP, ev: smOne, measure: "1"},
			gmxIsoNode{key: row.controlC, ev: smTwo, measure: "2"},
		)
		rules := make([]economics.RatingRule, 0, len(declared))
		entries := make([]smEvidenceEntry, 0, len(declared))
		measures := make([]metering.Measure, 0, len(declared))
		seen := make(map[string]struct{}, len(declared))
		for i, node := range declared {
			canonical := node.key.CanonicalKey()
			if _, duplicate := seen[canonical]; duplicate {
				continue
			}
			seen[canonical] = struct{}{}
			rules = append(rules, b1Rule(t, fmt.Sprintf("gmx-isolation-%s-free-%d", row.name, i), node.key, "0"))
			entries = append(entries, smEvidenceEntry{key: node.key, ev: node.ev})
			measures = append(measures, b1Measure(t, node.key, node.measure))
		}
		return rules, gmxCase{
			obs:    f3Observation(t, "gmx-isolation-"+row.name, measures...),
			oracle: smEvidenceEntries(smScope, entries...),
		}
	}

	rows := []gmxIsoRow{
		{
			// n=4. The cross-direction subset asserts 1 >= 2, which is false, so
			// honouring it would be a contradiction; the rest of the graph is a
			// real containment chain, so the odd edge is not the only thing under
			// test. Only the parent's direction differs between the odd edge and
			// the control, so the child identity is shared.
			name:      "n4/cross_direction_subset",
			nodes:     4,
			oddParent: b1Key(metering.DirectionOutput, "vendor:gmx_iso4_parent", metering.UnitToken),
			oddChild:  b1Key(metering.DirectionInput, "vendor:gmx_iso4_child", metering.UnitToken),
			rels: []metering.ComponentRelationship{
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[0], Child: gmxNodeKeys[1]},
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[0], Child: gmxNodeKeys[2]},
				{Kind: metering.RelationshipPartition, Parent: gmxNodeKeys[2], Child: gmxNodeKeys[3]},
			},
			oddRel:     metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: b1Key(metering.DirectionOutput, "vendor:gmx_iso4_parent", metering.UnitToken), Child: b1Key(metering.DirectionInput, "vendor:gmx_iso4_child", metering.UnitToken)},
			controlRel: metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: b1Key(metering.DirectionInput, "vendor:gmx_iso4_parent", metering.UnitToken), Child: b1Key(metering.DirectionInput, "vendor:gmx_iso4_child", metering.UnitToken)},
			controlP:   b1Key(metering.DirectionInput, "vendor:gmx_iso4_parent", metering.UnitToken),
			controlC:   b1Key(metering.DirectionInput, "vendor:gmx_iso4_child", metering.UnitToken),
		},
		{
			// n=5. The same idea on the wider graph, with the odd edge hanging off a
			// node that also carries a complete cover, so the cross-direction edge
			// competes with a real conservation claim rather than standing alone.
			name:      "n5/cross_direction_subset",
			nodes:     5,
			oddParent: b1Key(metering.DirectionOutput, "vendor:gmx_iso5_parent", metering.UnitToken),
			oddChild:  b1Key(metering.DirectionInput, "vendor:gmx_iso5_child", metering.UnitToken),
			rels: []metering.ComponentRelationship{
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[0], Child: gmxNodeKeys[1]},
				{Kind: metering.RelationshipPartition, Parent: gmxNodeKeys[1], Child: gmxNodeKeys[2]},
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[1], Child: gmxNodeKeys[3]},
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[2], Child: gmxNodeKeys[4]},
			},
			oddRel:     metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: b1Key(metering.DirectionOutput, "vendor:gmx_iso5_parent", metering.UnitToken), Child: b1Key(metering.DirectionInput, "vendor:gmx_iso5_child", metering.UnitToken)},
			controlRel: metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: b1Key(metering.DirectionInput, "vendor:gmx_iso5_parent", metering.UnitToken), Child: b1Key(metering.DirectionInput, "vendor:gmx_iso5_child", metering.UnitToken)},
			controlP:   b1Key(metering.DirectionInput, "vendor:gmx_iso5_parent", metering.UnitToken),
			controlC:   b1Key(metering.DirectionInput, "vendor:gmx_iso5_child", metering.UnitToken),
		},
		{
			// n=4, cross UNIT. A cross-unit CONTAINMENT edge cannot be published at
			// all: ValidateComponentSchemas requires equal units on every
			// non-transform relationship. The only legal cross-unit edge is
			// therefore a transform, which is inert for two independent reasons.
			// Only the child's unit differs between the odd edge and the control,
			// so the parent identity is shared.
			name:      "n4/cross_unit_transform",
			nodes:     4,
			oddParent: b1Key(metering.DirectionInput, "vendor:gmx_unit4_parent", metering.UnitToken),
			oddChild:  b1Key(metering.DirectionInput, "vendor:gmx_unit4_child", metering.UnitImage),
			rels: []metering.ComponentRelationship{
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[0], Child: gmxNodeKeys[1]},
				{Kind: metering.RelationshipPartition, Parent: gmxNodeKeys[1], Child: gmxNodeKeys[2]},
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[2], Child: gmxNodeKeys[3]},
			},
			oddRel:     metering.ComponentRelationship{Kind: metering.RelationshipTransform, Parent: b1Key(metering.DirectionInput, "vendor:gmx_unit4_parent", metering.UnitToken), Child: b1Key(metering.DirectionInput, "vendor:gmx_unit4_child", metering.UnitImage)},
			controlRel: metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: b1Key(metering.DirectionInput, "vendor:gmx_unit4_parent", metering.UnitToken), Child: b1Key(metering.DirectionInput, "vendor:gmx_unit4_child", metering.UnitToken)},
			controlP:   b1Key(metering.DirectionInput, "vendor:gmx_unit4_parent", metering.UnitToken),
			controlC:   b1Key(metering.DirectionInput, "vendor:gmx_unit4_child", metering.UnitToken),
		},
		{
			name:      "n5/cross_unit_transform",
			nodes:     5,
			oddParent: b1Key(metering.DirectionInput, "vendor:gmx_unit5_parent", metering.UnitToken),
			oddChild:  b1Key(metering.DirectionInput, "vendor:gmx_unit5_child", metering.UnitImage),
			rels: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: gmxNodeKeys[0], Child: gmxNodeKeys[1]},
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[0], Child: gmxNodeKeys[2]},
				{Kind: metering.RelationshipPartition, Parent: gmxNodeKeys[2], Child: gmxNodeKeys[3]},
				{Kind: metering.RelationshipSubset, Parent: gmxNodeKeys[3], Child: gmxNodeKeys[4]},
			},
			oddRel:     metering.ComponentRelationship{Kind: metering.RelationshipTransform, Parent: b1Key(metering.DirectionInput, "vendor:gmx_unit5_parent", metering.UnitToken), Child: b1Key(metering.DirectionInput, "vendor:gmx_unit5_child", metering.UnitImage)},
			controlRel: metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: b1Key(metering.DirectionInput, "vendor:gmx_unit5_parent", metering.UnitToken), Child: b1Key(metering.DirectionInput, "vendor:gmx_unit5_child", metering.UnitToken)},
			controlP:   b1Key(metering.DirectionInput, "vendor:gmx_unit5_parent", metering.UnitToken),
			controlC:   b1Key(metering.DirectionInput, "vendor:gmx_unit5_child", metering.UnitToken),
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			withOdd := append(append([]metering.ComponentRelationship(nil), row.rels...), row.oddRel)
			withoutOdd := append([]metering.ComponentRelationship(nil), row.rels...)
			control := append(append([]metering.ComponentRelationship(nil), row.rels...), row.controlRel)

			for label, rels := range map[string][]metering.ComponentRelationship{
				"with_odd_edge":    withOdd,
				"odd_edge_removed": withoutOdd,
			} {
				if err := metering.ValidateComponentSchemas(smSchemas(rels)); err != nil {
					t.Fatalf("%s: %s is unpublishable, so the comparison would be between two publication failures: %v", row.name, label, err)
				}
			}
			if err := metering.ValidateComponentSchemas(smSchemas(control)); err != nil {
				t.Fatalf("%s: the same-direction same-unit CONTROL is unpublishable: %v", row.name, err)
			}

			rules, evidence := buildWorld(t, row)

			isolatedOracle := gmxSolveOnce(t, tally, withOdd, evidence)
			droppedOracle := gmxSolveOnce(t, tally, withoutOdd, evidence)
			controlOracle := gmxSolveOnce(t, tally, control, evidence)

			tally.cases.Add(1)
			tally.guardA5.Add(1)
			if gmxOracleVerdict(isolatedOracle) != gmxOracleVerdict(droppedOracle) {
				tally.add("A5", fmt.Sprintf("%s the ORACLE does not treat the odd edge as inert: with_odd=%s odd_removed=%s",
					row.name, gmxOracleVerdict(isolatedOracle), gmxOracleVerdict(droppedOracle)))
			}
			tally.cases.Add(1)
			if len(controlOracle.Contradicted) == 0 {
				tally.add("A5", fmt.Sprintf("%s the same-direction same-unit CONTROL reported no ORACLE contradiction, so the quantities are not load-bearing and the inertness check is vacuous here",
					row.name))
			}

			isolatedResolved := f356Schema(t, "gmx-isolation-odd-"+row.name, rules, withOdd)
			droppedResolved := f356Schema(t, "gmx-isolation-dropped-"+row.name, rules, withoutOdd)
			controlResolved := f356Schema(t, "gmx-isolation-control-"+row.name, rules, control)

			for _, seam := range review5beSeams() {
				isolatedVal, isolatedErr := seam.rate(t, isolatedResolved, evidence.obs)
				droppedVal, droppedErr := seam.rate(t, droppedResolved, evidence.obs)
				isolated, dropped := smInspect(isolatedVal), smInspect(droppedVal)
				isolated.errClass, dropped.errClass = smErrorClass(isolatedErr), smErrorClass(droppedErr)

				tally.cases.Add(1)
				tally.seamCalls.Add(2)
				tally.guardA5.Add(1)
				if isolated.errClass != dropped.errClass ||
					isolated.completeness != dropped.completeness ||
					!slices.Equal(isolated.components, dropped.components) ||
					isolated.total != dropped.total {
					tally.add("A5", fmt.Sprintf("%s/%s the odd edge is NOT inert: with_odd{err=%s compl=%s total=%s components=%v} odd_removed{err=%s compl=%s total=%s components=%v}",
						row.name, seam.name, isolated.errClass, isolated.completeness, isolated.total, isolated.components,
						dropped.errClass, dropped.completeness, dropped.total, dropped.components))
				}

				controlVal, controlErr := seam.rate(t, controlResolved, evidence.obs)
				controlClass := smErrorClass(controlErr)
				tally.seamCalls.Add(1)
				if !smIsQuantityContradiction(controlClass) {
					out := smInspect(controlVal)
					tally.add("A5", fmt.Sprintf("%s/%s the same-direction same-unit CONTROL reported %q (completeness=%s components=%v) where the oracle reports %v; its silence would make the inertness check vacuous",
						row.name, seam.name, controlClass, out.completeness, out.components, controlOracle.Contradicted))
				} else if !mtpIs(controlErr, controlClass) {
					tally.add("A5", fmt.Sprintf("%s/%s the CONTROL's class %q does not match the real production sentinel", row.name, seam.name, controlClass))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Named regression reproducers.
//
// There is deliberately NO test in this section for the population above. The
// generated sweep reported A1 = 0, A2 = 0, A3 = 0, A4 = 0 and A5 = 0 across
// every case it executed, so no mismatch was found and therefore no minimal
// reproducer is pinned. A named regression is added here the moment any
// assertion reports a failure; it is never added speculatively, and neither the
// oracle nor production is edited to make a case pass.
