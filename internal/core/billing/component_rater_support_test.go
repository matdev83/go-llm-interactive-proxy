package billing

// TEST-ONLY BRIDGE AND WHITE-BOX PREDICATE CONTRACT.
//
// The relation predicate in component_rater_support.go is unexported, and the
// census and report rendering the harness drives through it are test-only, so
// nothing in this file changes a verdict, a completeness, an error class, a
// diagnostic or an amount. It exists for two reasons:
//
//  1. SupportShadowOverlapAudit is the exported-TEST-ONLY door onto the predicate
//     for the external agreement harness (component_rater_support_agreement_test.go,
//     package billing_test), which must reuse the populations declared by
//     billing_schema_model_test.go, billing_acceptance_vectors_test.go and the
//     B1/F2/F3/R7/N1/N2/5be/583/f356/62a/63c/64a/65 regressions. Those live in the
//     EXTERNAL test package, so the door must be exported; the repo already uses
//     exactly this pattern (export_test.go in fourteen packages). Putting the door
//     in a test file rather than in the production file is what keeps the compiled
//     package's symbol table untouched: there is no production name to call.
//  2. The tests below pin the predicate's own contract and PROVE, mechanically,
//     that exactly ONE production file names it, that file is the structural
//     overlap verdict, and that every reference sits inside that verdict's single
//     payable-scope pass.
//
// The bridge rebuilds the per-scope payable population the way component_rater.go's
// rating loop does, because that population is exactly the set the retired
// production special cases judged and it is not part of any returned value (a
// suppressed conflict line is never emitted). The rebuild is TEST code, and
// TestSupportAuditPayablePopulationMatchesEmittedLines in the agreement harness
// checks it against production on every non-conflict case of every population.

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// The three shadow answers, as the stable strings the agreement report counts.
const (
	SupportRelationDefiniteOverlap  = "definite_overlap"
	SupportRelationProvenDisjoint   = "proven_disjoint"
	SupportRelationUnknownIntersect = "unknown_intersection"
)

// supportShadowPredicateNames is the closed set of declarations the relation
// predicate consists of, including the candidate enumerator production runs on, so
// a production file naming ANY of them is a production consumer of the structural
// overlap verdict. supportShadowPredicateMethods is the same guard over the two
// receiver methods whose bare names are ordinary English -- "relation" and
// "contains" occur throughout this package's prose and helpers -- so those are
// scanned in the QUALIFIED form a call site has to use.
const supportShadowPredicateNames = "shadowContributingRelations|" +
	"shadowPositiveContributors|" +
	"shadowOverlapPair|" +
	"shadowOverlapPairKeys|" +
	"newShadowScope|" +
	"shadowScope|" +
	"componentLineRelation|" +
	"overlapCandidates|" +
	"relationDefiniteOverlap|" +
	"relationProvenDisjoint|" +
	"relationUnknownIntersection"

const supportShadowPredicateMethods = ".relation(|" + ".contains("

// SupportOverlapPair is one pair of distinct positive contributors in one
// reduction scope, with the shadow predicate's relation. It carries a relation
// and nothing else: no amount, no intersection value, no cross product.
type SupportOverlapPair struct {
	Scope    string
	A, B     string
	Relation string
}

// SupportOverlapAudit is the shadow answer for one rated input: every distinct
// pair of positive contributors per scope, the payable population the pairs were
// taken over, the ONE cover authority's resolution for every declared cover parent
// per scope, and the PRODUCTION overlap resolver's own decision over the very same
// populations.
type SupportOverlapAudit struct {
	Pairs   []SupportOverlapPair
	Payable map[string][]string
	// Covers is scope -> cover parent canonical key -> "proven" | "ambiguous" |
	// "unresolved". It is the authority's own tri-state, projected to a string so
	// the agreement report can NAME the commercial reason production is stricter
	// rather than only counting it.
	Covers map[string]map[string]string
	// ProductionBlocked is the production overlap resolver's own first-error
	// flag: true iff it raised ErrSchemaOverlapConflict.
	ProductionBlocked bool
	// ProductionConflicts is, per scope, the ascending canonical keys the
	// production resolver recorded as WITHHELD. It is reported, never consulted by
	// production: this is the observer the agreement harness compares against, and
	// it is why the comparison unit is "did the resolver act" rather than "did the
	// error class happen to be overlap_conflict". The fourth production special
	// case withholds a line and classifies the valuation under the PARTITION
	// sentinels rather than the overlap one, so the error class alone would miss
	// it entirely.
	ProductionConflicts map[string][]string
}

