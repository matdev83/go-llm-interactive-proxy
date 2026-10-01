package billing

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const advisoryOracleScope = "advisory-scope"

func advisoryOracleKey(name string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: "vendor:advisory_" + name,
		Unit:      metering.UnitToken,
		SchemaID:  "advisory-oracle-v1",
	}
}

func advisoryOracleEdge(kind metering.RelationshipKind, parent, child string) metering.ComponentRelationship {
	return metering.ComponentRelationship{Kind: kind, Parent: advisoryOracleKey(parent), Child: advisoryOracleKey(child)}
}

func advisoryOracleCanonical(name string) string {
	return advisoryOracleKey(name).CanonicalKey()
}

func advisoryOracleContext() economics.Valuation {
	context := economics.SupportAdvisoryContext{
		Version: economics.SupportAdvisoryVersionV1,
		Tariff: economics.RatingSnapshotRef{
			VersionRef: economics.VersionRef{ID: "advisory-tariff", Version: "1"},
			RaterID:    "advisory-rater",
		},
		TariffContent: economics.SnapshotContentRef{
			ContentRef:  "advisory-tariff-content",
			ContentHash: strings.Repeat("a", economics.SnapshotContentHashBytes),
		},
	}
	return economics.Valuation{SupportAdvisoryContexts: []economics.SupportAdvisoryContext{context}}
}

func advisoryOracleProgram(edges ...metering.ComponentRelationship) schemaProgram {
	return compileSchemaProgram([]metering.ComponentSchema{{ID: "advisory-oracle-v1", Version: "1", Relationships: edges}})
}

func advisoryOracleContributors(names ...string) map[string]map[string]struct{} {
	contributors := make(map[string]struct{}, len(names))
	for _, name := range names {
		contributors[advisoryOracleCanonical(name)] = struct{}{}
	}
	return map[string]map[string]struct{}{advisoryOracleScope: contributors}
}

func advisoryOracleCoverage(program *schemaProgram, resolvedParents ...string) map[string]completeCoverVerdict {
	verdict := completeCoverVerdict{program: program, resolved: make([]bool, len(program.keyOf))}
	for _, parent := range resolvedParents {
		verdict.resolved[program.byKey[advisoryOracleCanonical(parent)]] = true
	}
	return map[string]completeCoverVerdict{advisoryOracleScope: verdict}
}

func advisoryOracleLimits(candidateExaminations, graphVisits, pairs int) supportAssessmentLimits {
	return supportAssessmentLimits{
		candidateExaminations: candidateExaminations,
		graphVisits:           graphVisits,
		pairs:                 pairs,
	}
}

func advisoryOraclePairFixture(pairCount int) (schemaProgram, map[string]map[string]struct{}) {
	var edges []metering.ComponentRelationship
	contributors := make(map[string]struct{}, pairCount*2)
	for pair := range pairCount {
		parent := fmt.Sprintf("pair-%03d-parent", pair)
		left := fmt.Sprintf("pair-%03d-left", pair)
		right := fmt.Sprintf("pair-%03d-right", pair)
		edges = append(edges,
			advisoryOracleEdge(metering.RelationshipSubset, parent, left),
			advisoryOracleEdge(metering.RelationshipSubset, parent, right),
		)
		contributors[advisoryOracleCanonical(left)] = struct{}{}
		contributors[advisoryOracleCanonical(right)] = struct{}{}
	}
	var schemas []metering.ComponentSchema
	for start, schemaNumber := 0, 0; start < len(edges); start, schemaNumber = start+metering.MaxComponentSchemaRelationships, schemaNumber+1 {
		end := min(start+metering.MaxComponentSchemaRelationships, len(edges))
		schemas = append(schemas, metering.ComponentSchema{
			ID:            fmt.Sprintf("advisory-oracle-%d", schemaNumber),
			Version:       "1",
			Relationships: edges[start:end],
		})
	}
	program := compileSchemaProgram(schemas)
	return program, map[string]map[string]struct{}{advisoryOracleScope: contributors}
}

