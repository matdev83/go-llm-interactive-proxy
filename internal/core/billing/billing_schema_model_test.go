package billing_test

// Differential model check: production frozen-schema quantity semantics versus
// the independent oracle in internal/testkit/billsem.
//
// This file drives BOTH implementations over a systematically enumerated state
// space and reports every disagreement. It deliberately does NOT modify
// production code and does NOT adjust the oracle. The oracle is a second,
// independently written implementation: if it cannot fail, it is worthless, so
// mismatches are hard test failures carrying a minimal named reproducer.
//
// State space. Node order is fixed (n1 -> n2 -> n3), so every generated graph
// is forward and therefore acyclic by construction; each graph is still run
// through the public metering.ValidateComponentSchemas validator and any graph
// it rejects is skipped and counted. The three edge slots are
//
//	(n1 -> n2), (n1 -> n3), (n2 -> n3)
//
// and each independently takes one of seven shapes: none, subset,
// partition-required, partition-optional, aggregate-required,
// aggregate-optional, transform. That is 7^3 = 343 graphs.
//
// Per-node evidence is assigned independently from five states: absent, exact
// 0, exact 1, exact 2, unavailable. That is 5^3 = 125 assignments.
//
// Two seams are driven, exactly the ones the existing adversarial suite uses
// (review5beSeams): the operator/local E seam and the customer-policy R seam.
//
// Matrices:
//
//   - STRUCTURE sweep: all 343 graphs x all 125 evidence assignments x 2 seams,
//     with a tariff-irrelevant (explicit-free, unit price "0") rule on every
//     node so nothing is ever payable and the structure is isolated from money.
//   - COMMERCIAL sweep: the SAME 343 graphs x the SAME 125 evidence assignments
//     x 2 seams, with positive per-node unit prices, so money and
//     zero-manufacturing behaviour are exercised. The evidence value set is
//     deliberately identical to the structure sweep's: dropping a value here
//     would silently trade coverage of the exact-2 shape away for tractability,
//     and the two matrices together are 343 x 125 x 2 x 2 = 171,500 cases.
//   - ORDER invariance: every graph re-declared with its relationship slice
//     reversed and an empty second schema appended, compared on three
//     representative evidence assignments.
//
// Assertions (see the individual test functions for the precise property):
// A1 oracle-contradiction safety, A2 no zero manufacturing, A3 transform is not
// containment, A4 declaration-order invariance, A5 direction/unit isolation,
// A6 agree-where-both-classify plus the production-stricter population.
//
// A2 clause 2 is TARIFF-AWARE on purpose. The oracle is deliberately
// commercial-free: it reports a complete coverage as Incomplete whenever a
// required member has no representation, whatever that member's tariff says. So
// "oracle Incomplete AND production Complete" on its own is not a production
// defect -- treating it as one conflates STRUCTURAL incompleteness with
// COMMERCIAL load-bearingness, which is a category error, and it would demand
// that production fail closed on the deliberate explicit-free boundary. Each such
// case is therefore classified instead of failed:
//
//   - legitimate: no declared REQUIRED complete member that is missing or
//     unavailable carries an applicable non-free rule. Nothing the cover could
//     hide would have billed, so a complete certification is correct and the
//     divergence from the oracle is the ratified decision. These are COUNTED and
//     REPORTED in the summary line; the volume stays visible rather than being
//     silently dropped, because it is the evidence that the boundary is applied
//     rather than approximated.
//   - illegitimate: at least one such member DOES carry an applicable non-free
//     rule, so money was owed for a member that was never reported and
//     production must not certify complete. These are hard failures.

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/billsem"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const smScope = "sm"

// ---------------------------------------------------------------------------
// Enumeration primitives.
// ---------------------------------------------------------------------------

// smEdgeKind is one of the seven per-slot edge shapes.
type smEdgeKind int

const (
	smNone smEdgeKind = iota
	smSubset
	smPartitionRequired
	smPartitionOptional
	smAggregateRequired
	smAggregateOptional
	smTransform
	smEdgeKindCount
)

func (k smEdgeKind) String() string {
	switch k {
	case smNone:
		return "none"
	case smSubset:
		return "subset"
	case smPartitionRequired:
		return "partition-required"
	case smPartitionOptional:
		return "partition-optional"
	case smAggregateRequired:
		return "aggregate-required"
	case smAggregateOptional:
		return "aggregate-optional"
	case smTransform:
		return "transform"
	default:
		return fmt.Sprintf("edge(%d)", int(k))
	}
}

// smEv is one of the five independent per-node evidence states.
type smEv int

const (
	smAbsent smEv = iota
	smZero
	smOne
	smTwo
	smUnavailable
	smEvCount
)

func (e smEv) String() string {
	switch e {
	case smAbsent:
		return "absent"
	case smZero:
		return "exact(0)"
	case smOne:
		return "exact(1)"
	case smTwo:
		return "exact(2)"
	case smUnavailable:
		return "unavailable"
	default:
		return fmt.Sprintf("ev(%d)", int(e))
	}
}

var smNodeKeys = [3]metering.ComponentKey{
	r7Key("vendor:sm_n1"),
	r7Key("vendor:sm_n2"),
	r7Key("vendor:sm_n3"),
}

var smEdgeSlots = [3][2]int{{0, 1}, {0, 2}, {1, 2}}

// smGraphs enumerates all 7^3 graph shapes.
func smGraphs() [][3]smEdgeKind {
	out := make([][3]smEdgeKind, 0, int(smEdgeKindCount)*int(smEdgeKindCount)*int(smEdgeKindCount))
	for a := range smEdgeKindCount {
		for b := range smEdgeKindCount {
			for c := range smEdgeKindCount {
				out = append(out, [3]smEdgeKind{a, b, c})
			}
		}
	}
	return out
}

// smAllEvidence enumerates all 5^3 evidence assignments. Both the structure and
// the commercial matrix use this exact set: the two sweeps differ only in the
// tariff they declare, and that tariff is precisely what A2 clause 2 measures, so
// reducing one side's evidence space would make the comparison meaningless.
func smAllEvidence() [][3]smEv {
	return smEvidenceProduct([]smEv{smAbsent, smZero, smOne, smTwo, smUnavailable})
}

func smEvidenceProduct(values []smEv) [][3]smEv {
	out := make([][3]smEv, 0, len(values)*len(values)*len(values))
	for _, a := range values {
		for _, b := range values {
			for _, c := range values {
				out = append(out, [3]smEv{a, b, c})
			}
		}
	}
	return out
}

