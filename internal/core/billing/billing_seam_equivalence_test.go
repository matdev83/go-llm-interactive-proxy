package billing_test

// E/R seam equivalence (handoff section 16).
//
// The operator/local E seam and the customer-policy R seam deliberately differ in
// evidence SELECTION (BasisLocalExpected keeps local-origin quantity envelopes;
// BasisCustomerPolicy keeps the frozen retail B-leg selection, narrows the input
// set through selectRetailInferenceSelection and applies a retailComponentMask)
// and in PERSPECTIVE, payer, subject binding and valuation identity. Those
// differences are ratified and are NOT what this file measures.
//
// What IS measured is structural GRAPH behaviour. Given the SAME selected
// quantity set, the SAME frozen tariff and the SAME effective qualifiers, the
// two seams must agree on four things:
//
//	1. the structural error class, matched with errors.Is against the real
//	   production sentinels (a SET, not a first match, because the rater joins
//	   independent typed diagnostics);
//	2. complete / partial / conflict completeness;
//	3. component support and overlap decisions: for every declared node,
//	   whether a line was emitted, its status, and whether its exact amount is
//	   strictly positive, i.e. which components were suppressed or withheld;
//	4. exact PRE-ROUND line arithmetic. The pre-round amount is the exact
//	   rational the rater computed -- LineItem.Amount when the value terminates,
//	   otherwise the AmountNumerator/AmountDenominator pair. The ROUNDED money
//	   (RoundedAmount) is deliberately NOT compared, so a rounding-policy
//	   difference between the planes can never mask or manufacture a structural
//	   difference.
//
// Everything the seams are allowed to disagree about (Perspective, Basis, Payer,
// Subject, Scope, Tariff/Policy refs, InputObservations, Valuation.ID) is left
// out of the comparison by construction: the tuple below contains only the four
// structural facets.
//
// The base-graph spread is deliberately wide and includes cases with NO declared
// relationship at all, so the harness proves the two seams agree on ordinary
// non-schema rating too and not only on the frozen-schema state machine.

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ---------------------------------------------------------------------------
// Structural facets.
// ---------------------------------------------------------------------------

// eqSentinels is the real production sentinel set, matched by identity. The
// order here is only the report order; eqErrClass sorts its output, so the
// comparison is order independent.
var eqSentinels = []struct {
	class string
	err   error
}{
	{"overlap_conflict", billing.ErrSchemaOverlapConflict},
	{"partition_contradiction", billing.ErrSchemaPartitionContradiction},
	{"partition_incomparable", billing.ErrSchemaPartitionIncomparable},
	{"partition_incomplete", billing.ErrSchemaPartitionIncomplete},
	{"subset_contradiction", billing.ErrSchemaSubsetContradiction},
	{"quantity_contradiction", billing.ErrSchemaQuantityContradiction},
}

// eqErrClass renders the structural error class as a sorted, joined set of the
// production sentinels the error satisfies. A nil error is "nil"; an error that
// matches no sentinel is reported verbatim under "other" so a newly introduced
// typed failure can never be silently swallowed by the comparison.
func eqErrClass(err error) string {
	if err == nil {
		return "nil"
	}
	matched := make([]string, 0, len(eqSentinels))
	for _, sentinel := range eqSentinels {
		if errors.Is(err, sentinel.err) {
			matched = append(matched, sentinel.class)
		}
	}
	if len(matched) == 0 {
		return "other(" + err.Error() + ")"
	}
	sort.Strings(matched)
	return strings.Join(matched, "+")
}

// eqPreRound renders one line's EXACT pre-round amount. It prefers the exact
// rational pair, because Amount is populated only when the value terminates; a
// terminating value is rendered as a reduced rational so the two encodings of
// the same number compare identically.
func eqPreRound(line *economics.LineItem) string {
	if line == nil {
		return ""
	}
	if line.AmountNumerator != "" {
		num, numOK := new(big.Int).SetString(line.AmountNumerator, 10)
		den, denOK := new(big.Int).SetString(line.AmountDenominator, 10)
		if numOK && denOK && den.Sign() != 0 {
			return new(big.Rat).SetFrac(num, den).RatString()
		}
		return line.AmountNumerator + "/" + line.AmountDenominator
	}
	if line.Amount != nil {
		if rat, err := line.Amount.ToRat(); err == nil {
			return rat.RatString()
		}
		return line.Amount.CanonicalString()
	}
	return ""
}

