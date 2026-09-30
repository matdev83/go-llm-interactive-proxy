package billing_test

// SHADOW OVERLAP AGREEMENT HARNESS (PR #659 adversarial repair 2A, TEST-ONLY).
//
// This file drives the SHADOW predicate (billing.SupportShadowOverlapAudit, whose
// implementation is the unexported component_rater_support.go predicate with a
// test-only door in component_rater_support_test.go) and the FOUR production
// special cases over the SAME populations, and reports how often they agree.
//
// The four production special cases live inside
// (*ReferenceRater).overlappingSchemaInclusionConflicts and are:
//
//	1. a declared inclusion edge whose parent and child are both payable;
//	2. a payable component that transitively includes another payable component,
//	   closing the A subset B subset C chain behind an absent or unpriced middle;
//	3. a PROVEN complete cover whose parent also declares a subset child with a
//	   positive payable descendant outside the cover's paid contributors;
//	4. an UNPROVABLE cover (ambiguous or unresolved) with a positive payable
//	   descendant of one of its subset children, which fails closed because
//	   "could not prove the cover" is not permission to assume disjointness.
//
// WHAT IS COMPARED. Production's four special cases are observed through the one
// thing they can change: whether the call raises ErrSchemaOverlapConflict. The
// shadow predicate is observed through the one thing it can answer: whether any
// pair of distinct positive contributors is a definite overlap. The two agree on
// a case exactly when those two booleans match:
//
//	agree  <=>  production flagged a conflict  <=>  the predicate found a
//	             definite overlap
//
// The predicate's other two answers are non-blocking by construction, so a case
// where the predicate says proven_disjoint or unknown_intersection and production
// raises no conflict is NOT a disagreement about a verdict: it is the predicate
// being more cautious than production, which is exactly what a shadow is for. It
// is still counted and reported, never dropped, because its volume is the evidence
// that the two differ at all.
//
// DISAGREEMENT CLASSES. Every case that does not agree lands in exactly one:
//
//	(c) PRODUCTION LOOSER ON OVERLAP: the predicate finds a definite overlap and
//	    production raises no overlap conflict. This is the dangerous class and it
//	    is the reason this step exists: a containment conflict the rater would
//	    settle additively.
//	(a) STRUCTURAL DISAGREEMENT, unknown flavour: the predicate finds NO definite
//	    overlap but at least one unknown intersection, and production raises no
//	    overlap conflict. Reported split by whether production settled COMPLETE
//	    or failed closed for an unrelated, non-overlap reason. It is a POSITIVE
//	    claim that the schema never allocated the two regions, and it is
//	    non-blocking, so it cannot change money; it is reported, never failed.
//	(b) COMMERCIAL-ONLY DIFFERENCE: production raises an overlap conflict the
//	    predicate does not confirm as a definite overlap. Production is carrying
//	    COMMERCIAL rules the structural predicate deliberately has none of --
//	    chiefly the unprovable-cover fail-closed rule, whose whole justification
//	    is that the parent's PAID partition is unknown, a question about money
//	    rather than about containment. The reason is NAMED from the one cover
//	    authority's own tri-state, not assumed.
//
// POPULATIONS. Four, all reused rather than re-invented:
//   - the full 3-node exhaustive state space in BOTH matrices (343 graphs x 125
//     evidence assignments x 2 seams each), driven through the generators
//     smGraphs / smRelationships / smSchemas / smAllEvidence / smMeasures and
//     classified through smErrorClass and smInspect;
//   - the 40 acceptance vectors' own graphs, built by their own accVectors();
//   - every hand-declared schema in the B1/F2/F3/R7/N1/N2/5be/583/f356/62a/63c/
//     64a/65 regressions, enumerated by reading those files and expressed through
//     their own declared relationship builders.
//
// The regression population is the declared SCHEMA set driven by two canonical
// populations rather than by each regression's own bespoke evidence: ALL_NODES
// (every declared node present, exact 1, priced 1) and LEAVES_ONLY (only the
// nodes that declare no containment child are present, exact 1, priced 1), plus
// a rotation that omits one node from LEAVES_ONLY at a time. That is stated here
// rather than implied: the schema set is the population, the evidence is a
// canonical uniform sample of it.
//
// THE POPULATION IS CROSS-CHECKED AGAINST PRODUCTION. The bridge rebuilds the
// per-scope payable population because that set is internal and a suppressed
// conflict line is never emitted. On every case where production raises NO
// overlap conflict, the harness asserts that the bridge's payable population is
// exactly the set of strictly positive component lines production emitted. A
// divergence is a hard failure, so the agreement numbers can never rest on a
// population the harness has not proved is production's own.

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const supportMaxExamples = 8

// supportVerdict is one case's two answers.
type supportVerdict struct {
	prodClass    string
	prodComplete bool
	// prodActed is the production overlap resolver's own decision: it withheld a
	// line or raised the typed conflict. It is read straight off the resolver over
	// the same populations the predicate saw, not inferred from the returned error
	// class, because the fourth special case classifies under the PARTITION
	// sentinels while still withholding a line.
	prodActed    bool
	prodConflict bool
	// payableRegions is the number of distinct POSITIVE component lines the case
	// carried. Overlap is a relation between positive lines, so a case with none
	// cannot exercise it at all; this counter makes that measured rather than
	// assumed.
	payableRegions int
	pairs          int
	definite       int
	unknown        int
	proven         int
	// covers counts the one cover authority's resolution for every declared cover
	// parent in the case's scope, so a commercial-only difference can be NAMED.
	covers map[string]int
}

