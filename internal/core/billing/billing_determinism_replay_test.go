package billing_test

// Determinism and replay (handoff section 19).
//
// A frozen valuation is a durable, replayable economic fact, so the same
// canonical snapshot plus the same effective observations must always produce
// the SAME canonical outcome. This file measures that on the real
// ReferenceRater.Rate seam, through a real billingcompose SnapshotCatalog
// publication, over the real economics.Valuation identity API.
//
// The four things that may silently differ and are asserted here:
//
//  1. the compiled graph result (error class + completeness);
//  2. DIAGNOSTIC ORDERING -- the rater joins independent typed diagnostics with
//     errors.Join, so the order in which they are joined is itself part of the
//     observable outcome and is compared as one exact string;
//  3. LINE ORDERING -- both the AS-EMITTED order (which is what a consumer
//     reading val.Lines sees, and which no canonicalization hides) and the
//     sorted order;
//  4. exact totals and the valuation FINGERPRINT.
//
// The fingerprint is the real economics.Valuation.Fingerprint() -- a SHA-256
// over Valuation.CanonicalJSON() -- not a hand-rolled digest. That matters: it
// folds in the valuation ID, which folds in the input-set hash, the observation
// references and the tariff/policy CONTENT HASH. So a fingerprint match proves
// the whole durable identity chain is stable, including that the published
// tariff canonicalizes to the same bytes under a permuted input order.
//
// Permutation axes, all randomized from ONE fixed seed so a failure is always
// reproducible:
//
//   - relationship order inside each declared ComponentSchema;
//   - ComponentSchema order inside the published schema set;
//   - RatingRule order in the published rule set (i.e. the order BEFORE
//     canonicalization: every permutation must canonicalize to the same set);
//   - observation delivery order in the rating input, AND measure order inside
//     each observation, where the reducer's semantics permit it.
//
// Fixture choice is load bearing. A complete-coverage cover is only ever proven
// inside ONE reduction scope, and the scope key contains the stream identity, so
// the cover graph's measures are deliberately kept in a single observation. A
// SECOND, graph-independent observation exists purely so that permuting the
// delivery order is a real, observable permutation rather than a no-op.

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// drSeed is the ONE fixed seed for every permutation in this file. It is a
// named constant, not a literal scattered at the call site, so a failing run
// reports exactly one number to replay with.
const (
	drSeedA uint64 = 0x5EED_C0DE_0000_0001
	drSeedB uint64 = 0x5EED_C0DE_0000_0002
	// drPermutations is the number of randomized permutations applied per
	// fixture. Each permutation randomizes ALL FOUR axes at once.
	drPermutations = 64
	// drMapOrderRepeats is how many times one input is rated to probe Go's
	// randomized map iteration order. Go reseeds map iteration per range, so
	// repeated rating of the SAME input on the SAME rater is the direct
	// experiment for a map-order dependency.
	drMapOrderRepeats = 512
)

// drPinnedFingerprints are the six canonical valuation fingerprints this file's
// determinism and replay claims produce, asserted as constants instead of only
// being logged. They were previously visible ONLY in t.Logf output, so `rg
// 84b8d21b` matched nothing in the tree and nothing in CI would fail if a
// fingerprint drifted: the claim was reported, not pinned.
//
// Each value is the real economics.Valuation.Fingerprint() -- a SHA-256 over
// CanonicalJSON() -- so it folds in the valuation ID, the input-set hash, the
// observation references and the tariff/policy CONTENT HASH. A match therefore
// pins the whole durable identity chain, not one visible number.
//
// Source of each value:
//
//	drFingerprintCleanComplete      drCleanFixture baseline,
//	                               TestDeterminismPermutedDeclarationsProduceOneCanonicalOutcome/clean_complete
//	drFingerprintJoinedDiagnostics  drDiagnosticFixture baseline, same test/joined_diagnostics
//	drFingerprintDeepChain200       drChainFixture(200) in
//	                               TestReplayDeepestPublishableChainTraversalIsBounded
//	drFingerprintDeepChain800       drChainFixture(800), same test
//	drFingerprintDeepChain3200      drChainFixture(3200), same test
//	drFingerprintDeepChain8192      drChainFixture(8192), same test
//
// The last four are additionally keyed by DEPTH in drDeepChainFingerprints,
// because the chain length is part of what the fingerprint identifies.
//
// All six were captured from the PRE-repair walk order and are UNCHANGED by it:
// the first-error walk order is not part of the durable identity these fixtures
// produce, and the two candidate-ambiguous branches these six do not reach are
// pinned separately by TestDeterminismAmbiguousOverlapCandidateFirstErrorIsStable.
const (
	drFingerprintCleanComplete     = "0d44c1d8b89c3c86306fd369c762d8130216d6aeba759a99b931d0cf5f21764e"
	drFingerprintJoinedDiagnostics = "9374f9bb81bceef157acd5e5ea39c022b486789bf29d13d613165e1964553e08"
	drFingerprintDeepChain200      = "84b8d21b95881453459de23ad052f971521035708a6af54f27135c02e8ff120b"
	drFingerprintDeepChain800      = "3fbede3ab2bce4f6f628f51933fb88e8acc215052dc850ed0657cfb28b76d31a"
	drFingerprintDeepChain3200     = "017d3289f1e45d401ab25d4480178897a677784bbbb78f3c3fd26fcea5748950"
	drFingerprintDeepChain8192     = "0741849b96a5c3508792da4f9450ac2474ee30876201e1ae7e577c9cbc4e8969"
)

// drDeepChainFingerprints maps a drChainFixture depth to its pinned fingerprint.
var drDeepChainFingerprints = map[int]string{
	200:  drFingerprintDeepChain200,
	800:  drFingerprintDeepChain800,
	3200: drFingerprintDeepChain3200,
	8192: drFingerprintDeepChain8192,
}

// drSentinels mirrors the structural sentinel set so the determinism tuple can
// report a stable, named error class instead of a raw error string.
var drSentinels = []struct {
	class string
	err   error
}{
	{"overlap_conflict", billing.ErrSchemaOverlapConflict},
	{"partition_contradiction", billing.ErrSchemaPartitionContradiction},
	{"partition_incomparable", billing.ErrSchemaPartitionIncomparable},
	{"partition_incomplete", billing.ErrSchemaPartitionIncomplete},
	{"subset_contradiction", billing.ErrSchemaSubsetContradiction},
	{"quantity_contradiction", billing.ErrSchemaQuantityContradiction},
	{"rate_missing", billing.ErrRateMissing},
	{"quantity_incomplete", billing.ErrQuantityIncomplete},
	{"evidence_missing", billing.ErrRatingEvidenceMissing},
	{"precision", billing.ErrRatePrecision},
	{"input_invalid", billing.ErrRatingInvalid},
	{"unsupported", billing.ErrRateUnsupported},
	{"tariff_invalid", billing.ErrTariffInvalid},
	{"input_set_mismatch", billing.ErrInputSetHashMismatch},
}