// ProductionActed reports whether the production overlap resolver withheld or
// refused anything at all, under either signal. This is production's side of the
// agreement unit.
func (a SupportOverlapAudit) ProductionActed() bool {
	return a.ProductionBlocked || len(a.ProductionConflicts) > 0
}

// Blocking reports whether the shadow predicate found any definite overlap. That
// is the only answer a future authority could act on: the other two are
// non-blocking by construction and would change no money.
func (a SupportOverlapAudit) Blocking() bool {
	for _, pair := range a.Pairs {
		if pair.Relation == SupportRelationDefiniteOverlap {
			return true
		}
	}
	return false
}

// Relations counts the pairs by relation name.
func (a SupportOverlapAudit) Relations() map[string]int {
	counts := map[string]int{}
	for _, pair := range a.Pairs {
		counts[pair.Relation]++
	}
	return counts
}

// SupportShadowOverlapAudit runs the shadow predicate over one rated input. It is
// pure with respect to production: it reads the frozen compiled program and the
// one cover authority's verdict for each scope, and returns a relation per pair.
// It never calls Rate, never emits a line and never returns an amount.
func SupportShadowOverlapAudit(snapshot economics.TariffSnapshot, input economics.PostUsageRatingInput) (SupportOverlapAudit, error) {
	audit := SupportOverlapAudit{
		Payable:             map[string][]string{},
		Covers:              map[string]map[string]string{},
		ProductionConflicts: map[string][]string{},
	}
	rater, err := NewReferenceRater(snapshot)
	if err != nil {
		return audit, err
	}
	// An input production itself refuses carries no reduced evidence at all, so the
	// shadow has no population either. The empty audit is the faithful answer, not
	// a swallowed failure: production also rated nothing.
	if err := input.Validate(); err != nil {
		return audit, nil
	}
	if err := rater.validateSnapshotBinding(input); err != nil {
		return audit, nil
	}
	switch input.Basis {
	case economics.BasisLocalExpected:
		input = inputForPlane(input, isLocalQuantityObservation)
	case economics.BasisCustomerPolicy:
		input = inputForPlane(input, isRetailBLegObservation)
	case economics.BasisProviderQuantityLocal:
		input = inputForPlane(input, isProviderQuantityObservation)
	default:
		return audit, fmt.Errorf("support shadow audit: unsupported basis %q", input.Basis)
	}
	input, err = canonicalizeRatingInput(input)
	if err != nil {
		return audit, err
	}
	qualifiers, err := effectiveQualifiers(rater.snapshot.EffectiveQualifiers, input.EffectiveQualifiers)
	if err != nil {
		return audit, err
	}
	selected := func(observation metering.Observation) bool {
		switch input.Basis {
		case economics.BasisProviderQuantityLocal:
			return observation.Origin == metering.OriginProvider
		case economics.BasisCustomerPolicy:
			return isRetailQuantityObservation(observation)
		default:
			return observation.Origin == metering.OriginLocal
		}
	}
	// The customer-policy plane narrows the reduction to the frozen contractual
	// basis; every other plane selects on origin alone and passes a nil mask.
	var mask retailComponentMask
	if input.Basis == economics.BasisCustomerPolicy {
		selection, selectionErr := selectRetailInferenceSelection(input.Observations)
		if selectionErr != nil {
			return audit, selectionErr
		}
		mask = selection.kept
	}
	exclusions := rater.includedChildExclusions(input.Observations, selected, mask, qualifiers)
	aggregates, _, _ := aggregateMeasures(input.Observations, input.Subject.StoreID, selected, mask, exclusionPredicate(exclusions))
	if len(aggregates) == 0 {
		return audit, nil
	}
	// Conservation is a property of the full exclusion-free reduction, so the
	// cover authority is handed that projection exactly as the rating loop does.
	consistency := aggregates
	if len(exclusions) != 0 {
		if full, _, fullErr := aggregateMeasures(input.Observations, input.Subject.StoreID, selected, mask, nil); fullErr == nil && full != nil {
			consistency = full
		}
	}
	covered, _, _, _ := rater.completeChildPartitionCoverage(consistency, qualifiers)
	// A covered, genuinely unpriced parent is removed from the economic context
	// used for whole-context selection, so the amounts below match the emitted
	// ones.
	contextAggregates := aggregates
	if len(covered) != 0 {
		contextAggregates = make([]aggregateMeasure, 0, len(aggregates))
		for _, item := range aggregates {
			if rater.suppressedCompletePartitionParent(covered, item, qualifiers) {
				continue
			}
			contextAggregates = append(contextAggregates, item)
		}
	}
	rateable := make(map[string]map[string]struct{})
	payable := make(map[string]map[string]struct{})
	for _, item := range aggregates {
		if isInformationalMeasure(item.key) || (item.complete && item.rat != nil && item.rat.Sign() == 0) {
			if _, probeErr := rater.resolveRule(item.key, qualifiers); errors.Is(probeErr, ErrRateMissing) {
				continue
			}
		}
		line, lineErr := rater.rateMeasureLine(item, contextAggregates, qualifiers, input, nil)
		if lineErr != nil {
			continue
		}
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		canonical := key.CanonicalKey()
		rateableScope := rateable[item.scopeKey]
		if rateableScope == nil {
			rateableScope = make(map[string]struct{})
			rateable[item.scopeKey] = rateableScope
		}
		rateableScope[canonical] = struct{}{}
		amount, ok := lineAmountRat(line)
		if !ok || amount.Sign() <= 0 {
			continue
		}
		scope := payable[item.scopeKey]
		if scope == nil {
			scope = make(map[string]struct{})
			payable[item.scopeKey] = scope
		}
		scope[canonical] = struct{}{}
	}
	// PRODUCTION'S OWN SIDE OF THE COMPARISON. The resolver's remaining special
	// cases are read straight off the resolver, over the very same populations, so
	// the agreement unit is exactly "did production withhold or refuse" rather than
	// a proxy read off the returned error class. Nothing here changes production:
	// the call is pure over the compiled program and the derived evidence, and its
	// results are only reported.
	dependencies := newCommercialDependencySet(func(key metering.ComponentKey) (economics.RatingRule, error) {
		return rater.resolveRule(key, qualifiers)
	})
	conflicts, _, _, _, overlapErr := rater.overlappingSchemaInclusionConflicts(
		payable, dependencies, rateable, consistency, covered,
	)
	audit.ProductionBlocked = overlapErr != nil
	for scope, keys := range conflicts {
		audit.ProductionConflicts[scope] = sortedCanonicalKeys(keys)
	}

	evidenceByScope := make(map[string][]aggregateMeasure)
	for _, item := range consistency {
		evidenceByScope[item.scopeKey] = append(evidenceByScope[item.scopeKey], item)
	}
	scopes := make([]string, 0, len(payable))
	for scope, keys := range payable {
		audit.Payable[scope] = sortedCanonicalKeys(keys)
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	for _, scope := range scopes {
		// The ONE cover authority, asked once per scope for the declared cover
		// verdicts the predicate's proven-disjointness branch reads.
		cover := resolveCompleteCovers(&rater.program, evidenceByScope[scope])
		resolutions := make(map[string]string, len(rater.program.coverParents))
		for _, parent := range rater.program.coverParents {
			resolutions[rater.program.keyStrings[parent]] = supportCoverResolutionName(cover.resolutionFor(rater.program.keyStrings[parent]))
		}
		audit.Covers[scope] = resolutions
		for _, pair := range shadowContributingRelations(&rater.program, cover, payable[scope]) {
			first, second := shadowOverlapPairKeys(&rater.program, pair)
			audit.Pairs = append(audit.Pairs, SupportOverlapPair{
				Scope:    scope,
				A:        first,
				B:        second,
				Relation: pair.relation.name(),
			})
		}
	}
	return audit, nil
}

// supportCoverResolutionName projects the one cover authority's tri-state to a
// stable string for the agreement report.
func supportCoverResolutionName(resolution coverResolution) string {
	switch resolution {
	case coverProven:
		return "proven"
	case coverAmbiguous:
		return "ambiguous"
	default:
		return "unresolved"
	}
}

// TestSupportPredicateHasExactlyOneProductionCaller proves mechanically that the
// relation predicate IS reachable from production, and from EXACTLY ONE production
// file, for EXACTLY ONE purpose.
//
// WHY THAT IS THE INVARIANT NOW. The predicate stopped being a shadow: it is the
// single authority for the structural overlap verdict, the resolver reaching every
// pair of positive contributors a scope declares a containment relation between and
// a relationDefiniteOverlap answer IS the conflict. The pre-retirement guard
// asserted the exact inverse -- no production caller at all -- so it is REPLACED,
// not deleted: the retirement it policed is done, and a second caller is that
// retired special case reintroduced from the other side.
//
// The scan covers the non-test sources of THIS package, which is the whole blast
// radius a package-internal predicate can have: an external caller would have to
// import the package and therefore use an exported name, and the predicate exports
// none. The declaring file is DISCOVERED, not named -- one file may claim the
// predicate's entry point, and it must then declare every name in the closed set,
// so the excluded file cannot quietly grow to hide a caller. The referencing file is
// discovered the same way, must declare the structural overlap verdict, and must
// carry EVERY reference inside that resolver's single payable-scope pass.
func TestSupportPredicateHasExactlyOneProductionCaller(t *testing.T) {
	t.Parallel()
	sources, err := supportPackageSources(false)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	declaring := supportShadowDeclaringFile(t, sources)
	for _, name := range strings.Split(supportShadowPredicateNames, "|") {
		if !strings.Contains(string(sources[declaring]), name) {
			t.Errorf("the declaring file %s does not declare %q; the excluded file is not the predicate's own home", declaring, name)
		}
	}
	caller, referenced := "", 0
	for name, source := range sources {
		if name == declaring {
			continue
		}
		lines := supportShadowPredicateReferences(source)
		if len(lines) == 0 {
			continue
		}
		if caller != "" {
			t.Errorf("production file %s:%d also names the predicate; it must have EXACTLY ONE production caller and %s already is one", name, lines[0], caller)
			continue
		}
		caller, referenced = name, len(lines)
	}
	if caller == "" {
		t.Fatal("no production file names the predicate; it is the structural overlap verdict's single authority and must be reachable from production")
	}
	lines := strings.Split(string(sources[caller]), "\n")
	if !strings.Contains(strings.Join(lines, "\n"), "func (r *ReferenceRater) overlappingSchemaInclusionConflicts(") {
		t.Errorf("the sole production caller %s does not declare the structural overlap verdict; the predicate's only production authority must be that verdict", caller)
	}
	// THE STRUCTURAL PASS, AND NOTHING ELSE. The pass is located by the gofmt-fixed
	// header line and bounded by indentation, so this scans the compiled shape
	// rather than a comment that could be edited into agreement.
	open, passes := -1, 0
	for index, line := range lines {
		if line == "\tfor _, scope := range payableScopes {" {
			open, passes = index, passes+1
		}
	}
	if passes != 1 {
		t.Fatalf("the sole production caller %s declares %d payable-scope passes; the structural verdict has exactly one home", caller, passes)
	}
	for _, line := range supportShadowPredicateReferences(sources[caller]) {
		if closed := supportBlockEnd(lines, open); line <= open || line > closed {
			t.Errorf("production file %s:%d names the predicate outside the payable-scope structural pass (lines %d-%d); the verdict has exactly one home", caller, line, open+1, closed)
		}
	}
	tests, err := supportPackageSources(true)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	exercised := 0
	for _, source := range tests {
		if strings.Contains(string(source), "shadowContributingRelations(") {
			exercised++
		}
	}
	if exercised == 0 {
		t.Error("no test file calls the test-only pair census; the predicate's own contract and the agreement harness would be silently unexercised")
	}
	t.Logf("SUPPORT relation predicate: declared in %s; called from %s only (%d references, all inside its payable-scope structural pass); test-only census driven by %d test files",
		declaring, caller, referenced, exercised)
}

// supportShadowDeclaringFile is the ONE file allowed to declare the predicate's
// entry point, discovered by looking for it rather than by a hard-coded path, so
// the exclusion cannot be widened quietly to hide a second production consumer.
func supportShadowDeclaringFile(t *testing.T, sources map[string][]byte) string {
	t.Helper()
	declaring := ""
	for name, source := range sources {
		if !strings.Contains(string(source), "func shadowContributingRelations(") {
			continue
		}
		if declaring != "" {
			t.Fatalf("the predicate is declared in both %s and %s", declaring, name)
		}
		declaring = name
	}
	if declaring == "" {
		t.Fatal("no production file declares the predicate; the single-authority proof would be vacuous")
	}
	return declaring
}

// supportPackageSources reads the sources of THIS package that are, or are not,
// test files, which is the whole scan surface a package-internal predicate has.
func supportPackageSources(wantTests bool) (map[string][]byte, error) {
	sources := map[string][]byte{}
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") != wantTests {
			continue
		}
		source, readErr := os.ReadFile(filepath.Clean(name))
		if readErr != nil {
			return nil, readErr
		}
		sources[name] = source
	}
	return sources, nil
}