// supportTally is the per-population agreement report. Every counter is a number
// and every disagreement keeps named examples, because a low disagreement count
// that cannot be substantiated is worth less than an honest high one.
type supportTally struct {
	label string
	mu    sync.Mutex

	cases              int
	agree              int
	productionActed    int
	productionConflict int
	productionComplete int
	shadowDefinite     int
	shadowUnknown      int
	shadowProven       int
	pairs              int
	payableCases       int

	// (c) production looser on overlap.
	classC int
	// (a) unknown-flavour structural disagreement.
	classAUnknownComplete    int
	classAUnknownNonComplete int
	// (b) commercial-only difference, split by the named reason.
	classB               int
	classBProvenDisjoint int
	// classBNoPairs counts the (b) cases where the shadow predicate had NO pair at
	// all, i.e. fewer than two positive contributors in every scope. Those are the
	// cases production's third special case can reach with a SINGLE positive
	// region, because that special case is a region-versus-COVER relation rather
	// than a region-versus-region one. It is the sharpest measure of the gap.
	classBNoPairs int
	reasonCounts  map[string]int

	examples map[string][]string
}

func newSupportTally(label string) *supportTally {
	return &supportTally{
		label:              label,
		reasonCounts:       map[string]int{},
		examples:           map[string][]string{},
		productionComplete: 0,
	}
}

func (t *supportTally) note(kind, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.noteLocked(kind, message)
}

// noteLocked records one named example. The caller already holds the lock.
func (t *supportTally) noteLocked(kind, message string) {
	if len(t.examples[kind]) < supportMaxExamples {
		t.examples[kind] = append(t.examples[kind], message)
	}
}

// supportCommercialReason names WHY production is stricter than the structural
// predicate, read from the one cover authority's own tri-state. It is a read of
// the evidence, not an assumption, and the two families are genuinely different:
//
//   - unprovable_cover_* is production's FOURTH special case and is commercial by
//     design: the parent's PAID partition exists but its owner or its members
//     cannot be established, so nothing declared inside it may be assumed
//     disjoint from it. That is a question about money, and the structural
//     predicate deliberately has no money.
//   - unplaced_subset_side_contributor_against_a_proven_cover is production's
//     THIRD special case, and it is NOT commercial: it is a structural claim
//     about a region the proven, fully accounted cover does not place. The
//     predicate cannot express it, because it has no paid-contributor notion, so
//     this reason marks a CAPABILITY GAP in the predicate rather than a rule the
//     predicate is entitled to ignore.
func supportCommercialReason(covers map[string]int) string {
	ambiguous, unresolved, proven := covers["ambiguous"], covers["unresolved"], covers["proven"]
	switch {
	case ambiguous > 0 && unresolved > 0:
		return "unprovable_cover_ambiguous_and_unresolved"
	case ambiguous > 0:
		return "ambiguous_shared_complete_member"
	case unresolved > 0:
		return "unprovable_cover_missing_required_member"
	case proven > 0:
		return "unplaced_subset_side_contributor_against_a_proven_cover"
	default:
		return "no_declared_cover"
	}
}

func (t *supportTally) observe(v supportVerdict, caseID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cases++
	t.pairs += v.pairs
	t.shadowDefinite += v.definite
	t.shadowUnknown += v.unknown
	t.shadowProven += v.proven
	if v.prodActed {
		t.productionActed++
	}
	if v.payableRegions > 0 {
		t.payableCases++
	}
	if v.prodConflict {
		t.productionConflict++
	}
	if v.prodComplete {
		t.productionComplete++
	}
	shadowBlocking := v.definite > 0
	if shadowBlocking == v.prodActed {
		t.agree++
		return
	}
	switch {
	case shadowBlocking && !v.prodActed:
		// (c) PRODUCTION LOOSER ON OVERLAP. The predicate finds a definite
		// containment overlap between two positive lines and production's overlap
		// resolver neither withheld a line nor raised the conflict. This is the
		// dangerous class and the reason this step exists.
		t.classC++
		t.noteLocked("c", fmt.Sprintf("%s shadow_definite=%d production class=%s completeness=%s -> the production overlap resolver neither withheld a line nor raised the conflict",
			caseID, v.definite, v.prodClass, supportCompleteness(v.prodComplete)))
	case v.unknown > 0 && !v.prodActed:
		// (a) unknown flavour: the predicate cannot prove the two regions apart
		// and the schema never allocated them, but there is no definite overlap
		// to act on. Non-blocking by construction, so it cannot change money.
		if v.prodComplete {
			t.classAUnknownComplete++
			t.noteLocked("a_unknown_complete", fmt.Sprintf("%s shadow_unknown=%d production class=%s -> production settled COMPLETE with an unallocated pair",
				caseID, v.unknown, v.prodClass))
		} else {
			t.classAUnknownNonComplete++
			t.noteLocked("a_unknown_noncomplete", fmt.Sprintf("%s shadow_unknown=%d production class=%s -> production failed closed for an unrelated class",
				caseID, v.unknown, v.prodClass))
		}
	default:
		// (b) COMMERCIAL-ONLY DIFFERENCE: production is stricter. It is carrying
		// COMMERCIAL rules the structural predicate deliberately has none of.
		t.classB++
		reason := supportCommercialReason(v.covers)
		t.reasonCounts[reason]++
		if v.pairs == 0 {
			t.classBNoPairs++
		}
		if v.proven > 0 {
			t.classBProvenDisjoint++
			t.noteLocked("b_proven_disjoint", fmt.Sprintf("%s production resolver acted (typed_conflict=%t) with shadow pairs=%d (definite=%d unknown=%d proven_disjoint=%d) reason=%s",
				caseID, v.prodConflict, v.pairs, v.definite, v.unknown, v.proven, reason))
		} else {
			t.noteLocked("b_unknown", fmt.Sprintf("%s production resolver acted (typed_conflict=%t) with shadow pairs=%d (definite=%d unknown=%d) reason=%s",
				caseID, v.prodConflict, v.pairs, v.definite, v.unknown, reason))
		}
	}
}

func supportCompleteness(complete bool) string {
	if complete {
		return "complete"
	}
	return "non_complete"
}