func smRelationship(kind smEdgeKind, parent, child metering.ComponentKey) (metering.ComponentRelationship, bool) {
	switch kind {
	case smNone:
		return metering.ComponentRelationship{}, false
	case smSubset:
		return metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: parent, Child: child}, true
	case smPartitionRequired:
		return metering.ComponentRelationship{Kind: metering.RelationshipPartition, Parent: parent, Child: child}, true
	case smPartitionOptional:
		return metering.ComponentRelationship{Kind: metering.RelationshipPartition, Parent: parent, Child: child, Optional: true}, true
	case smAggregateRequired:
		return metering.ComponentRelationship{Kind: metering.RelationshipAggregate, Parent: parent, Child: child}, true
	case smAggregateOptional:
		return metering.ComponentRelationship{Kind: metering.RelationshipAggregate, Parent: parent, Child: child, Optional: true}, true
	case smTransform:
		return metering.ComponentRelationship{Kind: metering.RelationshipTransform, Parent: parent, Child: child}, true
	default:
		return metering.ComponentRelationship{}, false
	}
}

func smRelationships(opts [3]smEdgeKind) []metering.ComponentRelationship {
	rels := make([]metering.ComponentRelationship, 0, 3)
	for i, opt := range opts {
		if rel, ok := smRelationship(opt, smNodeKeys[smEdgeSlots[i][0]], smNodeKeys[smEdgeSlots[i][1]]); ok {
			rels = append(rels, rel)
		}
	}
	return rels
}

func smSchemas(rels []metering.ComponentRelationship) []metering.ComponentSchema {
	return []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: rels}}
}

func smOptsName(opts [3]smEdgeKind) string {
	return opts[0].String() + "|" + opts[1].String() + "|" + opts[2].String()
}

func smEvName(ev [3]smEv) string {
	return ev[0].String() + "|" + ev[1].String() + "|" + ev[2].String()
}

func smOptsCode(opts [3]smEdgeKind) string {
	return fmt.Sprintf("%d%d%d", opts[0], opts[1], opts[2])
}

func smEvCode(ev [3]smEv) string {
	return fmt.Sprintf("%d%d%d", ev[0], ev[1], ev[2])
}

// ---------------------------------------------------------------------------
// Evidence / observation construction.
// ---------------------------------------------------------------------------

func smFreeRules(t *testing.T) []economics.RatingRule {
	t.Helper()
	return []economics.RatingRule{
		b1Rule(t, "sm-free-n1", smNodeKeys[0], "0"),
		b1Rule(t, "sm-free-n2", smNodeKeys[1], "0"),
		b1Rule(t, "sm-free-n3", smNodeKeys[2], "0"),
	}
}

func smCommercialRules(t *testing.T) []economics.RatingRule {
	t.Helper()
	return []economics.RatingRule{
		b1Rule(t, "sm-paid-n1", smNodeKeys[0], "3"),
		b1Rule(t, "sm-paid-n2", smNodeKeys[1], "5"),
		b1Rule(t, "sm-paid-n3", smNodeKeys[2], "7"),
	}
}

// smMeasureFor maps one evidence state to a measure. Absent produces no measure.
func smMeasureFor(t *testing.T, key metering.ComponentKey, e smEv) (metering.Measure, bool) {
	t.Helper()
	switch e {
	case smAbsent:
		return metering.Measure{}, false
	case smZero:
		return b1Measure(t, key, "0"), true
	case smOne:
		return b1Measure(t, key, "1"), true
	case smTwo:
		return b1Measure(t, key, "2"), true
	case smUnavailable:
		return b1UnavailableMeasure(key), true
	default:
		return metering.Measure{}, false
	}
}

func smMustMeasure(t *testing.T, key metering.ComponentKey, e smEv) metering.Measure {
	t.Helper()
	measure, ok := smMeasureFor(t, key, e)
	if !ok {
		t.Fatalf("smMustMeasure: node %s is absent", key.Component)
	}
	return measure
}

func smMeasures(t *testing.T, ev [3]smEv) []metering.Measure {
	t.Helper()
	measures := make([]metering.Measure, 0, 3)
	for i, e := range ev {
		if measure, ok := smMeasureFor(t, smNodeKeys[i], e); ok {
			measures = append(measures, measure)
		}
	}
	return measures
}

type smEvidenceEntry struct {
	key metering.ComponentKey
	ev  smEv
}

func smEvidenceEntries(scope string, entries ...smEvidenceEntry) billsem.Evidence {
	evidence := billsem.NewEvidence()
	states := make(map[string]billsem.ObsState, len(entries))
	values := make(map[string]*big.Rat, len(entries))
	for _, entry := range entries {
		key := entry.key.CanonicalKey()
		switch entry.ev {
		case smAbsent:
			states[key] = billsem.ObsAbsent
		case smZero:
			states[key] = billsem.ObsExact
			values[key] = big.NewRat(0, 1)
		case smOne:
			states[key] = billsem.ObsExact
			values[key] = big.NewRat(1, 1)
		case smTwo:
			states[key] = billsem.ObsExact
			values[key] = big.NewRat(2, 1)
		case smUnavailable:
			states[key] = billsem.ObsUnavailable
		}
	}
	evidence.States[scope] = states
	evidence.Values[scope] = values
	return evidence
}

func smEvidenceForNodes(ev [3]smEv) billsem.Evidence {
	entries := make([]smEvidenceEntry, 0, 3)
	for i, e := range ev {
		entries = append(entries, smEvidenceEntry{key: smNodeKeys[i], ev: e})
	}
	return smEvidenceEntries(smScope, entries...)
}

// ---------------------------------------------------------------------------
// Oracle and production outcome extraction.
// ---------------------------------------------------------------------------

func smSolveOracle(t *testing.T, schemas []metering.ComponentSchema, evidence billsem.Evidence) billsem.ScopeResult {
	t.Helper()
	results, err := billsem.Solve(schemas, evidence)
	if err != nil {
		t.Fatalf("billsem.Solve: %v", err)
	}
	for _, result := range results {
		if result.Scope == smScope {
			return result
		}
	}
	t.Fatalf("billsem.Solve produced no scope %q; results=%+v", smScope, results)
	return billsem.ScopeResult{}
}

func smOracleNodes(t *testing.T, rels []metering.ComponentRelationship, ev [3]smEv) billsem.ScopeResult {
	t.Helper()
	return smSolveOracle(t, smSchemas(rels), smEvidenceForNodes(ev))
}