// supportShadowPredicateReferences lists the 1-based lines of one source that name
// a shadow declaration or call a shadow receiver method.
func supportShadowPredicateReferences(source []byte) []int {
	names := strings.Split(supportShadowPredicateNames+"|"+supportShadowPredicateMethods, "|")
	var found []int
	for index, line := range strings.Split(string(source), "\n") {
		for _, name := range names {
			if strings.Contains(line, name) {
				found = append(found, index+1)
				break
			}
		}
	}
	return found
}

// supportBlockEnd is the last line of the block opened at open, found by the
// indentation gofmt fixes: a later non-blank line indented no deeper than the
// header has left the block.
func supportBlockEnd(lines []string, open int) int {
	indent := func(line string) int { return len(line) - len(strings.TrimLeft(line, " \t")) }
	depth := indent(lines[open])
	for index := open + 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != "" && indent(lines[index]) <= depth {
			return index
		}
	}
	return len(lines)
}

// ---------------------------------------------------------------------------
// Graph fixtures for the white-box contract tests.
// ---------------------------------------------------------------------------

const supportSchemaID = "support-shadow-v1"

// supportKey is the one component-identity factory for the white-box fixtures. A
// single schema-qualified, input-direction, token-unit namespace keeps the
// fixtures independent of component-name heuristics, exactly as every declared
// production graph is.
func supportKey(component string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: "vendor:support_" + component,
		Unit:      metering.UnitToken,
		SchemaID:  supportSchemaID,
	}
}