// eqOutcome is the comparable structural outcome of ONE seam. It deliberately
// contains no perspective, payer, subject, scope, or reference material.
type eqOutcome struct {
	errClass     string
	completeness economics.Completeness
	// support maps every declared node's canonical key to its line decision:
	// "withheld" when no line was emitted, otherwise "rated"/"free"/"<status>"
	// suffixed with ":positive" or ":zero" by the sign of the exact amount.
	support map[string]string
	// preRound maps every declared node's canonical key to its exact pre-round
	// amount, or "" when the line carries no amount at all.
	preRound map[string]string
}

// eqFacetDiff renders the structural difference between two outcomes so a
// disagreement is a self-contained, minimal reproducer in the failure text.
func (o eqOutcome) diff(other eqOutcome) string {
	parts := make([]string, 0, 4)
	if o.errClass != other.errClass {
		parts = append(parts, fmt.Sprintf("err_class{E=%q R=%q}", o.errClass, other.errClass))
	}
	if o.completeness != other.completeness {
		parts = append(parts, fmt.Sprintf("completeness{E=%q R=%q}", o.completeness, other.completeness))
	}
	keys := make([]string, 0, len(o.support))
	for key := range o.support {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		e, r := o.support[key], other.support[key]
		if e != r {
			parts = append(parts, fmt.Sprintf("support[%s]{E=%q R=%q}", eqShort(key), e, r))
		}
	}
	amountKeys := make([]string, 0, len(o.preRound))
	for key := range o.preRound {
		amountKeys = append(amountKeys, key)
	}
	sort.Strings(amountKeys)
	for _, key := range amountKeys {
		e, r := o.preRound[key], other.preRound[key]
		if e != r {
			parts = append(parts, fmt.Sprintf("pre_round[%s]{E=%q R=%q}", eqShort(key), e, r))
		}
	}
	return strings.Join(parts, " ")
}

// eqShort renders a canonical component key as its bare component name so a
// failure names the component rather than a JSON blob.
func eqShort(canonicalKey string) string {
	if index := strings.LastIndex(canonicalKey, "/"); index >= 0 && index+1 < len(canonicalKey) {
		return canonicalKey[index+1:]
	}
	return canonicalKey
}

// eqInspect extracts the structural outcome from one seam's valuation, naming
// every declared node so a WITHHELD component is visible as a decision rather
// than as an absence.
func eqInspect(val economics.Valuation, nodes []metering.ComponentKey) eqOutcome {
	out := eqOutcome{
		errClass:     "nil",
		completeness: val.Completeness,
		support:      make(map[string]string, len(nodes)),
		preRound:     make(map[string]string, len(nodes)),
	}
	seen := make(map[string]struct{}, len(val.Lines))
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Component == nil {
			continue
		}
		canonical := line.Component.CanonicalKey()
		if _, duplicate := seen[canonical]; duplicate {
			// A duplicated identity is itself a structural fact, so it is
			// recorded rather than collapsed: the comparison then fails
			// visibly instead of matching a first-wins winner.
			out.support[canonical] = fmt.Sprintf("duplicate:%s", line.Status)
			continue
		}
		seen[canonical] = struct{}{}
		status := string(line.Status)
		if status == "" {
			status = string(economics.RatingLineRated)
		}
		exact := eqPreRound(line)
		sign := "none"
		if exact != "" {
			rat, ok := new(big.Rat).SetString(exact)
			if ok {
				sign = "zero"
				if rat.Sign() > 0 {
					sign = "positive"
				}
			}
		}
		out.support[canonical] = status + ":" + sign
		out.preRound[canonical] = exact
	}
	for i := range nodes {
		canonical := nodes[i].CanonicalKey()
		if _, emitted := seen[canonical]; emitted {
			continue
		}
		if _, already := out.support[canonical]; already {
			continue
		}
		out.support[canonical] = "withheld"
	}
	return out
}

// ---------------------------------------------------------------------------
// Case construction.
// ---------------------------------------------------------------------------