func smErrorClass(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, billing.ErrSchemaPartitionContradiction):
		return "partition_contradiction"
	case errors.Is(err, billing.ErrSchemaPartitionIncomplete):
		return "partition_incomplete"
	case errors.Is(err, billing.ErrSchemaPartitionIncomparable):
		return "partition_incomparable"
	case errors.Is(err, billing.ErrSchemaSubsetContradiction):
		return "subset_contradiction"
	case errors.Is(err, billing.ErrSchemaQuantityContradiction):
		return "quantity_contradiction"
	case errors.Is(err, billing.ErrSchemaOverlapConflict):
		return "overlap_conflict"
	default:
		return "other"
	}
}

func smIsQuantityContradiction(class string) bool {
	switch class {
	case "partition_contradiction", "subset_contradiction", "quantity_contradiction":
		return true
	default:
		return false
	}
}

func smLinePositive(line *economics.LineItem) bool {
	if line.Amount != nil {
		if rat, err := line.Amount.ToRat(); err == nil && rat.Sign() > 0 {
			return true
		}
	}
	if line.AmountNumerator != "" {
		numerator, okN := new(big.Int).SetString(line.AmountNumerator, 10)
		denominator, okD := new(big.Int).SetString(line.AmountDenominator, 10)
		if okN && okD && denominator.Sign() > 0 && numerator.Sign() > 0 {
			return true
		}
	}
	return false
}

type smOutcome struct {
	errClass     string
	completeness economics.Completeness
	components   []string
	total        string
	positive     map[string]bool
}

func smInspect(val economics.Valuation) smOutcome {
	out := smOutcome{completeness: val.Completeness, total: "<none>", positive: map[string]bool{}}
	if len(val.Totals) > 0 && val.Totals[0].Amount != nil {
		out.total = val.Totals[0].Amount.CanonicalString()
	}
	components := make([]string, 0, len(val.Lines))
	for i := range val.Lines {
		line := &val.Lines[i]
		identity := "<fixed>"
		if line.Component != nil {
			identity = line.Component.CanonicalKey()
		}
		components = append(components, identity)
		if smLinePositive(line) {
			out.positive[identity] = true
		}
	}
	sort.Strings(components)
	out.components = components
	return out
}

// ---------------------------------------------------------------------------
// Tally / report.
// ---------------------------------------------------------------------------

const smMaxExamplesPerAssertion = 20

type smReport struct {
	mu sync.Mutex

	graphCount    int
	skipped       int
	evidenceCount int
	seams         int
	cases         int

	oracleContradicted int
	oracleIncomplete   int
	agreeIncomplete    int
	prodStricter       int
	prodStricterBeyond int

	guardedA1 int
	guardedA2 int
	guardedA4 int

	// Diagnostic splits for the A1/A2 findings, so the report distinguishes a
	// systematic oracle interpretation from an incidental one.
	a1WithOptionalEdge int
	a1WithoutOptional  int
	a2PositiveLine     int
	a2CoverTotal       int
	a2CoverObserved    int
	a2CoverUnobserved  int
	// a2Legitimate and a2Illegitimate are the TARIFF-AWARE split of the
	// oracle-Incomplete-and-production-Complete population. The legitimate count
	// is reported, never failed: it is the retained volume of cases where the
	// missing cover member was explicitly free or unpriced, so no money could be
	// hiding and a complete certification is the ratified outcome. The
	// illegitimate count is a hard failure: a required member that was never
	// reported carried a non-free rule, so money was owed. Their sum is
	// a2CoverTotal, which is also the whole reverse-disagreement population.
	a2Legitimate   int
	a2Illegitimate int
	// oracleIncompleteProductionComplete counts the reverse disagreement:
	// the oracle calls a node Incomplete while production certifies Complete.
	oracleIncompleteProductionComplete int

	// stricterBeyondClasses breaks the production-stricter-beyond-oracle
	// population down by the production diagnostic that caused it, so an
	// assessment of over-rejection is evidence-based rather than assumed.
	stricterBeyondClasses map[string]int

	counts   map[string]int
	examples map[string][]string
}

func newSMReport() *smReport {
	return &smReport{
		counts:                map[string]int{},
		examples:              map[string][]string{},
		stricterBeyondClasses: map[string]int{},
	}
}

func (r *smReport) noteStricterBeyond(class string) {
	r.mu.Lock()
	r.stricterBeyondClasses[class]++
	r.mu.Unlock()
}

func (r *smReport) add(kind, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[kind]++
	if len(r.examples[kind]) < smMaxExamplesPerAssertion {
		r.examples[kind] = append(r.examples[kind], message)
	}
}

func (r *smReport) observe(oracleContradicted, oracleIncomplete bool, prodComplete bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cases++
	if oracleContradicted {
		r.oracleContradicted++
		r.guardedA1++
	}
	if oracleIncomplete {
		r.oracleIncomplete++
	}
	if oracleIncomplete && !prodComplete {
		r.agreeIncomplete++
	}
	if oracleIncomplete && prodComplete {
		r.oracleIncompleteProductionComplete++
	}
	if !prodComplete && !oracleContradicted {
		r.prodStricter++
	}
	if !prodComplete && !oracleContradicted && !oracleIncomplete {
		r.prodStricterBeyond++
	}
}

func (r *smReport) guardA2() {
	r.mu.Lock()
	r.guardedA2++
	r.mu.Unlock()
}

func (r *smReport) guardA4() {
	r.mu.Lock()
	r.guardedA4++
	r.mu.Unlock()
}

func (r *smReport) finish(t *testing.T, label string, d time.Duration) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Logf("SCHEMA-MODEL[%s] graphs=%d validator_skipped=%d evidence_per_graph=%d seams=%d TOTAL_CASES_EXECUTED=%d wall=%s",
		label, r.graphCount, r.skipped, r.evidenceCount, r.seams, r.cases, d.Round(time.Millisecond))
	t.Logf("SCHEMA-MODEL[%s] oracle_contradicted_cases=%d oracle_incomplete_cases=%d agree_incomplete=%d production_stricter=%d production_stricter_beyond_oracle=%d",
		label, r.oracleContradicted, r.oracleIncomplete, r.agreeIncomplete, r.prodStricter, r.prodStricterBeyond)
	t.Logf("SCHEMA-MODEL[%s] A1_checks=%d A2_checks=%d A4_checks=%d A1_failures_with_optional_edge=%d A1_failures_without_optional=%d A2_positive_line_failures=%d A2_illegitimate=%d A2_legitimate=%d oracle_incomplete_production_complete_population=%d observed=%d unobserved=%d",
		label, r.guardedA1, r.guardedA2, r.guardedA4, r.a1WithOptionalEdge, r.a1WithoutOptional, r.a2PositiveLine, r.a2Illegitimate, r.a2Legitimate, r.a2CoverTotal, r.a2CoverObserved, r.a2CoverUnobserved)
	t.Logf("SCHEMA-MODEL[%s] PRODUCTION_LESS_STRICT oracle_incomplete_production_complete=%d observed_parent_required_cover=%d unobserved_or_other=%d",
		label, r.oracleIncompleteProductionComplete, r.a2CoverObserved, r.a2CoverUnobserved)
	for _, class := range billsem.SortedKeys(r.stricterBeyondClasses) {
		t.Logf("SCHEMA-MODEL[%s] PRODUCTION_STRICTER_BEYOND_ORACLE class=%s count=%d", label, class, r.stricterBeyondClasses[class])
	}
	total := 0
	for _, kind := range smAssertionKinds {
		count := r.counts[kind]
		total += count
		t.Logf("SCHEMA-MODEL[%s] %s failures=%d", label, kind, count)
	}
	if total == 0 {
		t.Logf("SCHEMA-MODEL[%s] no mismatches found", label)
		return
	}
	for _, kind := range smAssertionKinds {
		for _, example := range r.examples[kind] {
			t.Errorf("SCHEMA-MODEL[%s] %s MISMATCH: %s", label, kind, example)
		}
	}
}