// supportCanonical is the canonical identity a fixture node takes, derived through
// the same Normalize/CanonicalKey path production uses, so the white-box tests
// never hand-build a JSON identity.
func supportCanonical(component string) string {
	key, err := supportKey(component).Normalize()
	if err != nil {
		panic(err)
	}
	return key.CanonicalKey()
}

func supportEdge(kind metering.RelationshipKind, parent, child string) metering.ComponentRelationship {
	parentKey, parentErr := supportKey(parent).Normalize()
	childKey, childErr := supportKey(child).Normalize()
	if parentErr != nil || childErr != nil {
		panic("supportEdge: invalid support component identity")
	}
	return metering.ComponentRelationship{Kind: kind, Parent: parentKey, Child: childKey}
}

// supportMeasures projects declared component quantities onto the reduced
// evidence the ONE cover authority reads. A component absent from the map is
// quantityAbsent, which is exactly what a provider never reported means.
func supportMeasures(quantities map[string]int64) []aggregateMeasure {
	items := make([]aggregateMeasure, 0, len(quantities))
	for _, component := range sortedInt64Keys(quantities) {
		key, err := supportKey(component).Normalize()
		if err != nil {
			panic(err)
		}
		items = append(items, aggregateMeasure{
			key:      key,
			scopeKey: "support-scope",
			complete: true,
			rat:      new(big.Rat).SetInt64(quantities[component]),
		})
	}
	return items
}

