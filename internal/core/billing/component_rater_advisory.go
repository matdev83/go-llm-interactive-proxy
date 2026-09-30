package billing

import (
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

type supportAssessmentLimits struct {
	candidateExaminations int
	graphVisits           int
	pairs                 int
}

type supportAssessmentStats struct {
	candidateExaminations    int
	graphVisits              int
	duplicateCandidateVisits int
}

type supportReachability struct {
	ids []int
}

type supportAssessmentQueries struct {
	program *schemaProgram
	cover   completeCoverVerdict
	limits  supportAssessmentLimits
	stats   *supportAssessmentStats
	cache   map[edgeClass]map[int]*supportReachability
}

func (r *ReferenceRater) assessSupportUncertainty(
	valuation economics.Valuation,
	contributors map[string]map[string]struct{},
	coverage map[string]completeCoverVerdict,
) *economics.SupportAdvisoryReport {
	report, _ := r.assessSupportUncertaintyWithLimits(valuation, contributors, coverage, supportAssessmentLimits{
		candidateExaminations: economics.MaxSupportAdvisoryCandidateExaminations,
		graphVisits:           economics.MaxSupportAdvisoryGraphVisits,
		pairs:                 economics.MaxSupportAdvisoryPairs,
	})
	return report
}

// assessSupportUncertaintyWithLimits is the per-call seam for bounded oracle
// tests. Limits and counters belong to one independent rating group; no process
// global state or conflict-path reachability cache is shared.
func (r *ReferenceRater) assessSupportUncertaintyWithLimits(
	valuation economics.Valuation,
	contributors map[string]map[string]struct{},
	coverage map[string]completeCoverVerdict,
	limits supportAssessmentLimits,
) (*economics.SupportAdvisoryReport, supportAssessmentStats) {
	var stats supportAssessmentStats
	if r == nil || len(r.program.keyOf) == 0 || len(contributors) == 0 {
		return nil, stats
	}
	if len(valuation.SupportAdvisoryContexts) != 1 || valuation.SupportAdvisoryContexts[0].Version != economics.SupportAdvisoryVersionV1 {
		return nil, stats
	}

	contextKey := valuation.SupportAdvisoryContexts[0].Key()
	report := &economics.SupportAdvisoryReport{}
	scopes := make([]string, 0, len(contributors))
	for scope, population := range contributors {
		if len(population) != 0 {
			scopes = append(scopes, scope)
		}
	}
	slices.Sort(scopes)

	for _, scope := range scopes {
		positive := supportContributorIDs(&r.program, contributors[scope])
		if len(positive) < 2 {
			continue
		}
		cover, ok := coverage[scope]
		if !ok || cover.program != &r.program || len(cover.resolved) != len(r.program.keyOf) {
			report.IncompleteContexts = append(report.IncompleteContexts, economics.SupportAdvisoryIncomplete{
				ContextKey: contextKey,
				Reason:     economics.SupportAdvisoryEvidenceUnavailable,
			})
			return supportNonEmptyReport(report), stats
		}

		queries := newSupportAssessmentQueries(&r.program, cover, limits, &stats)
		buckets, assessed := queries.candidateBuckets(positive)
		if !assessed {
			appendSupportAssessmentIncomplete(report, contextKey, economics.SupportAdvisoryGraphBudget)
			return supportNonEmptyReport(report), stats
		}

		for _, left := range positive {
			rights, candidatesReady := queries.rightCandidates(left, buckets)
			if !candidatesReady {
				appendSupportAssessmentIncomplete(report, contextKey, economics.SupportAdvisoryGraphBudget)
				return supportNonEmptyReport(report), stats
			}
			for _, right := range rights {
				if stats.candidateExaminations >= limits.candidateExaminations {
					appendSupportAssessmentIncomplete(report, contextKey, economics.SupportAdvisoryCandidateBudget)
					return supportNonEmptyReport(report), stats
				}
				stats.candidateExaminations++
				relation, pairAssessed := classifySupportRelation(left, right, queries)
				if !pairAssessed {
					appendSupportAssessmentIncomplete(report, contextKey, economics.SupportAdvisoryGraphBudget)
					return supportNonEmptyReport(report), stats
				}
				if relation != relationUnknownIntersection {
					continue
				}
				if len(report.Pairs) >= limits.pairs {
					appendSupportAssessmentIncomplete(report, contextKey, economics.SupportAdvisoryPairLimit)
					return supportNonEmptyReport(report), stats
				}
				report.Pairs = append(report.Pairs, economics.SupportAdvisoryPair{
					ContextKey: contextKey,
					ScopeKey:   scope,
					Left:       r.program.keyOf[left].Clone(),
					Right:      r.program.keyOf[right].Clone(),
				})
			}
		}
	}
	return supportNonEmptyReport(report), stats
}

func supportNonEmptyReport(report *economics.SupportAdvisoryReport) *economics.SupportAdvisoryReport {
	if report == nil || len(report.Pairs) == 0 && len(report.IncompleteContexts) == 0 {
		return nil
	}
	return report
}

func appendSupportAssessmentIncomplete(report *economics.SupportAdvisoryReport, contextKey string, reason economics.SupportAdvisoryReason) {
	report.IncompleteContexts = append(report.IncompleteContexts, economics.SupportAdvisoryIncomplete{ContextKey: contextKey, Reason: reason})
}

func supportContributorIDs(program *schemaProgram, contributors map[string]struct{}) []int {
	ids := make([]int, 0, len(contributors))
	for key := range contributors {
		if node, ok := program.byKey[key]; ok {
			ids = append(ids, node)
		}
	}
	slices.Sort(ids)
	return ids
}

func newSupportAssessmentQueries(
	program *schemaProgram,
	cover completeCoverVerdict,
	limits supportAssessmentLimits,
	stats *supportAssessmentStats,
) *supportAssessmentQueries {
	return &supportAssessmentQueries{
		program: program,
		cover:   cover,
		limits:  limits,
		stats:   stats,
		cache: map[edgeClass]map[int]*supportReachability{
			allInclusionEdges:     make(map[int]*supportReachability),
			reverseInclusionEdges: make(map[int]*supportReachability),
		},
	}
}

func (q *supportAssessmentQueries) graphVisit() bool {
	if q.stats.graphVisits >= q.limits.graphVisits {
		return false
	}
	q.stats.graphVisits++
	return true
}

// reachable builds and caches a complete strict closure. A partial walk is
// discarded so callers can never mistake an unfinished ancestor/branch set for
// proof of separation.
func (q *supportAssessmentQueries) reachable(class edgeClass, start int) (*supportReachability, bool) {
	cache := q.cache[class]
	if cached := cache[start]; cached != nil {
		return cached, true
	}
	adjacency := q.program.adjacency(class)
	if start < 0 || start >= len(adjacency) {
		return nil, false
	}
	if !q.graphVisit() {
		return nil, false
	}
	queue := []int{start}
	seen := map[int]struct{}{start: {}}
	for head := 0; head < len(queue); head++ {
		current := queue[head]
		for _, next := range adjacency[current] {
			if !q.graphVisit() {
				return nil, false
			}
			if _, exists := seen[next]; exists {
				continue
			}
			if !q.graphVisit() {
				return nil, false
			}
			seen[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	delete(seen, start)
	var ids []int
	for node := range seen {
		if !q.graphVisit() {
			return nil, false
		}
		ids = append(ids, node)
	}
	slices.Sort(ids)
	closure := &supportReachability{ids: ids}
	if !q.graphVisit() {
		return nil, false
	}
	cache[start] = closure
	return closure, true
}

func (q *supportAssessmentQueries) containsRegion(container, contained int) (bool, bool) {
	descendants, assessed := q.reachable(allInclusionEdges, container)
	if !assessed {
		return false, false
	}
	low, high := 0, len(descendants.ids)
	for low < high {
		if !q.graphVisit() {
			return false, false
		}
		middle := low + (high-low)/2
		if descendants.ids[middle] < contained {
			low = middle + 1
		} else {
			high = middle
		}
	}
	if low == len(descendants.ids) {
		return false, true
	}
	if !q.graphVisit() {
		return false, false
	}
	return descendants.ids[low] == contained, true
}

func (q *supportAssessmentQueries) sharesAncestor(a, b int) (bool, bool) {
	ancestorsA, assessed := q.reachable(reverseInclusionEdges, a)
	if !assessed {
		return false, false
	}
	ancestorsB, assessed := q.reachable(reverseInclusionEdges, b)
	if !assessed {
		return false, false
	}
	for i, j := 0, 0; i < len(ancestorsA.ids) && j < len(ancestorsB.ids); {
		if !q.graphVisit() {
			return false, false
		}
		left := ancestorsA.ids[i]
		if !q.graphVisit() {
			return false, false
		}
		right := ancestorsB.ids[j]
		switch {
		case left == right:
			return true, true
		case left < right:
			i++
		default:
			j++
		}
	}
	return false, true
}

func (q *supportAssessmentQueries) partitionSeparates(a, b int) (bool, bool) {
	for _, parent := range q.program.coverParents {
		if !q.graphVisit() {
			return false, false
		}
		if !q.cover.resolved[parent] {
			continue
		}
		children := q.program.completeChildren[parent]
		if len(children) < 2 {
			continue
		}
		branchA, branchB := -1, -1
		for _, child := range children {
			if !q.graphVisit() {
				return false, false
			}
			if branchA == -1 {
				inBranch, assessed := q.inBranch(child, a)
				if !assessed {
					return false, false
				}
				if inBranch {
					branchA = child
				}
			}
			if branchB == -1 {
				inBranch, assessed := q.inBranch(child, b)
				if !assessed {
					return false, false
				}
				if inBranch {
					branchB = child
				}
			}
		}
		if branchA != -1 && branchB != -1 && branchA != branchB {
			return true, true
		}
	}
	return false, true
}

func (q *supportAssessmentQueries) inBranch(child, region int) (bool, bool) {
	if child == region {
		return true, true
	}
	return q.containsRegion(child, region)
}

// candidateBuckets indexes each contributor under every strict ancestor and
// under itself. The self entry lets a branch root and its descendant reach the
// relation predicate, which then excludes containment through the shared rule.
func (q *supportAssessmentQueries) candidateBuckets(contributors []int) (map[int][]int, bool) {
	buckets := make(map[int][]int)
	for _, contributor := range contributors {
		ancestors, assessed := q.reachable(reverseInclusionEdges, contributor)
		if !assessed {
			return nil, false
		}
		for _, ancestor := range ancestors.ids {
			if !q.graphVisit() {
				return nil, false
			}
			if !q.graphVisit() {
				return nil, false
			}
			buckets[ancestor] = append(buckets[ancestor], contributor)
		}
		if !q.graphVisit() {
			return nil, false
		}
		buckets[contributor] = append(buckets[contributor], contributor)
	}
	return buckets, true
}

// rightCandidates expands only this contributor's ancestor buckets. Its map is
// bounded by graph visits, and sorting it produces a canonical pair prefix even
// when two contributors meet in several buckets of a diamond.
func (q *supportAssessmentQueries) rightCandidates(left int, buckets map[int][]int) ([]int, bool) {
	ancestors, assessed := q.reachable(reverseInclusionEdges, left)
	if !assessed {
		return nil, false
	}
	rights := make(map[int]struct{})
	expand := func(ancestor int) bool {
		bucket := buckets[ancestor]
		for index := range bucket {
			if !q.graphVisit() {
				return false
			}
			right := bucket[index]
			if right <= left {
				continue
			}
			if _, exists := rights[right]; exists {
				q.stats.duplicateCandidateVisits++
				continue
			}
			if !q.graphVisit() {
				return false
			}
			rights[right] = struct{}{}
		}
		return true
	}

	selfExpanded := false
	for _, ancestor := range ancestors.ids {
		if !q.graphVisit() {
			return nil, false
		}
		if !selfExpanded && left < ancestor {
			if !expand(left) {
				return nil, false
			}
			selfExpanded = true
		}
		if ancestor == left {
			selfExpanded = true
		}
		if !expand(ancestor) {
			return nil, false
		}
	}
	if !selfExpanded && !expand(left) {
		return nil, false
	}

	var ordered []int
	for right := range rights {
		if !q.graphVisit() {
			return nil, false
		}
		ordered = append(ordered, right)
	}
	slices.Sort(ordered)
	return ordered, true
}