func advisoryOraclePairLabels(report *economics.SupportAdvisoryReport) []string {
	if report == nil || len(report.Pairs) == 0 {
		return nil
	}
	labels := make([]string, 0, len(report.Pairs))
	for _, pair := range report.Pairs {
		labels = append(labels, pair.Left.Component+"|"+pair.Right.Component)
	}
	return labels
}

type independentAdvisoryPair struct {
	label   string
	unknown bool
}

func independentStrictReachability(adjacency map[string][]string, start string) map[string]struct{} {
	seen := make(map[string]struct{})
	queue := append([]string(nil), adjacency[start]...)
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		if _, exists := seen[node]; exists {
			continue
		}
		seen[node] = struct{}{}
		queue = append(queue, adjacency[node]...)
	}
	return seen
}

// independentAdvisoryOracle derives candidate and unknown relations directly
// from the declared edge list. It intentionally does not call the production
// shadow predicate, compiled closures, or candidate enumerator.
func independentAdvisoryOracle(names []string, edges []metering.ComponentRelationship) []independentAdvisoryPair {
	adjacency := make(map[string][]string, len(names))
	for _, edge := range edges {
		adjacency[edge.Parent.Component] = append(adjacency[edge.Parent.Component], edge.Child.Component)
	}
	reverse := reverseAdvisoryAdjacency(adjacency, names)
	ancestors := make(map[string]map[string]struct{}, len(names))
	descendants := make(map[string]map[string]struct{}, len(names))
	reflexive := make(map[string]map[string]struct{}, len(names))
	for _, name := range names {
		ancestors[name] = independentStrictReachability(reverse, name)
		descendants[name] = independentStrictReachability(adjacency, name)
		reflexive[name] = make(map[string]struct{}, len(ancestors[name])+1)
		reflexive[name][name] = struct{}{}
		for ancestor := range ancestors[name] {
			reflexive[name][ancestor] = struct{}{}
		}
	}
	var pairs []independentAdvisoryPair
	for leftIndex, left := range names {
		for _, right := range names[leftIndex+1:] {
			commonCandidate := intersects(reflexive[left], reflexive[right])
			if !commonCandidate {
				continue
			}
			_, leftContainsRight := descendants[left][right]
			_, rightContainsLeft := descendants[right][left]
			unknown := !leftContainsRight && !rightContainsLeft && intersects(ancestors[left], ancestors[right])
			pairs = append(pairs, independentAdvisoryPair{
				label:   advisoryOracleKey(strings.TrimPrefix(left, "vendor:advisory_")).Component + "|" + advisoryOracleKey(strings.TrimPrefix(right, "vendor:advisory_")).Component,
				unknown: unknown,
			})
		}
	}
	return pairs
}

func reverseAdvisoryAdjacency(adjacency map[string][]string, names []string) map[string][]string {
	reverse := make(map[string][]string, len(names))
	for _, name := range names {
		reverse[name] = nil
	}
	for parent, children := range adjacency {
		for _, child := range children {
			reverse[child] = append(reverse[child], parent)
		}
	}
	return reverse
}

func intersects(a, b map[string]struct{}) bool {
	for value := range a {
		if _, exists := b[value]; exists {
			return true
		}
	}
	return false
}

func independentUnknownLabels(pairs []independentAdvisoryPair) []string {
	var labels []string
	for _, pair := range pairs {
		if pair.unknown {
			labels = append(labels, pair.label)
		}
	}
	return labels
}

func independentUnknownPrefixForCandidateLimit(pairs []independentAdvisoryPair, limit int) ([]string, bool) {
	var labels []string
	examined := 0
	for _, pair := range pairs {
		if examined >= limit {
			return labels, true
		}
		examined++
		if pair.unknown {
			labels = append(labels, pair.label)
		}
	}
	return labels, false
}

func advisoryOracleIncompleteReason(report *economics.SupportAdvisoryReport) (economics.SupportAdvisoryReason, bool) {
	if report == nil || len(report.IncompleteContexts) == 0 {
		return "", false
	}
	return report.IncompleteContexts[0].Reason, true
}