func sortedInt64Keys(values map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func supportPayable(components ...string) map[string]struct{} {
	payable := make(map[string]struct{}, len(components))
	for _, component := range components {
		payable[supportCanonical(component)] = struct{}{}
	}
	return payable
}

// supportRenderPairs renders the whole shadow answer for one declared graph, one
// quantity assignment and one payable population, as a deterministic text
// projection the determinism and quantity-invariance tests compare.
func supportRenderPairs(
	relationships []metering.ComponentRelationship,
	schemas []metering.ComponentSchema,
	quantities map[string]int64,
	payable map[string]struct{},
) string {
	if len(schemas) == 0 {
		schemas = []metering.ComponentSchema{{ID: supportSchemaID, Version: "1", Relationships: relationships}}
	}
	program := compileSchemaProgram(schemas)
	cover := resolveCompleteCovers(&program, supportMeasures(quantities))
	rendered := make([]string, 0, 4)
	for _, pair := range shadowContributingRelations(&program, cover, payable) {
		first, second := shadowOverlapPairKeys(&program, pair)
		rendered = append(rendered, first+" | "+second+" | "+pair.relation.name())
	}
	return strings.Join(rendered, "\n")
}

// ---------------------------------------------------------------------------
// The predicate's own contract, pinned directly against the compiled program.
// ---------------------------------------------------------------------------

// TestSupportPredicateThreeResults pins each of the three answers on the smallest
// graph that can produce it, so the vocabulary is executable rather than
// described.
//
// INVARIANT: the shadow predicate is a total, pure function of the compiled
// program and the scope's cover verdict, returning exactly one of three relations
// for every pair of DISTINCT positive contributors. Strict containment in either
// direction is a definite overlap, because the component rating vocabulary has no
// implicit surcharge to absorb it. A shared ancestor that no validated complete
// partition separates is an unknown intersection, which is non-blocking. Two
// contributors are provably disjoint only when the schema itself separates them:
// through a resolved cover's distinct branches, or by putting them under separate
// trees of the same containment forest.
func TestSupportPredicateThreeResults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		relationships []metering.ComponentRelationship
		quantities    map[string]int64
		positive      [2]string
		want          string
	}{
		{
			name:          "strict_containment_is_a_definite_overlap",
			relationships: []metering.ComponentRelationship{supportEdge(metering.RelationshipSubset, "parent", "child")},
			quantities:    map[string]int64{"parent": 2, "child": 1},
			positive:      [2]string{"child", "parent"},
			want:          SupportRelationDefiniteOverlap,
		},
		{
			name: "containment_across_a_class_change_is_the_same_relation",
			relationships: []metering.ComponentRelationship{
				supportEdge(metering.RelationshipSubset, "ancestor", "middle"),
				supportEdge(metering.RelationshipPartition, "middle", "leaf"),
			},
			quantities: map[string]int64{"ancestor": 3, "leaf": 1},
			positive:   [2]string{"ancestor", "leaf"},
			want:       SupportRelationDefiniteOverlap,
		},
		{
			name: "resolved_cover_separates_into_distinct_branches",
			relationships: []metering.ComponentRelationship{
				supportEdge(metering.RelationshipPartition, "parent", "left"),
				supportEdge(metering.RelationshipPartition, "parent", "right"),
			},
			quantities: map[string]int64{"parent": 2, "left": 1, "right": 1},
			positive:   [2]string{"left", "right"},
			want:       SupportRelationProvenDisjoint,
		},
		{
			name: "shared_ancestor_with_no_declared_cover_is_an_unknown_intersection",
			relationships: []metering.ComponentRelationship{
				supportEdge(metering.RelationshipSubset, "parent", "left"),
				supportEdge(metering.RelationshipSubset, "parent", "right"),
			},
			quantities: map[string]int64{"parent": 2, "left": 1, "right": 1},
			positive:   [2]string{"left", "right"},
			want:       SupportRelationUnknownIntersect,
		},
		{
			name: "an_unresolved_cover_separates_nothing",
			relationships: []metering.ComponentRelationship{
				supportEdge(metering.RelationshipPartition, "parent", "left"),
				supportEdge(metering.RelationshipPartition, "parent", "absent_member"),
				supportEdge(metering.RelationshipSubset, "parent", "extra"),
			},
			// A REQUIRED declared member is never reported, so the one cover
			// authority cannot resolve this coverage, and an unresolved cover
			// allocates nothing: the two positive regions share the parent and
			// nothing separates them.
			quantities: map[string]int64{"left": 1, "extra": 1},
			positive:   [2]string{"extra", "left"},
			want:       SupportRelationUnknownIntersect,
		},
		{
			name: "a_declared_child_is_in_its_own_branch",
			relationships: []metering.ComponentRelationship{
				supportEdge(metering.RelationshipPartition, "parent", "left"),
				supportEdge(metering.RelationshipPartition, "parent", "right"),
			},
			// Both declared members are reported exactly, so the coverage IS
			// resolved, and the two are the direct children it places apart.
			quantities: map[string]int64{"parent": 2, "left": 1, "right": 1},
			positive:   [2]string{"left", "right"},
			want:       SupportRelationProvenDisjoint,
		},
		{
			name: "separate_trees_of_one_forest_are_provably_disjoint",
			relationships: []metering.ComponentRelationship{
				supportEdge(metering.RelationshipPartition, "left", "left_leaf"),
				supportEdge(metering.RelationshipPartition, "right", "right_leaf"),
			},
			quantities: map[string]int64{"left_leaf": 1, "right_leaf": 1},
			positive:   [2]string{"left_leaf", "right_leaf"},
			want:       SupportRelationProvenDisjoint,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			program := compileSchemaProgram([]metering.ComponentSchema{{
				ID: supportSchemaID, Version: "1", Relationships: tc.relationships,
			}})
			cover := resolveCompleteCovers(&program, supportMeasures(tc.quantities))
			pairs := shadowContributingRelations(&program, cover, supportPayable(tc.positive[:]...))
			if len(pairs) != 1 {
				t.Fatalf("pairs=%d, want exactly one distinct positive pair", len(pairs))
			}
			if got := pairs[0].relation.name(); got != tc.want {
				t.Errorf("relation=%s, want %s", got, tc.want)
			}
			first, second := shadowOverlapPairKeys(&program, pairs[0])
			if first != supportCanonical(tc.positive[0]) || second != supportCanonical(tc.positive[1]) {
				t.Errorf("pair identities=(%s,%s), want the ascending-node-id order %v", first, second, tc.positive)
			}
		})
	}
}