// eqNode is one declared component with its tariff and its evidence.
//
// The PRICING of a node is the load-bearing part of the fixture. A frozen
// complete-coverage edge whose parent also carries a rule is a genuine payable
// overlap and the rater (correctly) refuses the whole valuation as
// ErrSchemaOverlapConflict, which would mask every other structural path. The
// real child-only aggregate shape therefore leaves the aggregate parent
// UNPRICED -- price "" means no rule is declared at all -- so the complete-cover
// proof, the conservation arithmetic and the interval solver are what decide the
// outcome. Cases that DO want the overlap path simply price both sides.
type eqNode struct {
	name string
	// price is the frozen unit price, or "" for no declared rule.
	price string
	// direction defaults to metering.DirectionInput when empty.
	direction metering.FlowDirection
	// evidence is the normalized selected quantity: a decimal literal, or
	// "absent" for no measure at all, or "unavailable" for a measure with no
	// comparable value.
	evidence string
}

// eqRel is one declared relationship, named by component name.
type eqRel struct {
	kind     metering.RelationshipKind
	parent   string
	child    string
	optional bool
}

// eqCase is one base graph plus one evidence assignment.
type eqCase struct {
	name  string
	class string
	nodes []eqNode
	rels  []eqRel
}

// eqClasses is the declared graph-class spread this harness asserts over.
var eqClasses = []string{
	"nested_covers",
	"mixed_chain",
	"diamond",
	"optional_leaves",
	"subset_siblings",
	"shared_descendant",
	"non_schema",
}