var smAssertionKinds = []string{"A1", "A2", "A3", "A4", "A5"}

// ---------------------------------------------------------------------------
// Core per-case differential evaluation.
// ---------------------------------------------------------------------------

func smCaseID(label, seam string, opts [3]smEdgeKind, ev [3]smEv) string {
	return fmt.Sprintf("%s seam=%s graph[%s] evidence[%s]", label, seam, smOptsName(opts), smEvName(ev))
}

// smEvaluateCase drives one (graph, evidence, seam) case through both
// implementations and applies A1, A2 and the A6 comparison counters.
//
// rulesAreNonFree states whether the tariff this matrix declared is non-free: the
// structure matrix prices every node "0" (explicit free) and the commercial matrix
// prices every node positively, and the two differ in exactly that flag. It is the
// input to the TARIFF-AWARE A2 clause 2 split, and it is passed explicitly rather
// than inferred from the label so the classification cannot silently follow a
// renamed sweep.
func smEvaluateCase(
	t *testing.T,
	report *smReport,
	label string,
	seam review5beSeam,
	resolved economics.TariffSnapshot,
	rels []metering.ComponentRelationship,
	opts [3]smEdgeKind,
	ev [3]smEv,
	rulesAreNonFree bool,
) {
	t.Helper()
	caseID := smCaseID(label, seam.name, opts, ev)

	oracle := smOracleNodes(t, rels, ev)
	oracleContradicted := len(oracle.Contradicted) > 0
	oracleIncomplete := len(oracle.Incomplete) > 0

	obsID := "sm-" + label + "-" + seam.name + "-" + smOptsCode(opts) + "-" + smEvCode(ev)
	obs := f3Observation(t, obsID, smMeasures(t, ev)...)
	val, err := seam.rate(t, resolved, obs)
	out := smInspect(val)
	out.errClass = smErrorClass(err)
	prodComplete := out.completeness == economics.CompletenessComplete

	report.observe(oracleContradicted, oracleIncomplete, prodComplete)
	if !prodComplete && !oracleContradicted && !oracleIncomplete {
		report.noteStricterBeyond(out.errClass)
	}

	// A1: oracle quantity contradiction must forbid a Complete production result.
	if oracleContradicted && prodComplete {
		report.mu.Lock()
		if smAnyOptionalEdge(opts) {
			report.a1WithOptionalEdge++
		} else {
			report.a1WithoutOptional++
		}
		report.mu.Unlock()
		report.add("A1", fmt.Sprintf("%s oracle_contradicted=[%s] production=Complete err=%s",
			caseID, strings.Join(oracle.Contradicted, ","), out.errClass))
	}

	// A2 clause 1: an Absent/Unavailable node must never emit a positive line.
	unknownEvidence := false
	for i, e := range ev {
		if e != smAbsent && e != smUnavailable {
			continue
		}
		unknownEvidence = true
		key := smNodeKeys[i].CanonicalKey()
		if out.positive[key] {
			report.mu.Lock()
			report.a2PositiveLine++
			report.mu.Unlock()
			report.add("A2", fmt.Sprintf("%s node=%s evidence=%s emitted_positive_line total=%s err=%s components=%v",
				caseID, smNodeKeys[i].Component, e, out.total, out.errClass, out.components))
		}
	}
	if unknownEvidence {
		report.guardA2()
	}
	// A2 clause 2, TARIFF-AWARE. The oracle is deliberately commercial-free, so
	// "oracle Incomplete AND production Complete" conflates STRUCTURAL
	// incompleteness with COMMERCIAL load-bearingness, which is a category error:
	// the cover can be structurally incomplete while nothing money-bearing is
	// hidden behind it. Classify instead of fail.
	observedCover := smObservedParentWithUnknownRequiredMember(opts, ev)
	if oracleIncomplete && prodComplete {
		loadBearing := smMissingRequiredCoverMembers(opts, ev)
		illegitimate := rulesAreNonFree && len(loadBearing) != 0
		report.mu.Lock()
		report.a2CoverTotal++
		if observedCover {
			report.a2CoverObserved++
		} else {
			report.a2CoverUnobserved++
		}
		if illegitimate {
			report.a2Illegitimate++
		} else {
			report.a2Legitimate++
		}
		report.mu.Unlock()
		if illegitimate {
			report.add("A2", fmt.Sprintf("%s production=Complete although the required complete member(s) %s are absent/unavailable and carry a non-free rule; total=%s components=%v oracle_incomplete=%v observed_parent=%v",
				caseID, strings.Join(smNodeNames(loadBearing), ","), out.total, out.components, oracle.Incomplete, observedCover))
		}
	}
}

func smAnyOptionalEdge(opts [3]smEdgeKind) bool {
	for _, opt := range opts {
		if opt == smPartitionOptional || opt == smAggregateOptional {
			return true
		}
	}
	return false
}

// smMissingRequiredCoverMembers returns the node indices of every declared
// REQUIRED complete-coverage member whose quantity is unknown in this evidence
// assignment: absent (no measure at all) or unavailable (a measure carrying no
// comparable value). Either one makes the cover equation unknowable.
//
// An absent OPTIONAL member is deliberately excluded: the frozen schema declares
// its own edge-local zero, so it is a proven share rather than missing money and
// must never be what a cover is said to be hiding.
func smMissingRequiredCoverMembers(opts [3]smEdgeKind, ev [3]smEv) []int {
	members := make([]int, 0, 3)
	for i, opt := range opts {
		if opt != smPartitionRequired && opt != smAggregateRequired {
			continue
		}
		child := smEdgeSlots[i][1]
		if ev[child] == smAbsent || ev[child] == smUnavailable {
			members = append(members, child)
		}
	}
	return members
}