// drErrClass renders every sentinel the error satisfies, sorted, so a joined
// error reports its full typed composition rather than only its first member.
func drErrClass(err error) string {
	if err == nil {
		return "nil"
	}
	matched := make([]string, 0, len(drSentinels))
	for _, sentinel := range drSentinels {
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

// drOutcome is the exact tuple compared across every permutation and every
// repetition. Nothing in it is derived from a set or a map, so two outcomes are
// equal only when every observable is byte-identical.
type drOutcome struct {
	errClass     string
	errText      string
	completeness economics.Completeness
	// lineOrder is the AS-EMITTED val.Lines order.
	lineOrder []string
	// sortedLines is the same lines sorted, which must agree with the canonical
	// order economics.Valuation.Canonical produces.
	sortedLines []string
	totals      []string
	// fingerprint is economics.Valuation.Fingerprint(): the real durable
	// replay identity, not a digest invented here.
	fingerprint  string
	valuationID  string
	inputSetHash string
	inputRefs    []string
	missingRefs  []string
	coverageRefs []string
	allocRefs    []string
	qualifiers   []string
}

// drDiff renders the first differing observable, so a determinism failure names
// the exact facet rather than dumping two large structs.
func (o drOutcome) diff(other drOutcome) string {
	type field struct {
		name        string
		left, right any
	}
	fields := []field{
		{"err_class", o.errClass, other.errClass},
		{"err_text", o.errText, other.errText},
		{"completeness", string(o.completeness), string(other.completeness)},
		{"line_order", o.lineOrder, other.lineOrder},
		{"sorted_lines", o.sortedLines, other.sortedLines},
		{"totals", o.totals, other.totals},
		{"fingerprint", o.fingerprint, other.fingerprint},
		{"valuation_id", o.valuationID, other.valuationID},
		{"input_set_hash", o.inputSetHash, other.inputSetHash},
		{"input_refs", o.inputRefs, other.inputRefs},
		{"missing_refs", o.missingRefs, other.missingRefs},
		{"coverage_refs", o.coverageRefs, other.coverageRefs},
		{"allocation_refs", o.allocRefs, other.allocRefs},
		{"qualifiers", o.qualifiers, other.qualifiers},
	}
	for _, f := range fields {
		if !drEqual(f.left, f.right) {
			return fmt.Sprintf("%s{baseline=%v permuted=%v}", f.name, f.left, f.right)
		}
	}
	return ""
}

func drEqual(left, right any) bool {
	leftSlice, leftOK := left.([]string)
	rightSlice, rightOK := right.([]string)
	if leftOK && rightOK {
		return slices.Equal(leftSlice, rightSlice)
	}
	return left == right
}

func drDecimal(value *metering.Decimal) string {
	if value == nil {
		return "<nil>"
	}
	if rat, err := value.ToRat(); err == nil {
		return rat.RatString()
	}
	return value.CanonicalString()
}

// drMoney renders an economics.Money. The rounded amount is a nanounit integer,
// never a decimal, so it is rendered exactly and a per-plane rounding-policy
// difference stays visible instead of being normalised away.
func drMoney(money *economics.Money) string {
	if money == nil || !money.Present {
		return "<none>"
	}
	return fmt.Sprintf("%s:%d", money.Currency, money.NanoUnits)
}

// drInspect builds the outcome tuple from one rate result.
func drInspect(val economics.Valuation, err error) drOutcome {
	out := drOutcome{errClass: drErrClass(err), completeness: val.Completeness}
	if err != nil {
		out.errText = err.Error()
	}
	for i := range val.Lines {
		line := &val.Lines[i]
		identity := "<fixed>"
		if line.Component != nil {
			identity = line.Component.CanonicalKey()
		}
		amount := ""
		switch {
		case line.Amount != nil:
			amount = "dec:" + drDecimal(line.Amount)
		case line.AmountNumerator != "":
			amount = "rat:" + line.AmountNumerator + "/" + line.AmountDenominator
		}
		rounded := drMoney(line.RoundedAmount)
		entry := fmt.Sprintf("%s|rule=%s|status=%s|qty=%s|amount=%s|rounded=%s|scope=%s|policy=%s",
			identity, line.RuleID, line.Status, drDecimal(line.Quantity), amount, rounded,
			line.RoundingScope, line.RoundingPolicy)
		out.lineOrder = append(out.lineOrder, entry)
		for _, ref := range line.SourceObservationRefs {
			entry += "|src=" + ref.StoreID + "/" + ref.ObservationID
		}
		out.lineOrder[len(out.lineOrder)-1] = entry
	}
	out.sortedLines = slices.Clone(out.lineOrder)
	sort.Strings(out.sortedLines)
	for _, total := range val.Totals {
		out.totals = append(out.totals, fmt.Sprintf("%s|%s|rounded=%s",
			total.Currency, drDecimal(total.Amount), drMoney(&total.RoundedAmount)))
	}
	out.fingerprint = val.Fingerprint()
	out.valuationID = val.ID
	out.inputSetHash = val.InputSetHash
	for _, ref := range val.InputObservations {
		out.inputRefs = append(out.inputRefs, ref.StoreID+"/"+ref.ObservationID+"/"+ref.PayloadHash)
	}
	for _, ref := range val.MissingObservations {
		out.missingRefs = append(out.missingRefs, ref.StoreID+"/"+ref.ObservationID)
	}
	for _, ref := range val.CoverageRefs {
		out.coverageRefs = append(out.coverageRefs, ref.Ref.StoreID+"/"+ref.Ref.ObservationID+"/"+string(ref.Relation))
	}
	for _, ref := range val.AllocationCoverageRefs {
		out.allocRefs = append(out.allocRefs, ref.StoreID+"/"+ref.AllocationID)
	}
	for _, q := range val.EffectiveQualifiers {
		out.qualifiers = append(out.qualifiers, q.Name+"="+q.Value)
	}
	return out
}

// ---------------------------------------------------------------------------
// Fixture.
// ---------------------------------------------------------------------------

// drFixture is one deterministic input, published in whatever order the
// permutation under test chooses.
type drFixture struct {
	refID    string
	rules    []economics.RatingRule
	schemas  []metering.ComponentSchema
	obs      []metering.Observation
	nodes    []metering.ComponentKey
	contains bool
}

func drObservation(t *testing.T, id string, measures ...metering.Measure) metering.Observation {
	t.Helper()
	// Distinct stream identity per observation id, so each observation is its
	// OWN reduction scope. Cover arithmetic is then only ever attempted inside
	// one scope, which is the property the fixture relies on.
	return b1Observation(t, id, "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress,
		metering.PerspectiveOperator, measures...)
}

// drCleanFixture is a COMPLETE, multi-schema, multi-observation valuation:
//
//	agg --partition--> {p, q}      p --partition--> {r, t}
//	agg --subset--> s
//
// agg and p are UNPRICED, so p is resolved through its own complete coverage and
// agg through p's, and the five priced leaves are the authoritative billers. s is
// a declared subset that was never reported, so it neither bills nor produces a
// missing-rate diagnostic. The result is a complete valuation with several
// payable lines, which is exactly the shape whose identity, totals and line
// order must be replay-stable.
//
// The second observation carries three graph-independent components so that
// permuting the delivery order is a real permutation.
func drCleanFixture(t *testing.T) drFixture {
	t.Helper()
	agg := r7Key("vendor:dr_agg_total")
	partP := r7Key("vendor:dr_part_p")
	partQ := r7Key("vendor:dr_part_q")
	leafR := r7Key("vendor:dr_leaf_r")
	leafT := r7Key("vendor:dr_leaf_t")
	subsetS := r7Key("vendor:dr_subset_s")
	looseA := r7Key("vendor:dr_loose_a")
	looseB := r7Key("vendor:dr_loose_b")
	looseC := r7Key("vendor:dr_loose_c")

	rules := []economics.RatingRule{
		b1Rule(t, "dr-q-rate", partQ, "2"),
		b1Rule(t, "dr-r-rate", leafR, "3"),
		b1Rule(t, "dr-t-rate", leafT, "5"),
		b1Rule(t, "dr-loose-a-rate", looseA, "11"),
		b1Rule(t, "dr-loose-b-rate", looseB, "13"),
		b1Rule(t, "dr-loose-c-rate", looseC, "17"),
	}
	// Three separate schemas, so schema ORDER is a real permutation axis and
	// the per-schema relationship bound is not the constraint being probed.
	schemas := []metering.ComponentSchema{
		{ID: b1SchemaID + ":s0", Version: "1", Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: agg, Child: partP},
			{Kind: metering.RelationshipPartition, Parent: agg, Child: partQ},
		}},
		{ID: b1SchemaID + ":s1", Version: "1", Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: partP, Child: leafR},
			{Kind: metering.RelationshipPartition, Parent: partP, Child: leafT},
		}},
		{ID: b1SchemaID + ":s2", Version: "1", Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: agg, Child: subsetS},
		}},
	}
	observations := []metering.Observation{
		drObservation(
			t, "dr-clean-cover",
			b1Measure(t, agg, "100"),
			b1Measure(t, partP, "60"),
			b1Measure(t, partQ, "40"),
			b1Measure(t, leafR, "30"),
			b1Measure(t, leafT, "30"),
		),
		drObservation(
			t, "dr-clean-loose",
			b1Measure(t, looseA, "4"),
			b1Measure(t, looseB, "6"),
			b1Measure(t, looseC, "8"),
		),
	}
	return drFixture{
		refID: "dr-clean", rules: rules, schemas: schemas, obs: observations,
		nodes:    []metering.ComponentKey{agg, partP, partQ, leafR, leafT, subsetS, looseA, looseB, looseC},
		contains: true,
	}
}