func TestComponentRaterSupportAdvisorySiblingAndPartitionRegression(t *testing.T) {
	t.Parallel()
	t.Run("unresolved_subset_siblings_are_unknown", func(t *testing.T) {
		t.Parallel()
		program := advisoryOracleProgram(
			advisoryOracleEdge(metering.RelationshipSubset, "parent", "left"),
			advisoryOracleEdge(metering.RelationshipSubset, "parent", "right"),
		)
		rater := &ReferenceRater{program: program}
		got := rater.assessSupportUncertainty(advisoryOracleContext(), advisoryOracleContributors("left", "right"), advisoryOracleCoverage(&rater.program))
		if got == nil || len(got.Pairs) != 1 {
			t.Fatalf("report=%+v, want one unknown sibling pair", got)
		}
		if got.Pairs[0].Left.CanonicalKey() != advisoryOracleCanonical("left") || got.Pairs[0].Right.CanonicalKey() != advisoryOracleCanonical("right") {
			t.Fatalf("pair=(%s,%s), want canonical left/right", got.Pairs[0].Left.CanonicalKey(), got.Pairs[0].Right.CanonicalKey())
		}
	})
	t.Run("resolved_partition_branch_roots_are_separated", func(t *testing.T) {
		t.Parallel()
		program := advisoryOracleProgram(
			advisoryOracleEdge(metering.RelationshipPartition, "parent", "left"),
			advisoryOracleEdge(metering.RelationshipPartition, "parent", "right"),
		)
		rater := &ReferenceRater{program: program}
		got := rater.assessSupportUncertainty(advisoryOracleContext(), advisoryOracleContributors("left", "right"), advisoryOracleCoverage(&rater.program, "parent"))
		if got != nil {
			t.Fatalf("report=%+v, want no pair for resolved partition branch roots", got)
		}
	})
}

func TestComponentRaterSupportAdvisoryStructuralOracle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		edges           []metering.ComponentRelationship
		contributors    []string
		resolvedParents []string
		wantPairLabels  []string
		wantCandidateN  int
	}{
		{
			name: "strict_containment_is_not_reported",
			edges: []metering.ComponentRelationship{
				advisoryOracleEdge(metering.RelationshipSubset, "parent", "child"),
			},
			contributors: []string{"parent", "child"},
		},
		{
			name: "separate_forest_trees_are_not_reported",
			edges: []metering.ComponentRelationship{
				advisoryOracleEdge(metering.RelationshipSubset, "left-root", "left-leaf"),
				advisoryOracleEdge(metering.RelationshipSubset, "right-root", "right-leaf"),
			},
			contributors:   []string{"left-leaf", "right-leaf"},
			wantCandidateN: 0,
		},
		{
			name: "unresolved_partition_does_not_separate_siblings",
			edges: []metering.ComponentRelationship{
				advisoryOracleEdge(metering.RelationshipPartition, "parent", "left"),
				advisoryOracleEdge(metering.RelationshipPartition, "parent", "right"),
			},
			contributors:   []string{"left", "right"},
			wantPairLabels: []string{"vendor:advisory_left|vendor:advisory_right"},
		},
		{
			name: "resolved_partition_separates_branch_roots_and_descendants",
			edges: []metering.ComponentRelationship{
				advisoryOracleEdge(metering.RelationshipPartition, "parent", "left"),
				advisoryOracleEdge(metering.RelationshipPartition, "parent", "right"),
				advisoryOracleEdge(metering.RelationshipSubset, "left", "left-leaf"),
				advisoryOracleEdge(metering.RelationshipSubset, "right", "right-leaf"),
			},
			contributors:    []string{"left", "right", "left-leaf", "right-leaf"},
			resolvedParents: []string{"parent"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			program := advisoryOracleProgram(test.edges...)
			rater := &ReferenceRater{program: program}
			coverage := advisoryOracleCoverage(&rater.program, test.resolvedParents...)
			report, stats := rater.assessSupportUncertaintyWithLimits(
				advisoryOracleContext(),
				advisoryOracleContributors(test.contributors...),
				coverage,
				advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs),
			)
			if got := advisoryOraclePairLabels(report); !reflect.DeepEqual(got, test.wantPairLabels) {
				t.Errorf("pairs=%v, want independent structural oracle %v", got, test.wantPairLabels)
			}
			if test.wantCandidateN != 0 && stats.candidateExaminations != test.wantCandidateN {
				t.Errorf("candidate examinations=%d, want %d", stats.candidateExaminations, test.wantCandidateN)
			}
		})
	}
}

