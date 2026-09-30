package billing_test

// Metamorphic property tests for the frozen component-schema quantity,
// containment and overlap semantics of the production reference rater.
//
// A metamorphic property states a TRANSFORMATION of a (schema, evidence) pair and
// asserts an INVARIANT of the observable outcome across it. Nothing in this file
// asks whether a particular answer is right; every assertion is of the form
// "these two declarations of one physical situation must produce the same
// observable outcome". That is the class of check that survives a schema or a
// rater change: a transformation that provably preserves the world must also
// preserve the verdict, so any difference is either a regression in production or
// a wrong model in this file, and both are worth a failing test.
//
// THE OBSERVABLE TUPLE. Every comparison is made on the full tuple, never on a
// boolean "did it error":
//
//	typed error class   errors.Is against the real billing sentinels, resolved
//	                    through smErrorClass (the differential model check's
//	                    switch) and, for every stated expectation, re-asserted
//	                    through mtpIs against the sentinel itself
//	completeness        economics.Completeness
//	emitted identities  the sorted canonical component keys of every emitted
//	                    line, taken from smInspect
//	payable total       the exact rational carried by valuation.Totals[0]
//
// Every property is table-driven over several base graphs and is driven through
// BOTH rating seams of review5beSeams: the operator/local E seam
// (f3RateOperator) and the customer-policy R seam (f3RateCustomer). Each
// property prints one summary line carrying its base-graph count, its case count
// and its wall time, so a RED run reports the whole population rather than the
// first failure it happened to reach.
//
// REUSE. The enumeration primitives (smGraphs, smRelationships, smSchemas,
// smEvidenceForNodes, smErrorClass, smInspect, smIsQuantityContradiction,
// smLinePositive, smMeasureFor, smReverse), the oracle bridge (smSolveOracle via
// mtpOracle) and the seam table (review5beSeams) all come from the differential
// model check in billing_schema_model_test.go. The frozen-snapshot builders
// (b1Rule, b1Measure, b1UnavailableMeasure, r7Key, f3Resolved, f356Schema,
// f3Observation, b1Observation, b1OperatorInput, b1RetailInput, b1SchemaID) come
// from the existing seams. Nothing is duplicated here; this file only adds the
// transformation harness, the properties, and the per-property tallies.
//
// THE ORACLE'S ROLE. internal/testkit/billsem is used to establish what the
// structural model says about a scenario BEFORE production is judged on it: that
// a scenario really is, or really is not, structurally contradictory, that the
// two forms of a transformation really are the same world, and that the
// absent/exact-zero distinction is one the model keeps. It is deliberately
// commercial-free, so "the oracle says incomplete while production says complete"
// is never treated as a disagreement by itself. Every production assertion in
// this file is on production's observable behaviour.
//
// DIAGNOSIS POLICY. A property that comes out RED is not weakened. Each RED is
// reported with a minimal named reproducer and an explicit judgement of whether
// the PROPERTY is wrong (the invariant is genuinely not a property of the model)
// or PRODUCTION is wrong (the rater loses or changes a decision under a
// transformation that preserves the world). No production code is edited here.
//
// FINDINGS AT THE TIME OF WRITING. Five of the twelve test functions are RED, and
// every RED has been diagnosed. The single root cause behind three of them is
// stated once here, because it is the same defect seen from three transformations.
//
//   ROOT CAUSE. A REQUIRED member of a declared complete coverage that is ABSENT is
//   treated as missing evidence by the conservation classifier
//   (completeChildPartitionCoverage, the `missingMember` branch) and by the interval
//   solver (representedExactQuantities, the `quantityAbsent && !optional` branch) --
//   an absent intermediate's quantity is never derived from its own proven complete
//   cover. The OVERLAP resolver in the same file does implement that derivation
//   (collectCompleteCoverMembers, "an absent aggregate is covered by its complete
//   priced partition without faking its quantity"), and the independent model in
//   internal/testkit/billsem does NOT: there, an absent required member breaks
//   deriveRepresented too. So the two production analyses disagree with each other
//   on the same graph, and the same transformation is judged differently depending
//   on which analysis is asked. The consequences, one per property:
//
//     P1 edge subdivision          RED on 15 of 26 subtests, ALL of them in the
//                                  complete-coverage classes. The 5 subset base
//                                  graphs (10 subtests) are GREEN, which localises
//                                  the defect exactly: transitive containment is
//                                  handled, the transitive CONSERVATION CLAIM is
//                                  not. The cleanest reproducer is
//                                  `partition/partition_conserved`: a conserved
//                                  complete cover a = c + d rates COMPLETE, and
//                                  inserting the unobserved alias b between a and c
//                                  turns it into ErrSchemaPartitionIncomplete.
//     P2 complete-cover expansion  RED on 12 of 16 subtests. The 4 GREEN subtests
//                                  are `partial_containment_member` (a --subset-->
//                                  b, where no cover is claimed over a at all) and
//                                  `unobserved_ancestor` (where the overlap
//                                  resolver's recursion happens to win). The money
//                                  is IDENTICAL in every RED row; only the
//                                  classification and the completeness move.
//     P5 pricing metamorphism      Clause 1 is GREEN: no tariff ever lets a
//                                  structurally contradicted graph certify complete
//                                  money, across 12,720 rated cases. Clause 2 is
//                                  RED on 48 subtests with ONE shape:
//                                  paid -> overlap_conflict,
//                                  explicit_free -> partition_incomparable,
//                                  no_rule -> partition_contradiction. The choice
//                                  is made in the `case ev.tainted` branch, which
//                                  gates "record the arithmetic contradiction" on
//                                  `!billable(ev, scopeCovered)` -- a rule-resolution,
//                                  i.e. commercial, predicate.
//
// P3 flattening is RED on 1 of 14 subtests, for a different and narrower reason, and
// P6/P7/P8/P4 are GREEN. P3's single RED is `paid_subset_outside_the_cover` under
// the paid-leaves tariff: the conflict suppression set is built from the parent's
// DIRECT complete members, so an explicitly-free intermediate leaves the deeper
// payable leaves outside the suppression and a conflict valuation still settles
// them (total 3) -- while flattening removes the intermediate, the same declared
// world then suppresses them (total 0).

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/billsem"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ---------------------------------------------------------------------------
// Declared base graphs.
//
// A base graph owns a private component-key namespace, so no two graphs can ever
// interact and a scenario can never accidentally depend on a key left over from
// another scenario. It owns only the DECLARED structure: pricing and evidence are
// supplied per scenario so several scenarios can share one structure and differ
// only in the transformation under test.
// ---------------------------------------------------------------------------

// mtpEdge is one declared frozen edge over graph roles.
type mtpEdge struct {
	kind     metering.RelationshipKind
	parent   string
	child    string
	optional bool
}

type mtpGraph struct {
	name string
	keys map[string]metering.ComponentKey
}

func mtpNewGraph(name string, roles ...string) *mtpGraph {
	keys := make(map[string]metering.ComponentKey, len(roles))
	for _, role := range roles {
		keys[role] = r7Key("vendor:mtp_" + name + "_" + role)
	}
	return &mtpGraph{name: name, keys: keys}
}

func (g *mtpGraph) key(role string) metering.ComponentKey {
	key, ok := g.keys[role]
	if !ok {
		panic("mtpGraph " + g.name + " declares no role " + role)
	}
	return key
}

// canon renders a role as the canonical component identity production emits, so a
// property can name a substituted identity in the same vocabulary the
// emitted-identity list uses.
func (g *mtpGraph) canon(role string) string { return g.key(role).CanonicalKey() }

func mtpRels(g *mtpGraph, edges ...mtpEdge) []metering.ComponentRelationship {
	rels := make([]metering.ComponentRelationship, 0, len(edges))
	for _, edge := range edges {
		rels = append(rels, metering.ComponentRelationship{
			Kind: edge.kind, Parent: g.key(edge.parent), Child: g.key(edge.child), Optional: edge.optional,
		})
	}
	return rels
}

// mtpOneSchema wraps a relationship set in the single frozen schema most
// scenarios use, so two forms differ only in their declared edges.
func mtpOneSchema(rels []metering.ComponentRelationship) []metering.ComponentSchema {
	return []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: rels}}
}

// mtpValidate fails a scenario whose own declared graph is unpublishable, which
// would otherwise turn a metamorphic comparison into a comparison of two
// publication failures.
func mtpValidate(t *testing.T, label string, schemas []metering.ComponentSchema) {
	t.Helper()
	if err := metering.ValidateComponentSchemas(schemas); err != nil {
		t.Fatalf("%s: the scenario's own frozen schema set is unpublishable, so the property would compare two publication failures: %v", label, err)
	}
}