// smNodeNames renders node indices as their component names so a diagnostic names
// the members that decide a classification.
func smNodeNames(indices []int) []string {
	names := make([]string, 0, len(indices))
	for _, index := range indices {
		names = append(names, smNodeKeys[index].Component)
	}
	return names
}

// smObservedParentWithUnknownRequiredMember reports whether an OBSERVED node is
// the parent of a required complete-coverage edge whose child is absent or
// unavailable. Only that shape is a production-diagnosable cover equation.
func smObservedParentWithUnknownRequiredMember(opts [3]smEdgeKind, ev [3]smEv) bool {
	for i, opt := range opts {
		if opt != smPartitionRequired && opt != smAggregateRequired {
			continue
		}
		parent := smEdgeSlots[i][0]
		child := smEdgeSlots[i][1]
		if ev[parent] == smAbsent || ev[parent] == smUnavailable {
			continue
		}
		if ev[child] == smAbsent || ev[child] == smUnavailable {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// A1/A2/A6 structure sweep (tariff irrelevant) and A1/A2/A6 commercial sweep.
// ---------------------------------------------------------------------------

func TestSchemaModelStructureSweep(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := newSMReport()
	graphs := smGraphs()
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			report.skipped++
			t.Logf("validator skipped graph [%s]: %v", smOptsName(opts), err)
			continue
		}
		valid = append(valid, opts)
	}
	evidence := smAllEvidence()
	seams := review5beSeams()
	report.graphCount = len(graphs)
	report.evidenceCount = len(evidence)
	report.seams = len(seams)

	t.Cleanup(func() { report.finish(t, "structure", time.Since(start)) })

	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			rels := smRelationships(opts)
			resolved := f356Schema(t, fmt.Sprintf("sm-structure-%d", gi), smFreeRules(t), rels)
			for _, ev := range evidence {
				for _, seam := range seams {
					smEvaluateCase(t, report, "structure", seam, resolved, rels, opts, ev, false)
				}
			}
		})
	}
}