// eqCases is the full case table. Every entry names a graph class so the report
// can break the agreement rate down per class.
//
// Distinct per-node prices are used throughout, so a component billed at another
// component's rate shows up as a different EXACT pre-round amount rather than as
// a coincidentally equal total.
func eqCases() []eqCase {
	cases := []eqCase{
		// --- nested covers: multi-level, absent intermediates, priced leaves ---
		// A -> {B, C}; B -> {X, Y}. A and B unpriced, so B's and then A's
		// complete cover must be proven from the priced leaves alone.
		{
			name: "nested_covers_conserved_all_present", class: "nested_covers",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "", evidence: "60"},
				{name: "c", price: "4", evidence: "40"},
				{name: "x", price: "5", evidence: "30"},
				{name: "y", price: "6", evidence: "30"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "x"},
				{kind: metering.RelationshipPartition, parent: "b", child: "y"},
			},
		},
		{
			// B reports 60 but its complete partition sums 30 + 40 = 70.
			name: "nested_covers_contradicted_leaf_sum", class: "nested_covers",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "", evidence: "60"},
				{name: "c", price: "4", evidence: "40"},
				{name: "x", price: "5", evidence: "30"},
				{name: "y", price: "6", evidence: "40"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "x"},
				{kind: metering.RelationshipPartition, parent: "b", child: "y"},
			},
		},
		{
			// B is never reported, yet its own complete coverage is resolved, so
			// A's cover is decided on the same equation either way.
			name: "nested_covers_absent_intermediate", class: "nested_covers",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "", evidence: "absent"},
				{name: "c", price: "4", evidence: "40"},
				{name: "x", price: "5", evidence: "30"},
				{name: "y", price: "6", evidence: "30"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "x"},
				{kind: metering.RelationshipPartition, parent: "b", child: "y"},
			},
		},
		{
			// B is absent AND its own coverage has an absent REQUIRED member, so
			// the share is genuinely missing and nothing may be invented.
			name: "nested_covers_absent_intermediate_missing_member", class: "nested_covers",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "", evidence: "absent"},
				{name: "c", price: "4", evidence: "40"},
				{name: "x", price: "5", evidence: "30"},
				{name: "y", price: "6", evidence: "absent"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "x"},
				{kind: metering.RelationshipPartition, parent: "b", child: "y"},
			},
		},
		{
			// Present-but-unusable evidence is a different fact from never
			// having reported the member.
			name: "nested_covers_unavailable_member", class: "nested_covers",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "", evidence: "60"},
				{name: "c", price: "4", evidence: "40"},
				{name: "x", price: "5", evidence: "unavailable"},
				{name: "y", price: "6", evidence: "30"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "x"},
				{kind: metering.RelationshipPartition, parent: "b", child: "y"},
			},
		},

		// --- mixed chains: the edge class changes partway; containment is one union ---
		{
			// A subset B; B partition {C, D}. A and B unpriced, so B's cover is
			// the only thing standing between the leaves and a false complete.
			name: "mixed_chain_subset_then_partition_consistent", class: "mixed_chain",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "", evidence: "80"},
				{name: "c", price: "3", evidence: "30"},
				{name: "d", price: "4", evidence: "50"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "b", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "d"},
			},
		},
		{
			// The chain changes class the other way: A partition B; B subset C;
			// B partition D. B reports 80 but C + D sums to 120.
			name: "mixed_chain_partition_then_subset_contradiction", class: "mixed_chain",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "80"},
				{name: "b", price: "", evidence: "80"},
				{name: "c", price: "3", evidence: "30"},
				{name: "d", price: "4", evidence: "90"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipSubset, parent: "b", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "d"},
			},
		},
		{
			// A pure subset chain A -> B -> C -> D where the far descendant
			// exceeds its ancestor. Every hop changes the bound, so a checker
			// that only looked at direct children would miss it entirely. Only D
			// is priced, so no payable overlap masks the containment breach.
			name: "mixed_chain_deep_subset_transitive_breach", class: "mixed_chain",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "10"},
				{name: "b", price: "", evidence: "10"},
				{name: "c", price: "", evidence: "10"},
				{name: "d", price: "4", evidence: "11"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "a", child: "b"},
				{kind: metering.RelationshipSubset, parent: "b", child: "c"},
				{kind: metering.RelationshipSubset, parent: "c", child: "d"},
			},
		},
		{
			// Aggregate and partition are the same complete-coverage claim, so
			// mixing the two spellings must not change the arithmetic.
			name: "mixed_chain_aggregate_and_partition_synonyms", class: "mixed_chain",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "60"},
				{name: "b", price: "3", evidence: "20"},
				{name: "c", price: "4", evidence: "40"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipAggregate, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
			},
		},

		{
			// A negative effective reduced quantity would be a structural
			// contradiction (decision 7). The table cannot reach it through a
			// valid observation -- see
			// TestEquivalenceNegativeQuantityIsBlockedAtTheSDKBoundary -- so
			// this node simply keeps the ordinary positive evidence.
			name: "mixed_chain_subset_parent_only", class: "mixed_chain",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "10"},
				{name: "b", price: "4", evidence: "5"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "a", child: "b"},
			},
		},

		// --- diamonds: a shared descendant below two branches ---
		//
		// A complete-coverage diamond (b and c BOTH complete children of a and
		// both containing d) is refused at PUBLICATION: the disjoint additive
		// sum of b and c double-declares d, whether d is reached by a complete
		// or a subset edge. The legal fan-in diamond therefore hangs the two
		// branches off SUBSET edges, so no complete claim double-declares the
		// shared descendant, and d is declared as a complete child of BOTH
		// branches -- the shared-complete-owner runtime classification.
		{
			name: "diamond_subset_branches_shared_complete_descendant", class: "diamond",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "", evidence: "60"},
				{name: "c", price: "", evidence: "40"},
				{name: "d", price: "4", evidence: "20"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "a", child: "b"},
				{kind: metering.RelationshipSubset, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "d"},
				{kind: metering.RelationshipPartition, parent: "c", child: "d"},
			},
		},
		{
			name: "diamond_subset_branches_shared_complete_descendant_absent_branch", class: "diamond",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "", evidence: "absent"},
				{name: "c", price: "", evidence: "40"},
				{name: "d", price: "4", evidence: "20"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "a", child: "b"},
				{kind: metering.RelationshipSubset, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "b", child: "d"},
				{kind: metering.RelationshipPartition, parent: "c", child: "d"},
			},
		},
		{
			// Two independently priced branches that both contain one payable
			// tip: the overlap resolver must refuse it and both seams must agree.
			name: "diamond_subset_fan_in", class: "diamond",
			nodes: []eqNode{
				{name: "root", price: "1", evidence: "100"},
				{name: "left", price: "2", evidence: "60"},
				{name: "right", price: "3", evidence: "40"},
				{name: "tip", price: "4", evidence: "30"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "left", child: "tip"},
				{kind: metering.RelationshipSubset, parent: "right", child: "tip"},
			},
		},

		// --- optional leaves: an absent optional member is the schema's own zero ---
		{
			name: "optional_leaves_absent_optional_conserved", class: "optional_leaves",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "2", evidence: "70"},
				{name: "c", price: "3", evidence: "absent"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c", optional: true},
			},
		},
		{
			name: "optional_leaves_present_optional_conserved", class: "optional_leaves",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "2", evidence: "70"},
				{name: "c", price: "3", evidence: "30"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c", optional: true},
			},
		},
		{
			// A is absent, and BOTH its complete members are absent optionals, so
			// A's cover is a proven zero. A payable subset D of 5 then
			// contradicts it, and the bound must reach A through its own absent
			// optional zeros.
			name: "optional_leaves_optional_contradicts_subset", class: "optional_leaves",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "absent"},
				{name: "b", price: "2", evidence: "absent"},
				{name: "c", price: "3", evidence: "absent"},
				{name: "d", price: "4", evidence: "5"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b", optional: true},
				{kind: metering.RelationshipPartition, parent: "a", child: "c", optional: true},
				{kind: metering.RelationshipSubset, parent: "a", child: "d"},
			},
		},
		{
			name: "optional_leaves_required_absent", class: "optional_leaves",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "2", evidence: "absent"},
				{name: "c", price: "3", evidence: "40"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
			},
		},

		// --- subset siblings: several independently payable subsets of one parent ---
		{
			name: "subset_siblings_disjoint", class: "subset_siblings",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "2", evidence: "10"},
				{name: "c", price: "3", evidence: "20"},
				{name: "d", price: "4", evidence: "30"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "a", child: "b"},
				{kind: metering.RelationshipSubset, parent: "a", child: "c"},
				{kind: metering.RelationshipSubset, parent: "a", child: "d"},
			},
		},
		{
			// A's complete partition already accounts for A, so a separately
			// priced subset of A is an additive double charge.
			name: "subset_siblings_partition_cover_plus_priced_subset", class: "subset_siblings",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "2", evidence: "60"},
				{name: "c", price: "3", evidence: "40"},
				{name: "d", price: "4", evidence: "20"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipSubset, parent: "a", child: "d"},
			},
		},
		{
			name: "subset_siblings_partition_cover_without_priced_subset", class: "subset_siblings",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "2", evidence: "60"},
				{name: "c", price: "3", evidence: "40"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
			},
		},
		{
			// Both sides priced over a subset edge: the plain overlap path.
			name: "subset_siblings_priced_parent_includes_priced_child", class: "subset_siblings",
			nodes: []eqNode{
				{name: "a", price: "1", evidence: "100"},
				{name: "b", price: "2", evidence: "40"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "a", child: "b"},
			},
		},
		{
			// A -> D -> B is a redundant path that reaches the already-paid B.
			// It must not duplicate A's charge, and D being absent must not
			// suppress the genuinely independent sibling C.
			name: "subset_siblings_redundant_path_reaches_paid_child", class: "subset_siblings",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "b", price: "2", evidence: "60"},
				{name: "c", price: "3", evidence: "40"},
				{name: "d", price: "4", evidence: "absent"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipSubset, parent: "a", child: "d"},
				{kind: metering.RelationshipSubset, parent: "d", child: "b"},
			},
		},

		// --- shared descendants: a member declared by two complete parents ---
		{
			name: "shared_descendant_complete_owner", class: "shared_descendant",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "0"},
				{name: "z", price: "", evidence: "0"},
				{name: "b", price: "3", evidence: "60"},
				{name: "c", price: "4", evidence: "0"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "z", child: "b"},
			},
		},
		{
			// Both parents OBSERVED, both sharing B: neither cover can be proven,
			// so the classification is incomparable rather than contradictory,
			// and the child-only money must not look complete.
			name: "shared_descendant_observed_parent", class: "shared_descendant",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "z", price: "", evidence: "100"},
				{name: "b", price: "3", evidence: "60"},
				{name: "c", price: "4", evidence: "40"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "z", child: "b"},
			},
		},
		{
			// The same shared ownership, but the other parent was never reported:
			// the refusal is still about ownership, not about a missing operand.
			name: "shared_descendant_absent_other_parent", class: "shared_descendant",
			nodes: []eqNode{
				{name: "a", price: "", evidence: "100"},
				{name: "z", price: "", evidence: "absent"},
				{name: "b", price: "3", evidence: "60"},
				{name: "c", price: "4", evidence: "40"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipPartition, parent: "a", child: "b"},
				{kind: metering.RelationshipPartition, parent: "a", child: "c"},
				{kind: metering.RelationshipPartition, parent: "z", child: "b"},
			},
		},

		// --- ordinary non-schema graphs: no usable declared containment ---
		{
			name: "non_schema_independent_components", class: "non_schema",
			nodes: []eqNode{
				{name: "solo_a", price: "2", evidence: "10"},
				{name: "solo_b", price: "3", evidence: "20"},
				{name: "solo_c", price: "5", evidence: "0"},
			},
		},
		{
			name: "non_schema_transform_edge_is_not_containment", class: "non_schema",
			nodes: []eqNode{
				{name: "tf_parent", price: "1", evidence: "1"},
				{name: "tf_child", price: "2", evidence: "9"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipTransform, parent: "tf_parent", child: "tf_child"},
			},
		},
		{
			name: "non_schema_cross_direction_edge_ignored", class: "non_schema",
			nodes: []eqNode{
				{name: "in_total", price: "1", evidence: "10"},
				{name: "out_total", price: "2", direction: metering.DirectionOutput, evidence: "20"},
			},
			rels: []eqRel{
				{kind: metering.RelationshipSubset, parent: "in_total", child: "out_total"},
			},
		},
	}
	return cases
}