// TestSupportPredicateContributorsAreCanonicalLinesNotPaths pins requirement 2.
//
// INVARIANT: contributor identity is the canonical component line, which IS the
// compiled node id, so a region reachable by SEVERAL declared paths is one
// contributor. A diamond therefore yields no self-pair, and a redundant path can
// never manufacture a conflict against the region it reaches.
func TestSupportPredicateContributorsAreCanonicalLinesNotPaths(t *testing.T) {
	t.Parallel()
	// left and right both reach shared, by two declared paths apiece. A PATH walk
	// would report the region four times and pair it with itself.
	relationships := []metering.ComponentRelationship{
		supportEdge(metering.RelationshipPartition, "left", "shared"),
		supportEdge(metering.RelationshipSubset, "left", "shared"),
		supportEdge(metering.RelationshipPartition, "right", "shared"),
		supportEdge(metering.RelationshipSubset, "right", "shared"),
	}
	quantities := map[string]int64{"shared": 4}
	program := compileSchemaProgram([]metering.ComponentSchema{{ID: supportSchemaID, Version: "1", Relationships: relationships}})
	cover := resolveCompleteCovers(&program, supportMeasures(quantities))

	if pairs := shadowContributingRelations(&program, cover, supportPayable("shared")); len(pairs) != 0 {
		t.Fatalf("pairs=%+v, want none: one contributor cannot intersect itself", pairs)
	}
	pairs := shadowContributingRelations(&program, cover, supportPayable("left", "shared"))
	if len(pairs) != 1 {
		t.Fatalf("pairs=%d, want exactly one pair: the shared region is ONE contributor reachable by four declared paths", len(pairs))
	}
	if got := pairs[0].relation.name(); got != SupportRelationDefiniteOverlap {
		t.Errorf("relation=%s, want %s: left strictly contains the shared region", got, SupportRelationDefiniteOverlap)
	}
	if positive := shadowPositiveContributors(&program, supportPayable("left", "shared")); len(positive) != 2 {
		t.Errorf("positive contributors=%d, want 2: identity is the canonical line, not the path", len(positive))
	}
}