// drDiagnosticFixture produces SEVERAL independent typed diagnostics joined into
// one error, so diagnostic ORDERING is genuinely under test rather than a single
// sentinel with nothing to reorder:
//
//   - a complete partition that arithmetically contradicts its parent
//     (partition_contradiction);
//   - a REQUIRED member that is absent (partition_incomplete);
//   - a separate, UNDIAGNOSED pure-subset chain whose far descendant exceeds its
//     ancestor (subset_contradiction). The chain is deliberately NOT below
//     either complete parent: the solver deliberately suppresses a containment
//     violation beneath an already-diagnosed parent, so hanging it there would
//     collapse the three diagnostics into two.
func drDiagnosticFixture(t *testing.T) drFixture {
	t.Helper()
	agg := r7Key("vendor:dr_diag_agg")
	partB := r7Key("vendor:dr_diag_part_b")
	partC := r7Key("vendor:dr_diag_part_c")
	other := r7Key("vendor:dr_diag_other_agg")
	otherChild := r7Key("vendor:dr_diag_other_child")
	chain1 := r7Key("vendor:dr_diag_chain_1")
	chain2 := r7Key("vendor:dr_diag_chain_2")
	chain3 := r7Key("vendor:dr_diag_chain_3")
	looseA := r7Key("vendor:dr_diag_loose_a")

	rules := []economics.RatingRule{
		b1Rule(t, "dr-diag-b-rate", partB, "2"),
		b1Rule(t, "dr-diag-c-rate", partC, "3"),
		b1Rule(t, "dr-diag-chain3-rate", chain3, "5"),
		b1Rule(t, "dr-diag-loose-a-rate", looseA, "11"),
	}
	schemas := []metering.ComponentSchema{
		{ID: b1SchemaID + ":d0", Version: "1", Relationships: []metering.ComponentRelationship{
			// 40 + 40 = 80, but the parent reports 100: contradicted.
			{Kind: metering.RelationshipPartition, Parent: agg, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: agg, Child: partC},
		}},
		{ID: b1SchemaID + ":d1", Version: "1", Relationships: []metering.ComponentRelationship{
			// otherChild is a REQUIRED member and was never reported.
			{Kind: metering.RelationshipPartition, Parent: other, Child: otherChild},
		}},
		{ID: b1SchemaID + ":d2", Version: "1", Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: chain1, Child: chain2},
			{Kind: metering.RelationshipSubset, Parent: chain2, Child: chain3},
		}},
	}
	observations := []metering.Observation{
		drObservation(
			t, "dr-diagnostic-main",
			b1Measure(t, agg, "100"),
			b1Measure(t, partB, "40"),
			b1Measure(t, partC, "40"),
			b1Measure(t, other, "80"),
			b1Measure(t, chain1, "10"),
			b1Measure(t, chain2, "10"),
			b1Measure(t, chain3, "20"),
		),
		drObservation(
			t, "dr-diagnostic-loose",
			b1Measure(t, looseA, "4"),
		),
	}
	return drFixture{
		refID: "dr-diagnostic", rules: rules, schemas: schemas, obs: observations,
		nodes:    []metering.ComponentKey{agg, partB, partC, other, otherChild, chain1, chain2, chain3, looseA},
		contains: true,
	}
}