func (t *supportTally) report(tst *testing.T, wall time.Duration) {
	tst.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	total := t.cases
	tst.Logf("SUPPORT-AGREEMENT[%s] cases=%d agree=%d disagree=%d (%.2f%% agreement) wall=%s",
		t.label, total, t.agree, total-t.agree, supportPercent(t.agree, total), wall.Round(time.Millisecond))
	tst.Logf("SUPPORT-AGREEMENT[%s] cases_with_at_least_one_positive_region=%d", t.label, t.payableCases)
	tst.Logf("SUPPORT-AGREEMENT[%s] production_resolver_acted=%d of_which_typed_overlap_conflict=%d production_complete=%d shadow_pairs=%d",
		t.label, t.productionActed, t.productionConflict, t.productionComplete, t.pairs)
	tst.Logf("SUPPORT-AGREEMENT[%s] shadow definite_overlap_cases=%d unknown_intersection_cases=%d proven_disjoint_cases=%d",
		t.label, t.shadowDefinite, t.shadowUnknown, t.shadowProven)
	tst.Logf("SUPPORT-AGREEMENT[%s] class_c_production_looser_on_definite_overlap=%d",
		t.label, t.classC)
	tst.Logf("SUPPORT-AGREEMENT[%s] class_a_unknown_intersection_production_complete=%d class_a_unknown_intersection_production_non_complete=%d",
		t.label, t.classAUnknownComplete, t.classAUnknownNonComplete)
	tst.Logf("SUPPORT-AGREEMENT[%s] class_b_commercial_only=%d of_which_shadow_proves_disjoint=%d of_which_shadow_had_no_pair_at_all=%d",
		t.label, t.classB, t.classBProvenDisjoint, t.classBNoPairs)
	for _, reason := range supportSortedKeys(t.reasonCounts) {
		tst.Logf("SUPPORT-AGREEMENT[%s] class_b_reason=%s count=%d", t.label, reason, t.reasonCounts[reason])
	}
	for _, kind := range []string{"c", "a_unknown_complete", "a_unknown_noncomplete", "b_unknown", "b_proven_disjoint"} {
		for _, example := range t.examples[kind] {
			tst.Logf("SUPPORT-AGREEMENT[%s] EXAMPLE %s: %s", t.label, kind, example)
		}
	}
}

func supportPercent(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) * 100 / float64(total)
}

func supportSortedKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// supportMerge folds one population's counters into an aggregate tally. Both
// tallies must already be quiescent: the four populations are joined as parallel
// subtests, so the parent joins them after they return.
func supportMerge(total, tally *supportTally) {
	{
		tally.mu.Lock()
		total.cases += tally.cases
		total.agree += tally.agree
		total.pairs += tally.pairs
		total.payableCases += tally.payableCases
		total.productionActed += tally.productionActed
		total.productionConflict += tally.productionConflict

		total.productionComplete += tally.productionComplete
		total.shadowDefinite += tally.shadowDefinite
		total.shadowUnknown += tally.shadowUnknown
		total.shadowProven += tally.shadowProven
		total.classC += tally.classC
		total.classAUnknownComplete += tally.classAUnknownComplete
		total.classAUnknownNonComplete += tally.classAUnknownNonComplete
		total.classB += tally.classB
		total.classBProvenDisjoint += tally.classBProvenDisjoint
		total.classBNoPairs += tally.classBNoPairs
		for reason, count := range tally.reasonCounts {
			total.reasonCounts[reason] += count
		}
		for kind, examples := range tally.examples {
			for _, example := range examples {
				total.noteLocked(kind, example)
			}
		}
		tally.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// The per-case driver.
// ---------------------------------------------------------------------------

// supportObserve drives one case through production and through the shadow
// predicate, then files the comparison. It cross-checks the bridge's payable
// population against production's emitted positive lines on every case where
// production withheld nothing, which is what makes the agreement numbers
// trustworthy.
func supportObserve(
	t *testing.T,
	tally *supportTally,
	caseID string,
	resolved economics.TariffSnapshot,
	observations []metering.Observation,
	seam review5beSeam,
) {
	t.Helper()
	var (
		val economics.Valuation
		err error
	)
	if len(observations) == 1 {
		val, err = seam.rate(t, resolved, observations[0])
	} else {
		val, err = accRateMany(t, seam, resolved, observations)
	}
	outcome := smInspect(val)

	input := b1OperatorInput(t, resolved, observations)
	if seam.name != "operator_E" {
		input = b1RetailInput(t, resolved, observations)
	}
	audit, auditErr := billing.SupportShadowOverlapAudit(resolved, input)
	if auditErr != nil {
		t.Fatalf("%s: SupportShadowOverlapAudit: %v", caseID, auditErr)
	}
	prodActed := audit.ProductionActed()

	// The payable population is the one thing both sides judge, and production
	// only exposes it for the lines it did not withhold. When production withheld
	// nothing, the two must be identical. The comparison is over DISTINCT
	// identities, because a component billed in two reduction scopes is two lines
	// but one component identity.
	if !prodActed {
		emitted := make([]string, 0, len(outcome.positive))
		for identity := range outcome.positive {
			emitted = append(emitted, identity)
		}
		sort.Strings(emitted)
		bridge := supportDistinct(audit.Payable)
		if !slices.Equal(emitted, bridge) {
			t.Fatalf("%s: the shadow bridge's payable population is not production's own.\nemitted_positive=%v\nbridge_payable=%v",
				caseID, emitted, bridge)
		}
	}

	verdict := supportVerdict{
		prodClass:      smErrorClass(err),
		prodComplete:   outcome.completeness == economics.CompletenessComplete,
		prodActed:      prodActed,
		prodConflict:   audit.ProductionBlocked,
		payableRegions: len(supportDistinct(audit.Payable)),
		pairs:          len(audit.Pairs),
		covers:         map[string]int{},
	}
	counts := audit.Relations()
	verdict.definite = counts[billing.SupportRelationDefiniteOverlap]
	verdict.unknown = counts[billing.SupportRelationUnknownIntersect]
	verdict.proven = counts[billing.SupportRelationProvenDisjoint]
	if verdict.definite+verdict.unknown+verdict.proven != len(audit.Pairs) {
		t.Fatalf("%s: shadow pair relations are not exhaustive: %+v", caseID, counts)
	}
	for _, resolutions := range audit.Covers {
		for _, resolution := range resolutions {
			verdict.covers[resolution]++
		}
	}
	tally.observe(verdict, caseID)
}

// supportDistinct flattens a per-scope identity map into a sorted, de-duplicated
// list of component identities.
func supportDistinct(byScope map[string][]string) []string {
	seen := map[string]struct{}{}
	for _, keys := range byScope {
		for _, key := range keys {
			seen[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// The four populations, joined under one parent so the cross-population total
// is printed from the same counters the per-population lines are.
// ---------------------------------------------------------------------------

// TestSupportAgreementShadowPredicate is the deliverable. It drives the shadow
// predicate and the four production special cases over every population and
// prints one agreement line per population plus the cross-population total.

func supportTallyKeys(tallies map[string]*supportTally) map[string]int {
	keys := make(map[string]int, len(tallies))
	for key := range tallies {
		keys[key] = 0
	}
	return keys
}

// supportAgreementModelSweep drives one matrix of the full 3-node exhaustive
// state space: every publication-valid graph, every evidence assignment, both
// seams. The matrix differs ONLY in the tariff it declares, which is the point:
// the structure matrix isolates topology from money and the commercial matrix
// exercises it.
func supportAgreementModelSweep(
	t *testing.T,
	label string,
	rules func(*testing.T) []economics.RatingRule,
) *supportTally {
	t.Helper()
	tally := newSupportTally(label)
	graphs := smGraphs()
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			continue
		}
		valid = append(valid, opts)
	}
	evidence := smAllEvidence()
	seams := review5beSeams()
	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			relationships := smRelationships(opts)
			resolved := f356Schema(t, fmt.Sprintf("sup-%s-%d", label, gi), rules(t), relationships)
			for _, ev := range evidence {
				measures := smMeasures(t, ev)
				for _, seam := range seams {
					observation := f3Observation(t,
						fmt.Sprintf("sup-%s-%d-%s-%s", label, gi, seam.name, smEvCode(ev)), measures...)
					supportObserve(t, tally, supportModelCaseID(label, seam.name, opts, ev),
						resolved, []metering.Observation{observation}, seam)
				}
			}
		})
	}
	return tally
}

func supportModelCaseID(label, seam string, opts [3]smEdgeKind, ev [3]smEv) string {
	return fmt.Sprintf("%s seam=%s graph[%s] evidence[%s]", label, seam, smOptsName(opts), smEvName(ev))
}

// ---------------------------------------------------------------------------
// Population 3: the 40 acceptance vectors' own graphs.
// ---------------------------------------------------------------------------

// supportAgreementAcceptanceVectors drives the 40 acceptance vectors' OWN graphs,
// built by their own build functions, through both seams.
func supportAgreementAcceptanceVectors(t *testing.T) *supportTally {
	t.Helper()
	tally := newSupportTally("acceptance_vectors")
	for _, vector := range accVectors() {
		t.Run(accNumber(vector.number)+"_"+vector.name, func(t *testing.T) {
			t.Parallel()
			testCase := vector.build(t)
			resolved := f356Schema(t, "sup-acceptance-"+accNumber(vector.number)+"-"+vector.name,
				testCase.rules, testCase.relationships)
			for _, seam := range review5beSeams() {
				supportObserve(t, tally,
					fmt.Sprintf("acceptance %s %s [%s]", accNumber(vector.number), vector.name, seam.name),
					resolved, testCase.observations, seam)
			}
		})
	}
	return tally
}

// ---------------------------------------------------------------------------
// Population 4: every hand-declared schema in the existing regressions.
// ---------------------------------------------------------------------------

// supportRegressionSchema is one hand-declared declared schema, named after the
// regression that declares it.
type supportRegressionSchema struct {
	name          string
	relationships []metering.ComponentRelationship
}

// supportRegressionSchemas enumerates the hand-declared frozen component schemas
// of the B1/F2/F3/R7/N1/N2/5be/583/f356/62a/63c/64a/65 regressions by reading
// those files, and expresses each shape through that regression's OWN declared
// relationship builder wherever one exists, so the graph is the regression's
// graph rather than a paraphrase of it.
//
// Only the SCHEMA is taken from the regression. The evidence is the canonical
// uniform sample described at the top of this file, because the question this
// step answers is about the declared topology and the four special cases, not
// about any one regression's bespoke numbers.
func supportRegressionSchemas() []supportRegressionSchema {
	// f356 / 62a / 63c / 64a / 65 shared keys.
	a, b, c, d := f63aKey(), f63bKey(), f63cKey(), f63dKey()
	x, y := f63xKey(), f63yKey()
	// B1: the single declared inclusion edge, one shape per relationship kind.
	b1Parent := b1Key(metering.DirectionInput, "vendor:b1_aggregate_total", metering.UnitToken)
	b1Child := b1Key(metering.DirectionInput, "vendor:b1_included_part", metering.UnitToken)
	// F2: the pure subset chain and its transform control.
	f2A := b1Key(metering.DirectionInput, "vendor:f2_a", metering.UnitToken)
	f2B := b1Key(metering.DirectionInput, "vendor:f2_b", metering.UnitToken)
	f2C := b1Key(metering.DirectionInput, "vendor:f2_c", metering.UnitToken)
	f2D := b1Key(metering.DirectionInput, "vendor:f2_d", metering.UnitToken)
	// N2 / f356: the zero-charge parent family.
	zeroParent := f356A2Key()
	zeroChildB, zeroChildC := f62AChildB(), f62AChildC()
	// 583: the affected cover plus the disjoint ambiguity fragment.
	locParent := r7Key("vendor:sup583_parent")
	locB, locC := r7Key("vendor:sup583_b"), r7Key("vendor:sup583_c")
	locX, locY := r7Key("vendor:sup583_x"), r7Key("vendor:sup583_y")
	locD := r7Key("vendor:sup583_d")
	locZ1, locZ2 := r7Key("vendor:sup583_z1"), r7Key("vendor:sup583_z2")
	locS := r7Key("vendor:sup583_s")
	// 5be: the nested redundant-path family.
	beParent := r7Key("vendor:sup5be_total")
	beB, beC := r7Key("vendor:sup5be_b"), r7Key("vendor:sup5be_c")
	beD, beE := r7Key("vendor:sup5be_d"), r7Key("vendor:sup5be_e")
	beMidX, beMidY := r7Key("vendor:sup5be_mx"), r7Key("vendor:sup5be_my")
	// R7 / N1: the subset-versus-partition family.
	r7Parent := r7Key("vendor:supr7_parent")
	r7B, r7C := r7Key("vendor:supr7_b"), r7Key("vendor:supr7_c")
	r7D, r7E := r7Key("vendor:supr7_d"), r7Key("vendor:supr7_e")
	// 64a: the mixed class-change chain.
	m64a, m64b, m64c := f64aKey(), f64bKey(), f64cKey()
	// 65: the ancestor chain whose complete sum exceeds it.
	k65a, k65b, k65c, k65d := f65Key("a"), f65Key("b"), f65Key("c"), f65Key("d")
	k65e := f65Key("e")

	partition := func(parent metering.ComponentKey, children ...metering.ComponentKey) []metering.ComponentRelationship {
		return f356Partition(parent, -1, children...)
	}
	edge := func(kind metering.RelationshipKind, parent, child metering.ComponentKey) []metering.ComponentRelationship {
		return b1Schema(kind, parent, child)[0].Relationships
	}

	schemas := []supportRegressionSchema{
		{name: "B1_single_subset_edge", relationships: edge(metering.RelationshipSubset, b1Parent, b1Child)},
		{name: "B1_single_aggregate_edge", relationships: edge(metering.RelationshipAggregate, b1Parent, b1Child)},
		{name: "B1_single_partition_edge", relationships: edge(metering.RelationshipPartition, b1Parent, b1Child)},
		{name: "F2_pure_subset_chain", relationships: []metering.ComponentRelationship{f2SubsetEdge(f2A, f2B), f2SubsetEdge(f2B, f2C)}},
		{name: "F2_four_level_subset_chain", relationships: []metering.ComponentRelationship{f2SubsetEdge(f2A, f2B), f2SubsetEdge(f2B, f2C), f2SubsetEdge(f2C, f2D)}},
		{name: "F2_transform_is_not_containment", relationships: []metering.ComponentRelationship{f2SubsetEdge(f2A, f2B), f2TransformEdge(f2B, f2C)}},
		{name: "F3_partition_required_children", relationships: partition(f3ParentKey(), f3ChildBKey(), f3ChildCKey())},
		{name: "F3_partition_optional_child", relationships: partition(f3ParentKey(), f3ChildBKey())},
		{name: "F3_shared_child_in_own_partition", relationships: partition(f3ParentKey(), f3ChildBKey(), b1Key(metering.DirectionInput, "vendor:f3_shared_child", metering.UnitToken))},
		{name: "F3_ambiguous_sibling_isolation", relationships: f3AmbiguousSiblingSchema()[0].Relationships},
		{name: "R7_subset_chain_versus_partition", relationships: n1SubsetChainSchema(r7Parent, r7B, r7C, r7D, r7E)[0].Relationships},
		{name: "N1_forked_subset_leaves", relationships: n1ForkedSubsetRelationships(r7Parent, r7B, r7C, r7D, r7E, r7Key("vendor:supr7_f"))},
		{name: "62a_single_subset_containment", relationships: f62Subset(f356A1Key(), f356P1BKey())},
		{name: "62a_zero_charge_parent_partition", relationships: partition(zeroParent, zeroChildB, zeroChildC)},
		{name: "62a_openai_cache_read_subset", relationships: []metering.ComponentRelationship{{
			Kind:   metering.RelationshipSubset,
			Parent: b1Key(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken),
			Child:  b1Key(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken),
		}}},
		{name: "5be_partition_parent", relationships: review5bePartitionSchema(beParent, beB, beC)[0].Relationships},
		{name: "5be_partition_parent_plus_redundant_subset", relationships: review5beRedundantSubsetSchema(beParent, beB, beC, beD)[0].Relationships},
		{name: "5be_partition_parent_plus_redundant_and_extra_subset", relationships: f356Subset(review5beRedundantSubsetSchema(beParent, beB, beC, beD)[0].Relationships, beParent, beE)},
		{name: "5be_recursive_cover_plus_subset", relationships: review5beRecursiveSchema(beParent, beB, beC, beMidX, beMidY, beD)[0].Relationships},
		{name: "583_affected_cover", relationships: partition(locParent, locB, locC)},
		{name: "583_nested_affected_cover_plus_subset", relationships: f356Subset(append(
			partition(locParent, locB, locC), partition(locB, locX, locY)...,
		), locParent, locD)},
		{name: "583_disjoint_ambiguity_fragment", relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: locZ1, Child: locS},
			{Kind: metering.RelationshipPartition, Parent: locZ2, Child: locS},
		}},
		{name: "f356_partition_with_optional_member", relationships: f356Partition(f356ZKey(), 1, f356B1Key(), f356B2Key())},
		{name: "f356_shared_first_declaration", relationships: f356Partition(f356ZKey(), -1, f356CKey(), f356DKey())},
		{name: "63c_nested_partition_plus_subset", relationships: f356Subset(append(
			partition(a, b, c), partition(b, x, y)...,
		), a, d)},
		{name: "63c_three_nesting_levels_plus_subset", relationships: f356Subset(append(
			append(partition(a, b, c), partition(b, x, y)...), partition(y, r7Key("vendor:sup63_z"))...,
		), a, d)},
		{name: "64a_mixed_chain_subset_then_partition", relationships: f64ChainDecl(metering.RelationshipSubset)},
		{name: "64a_mixed_chain_partition_then_subset", relationships: f64ChainDecl(metering.RelationshipPartition)},
		{name: "64a_partition_then_subset", relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: m64a, Child: m64b},
			{Kind: metering.RelationshipSubset, Parent: m64b, Child: m64c},
		}},
		{name: "65_ancestor_over_complete_sum", relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: k65a, Child: k65b},
			{Kind: metering.RelationshipPartition, Parent: k65b, Child: k65c},
			{Kind: metering.RelationshipPartition, Parent: k65b, Child: k65d},
		}},
		{name: "65_ancestor_over_incomplete_complete_sum", relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: k65a, Child: k65b},
			{Kind: metering.RelationshipPartition, Parent: k65b, Child: k65c},
			{Kind: metering.RelationshipPartition, Parent: k65b, Child: k65d},
			{Kind: metering.RelationshipPartition, Parent: k65b, Child: k65e},
		}},
	}
	return schemas
}