func mtpSortedRoles(g *mtpGraph) []string {
	roles := make([]string, 0, len(g.keys))
	for role := range g.keys {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// ---------------------------------------------------------------------------
// Evidence.
//
// A conservation property needs covers whose members sum to their parent's
// quantity, and a five-state vocabulary cannot express sums like "two units
// split into one and one, plus a one-unit sibling". So the differential model
// check's five states are restated here as exact quantities, and mtpExact adds
// any further exact quantity.
// ---------------------------------------------------------------------------

// mtpCustomExact is one past the differential model check's five evidence states.
// It marks an exact report whose quantity is written out literally.
const mtpCustomExact smEv = smEvCount

type mtpEv struct {
	state    smEv
	quantity string
}

var (
	mtpAbsentEv      = mtpEv{state: smAbsent}
	mtpZeroEv        = mtpEv{state: smZero, quantity: "0"}
	mtpOneEv         = mtpEv{state: smOne, quantity: "1"}
	mtpTwoEv         = mtpEv{state: smTwo, quantity: "2"}
	mtpUnavailableEv = mtpEv{state: smUnavailable}
)

// mtpExact builds an exact evidence state with an arbitrary quantity.
func mtpExact(quantity string) mtpEv { return mtpEv{state: mtpCustomExact, quantity: quantity} }

func (e mtpEv) measure(t *testing.T, key metering.ComponentKey) (metering.Measure, bool) {
	t.Helper()
	if e.state == mtpCustomExact {
		return b1Measure(t, key, e.quantity), true
	}
	return smMeasureFor(t, key, e.state)
}

func (e mtpEv) oracleState() (billsem.ObsState, *big.Rat) {
	switch e.state {
	case smAbsent:
		return billsem.ObsAbsent, nil
	case smZero:
		return billsem.ObsExact, big.NewRat(0, 1)
	case smOne:
		return billsem.ObsExact, big.NewRat(1, 1)
	case smTwo:
		return billsem.ObsExact, big.NewRat(2, 1)
	case smUnavailable:
		return billsem.ObsUnavailable, nil
	case mtpCustomExact:
		rat, ok := new(big.Rat).SetString(e.quantity)
		if !ok {
			panic("mtpExact: quantity " + e.quantity + " is not a rational")
		}
		return billsem.ObsExact, rat
	default:
		panic("mtpEv carries an unknown evidence state")
	}
}

// mtpEvidence maps a role to its reported state. A role the map does not mention
// is ABSENT.
type mtpEvidence map[string]mtpEv

func (e mtpEvidence) measures(t *testing.T, g *mtpGraph) []metering.Measure {
	t.Helper()
	roles := mtpSortedRoles(g)
	measures := make([]metering.Measure, 0, len(roles))
	for _, role := range roles {
		if measure, ok := e[role].measure(t, g.key(role)); ok {
			measures = append(measures, measure)
		}
	}
	return measures
}

func (e mtpEvidence) obs(t *testing.T, g *mtpGraph, id string) metering.Observation {
	t.Helper()
	return f3Observation(t, id, e.measures(t, g)...)
}

// mtpOracleEvidence projects every DECLARED role of one graph onto the oracle's
// evidence vocabulary. A declared role the evidence omits is marked ABSENT, which
// the oracle keeps strictly distinct from an exact zero; that distinction is the
// whole point of the optionality property, so it is made explicit here rather
// than inferred from a missing map entry.
func mtpOracleEvidence(g *mtpGraph, ev mtpEvidence) billsem.Evidence {
	evidence := billsem.NewEvidence()
	states := make(map[string]billsem.ObsState, len(g.keys))
	values := make(map[string]*big.Rat, len(g.keys))
	for _, role := range mtpSortedRoles(g) {
		canonical := g.key(role).CanonicalKey()
		state, value := ev[role].oracleState()
		states[canonical] = state
		if value != nil {
			values[canonical] = value
		}
	}
	evidence.States[smScope] = states
	evidence.Values[smScope] = values
	return evidence
}

// mtpOracle runs the independent oracle over one form and returns its scope
// result. It delegates to the differential model check's own bridge, smSolveOracle,
// so the single billsem.Solve call and the single scope lookup stay in one place; it
// is used only to certify what the structural model says about a scenario, and never
// to assert production's money.
func mtpOracle(t *testing.T, label string, schemas []metering.ComponentSchema, evidence billsem.Evidence) billsem.ScopeResult {
	t.Helper()
	result := smSolveOracle(t, schemas, evidence)
	// The bridge already fails closed on a missing scope; this re-checks the one
	// it cannot, namely that it answered for the scope this file declared its
	// evidence under, so a change to the bridge cannot silently hand back an
	// empty scope result that every precondition would then read as "clean".
	if result.Scope != smScope {
		t.Fatalf("%s: the oracle bridge answered for scope %q, want %q", label, result.Scope, smScope)
	}
	return result
}

// mtpOracleClean states whether the oracle finds a form structurally sound: no
// node whose enforced lower bound exceeds its upper bound, and no node it calls
// incomplete.
func mtpOracleClean(result billsem.ScopeResult) bool {
	return len(result.Contradicted) == 0 && len(result.Incomplete) == 0
}

// mtpOracleSharedClass renders the oracle's classification of the roles two forms
// have in COMMON. The subdivided form necessarily declares one extra node (the
// intermediate), so a comparison over the whole node universe would report that
// extra node as a difference even when every shared node is classified
// identically; the claim under test is about the shared world, so that is what is
// compared here.
func mtpOracleSharedClass(g *mtpGraph, result billsem.ScopeResult, roles ...string) string {
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		node, known := result.Nodes[g.canon(role)]
		if !known {
			out = append(out, role+"=<not-a-node>")
			continue
		}
		represented := "-"
		if node.Represented != nil {
			represented = node.Represented.RatString()
		}
		out = append(out, fmt.Sprintf("%s=%s/%s", role, node.Class, represented))
	}
	return strings.Join(out, " ")
}

// mtpOracleVerdicts renders the model's two SET-LEVEL verdicts for a form: whether
// ANY node is contradicted, and whether ANY node is incomplete. Two declarations of
// the same equations must agree on both. The comparison is deliberately at set
// level rather than per node, because a transformation that removes or relocates a
// node may legitimately move WHICH node carries a verdict -- flattening a cover
// whose intermediate is missing evidence localises the missing member on the
// parent instead of on the intermediate -- while the model's own verdict about the
// declared world is unchanged.
func mtpOracleVerdicts(result billsem.ScopeResult) string {
	return fmt.Sprintf("contradicted=%v incomplete=%v ambiguous=%v",
		len(result.Contradicted) > 0, len(result.Incomplete) > 0, len(result.Ambiguous) > 0)
}

// ---------------------------------------------------------------------------
// Pricing.
//
// An EMPTY price declares NO RULE AT ALL, which production treats as a different
// thing from an explicit-free "0" rule and which the pricing-metamorphism
// property needs as its third state.
// ---------------------------------------------------------------------------

type mtpPricing map[string]string

func (p mtpPricing) rules(t *testing.T, g *mtpGraph, tag string) []economics.RatingRule {
	t.Helper()
	roles := make([]string, 0, len(p))
	for role := range p {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	rules := make([]economics.RatingRule, 0, len(roles))
	for _, role := range roles {
		if p[role] == "" {
			continue
		}
		rules = append(rules, b1Rule(t, "mtp-"+tag+"-"+g.name+"-"+role, g.key(role), p[role]))
	}
	return rules
}

// mtpFree prices every declared role explicitly free. Every declared rate is
// distinct per role wherever money is being compared, because a transformation
// that moved money between two identities would be invisible under equal rates.
func mtpFree(g *mtpGraph) mtpPricing {
	prices := make(mtpPricing, len(g.keys))
	for role := range g.keys {
		prices[role] = "0"
	}
	return prices
}

// mtpPaid prices every declared role at a distinct positive rate, so a
// transformation that moved money between two identities is always visible.
func mtpPaid(g *mtpGraph) mtpPricing {
	prices := make(mtpPricing, len(g.keys))
	rate := 1
	for _, role := range mtpSortedRoles(g) {
		prices[role] = strconv.Itoa(rate)
		rate++
	}
	return prices
}

func mtpNoRule(g *mtpGraph) mtpPricing {
	prices := make(mtpPricing, len(g.keys))
	for role := range g.keys {
		prices[role] = ""
	}
	return prices
}

// ---------------------------------------------------------------------------
// The observable outcome and the comparison primitives.
// ---------------------------------------------------------------------------

// mtpOutcome is one (form, seam) observable result: the full tuple plus the exact
// per-identity payable money, so a transformation that deliberately MOVES money
// from one identity to another can be compared on the amount that moved and not
// only on the identities that did not move.
type mtpOutcome struct {
	seam         string
	err          error
	errClass     string
	completeness economics.Completeness
	components   []string
	display      []string
	total        string
	// positive maps each emitted component identity to the exact positive
	// payable amount attributed to it.
	positive map[string]*big.Rat
	// payable is the sorted set of identities that actually carry money, a
	// strictly smaller set than the emitted identities whenever a component rated
	// zero or rated free.
	payable []string
}

func (o mtpOutcome) tuple() string {
	return fmt.Sprintf("seam=%s err=%s compl=%s total=%s components=%s payable=%s",
		o.seam, o.errClass, o.completeness, o.total, strings.Join(o.display, ","), strings.Join(o.payable, ","))
}

// mtpSentinel maps a class name back to the real production sentinel, so a stated
// expectation is asserted through errors.Is against the sentinel itself rather
// than against a string this file's own switch produced.
func mtpSentinel(class string) error {
	switch class {
	case "partition_contradiction":
		return billing.ErrSchemaPartitionContradiction
	case "partition_incomplete":
		return billing.ErrSchemaPartitionIncomplete
	case "partition_incomparable":
		return billing.ErrSchemaPartitionIncomparable
	case "subset_contradiction":
		return billing.ErrSchemaSubsetContradiction
	case "quantity_contradiction":
		return billing.ErrSchemaQuantityContradiction
	case "overlap_conflict":
		return billing.ErrSchemaOverlapConflict
	default:
		return nil
	}
}

// mtpIs reports whether the retained error wraps the real sentinel named by the
// class smErrorClass resolved. It is the typed form of the same fact.
func mtpIs(err error, class string) bool {
	sentinel := mtpSentinel(class)
	return sentinel != nil && errors.Is(err, sentinel)
}

// mtpLineAmount extracts the exact rational amount of one line, honouring both
// the decimal and the exact-rational representation.
func mtpLineAmount(line *economics.LineItem) *big.Rat {
	if line.Amount != nil {
		if rat, err := line.Amount.ToRat(); err == nil {
			return rat
		}
	}
	if line.AmountNumerator != "" && line.AmountDenominator != "" {
		numerator, okN := new(big.Int).SetString(line.AmountNumerator, 10)
		denominator, okD := new(big.Int).SetString(line.AmountDenominator, 10)
		if okN && okD && denominator.Sign() != 0 {
			return new(big.Rat).SetFrac(numerator, denominator)
		}
	}
	return nil
}

// mtpRat decodes the canonical decimal form used by the totals ("240/0" is 240,
// "1/2" is one half) into an exact rational, so two totals can be added rather
// than only compared for equality.
func mtpRat(canonical string) (*big.Rat, bool) {
	coefficient, scale, ok := strings.Cut(canonical, "/")
	if !ok {
		return nil, false
	}
	numerator, okN := new(big.Int).SetString(coefficient, 10)
	exponent, errE := strconv.Atoi(scale)
	if !okN || errE != nil || exponent < 0 {
		return nil, false
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
	return new(big.Rat).SetFrac(numerator, denominator), true
}

// mtpIsSpecificCoverDiagnosis reports whether errClass is one of the
// complete-coverage diagnoses that are strictly MORE SPECIFIC than a bare
// quantity contradiction for the same node: the member was never reported, or
// its children are ambiguously shared. Both rest on the declared structure
// itself, which is why either may stand in for the solver's suppressed verdict.
func mtpIsSpecificCoverDiagnosis(errClass string) bool {
	switch errClass {
	case "partition_incomplete", "partition_incomparable":
		return true
	default:
		return false
	}
}

func mtpCollect(seam string, val economics.Valuation, err error) mtpOutcome {
	inspected := smInspect(val)
	out := mtpOutcome{
		seam:         seam,
		err:          err,
		errClass:     smErrorClass(err),
		completeness: inspected.completeness,
		components:   inspected.components,
		total:        inspected.total,
		positive:     make(map[string]*big.Rat, len(inspected.components)),
	}
	for i := range val.Lines {
		line := &val.Lines[i]
		if line.Component == nil {
			continue
		}
		// display is a compact but complete identity of the same component the
		// canonical list names, so a RED message stays readable.
		compact := fmt.Sprintf("%s/%s/%s", line.Component.Direction, line.Component.Component, line.Component.Unit)
		out.display = append(out.display, compact)
		if !smLinePositive(line) {
			continue
		}
		amount := mtpLineAmount(line)
		if amount == nil {
			continue
		}
		canonical := line.Component.CanonicalKey()
		prior, seen := out.positive[canonical]
		if !seen {
			out.positive[canonical] = new(big.Rat).Set(amount)
			out.payable = append(out.payable, compact)
			continue
		}
		prior.Add(prior, amount)
	}
	sort.Strings(out.display)
	sort.Strings(out.payable)
	return out
}

// ---------------------------------------------------------------------------
// Rating through both seams.
// ---------------------------------------------------------------------------

// mtpForm is one concrete rated input: the frozen schema set, the tariff, and the
// evidence. Both seams see exactly the same form.
type mtpForm struct {
	schemas []metering.ComponentSchema
	rules   []economics.RatingRule
	obs     []metering.Observation
}

// mtpRateSeam rates one form through one seam. A review5beSeam carries the real
// single-observation production closure, which every one-observation form in this
// file uses unchanged; only the scope-split property needs more than one
// observation in a single call, and that goes through the very same two
// production entry points with the whole observation set.
func mtpRateSeam(t *testing.T, seam review5beSeam, resolved economics.TariffSnapshot, obs []metering.Observation) (economics.Valuation, error) {
	t.Helper()
	if len(obs) == 0 {
		t.Fatalf("form carries no observation, so there is nothing to rate")
	}
	if len(obs) == 1 {
		return seam.rate(t, resolved, obs[0])
	}
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	switch seam.name {
	case "operator_E":
		return rater.Rate(context.Background(), b1OperatorInput(t, resolved, obs))
	case "customer_policy_R":
		return billing.RateCustomerPolicyObservation(context.Background(), b1RetailInput(t, resolved, obs), resolved)
	default:
		t.Fatalf("unknown seam %q: this file refuses to guess a rating path for a seam it does not know", seam.name)
		return economics.Valuation{}, nil
	}
}

// mtpRateForm drives one form through BOTH review5beSeams and returns one outcome
// per seam, in review5beSeams order.
func mtpRateForm(t *testing.T, refID string, form mtpForm) []mtpOutcome {
	t.Helper()
	resolved := f3Resolved(t, refID, form.rules, form.schemas)
	seams := review5beSeams()
	out := make([]mtpOutcome, 0, len(seams))
	for _, seam := range seams {
		val, err := mtpRateSeam(t, seam, resolved, form.obs)
		out = append(out, mtpCollect(seam.name, val, err))
	}
	return out
}

// ---------------------------------------------------------------------------
// Comparison primitives.
// ---------------------------------------------------------------------------

// mtpSameTuple reports whether two outcomes agree on the full observable tuple.
func mtpSameTuple(a, b mtpOutcome) bool {
	return a.errClass == b.errClass &&
		a.completeness == b.completeness &&
		a.total == b.total &&
		slices.Equal(a.components, b.components)
}

// mtpAssertSameTuple fails unless the two forms agree on EVERY observable field.
// It is deliberately not reducible to "both errored": the error class, the
// completeness, the emitted identities and the payable total are each compared
// on their own, and a single disagreement is a failure.
func mtpAssertSameTuple(t *testing.T, label string, reference, transformed mtpOutcome) {
	t.Helper()
	if mtpSameTuple(reference, transformed) {
		return
	}
	t.Errorf("%s: the observable outcome changed under a world-preserving transformation\n  reference  : %s\n  transformed: %s",
		label, reference.tuple(), transformed.tuple())
}

func mtpWithout(components, drop []string) []string {
	if len(drop) == 0 {
		return components
	}
	removed := make(map[string]struct{}, len(drop))
	for _, identity := range drop {
		removed[identity] = struct{}{}
	}
	kept := make([]string, 0, len(components))
	for _, identity := range components {
		if _, gone := removed[identity]; !gone {
			kept = append(kept, identity)
		}
	}
	return kept
}

func mtpPositiveOutside(money map[string]*big.Rat, drop []string) *big.Rat {
	removed := make(map[string]struct{}, len(drop))
	for _, identity := range drop {
		removed[identity] = struct{}{}
	}
	total := new(big.Rat)
	for identity, amount := range money {
		if _, gone := removed[identity]; !gone {
			total.Add(total, amount)
		}
	}
	return total
}

func mtpPositiveInside(money map[string]*big.Rat, keep []string) *big.Rat {
	wanted := make(map[string]struct{}, len(keep))
	for _, identity := range keep {
		wanted[identity] = struct{}{}
	}
	total := new(big.Rat)
	for identity, amount := range money {
		if _, ok := wanted[identity]; ok {
			total.Add(total, amount)
		}
	}
	return total
}

// mtpTotalRat decodes an outcome's payable total. A rejected valuation may carry
// no CurrencyTotal at all, which means "nothing payable was settled" and is
// therefore zero; a valuation that DID emit a positive payable line and still
// carries no total is an internal inconsistency and is reported as such rather
// than silently read as zero, because the total is one of the four observable
// fields the properties compare.
func mtpTotalRat(t *testing.T, label string, outcome mtpOutcome) *big.Rat {
	t.Helper()
	if total, ok := mtpRat(outcome.total); ok {
		return total
	}
	settled := mtpPositiveOutside(outcome.positive, nil)
	if settled.Sign() != 0 {
		t.Errorf("%s: the valuation emitted a strictly positive payable line for %s but carries no payable total, so the total cannot be compared: %s",
			label, settled.RatString(), outcome.tuple())
	}
	return new(big.Rat)
}

// mtpCountIdentities returns the multiplicity of every identity in a sorted
// identity list, so one list can be checked for MULTISET containment in another.
// A sorted list makes a plain prefix comparison wrong: an identity may legitimately
// be emitted once by the base and twice by an extension that adds a second scope.
func mtpCountIdentities(components []string) map[string]int {
	counts := make(map[string]int, len(components))
	for _, identity := range components {
		counts[identity]++
	}
	return counts
}

// mtpAssertSubstituted is the one deliberate exception to mtpAssertSameTuple, for
// a transformation that relocates a quantity from one component identity onto a
// DIFFERENT SET of component identities: expanding a complete cover into its
// children bills the children instead of the covered node, and the billed identity
// is exactly what the transformation changes, so the identity list cannot be
// identical by construction.
//
// Everything else must still be identical: the error class, the completeness,
// every identity OUTSIDE the substituted sets, the total payable across the whole
// valuation, and above all the AMOUNT, which must be carried in full by the
// substituted identities rather than lost or duplicated. That last clause is the
// one with teeth: it fails both if the expansion silently drops the covered
// node's money and if it silently pays it twice.
func mtpAssertSubstituted(t *testing.T, label string, reference, transformed mtpOutcome, dropReference, dropTransformed []string) {
	t.Helper()
	if reference.errClass != transformed.errClass {
		t.Errorf("%s: typed error class changed under a world-preserving transformation: reference %q transformed %q\n  reference  : %s\n  transformed: %s",
			label, reference.errClass, transformed.errClass, reference.tuple(), transformed.tuple())
	}
	if reference.completeness != transformed.completeness {
		t.Errorf("%s: completeness changed under a world-preserving transformation: reference %q transformed %q\n  reference  : %s\n  transformed: %s",
			label, reference.completeness, transformed.completeness, reference.tuple(), transformed.tuple())
	}
	keptReference := mtpWithout(reference.components, dropReference)
	keptTransformed := mtpWithout(transformed.components, dropTransformed)
	if !slices.Equal(keptReference, keptTransformed) {
		t.Errorf("%s: an emitted identity OUTSIDE the substituted set changed: reference %v transformed %v\n  reference  : %s\n  transformed: %s",
			label, keptReference, keptTransformed, reference.tuple(), transformed.tuple())
	}
	if reference.total != transformed.total {
		referenceTotal := mtpTotalRat(t, label+" reference", reference)
		transformedTotal := mtpTotalRat(t, label+" transformed", transformed)
		if referenceTotal.Cmp(transformedTotal) != 0 {
			t.Errorf("%s: the payable total changed under a world-preserving transformation: reference %s transformed %s\n  reference  : %s\n  transformed: %s",
				label, referenceTotal.RatString(), transformedTotal.RatString(), reference.tuple(), transformed.tuple())
		}
	}
	carriedReference := mtpPositiveInside(reference.positive, dropReference)
	carriedTransformed := mtpPositiveInside(transformed.positive, dropTransformed)
	if carriedReference.Cmp(carriedTransformed) != 0 {
		t.Errorf("%s: the substituted identities do not carry equal money: reference %s transformed %s; the quantity was lost or duplicated by the transformation\n  reference  : %s\n  transformed: %s",
			label, carriedReference.RatString(), carriedTransformed.RatString(), reference.tuple(), transformed.tuple())
	}
	outsideReference := mtpPositiveOutside(reference.positive, dropReference)
	outsideTransformed := mtpPositiveOutside(transformed.positive, dropTransformed)
	if outsideReference.Cmp(outsideTransformed) != 0 {
		t.Errorf("%s: payable money outside the substituted set changed: reference %s transformed %s\n  reference  : %s\n  transformed: %s",
			label, outsideReference.RatString(), outsideTransformed.RatString(), reference.tuple(), transformed.tuple())
	}
}

// ---------------------------------------------------------------------------
// Per-property tally.
// ---------------------------------------------------------------------------

// mtpSkewCap bounds how many cross-seam disagreements are quoted in a summary so
// a systemic skew cannot flood the log. The counter itself is unbounded.
const mtpSkewCap = 3

// mtpTally counts the executed cases of one property and prints a single summary
// line, so a RED run reports the whole population rather than only the first
// failure it happened to reach. It is safe for parallel subtests.
type mtpTally struct {
	property   string
	start      time.Time
	baseGraphs atomic.Int64
	cases      atomic.Int64
	seamSkew   atomic.Int64

	mu    sync.Mutex
	skews []string
}

func newMTPTally(property string, baseGraphs int) *mtpTally {
	tally := &mtpTally{property: property, start: time.Now()}
	tally.baseGraphs.Store(int64(baseGraphs))
	return tally
}

func (p *mtpTally) countCase() { p.cases.Add(1) }

// mtpNoteSeamSkew records a disagreement between the two seams on ONE form. A skew
// is a property of the seams rather than of the transformation under test, so it
// is counted and reported rather than failed here; it is never dropped silently.
func (p *mtpTally) mtpNoteSeamSkew(label string, outcomes []mtpOutcome) {
	for i := 1; i < len(outcomes); i++ {
		if mtpSameTuple(outcomes[0], outcomes[i]) {
			continue
		}
		p.seamSkew.Add(1)
		p.mu.Lock()
		if len(p.skews) < mtpSkewCap {
			p.skews = append(p.skews, fmt.Sprintf("%s %s vs %s", label, outcomes[0].tuple(), outcomes[i].tuple()))
		}
		p.mu.Unlock()
	}
}

func (p *mtpTally) finish(t *testing.T) {
	t.Helper()
	t.Logf("METAMORPHIC[%s] base_graphs=%d cases_executed=%d seams=%d cross_seam_disagreements=%d wall=%s",
		p.property, p.baseGraphs.Load(), p.cases.Load(), len(review5beSeams()), p.seamSkew.Load(),
		time.Since(p.start).Round(time.Millisecond))
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, note := range p.skews {
		t.Logf("METAMORPHIC[%s] CROSS_SEAM_DISAGREEMENT %s", p.property, note)
	}
}

// ---------------------------------------------------------------------------
// The transformations.
// ---------------------------------------------------------------------------

// mtpSubdivide inserts an UNOBSERVED intermediate on the from -> to edge, turning
// `from --K--> to` into `from --K--> via` + `via --inner--> to`. The intermediate
// is never given a measure, so the subdivision is a pure re-declaration: the same
// region under `from`, the same declared quantity, one more declared node that
// reports nothing.
//
// The inner class is NOT free. For a partial (subset) outer edge, containment is
// transitive, so a subset hop asserts exactly what the direct edge asserted and
// `a subset c` and `a subset b subset c` are the same world. For a COMPLETE outer
// edge the hop must ALSO be complete, which makes the intermediate a definitional
// alias of the child: `a = b + d` together with `b = c` is `a = c + d`, the very
// equation the direct edge declared, with the intermediate pinned rather than
// free. A subset hop under a complete outer edge would be a DIFFERENT and strictly
// weaker declaration -- it would replace the equation `a = c` with the mere
// containment `c inside b` and leave `a = b` unevaluable -- so no such row is
// claimed by the property.
func mtpSubdivide(direct []mtpEdge, outer, inner metering.RelationshipKind, from, via, to string) []mtpEdge {
	out := make([]mtpEdge, 0, len(direct)+1)
	for _, edge := range direct {
		if edge.parent == from && edge.child == to {
			out = append(out, mtpEdge{kind: outer, parent: from, child: via, optional: edge.optional})
			out = append(out, mtpEdge{kind: inner, parent: via, child: to})
			continue
		}
		out = append(out, edge)
	}
	return out
}

// mtpExpand replaces an OBSERVED contained node with an ABSENT one whose own
// declared complete cover carries the same quantity: the contained node keeps its
// incoming edge, loses its measure, and gains a complete cover whose members sum
// to the quantity it used to report. The physical world is unchanged -- the
// schema's own complete-coverage rule still determines the node's quantity as the
// sum of its children -- and the expanded form strictly carries MORE information,
// because it also states how the quantity splits. That asymmetry is the argument
// for the invariant: any verdict reachable in the observed form is derivable in
// the expanded form, so the expanded form must reach the same verdict about every
// ancestor and about the disjointness of every sibling.
func mtpExpand(direct []mtpEdge, contained string, kind metering.RelationshipKind, parts ...string) []mtpEdge {
	out := make([]mtpEdge, 0, len(direct)+len(parts))
	out = append(out, direct...)
	for _, part := range parts {
		out = append(out, mtpEdge{kind: kind, parent: contained, child: part})
	}
	return out
}

// mtpFlatten lifts a nested complete cover into its parent: the parent -> middle
// edge and every middle -> leaf edge are replaced by parent -> leaf edges that
// keep each leaf's optionality. A complete coverage is a statement about a
// disjoint additive sum, so the nested and the flattened declarations assert the
// same equation over the same quantities; only the level at which the
// intermediate node is named changes.
func mtpFlatten(nested []mtpEdge, parent, middle string, leaves ...string) []mtpEdge {
	lifted := make(map[string]bool, len(leaves))
	dropped := map[string]struct{}{middle: {}}
	out := make([]mtpEdge, 0, len(nested))
	for _, edge := range nested {
		if edge.parent == middle {
			dropped[edge.child] = struct{}{}
			continue
		}
		if edge.parent == parent {
			if _, gone := dropped[edge.child]; gone {
				continue
			}
		}
		out = append(out, edge)
	}
	for _, leaf := range leaves {
		for _, edge := range nested {
			if edge.parent == middle && edge.child == leaf {
				lifted[leaf] = edge.optional
			}
		}
		out = append(out, mtpEdge{kind: metering.RelationshipPartition, parent: parent, child: leaf, optional: lifted[leaf]})
	}
	return out
}

// mtpWithOptional forces the optional flag on exactly one parent -> child edge and
// clears it when optional is false, so a scenario can declare one base graph in
// both the REQUIRED and the OPTIONAL form with the flag as the only difference.
func mtpWithOptional(edges []mtpEdge, parent, child string, optional bool) []mtpEdge {
	out := make([]mtpEdge, len(edges))
	copy(out, edges)
	for i := range out {
		if out[i].parent == parent && out[i].child == child {
			out[i].optional = optional
		}
	}
	return out
}

// mtpSplitSchemas splits one relationship set across two frozen schemas, in the
// requested order. Publication requires unique schema ids, so the second schema
// carries its own; nothing in the rater reads the schema id of a relationship, so
// the split is a pure re-declaration.
func mtpSplitSchemas(first, second []metering.ComponentRelationship) []metering.ComponentSchema {
	return []metering.ComponentSchema{
		{ID: b1SchemaID, Version: "1", Relationships: first},
		{ID: b1SchemaID + "_split", Version: "1", Relationships: second},
	}
}

// ---------------------------------------------------------------------------
// PROPERTY 1 -- edge subdivision invariance.
// ---------------------------------------------------------------------------

type mtpSubdivRow struct {
	name   string
	class  string
	outer  metering.RelationshipKind
	inner  metering.RelationshipKind
	direct []mtpEdge
	ev     mtpEvidence
	// shared names the roles the two forms have in common, i.e. every role except
	// the inserted intermediate.
	shared []string
	// prices declares the reference scenario's tariff. The ancestor is left
	// UNPRICED throughout, because a priced ancestor and a priced member of its
	// own complete cover is itself an overlap conflict (see the R7 and 5B
	// vectors), which would make every comparison a comparison of two conflicts
	// instead of a comparison of the subdivision.
	prices mtpPricing
}

func mtpSubdivRows() []mtpSubdivRow {
	subset := metering.RelationshipSubset
	partition := metering.RelationshipPartition
	aggregate := metering.RelationshipAggregate
	// c, d and e carry distinct rates so a money-moving subdivision is visible.
	prices := func(rate string) mtpPricing {
		return mtpPricing{"a": "", "b": rate, "c": "3", "d": "4", "e": "5"}
	}
	return []mtpSubdivRow{
		// ---- partial containment: the intermediate carries no quantity, so the
		// whole containment statement is the transitive chain. ----
		{
			name: "subset_conserved", class: "subset", outer: subset, inner: subset,
			direct: []mtpEdge{{kind: subset, parent: "a", child: "c"}, {kind: subset, parent: "a", child: "d"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "subset_child_exceeds_ancestor", class: "subset", outer: subset, inner: subset,
			direct: []mtpEdge{{kind: subset, parent: "a", child: "c"}, {kind: subset, parent: "a", child: "d"}},
			ev:     mtpEvidence{"a": mtpOneEv, "c": mtpTwoEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "subset_parent_absent", class: "subset", outer: subset, inner: subset,
			direct: []mtpEdge{{kind: subset, parent: "a", child: "c"}, {kind: subset, parent: "a", child: "d"}},
			ev:     mtpEvidence{"c": mtpOneEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "subset_child_unavailable", class: "subset", outer: subset, inner: subset,
			direct: []mtpEdge{{kind: subset, parent: "a", child: "c"}, {kind: subset, parent: "a", child: "d"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpUnavailableEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "subset_with_paid_subset_sibling", class: "subset", outer: subset, inner: subset,
			direct: []mtpEdge{{kind: subset, parent: "a", child: "c"}, {kind: subset, parent: "a", child: "d"}, {kind: subset, parent: "a", child: "e"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv, "e": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		// ---- complete coverage: the hop is complete, so the intermediate is a
		// definitional alias of the child. ----
		{
			name: "partition_conserved", class: "partition", outer: partition, inner: partition,
			direct: []mtpEdge{{kind: partition, parent: "a", child: "c"}, {kind: partition, parent: "a", child: "d"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "partition_contradicted", class: "partition", outer: partition, inner: partition,
			direct: []mtpEdge{{kind: partition, parent: "a", child: "c"}, {kind: partition, parent: "a", child: "d"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpTwoEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "partition_contradicted_zero_parent", class: "partition", outer: partition, inner: partition,
			direct: []mtpEdge{{kind: partition, parent: "a", child: "c"}, {kind: partition, parent: "a", child: "d"}},
			ev:     mtpEvidence{"a": mtpZeroEv, "c": mtpOneEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "partition_optional_member", class: "partition-optional", outer: partition, inner: partition,
			direct: []mtpEdge{{kind: partition, parent: "a", child: "c"}, {kind: partition, parent: "a", child: "d", optional: true}},
			ev:     mtpEvidence{"a": mtpOneEv, "c": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "aggregate_conserved", class: "aggregate", outer: aggregate, inner: aggregate,
			direct: []mtpEdge{{kind: aggregate, parent: "a", child: "c"}, {kind: aggregate, parent: "a", child: "d"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "aggregate_contradicted", class: "aggregate", outer: aggregate, inner: aggregate,
			direct: []mtpEdge{{kind: aggregate, parent: "a", child: "c"}, {kind: aggregate, parent: "a", child: "d"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpTwoEv, "d": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		// A complete cover with a paid subset child: the external support decision
		// (is the subset provably disjoint from the paid partition?) is the one
		// that has to survive a subdivision of the partition member.
		{
			name: "partition_with_paid_subset", class: "partition", outer: partition, inner: partition,
			direct: []mtpEdge{{kind: partition, parent: "a", child: "c"}, {kind: partition, parent: "a", child: "d"}, {kind: subset, parent: "a", child: "e"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv, "e": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
		{
			name: "partition_with_paid_subset_contradicted", class: "partition", outer: partition, inner: partition,
			direct: []mtpEdge{{kind: partition, parent: "a", child: "c"}, {kind: partition, parent: "a", child: "d"}, {kind: subset, parent: "a", child: "e"}},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpTwoEv, "d": mtpOneEv, "e": mtpOneEv},
			shared: []string{"a", "c", "d", "e"}, prices: prices("1"),
		},
	}
}

// TestMetamorphicEdgeSubdivisionInvariance is PROPERTY 1.
//
// TRANSFORMATION. On the base graph, the direct edge `a --K--> c` is replaced by
// `a --K--> b` + `b --K--> c` with b UNOBSERVED. The base graphs cover the subset
// class, the complete (partition and aggregate) classes, the optional complete
// member, a contradicted sum, a zero parent, an unavailable member, an absent
// parent, and a complete cover with a paid subset child.
//
// INVARIANT. Inserting a node that reports nothing is a pure re-declaration: the
// region under a, the conservation claim if the outer edge is complete, and the
// disjointness obligation of a's paid subset child are all unchanged, so the
// observable classification and the money must be identical. The property is
// asserted on the full tuple, through both seams, and the intermediate is given a
// real tariff of its own so a subdivision that started to bill the intermediate
// would be caught.
//
// Before production is judged, the independent oracle is asked whether the two
// forms are the same world: it must classify every SHARED role identically in
// both. Without that check a RED could just be a mis-specified scenario.
func TestMetamorphicEdgeSubdivisionInvariance(t *testing.T) {
	t.Parallel()
	rows := mtpSubdivRows()
	tally := newMTPTally("edge_subdivision", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p1_"+row.class+"_"+row.name, "a", "b", "c", "d", "e")
		directEdges := row.direct
		subdividedEdges := mtpSubdivide(row.direct, row.outer, row.inner, "a", "b", "c")

		for _, variant := range []struct {
			label  string
			prices mtpPricing
		}{
			{label: "declared_prices", prices: row.prices},
			{label: "explicit_free", prices: mtpFree(graph)},
		} {
			t.Run(row.class+"/"+row.name+"/"+variant.label, func(t *testing.T) {
				t.Parallel()
				tag := "p1-" + row.name + "-" + variant.label
				directSchemas := mtpOneSchema(mtpRels(graph, directEdges...))
				subdividedSchemas := mtpOneSchema(mtpRels(graph, subdividedEdges...))
				mtpValidate(t, tag+"-direct", directSchemas)
				mtpValidate(t, tag+"-subdivided", subdividedSchemas)

				directOracle := mtpOracle(t, tag+"-direct-oracle", directSchemas, mtpOracleEvidence(graph, row.ev))
				subdividedOracle := mtpOracle(t, tag+"-subdivided-oracle", subdividedSchemas, mtpOracleEvidence(graph, row.ev))
				directClass := mtpOracleSharedClass(graph, directOracle, row.shared...)
				subdividedClass := mtpOracleSharedClass(graph, subdividedOracle, row.shared...)
				if directClass != subdividedClass {
					t.Errorf("%s: the independent oracle does NOT agree that the two declarations are the same world, so this row is not a world-preserving transformation and the property does not apply to it\n  direct   : %s\n  subdivided: %s",
						tag, directClass, subdividedClass)
				}

				directOutcomes := mtpRateForm(t, tag+"-direct", mtpForm{
					schemas: directSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.ev.obs(t, graph, tag+"-direct")},
				})
				subdividedOutcomes := mtpRateForm(t, tag+"-subdivided", mtpForm{
					schemas: subdividedSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.ev.obs(t, graph, tag+"-sub")},
				})
				tally.mtpNoteSeamSkew(tag+"-direct", directOutcomes)
				tally.mtpNoteSeamSkew(tag+"-subdivided", subdividedOutcomes)

				for i, seam := range review5beSeams() {
					tally.countCase()
					mtpAssertSameTuple(t, tag+"/"+seam.name, directOutcomes[i], subdividedOutcomes[i])
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// PROPERTY 2 -- complete-cover expansion invariance.
// ---------------------------------------------------------------------------

type mtpExpandRow struct {
	name string
	// direct declares the observed form; expanded is derived from it by
	// mtpExpand, so the two forms differ by exactly the described transformation.
	direct []mtpEdge
	// ev is the observed form's evidence; evExpanded replaces the contained node's
	// report with the declared cover whose members sum to the same quantity.
	ev         mtpEvidence
	evExpanded mtpEvidence
	// parts are the roles of the expansion children, in declaration order.
	parts []string
	inner metering.RelationshipKind
	// prices leaves the ancestor UNPRICED, so the children are the surviving money
	// in both forms and the substituted amount is directly comparable.
	prices mtpPricing
	// dropDirect and dropExpanded name the identities the transformation itself
	// substitutes.
	dropDirect   []string
	dropExpanded []string
	// shared names the roles the two forms have in common.
	shared []string
}

func mtpExpandRows() []mtpExpandRow {
	partition := metering.RelationshipPartition
	aggregate := metering.RelationshipAggregate
	subset := metering.RelationshipSubset
	// The covered node and its expansion children share one rate, so the
	// substituted amount is a like-for-like comparison; the sibling is distinct so
	// a moved sibling charge would show up.
	coverPrices := mtpPricing{"a": "", "b": "1", "c1": "1", "c2": "1", "c3": "1", "d": "3", "e": "5", "z": ""}
	return []mtpExpandRow{
		{
			// A is a complete parent whose cover is {B, D} and is conserved.
			// Expanding B into its own complete cover moves B's two units onto C1
			// and C2 and changes nothing physical.
			name: "complete_cover_member",
			direct: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "d"},
			},
			ev:           mtpEvidence{"a": mtpExact("2"), "b": mtpExact("2"), "d": mtpZeroEv},
			evExpanded:   mtpEvidence{"a": mtpExact("2"), "d": mtpZeroEv, "c1": mtpOneEv, "c2": mtpOneEv},
			parts:        []string{"c1", "c2"},
			inner:        partition,
			prices:       coverPrices,
			dropDirect:   []string{"b"},
			dropExpanded: []string{"c1", "c2"},
			shared:       []string{"a", "d", "e", "z"},
		},
		{
			name: "complete_cover_member_aggregate",
			direct: []mtpEdge{
				{kind: aggregate, parent: "a", child: "b"},
				{kind: aggregate, parent: "a", child: "d"},
			},
			ev:           mtpEvidence{"a": mtpExact("2"), "b": mtpExact("2"), "d": mtpZeroEv},
			evExpanded:   mtpEvidence{"a": mtpExact("2"), "d": mtpZeroEv, "c1": mtpOneEv, "c2": mtpOneEv},
			parts:        []string{"c1", "c2"},
			inner:        aggregate,
			prices:       coverPrices,
			dropDirect:   []string{"b"},
			dropExpanded: []string{"c1", "c2"},
			shared:       []string{"a", "d", "e", "z"},
		},
		{
			// The ancestor contains B only PARTIALLY. No cover is claimed over A,
			// so only the ancestor's own quantity constraints are at stake.
			name: "partial_containment_member",
			direct: []mtpEdge{
				{kind: subset, parent: "a", child: "b"},
				{kind: subset, parent: "a", child: "d"},
			},
			ev:           mtpEvidence{"a": mtpExact("2"), "b": mtpExact("2"), "d": mtpZeroEv},
			evExpanded:   mtpEvidence{"a": mtpExact("2"), "d": mtpZeroEv, "c1": mtpOneEv, "c2": mtpOneEv},
			parts:        []string{"c1", "c2"},
			inner:        partition,
			prices:       coverPrices,
			dropDirect:   []string{"b"},
			dropExpanded: []string{"c1", "c2"},
			shared:       []string{"a", "d", "e", "z"},
		},
		{
			// Three-way expansion: the covered node splits into three shares, so the
			// transformation is not a one-to-one renaming of identities.
			name: "complete_cover_member_three_way",
			direct: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "d"},
			},
			ev:           mtpEvidence{"a": mtpExact("2"), "b": mtpExact("2"), "d": mtpZeroEv},
			evExpanded:   mtpEvidence{"a": mtpExact("2"), "d": mtpZeroEv, "c1": mtpOneEv, "c2": mtpZeroEv, "c3": mtpOneEv},
			parts:        []string{"c1", "c2", "c3"},
			inner:        partition,
			prices:       coverPrices,
			dropDirect:   []string{"b"},
			dropExpanded: []string{"c1", "c2", "c3"},
			shared:       []string{"a", "d", "e", "z"},
		},
		{
			// A complete cover with a paid subset child, so the expansion has to
			// preserve the DISJOINTNESS decision and not only the arithmetic.
			name: "complete_cover_with_paid_subset",
			direct: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "d"},
				{kind: subset, parent: "a", child: "e"},
			},
			ev:           mtpEvidence{"a": mtpExact("2"), "b": mtpExact("2"), "d": mtpZeroEv, "e": mtpOneEv},
			evExpanded:   mtpEvidence{"a": mtpExact("2"), "d": mtpZeroEv, "e": mtpOneEv, "c1": mtpOneEv, "c2": mtpOneEv},
			parts:        []string{"c1", "c2"},
			inner:        partition,
			prices:       coverPrices,
			dropDirect:   []string{"b"},
			dropExpanded: []string{"c1", "c2"},
			shared:       []string{"a", "d", "e", "z"},
		},
		{
			// The ancestor is not observed at all, so only the declared structure
			// and the surviving sibling's money are at stake.
			name: "unobserved_ancestor",
			direct: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "d"},
			},
			ev:           mtpEvidence{"b": mtpExact("2"), "d": mtpZeroEv},
			evExpanded:   mtpEvidence{"d": mtpZeroEv, "c1": mtpOneEv, "c2": mtpOneEv},
			parts:        []string{"c1", "c2"},
			inner:        partition,
			prices:       coverPrices,
			dropDirect:   []string{"b"},
			dropExpanded: []string{"c1", "c2"},
			shared:       []string{"a", "d", "e", "z"},
		},
		{
			// A one-member complete cover: the degenerate expansion, where the
			// ancestor's whole cover is the expanded node.
			name: "sole_cover_member",
			direct: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
			},
			ev:           mtpEvidence{"a": mtpExact("2"), "b": mtpExact("2")},
			evExpanded:   mtpEvidence{"a": mtpExact("2"), "c1": mtpOneEv, "c2": mtpOneEv},
			parts:        []string{"c1", "c2"},
			inner:        partition,
			prices:       coverPrices,
			dropDirect:   []string{"b"},
			dropExpanded: []string{"c1", "c2"},
			shared:       []string{"a", "d", "e", "z"},
		},
		{
			// A grandparent: the expanded node is itself a member of a larger
			// cover, so a lost recursion would be visible two levels up.
			name: "nested_ancestors",
			direct: []mtpEdge{
				{kind: partition, parent: "z", child: "a"},
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "d"},
			},
			ev:           mtpEvidence{"z": mtpExact("2"), "a": mtpExact("2"), "b": mtpExact("2"), "d": mtpZeroEv},
			evExpanded:   mtpEvidence{"z": mtpExact("2"), "a": mtpExact("2"), "d": mtpZeroEv, "c1": mtpOneEv, "c2": mtpOneEv},
			parts:        []string{"c1", "c2"},
			inner:        partition,
			prices:       coverPrices,
			dropDirect:   []string{"b"},
			dropExpanded: []string{"c1", "c2"},
			shared:       []string{"a", "d", "e", "z"},
		},
	}
}

// TestMetamorphicCompleteCoverExpansionInvariance is PROPERTY 2, the one that
// catches the "each child is inside the ancestor but their SUM is not" class.
//
// TRANSFORMATION. A contained node B observed as B = q is replaced by B ABSENT
// plus a declared complete cover B -> {C1..Cn} with SUM(Ci) = q. The physical
// world is unchanged: the schema's own complete-coverage rule still determines
// B's quantity as the sum of its children, and the expanded form strictly carries
// MORE information, because it also states how q splits. That asymmetry is the
// argument for the invariant: any verdict reachable in the observed form is
// derivable in the expanded form, so the expanded form must reach the same
// verdict about every ancestor and about the disjointness of every sibling.
//
// INVARIANT. Outside the substituted identities the emitted lines, the typed
// error class, the completeness and the payable total are identical, AND the
// substituted identities carry exactly the same amount, so the expansion neither
// loses the covered node's money nor pays it twice.
//
// The oracle is asked, first, whether both forms are structurally sound. That is
// what makes a RED here attributable: if the model considers the expanded form
// sound and production does not, the defect is in production.
func TestMetamorphicCompleteCoverExpansionInvariance(t *testing.T) {
	t.Parallel()
	rows := mtpExpandRows()
	tally := newMTPTally("complete_cover_expansion", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p2_"+row.name, "a", "b", "d", "e", "z", "c1", "c2", "c3")
		directEdges := row.direct
		expandedEdges := mtpExpand(row.direct, "b", row.inner, row.parts...)
		dropDirect := make([]string, 0, len(row.dropDirect))
		for _, role := range row.dropDirect {
			dropDirect = append(dropDirect, graph.canon(role))
		}
		dropExpanded := make([]string, 0, len(row.dropExpanded))
		for _, role := range row.dropExpanded {
			dropExpanded = append(dropExpanded, graph.canon(role))
		}

		for _, variant := range []struct {
			label  string
			prices mtpPricing
		}{
			{label: "declared_prices", prices: row.prices},
			{label: "explicit_free", prices: mtpFree(graph)},
		} {
			t.Run(row.name+"/"+variant.label, func(t *testing.T) {
				t.Parallel()
				tag := "p2-" + row.name + "-" + variant.label
				directSchemas := mtpOneSchema(mtpRels(graph, directEdges...))
				expandedSchemas := mtpOneSchema(mtpRels(graph, expandedEdges...))
				mtpValidate(t, tag+"-direct", directSchemas)
				mtpValidate(t, tag+"-expanded", expandedSchemas)

				// The model must consider BOTH forms structurally sound, otherwise
				// the comparison below would be between two rejections for
				// unrelated reasons and the property would not apply.
				directOracle := mtpOracle(t, tag+"-direct-oracle", directSchemas, mtpOracleEvidence(graph, row.ev))
				expandedOracle := mtpOracle(t, tag+"-expanded-oracle", expandedSchemas, mtpOracleEvidence(graph, row.evExpanded))
				if !mtpOracleClean(directOracle) {
					t.Errorf("%s: the observed form is already structurally unsound by the model (contradicted=%v incomplete=%v), so the expansion comparison would be vacuous",
						tag, directOracle.Contradicted, directOracle.Incomplete)
				}
				if !mtpOracleClean(expandedOracle) {
					t.Errorf("%s: the model considers the EXPANDED form structurally unsound (contradicted=%v incomplete=%v) even though its children sum exactly to the covered node's quantity, so the two forms are NOT the same world and the property does not apply to this row\n  oracle nodes: %s",
						tag, expandedOracle.Contradicted, expandedOracle.Incomplete,
						mtpOracleSharedClass(graph, expandedOracle, row.shared...))
				}

				directOutcomes := mtpRateForm(t, tag+"-direct", mtpForm{
					schemas: directSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.ev.obs(t, graph, tag+"-direct")},
				})
				expandedOutcomes := mtpRateForm(t, tag+"-expanded", mtpForm{
					schemas: expandedSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.evExpanded.obs(t, graph, tag+"-expanded")},
				})
				tally.mtpNoteSeamSkew(tag+"-direct", directOutcomes)
				tally.mtpNoteSeamSkew(tag+"-expanded", expandedOutcomes)

				for i, seam := range review5beSeams() {
					tally.countCase()
					mtpAssertSubstituted(t, tag+"/"+seam.name, directOutcomes[i], expandedOutcomes[i], dropDirect, dropExpanded)
				}
			})
		}
	}
}

// TestMetamorphicCompleteCoverExpansionSumViolationIsCaught is the ONE-SIDED
// control for property 2, and it is the clause with teeth: an expansion whose
// children do NOT sum to the covered node's quantity describes a DIFFERENT world,
// and the system must diagnose it.
//
// TRANSFORMATION. A = q with A --subset--> B. The observed form reports B = q and is
// consistent. The expanded form removes B's report and declares B -> {C1, C2} with
// C1 + C2 > q, so the intermediate's own quantity is larger than the ancestor's.
// Each child individually is still inside the ancestor -- that is the trap: both
// C1 <= A and C2 <= A hold -- but their SUM is not, so the ancestor's containment
// is impossible evidence.
//
// INVARIANT. The observed form rates completely, the expanded form is rejected
// with a typed QUANTITY contradiction on both seams, and the model independently
// reports the same contradiction on the expanded form and none on the observed one.
// The ancestor is declared EXPLICIT FREE so it never becomes payable: a payable
// ancestor together with a payable member of its own containment would be an
// overlap conflict, and the conflict outranks the quantity verdict in the single
// reported error, which would hide exactly the diagnosis under test.
func TestMetamorphicCompleteCoverExpansionSumViolationIsCaught(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name     string
		observed mtpEvidence
		expanded mtpEvidence
		prices   mtpPricing
	}{
		{
			// A = 1, and both children are individually inside it (2 <= 1 is
			// false, so use the row below for the sharpest version); here the
			// children simply sum above the ancestor.
			name:     "children_sum_above_ancestor",
			observed: mtpEvidence{"a": mtpOneEv, "b": mtpOneEv},
			expanded: mtpEvidence{"a": mtpOneEv, "c1": mtpTwoEv, "c2": mtpTwoEv},
			prices:   mtpPricing{"a": "0", "b": "1", "c1": "1", "c2": "1"},
		},
		{
			name:     "zero_ancestor",
			observed: mtpEvidence{"a": mtpZeroEv, "b": mtpZeroEv},
			expanded: mtpEvidence{"a": mtpZeroEv, "c1": mtpOneEv, "c2": mtpOneEv},
			prices:   mtpPricing{"a": "0", "b": "1", "c1": "1", "c2": "1"},
		},
		{
			// The sharpest shape: A = 2, C1 = 2 and C2 = 1. Each child is inside
			// the ancestor on its own, and only their sum 3 is not.
			name:     "each_child_inside_but_sum_is_not",
			observed: mtpEvidence{"a": mtpExact("2"), "b": mtpExact("2")},
			expanded: mtpEvidence{"a": mtpExact("2"), "c1": mtpTwoEv, "c2": mtpOneEv},
			prices:   mtpPricing{"a": "0", "b": "1", "c1": "1", "c2": "1"},
		},
	}
	tally := newMTPTally("complete_cover_expansion_sum_violation", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p2v_"+row.name, "a", "b", "c1", "c2")
		observedEdges := []mtpEdge{{kind: metering.RelationshipSubset, parent: "a", child: "b"}}
		expandedEdges := mtpExpand(observedEdges, "b", metering.RelationshipPartition, "c1", "c2")

		for _, variant := range []struct {
			label  string
			prices mtpPricing
		}{
			{label: "declared_prices", prices: row.prices},
			{label: "explicit_free", prices: mtpFree(graph)},
		} {
			t.Run(row.name+"/"+variant.label, func(t *testing.T) {
				t.Parallel()
				tag := "p2v-" + row.name + "-" + variant.label
				observedSchemas := mtpOneSchema(mtpRels(graph, observedEdges...))
				expandedSchemas := mtpOneSchema(mtpRels(graph, expandedEdges...))
				mtpValidate(t, tag+"-expanded", expandedSchemas)

				observedOracle := mtpOracle(t, tag+"-observed-oracle", observedSchemas, mtpOracleEvidence(graph, row.observed))
				if len(observedOracle.Contradicted) != 0 {
					t.Errorf("%s: the observed form is contradicted by the model (%v) although the intermediate is reported at the ancestor's own quantity",
						tag, observedOracle.Contradicted)
				}
				expandedOracle := mtpOracle(t, tag+"-expanded-oracle", expandedSchemas, mtpOracleEvidence(graph, row.expanded))
				if len(expandedOracle.Contradicted) == 0 {
					t.Errorf("%s: the model does not report a contradiction for an expanded cover whose children sum above the ancestor, so this scenario no longer exercises the sum bound",
						tag)
				}

				observedOutcomes := mtpRateForm(t, tag+"-observed", mtpForm{
					schemas: observedSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.observed.obs(t, graph, tag+"-observed")},
				})
				expandedOutcomes := mtpRateForm(t, tag+"-expanded", mtpForm{
					schemas: expandedSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.expanded.obs(t, graph, tag+"-expanded")},
				})
				tally.mtpNoteSeamSkew(tag+"-observed", observedOutcomes)
				tally.mtpNoteSeamSkew(tag+"-expanded", expandedOutcomes)

				for i, seam := range review5beSeams() {
					tally.countCase()
					label := tag + "/" + seam.name
					observed := observedOutcomes[i]
					if observed.completeness != economics.CompletenessComplete {
						t.Errorf("%s: the observed form (A subset B with B reported at A's own quantity) must rate completely, got %s", label, observed.tuple())
					}
					expanded := expandedOutcomes[i]
					if expanded.completeness == economics.CompletenessComplete {
						t.Errorf("%s: an expanded cover whose children sum ABOVE the ancestor certified complete money: each child is individually inside the ancestor, so only the SUM constraint catches this; %s model_contradicted=%v",
							label, expanded.tuple(), expandedOracle.Contradicted)
					}
					if !smIsQuantityContradiction(expanded.errClass) {
						t.Errorf("%s: an expanded cover whose children sum above the ancestor must be a typed QUANTITY contradiction, got class %q (%v); %s",
							label, expanded.errClass, expanded.err, expanded.tuple())
					}
				}
			})
		}
	}
}

// TestMetamorphicCompleteCoverExpansionPresentIntermediateControl is the
// ISOLATING control for property 2. It establishes that production DOES resolve a
// recursively covered intermediate when that intermediate is PRESENT but
// unpriced, so a property-2 RED cannot be dismissed as "a recursive cover is
// simply out of scope".
//
// TRANSFORMATION. Both forms declare the same two-level cover A -> {B, D} and
// B -> {C1, C2}. The only difference is where B's quantity is billed: in the
// direct form B owns a rate and C1/C2 are absent, so B pays for itself; in the
// recursive form B owns no rate, its own cover carries the quantity, and B's share
// is billed by C1 + C2 at the same per-unit rate.
//
// INVARIANT. Both forms rate completely and settle the same total, with the
// recursive form billing the leaves and the direct form billing the intermediate.
func TestMetamorphicCompleteCoverExpansionPresentIntermediateControl(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name     string
		outer    metering.RelationshipKind
		observed mtpEvidence
		expanded mtpEvidence
		leaves   []string
		grand    bool
	}{
		{
			name:     "partition_two_levels",
			outer:    metering.RelationshipPartition,
			observed: mtpEvidence{"a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv},
			expanded: mtpEvidence{"a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv, "c1": mtpOneEv, "c2": mtpOneEv},
			leaves:   []string{"c1", "c2"},
		},
		{
			name:     "aggregate_two_levels",
			outer:    metering.RelationshipAggregate,
			observed: mtpEvidence{"a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv},
			expanded: mtpEvidence{"a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv, "c1": mtpOneEv, "c2": mtpOneEv},
			leaves:   []string{"c1", "c2"},
		},
		{
			name:     "three_way_two_levels",
			outer:    metering.RelationshipPartition,
			observed: mtpEvidence{"a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv},
			expanded: mtpEvidence{"a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv, "c1": mtpOneEv, "c2": mtpZeroEv, "c3": mtpOneEv},
			leaves:   []string{"c1", "c2", "c3"},
		},
		{
			name:     "grandparent_cover",
			outer:    metering.RelationshipPartition,
			observed: mtpEvidence{"z": mtpExact("3"), "a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv},
			expanded: mtpEvidence{"z": mtpExact("3"), "a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv, "c1": mtpOneEv, "c2": mtpOneEv},
			leaves:   []string{"c1", "c2"},
			grand:    true,
		},
	}
	tally := newMTPTally("complete_cover_expansion_present_control", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p2c_"+row.name, "a", "b", "c1", "c2", "c3", "d", "z")
		directEdges := []mtpEdge{
			{kind: row.outer, parent: "a", child: "b"},
			{kind: row.outer, parent: "a", child: "d"},
		}
		if row.grand {
			directEdges = append([]mtpEdge{{kind: row.outer, parent: "z", child: "a"}}, directEdges...)
		}
		recursiveEdges := append([]mtpEdge{}, directEdges...)
		for _, leaf := range row.leaves {
			recursiveEdges = append(recursiveEdges, mtpEdge{kind: metering.RelationshipPartition, parent: "b", child: leaf})
		}
		// The recursive form gives the intermediate no rate: its share is billed by
		// its own children at the same per-unit rate the intermediate would have
		// charged, so the two forms settle the same amount.
		directPrices := mtpPricing{"a": "", "b": "1", "c1": "", "c2": "", "c3": "", "d": "3", "z": ""}
		recursivePrices := mtpPricing{"a": "", "b": "", "c1": "1", "c2": "1", "c3": "1", "d": "3", "z": ""}
		dropDirect := []string{graph.canon("b")}
		dropRecursive := make([]string, 0, len(row.leaves))
		for _, leaf := range row.leaves {
			dropRecursive = append(dropRecursive, graph.canon(leaf))
		}

		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			tag := "p2c-" + row.name
			directSchemas := mtpOneSchema(mtpRels(graph, directEdges...))
			recursiveSchemas := mtpOneSchema(mtpRels(graph, recursiveEdges...))
			mtpValidate(t, tag+"-direct", directSchemas)
			mtpValidate(t, tag+"-recursive", recursiveSchemas)

			directOutcomes := mtpRateForm(t, tag+"-direct", mtpForm{
				schemas: directSchemas,
				rules:   directPrices.rules(t, graph, tag),
				obs:     []metering.Observation{row.observed.obs(t, graph, tag+"-direct")},
			})
			recursiveOutcomes := mtpRateForm(t, tag+"-recursive", mtpForm{
				schemas: recursiveSchemas,
				rules:   recursivePrices.rules(t, graph, tag),
				obs:     []metering.Observation{row.expanded.obs(t, graph, tag+"-recursive")},
			})
			tally.mtpNoteSeamSkew(tag+"-direct", directOutcomes)
			tally.mtpNoteSeamSkew(tag+"-recursive", recursiveOutcomes)

			for i, seam := range review5beSeams() {
				tally.countCase()
				label := tag + "/" + seam.name
				direct := directOutcomes[i]
				recursive := recursiveOutcomes[i]
				if direct.completeness != economics.CompletenessComplete {
					t.Errorf("%s: the directly rated form must rate completely, got %s", label, direct.tuple())
				}
				if recursive.completeness != economics.CompletenessComplete {
					t.Errorf("%s: production does NOT resolve a recursively covered PRESENT intermediate, so a property-2 result cannot be attributed to a missing capability: %s",
						label, recursive.tuple())
					continue
				}
				mtpAssertSubstituted(t, label, direct, recursive, dropDirect, dropRecursive)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PROPERTY 3 -- flattening invariance.
// ---------------------------------------------------------------------------

type mtpFlattenRow struct {
	name   string
	nested []mtpEdge
	parent string
	middle string
	leaves []string
	ev     mtpEvidence
}

func mtpFlattenRows() []mtpFlattenRow {
	partition := metering.RelationshipPartition
	return []mtpFlattenRow{
		{
			name: "leaves_priced_conserved",
			nested: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
			},
			parent: "a", middle: "b", leaves: []string{"x", "y"},
			ev: mtpEvidence{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpOneEv, "y": mtpOneEv},
		},
		{
			name: "outer_sum_contradicted",
			nested: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
			},
			parent: "a", middle: "b", leaves: []string{"x", "y"},
			ev: mtpEvidence{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("3"), "x": mtpOneEv, "y": mtpOneEv},
		},
		{
			name: "inner_sum_contradicted",
			nested: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
			},
			parent: "a", middle: "b", leaves: []string{"x", "y"},
			ev: mtpEvidence{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpTwoEv, "y": mtpOneEv},
		},
		{
			name: "optional_sibling",
			nested: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c", optional: true},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
			},
			parent: "a", middle: "b", leaves: []string{"x", "y"},
			ev: mtpEvidence{"a": mtpExact("2"), "b": mtpExact("2"), "x": mtpOneEv, "y": mtpOneEv},
		},
		{
			name: "optional_leaf",
			nested: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y", optional: true},
			},
			parent: "a", middle: "b", leaves: []string{"x", "y"},
			ev: mtpEvidence{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpTwoEv},
		},
		{
			name: "missing_required_leaf",
			nested: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
			},
			parent: "a", middle: "b", leaves: []string{"x", "y"},
			ev: mtpEvidence{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpTwoEv},
		},
		{
			name: "paid_subset_outside_the_cover",
			nested: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
				{kind: metering.RelationshipSubset, parent: "a", child: "e"},
			},
			parent: "a", middle: "b", leaves: []string{"x", "y"},
			ev: mtpEvidence{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpOneEv, "y": mtpOneEv, "e": mtpOneEv},
		},
	}
}

// TestMetamorphicFlatteningInvariance is PROPERTY 3.
//
// TRANSFORMATION. A nested complete cover A -> {B, C} with B -> {X, Y} is flattened
// into the leaf representation A -> {X, Y, C}. A complete coverage is a statement
// about a disjoint additive sum, so the two declarations assert the same equation
// over the same quantities, at the same total, with the same disjointness; only
// the level at which the intermediate node is named changes.
//
// INVARIANT. The typed error class, the completeness, the payable total, and every
// emitted identity other than the intermediate's must be identical, and the
// intermediate's own money must be zero in both forms. The base graphs cover the
// fully conserved case, a contradicted outer sum, a contradicted inner sum, an
// optional sibling, an optional leaf, a missing required leaf, and a paid subset
// child whose disjointness from the cover is the external support decision that
// must survive.
//
// The oracle is asked first whether the two declarations are the same world, and
// the property is only applied where they are.
func TestMetamorphicFlatteningInvariance(t *testing.T) {
	t.Parallel()
	rows := mtpFlattenRows()
	tally := newMTPTally("flattening", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p3_"+row.name, "a", "b", "c", "x", "y", "e")
		nestedEdges := row.nested
		flatEdges := mtpFlatten(row.nested, row.parent, row.middle, row.leaves...)
		// The flattening acts on exactly two identities: the intermediate it
		// removes, and the parent whose declared cover it rewrites. The parent may
		// legitimately gain or lose its own unpriced line depending on whether the
		// outer cover still resolves, and the intermediate is gone from the schema
		// entirely, so both are substituted. Every OTHER identity and every amount
		// must be identical.
		dropped := []string{graph.canon(row.parent), graph.canon(row.middle)}

		for _, variant := range []struct {
			label  string
			prices mtpPricing
		}{
			// Neither ancestor carries a positive rate, so the leaves are the
			// surviving money in both forms. A rated ancestor together with a rated
			// member of its own complete cover is itself an overlap conflict, which
			// would make every comparison a comparison of two conflicts. The
			// intermediate is EXPLICIT FREE rather than unrated: the flattened form
			// no longer declares it inside anything, so a rule-less intermediate
			// would become an unaccounted orphan and the flattening would change the
			// world by dropping a component the provider did report.
			{label: "leaves_priced", prices: mtpPricing{"a": "", "b": "0", "c": "3", "x": "1", "y": "2", "e": "5"}},
			{label: "explicit_free", prices: mtpFree(graph)},
		} {
			t.Run(row.name+"/"+variant.label, func(t *testing.T) {
				t.Parallel()
				tag := "p3-" + row.name + "-" + variant.label
				nestedSchemas := mtpOneSchema(mtpRels(graph, nestedEdges...))
				flatSchemas := mtpOneSchema(mtpRels(graph, flatEdges...))
				mtpValidate(t, tag+"-nested", nestedSchemas)
				mtpValidate(t, tag+"-flat", flatSchemas)

				// The two node universes differ by exactly the intermediate, so the
				// comparison is over the leaves the two forms share plus the model's
				// two set-level verdicts. WHICH node carries a verdict may move when
				// the intermediate disappears; whether a verdict exists at all may
				// not.
				leafRoles := []string{"c", "x", "y", "e"}
				nestedOracle := mtpOracle(t, tag+"-nested-oracle", nestedSchemas, mtpOracleEvidence(graph, row.ev))
				flatOracle := mtpOracle(t, tag+"-flat-oracle", flatSchemas, mtpOracleEvidence(graph, row.ev))
				nestedVerdict := mtpOracleVerdicts(nestedOracle)
				flatVerdict := mtpOracleVerdicts(flatOracle)
				nestedLeaves := mtpOracleSharedClass(graph, nestedOracle, leafRoles...)
				flatLeaves := mtpOracleSharedClass(graph, flatOracle, leafRoles...)
				if nestedVerdict != flatVerdict || nestedLeaves != flatLeaves {
					t.Errorf("%s: the model does NOT agree that the nested and the flattened declarations assert the same equations over the same quantities, so the property does not apply to this row\n  nested: %s | %s\n  flat  : %s | %s",
						tag, nestedVerdict, nestedLeaves, flatVerdict, flatLeaves)
				}

				nestedOutcomes := mtpRateForm(t, tag+"-nested", mtpForm{
					schemas: nestedSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.ev.obs(t, graph, tag+"-nested")},
				})
				flatOutcomes := mtpRateForm(t, tag+"-flat", mtpForm{
					schemas: flatSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.ev.obs(t, graph, tag+"-flat")},
				})
				tally.mtpNoteSeamSkew(tag+"-nested", nestedOutcomes)
				tally.mtpNoteSeamSkew(tag+"-flat", flatOutcomes)

				for i, seam := range review5beSeams() {
					tally.countCase()
					mtpAssertSubstituted(t, tag+"/"+seam.name, nestedOutcomes[i], flatOutcomes[i], dropped, dropped)
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// PROPERTY 4 -- declaration-order invariance.
// ---------------------------------------------------------------------------

type mtpOrderRow struct {
	name  string
	edges []mtpEdge
	evs   []mtpEvidence
	// prices leaves the outer ancestors unpriced so the leaves are the money; a
	// rated ancestor together with a rated cover member is an overlap conflict and
	// every order would then produce the same conflict for an uninteresting
	// reason.
	prices mtpPricing
}

func mtpOrderRows() []mtpOrderRow {
	partition := metering.RelationshipPartition
	subset := metering.RelationshipSubset
	prices := mtpPricing{"a": "", "b": "", "c": "3", "d": "4", "e": "5", "x": "1", "y": "2", "z": ""}
	return []mtpOrderRow{
		{
			name: "complete_cover",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
			},
			evs: []mtpEvidence{
				{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv},
				{"a": mtpExact("2"), "c": mtpTwoEv, "d": mtpZeroEv},
				{"a": mtpExact("2"), "c": mtpOneEv},
			},
			prices: prices,
		},
		{
			name: "optional_member",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d", optional: true},
			},
			evs: []mtpEvidence{
				{"a": mtpOneEv, "c": mtpOneEv},
				{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv},
			},
			prices: prices,
		},
		{
			name: "nested_cover_with_paid_subset",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
				{kind: subset, parent: "a", child: "e"},
			},
			evs: []mtpEvidence{
				{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpOneEv, "y": mtpOneEv, "e": mtpOneEv},
				{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpTwoEv, "y": mtpOneEv},
			},
			prices: prices,
		},
		{
			name: "shared_complete_child",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "z", child: "c"},
			},
			evs: []mtpEvidence{
				{"a": mtpOneEv, "z": mtpOneEv, "c": mtpOneEv},
				{"a": mtpExact("2"), "z": mtpOneEv, "c": mtpOneEv},
			},
			prices: prices,
		},
		{
			name: "subset_chain_with_unavailable_leaf",
			edges: []mtpEdge{
				{kind: subset, parent: "a", child: "b"},
				{kind: subset, parent: "b", child: "c"},
				{kind: subset, parent: "a", child: "d"},
			},
			evs: []mtpEvidence{
				{"a": mtpExact("2"), "b": mtpOneEv, "c": mtpUnavailableEv, "d": mtpOneEv},
				{"a": mtpOneEv, "b": mtpOneEv, "c": mtpTwoEv, "d": mtpOneEv},
			},
			prices: prices,
		},
		{
			name: "expansion_shape",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "d"},
				{kind: partition, parent: "b", child: "c1"},
				{kind: partition, parent: "b", child: "c2"},
			},
			evs: []mtpEvidence{
				{"a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv, "c1": mtpOneEv, "c2": mtpOneEv},
				{"a": mtpExact("3"), "b": mtpExact("2"), "d": mtpOneEv},
			},
			prices: mtpPricing{"a": "", "b": "1", "c1": "1", "c2": "1", "d": "3", "e": "5", "x": "1", "y": "2", "z": ""},
		},
	}
}

// mtpOrderSchemas renders one base graph's relationships in six declaration
// orders. None of them changes the declared edge set, so all six must produce the
// identical observable outcome: every class and set production builds over a
// frozen schema is required to be order-independent, and every place where it is
// not is a decision that depends on a publishing accident rather than on the
// world.
func mtpOrderSchemas(rels []metering.ComponentRelationship) [][]metering.ComponentSchema {
	copied := append([]metering.ComponentRelationship{}, rels...)
	half := len(rels) / 2
	head := append([]metering.ComponentRelationship{}, rels[:half]...)
	tail := append([]metering.ComponentRelationship{}, rels[half:]...)
	emptySchema := metering.ComponentSchema{ID: b1SchemaID + "_empty", Version: "1"}
	return [][]metering.ComponentSchema{
		mtpOneSchema(copied),
		mtpOneSchema(smReverse(rels)),
		{mtpOneSchema(copied)[0], emptySchema},
		{emptySchema, mtpOneSchema(copied)[0]},
		mtpSplitSchemas(head, tail),
		mtpSplitSchemas(tail, head),
	}
}

var mtpOrderNames = []string{"as_declared", "reversed", "empty_schema_appended", "empty_schema_prepended", "split_forward", "split_reversed"}

// TestMetamorphicDeclarationOrderInvariance is PROPERTY 4.
//
// TRANSFORMATION. The relationship set is re-declared in six orders: as written,
// reversed, with an empty schema appended, with an empty schema prepended, and
// split across two frozen schemas in both split orders. The declared edge SET is
// identical in all six, so the frozen content differs only in serialisation order
// and in the number of schemas it is spread across.
//
// INVARIANT. The typed error class, the completeness, the sorted emitted component
// identities and the payable total are identical across all six orders, on both
// seams, for every evidence assignment of every base graph.
func TestMetamorphicDeclarationOrderInvariance(t *testing.T) {
	t.Parallel()
	rows := mtpOrderRows()
	evidenceAssignments := 0
	for _, row := range rows {
		evidenceAssignments += len(row.evs)
	}
	tally := newMTPTally("declaration_order", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p4_"+row.name, "a", "b", "c", "c1", "c2", "d", "e", "x", "y", "z")
		rels := mtpRels(graph, row.edges...)
		schemas := mtpOrderSchemas(rels)
		for i, ordered := range schemas {
			mtpValidate(t, fmt.Sprintf("p4-%s/%s", row.name, mtpOrderNames[i]), ordered)
		}
		for _, ev := range row.evs {
			t.Run(fmt.Sprintf("%s/%s", row.name, smEvName([3]smEv{ev["a"].state, ev["c"].state, ev["d"].state})), func(t *testing.T) {
				t.Parallel()
				tag := "p4-" + row.name + "-" + strings.Join(mtpSortedEvidence(ev), "")
				// The SAME evidence object for every declaration order, so the only
				// variable is the order itself.
				obs := ev.obs(t, graph, tag)
				var reference []mtpOutcome
				for i, ordered := range schemas {
					outcomes := mtpRateForm(t, fmt.Sprintf("%s-%s", tag, mtpOrderNames[i]), mtpForm{
						schemas: ordered,
						rules:   row.prices.rules(t, graph, tag),
						obs:     []metering.Observation{obs},
					})
					tally.mtpNoteSeamSkew(tag+"-"+mtpOrderNames[i], outcomes)
					if i == 0 {
						reference = outcomes
						continue
					}
					for seamIndex, seam := range review5beSeams() {
						tally.countCase()
						mtpAssertSameTuple(t, tag+"/"+seam.name+"/"+mtpOrderNames[i], reference[seamIndex], outcomes[seamIndex])
					}
				}
			})
		}
	}
}

func mtpSortedEvidence(ev mtpEvidence) []string {
	out := make([]string, 0, len(ev))
	for _, role := range mtpSortedEvidenceRoles(ev) {
		out = append(out, role+"="+ev[role].quantity+mtpEvStateSuffix(ev[role].state))
	}
	return out
}

func mtpSortedEvidenceRoles(ev mtpEvidence) []string {
	roles := make([]string, 0, len(ev))
	for role := range ev {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

func mtpEvStateSuffix(state smEv) string {
	switch state {
	case smAbsent:
		return "!"
	case smUnavailable:
		return "?"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// PROPERTY 5 -- pricing metamorphism.
// ---------------------------------------------------------------------------

// TestMetamorphicPricingMetamorphism is PROPERTY 5.
//
// TRANSFORMATION. The SAME declared graph and the SAME evidence are rated three
// times, under three tariffs that differ ONLY in whether a role carries a positive
// rate, an explicit-free rate, or no rule at all.
//
// INVARIANT. A price is a commercial fact, so it MAY change which cover members are
// a commercial dependency and MAY change the completeness. It may NEVER change a
// STRUCTURAL quantity verdict: whether the declared quantities contradict each other
// is a property of the numbers and the declared edges, and a tariff cannot make
// 3 + 4 stop being different from 5.
//
// The base graphs are the whole validated three-node enumeration from the
// differential model check, and the contradictory/non-contradictory split is decided
// by the independent oracle, which is deliberately commercial-free. The property is
// never vacuous in either direction: every graph the model calls contradicted must
// be rejected under every tariff, and every graph the model calls clean must be
// accepted as structurally sound under every tariff.
//
// THREE CLAUSES, IN ORDER OF STRENGTH.
//
//  1. NON-SHADOWABLE. A structural contradiction must never certify complete money,
//     under any of the three tariffs. No other diagnosis can outrank that
//     consequence, so this clause carries the invariant on its own.
//
//  2. CROSS-TARIFF CLASS INVARIANCE. The reported typed class may differ between
//     tariffs ONLY where a higher-priority structural finding that is itself
//     tariff-dependent preempts it. Production reports ONE typed error per valuation,
//     and a structural overlap conflict outranks every other structural diagnosis in
//     that single slot; an overlap conflict needs two PAYABLE ends, so whether it
//     fires is a tariff fact while the diagnosis it outranks is not. So if the three
//     tariffs do not report the same class, every class that is not an overlap
//     conflict must still agree with every other non-overlap class. A tariff that
//     turned a partition contradiction into a missing-operand complaint, or a subset
//     contradiction into a clean settlement, fails here.
//
//  3. TWO-SIDED MODEL AGREEMENT. Where the outcome is not outranked AND the model
//     reports no ambiguous (shared complete ownership) node, the typed
//     quantity-contradiction class must equal the model's own verdict, re-asserted
//     through errors.Is against the real sentinel. Ambiguity is excluded because
//     production deliberately reports it INSTEAD of the arithmetic: a partition whose
//     child is shared by two complete parents has a sum that is not comparable, so
//     the more precise diagnosis wins and the quantity verdict is deliberately not
//     reported. That precedence is a fixed part of the model, identical under every
//     tariff, so it is not what this property measures.
func TestMetamorphicPricingMetamorphism(t *testing.T) {
	t.Parallel()
	graphs := smGraphs()
	evidenceSets := [][3]smEv{
		{smOne, smOne, smOne},
		{smTwo, smTwo, smTwo},
		{smZero, smOne, smTwo},
		{smOne, smAbsent, smOne},
		{smAbsent, smOne, smOne},
		{smOne, smUnavailable, smOne},
		{smTwo, smZero, smAbsent},
		{smAbsent, smAbsent, smAbsent},
	}
	nodes := &mtpGraph{name: "p5", keys: smNodeKeySet()}
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			continue
		}
		valid = append(valid, opts)
	}
	tally := newMTPTally("pricing_metamorphism", len(valid))
	t.Cleanup(func() { tally.finish(t) })

	// The three tallies below are written from parallel subtests, so they are
	// atomic rather than plain counters: a non-deterministic summary line would be
	// indistinguishable from a non-deterministic verdict.
	var contradictory, clean, outranked atomic.Int64
	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			rels := smRelationships(opts)
			schemas := smSchemas(rels)
			for _, ev := range evidenceSets {
				oracle := mtpOracle(t, "p5-oracle", schemas, smEvidenceForNodes(ev))
				wantContradiction := len(oracle.Contradicted) > 0
				if wantContradiction {
					contradictory.Add(1)
				} else {
					clean.Add(1)
				}
				obs := smMeasures(t, ev)
				// observed collects, per seam, the class each pricing state
				// reported, so clause 2 can compare them across the tariffs.
				observed := map[string][]string{}
				for _, state := range []struct {
					label  string
					prices mtpPricing
				}{
					{label: "paid", prices: mtpPaid(nodes)},
					{label: "explicit_free", prices: mtpFree(nodes)},
					{label: "no_rule", prices: mtpNoRule(nodes)},
				} {
					refID := fmt.Sprintf("p5-g%03d-%s-%s", gi, state.label, smEvCode(ev))
					resolved := f3Resolved(t, refID, state.prices.rules(t, nodes, fmt.Sprintf("g%03d-%s", gi, state.label)), schemas)
					for _, seam := range review5beSeams() {
						val, err := seam.rate(t, resolved, f3Observation(t, refID, obs...))
						outcome := mtpCollect(seam.name, val, err)
						label := fmt.Sprintf("p5 graph[%s] evidence[%s] pricing=%s seam=%s", smOptsName(opts), smEvName(ev), state.label, seam.name)
						observed[seam.name] = append(observed[seam.name], state.label+"="+outcome.errClass)
						tally.countCase()

						// Clause 1, the non-shadowable one.
						if wantContradiction && outcome.completeness == economics.CompletenessComplete {
							t.Errorf("%s: the tariff allowed a structurally CONTRADICTED graph to certify complete money; the model reports contradicted=%v and %s",
								label, oracle.Contradicted, outcome.tuple())
						}

						if outcome.errClass == "overlap_conflict" {
							// Outranked by a structural overlap, whose firing is a
							// tariff fact, so it is excluded from the class comparison.
							// Clause 1 does not skip it.
							outranked.Add(1)
							continue
						}
						if verdict := smIsQuantityContradiction(outcome.errClass); verdict && !mtpIs(err, outcome.errClass) {
							t.Errorf("%s: a quantity contradiction was reported as class %q but the retained error does not wrap the real sentinel",
								label, outcome.errClass)
						}
					}
				}
				// Clause 2, the cross-tariff class invariance.
				for _, seam := range review5beSeams() {
					distinct := map[string]struct{}{}
					for _, class := range mtpStructuralClasses(observed[seam.name]) {
						distinct[class] = struct{}{}
					}
					if len(distinct) <= 1 {
						continue
					}
					tally.countCase()
					t.Errorf("p5 graph[%s] evidence[%s] seam=%s: the tariff changed the reported STRUCTURAL class across the three pricing states; only a structural overlap may preempt another diagnosis, because only its firing is tariff-dependent. Reported: %v",
						smOptsName(opts), smEvName(ev), seam.name, observed[seam.name])
				}
			}
		})
	}
	t.Cleanup(func() {
		t.Logf("METAMORPHIC[pricing_metamorphism] evidence_assignments=%d model_contradictory_pairs=%d model_clean_pairs=%d outcomes_outranked_by_overlap=%d",
			len(evidenceSets), contradictory.Load(), clean.Load(), outranked.Load())
	})
}

// TestMetamorphicStructuralVerdictAgreesWithModel is the model-agreement
// CROSS-CHECK that accompanies property 5. It is a separate test, and deliberately
// not a clause of property 5, because it measures something different: not whether
// a tariff changes a verdict, but whether production's verdict and the independent
// model's verdict are the same verdict.
//
// It uses the same validated three-node enumeration and the same evidence
// assignments, with the EXPLICIT-FREE tariff, because a structural overlap conflict
// needs two payable ends and is therefore deliberately absent there. Outcomes that
// the model reports as AMBIGUOUS are excluded, because production deliberately
// reports the shared-child ambiguity in preference to the arithmetic: a partition
// whose child is declared by two complete parents has a sum that is not comparable,
// and the more precise diagnosis wins.
//
// INVARIANT. Where the model is unambiguous, the typed quantity-contradiction class
// production reports must equal the model's own verdict, re-asserted through
// errors.Is against the real sentinel.
func TestMetamorphicStructuralVerdictAgreesWithModel(t *testing.T) {
	t.Parallel()
	graphs := smGraphs()
	evidenceSets := [][3]smEv{
		{smOne, smOne, smOne},
		{smTwo, smTwo, smTwo},
		{smZero, smOne, smTwo},
		{smOne, smAbsent, smOne},
		{smAbsent, smOne, smOne},
		{smOne, smUnavailable, smOne},
		{smTwo, smZero, smAbsent},
		{smAbsent, smAbsent, smAbsent},
	}
	nodes := &mtpGraph{name: "p5x", keys: smNodeKeySet()}
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			continue
		}
		valid = append(valid, opts)
	}
	tally := newMTPTally("structural_verdict_vs_model", len(valid))
	t.Cleanup(func() { tally.finish(t) })

	var compared, ambiguousSkipped atomic.Int64
	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			schemas := smSchemas(smRelationships(opts))
			resolved := f3Resolved(t, fmt.Sprintf("p5x-g%03d", gi), mtpFree(nodes).rules(t, nodes, fmt.Sprintf("g%03d", gi)), schemas)
			for _, ev := range evidenceSets {
				oracle := mtpOracle(t, "p5x-oracle", schemas, smEvidenceForNodes(ev))
				if len(oracle.Ambiguous) > 0 {
					ambiguousSkipped.Add(1)
					continue
				}
				wantContradiction := len(oracle.Contradicted) > 0
				compared.Add(1)
				obs := smMeasures(t, ev)
				for _, seam := range review5beSeams() {
					val, err := seam.rate(t, resolved, f3Observation(t, fmt.Sprintf("p5x-g%03d-%s", gi, smEvCode(ev)), obs...))
					outcome := mtpCollect(seam.name, val, err)
					label := fmt.Sprintf("p5x graph[%s] evidence[%s] seam=%s", smOptsName(opts), smEvName(ev), seam.name)
					tally.countCase()
					verdict := smIsQuantityContradiction(outcome.errClass)
					// When the model finds a physical impossibility but the
					// complete-coverage proof already produced a MORE SPECIFIC
					// diagnosis for the same node - the member was never reported,
					// or its children are ambiguously shared - that diagnosis is
					// authoritative and the solver's own verdict is deliberately
					// suppressed as a duplicate of it. Requiring class equality
					// here would forbid a better answer, so the substituted class is
					// accepted, and the SAFETY property that actually matters is
					// asserted instead: production must still never certify
					// complete money.
					substituted := wantContradiction && !verdict && mtpIsSpecificCoverDiagnosis(outcome.errClass)
					if verdict != wantContradiction && !substituted {
						t.Errorf("%s: production and the independent model disagree on the STRUCTURAL quantity verdict: production reports contradiction=%v (class %q) and the model reports contradiction=%v (model_contradicted=%v); %s",
							label, verdict, outcome.errClass, wantContradiction, oracle.Contradicted, outcome.tuple())
					}
					if wantContradiction && outcome.completeness == economics.CompletenessComplete {
						t.Errorf("%s: the model found a physical impossibility but production certified COMPLETE (class %q); %s",
							label, outcome.errClass, outcome.tuple())
					}
					if substituted && outcome.completeness == economics.CompletenessComplete {
						t.Errorf("%s: a more specific cover diagnosis (%q) was substituted yet the valuation is still COMPLETE; %s",
							label, outcome.errClass, outcome.tuple())
					}
					if verdict && !mtpIs(err, outcome.errClass) {
						t.Errorf("%s: a quantity contradiction was reported as class %q but the retained error does not wrap the real sentinel",
							label, outcome.errClass)
					}
				}
			}
		})
	}
	t.Cleanup(func() {
		t.Logf("METAMORPHIC[structural_verdict_vs_model] graph_evidence_pairs_compared=%d pairs_skipped_model_ambiguous=%d",
			compared.Load(), ambiguousSkipped.Load())
	})
}

// mtpStructuralClasses renders the per-pricing-state reported classes of one seam,
// keeping only the classes that are a STRUCTURAL verdict: one of the six frozen
// schema sentinels, or "nil" for "no structural finding". Two classes are dropped
// on purpose. A structural overlap conflict is dropped because its firing needs two
// payable ends and is therefore a tariff fact, and it is precisely the finding that
// preempts the others in the single reported error. Any other error -- a missing
// rate, a quantity-incomplete line -- is a COMMERCIAL or line-level diagnostic, not a
// verdict about the declared quantities, and comparing it would compare pricing
// against structure, which is the category error this property exists to avoid.
func mtpStructuralClasses(reported []string) []string {
	out := make([]string, 0, len(reported))
	for _, entry := range reported {
		_, class, ok := strings.Cut(entry, "=")
		if !ok || class == "overlap_conflict" {
			continue
		}
		if class != "nil" && mtpSentinel(class) == nil {
			continue
		}
		out = append(out, class)
	}
	return out
}

// smNodeKeySet exposes the three enumerated node keys of the differential model
// check as a role map, so this file reuses the same enumeration and the same
// helper functions without redeclaring the keys.
func smNodeKeySet() map[string]metering.ComponentKey {
	return map[string]metering.ComponentKey{
		"n1": smNodeKeys[0],
		"n2": smNodeKeys[1],
		"n3": smNodeKeys[2],
	}
}

// ---------------------------------------------------------------------------
// PROPERTY 6 -- optionality metamorphism.
// ---------------------------------------------------------------------------

type mtpOptionalRow struct {
	name   string
	edges  []mtpEdge
	parent string
	member string
	// parentQty must equal the sibling's reported quantity, so the cover is
	// conserved whichever way the member is declared.
	parentQty  mtpEv
	siblingQty mtpEv
	// sibling is the always-present required member of the cover.
	sibling string
	// alwaysPresent carries the evidence for any further declared member, so a
	// multi-member cover stays conserved and the row exercises the member under
	// test rather than an unrelated absent required member.
	alwaysPresent mtpEvidence
}

func mtpOptionalRows() []mtpOptionalRow {
	partition := metering.RelationshipPartition
	return []mtpOptionalRow{
		{
			name:   "single_unit_sibling",
			edges:  []mtpEdge{{kind: partition, parent: "a", child: "c"}, {kind: partition, parent: "a", child: "d"}},
			parent: "a", member: "d", sibling: "c", parentQty: mtpOneEv, siblingQty: mtpOneEv,
		},
		{
			name:   "two_unit_sibling",
			edges:  []mtpEdge{{kind: partition, parent: "a", child: "c"}, {kind: partition, parent: "a", child: "d"}},
			parent: "a", member: "d", sibling: "c", parentQty: mtpExact("2"), siblingQty: mtpExact("2"),
		},
		{
			name: "optional_sibling_too",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d", optional: true},
			},
			parent: "a", member: "d", sibling: "c", parentQty: mtpOneEv, siblingQty: mtpOneEv,
		},
		{
			name: "aggregate_cover",
			edges: []mtpEdge{
				{kind: metering.RelationshipAggregate, parent: "a", child: "c"},
				{kind: metering.RelationshipAggregate, parent: "a", child: "d"},
			},
			parent: "a", member: "d", sibling: "c", parentQty: mtpOneEv, siblingQty: mtpOneEv,
		},
		{
			name: "cover_with_zero_valued_subset_outside",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
				{kind: metering.RelationshipSubset, parent: "a", child: "e"},
			},
			parent: "a", member: "d", sibling: "c", parentQty: mtpOneEv, siblingQty: mtpOneEv,
			alwaysPresent: mtpEvidence{"e": mtpZeroEv},
		},
		{
			name: "three_member_cover",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
				{kind: partition, parent: "a", child: "f"},
			},
			parent: "a", member: "d", sibling: "c", parentQty: mtpOneEv, siblingQty: mtpOneEv,
			alwaysPresent: mtpEvidence{"f": mtpZeroEv},
		},
	}
}

// mtpOptionalEvidence builds the row's evidence: the conserved parent and sibling
// plus whatever further members the row always reports.
func (r mtpOptionalRow) mtpOptionalEvidence(member mtpEv) mtpEvidence {
	evidence := mtpEvidence{r.parent: r.parentQty, r.sibling: r.siblingQty, r.member: member}
	maps.Copy(evidence, r.alwaysPresent)
	return evidence
}

// TestMetamorphicOptionalAbsentIsNotExactZero is the first half of PROPERTY 6.
//
// TRANSFORMATION. The optional member of a conserved complete cover is declared
// ABSENT in one form and reported at an explicit EXACT ZERO in the other.
//
// INVARIANT. Under the schema's own optional-zero semantics the two forms describe
// the same cover and MUST therefore settle the same money; a rater that treated a
// provider-reported zero as a missing operand would be caught. But they are NEVER
// EQUIVALENT: the exact-zero form emits the member's identity in the immutable
// line set and the absent form does not, so the audit record of what the provider
// actually reported stays distinct. The model is asked to certify that the two
// evidence states it keeps are themselves distinct, which is what makes the
// production distinction meaningful rather than cosmetic.
//
// The ancestor carries no rate, so the cover's members are the surviving money and
// the member's own line is visible in both forms.
func TestMetamorphicOptionalAbsentIsNotExactZero(t *testing.T) {
	t.Parallel()
	rows := mtpOptionalRows()
	tally := newMTPTally("optionality_absent_vs_zero", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p6z_"+row.name, "a", row.sibling, row.member, "e", "f")
		optionalEdges := mtpWithOptional(row.edges, row.parent, row.member, true)
		optionalSchemas := mtpOneSchema(mtpRels(graph, optionalEdges...))
		mtpValidate(t, "p6z-"+row.name, optionalSchemas)

		absentEvidence := row.mtpOptionalEvidence(mtpAbsentEv)
		zeroEvidence := row.mtpOptionalEvidence(mtpZeroEv)

		// The model must keep the two states apart, otherwise there is nothing for
		// production to keep apart.
		absentOracle := mtpOracle(t, "p6z-"+row.name+"-absent-oracle", optionalSchemas, mtpOracleEvidence(graph, absentEvidence))
		zeroOracle := mtpOracle(t, "p6z-"+row.name+"-zero-oracle", optionalSchemas, mtpOracleEvidence(graph, zeroEvidence))
		absentNode := absentOracle.Nodes[graph.canon(row.member)]
		zeroNode := zeroOracle.Nodes[graph.canon(row.member)]
		if !absentNode.Absent {
			t.Errorf("p6z %s: the model does not treat the absent optional member as absent, so this scenario does not test production", row.name)
		}
		if zeroNode.Absent {
			t.Errorf("p6z %s: the model treats the exact-zero member as absent, which would collapse the distinction this property is about", row.name)
		}

		for _, variant := range []struct {
			label  string
			prices mtpPricing
		}{
			{label: "paid_members", prices: mtpPricing{row.parent: "", row.sibling: "3", row.member: "2", "e": "5", "f": "7"}},
			{label: "explicit_free", prices: mtpFree(graph)},
		} {
			t.Run(row.name+"/"+variant.label, func(t *testing.T) {
				t.Parallel()
				tag := "p6z-" + row.name + "-" + variant.label
				absentOutcomes := mtpRateForm(t, tag+"-absent", mtpForm{
					schemas: optionalSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{absentEvidence.obs(t, graph, tag+"-absent")},
				})
				zeroOutcomes := mtpRateForm(t, tag+"-zero", mtpForm{
					schemas: optionalSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{zeroEvidence.obs(t, graph, tag+"-zero")},
				})
				tally.mtpNoteSeamSkew(tag+"-absent", absentOutcomes)
				tally.mtpNoteSeamSkew(tag+"-zero", zeroOutcomes)

				member := graph.canon(row.member)
				for i, seam := range review5beSeams() {
					tally.countCase()
					label := tag + "/" + seam.name
					absent := absentOutcomes[i]
					zero := zeroOutcomes[i]
					if absent.errClass != zero.errClass {
						t.Errorf("%s: the optional-zero semantics changed the typed error class between an absent and an exact-zero member: absent %q zero %q\n  absent: %s\n  zero  : %s",
							label, absent.errClass, zero.errClass, absent.tuple(), zero.tuple())
					}
					if absent.completeness != zero.completeness {
						t.Errorf("%s: the optional-zero semantics changed the completeness between an absent and an exact-zero member: absent %q zero %q\n  absent: %s\n  zero  : %s",
							label, absent.completeness, zero.completeness, absent.tuple(), zero.tuple())
					}
					if absent.total != zero.total {
						t.Errorf("%s: the money must MATCH: a provider-reported exact zero and a declared optional zero describe the same share, so the payable total must be identical; absent %s zero %s\n  absent: %s\n  zero  : %s",
							label, absent.total, zero.total, absent.tuple(), zero.tuple())
					}
					if slices.Contains(absent.components, member) {
						t.Errorf("%s: an ABSENT optional member was emitted as a line, so the audit record can no longer distinguish it from a reported zero: %s", label, absent.tuple())
					}
					if !slices.Contains(zero.components, member) {
						t.Errorf("%s: a REPORTED exact-zero optional member emitted no identity, so the audit record cannot distinguish a reported zero from an absent member: %s", label, zero.tuple())
					}
					if mtpSameTuple(absent, zero) {
						t.Errorf("%s: an absent optional member and an exact-zero member produced an IDENTICAL observable tuple, so the two evidence states are not distinguishable at all: %s", label, absent.tuple())
					}
				}
			})
		}
	}
}

// TestMetamorphicOptionalityRequiredAbsenceIsDiagnosed is the second half of
// PROPERTY 6.
//
// TRANSFORMATION. The same member of the same complete cover is declared REQUIRED in
// one form and OPTIONAL in the other, and is ABSENT in both.
//
// INVARIANT. The two forms are NOT equivalent, and the direction of the difference
// is fixed: an absent REQUIRED member is missing evidence and is diagnosed as
// ErrSchemaPartitionIncomplete, while an absent OPTIONAL member is the schema's own
// declared edge-local zero and is never diagnosed and never prevents a complete
// settlement. The member carries a positive rate in the paid variant, so the
// required form is also the form in which money was owed and never reported; the
// explicit-free variant shows the distinction survives a tariff that makes the
// member worthless. The ancestor carries no rate, so the diagnosis is about the
// COVER and not about a parent that could have paid for it itself.
func TestMetamorphicOptionalityRequiredAbsenceIsDiagnosed(t *testing.T) {
	t.Parallel()
	rows := mtpOptionalRows()
	tally := newMTPTally("optionality_required_vs_optional", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p6r_"+row.name, "a", row.sibling, row.member, "e", "f")
		evidence := row.mtpOptionalEvidence(mtpAbsentEv)
		requiredSchemas := mtpOneSchema(mtpRels(graph, mtpWithOptional(row.edges, row.parent, row.member, false)...))
		optionalSchemas := mtpOneSchema(mtpRels(graph, mtpWithOptional(row.edges, row.parent, row.member, true)...))
		mtpValidate(t, "p6r-"+row.name+"-required", requiredSchemas)
		mtpValidate(t, "p6r-"+row.name+"-optional", optionalSchemas)

		for _, variant := range []struct {
			label  string
			prices mtpPricing
		}{
			{label: "paid_member", prices: mtpPricing{row.parent: "", row.sibling: "3", row.member: "2", "e": "5", "f": "7"}},
			{label: "explicit_free", prices: mtpFree(graph)},
		} {
			t.Run(row.name+"/"+variant.label, func(t *testing.T) {
				t.Parallel()
				tag := "p6r-" + row.name + "-" + variant.label
				requiredOutcomes := mtpRateForm(t, tag+"-required", mtpForm{
					schemas: requiredSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{evidence.obs(t, graph, tag+"-required")},
				})
				optionalOutcomes := mtpRateForm(t, tag+"-optional", mtpForm{
					schemas: optionalSchemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{evidence.obs(t, graph, tag+"-optional")},
				})
				tally.mtpNoteSeamSkew(tag+"-required", requiredOutcomes)
				tally.mtpNoteSeamSkew(tag+"-optional", optionalOutcomes)

				for i, seam := range review5beSeams() {
					tally.countCase()
					label := tag + "/" + seam.name
					required := requiredOutcomes[i]
					optional := optionalOutcomes[i]
					if mtpSameTuple(required, optional) {
						t.Errorf("%s: a REQUIRED and an OPTIONAL absent member produced an IDENTICAL observable tuple; the optional flag must change the verdict: %s", label, required.tuple())
					}
					if optional.completeness != economics.CompletenessComplete {
						t.Errorf("%s: an absent OPTIONAL member is the schema's own declared edge-local zero, so it must never prevent a complete settlement; %s", label, optional.tuple())
					}
					if mtpIs(optional.err, "partition_incomplete") {
						t.Errorf("%s: an absent OPTIONAL member was diagnosed as a missing REQUIRED member; %s", label, optional.tuple())
					}
					if required.completeness == economics.CompletenessComplete {
						t.Errorf("%s: an absent REQUIRED member certified complete money; %s", label, required.tuple())
					}
					if !mtpIs(required.err, "partition_incomplete") {
						t.Errorf("%s: an absent REQUIRED member must be diagnosed as ErrSchemaPartitionIncomplete, got class %q (%v); %s",
							label, required.errClass, required.err, required.tuple())
					}
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// PROPERTY 7 -- irrelevant-extension invariance.
// ---------------------------------------------------------------------------

type mtpExtensionRow struct {
	name  string
	edges []mtpEdge
	ev    mtpEvidence
	// unrelatedParent and unrelatedChild are the two roles of the isolated
	// fragment the extension adds. The parent carries NO rate, so the fragment is
	// an ordinary child-only tariff shape and adding it cannot create an internal
	// overlap conflict of its own.
	unrelatedParent string
	unrelatedChild  string
}

func mtpExtensionRows() []mtpExtensionRow {
	partition := metering.RelationshipPartition
	subset := metering.RelationshipSubset
	return []mtpExtensionRow{
		{
			name: "complete_cover",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
			},
			ev:              mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv},
			unrelatedParent: "u", unrelatedChild: "v",
		},
		{
			name: "complete_cover_contradicted",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
			},
			ev:              mtpEvidence{"a": mtpExact("2"), "c": mtpTwoEv, "d": mtpZeroEv},
			unrelatedParent: "u", unrelatedChild: "v",
		},
		{
			name: "missing_required_member",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
			},
			ev:              mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv},
			unrelatedParent: "u", unrelatedChild: "v",
		},
		{
			name: "paid_subset_overlap",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
				{kind: subset, parent: "a", child: "e"},
			},
			ev:              mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv, "e": mtpOneEv},
			unrelatedParent: "u", unrelatedChild: "v",
		},
		{
			name: "nested_cover",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
			},
			ev:              mtpEvidence{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpOneEv, "y": mtpOneEv},
			unrelatedParent: "u", unrelatedChild: "v",
		},
		{
			name: "shared_complete_child",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "z", child: "c"},
			},
			ev:              mtpEvidence{"a": mtpOneEv, "z": mtpOneEv, "c": mtpOneEv},
			unrelatedParent: "u", unrelatedChild: "v",
		},
	}
}

// TestMetamorphicIrrelevantExtensionInvariance is PROPERTY 7.
//
// TRANSFORMATION. A second, completely isolated frozen schema is published
// alongside the base one. The extension declares its own parent and its own child
// and shares NO component identity, NO edge and NO scope with the base graph, so it
// cannot state anything about the base world.
//
// SCOPE, STATED PRECISELY. Production's model treats a PUBLISHED frozen schema as a
// declaration about every rated call, not only about the components the call happens
// to mention: a declared REQUIRED complete-coverage member that carries a non-free
// rule and that the call never reported is classified as money the schema is hiding,
// and that classification is injected into every scope that has any rateable line
// (overlappingSchemaInclusionConflicts, the unobserved-parent branch). An extension
// whose member therefore carries a rate is NOT an irrelevant extension in that
// model, and the full-tuple claim is not made for it; what IS claimed for it is
// stated exactly, as the second variant below. The full-tuple claim is made for a
// MONEY-FREE extension, which is the honest reading of "isolated unrelated schema".
//
// INVARIANT, VARIANT "money_free_declaration_only". The extension declares a
// fragment whose only member is explicit free, and no evidence is added for it. The
// FULL observable tuple -- typed error class, completeness, sorted emitted
// identities and payable total -- must be identical. This is the strong form, and it
// is the one that catches a global rather than per-parent effect: a shared-child
// taint, a scope-level classification or a cover fixpoint that leaked across an
// unrelated schema would all show up here.
//
// INVARIANT, VARIANT "non_free_declaration_only". The extension declares a fragment
// whose member carries a positive rate, and no evidence is added for it. Production
// is then entitled to add a missing-member diagnosis, so the completeness and the
// error class are NOT claimed to be unchanged. Everything else is: every base
// identity must still be emitted with the same multiplicity, no identity outside the
// extension's own fragment may appear, the base money must be untouched, and the
// base payable total must be unchanged. That is the precise boundary: the extension
// may add a diagnosis, it may never move, drop or duplicate the base's money.
//
// INVARIANT, VARIANT "with_evidence". The extension declares a fragment with a
// positive rate AND fully observed, conserved evidence for it. The typed error class
// and the completeness must be identical to the base, every base identity must
// survive with the same multiplicity, the only additional identities must belong to
// the extension's own fragment, the base money must be untouched, the fragment's
// child must be billed exactly its own quantity times its own rate, and the payable
// total must be the base total plus exactly that amount.
func TestMetamorphicIrrelevantExtensionInvariance(t *testing.T) {
	t.Parallel()
	rows := mtpExtensionRows()
	tally := newMTPTally("irrelevant_extension", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		graph := mtpNewGraph("p7_"+row.name, "a", "b", "c", "d", "e", "x", "y", "z", row.unrelatedParent, row.unrelatedChild)
		baseSchemas := mtpOneSchema(mtpRels(graph, row.edges...))
		extensionSchemas := []metering.ComponentSchema{{
			ID: b1SchemaID + "_extension", Version: "1",
			Relationships: mtpRels(graph, mtpEdge{kind: metering.RelationshipPartition, parent: row.unrelatedParent, child: row.unrelatedChild}),
		}}
		extended := append(append([]metering.ComponentSchema{}, baseSchemas...), extensionSchemas...)
		fragment := []string{graph.canon(row.unrelatedParent), graph.canon(row.unrelatedChild)}
		basePrices := mtpPricing{"a": "", "b": "", "c": "3", "d": "4", "e": "5", "x": "1", "y": "2", "z": ""}

		// The extension always carries the same conserved evidence when it is
		// given any, so the added fragment is itself internally consistent.
		addedEvidence := []metering.Measure{
			b1Measure(t, graph.key(row.unrelatedParent), "3"),
			b1Measure(t, graph.key(row.unrelatedChild), "3"),
		}
		addedQuantity, _ := new(big.Rat).SetString("3")

		for _, variant := range []struct {
			label string
			// baseTariff prices the base world only; the extension's parent never
			// carries a rate, so the fragment is an ordinary child-only shape.
			baseTariff mtpPricing
			// extensionRate is the added child's rate. "0" makes the fragment
			// money-free; anything else makes it a money-bearing declaration.
			extensionRate string
			addEvidence   bool
		}{
			{label: "money_free_declaration_only", baseTariff: basePrices, extensionRate: "0"},
			{label: "non_free_declaration_only", baseTariff: basePrices, extensionRate: "9"},
			{label: "non_free_with_evidence", baseTariff: basePrices, extensionRate: "9", addEvidence: true},
			{label: "explicit_free_declaration_only", baseTariff: mtpFree(graph), extensionRate: "0"},
			{label: "explicit_free_with_evidence", baseTariff: mtpFree(graph), extensionRate: "0", addEvidence: true},
		} {
			t.Run(row.name+"/"+variant.label, func(t *testing.T) {
				t.Parallel()
				tag := "p7-" + row.name + "-" + variant.label
				mtpValidate(t, tag+"-base", baseSchemas)
				mtpValidate(t, tag+"-extended", extended)

				prices := mtpPricing{}
				maps.Copy(prices, variant.baseTariff)
				prices[row.unrelatedParent] = ""
				prices[row.unrelatedChild] = variant.extensionRate

				extendedMeasures := row.ev.measures(t, graph)
				if variant.addEvidence {
					extendedMeasures = append(extendedMeasures, addedEvidence...)
				}
				// The base form is always rated WITHOUT the extension's evidence,
				// so the only variable is the extension itself.
				baseOutcomes := mtpRateForm(t, tag+"-base", mtpForm{
					schemas: baseSchemas,
					rules:   prices.rules(t, graph, tag),
					obs:     []metering.Observation{row.ev.obs(t, graph, tag+"-base")},
				})
				extendedOutcomes := mtpRateForm(t, tag+"-extended", mtpForm{
					schemas: extended,
					rules:   prices.rules(t, graph, tag),
					obs:     []metering.Observation{f3Observation(t, tag+"-extended", extendedMeasures...)},
				})
				tally.mtpNoteSeamSkew(tag+"-base", baseOutcomes)
				tally.mtpNoteSeamSkew(tag+"-extended", extendedOutcomes)

				addedMoney := new(big.Rat).Mul(addedQuantity, mustMTPQuantity(t, variant.extensionRate))

				for i, seam := range review5beSeams() {
					tally.countCase()
					label := tag + "/" + seam.name
					base := baseOutcomes[i]
					extended := extendedOutcomes[i]

					if !variant.addEvidence && variant.extensionRate == "0" {
						// The strong form: a money-free declaration must change
						// nothing at all.
						mtpAssertSameTuple(t, label, base, extended)
						continue
					}

					if variant.addEvidence {
						// With the fragment's own evidence the extension asserts
						// nothing about the base, so the base's verdict must stand.
						if base.errClass != extended.errClass {
							t.Errorf("%s: an isolated, fully evidenced unrelated fragment changed the typed error class: base %q extended %q\n  base    : %s\n  extended: %s",
								label, base.errClass, extended.errClass, base.tuple(), extended.tuple())
						}
						if base.completeness != extended.completeness {
							t.Errorf("%s: an isolated, fully evidenced unrelated fragment changed the completeness: base %q extended %q\n  base    : %s\n  extended: %s",
								label, base.completeness, extended.completeness, base.tuple(), extended.tuple())
						}
					}

					// Every base identity must survive with the same multiplicity, and
					// the only identities the extension may add are its own.
					baseCounts := mtpCountIdentities(base.components)
					extendedCounts := mtpCountIdentities(extended.components)
					isFragment := map[string]struct{}{graph.canon(row.unrelatedParent): {}, graph.canon(row.unrelatedChild): {}}
					for identity, want := range baseCounts {
						if got := extendedCounts[identity]; got < want {
							t.Errorf("%s: the extension changed the emitted multiplicity of a BASE identity: want at least %d got %d\n  base    : %s\n  extended: %s",
								label, want, got, base.tuple(), extended.tuple())
						}
					}
					for identity := range extendedCounts {
						if _, fromBase := baseCounts[identity]; fromBase {
							continue
						}
						if _, own := isFragment[identity]; !own {
							t.Errorf("%s: an emitted identity outside both the base world and the extension's own fragment appeared\n  base    : %s\n  extended: %s",
								label, base.tuple(), extended.tuple())
						}
					}

					// The base money must be untouched, and the base's own payable
					// total must be untouched, whatever the extension does.
					baseMoney := mtpPositiveOutside(base.positive, nil)
					extendedBaseMoney := mtpPositiveOutside(extended.positive, fragment)
					if baseMoney.Cmp(extendedBaseMoney) != 0 {
						t.Errorf("%s: an isolated unrelated fragment moved the base money: base %s extended(base only) %s\n  base    : %s\n  extended: %s",
							label, baseMoney.RatString(), extendedBaseMoney.RatString(), base.tuple(), extended.tuple())
					}
					if got, want := mtpTotalRat(t, label+" base", base), baseMoney; got.Cmp(want) != 0 {
						t.Errorf("%s: the base payable total does not equal the sum of the base positive lines, so the additive reference is not trustworthy: total %s lines %s\n  base: %s",
							label, got.RatString(), want.RatString(), base.tuple())
					}
					baseTotal := mtpTotalRat(t, label+" base", base)
					if !variant.addEvidence {
						if got := mtpTotalRat(t, label+" extended", extended); got.Cmp(baseTotal) != 0 {
							t.Errorf("%s: declaring an isolated unrelated fragment changed the base payable total: base %s extended %s\n  base    : %s\n  extended: %s",
								label, baseTotal.RatString(), got.RatString(), base.tuple(), extended.tuple())
						}
					}

					if !variant.addEvidence {
						continue
					}
					// The added child is billed exactly its own quantity times its own
					// rate, and the total is the base total plus exactly that.
					gotAdded := mtpPositiveInside(extended.positive, []string{graph.canon(row.unrelatedChild)})
					if gotAdded.Cmp(addedMoney) != 0 {
						t.Errorf("%s: the added component is not billed exactly its own quantity times its own rate: want %s got %s; %s",
							label, addedMoney.RatString(), gotAdded.RatString(), extended.tuple())
					}
					wantTotal := new(big.Rat).Add(baseTotal, addedMoney)
					if got := mtpTotalRat(t, label+" extended", extended); got.Cmp(wantTotal) != 0 {
						t.Errorf("%s: the payable total is not the base total plus exactly the unrelated component's own money: want %s got %s\n  base    : %s\n  extended: %s",
							label, wantTotal.RatString(), got.RatString(), base.tuple(), extended.tuple())
					}
				}
			})
		}
	}
}

// mustMTPQuantity decodes a declared unit rate, failing the test on a literal the
// scenario itself supplied.
func mustMTPQuantity(t *testing.T, raw string) *big.Rat {
	t.Helper()
	quantity, ok := new(big.Rat).SetString(raw)
	if !ok {
		t.Fatalf("declared rate %q is not a rational", raw)
	}
	return quantity
}

// ---------------------------------------------------------------------------
// PROPERTY 8 -- scope-split invariance.
// ---------------------------------------------------------------------------

type mtpScopeRow struct {
	name string
	// edges declares the base graph. It is identical in all three rated forms;
	// only the SCOPE each component is reported in changes.
	edges []mtpEdge
	// firstScope and secondScope partition the graph's roles into the two B-leg
	// reduction scopes.
	firstScope  []string
	secondScope []string
	ev          mtpEvidence
	prices      mtpPricing
}

func mtpScopeRows() []mtpScopeRow {
	partition := metering.RelationshipPartition
	subset := metering.RelationshipSubset
	return []mtpScopeRow{
		{
			name: "complete_cover_replicated",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
			},
			firstScope: []string{"a", "c", "d"}, secondScope: []string{"a", "c", "d"},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv},
			prices: mtpPricing{"a": "", "c": "3", "d": "4", "e": "5", "z": ""},
		},
		{
			name: "partial_containment_replicated",
			edges: []mtpEdge{
				{kind: subset, parent: "a", child: "c"},
				{kind: subset, parent: "a", child: "d"},
			},
			firstScope: []string{"a", "c", "d"}, secondScope: []string{"a", "c", "d"},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv},
			prices: mtpPricing{"a": "", "c": "3", "d": "4", "e": "5", "z": ""},
		},
		{
			// The shared complete child is an AMBIGUITY in one scope, because two
			// complete parents claim the same share. Split across two scopes each
			// parent owns its own scope's child, so neither is ambiguous. This is
			// the base graph that makes the scope split load-bearing rather than
			// cosmetic: merging the two scopes would create a cross-scope shape the
			// system must NOT turn into a conflict.
			name: "shared_complete_child_split",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "z", child: "c"},
			},
			firstScope: []string{"a", "c"}, secondScope: []string{"z", "c"},
			ev:     mtpEvidence{"a": mtpOneEv, "z": mtpOneEv, "c": mtpOneEv},
			prices: mtpPricing{"a": "", "c": "3", "d": "4", "e": "5", "z": ""},
		},
		{
			// A complete cover with a zero-valued paid subset child. The subset
			// carries no money, so it never collides with the cover, and both
			// scopes stay internally consistent.
			name: "zero_valued_subset_split",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
				{kind: subset, parent: "a", child: "e"},
			},
			firstScope: []string{"a", "c", "d", "e"}, secondScope: []string{"a", "c", "d", "e"},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv, "d": mtpOneEv, "e": mtpZeroEv},
			prices: mtpPricing{"a": "", "c": "3", "d": "4", "e": "5", "z": ""},
		},
		{
			name: "contradicted_cover_split",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
			},
			firstScope: []string{"a", "c", "d"}, secondScope: []string{"a", "c", "d"},
			ev:     mtpEvidence{"a": mtpOneEv, "c": mtpTwoEv, "d": mtpZeroEv},
			prices: mtpPricing{"a": "", "c": "3", "d": "4", "e": "5", "z": ""},
		},
		{
			name: "missing_required_member_split",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d"},
			},
			firstScope: []string{"a", "c", "d"}, secondScope: []string{"a", "c", "d"},
			ev:     mtpEvidence{"a": mtpExact("2"), "c": mtpOneEv},
			prices: mtpPricing{"a": "", "c": "3", "d": "4", "e": "5", "z": ""},
		},
		{
			name: "optional_member_split",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "a", child: "d", optional: true},
			},
			firstScope: []string{"a", "c", "d"}, secondScope: []string{"a", "c", "d"},
			ev:     mtpEvidence{"a": mtpOneEv, "c": mtpOneEv},
			prices: mtpPricing{"a": "", "c": "3", "d": "4", "e": "5", "z": ""},
		},
		{
			name: "nested_cover_replicated",
			edges: []mtpEdge{
				{kind: partition, parent: "a", child: "b"},
				{kind: partition, parent: "a", child: "c"},
				{kind: partition, parent: "b", child: "x"},
				{kind: partition, parent: "b", child: "y"},
			},
			firstScope: []string{"a", "b", "c", "x", "y"}, secondScope: []string{"a", "b", "c", "x", "y"},
			ev:     mtpEvidence{"a": mtpExact("4"), "b": mtpExact("2"), "c": mtpExact("2"), "x": mtpOneEv, "y": mtpOneEv},
			prices: mtpPricing{"a": "", "b": "", "c": "3", "d": "4", "e": "5", "x": "1", "y": "2", "z": ""},
		},
	}
}