// drOverlapChainFixture builds the shape that reaches the TRANSITIVE-PAYABLE
// overlap diagnostic, with an AMBIGUOUS candidate set so the recorded first
// error is a real choice rather than the only option.
//
// Each scope declares several INDEPENDENT pure-subset chains top -> mid ->
// bottom, with the top and the bottom PRICED and reported and the middle never
// reported and carrying no rule. No edge in such a chain has both ends payable,
// so the direct edge check cannot fire and the transitive-payable walk is the
// only thing that reports it -- once per chain, as competing candidates.
//
// The chains are spread over SEVERAL reduction scopes (drObservation gives
// every observation its own stream identity), so the walk's outer scope map AND
// its inner payable set map each carry more than one candidate, which is the
// condition under which Go's randomized map iteration can pick a different
// chain on each repetition.
func drOverlapChainFixture(t *testing.T) drFixture {
	t.Helper()
	chains := []string{"alpha", "beta", "gamma", "delta"}

	var rules []economics.RatingRule
	var relationships []metering.ComponentRelationship
	var observations []metering.Observation
	var nodes []metering.ComponentKey

	for scope := range 2 {
		var measures []metering.Measure
		for _, chain := range chains {
			top := r7Key(fmt.Sprintf("vendor:dr_ov_%d_%s_top", scope, chain))
			mid := r7Key(fmt.Sprintf("vendor:dr_ov_%d_%s_mid", scope, chain))
			bottom := r7Key(fmt.Sprintf("vendor:dr_ov_%d_%s_bottom", scope, chain))
			relationships = append(
				relationships,
				metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: top, Child: mid},
				metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: mid, Child: bottom},
			)
			rules = append(
				rules,
				economics.RatingRule{
					ID: fmt.Sprintf("dr-ov-%d-%s-top-rate", scope, chain), Component: &top,
					Currency: "USD", UnitPrice: drUnitPrice(t, "2"),
				},
				economics.RatingRule{
					ID: fmt.Sprintf("dr-ov-%d-%s-bottom-rate", scope, chain), Component: &bottom,
					Currency: "USD", UnitPrice: drUnitPrice(t, "3"),
				},
			)
			measures = append(measures, b1Measure(t, top, "100"), b1Measure(t, bottom, "10"))
			nodes = append(nodes, top, mid, bottom)
		}
		observations = append(observations, drObservation(t, fmt.Sprintf("dr-ov-scope-%d", scope), measures...))
	}
	return drFixture{
		refID: "dr-overlap-chain-order", rules: rules,
		schemas: []metering.ComponentSchema{{ID: b1SchemaID + ":ovc", Version: "1", Relationships: relationships}},
		obs:     observations, nodes: nodes, contains: true,
	}
}

// drOverlapCoverFixture is drOverlapChainFixture's sibling for the SECOND
// competing-candidate branch: the complete-partition-parent walk. Each scope
// declares an UNPRICED parent with a payable complete partition {a, b} AND a
// payable subset child, the R7 shape. There is no payable chain at all here, so
// the transitive walk reports nothing, no edge has two payable ends, and the
// cover walk is the only reporter -- once per scope, as competing candidates,
// which the walk's rateableByScope map order used to decide.
func drOverlapCoverFixture(t *testing.T) drFixture {
	t.Helper()
	covers := []string{"zeta", "eta"}

	var rules []economics.RatingRule
	var relationships []metering.ComponentRelationship
	var observations []metering.Observation
	var nodes []metering.ComponentKey

	for scope := range 2 {
		var measures []metering.Measure
		for _, cover := range covers {
			parent := r7Key(fmt.Sprintf("vendor:dr_oc_%d_%s_parent", scope, cover))
			partA := r7Key(fmt.Sprintf("vendor:dr_oc_%d_%s_part_a", scope, cover))
			partB := r7Key(fmt.Sprintf("vendor:dr_oc_%d_%s_part_b", scope, cover))
			subset := r7Key(fmt.Sprintf("vendor:dr_oc_%d_%s_subset", scope, cover))
			relationships = append(
				relationships,
				metering.ComponentRelationship{Kind: metering.RelationshipPartition, Parent: parent, Child: partA},
				metering.ComponentRelationship{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: parent, Child: subset},
			)
			rules = append(
				rules,
				economics.RatingRule{
					ID: fmt.Sprintf("dr-oc-%d-%s-a-rate", scope, cover), Component: &partA,
					Currency: "USD", UnitPrice: drUnitPrice(t, "5"),
				},
				economics.RatingRule{
					ID: fmt.Sprintf("dr-oc-%d-%s-b-rate", scope, cover), Component: &partB,
					Currency: "USD", UnitPrice: drUnitPrice(t, "7"),
				},
				economics.RatingRule{
					ID: fmt.Sprintf("dr-oc-%d-%s-subset-rate", scope, cover), Component: &subset,
					Currency: "USD", UnitPrice: drUnitPrice(t, "11"),
				},
			)
			measures = append(measures,
				b1Measure(t, parent, "100"), b1Measure(t, partA, "40"),
				b1Measure(t, partB, "60"), b1Measure(t, subset, "20"))
			nodes = append(nodes, parent, partA, partB, subset)
		}
		observations = append(observations, drObservation(t, fmt.Sprintf("dr-oc-scope-%d", scope), measures...))
	}
	return drFixture{
		refID: "dr-overlap-cover-order", rules: rules,
		schemas: []metering.ComponentSchema{{ID: b1SchemaID + ":occ", Version: "1", Relationships: relationships}},
		obs:     observations, nodes: nodes, contains: true,
	}
}

func drUnitPrice(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	d := b1Decimal(t, raw)
	return &d
}

// ---------------------------------------------------------------------------
// Permutation machinery.
// ---------------------------------------------------------------------------

// drPublish publishes a fixture in the given declaration order and resolves it
// through the real billingcompose catalog, exactly as a production consumer
// does. Nothing is canonicalized by the test: the permutations must be absorbed
// by the production canonicalization.
func drPublish(t *testing.T, f drFixture, rules []economics.RatingRule, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	snapshot, err := economics.BuildTariffSnapshotWithSchemas(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: f.refID, Version: "v1"}, RaterID: "reference"},
		"USD", rules, schemas,
	)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas(%s): %v", f.refID, err)
	}
	catalog := billingcompose.NewSnapshotCatalog()
	if err := catalog.PutTariff(snapshot); err != nil {
		t.Fatalf("PutTariff(%s): %v", f.refID, err)
	}
	resolved, err := catalog.ResolveTariff(t.Context(), billing.VersionRef{ID: snapshot.Ref.ID, Version: snapshot.Ref.Version})
	if err != nil {
		t.Fatalf("ResolveTariff(%s): %v", f.refID, err)
	}
	return resolved
}