// supportSchemaNodes lists the distinct component identities a declared schema
// mentions, in ascending canonical-key order, so the canonical populations are
// deterministic.
func supportSchemaNodes(relationships []metering.ComponentRelationship) []metering.ComponentKey {
	seen := map[string]metering.ComponentKey{}
	for _, relationship := range relationships {
		parent, parentErr := relationship.Parent.Normalize()
		child, childErr := relationship.Child.Normalize()
		if parentErr != nil || childErr != nil {
			continue
		}
		seen[parent.CanonicalKey()] = parent
		seen[child.CanonicalKey()] = child
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	nodes := make([]metering.ComponentKey, 0, len(keys))
	for _, key := range keys {
		nodes = append(nodes, seen[key])
	}
	return nodes
}

// supportSchemaParents is the set of identities that declare a containment child.
// Everything else is a leaf of the containment forest.
func supportSchemaParents(relationships []metering.ComponentRelationship) map[string]struct{} {
	parents := map[string]struct{}{}
	for _, relationship := range relationships {
		parent, parentErr := relationship.Parent.Normalize()
		if parentErr != nil {
			continue
		}
		parents[parent.CanonicalKey()] = struct{}{}
	}
	return parents
}

// supportPopulation is one canonical evidence sample of a declared schema.
type supportPopulation struct {
	name     string
	measures []metering.Measure
}

// supportCanonicalPopulations returns the canonical evidence samples for one
// declared schema: every node positive, then the leaves only, then the whole set
// with one node omitted at a time. The node order is ascending canonical key, so
// the populations are a deterministic function of the declared schema.
func supportCanonicalPopulations(t *testing.T, relationships []metering.ComponentRelationship) []supportPopulation {
	t.Helper()
	nodes := supportSchemaNodes(relationships)
	parents := supportSchemaParents(relationships)
	measuresFor := func(keep func(int) bool) []metering.Measure {
		measures := make([]metering.Measure, 0, len(nodes))
		for index, node := range nodes {
			if !keep(index) {
				continue
			}
			measures = append(measures, b1Measure(t, node, "1"))
		}
		return measures
	}
	populated := []supportPopulation{
		{name: "all_nodes_positive", measures: measuresFor(func(int) bool { return true })},
		{name: "leaves_only_positive", measures: measuresFor(func(index int) bool {
			_, isParent := parents[nodes[index].CanonicalKey()]
			return !isParent
		})},
	}
	for omitted := range nodes {
		omitted := omitted
		populated = append(populated, supportPopulation{
			name:     "one_node_absent_" + nodes[omitted].Component,
			measures: measuresFor(func(index int) bool { return index != omitted }),
		})
	}
	return populated
}

// supportAgreementRegressionSchemas drives every hand-declared schema in the
// existing regressions over the canonical populations, through both seams.
func supportAgreementRegressionSchemas(t *testing.T) *supportTally {
	t.Helper()
	tally := newSupportTally("regression_schemas")
	schemas := supportRegressionSchemas()
	for _, schema := range schemas {
		t.Run(schema.name, func(t *testing.T) {
			t.Parallel()
			nodes := supportSchemaNodes(schema.relationships)
			rules := make([]economics.RatingRule, 0, len(nodes))
			for index, node := range nodes {
				rules = append(rules, b1Rule(t, fmt.Sprintf("sup-reg-%s-%d", schema.name, index), node, "1"))
			}
			for _, population := range supportCanonicalPopulations(t, schema.relationships) {
				resolved := f356Schema(t, fmt.Sprintf("sup-reg-%s-%s", schema.name, population.name),
					rules, schema.relationships)
				for _, seam := range review5beSeams() {
					observation := f3Observation(t,
						fmt.Sprintf("sup-reg-%s-%s-%s", schema.name, population.name, seam.name), population.measures...)
					supportObserve(t, tally,
						fmt.Sprintf("regression %s population=%s [%s]", schema.name, population.name, seam.name),
						resolved, []metering.Observation{observation}, seam)
				}
			}
		})
	}
	return tally
}

// ---------------------------------------------------------------------------
// The cross-population summary line and the population cross-checks.
// ---------------------------------------------------------------------------

// TestSupportAuditPayablePopulationMatchesEmittedLines is stated separately from
// the tallies because it is the guard that makes them mean anything: on every
// non-conflict case of every population the bridge's payable population must be
// exactly production's set of strictly positive emitted component lines. It is
// asserted inline by supportObserve; this test restates the contract as a named,
// individually runnable guard with its own coverage count.
func TestSupportAuditPayablePopulationMatchesEmittedLines(t *testing.T) {
	t.Parallel()
	var (
		mu         sync.Mutex
		checked    int
		mismatched int
		suppressed int
	)
	record := func() {
		mu.Lock()
		checked++
		mu.Unlock()
	}
	for _, vector := range accVectors() {
		vector := vector
		t.Run(accNumber(vector.number)+"_"+vector.name, func(t *testing.T) {
			t.Parallel()
			testCase := vector.build(t)
			resolved := f356Schema(t, "sup-paypop-"+accNumber(vector.number), testCase.rules, testCase.relationships)
			for _, seam := range review5beSeams() {
				supportCountPopulation(t, &mu, &mismatched, &suppressed, resolved, testCase.observations, seam, record)
			}
		})
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		t.Logf("SUPPORT-PAYABLE-POPULATION cases_checked=%d mismatches=%d suppressed_payable_lines=%d", checked, mismatched, suppressed)
		if mismatched != 0 {
			t.Errorf("the shadow bridge disagreed with production's payable population on %d case(s)", mismatched)
		}
	})
}