// mtpScopedObs builds one observation per B-leg scope. The scope is carried by the
// observation identity, which is the reducer's own scope construction, so this
// does not declare a scope by hand.
func mtpScopedObs(t *testing.T, graph *mtpGraph, ev mtpEvidence, roles []string, bLegID string) metering.Observation {
	t.Helper()
	measures := make([]metering.Measure, 0, len(roles))
	for _, role := range roles {
		if measure, ok := ev[role].measure(t, graph.key(role)); ok {
			measures = append(measures, measure)
		}
	}
	return b1Observation(t, "mtp-scope-"+graph.name+"-"+bLegID, bLegID, metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, measures...)
}

// TestMetamorphicScopeSplitInvariance is PROPERTY 8.
//
// TRANSFORMATION. One base graph and one evidence assignment are reported in two
// different B-leg reduction scopes instead of one. Moving a region into its own
// scope removes every containment interaction ACROSS the split, and a region that
// is internally consistent in its own scope must stay internally consistent there.
//
// INVARIANT. The two-scope valuation is the SUM of the two single-scope valuations:
// the same payable total and the same typed error class, because the split cannot
// create a conflict that neither scope had. Where a single scope is already a typed
// rejection, the merged valuation must carry the same rejection rather than a
// differently-worded or absent one. The base graphs include a shared complete
// child that is an ambiguity in a single scope and is clean once split, which is
// the case where the split is load-bearing rather than cosmetic.
func TestMetamorphicScopeSplitInvariance(t *testing.T) {
	t.Parallel()
	rows := mtpScopeRows()
	tally := newMTPTally("scope_split", len(rows))
	t.Cleanup(func() { tally.finish(t) })

	for _, row := range rows {
		roles := append(append([]string{}, row.firstScope...), row.secondScope...)
		for _, edge := range row.edges {
			roles = append(roles, edge.parent, edge.child)
		}
		for role := range row.prices {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		graph := mtpNewGraph("p8_"+row.name, roles...)
		schemas := mtpOneSchema(mtpRels(graph, row.edges...))
		mtpValidate(t, "p8-"+row.name, schemas)

		firstObs := mtpScopedObs(t, graph, row.ev, row.firstScope, "b-leg-1")
		secondObs := mtpScopedObs(t, graph, row.ev, row.secondScope, "b-leg-2")

		for _, variant := range []struct {
			label  string
			prices mtpPricing
		}{
			{label: "declared_prices", prices: row.prices},
			{label: "explicit_free", prices: mtpFree(graph)},
		} {
			t.Run(row.name+"/"+variant.label, func(t *testing.T) {
				t.Parallel()
				tag := "p8-" + row.name + "-" + variant.label
				firstOutcomes := mtpRateForm(t, tag+"-first", mtpForm{
					schemas: schemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{firstObs},
				})
				secondOutcomes := mtpRateForm(t, tag+"-second", mtpForm{
					schemas: schemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{secondObs},
				})
				mergedOutcomes := mtpRateForm(t, tag+"-merged", mtpForm{
					schemas: schemas,
					rules:   variant.prices.rules(t, graph, tag),
					obs:     []metering.Observation{firstObs, secondObs},
				})
				tally.mtpNoteSeamSkew(tag+"-first", firstOutcomes)
				tally.mtpNoteSeamSkew(tag+"-second", secondOutcomes)
				tally.mtpNoteSeamSkew(tag+"-merged", mergedOutcomes)

				for i, seam := range review5beSeams() {
					tally.countCase()
					label := tag + "/" + seam.name
					first := firstOutcomes[i]
					second := secondOutcomes[i]
					merged := mergedOutcomes[i]

					if first.errClass != second.errClass || first.completeness != second.completeness {
						t.Errorf("%s: the two single scopes disagree with each other BEFORE any merging, so there is no additive reference: first %s second %s\n  first : %s\n  second: %s",
							label, first.errClass+"/"+string(first.completeness), second.errClass+"/"+string(second.completeness), first.tuple(), second.tuple())
						continue
					}
					if merged.errClass != first.errClass {
						t.Errorf("%s: splitting one region across two scopes changed the typed error class: single %q merged %q\n  first : %s\n  second: %s\n  merged: %s",
							label, first.errClass, merged.errClass, first.tuple(), second.tuple(), merged.tuple())
					}
					if merged.completeness != first.completeness {
						t.Errorf("%s: splitting one region across two scopes changed the completeness: single %q merged %q\n  first : %s\n  second: %s\n  merged: %s",
							label, first.completeness, merged.completeness, first.tuple(), second.tuple(), merged.tuple())
					}
					firstTotal, okFirst := mtpRat(first.total)
					secondTotal, okSecond := mtpRat(second.total)
					mergedTotal, okMerged := mtpRat(merged.total)
					if !okFirst || !okSecond || !okMerged {
						// A rejected valuation may legitimately carry no payable
						// total; the class and completeness checks above still ran.
						if first.completeness == economics.CompletenessComplete || merged.completeness == economics.CompletenessComplete {
							t.Errorf("%s: a COMPLETE valuation carries no payable total, so the additive claim cannot be checked\n  first : %s\n  second: %s\n  merged: %s",
								label, first.tuple(), second.tuple(), merged.tuple())
						}
						continue
					}
					want := new(big.Rat).Add(firstTotal, secondTotal)
					if want.Cmp(mergedTotal) != 0 {
						t.Errorf("%s: the two-scope payable total is NOT the sum of the two single-scope totals: want %s got %s\n  first : %s\n  second: %s\n  merged: %s",
							label, want.RatString(), mergedTotal.RatString(), first.tuple(), second.tuple(), merged.tuple())
					}
				}
			})
		}
	}
}