func TestComponentRaterSupportAdvisoryExhaustiveFourNodeOracleAndBudgetPrefixes(t *testing.T) {
	t.Parallel()
	nodeNames := []string{"vendor:advisory_node-0", "vendor:advisory_node-1", "vendor:advisory_node-2", "vendor:advisory_node-3"}
	pairs := [][2]int{{0, 1}, {0, 2}, {0, 3}, {1, 2}, {1, 3}, {2, 3}}
	for mask := range 1 << len(pairs) {
		var edges []metering.ComponentRelationship
		used := make(map[string]struct{})
		for edgeIndex, pair := range pairs {
			if mask&(1<<edgeIndex) == 0 {
				continue
			}
			parent, child := nodeNames[pair[0]], nodeNames[pair[1]]
			parentName := strings.TrimPrefix(parent, "vendor:advisory_")
			childName := strings.TrimPrefix(child, "vendor:advisory_")
			edges = append(edges, advisoryOracleEdge(metering.RelationshipSubset, parentName, childName))
			used[parent], used[child] = struct{}{}, struct{}{}
		}
		names := make([]string, 0, len(used))
		contributorMap := make(map[string]struct{}, len(used))
		for name := range used {
			names = append(names, name)
			contributorMap[advisoryOracleKey(strings.TrimPrefix(name, "vendor:advisory_")).CanonicalKey()] = struct{}{}
		}
		slices.Sort(names)
		program := advisoryOracleProgram(edges...)
		rater := &ReferenceRater{program: program}
		valuation := advisoryOracleContext()
		contributors := map[string]map[string]struct{}{advisoryOracleScope: contributorMap}
		coverage := advisoryOracleCoverage(&rater.program)
		oracle := independentAdvisoryOracle(names, edges)
		wantAll := independentUnknownLabels(oracle)

		unlimited := advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs)
		full, _ := rater.assessSupportUncertaintyWithLimits(valuation, contributors, coverage, unlimited)
		if got := advisoryOraclePairLabels(full); !reflect.DeepEqual(got, wantAll) {
			t.Fatalf("graph mask %06b full pairs=%v, independent oracle=%v", mask, got, wantAll)
		}

		for _, candidateLimit := range []int{0, 1, 3, 6} {
			want, overflow := independentUnknownPrefixForCandidateLimit(oracle, candidateLimit)
			report, _ := rater.assessSupportUncertaintyWithLimits(valuation, contributors, coverage,
				advisoryOracleLimits(candidateLimit, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs))
			if got := advisoryOraclePairLabels(report); !reflect.DeepEqual(got, want) {
				t.Fatalf("graph mask %06b candidate limit %d pairs=%v, oracle prefix=%v", mask, candidateLimit, got, want)
			}
			reason, incomplete := advisoryOracleIncompleteReason(report)
			if incomplete != overflow || incomplete && reason != economics.SupportAdvisoryCandidateBudget {
				t.Fatalf("graph mask %06b candidate limit %d incomplete=(%t,%q), want overflow=%t", mask, candidateLimit, incomplete, reason, overflow)
			}
		}

		for _, graphLimit := range []int{0, 8, 32, 128, economics.MaxSupportAdvisoryGraphVisits} {
			report, _ := rater.assessSupportUncertaintyWithLimits(valuation, contributors, coverage,
				advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, graphLimit, economics.MaxSupportAdvisoryPairs))
			got := advisoryOraclePairLabels(report)
			if len(got) > len(wantAll) || !slices.Equal(got, wantAll[:len(got)]) {
				t.Fatalf("graph mask %06b graph limit %d pairs=%v, not an oracle prefix %v", mask, graphLimit, got, wantAll)
			}
			reason, incomplete := advisoryOracleIncompleteReason(report)
			if incomplete && reason != economics.SupportAdvisoryGraphBudget {
				t.Fatalf("graph mask %06b graph limit %d reason=%q, want graph_budget", mask, graphLimit, reason)
			}
			if !incomplete && !reflect.DeepEqual(got, wantAll) {
				t.Fatalf("graph mask %06b graph limit %d reported clean with pairs=%v, want %v", mask, graphLimit, got, wantAll)
			}
		}
	}
}