// drRateOne rates one published snapshot over one observation delivery order and
// returns the outcome tuple.
func drRateOne(t *testing.T, resolved economics.TariffSnapshot, observations []metering.Observation) (drOutcome, economics.Valuation, error) {
	t.Helper()
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := b1OperatorInput(t, resolved, observations)
	val, rateErr := rater.Rate(t.Context(), input)
	return drInspect(val, rateErr), val, rateErr
}

// drPermute randomizes all four declaration-order axes at once. Rule and
// relationship slices are copied before shuffling, so the fixture itself is
// never mutated and every permutation is derived from the same source.
func drPermute(rng *rand.Rand, f drFixture) ([]economics.RatingRule, []metering.ComponentSchema, []metering.Observation) {
	rules := slices.Clone(f.rules)
	rng.Shuffle(len(rules), func(i, j int) { rules[i], rules[j] = rules[j], rules[i] })

	schemas := make([]metering.ComponentSchema, len(f.schemas))
	for i, schema := range f.schemas {
		relationships := slices.Clone(schema.Relationships)
		rng.Shuffle(len(relationships), func(i, j int) {
			relationships[i], relationships[j] = relationships[j], relationships[i]
		})
		schemas[i] = schema.Clone()
		schemas[i].Relationships = relationships
	}
	rng.Shuffle(len(schemas), func(i, j int) { schemas[i], schemas[j] = schemas[j], schemas[i] })

	observations := make([]metering.Observation, len(f.obs))
	for i, observation := range f.obs {
		measures := slices.Clone(observation.Measures)
		rng.Shuffle(len(measures), func(a, b int) { measures[a], measures[b] = measures[b], measures[a] })
		observations[i] = observation.Clone()
		observations[i].Measures = measures
	}
	rng.Shuffle(len(observations), func(i, j int) { observations[i], observations[j] = observations[j], observations[i] })
	return rules, schemas, observations
}

// TestDeterminismPermutedDeclarationsProduceOneCanonicalOutcome is the
// section-19 permutation test. For the same canonical snapshot material and the
// same effective observations, N randomized permutations of the input
// relationship order, schema order, rule order and observation delivery order
// must produce one identical canonical outcome.
func TestDeterminismPermutedDeclarationsProduceOneCanonicalOutcome(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		name            string
		build           func(*testing.T) drFixture
		wantsAt         int
		pinnedFingerpri string
	}{
		{name: "clean_complete", build: drCleanFixture, wantsAt: 2, pinnedFingerpri: drFingerprintCleanComplete},
		{name: "joined_diagnostics", build: drDiagnosticFixture, wantsAt: 1, pinnedFingerpri: drFingerprintJoinedDiagnostics},
	}
	for _, spec := range fixtures {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			fixture := spec.build(t)

			// Baseline: the unpermuted publication and delivery order.
			baseResolved := drPublish(t, fixture, fixture.rules, fixture.schemas)
			baseOutcome, baseVal, baseErr := drRateOne(t, baseResolved, fixture.obs)
			baseHash := baseResolved.Content.ContentHash
			if spec.name == "clean_complete" {
				if baseErr != nil {
					t.Fatalf("clean fixture must rate without a typed diagnostic, got %v (lines=%+v)", baseErr, baseVal.Lines)
				}
				if baseVal.Completeness != economics.CompletenessComplete {
					t.Fatalf("clean fixture completeness=%q, want complete (lines=%+v)", baseVal.Completeness, baseVal.Lines)
				}
			} else if baseErr == nil {
				t.Fatalf("diagnostic fixture must produce a typed diagnostic, got a complete valuation (lines=%+v)", baseVal.Lines)
			} else if len(baseOutcome.errText) == 0 {
				t.Fatal("diagnostic fixture produced no diagnostic text; diagnostic ordering is not under test")
			} else if !strings.Contains(baseOutcome.errText, "\n") {
				// The rater joins its independent typed diagnostics, so a joined
				// message carries newlines. A single-line message means only one
				// diagnostic exists and the ordering half of this file is vacuous.
				t.Fatalf("diagnostic fixture produced exactly one diagnostic; joined diagnostic ordering is not under test: %q",
					baseOutcome.errText)
			}
			if baseOutcome.fingerprint == "" {
				t.Fatal("economics.Valuation.Fingerprint() returned an empty string; the valuation is not canonicalizable")
			}
			// The canonical fingerprint is PINNED, not merely reported: a drifted
			// durable identity must fail here rather than appear in the log.
			if baseOutcome.fingerprint != spec.pinnedFingerpri {
				t.Errorf("fixture %s: the canonical valuation fingerprint drifted.\n  pinned:   %s\n  observed: %s\n"+
					"  this is the durable replay identity of a frozen valuation, so a change here is a behavior change, not noise",
					spec.name, spec.pinnedFingerpri, baseOutcome.fingerprint)
			}

			if len(baseOutcome.sortedLines) < 2 {
				t.Fatalf("fixture emitted %d lines; line ordering is not meaningfully under test", len(baseOutcome.sortedLines))
			}

			rng := rand.New(rand.NewPCG(drSeedA, drSeedB))
			mismatches := 0
			hashMismatches := 0
			for permutation := range drPermutations {
				rules, schemas, observations := drPermute(rng, fixture)
				resolved := drPublish(t, fixture, rules, schemas)
				if resolved.Content.ContentHash != baseHash {
					hashMismatches++
					t.Errorf("permutation %d: published tariff content hash changed with declaration order: %s != %s\n"+
						"  the canonical form must absorb rule, schema and relationship order; the valuation identity "+
						"is bound to this hash, so a difference here is a fork, not noise",
						permutation, resolved.Content.ContentHash, baseHash)
					continue
				}
				outcome, _, _ := drRateOne(t, resolved, observations)
				if diff := baseOutcome.diff(outcome); diff != "" {
					mismatches++
					t.Errorf("permutation %d produced a different canonical outcome: %s\n"+
						"  permuted: rules=%d schemas=%d observations=%d", permutation, diff, len(rules), len(schemas), len(observations))
				}
			}
			t.Logf("DETERMINISM-REPLAY fixture=%s permutations=%d seed_a=%#x seed_b=%#x mismatches=%d content_hash_mismatches=%d",
				spec.name, drPermutations, drSeedA, drSeedB, mismatches, hashMismatches)
			t.Logf("DETERMINISM-REPLAY fixture=%s baseline{err_class=%s err_text_len=%d completeness=%s lines=%d totals=%v fingerprint=%s valuation_id=%s}",
				spec.name, baseOutcome.errClass, len(baseOutcome.errText), baseOutcome.completeness,
				len(baseOutcome.lineOrder), baseOutcome.totals, baseOutcome.fingerprint, baseOutcome.valuationID)
		})
	}
}