// eqBuild turns one case into the frozen snapshot, the declared node keys and
// the single normalized observation both seams are fed.
//
// The snapshot is published and resolved through the real billingcompose
// SnapshotCatalog, exactly as production consumers do, so the harness exercises
// the frozen schema as published rather than a hand-built rater.
func eqBuild(t *testing.T, tc eqCase) (economics.TariffSnapshot, []metering.ComponentKey, metering.Observation) {
	t.Helper()
	keys := make(map[string]metering.ComponentKey, len(tc.nodes))
	ordered := make([]metering.ComponentKey, 0, len(tc.nodes))
	rules := make([]economics.RatingRule, 0, len(tc.nodes))
	measures := make([]metering.Measure, 0, len(tc.nodes))
	for index, node := range tc.nodes {
		direction := node.direction
		if direction == "" {
			direction = metering.DirectionInput
		}
		key := metering.ComponentKey{
			Direction: direction,
			Component: "vendor:eq_" + tc.name + "_" + node.name,
			Unit:      metering.UnitToken,
			SchemaID:  b1SchemaID,
		}
		keys[node.name] = key
		ordered = append(ordered, key)
		if node.price != "" {
			rules = append(rules, b1Rule(t, fmt.Sprintf("eq-%s-%02d-%s-rate", tc.name, index, node.name), key, node.price))
		}
		switch node.evidence {
		case "absent":
			continue
		case "unavailable":
			measures = append(measures, b1UnavailableMeasure(key))
		default:
			measures = append(measures, b1Measure(t, key, node.evidence))
		}
	}
	relationships := make([]metering.ComponentRelationship, 0, len(tc.rels))
	for _, rel := range tc.rels {
		parent, parentOK := keys[rel.parent]
		child, childOK := keys[rel.child]
		if !parentOK || !childOK {
			t.Fatalf("eqBuild(%s): relationship %s->%s names an undeclared component", tc.name, rel.parent, rel.child)
		}
		relationships = append(relationships, metering.ComponentRelationship{
			Kind: rel.kind, Parent: parent, Child: child, Optional: rel.optional,
		})
	}
	var schemas []metering.ComponentSchema
	if relationships != nil {
		schemas = []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: relationships}}
		// The case table is asserted to be PUBLISHABLE. A case the public
		// schema validator refuses is not a low agreement rate, it is a hole
		// in the table, so it fails here rather than silently disappearing
		// behind a publication error inside a seam comparison.
		if err := metering.ValidateComponentSchemas(schemas); err != nil {
			t.Fatalf("eqBuild(%s): declared graph is not publishable, so the case would never reach the rater: %v", tc.name, err)
		}
	}
	resolved := f3Resolved(t, "eq-"+tc.name, rules, schemas)
	obs := f3Observation(t, "eq-"+tc.name, measures...)
	return resolved, ordered, obs
}