func TestComponentRaterSupportAdvisoryDiamondDeduplicatesCandidates(t *testing.T) {
	t.Parallel()
	program := advisoryOracleProgram(
		advisoryOracleEdge(metering.RelationshipSubset, "root-a", "left"),
		advisoryOracleEdge(metering.RelationshipSubset, "root-a", "right"),
		advisoryOracleEdge(metering.RelationshipSubset, "root-b", "left"),
		advisoryOracleEdge(metering.RelationshipSubset, "root-b", "right"),
	)
	rater := &ReferenceRater{program: program}
	report, stats := rater.assessSupportUncertaintyWithLimits(
		advisoryOracleContext(), advisoryOracleContributors("left", "right"), advisoryOracleCoverage(&rater.program),
		advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs),
	)
	if got := advisoryOraclePairLabels(report); !reflect.DeepEqual(got, []string{"vendor:advisory_left|vendor:advisory_right"}) {
		t.Fatalf("pairs=%v, want one deduplicated diamond pair", got)
	}
	if stats.candidateExaminations != 1 || stats.duplicateCandidateVisits == 0 {
		t.Fatalf("stats=%+v, want one relation examination and charged duplicate visits", stats)
	}
}

func TestComponentRaterSupportAdvisoryScopeAndDeclarationOrder(t *testing.T) {
	t.Parallel()
	edges := []metering.ComponentRelationship{
		advisoryOracleEdge(metering.RelationshipSubset, "parent-a", "a-left"),
		advisoryOracleEdge(metering.RelationshipSubset, "parent-a", "a-right"),
		advisoryOracleEdge(metering.RelationshipSubset, "parent-z", "z-left"),
		advisoryOracleEdge(metering.RelationshipSubset, "parent-z", "z-right"),
	}
	input := map[string]map[string]struct{}{
		"scope-z": {advisoryOracleCanonical("z-right"): {}, advisoryOracleCanonical("z-left"): {}},
		"scope-a": {advisoryOracleCanonical("a-right"): {}, advisoryOracleCanonical("a-left"): {}},
	}
	assess := func(edges []metering.ComponentRelationship, contributors map[string]map[string]struct{}) *economics.SupportAdvisoryReport {
		program := advisoryOracleProgram(edges...)
		rater := &ReferenceRater{program: program}
		cover := completeCoverVerdict{program: &rater.program, resolved: make([]bool, len(rater.program.keyOf))}
		coverage := map[string]completeCoverVerdict{"scope-a": cover, "scope-z": cover}
		return rater.assessSupportUncertainty(advisoryOracleContext(), contributors, coverage)
	}
	first := assess(edges, input)
	reversed := append([]metering.ComponentRelationship(nil), edges...)
	slices.Reverse(reversed)
	secondInput := map[string]map[string]struct{}{
		"scope-a": {advisoryOracleCanonical("a-left"): {}, advisoryOracleCanonical("a-right"): {}},
		"scope-z": {advisoryOracleCanonical("z-left"): {}, advisoryOracleCanonical("z-right"): {}},
	}
	second := assess(reversed, secondInput)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("report depends on declaration or map order:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if got, want := advisoryOraclePairLabels(first), []string{"vendor:advisory_a-left|vendor:advisory_a-right", "vendor:advisory_z-left|vendor:advisory_z-right"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered pairs=%v, want %v", got, want)
	}

	isolation := map[string]map[string]struct{}{
		"scope-a": {advisoryOracleCanonical("a-left"): {}},
		"scope-z": {advisoryOracleCanonical("a-right"): {}},
	}
	if got := assess(edges, isolation); got != nil {
		t.Fatalf("cross-scope contributors created a pair: %+v", got)
	}
}