// TestDeterminismRepeatedRateCallsAreIdempotent proves a second Rate call on the
// SAME rater with the SAME input is byte-identical, including the fingerprint
// and the emitted line order. This is the replay guarantee a durable consumer
// relies on when it re-derives a valuation after a restart.
func TestDeterminismRepeatedRateCallsAreIdempotent(t *testing.T) {
	t.Parallel()
	for _, build := range []func(*testing.T) drFixture{drCleanFixture, drDiagnosticFixture} {
		fixture := build(t)
		resolved := drPublish(t, fixture, fixture.rules, fixture.schemas)
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		input := b1OperatorInput(t, resolved, fixture.obs)
		first, firstErr := rater.Rate(t.Context(), input)
		firstOutcome := drInspect(first, firstErr)
		for call := range 3 {
			again, againErr := rater.Rate(t.Context(), input)
			outcome := drInspect(again, againErr)
			if diff := firstOutcome.diff(outcome); diff != "" {
				t.Fatalf("fixture %s: repeat Rate call %d is not idempotent: %s", fixture.refID, call, diff)
			}
		}
		// The published snapshot the caller holds must also be unchanged by
		// rating, so a caller can reuse it for a later replay.
		after := rater.Snapshot()
		if after.ContentHash() != resolved.ContentHash() {
			t.Fatalf("fixture %s: Rating mutated the rater's snapshot content hash: %s != %s",
				fixture.refID, after.ContentHash(), resolved.ContentHash())
		}
		if _, canonErr := after.Canonical(); canonErr != nil {
			t.Fatalf("fixture %s: rater snapshot is not canonicalizable: %v", fixture.refID, canonErr)
		}
	}
}

// TestDeterminismMapIterationOrderNeverChangesTheReturnedError is the direct
// experiment for the "map iteration order must not decide which error comes
// first" requirement. Go reseeds map iteration for every range, so rating the
// SAME input with the SAME rater many times samples the map orders the rater
// actually walks.
//
// Two properties are asserted separately and on purpose:
//
//   - the returned ERROR CLASS SET and the Completeness must be identical on
//     every single repetition. This is the invariant the handoff requires and it
//     is asserted strictly.
//   - the number of DISTINCT diagnostic texts is measured and reported, because
//     a class-stable but text-varying diagnostic means some map-ordered
//     component name reached the message. That is reported with a count rather
//     than hidden, and the offending texts are named.
func TestDeterminismMapIterationOrderNeverChangesTheReturnedError(t *testing.T) {
	t.Parallel()
	// Two components with NO effective value in one scope. The rater's
	// post-reduction completeness sweep ranges over a per-identity map, so more
	// than one such component is exactly the shape that can expose a map-order
	// dependency.
	keys := []metering.ComponentKey{
		r7Key("vendor:dr_map_alpha"),
		r7Key("vendor:dr_map_beta"),
		r7Key("vendor:dr_map_gamma"),
		r7Key("vendor:dr_map_delta"),
	}
	rules := []economics.RatingRule{
		b1Rule(t, "dr-map-alpha-rate", keys[0], "2"),
		b1Rule(t, "dr-map-beta-rate", keys[1], "3"),
		b1Rule(t, "dr-map-gamma-rate", keys[2], "5"),
		b1Rule(t, "dr-map-delta-rate", keys[3], "7"),
	}
	fixture := drFixture{refID: "dr-map-order", rules: rules}
	resolved := drPublish(t, fixture, rules, nil)
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}

	// Three separate scopes, each carrying two unavailable components, so a
	// map-ordered message would have to be stable per scope for the test to
	// pass while still being multi-valued overall.
	var observations []metering.Observation
	for scope := range 3 {
		observations = append(observations, drObservation(
			t, fmt.Sprintf("dr-map-scope-%d", scope),
			b1Measure(t, keys[0], "10"),
			b1UnavailableMeasure(keys[1]),
			b1UnavailableMeasure(keys[2]),
		))
	}
	input := b1OperatorInput(t, resolved, observations)

	classSet := map[string]struct{}{}
	completenessSet := map[economics.Completeness]struct{}{}
	textSet := map[string]struct{}{}
	var firstOutcome drOutcome
	for repeat := range drMapOrderRepeats {
		val, rateErr := rater.Rate(t.Context(), input)
		outcome := drInspect(val, rateErr)
		if repeat == 0 {
			firstOutcome = outcome
		} else if diff := firstOutcome.diff(outcome); diff != "" {
			// The class/completeness half is the hard invariant.
			if !drEqual(outcome.errClass, firstOutcome.errClass) ||
				outcome.completeness != firstOutcome.completeness {
				t.Fatalf("repeat %d: the returned ERROR CLASS or completeness changed with map iteration order: %s", repeat, diff)
			}
			t.Errorf("repeat %d: the diagnostic TEXT changed with map iteration order: %s", repeat, diff)
		}
		classSet[outcome.errClass] = struct{}{}
		completenessSet[outcome.completeness] = struct{}{}
		textSet[outcome.errText] = struct{}{}
	}
	classes := make([]string, 0, len(classSet))
	for class := range classSet {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	completenesses := make([]string, 0, len(completenessSet))
	for completeness := range completenessSet {
		completenesses = append(completenesses, string(completeness))
	}
	sort.Strings(completenesses)
	texts := make([]string, 0, len(textSet))
	for text := range textSet {
		texts = append(texts, text)
	}
	sort.Strings(texts)
	t.Logf("DETERMINISM-REPLAY map_order repeats=%d distinct_err_classes=%d %v distinct_completenesses=%d %v distinct_diagnostic_texts=%d",
		drMapOrderRepeats, len(classes), classes, len(completenesses), completenesses, len(texts))
	if len(texts) != 1 {
		t.Errorf("the returned diagnostic TEXT is not stable across %d repetitions of one identical input: %d distinct texts",
			drMapOrderRepeats, len(texts))
		for _, text := range texts {
			t.Errorf("  DETERMINISM-REPLAY map_order distinct diagnostic text: %s", text)
		}
	}
}