// eqRateSeams drives both seams over the SAME resolved snapshot and the SAME
// single observation, and returns each seam's structural outcome.
func eqRateSeams(t *testing.T, resolved economics.TariffSnapshot, obs metering.Observation, nodes []metering.ComponentKey) map[string]eqOutcome {
	t.Helper()
	outcomes := make(map[string]eqOutcome, len(review5beSeams()))
	for _, seam := range review5beSeams() {
		val, err := seam.rate(t, resolved, obs)
		outcome := eqInspect(val, nodes)
		outcome.errClass = eqErrClass(err)
		outcomes[seam.name] = outcome
	}
	return outcomes
}

// TestEquivalenceOperatorAndCustomerPolicySeamsAgreeStructurally is the
// section-16 E/R equivalence harness. Every case drives the SAME selected
// quantity set and the SAME effective tariff through the operator E seam and
// the customer-policy R seam, and the four structural facets must match.
//
// Any disagreement is reported as a named case with the exact facet difference,
// and the per-class agreement rate is logged so a regression is visible as a
// number as well as a failure.
func TestEquivalenceOperatorAndCustomerPolicySeamsAgreeStructurally(t *testing.T) {
	t.Parallel()
	cases := eqCases()
	perClass := make(map[string]int, len(eqClasses))
	for _, class := range eqClasses {
		perClass[class] = 0
	}
	agreed := make(map[string]int, len(eqClasses))
	disagreements := make(map[string][]string, len(eqClasses))
	var tallyMu sync.Mutex

	for _, tc := range cases {
		if _, known := perClass[tc.class]; !known {
			t.Fatalf("case %q declares unknown graph class %q", tc.name, tc.class)
		}
		perClass[tc.class]++
		t.Run(tc.class+"/"+tc.name, func(t *testing.T) {
			t.Parallel()
			resolved, nodes, obs := eqBuild(t, tc)
			outcomes := eqRateSeams(t, resolved, obs, nodes)

			operator, operatorOK := outcomes["operator_E"]
			customer, customerOK := outcomes["customer_policy_R"]
			if !operatorOK || !customerOK {
				t.Fatalf("case %q: seam table is missing a seam: %+v", tc.name, outcomes)
			}
			if diff := operator.diff(customer); diff != "" {
				tallyMu.Lock()
				disagreements[tc.class] = append(disagreements[tc.class],
					fmt.Sprintf("case=%q class=%q %s", tc.name, tc.class, diff))
				tallyMu.Unlock()
				t.Errorf("E/R structural disagreement in %s\n  case: %s\n  class: %s\n  %s\n  E: err=%s completeness=%s\n  R: err=%s completeness=%s",
					tc.name, tc.name, tc.class, diff,
					operator.errClass, operator.completeness,
					customer.errClass, customer.completeness)
				return
			}
			tallyMu.Lock()
			agreed[tc.class]++
			tallyMu.Unlock()
			t.Logf("E/R-EQUIVALENCE case=%q class=%q err_class=%s completeness=%s support=%v pre_round=%v",
				tc.name, tc.class, operator.errClass, operator.completeness,
				operator.support, operator.preRound)
		})
	}

	t.Cleanup(func() {
		total, agree, disagreed := 0, 0, 0
		for _, class := range eqClasses {
			total += perClass[class]
			agree += agreed[class]
			disagreed += perClass[class] - agreed[class]
		}
		t.Logf("E/R-EQUIVALENCE cases=%d agreed=%d disagreed=%d agreement_rate=%.4f",
			total, agree, disagreed, float64(agree)/float64(total))
		for _, class := range eqClasses {
			t.Logf("E/R-EQUIVALENCE class=%s cases=%d agreed=%d disagreed=%d",
				class, perClass[class], agreed[class], perClass[class]-agreed[class])
		}
		for _, class := range eqClasses {
			for _, detail := range disagreements[class] {
				t.Logf("E/R-EQUIVALENCE DISAGREEMENT class=%s %s", class, detail)
			}
		}
	})
}