func TestComponentRaterSupportAdvisoryMissingCoverageIsIncomplete(t *testing.T) {
	t.Parallel()
	program := advisoryOracleProgram(
		advisoryOracleEdge(metering.RelationshipSubset, "parent", "left"),
		advisoryOracleEdge(metering.RelationshipSubset, "parent", "right"),
	)
	rater := &ReferenceRater{program: program}
	got := rater.assessSupportUncertainty(advisoryOracleContext(), advisoryOracleContributors("left", "right"), nil)
	if got == nil || len(got.Pairs) != 0 || len(got.IncompleteContexts) != 1 || got.IncompleteContexts[0].Reason != economics.SupportAdvisoryEvidenceUnavailable {
		t.Fatalf("report=%+v, want evidence_unavailable with no pair", got)
	}
}

func TestComponentRaterSupportAdvisoryBudgetsRetainCanonicalPrefix(t *testing.T) {
	t.Parallel()
	program, contributors := advisoryOraclePairFixture(2)
	rater := &ReferenceRater{program: program}
	cover := completeCoverVerdict{program: &rater.program, resolved: make([]bool, len(rater.program.keyOf))}
	coverage := map[string]completeCoverVerdict{advisoryOracleScope: cover}
	report, stats := rater.assessSupportUncertaintyWithLimits(
		advisoryOracleContext(), contributors, coverage,
		advisoryOracleLimits(1, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs),
	)
	want := []string{"vendor:advisory_pair-000-left|vendor:advisory_pair-000-right"}
	if got := advisoryOraclePairLabels(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("pairs=%v, want retained canonical prefix %v", got, want)
	}
	if stats.candidateExaminations != 1 || len(report.IncompleteContexts) != 1 || report.IncompleteContexts[0].Reason != economics.SupportAdvisoryCandidateBudget {
		t.Fatalf("stats=%+v report=%+v, want candidate_budget after first completed pair", stats, report)
	}

	partial, partialStats := rater.assessSupportUncertaintyWithLimits(
		advisoryOracleContext(), contributors, coverage,
		advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, 1, economics.MaxSupportAdvisoryPairs),
	)
	if partial == nil || len(partial.Pairs) != 0 || len(partial.IncompleteContexts) != 1 || partial.IncompleteContexts[0].Reason != economics.SupportAdvisoryGraphBudget || partialStats.graphVisits != 1 {
		t.Fatalf("partial report=%+v stats=%+v, want graph_budget, no pair, and one consumed graph visit", partial, partialStats)
	}
}

func TestComponentRaterSupportAdvisoryBudgetsAreGroupWideAcrossScopes(t *testing.T) {
	t.Parallel()
	program, oneScope := advisoryOraclePairFixture(1)
	rater := &ReferenceRater{program: program}
	population := oneScope[advisoryOracleScope]
	cover := completeCoverVerdict{program: &rater.program, resolved: make([]bool, len(rater.program.keyOf))}
	baseLimits := advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs)
	_, oneScopeStats := rater.assessSupportUncertaintyWithLimits(
		advisoryOracleContext(),
		map[string]map[string]struct{}{"scope-a": population},
		map[string]completeCoverVerdict{"scope-a": cover},
		baseLimits,
	)
	twoScopes := map[string]map[string]struct{}{"scope-a": population, "scope-b": population}
	coverage := map[string]completeCoverVerdict{"scope-a": cover, "scope-b": cover}

	candidateLimited, candidateStats := rater.assessSupportUncertaintyWithLimits(
		advisoryOracleContext(), twoScopes, coverage,
		advisoryOracleLimits(1, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs),
	)
	if candidateLimited == nil || len(candidateLimited.Pairs) != 1 || candidateLimited.Pairs[0].ScopeKey != "scope-a" || len(candidateLimited.IncompleteContexts) != 1 || candidateLimited.IncompleteContexts[0].Reason != economics.SupportAdvisoryCandidateBudget || candidateStats.candidateExaminations != 1 {
		t.Fatalf("candidate-limited report=%+v stats=%+v, want only first-scope pair and group-wide candidate_budget", candidateLimited, candidateStats)
	}

	graphLimited, graphStats := rater.assessSupportUncertaintyWithLimits(
		advisoryOracleContext(), twoScopes, coverage,
		advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, oneScopeStats.graphVisits, economics.MaxSupportAdvisoryPairs),
	)
	if graphLimited == nil || len(graphLimited.Pairs) != 1 || graphLimited.Pairs[0].ScopeKey != "scope-a" || len(graphLimited.IncompleteContexts) != 1 || graphLimited.IncompleteContexts[0].Reason != economics.SupportAdvisoryGraphBudget || graphStats.graphVisits != oneScopeStats.graphVisits {
		t.Fatalf("graph-limited report=%+v stats=%+v, want first-scope pair and group-wide graph_budget at %d visits", graphLimited, graphStats, oneScopeStats.graphVisits)
	}
}