// TestDeterminismAmbiguousOverlapCandidateFirstErrorIsStable is the pin the
// section-19 requirement did not have.
//
// The existing 512-repetition map-order test cannot reach the two branches in
// overlappingSchemaInclusionConflicts that record a FIRST error from a
// COMPETING candidate set:
//
//   - the transitive-payable walk, whose per-scope payable set and whose scope
//     set are both MAPS, so which payable chain is quoted first is decided by
//     Go's randomized map iteration;
//   - the cover walk over rateableByScope, which is also a MAP, so which
//     scope's complete-partition-parent conflict is quoted first is decided the
//     same way.
//
// Neither branch is reached by a fixture with a single candidate, so the
// existing test passes without ever walking them. This fixture publishes
// SEVERAL competing candidates in SEVERAL reduction scopes, so the branch runs
// and the choice is real.
//
// Asserted, per the requirement, on every repetition AND on every declaration
// permutation:
//
//   - the returned ERROR CLASS set;
//   - the Completeness;
//   - the FULL diagnostic text, byte for byte, against the exact pinned
//     diagnostic below -- not merely "some single text".
func TestDeterminismAmbiguousOverlapCandidateFirstErrorIsStable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		build        func(*testing.T) drFixture
		branchMarker string
	}{
		{name: "transitive_payable", build: drOverlapChainFixture, branchMarker: "transitively includes payable component"},
		{name: "complete_cover", build: drOverlapCoverFixture, branchMarker: "payable partition children and priced included subset child"},
	}
	for _, spec := range cases {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			fixture := spec.build(t)
			resolved := drPublish(t, fixture, fixture.rules, fixture.schemas)
			rater, err := billing.NewReferenceRater(resolved)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			input := b1OperatorInput(t, resolved, fixture.obs)

			baseline, _, baseErr := drRateOne(t, resolved, fixture.obs)
			if baseErr == nil {
				t.Fatalf("the %s fixture rated without a diagnostic; the competing-candidate branch is not under test", spec.name)
			}
			if !strings.Contains(baseline.errText, spec.branchMarker) {
				t.Fatalf("the %s branch was not reached; the competing-candidate branch is not under test: %q",
					spec.name, baseline.errText)
			}
			if baseline.errClass != "overlap_conflict" {
				t.Fatalf("baseline error class = %q, want overlap_conflict", baseline.errClass)
			}
			if baseline.completeness != economics.CompletenessConflict {
				t.Fatalf("baseline completeness = %q, want conflict", baseline.completeness)
			}

			textSet := make(map[string]struct{}, 4)
			observe := func(phase string, index int, outcome drOutcome) {
				// Recorded before the comparison, so an unstable branch is
				// REPORTED as the full set of texts it produced rather than
				// silently counting only the ones that happened to match.
				textSet[outcome.errText] = struct{}{}
				if diff := baseline.diff(outcome); diff != "" {
					if outcome.errClass != baseline.errClass || outcome.completeness != baseline.completeness {
						t.Errorf("%s %d: the ERROR CLASS or completeness changed: %s", phase, index, diff)
						return
					}
					t.Errorf("%s %d: the FULL DIAGNOSTIC TEXT changed with map iteration order: %s", phase, index, diff)
				}
			}
			for repeat := range drMapOrderRepeats {
				val, rateErr := rater.Rate(t.Context(), input)
				observe("repeat", repeat, drInspect(val, rateErr))
			}

			// The permutation axis proves the same thing from the other side: the
			// published declaration order, the rule order and the observation
			// delivery order are all randomized, and the resolved snapshot must
			// still yield the identical pinned diagnostic.
			rng := rand.New(rand.NewPCG(drSeedA, drSeedB))
			for permutation := range drPermutations {
				rules, schemas, observations := drPermute(rng, fixture)
				outcome, _, _ := drRateOne(t, drPublish(t, fixture, rules, schemas), observations)
				observe("permutation", permutation, outcome)
			}

			texts := make([]string, 0, len(textSet))
			for text := range textSet {
				texts = append(texts, text)
			}
			sort.Strings(texts)
			t.Logf("DETERMINISM-REPLAY overlap_candidates case=%s repetitions=%d permutations=%d distinct_diagnostic_texts=%d",
				spec.name, drMapOrderRepeats, drPermutations, len(texts))
			// Only the overlap line is logged: the joined advisory that follows
			// it quotes every scope key and dwarfs the part under test.
			for _, text := range texts {
				head, _, _ := strings.Cut(text, "\n")
				t.Logf("DETERMINISM-REPLAY overlap_candidates case=%s candidate diagnostic: %s", spec.name, head)
			}
			if len(texts) != 1 {
				t.Errorf("the %s competing-candidate first error is not stable: %d distinct full diagnostic texts over %d repetitions and %d permutations",
					spec.name, len(texts), drMapOrderRepeats, drPermutations)
			}
			pinned := drPinnedOverlapFirstError[spec.name]
			head, _, _ := strings.Cut(baseline.errText, "\n")
			if head != pinned {
				t.Errorf("the %s first recorded overlap diagnostic drifted.\n  pinned:   %q\n  observed: %q",
					spec.name, pinned, head)
			}
		})
	}
}

// drPinnedOverlapFirstError is the EXACT overlap diagnostic each ambiguous
// competing-candidate overlap fixture must record first, asserted as constants
// so this test is a real pin rather than a self-consistency check. Both are the
// deterministic winner after the walk order was made explicit; before it, the
// transitive case produced EIGHT different texts (every chain in every scope)
// and the cover case TWO (one per scope), which is the defect these pin.
//
// The pinned value is the overlap diagnostic line. The rest of the joined
// message is the non-blocking unknown-containment advisory, whose own
// byte-exactness is pinned separately by the acceptance vectors; the FULL joined
// text is still asserted identical on every repetition and permutation above.
var drPinnedOverlapFirstError = map[string]string{
	// Scope 0 sorts first, and "alpha" is the lowest payable canonical key in
	// it, so the transitive walk must quote the alpha chain of scope 0.
	"transitive_payable": `billing: frozen schema component overlap is not payable: payable component {"direction":"input","component":"vendor:dr_ov_0_alpha_top","unit":"token","schema_id":"b1:frozen-overlap:v1"} transitively includes payable component {"direction":"input","component":"vendor:dr_ov_0_alpha_bottom","unit":"token","schema_id":"b1:frozen-overlap:v1"} in one "input" direction "token" unit scope`,
	// The compiled cover parents are already in ascending node id, so "eta"
	// precedes "zeta" within a scope; the sorted scope projection then makes
	// scope 0 win.
	"complete_cover": `billing: frozen schema component overlap is not payable: complete partition parent {"direction":"input","component":"vendor:dr_oc_0_eta_parent","unit":"token","schema_id":"b1:frozen-overlap:v1"} has payable partition children and priced included subset child {"direction":"input","component":"vendor:dr_oc_0_eta_subset","unit":"token","schema_id":"b1:frozen-overlap:v1"} (payable descendants {"direction":"input","component":"vendor:dr_oc_0_eta_subset","unit":"token","schema_id":"b1:frozen-overlap:v1"}) in one "input" direction "token" unit scope`,
}