// TestSupportPredicateIsDeterministicUnderDeclarationOrder pins requirement 4.
//
// INVARIANT: the predicate's answer is a function of the compiled program, and
// the compiled program is independent of schema and relationship DECLARATION
// order. Reversing the declared relationship slice and appending an empty second
// schema must produce the same pair list, in the same order, with the same
// relations and the same identities.
func TestSupportPredicateIsDeterministicUnderDeclarationOrder(t *testing.T) {
	t.Parallel()
	forward := []metering.ComponentRelationship{
		supportEdge(metering.RelationshipSubset, "ancestor", "left"),
		supportEdge(metering.RelationshipSubset, "ancestor", "right"),
		supportEdge(metering.RelationshipPartition, "left", "leaf"),
		supportEdge(metering.RelationshipPartition, "right", "other_leaf"),
	}
	reversed := make([]metering.ComponentRelationship, len(forward))
	for index := range forward {
		reversed[len(forward)-1-index] = forward[index]
	}
	quantities := map[string]int64{"ancestor": 5, "left": 3, "right": 2, "leaf": 1, "other_leaf": 1}
	payable := supportPayable("ancestor", "left", "right", "leaf", "other_leaf")

	baseline := supportRenderPairs(forward, nil, quantities, payable)
	if baseline == "" {
		t.Fatal("the forward declaration produced no pair at all; the test proves nothing")
	}
	alternative := supportRenderPairs(reversed, []metering.ComponentSchema{
		{ID: supportSchemaID, Version: "1", Relationships: reversed},
		{ID: supportSchemaID + "_empty", Version: "1"},
	}, quantities, payable)
	if baseline != alternative {
		t.Errorf("declaration order reached the shadow answer:\nforward:\n%s\nreversed+empty:\n%s", baseline, alternative)
	}
}