func TestSchemaModelCommercialSweep(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := newSMReport()
	graphs := smGraphs()
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			report.skipped++
			continue
		}
		valid = append(valid, opts)
	}
	evidence := smAllEvidence()
	seams := review5beSeams()
	report.graphCount = len(graphs)
	report.evidenceCount = len(evidence)
	report.seams = len(seams)

	t.Cleanup(func() { report.finish(t, "commercial", time.Since(start)) })

	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			rels := smRelationships(opts)
			resolved := f356Schema(t, fmt.Sprintf("sm-commercial-%d", gi), smCommercialRules(t), rels)
			for _, ev := range evidence {
				for _, seam := range seams {
					smEvaluateCase(t, report, "commercial", seam, resolved, rels, opts, ev, true)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A4: declaration-order invariance.
// ---------------------------------------------------------------------------

func smReverse(rels []metering.ComponentRelationship) []metering.ComponentRelationship {
	out := make([]metering.ComponentRelationship, len(rels))
	for i := range rels {
		out[i] = rels[len(rels)-1-i]
	}
	return out
}

func TestSchemaModelOrderInvariance(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := newSMReport()
	graphs := smGraphs()
	evidenceSets := [][3]smEv{
		{smOne, smOne, smOne},
		{smTwo, smOne, smZero},
		{smAbsent, smOne, smUnavailable},
	}
	seams := review5beSeams()
	report.graphCount = len(graphs)
	report.evidenceCount = len(evidenceSets)
	report.seams = len(seams)

	t.Cleanup(func() { report.finish(t, "order", time.Since(start)) })

	// Skip publication-rejected graphs, exactly as the structure and commercial
	// sweeps already do. Both variants of an order-invariance pair are refused
	// identically, so the pair carries no signal; the skip is counted rather than
	// silently dropped. Filtering happens here, before the parallel subtests
	// start, so the shared skipped counter is never written concurrently.
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			report.skipped++
			t.Logf("validator skipped graph [%s]: %v", smOptsName(opts), err)
			continue
		}
		valid = append(valid, opts)
	}

	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			rels := smRelationships(opts)
			rules := smFreeRules(t)
			resolvedA := f356Schema(t, fmt.Sprintf("sm-order-a-%d", gi), rules, rels)
			resolvedB := f3Resolved(t, fmt.Sprintf("sm-order-b-%d", gi), rules, []metering.ComponentSchema{
				{ID: b1SchemaID, Version: "1", Relationships: smReverse(rels)},
				{ID: b1SchemaID + "_empty", Version: "1"},
			})
			for _, ev := range evidenceSets {
				for _, seam := range seams {
					caseID := smCaseID("order", seam.name, opts, ev)
					obsID := "sm-order-" + seam.name + "-" + smOptsCode(opts) + "-" + smEvCode(ev)
					obs := f3Observation(t, obsID, smMeasures(t, ev)...)
					valA, errA := seam.rate(t, resolvedA, obs)
					valB, errB := seam.rate(t, resolvedB, obs)
					outA, outB := smInspect(valA), smInspect(valB)
					outA.errClass, outB.errClass = smErrorClass(errA), smErrorClass(errB)
					report.guardA4()
					report.mu.Lock()
					report.cases++
					report.mu.Unlock()
					if outA.errClass != outB.errClass ||
						outA.completeness != outB.completeness ||
						!slices.Equal(outA.components, outB.components) ||
						outA.total != outB.total {
						report.add("A4", fmt.Sprintf("%s forward{err=%s compl=%s total=%s components=%v} reversed+empty{err=%s compl=%s total=%s components=%v}",
							caseID, outA.errClass, outA.completeness, outA.total, outA.components,
							outB.errClass, outB.completeness, outB.total, outB.components))
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A3: a transform edge is never containment.
// ---------------------------------------------------------------------------

func TestSchemaModelTransformIsNotContainment(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := newSMReport()
	seams := review5beSeams()
	report.graphCount = 3
	report.evidenceCount = 1
	report.seams = len(seams)
	t.Cleanup(func() { report.finish(t, "transform", time.Since(start)) })

	parent := r7Key("vendor:sm_tf_parent")
	child := r7Key("vendor:sm_tf_child")
	rels := []metering.ComponentRelationship{{Kind: metering.RelationshipTransform, Parent: parent, Child: child}}

	cases := []struct {
		name          string
		parent, child smEv
	}{
		{"child_greater_than_parent", smOne, smTwo},
		{"child_equal_to_parent", smOne, smOne},
		{"child_less_than_parent", smTwo, smOne},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			report.mu.Lock()
			report.cases++
			report.mu.Unlock()
			rules := []economics.RatingRule{
				b1Rule(t, "sm-tf-parent-"+tc.name, parent, "0"),
				b1Rule(t, "sm-tf-child-"+tc.name, child, "0"),
			}
			resolved := f356Schema(t, "sm-tf-"+tc.name, rules, rels)
			obs := f3Observation(t, "sm-tf-"+tc.name,
				smMustMeasure(t, parent, tc.parent), smMustMeasure(t, child, tc.child))
			oracle := smSolveOracle(t, smSchemas(rels), smEvidenceEntries(smScope,
				smEvidenceEntry{key: parent, ev: tc.parent},
				smEvidenceEntry{key: child, ev: tc.child}))
			if len(oracle.Contradicted) > 0 {
				report.add("A3", fmt.Sprintf("transform case=%s oracle contradicted=%v", tc.name, oracle.Contradicted))
			}
			for _, seam := range seams {
				val, err := seam.rate(t, resolved, obs)
				class := smErrorClass(err)
				if smIsQuantityContradiction(class) {
					report.add("A3", fmt.Sprintf("transform case=%s seam=%s production reported %s (must not treat transform as containment) completeness=%s components=%v",
						tc.name, seam.name, class, val.Completeness, smInspect(val).components))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A5: edges differing only in direction or unit are ignored by both.
// ---------------------------------------------------------------------------

func TestSchemaModelDirectionUnitIsolation(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := newSMReport()
	seams := review5beSeams()
	report.graphCount = 2
	report.evidenceCount = 1
	report.seams = len(seams)
	t.Cleanup(func() { report.finish(t, "isolation", time.Since(start)) })

	// Different direction, same unit: an input parent and an output child. If
	// the subset edge were honoured the 1 < 2 child would contradict its parent.
	directionParent := b1Key(metering.DirectionInput, "vendor:sm_dir_parent", metering.UnitToken)
	directionChild := b1Key(metering.DirectionOutput, "vendor:sm_dir_child", metering.UnitToken)
	directionRels := []metering.ComponentRelationship{{
		Kind: metering.RelationshipSubset, Parent: directionParent, Child: directionChild,
	}}

	// Same direction, different unit, carried by a transform edge: a transform
	// is never containment even when the units differ.
	unitParent := b1Key(metering.DirectionInput, "vendor:sm_unit_parent", metering.UnitToken)
	unitChild := b1Key(metering.DirectionInput, "vendor:sm_unit_child", metering.UnitImage)
	unitRels := []metering.ComponentRelationship{{
		Kind: metering.RelationshipTransform, Parent: unitParent, Child: unitChild,
	}}

	type isolationCase struct {
		name   string
		rels   []metering.ComponentRelationship
		parent metering.ComponentKey
		child  metering.ComponentKey
	}
	cases := []isolationCase{
		{"cross_direction_subset", directionRels, directionParent, directionChild},
		{"cross_unit_transform", unitRels, unitParent, unitChild},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			report.mu.Lock()
			report.cases++
			report.mu.Unlock()
			if err := metering.ValidateComponentSchemas(smSchemas(tc.rels)); err != nil {
				t.Fatalf("isolation graph %s should be valid: %v", tc.name, err)
			}
			rules := []economics.RatingRule{
				b1Rule(t, "sm-iso-parent-"+tc.name, tc.parent, "0"),
				b1Rule(t, "sm-iso-child-"+tc.name, tc.child, "0"),
			}
			resolved := f356Schema(t, "sm-iso-"+tc.name, rules, tc.rels)
			obs := f3Observation(t, "sm-iso-"+tc.name,
				b1Measure(t, tc.parent, "1"), b1Measure(t, tc.child, "2"))
			oracle := smSolveOracle(t, smSchemas(tc.rels), smEvidenceEntries(smScope,
				smEvidenceEntry{key: tc.parent, ev: smOne},
				smEvidenceEntry{key: tc.child, ev: smTwo}))
			if len(oracle.Contradicted) > 0 {
				report.add("A5", fmt.Sprintf("isolation case=%s oracle contradicted=%v (edge must be ignored)", tc.name, oracle.Contradicted))
			}
			for _, seam := range seams {
				val, err := seam.rate(t, resolved, obs)
				class := smErrorClass(err)
				if smIsQuantityContradiction(class) {
					report.add("A5", fmt.Sprintf("isolation case=%s seam=%s production reported %s (direction/unit-mismatched edge must be ignored) completeness=%s",
						tc.name, seam.name, class, val.Completeness))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Named regression reproducers for the mismatches the sweep found.
// ---------------------------------------------------------------------------

// TestSchemaModelRegressionOptionalCompleteMemberIsNotZero pins the minimal
// A1 counterexample found by the structure/commercial sweeps.
//
// Invariant: an absent OPTIONAL member of a declared complete coverage is the
// schema's own edge-local zero, so the parent is a proven zero; a subset
// descendant with a positive quantity then contradicts its ancestor and
// production must not certify Complete.
//
// Exact minimal graph (no transform, no other edges):
//
//	n1 --partition(optional)--> n2
//	n1 --subset--> n3
//
// Evidence: n1 absent, n2 absent, n3 exact 1 (n3 priced "1", n1/n2 explicit-free).
// The oracle reports a quantity contradiction on n1 and n3. Production returns
// Completeness=complete with total 1, i.e. it certifies positive payable money
// from contradictory containment evidence. Production declines to bound the
// parent when no member of its complete coverage is present, so the absent
// optional zero never propagates to n1 and the n3 subset bound is never checked.
func TestSchemaModelRegressionOptionalCompleteMemberIsNotZero(t *testing.T) {
	t.Parallel()
	n1 := r7Key("vendor:sm_reg_opt_n1")
	n2 := r7Key("vendor:sm_reg_opt_n2")
	n3 := r7Key("vendor:sm_reg_opt_n3")
	rels := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: n1, Child: n2, Optional: true},
		{Kind: metering.RelationshipSubset, Parent: n1, Child: n3},
	}
	rules := []economics.RatingRule{
		b1Rule(t, "sm-reg-opt-n1", n1, "0"),
		b1Rule(t, "sm-reg-opt-n2", n2, "0"),
		b1Rule(t, "sm-reg-opt-n3", n3, "1"),
	}
	resolved := f356Schema(t, "sm-reg-optional-cover", rules, rels)
	obs := f3Observation(t, "sm-reg-optional-cover",
		b1Measure(t, n3, "1"))
	oracle := smSolveOracle(t, smSchemas(rels), smEvidenceEntries(smScope,
		smEvidenceEntry{key: n1, ev: smAbsent},
		smEvidenceEntry{key: n2, ev: smAbsent},
		smEvidenceEntry{key: n3, ev: smOne}))
	if len(oracle.Contradicted) == 0 {
		t.Fatalf("oracle no longer reports the contradiction this regression pins; got %+v", oracle)
	}
	for _, seam := range review5beSeams() {
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if val.Completeness == economics.CompletenessComplete {
				t.Errorf("%s: production certified Complete total=%s from contradictory optional-cover evidence; oracle_contradicted=%v err=%v components=%v",
					seam.name, smInspect(val).total, oracle.Contradicted, err, smInspect(val).components)
			}
		})
	}
}

// TestSchemaModelRegressionAbsentSubsetChildDoesNotConstrainCompleteParent pins
// the second distinct A1 root cause the commercial sweep found, which has no
// optional edge at all.
//
// Invariant: a subset descendant's upper bound must propagate to an absent or
// unavailable subset child, which then constrains any complete parent that
// declares that child as a member. Otherwise a complete parent can be certified
// even though one of its member quantities is bounded above by something
// smaller than the parent's own quantity.
//
// Exact minimal graph:
//
//	n1 --subset--> n3
//	n2 --partition(required)--> n3
//
// Evidence: n1 exact 0, n2 exact 1, n3 absent. Since n3 <= n1 = 0 and n2 is
// complete over n3, n2 must be 0, contradicting n2 = 1. The oracle reports the
// contradiction on n2; production returns Completeness=complete with total 5
// (n2 priced "5"), because its subset upper-bound rule is gated on the child
// already having an exact representation and therefore never reaches the absent
// n3.
func TestSchemaModelRegressionAbsentSubsetChildDoesNotConstrainCompleteParent(t *testing.T) {
	t.Parallel()
	n1 := r7Key("vendor:sm_reg_bnd_n1")
	n2 := r7Key("vendor:sm_reg_bnd_n2")
	n3 := r7Key("vendor:sm_reg_bnd_n3")
	rels := []metering.ComponentRelationship{
		{Kind: metering.RelationshipSubset, Parent: n1, Child: n3},
		{Kind: metering.RelationshipPartition, Parent: n2, Child: n3},
	}
	rules := []economics.RatingRule{
		b1Rule(t, "sm-reg-bnd-n1", n1, "0"),
		b1Rule(t, "sm-reg-bnd-n2", n2, "5"),
		b1Rule(t, "sm-reg-bnd-n3", n3, "0"),
	}
	resolved := f356Schema(t, "sm-reg-absent-subset-bound", rules, rels)
	obs := f3Observation(t, "sm-reg-absent-subset-bound",
		b1Measure(t, n1, "0"), b1Measure(t, n2, "1"))
	oracle := smSolveOracle(t, smSchemas(rels), smEvidenceEntries(smScope,
		smEvidenceEntry{key: n1, ev: smZero},
		smEvidenceEntry{key: n2, ev: smOne},
		smEvidenceEntry{key: n3, ev: smAbsent}))
	if len(oracle.Contradicted) == 0 {
		t.Fatalf("oracle no longer reports the contradiction this regression pins; got %+v", oracle)
	}
	for _, seam := range review5beSeams() {
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if val.Completeness == economics.CompletenessComplete {
				t.Errorf("%s: production certified Complete total=%s although an absent subset child bounds a complete parent; oracle_contradicted=%v err=%v components=%v",
					seam.name, smInspect(val).total, oracle.Contradicted, err, smInspect(val).components)
			}
		})
	}
}

// TestSchemaModelRegressionUnobservedRequiredCoverIsNotComplete pins the
// minimal A2 counterexample on the ratified side of the commercial boundary.
//
// RULE PINNED: a REQUIRED member of a declared complete coverage that was never
// reported at all is MISSING evidence, and it is missing COMMERCIALLY when its own
// frozen rule could have billed money for some non-negative quantity. Only then
// may the enclosing valuation not be certified complete. This is deliberately
// narrower than "the cover is incomplete": an explicit-free member and a member
// with no rule at all are pinned complete by
// TestSchemaModelRegressionExplicitFreeMissingMemberIsNotADependency, and a
// member another scope settled is not missing money at all.
//
// Exact minimal graph:
//
//	p1 --aggregate(required)--> p2
//	p1 --subset--> p3
//
// Evidence: p1 absent, p2 absent, p3 exact 1. p2 is priced "2", so a non-free
// rule that would bill for any positive quantity; p1 and p3 are priced "1"/"1"
// so the parent is unobserved but not itself free, which keeps the scenario from
// being decided by the parent's own tariff. The oracle calls p1 Incomplete
// because its required complete member p2 has no representation. Production
// returned Completeness=complete with total 1: the parent is unobserved, so
// production's conservation proof never inspected it, and the amount-based
// commercial-relevance walk is structurally blind to p2 because an absent
// component produces no line and therefore no amount.
func TestSchemaModelRegressionUnobservedRequiredCoverIsNotComplete(t *testing.T) {
	t.Parallel()
	p1 := r7Key("vendor:sm_reg_req_p1")
	p2 := r7Key("vendor:sm_reg_req_p2")
	p3 := r7Key("vendor:sm_reg_req_p3")
	rels := []metering.ComponentRelationship{
		{Kind: metering.RelationshipAggregate, Parent: p1, Child: p2},
		{Kind: metering.RelationshipSubset, Parent: p1, Child: p3},
	}
	rules := []economics.RatingRule{
		b1Rule(t, "sm-reg-req-p1", p1, "1"),
		b1Rule(t, "sm-reg-req-p2", p2, "2"),
		b1Rule(t, "sm-reg-req-p3", p3, "1"),
	}
	resolved := f356Schema(t, "sm-reg-unobserved-required", rules, rels)
	obs := f3Observation(t, "sm-reg-unobserved-required",
		b1Measure(t, p3, "1"))
	oracle := smSolveOracle(t, smSchemas(rels), smEvidenceEntries(smScope,
		smEvidenceEntry{key: p1, ev: smAbsent},
		smEvidenceEntry{key: p2, ev: smAbsent},
		smEvidenceEntry{key: p3, ev: smOne}))
	if len(oracle.Incomplete) == 0 {
		t.Fatalf("oracle no longer reports the incomplete cover this regression pins; got %+v", oracle)
	}
	for _, seam := range review5beSeams() {
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if val.Completeness == economics.CompletenessComplete {
				t.Errorf("%s: production certified Complete total=%s although the unobserved required cover member p2 carries a non-free rule; oracle_incomplete=%v err=%v components=%v",
					seam.name, smInspect(val).total, oracle.Incomplete, err, smInspect(val).components)
			}
			if !errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
				t.Errorf("%s: err=%v, want ErrSchemaPartitionIncomplete for a missing required member that could have billed money; completeness=%q total=%s",
					seam.name, err, val.Completeness, smInspect(val).total)
			}
		})
	}
}

// TestSchemaModelRegressionExplicitFreeMissingMemberIsNotADependency pins the
// OTHER side of the same commercial boundary, so the ratified decision is
// executable rather than merely untested.
//
// RULE PINNED: a REQUIRED member of a declared complete coverage that was never
// reported is missing evidence, but it is NOT a commercial dependency when its
// own rule is EXPLICIT FREE. A component the tariff declares free can never be
// the money a cover is hiding, so the valuation stays complete and the surviving
// payable subset money settles additively. The same holds for a member with no
// rule at all, which is the deliberate aggregate-only OpenAI shape.
//
// This is the explicit counterweight to
// TestSchemaModelRegressionUnobservedRequiredCoverIsNotComplete, which is why the
// two graphs differ in exactly one value: the missing member's rate.
//
// Exact minimal graph (identical to that regression):
//
//	p1 --aggregate(required)--> p2
//	p1 --subset--> p3
//
// Evidence: p1 absent, p2 absent, p3 exact 1, with p2 priced "0" (explicit
// free) and p1/p3 priced "1". The oracle still calls p1 Incomplete, because the
// oracle is deliberately commercial-free: it reports structural incompleteness
// only. Production must nonetheless certify the valuation complete, and this
// divergence from the oracle is the ratified decision rather than a tolerance.
func TestSchemaModelRegressionExplicitFreeMissingMemberIsNotADependency(t *testing.T) {
	t.Parallel()
	p1 := r7Key("vendor:sm_reg_free_p1")
	p2 := r7Key("vendor:sm_reg_free_p2")
	p3 := r7Key("vendor:sm_reg_free_p3")
	rels := []metering.ComponentRelationship{
		{Kind: metering.RelationshipAggregate, Parent: p1, Child: p2},
		{Kind: metering.RelationshipSubset, Parent: p1, Child: p3},
	}
	rules := []economics.RatingRule{
		b1Rule(t, "sm-reg-free-p1", p1, "1"),
		b1Rule(t, "sm-reg-free-p2", p2, "0"),
		b1Rule(t, "sm-reg-free-p3", p3, "1"),
	}
	resolved := f356Schema(t, "sm-reg-explicit-free-missing-member", rules, rels)
	obs := f3Observation(t, "sm-reg-explicit-free-missing-member",
		b1Measure(t, p3, "1"))
	oracle := smSolveOracle(t, smSchemas(rels), smEvidenceEntries(smScope,
		smEvidenceEntry{key: p1, ev: smAbsent},
		smEvidenceEntry{key: p2, ev: smAbsent},
		smEvidenceEntry{key: p3, ev: smOne}))
	if len(oracle.Incomplete) == 0 {
		t.Fatalf("oracle no longer reports the structurally incomplete cover this boundary is measured against; got %+v", oracle)
	}
	for _, seam := range review5beSeams() {
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
				t.Errorf("%s: an explicit-free missing required member is not a commercial dependency, so the cover must not fail the valuation closed; err=%v completeness=%q total=%s lines=%+v",
					seam.name, err, val.Completeness, smInspect(val).total, val.Lines)
			}
			if err != nil {
				t.Errorf("%s: err=%v, want no diagnostic; completeness=%q total=%s lines=%+v",
					seam.name, err, val.Completeness, smInspect(val).total, val.Lines)
			}
			if val.Completeness != economics.CompletenessComplete {
				t.Errorf("%s: completeness=%q, want complete (oracle_incomplete=%v is structural only); total=%s lines=%+v",
					seam.name, val.Completeness, oracle.Incomplete, smInspect(val).total, val.Lines)
			}
			if total := smInspect(val).total; total != "1/0" {
				t.Errorf("%s: total=%s, want 1/0 (the explicit-free member bills nothing, so p3 settles alone)", seam.name, total)
			}
		})
	}
}