// TestEquivalenceSeamOutcomeIsStableAcrossRepetition proves the harness is
// comparing a stable quantity and not a flaky one: the same case rated N times
// on the same seam yields the identical structural tuple every time, so a
// disagreement above can never be a measurement artifact of one sampling.
func TestEquivalenceSeamOutcomeIsStableAcrossRepetition(t *testing.T) {
	t.Parallel()
	for _, tc := range eqCases() {
		t.Run(tc.class+"/"+tc.name, func(t *testing.T) {
			t.Parallel()
			resolved, nodes, obs := eqBuild(t, tc)
			first := eqRateSeams(t, resolved, obs, nodes)
			for attempt := range 3 {
				again := eqRateSeams(t, resolved, obs, nodes)
				for seam, want := range first {
					got, ok := again[seam]
					if !ok {
						t.Fatalf("seam %q vanished on repetition %d", seam, attempt)
					}
					if diff := want.diff(got); diff != "" {
						t.Fatalf("seam %q is not stable across repetition %d: %s", seam, attempt, diff)
					}
				}
			}
			_ = nodes
		})
	}
}

// eqReachableSentinels is the structural sentinel set the equivalence table is
// required to reach.
//
// ErrSchemaQuantityContradiction is deliberately NOT in it. That sentinel exists
// so the rater refuses to CLAMP a negative effective reduced quantity, but
// pkg/lipsdk/metering refuses a negative Measure value at the observation
// boundary, so no published evidence can reach the rater with one.
// TestEquivalenceNegativeQuantityIsBlockedAtTheSDKBoundary pins that fact
// rather than leaving the class silently unreached in this table.
var eqReachableSentinels = []string{
	"overlap_conflict",
	"partition_contradiction",
	"partition_incomparable",
	"partition_incomplete",
	"subset_contradiction",
}