// TestSupportPredicateFixedFeeIsNotARegion pins requirement 3.
//
// INVARIANT: a fixed fee is outside the component graph entirely. The compiled
// program holds component relationships only, so a payable fixed-fee rule has no
// node, never enters the positive contributor list, and can never form a pair --
// even when a caller offers its identity as if it were a payable region.
func TestSupportPredicateFixedFeeIsNotARegion(t *testing.T) {
	t.Parallel()
	relationships := []metering.ComponentRelationship{
		supportEdge(metering.RelationshipSubset, "parent", "child"),
	}
	program := compileSchemaProgram([]metering.ComponentSchema{{ID: supportSchemaID, Version: "1", Relationships: relationships}})
	cover := resolveCompleteCovers(&program, supportMeasures(map[string]int64{"parent": 2, "child": 1}))

	// The fixed fee is the only other money in the call, and its identity is
	// offered to the predicate exactly as a payable component line's would be.
	const fixedFeeIdentity = "fixed-fee:call:support-shadow-v1"
	payable := map[string]struct{}{
		supportCanonical("parent"): {},
		supportCanonical("child"):  {},
		fixedFeeIdentity:           {},
	}
	if positive := shadowPositiveContributors(&program, payable); len(positive) != 2 {
		t.Errorf("positive contributors=%d, want 2: a fixed fee has no node in the component graph", len(positive))
	}
	for _, node := range program.keyStrings {
		if node == fixedFeeIdentity {
			t.Fatal("a fixed fee must never be a compiled node")
		}
	}
	pairs := shadowContributingRelations(&program, cover, payable)
	if len(pairs) != 1 {
		t.Fatalf("pairs=%d, want exactly one: the fixed fee contributes no region", len(pairs))
	}
	if pairs[0].relation.name() != SupportRelationDefiniteOverlap {
		t.Errorf("relation=%s, want %s for the parent/child pair", pairs[0].relation.name(), SupportRelationDefiniteOverlap)
	}
	first, second := shadowOverlapPairKeys(&program, pairs[0])
	if first == fixedFeeIdentity || second == fixedFeeIdentity {
		t.Errorf("pair=(%s,%s) names the fixed fee as a region", first, second)
	}
}

// TestSupportPredicateInventsNoQuantity pins requirement 5.
//
// INVARIANT: the predicate answers a RELATION and nothing else. It returns no
// amount, no intersection value and no cross product: the pair count is exactly
// the number of unordered contributor pairs, never a product of quantities, and
// the answer is unchanged when every quantity is rescaled.
func TestSupportPredicateInventsNoQuantity(t *testing.T) {
	t.Parallel()
	relationships := []metering.ComponentRelationship{
		supportEdge(metering.RelationshipSubset, "ancestor", "left"),
		supportEdge(metering.RelationshipSubset, "ancestor", "right"),
		supportEdge(metering.RelationshipSubset, "ancestor", "third"),
	}
	scale := func(factor int64) []string {
		quantities := map[string]int64{
			"ancestor": 3 * factor, "left": 2 * factor, "right": 1 * factor, "third": 4 * factor,
		}
		program := compileSchemaProgram([]metering.ComponentSchema{{ID: supportSchemaID, Version: "1", Relationships: relationships}})
		cover := resolveCompleteCovers(&program, supportMeasures(quantities))
		rendered := make([]string, 0, 3)
		for _, pair := range shadowContributingRelations(&program, cover, supportPayable("left", "right", "third")) {
			first, second := shadowOverlapPairKeys(&program, pair)
			rendered = append(rendered, first+" | "+second+" | "+pair.relation.name())
		}
		return rendered
	}
	unit, scaled := scale(1), scale(1000)
	if strings.Join(unit, "\n") != strings.Join(scaled, "\n") {
		t.Errorf("rescaling every quantity changed the relation:\nunit:\n%s\nscaled:\n%s",
			strings.Join(unit, "\n"), strings.Join(scaled, "\n"))
	}
	// Three positive contributors are three unordered pairs: no fourth
	// combination, because a contributor is never compared with itself and no
	// ordered pair is ever produced.
	if len(unit) != 3 {
		t.Errorf("pairs=%d, want exactly 3 (one per unordered contributor pair of 3 contributors)", len(unit))
	}
}