// TestSchemaModelRegressionWholeContextRuleOnMissingMemberIsADependency pins the
// remaining clause of the commercial predicate: selection over the whole economic
// context, or over the period, is still a billable rule.
//
// RULE PINNED: a component whose quantity is UNKNOWN is a commercial dependency
// when its resolved rule is a whole-context or period-selected rule that can yield
// a positive amount. Such a rule is not free merely because the surrounding
// context is unknown -- asking whether the context is complete is a question
// about this call's evidence, and the predicate is about whether money was owed.
// A period-selected rule is evaluated under the same permissive period allowance
// the predicate uses, so it is never dismissed for lacking a period scope here.
//
// Exact minimal graph:
//
//	q1 --partition(required)--> q2
//	q1 --subset--> q3
//
// Evidence: q1 absent, q2 absent, q3 exact 1. q2 carries a whole-context
// positive rule ("3"); q1 and q3 carry ordinary linear rules.
func TestSchemaModelRegressionWholeContextRuleOnMissingMemberIsADependency(t *testing.T) {
	t.Parallel()
	q1 := r7Key("vendor:sm_reg_ctx_q1")
	q2 := r7Key("vendor:sm_reg_ctx_q2")
	q3 := r7Key("vendor:sm_reg_ctx_q3")
	rels := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: q1, Child: q2},
		{Kind: metering.RelationshipSubset, Parent: q1, Child: q3},
	}
	contextRule := b1Rule(t, "sm-reg-ctx-q2", q2, "3")
	contextRule.SelectionScope = economics.SelectionWholeContext
	rules := []economics.RatingRule{
		b1Rule(t, "sm-reg-ctx-q1", q1, "1"),
		contextRule,
		b1Rule(t, "sm-reg-ctx-q3", q3, "1"),
	}
	resolved := f356Schema(t, "sm-reg-whole-context-missing-member", rules, rels)
	obs := f3Observation(t, "sm-reg-whole-context-missing-member",
		b1Measure(t, q3, "1"))
	for _, seam := range review5beSeams() {
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			val, err := seam.rate(t, resolved, obs)
			if val.Completeness == economics.CompletenessComplete {
				t.Errorf("%s: production certified Complete total=%s although the missing required member carries a whole-context positive rule; err=%v components=%v",
					seam.name, smInspect(val).total, err, smInspect(val).components)
			}
			if !errors.Is(err, billing.ErrSchemaPartitionIncomplete) {
				t.Errorf("%s: err=%v, want ErrSchemaPartitionIncomplete for a missing required member selected over the whole context; completeness=%q total=%s",
					seam.name, err, val.Completeness, smInspect(val).total)
			}
		})
	}
}