// TestEquivalenceNegativeQuantityIsBlockedAtTheSDKBoundary pins why
// ErrSchemaQuantityContradiction is absent from the equivalence table. A negative
// Measure value is refused by the public observation validator with a
// correction/replacement message, so the rater's refuse-to-clamp guard can never
// be reached through a published observation. If a future SDK release starts
// admitting such a measure, this test fails and the sentinel joins
// eqReachableSentinels rather than staying quietly untested.
func TestEquivalenceNegativeQuantityIsBlockedAtTheSDKBoundary(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: "vendor:eq_negative_probe",
		Unit:      metering.UnitToken,
		SchemaID:  b1SchemaID,
	}
	negative, err := metering.ParseDecimal("-5")
	if err != nil {
		t.Fatalf("ParseDecimal(-5): %v", err)
	}
	obs := f3Observation(t, "eq-negative-probe", metering.Measure{
		Key: key, Value: &negative, Quality: metering.QualityObserved, MethodRef: b1SchemaID,
	})
	if err := obs.Validate(); err == nil {
		t.Fatalf("a negative Measure value is now accepted by pkg/lipsdk/metering; " +
			"ErrSchemaQuantityContradiction has become reachable and must be added to eqReachableSentinels " +
			"with a case that exercises it")
	} else if !strings.Contains(err.Error(), "negative value requires correction/replacement") {
		t.Fatalf("negative measure rejected for an unexpected reason %v; the guard "+
			"that keeps ErrSchemaQuantityContradiction unreachable is no longer the correction/replacement gate", err)
	}
}

// TestEquivalenceHarnessIsNotVacuous proves the case table actually reaches
// EVERY reachable structural path the equivalence claim is about. It requires all
// five production schema sentinels reachable through a published observation,
// plus nil, all three completeness classes, and a spread of distinct exact
// pre-round amounts. Without this, a table that silently stopped declaring
// relationships, or a harness that stopped reading the error, would still report
// a perfect 100% agreement rate over one path.
func TestEquivalenceHarnessIsNotVacuous(t *testing.T) {
	t.Parallel()
	classesSeen := map[string]struct{}{}
	completenessSeen := map[economics.Completeness]struct{}{}
	preRoundRatios := map[string]struct{}{}
	for _, tc := range eqCases() {
		resolved, nodes, obs := eqBuild(t, tc)
		outcomes := eqRateSeams(t, resolved, obs, nodes)
		for _, outcome := range outcomes {
			classesSeen[outcome.errClass] = struct{}{}
			completenessSeen[outcome.completeness] = struct{}{}
			for _, amount := range outcome.preRound {
				if amount != "" {
					preRoundRatios[amount] = struct{}{}
				}
			}
		}
	}
	if _, ok := classesSeen["nil"]; !ok {
		t.Errorf("equivalence table never reached a clean structural class; classes seen: %v", eqSortedSet(classesSeen))
	}
	for _, class := range eqReachableSentinels {
		if _, ok := classesSeen[class]; !ok {
			t.Errorf("equivalence table never reached structural class %q; classes seen: %v", class, eqSortedSet(classesSeen))
		}
	}
	for _, completeness := range []economics.Completeness{
		economics.CompletenessComplete,
		economics.CompletenessPartial,
		economics.CompletenessConflict,
	} {
		if _, ok := completenessSeen[completeness]; !ok {
			t.Errorf("equivalence table never reached completeness %q; %d distinct completenesses seen", completeness, len(completenessSeen))
		}
	}
	if len(preRoundRatios) < 10 {
		t.Fatalf("equivalence table produced only %d distinct exact pre-round amounts, want at least 10; seen: %v",
			len(preRoundRatios), eqSortedSet(preRoundRatios))
	}
	t.Logf("E/R-EQUIVALENCE coverage: %d distinct error classes %v, completenesses %d, distinct pre-round amounts %d",
		len(classesSeen), eqSortedSet(classesSeen), len(completenessSeen), len(preRoundRatios))
}

// eqSortedSet renders a set as a sorted slice, for deterministic reporting.
func eqSortedSet(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