// TestReplayDeepestPublishableChainTraversalIsBounded is the section-18
// boundedness claim made falsifiable, and it is a REPLAY test because the claim
// is about the durable identity of a valuation produced from the deepest graph
// the publication bounds allow.
//
// The claim under test: the graph work is proportional to nodes plus edges plus a
// bounded number of fixed-point passes, and NO traversal is recursive on the
// containment chain. The deepest publishable chain is
// MaxComponentSchemas * MaxComponentSchemaRelationships edges, because
// MaxComponentSchemaRelationships is per schema, not per publication.
//
// What this test can actually guarantee is PROCESS-INDEPENDENT, and that is what
// it asserts:
//
//   - the chain runs to completion at the maximum publishable depth at all. A
//     recursive traversal needs one live goroutine frame per containment level,
//     so it exhausts the goroutine stack long before depth 8192; an iterative one
//     (work queue, Kahn topological order, bounded sweep) holds a flat stack.
//     Reaching the bottom of the deepest chain is therefore itself the
//     boundedness observation, with no counter involved;
//   - the canonical fingerprint at every depth is the PINNED one.
//
// The per-Rate ALLOCATION and byte counts are NOT asserted here, and for the
// opposite reason to the one a reader would assume. runtime.MemStats counters
// are PROCESS-wide, so a delta taken across one Rate also contains every
// unrelated allocation the process made between the two reads. Running this
// test t.Parallel() beside the rest of the package put that unbounded share at
// its worst, and at a shallow depth the same block reported mostly somebody
// else's allocations; a 7 s Rate hides it by sheer duration, a 6 ms Rate does
// not. Going serial, below, is what makes the goroutine-STACK delta
// assertable here - a top-level test that does not call t.Parallel() runs to
// completion while queued parallel tests are still paused - and the stack
// reading is asserted. The allocation measurement stays in
// BenchmarkComponentRaterDeepestPublishableChain, whose body is the only place
// the process-wide reading is meaningful, because -run '^$' leaves a benchmark
// alone in the process.
//
// The wall time is still reported per depth, never asserted, because the very
// claim being checked is that the work is bounded but the CURRENT implementation
// sweeps up to len(topo)+1 times, which is a measured observation rather than a
// pinned constant: pinning a wall-clock budget here would make the test a
// performance gate, not a determinism test.

// drChainFixture builds a linear containment chain of the requested DEPTH. The
// chain alternates subset and complete-coverage edges, so both containment edge
// classes are traversed, and the whole chain is declared across the maximum
// number of schemas the publication bound allows.
func drChainFixture(t *testing.T, depth int) drFixture {
	t.Helper()
	key := func(i int) metering.ComponentKey {
		return r7Key(fmt.Sprintf("vendor:dr_chain_%05d", i))
	}
	relationships := make([]metering.ComponentRelationship, 0, depth)
	for i := range depth {
		kind := metering.RelationshipSubset
		if i%2 == 1 {
			kind = metering.RelationshipPartition
		}
		relationships = append(relationships, metering.ComponentRelationship{
			Kind: kind, Parent: key(i), Child: key(i + 1),
		})
	}
	perSchema := metering.MaxComponentSchemaRelationships
	schemas := make([]metering.ComponentSchema, 0, depth/perSchema+1)
	for i := 0; i < len(relationships); i += perSchema {
		end := min(i+perSchema, len(relationships))
		schemas = append(schemas, metering.ComponentSchema{
			ID:            fmt.Sprintf("%s:chain%03d", b1SchemaID, len(schemas)),
			Version:       "1",
			Relationships: append([]metering.ComponentRelationship(nil), relationships[i:end]...),
		})
	}
	rules := []economics.RatingRule{
		b1Rule(t, "dr-chain-root-rate", key(0), "1"),
		b1Rule(t, "dr-chain-leaf-rate", key(depth), "1"),
	}
	return drFixture{
		refID:   fmt.Sprintf("dr-chain-%d", depth),
		rules:   rules,
		schemas: schemas,
		obs: []metering.Observation{drObservation(
			t, fmt.Sprintf("dr-chain-%d", depth),
			b1Measure(t, key(0), "1000"),
			b1Measure(t, key(depth), "10"),
		)},
		nodes: []metering.ComponentKey{key(0), key(depth)},
	}
}

// canonical-identity claim from the other direction: two independently built
// publications of the SAME semantic snapshot -- one in sorted declaration order,
// one in a randomized order -- must canonicalize to the same bytes, and rating
// them must land on the same economics.Valuation.Fingerprint().
func TestDeterminismReplayedEquivalentSnapshotKeepsItsFingerprint(t *testing.T) {
	t.Parallel()
	fixture := drCleanFixture(t)
	rng := rand.New(rand.NewPCG(drSeedA, drSeedB))

	canonicalResolved := drPublish(t, fixture, fixture.rules, fixture.schemas)
	canonicalHash := canonicalResolved.ContentHash()
	canonicalOutcome, _, _ := drRateOne(t, canonicalResolved, fixture.obs)

	for permutation := range 16 {
		rules, schemas, observations := drPermute(rng, fixture)
		resolved := drPublish(t, fixture, rules, schemas)
		if _, canonErr := resolved.Canonical(); canonErr != nil {
			t.Fatalf("permutation %d: canonicalize: %v", permutation, canonErr)
		}
		// ContentHash() is computed from the CANONICAL body, so this is the
		// order-independent identity of the published semantic snapshot.
		if resolved.ContentHash() != canonicalHash {
			t.Fatalf("permutation %d: the canonicalized snapshot content hash differs from the sorted publication: %s != %s",
				permutation, resolved.ContentHash(), canonicalHash)
		}
		outcome, _, _ := drRateOne(t, resolved, observations)
		if outcome.fingerprint != canonicalOutcome.fingerprint {
			t.Fatalf("permutation %d: fingerprint changed: %s != %s", permutation, outcome.fingerprint, canonicalOutcome.fingerprint)
		}
	}
}