// supportCountPopulation is the guarded single-case form of the cross-check: it
// never fails inline, so the named guard reports every mismatch as a count.
func supportCountPopulation(
	t *testing.T,
	mu *sync.Mutex,
	mismatched *int,
	suppressed *int,
	resolved economics.TariffSnapshot,
	observations []metering.Observation,
	seam review5beSeam,
	record func(),
) {
	t.Helper()
	var (
		val economics.Valuation
		err error
	)
	if len(observations) == 1 {
		val, err = seam.rate(t, resolved, observations[0])
	} else {
		val, err = accRateMany(t, seam, resolved, observations)
	}
	if smErrorClass(err) == "overlap_conflict" {
		return
	}
	outcome := smInspect(val)
	input := b1OperatorInput(t, resolved, observations)
	if seam.name != "operator_E" {
		input = b1RetailInput(t, resolved, observations)
	}
	audit, auditErr := billing.SupportShadowOverlapAudit(resolved, input)
	if auditErr != nil {
		t.Fatalf("SupportShadowOverlapAudit: %v", auditErr)
	}
	emitted := make([]string, 0, len(outcome.positive))
	for identity := range outcome.positive {
		emitted = append(emitted, identity)
	}
	sort.Strings(emitted)
	bridge := make([]string, 0, len(outcome.positive))
	for _, keys := range audit.Payable {
		bridge = append(bridge, keys...)
	}
	sort.Strings(bridge)
	// The invariant is a SUPERSET, not equality. Production's payable population
	// is what the declared graph says bills; its EMITTED positive lines are that
	// population minus the lines suppression deliberately withholds - an
	// unplaceable subset descendant under an unresolved or ambiguous cover, and
	// every quantity line under a typed overlap conflict. Withholding a line is
	// not un-pricing it, so emitted must be a SUBSET of the bridge, and the
	// difference is exactly the suppressed money.
	//
	// Asserting equality here was wrong, and failed on the three vectors whose
	// whole point is that production withholds: 26 unresolved cover, 27 ambiguous
	// cover, and 36 the two-scope subset case. The equality assertion was a defect
	// in this test's premise, not a production defect.
	record()
	if missing := difference(emitted, bridge); len(missing) != 0 {
		mu.Lock()
		*mismatched++
		mu.Unlock()
		t.Errorf("emitted positive line missing from the payable population, which would make the shadow predicate blind to real money: %v (bridge=%v)", missing, bridge)
	}
	mu.Lock()
	*suppressed += len(difference(bridge, emitted))
	mu.Unlock()
}