func TestComponentRaterSupportAdvisoryPartialBranchClosureIsNotProof(t *testing.T) {
	t.Parallel()
	program := advisoryOracleProgram(
		advisoryOracleEdge(metering.RelationshipPartition, "parent", "left"),
		advisoryOracleEdge(metering.RelationshipPartition, "parent", "right"),
		advisoryOracleEdge(metering.RelationshipSubset, "left", "left-leaf"),
		advisoryOracleEdge(metering.RelationshipSubset, "right", "right-leaf"),
	)
	rater := &ReferenceRater{program: program}
	coverage := advisoryOracleCoverage(&rater.program, "parent")
	var stats supportAssessmentStats
	queries := newSupportAssessmentQueries(
		&rater.program,
		coverage[advisoryOracleScope],
		advisoryOracleLimits(10, 4, 10),
		&stats,
	)
	left := rater.program.byKey[advisoryOracleCanonical("left")]
	leftLeaf := rater.program.byKey[advisoryOracleCanonical("left-leaf")]
	rightLeaf := rater.program.byKey[advisoryOracleCanonical("right-leaf")]
	separated, assessed := queries.partitionSeparates(leftLeaf, rightLeaf)
	if assessed || separated || queries.cache[allInclusionEdges][left] != nil {
		t.Fatalf("separated=%t assessed=%t cache=%+v, want unassessed and no partial branch closure", separated, assessed, queries.cache[allInclusionEdges][left])
	}
	if stats.graphVisits != 4 {
		t.Fatalf("graph visits=%d, want budget exhaustion during the branch closure", stats.graphVisits)
	}
}

func TestComponentRaterSupportAdvisoryPairLimit128And129(t *testing.T) {
	t.Parallel()
	for _, pairCount := range []int{economics.MaxSupportAdvisoryPairs, economics.MaxSupportAdvisoryPairs + 1} {
		t.Run(fmt.Sprintf("pairs_%d", pairCount), func(t *testing.T) {
			t.Parallel()
			program, contributors := advisoryOraclePairFixture(pairCount)
			rater := &ReferenceRater{program: program}
			cover := completeCoverVerdict{program: &rater.program, resolved: make([]bool, len(rater.program.keyOf))}
			report, stats := rater.assessSupportUncertaintyWithLimits(
				advisoryOracleContext(), contributors, map[string]completeCoverVerdict{advisoryOracleScope: cover},
				advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs),
			)
			if report == nil {
				t.Fatal("missing support advisory report")
			}
			if len(report.Pairs) != economics.MaxSupportAdvisoryPairs {
				t.Fatalf("report pairs=%d, want %d", len(report.Pairs), economics.MaxSupportAdvisoryPairs)
			}
			if pairCount == economics.MaxSupportAdvisoryPairs {
				if len(report.IncompleteContexts) != 0 {
					t.Fatalf("exact-limit report=%+v, want complete result", report)
				}
				return
			}
			if len(report.IncompleteContexts) != 1 || report.IncompleteContexts[0].Reason != economics.SupportAdvisoryPairLimit || stats.candidateExaminations != economics.MaxSupportAdvisoryPairs+1 {
				t.Fatalf("129th pair stats=%+v report=%+v, want 128 pairs plus pair_limit after observing pair 129", stats, report)
			}
		})
	}
}