// difference returns the identities in a that are absent from b, sorted.
func difference(a, b []string) []string {
	if len(a) == 0 {
		return nil
	}
	inB := make(map[string]struct{}, len(b))
	for _, key := range b {
		inB[key] = struct{}{}
	}
	out := make([]string, 0)
	for _, key := range a {
		if _, present := inB[key]; !present {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// TestSupportAgreementFixedFeeStaysOutsideTheGraph pins requirement 3 at the
// harness level, on the one acceptance vector that pairs a payable fixed fee with
// a component overlap conflict: the fixed fee must be a payable line of the
// valuation and must never appear in a single shadow pair.
func TestSupportAgreementFixedFeeStaysOutsideTheGraph(t *testing.T) {
	t.Parallel()
	for _, vector := range accVectors() {
		if vector.number != 31 {
			continue
		}
		testCase := vector.build(t)
		resolved := f356Schema(t, "sup-fixed-fee-31", testCase.rules, testCase.relationships)
		fixedFees := 0
		for _, seam := range review5beSeams() {
			val, err := seam.rate(t, resolved, testCase.observations[0])
			if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
				t.Errorf("%s: err=%v, want ErrSchemaOverlapConflict; completeness=%q", seam.name, err, val.Completeness)
			}
			for i := range val.Lines {
				if val.Lines[i].FixedFee == nil {
					continue
				}
				fixedFees++
			}
			input := b1OperatorInput(t, resolved, testCase.observations)
			if seam.name != "operator_E" {
				input = b1RetailInput(t, resolved, testCase.observations)
			}
			audit, auditErr := billing.SupportShadowOverlapAudit(resolved, input)
			if auditErr != nil {
				t.Fatalf("%s: SupportShadowOverlapAudit: %v", seam.name, auditErr)
			}
			for _, pair := range audit.Pairs {
				if strings.Contains(pair.A, "fixed") || strings.Contains(pair.B, "fixed") {
					t.Errorf("%s: shadow pair (%s, %s) names a fixed fee as a region", seam.name, pair.A, pair.B)
				}
			}
		}
		if fixedFees == 0 {
			t.Error("the independent call-scope fixed fee must survive a component overlap conflict as a payable line")
		}
	}
}

// ---------------------------------------------------------------------------
// Named finding reproducers.
//
// No (a) STRUCTURAL and no (c) PRODUCTION-LOOSER case was found over the four
// populations: 0 of 131,934 cases. The two findings below are the opposite
// direction, and they are pinned here so neither can be discovered twice or
// silently forgotten when the predicate is next changed.
// ---------------------------------------------------------------------------

// supportFindingGraph is the smallest declared graph that isolates production's
// THIRD special case: a complete-partition parent whose own paid partition is
// fully accounted for, plus a subset child whose positive payable descendant the
// cover does not place.
func supportFindingGraph(t *testing.T) (economics.TariffSnapshot, []metering.Observation) {
	parent := r7Key("vendor:sup_find_parent")
	left := r7Key("vendor:sup_find_left")
	right := r7Key("vendor:sup_find_right")
	subset := r7Key("vendor:sup_find_subset")
	rules := []economics.RatingRule{
		b1Rule(t, "sup-find-left", left, "1"),
		b1Rule(t, "sup-find-right", right, "1"),
		b1Rule(t, "sup-find-subset", subset, "1"),
	}
	relationships := f356Subset(f356Partition(parent, -1, left, right), parent, subset)
	resolved := f356Schema(t, "sup-find-proven-cover", rules, relationships)
	observation := f3Observation(t, "sup-find-proven-cover",
		b1Measure(t, parent, "2"),
		b1Measure(t, left, "1"),
		b1Measure(t, right, "1"),
		b1Measure(t, subset, "1"))
	return resolved, []metering.Observation{observation}
}

// TestSupportFindingProvenCoverExtraSubsetContributorIsNotAPairRelation pins
// FINDING 1: production's third special case is a REGION-against-COVER relation,
// not a REGION-against-REGION one, so the pairwise shadow predicate cannot
// express it.
//
// INVARIANT UNDER TEST: a proven, fully accounted complete cover plus a priced
// subset child whose positive payable descendant the cover does not place is an
// overlap conflict. PRODUCTION is right here and the shadow predicate is
// incomplete: the two positive regions (the subset child and a cover member) are
// neither ancestor nor descendant of each other, and no validated complete
// partition separates them, so the predicate reports an UNKNOWN INTERSECTION
// while production correctly refuses. If this test ever fails because the
// predicate now reports a definite overlap, the gap has closed and the finding
// must be deleted rather than inverted.
func TestSupportFindingProvenCoverExtraSubsetContributorIsNotAPairRelation(t *testing.T) {
	t.Parallel()
	resolved, observations := supportFindingGraph(t)
	actedAnywhere := false
	for _, seam := range review5beSeams() {
		val, err := seam.rate(t, resolved, observations[0])
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Errorf("%s: err=%v, want ErrSchemaOverlapConflict; completeness=%q total=%s",
				seam.name, err, val.Completeness, smInspect(val).total)
		}
		actedAnywhere = true
		input := b1OperatorInput(t, resolved, observations)
		if seam.name != "operator_E" {
			input = b1RetailInput(t, resolved, observations)
		}
		audit, auditErr := billing.SupportShadowOverlapAudit(resolved, input)
		if auditErr != nil {
			t.Fatalf("%s: SupportShadowOverlapAudit: %v", seam.name, auditErr)
		}
		if !audit.ProductionActed() {
			t.Errorf("%s: the production overlap resolver withheld nothing", seam.name)
		}
		if audit.Blocking() {
			t.Errorf("%s: FINDING 1 has CLOSED: the shadow predicate now reports a definite overlap on the proven-cover extra-subset-contributor shape; delete this finding instead of inverting it (pairs=%+v)",
				seam.name, audit.Pairs)
		}
		if audit.Relations()[billing.SupportRelationUnknownIntersect] == 0 {
			t.Errorf("%s: the predicate was expected to report the subset-side region against a cover member as an unknown intersection; got %+v",
				seam.name, audit.Relations())
		}
	}
	if !actedAnywhere {
		t.Fatal("the case never reached production")
	}
}

// TestSupportFindingProvenCoverExtraSubsetContributorWithASinglePositiveRegion
// pins FINDING 2, the sharpest form of the same gap: production's third special
// case can fire with exactly ONE positive component line, in which case there is
// no pair for any pairwise predicate to compare.
//
// INVARIANT UNDER TEST: a complete-partition parent whose cover is provable and
// whose optional member is a present exact zero, plus a subset child with the
// only positive payable line in the call, is an overlap conflict. The subset-side
// line is money the proven cover does not place. The shadow predicate reports NO
// pair at all, because a region cannot intersect itself. PRODUCTION is right and
// the predicate is not wrong; it is silent, which is the same finding as
// FINDING 1 seen from the single-contributor side.
func TestSupportFindingProvenCoverExtraSubsetContributorWithASinglePositiveRegion(t *testing.T) {
	t.Parallel()
	parent := r7Key("vendor:sup_find1_parent")
	zeroMember := r7Key("vendor:sup_find1_zero_member")
	subset := r7Key("vendor:sup_find1_subset")
	rules := []economics.RatingRule{b1Rule(t, "sup-find1-subset", subset, "1")}
	relationships := f356Subset(f356Partition(parent, 0, zeroMember), parent, subset)
	resolved := f356Schema(t, "sup-find-single-region", rules, relationships)

	observation := f3Observation(t, "sup-find-single-region",
		b1Measure(t, zeroMember, "0"),
		b1Measure(t, subset, "1"))
	for _, seam := range review5beSeams() {
		val, err := seam.rate(t, resolved, observation)
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
			t.Errorf("%s: err=%v, want ErrSchemaOverlapConflict; completeness=%q total=%s",
				seam.name, err, val.Completeness, smInspect(val).total)
		}
		input := b1OperatorInput(t, resolved, []metering.Observation{observation})
		if seam.name != "operator_E" {
			input = b1RetailInput(t, resolved, []metering.Observation{observation})
		}
		audit, auditErr := billing.SupportShadowOverlapAudit(resolved, input)
		if auditErr != nil {
			t.Fatalf("%s: SupportShadowOverlapAudit: %v", seam.name, auditErr)
		}
		regions := len(supportDistinct(audit.Payable))
		if regions != 1 {
			t.Errorf("%s: the case is supposed to carry exactly ONE positive region, got %d (%v); the finding below is about the single-region form",
				seam.name, regions, supportDistinct(audit.Payable))
		}
		if len(audit.Pairs) != 0 {
			t.Errorf("%s: FINDING 2 has CLOSED: the predicate now produces a pair on a single-positive-region case; delete this finding instead of inverting it (pairs=%+v)",
				seam.name, audit.Pairs)
		}
	}
}