func TestComponentRaterSupportAdvisoryDisconnectedPopulationDoesNotSweepPairs(t *testing.T) {
	t.Parallel()
	const population = 50
	edges := make([]metering.ComponentRelationship, 0, population)
	contributors := make(map[string]struct{}, population)
	for index := range population {
		root := fmt.Sprintf("tree-%03d-root", index)
		leaf := fmt.Sprintf("tree-%03d-leaf", index)
		edges = append(edges, advisoryOracleEdge(metering.RelationshipSubset, root, leaf))
		contributors[advisoryOracleCanonical(leaf)] = struct{}{}
	}
	program := advisoryOracleProgram(edges...)
	rater := &ReferenceRater{program: program}
	coverage := advisoryOracleCoverage(&rater.program)
	report, stats := rater.assessSupportUncertaintyWithLimits(
		advisoryOracleContext(), map[string]map[string]struct{}{advisoryOracleScope: contributors}, coverage,
		advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs),
	)
	if report != nil || stats.candidateExaminations != 0 || stats.graphVisits >= population*population {
		t.Fatalf("report=%+v stats=%+v, want no candidates and bounded index work for disconnected trees", report, stats)
	}
}

func TestComponentRaterSupportAdvisoryDenseCandidatesStopAtPairLimit(t *testing.T) {
	t.Parallel()
	const population = 20
	edges := make([]metering.ComponentRelationship, 0, population)
	contributors := make([]string, 0, population)
	for index := range population {
		leaf := fmt.Sprintf("dense-leaf-%02d", index)
		edges = append(edges, advisoryOracleEdge(metering.RelationshipSubset, "dense-root", leaf))
		contributors = append(contributors, leaf)
	}
	program := advisoryOracleProgram(edges...)
	rater := &ReferenceRater{program: program}
	coverage := advisoryOracleCoverage(&rater.program)
	report, stats := rater.assessSupportUncertaintyWithLimits(
		advisoryOracleContext(), advisoryOracleContributors(contributors...), coverage,
		advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs),
	)
	if report == nil || len(report.Pairs) != economics.MaxSupportAdvisoryPairs || len(report.IncompleteContexts) != 1 || report.IncompleteContexts[0].Reason != economics.SupportAdvisoryPairLimit {
		t.Fatalf("report=%+v, want bounded unknown prefix and pair_limit", report)
	}
	if stats.candidateExaminations != economics.MaxSupportAdvisoryPairs+1 || stats.graphVisits > economics.MaxSupportAdvisoryGraphVisits {
		t.Fatalf("stats=%+v, want 129 candidate examinations and graph work within the cap", stats)
	}
}

func TestComponentRaterSupportAdvisoryDoesNotMutateInputs(t *testing.T) {
	t.Parallel()
	program := advisoryOracleProgram(
		advisoryOracleEdge(metering.RelationshipSubset, "root", "left"),
		advisoryOracleEdge(metering.RelationshipSubset, "root", "right"),
	)
	rater := &ReferenceRater{program: program}
	valuation := advisoryOracleContext()
	valuation.SupportAdvisoryContexts = slices.Clone(valuation.SupportAdvisoryContexts)
	contributors := advisoryOracleContributors("left", "right")
	contributorsBefore := make(map[string]map[string]struct{}, len(contributors))
	for scope, population := range contributors {
		copyOfPopulation := make(map[string]struct{}, len(population))
		for key := range population {
			copyOfPopulation[key] = struct{}{}
		}
		contributorsBefore[scope] = copyOfPopulation
	}
	coverage := advisoryOracleCoverage(&rater.program, "root")
	coverageBefore := make(map[string]completeCoverVerdict, len(coverage))
	for scope, verdict := range coverage {
		verdict.resolved = slices.Clone(verdict.resolved)
		coverageBefore[scope] = verdict
	}
	valuationBefore := economics.Valuation{SupportAdvisoryContexts: slices.Clone(valuation.SupportAdvisoryContexts)}

	_, _ = rater.assessSupportUncertaintyWithLimits(
		valuation, contributors, coverage,
		advisoryOracleLimits(economics.MaxSupportAdvisoryCandidateExaminations, economics.MaxSupportAdvisoryGraphVisits, economics.MaxSupportAdvisoryPairs),
	)
	if !reflect.DeepEqual(valuation, valuationBefore) || !reflect.DeepEqual(contributors, contributorsBefore) || !reflect.DeepEqual(coverage, coverageBefore) {
		t.Fatalf("assessment mutated valuation or supplied contributor/coverage evidence")
	}
}
