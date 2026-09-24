package billing

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// MatchStatements matches normalized imported statement revisions against an
// explicitly eligible retained charge/reconciliation evidence set. Matching is
// exact explicit charge-ID identity or complete compatible account/period/SKU
// aggregate coverage. Missing join keys stay unmatched; no timestamp-nearest,
// proportional, heuristic or guessed per-request/B-leg allocation is produced.
// The function is pure and deterministic across input order.
func MatchStatements(statements []NormalizedStatement, evidence []StatementChargeEvidence) (StatementMatchSet, error) {
	if len(statements) > MaxStatementMatchStatements {
		return StatementMatchSet{}, fmt.Errorf("%w: statement batch exceeds %d entries", ErrStatementMatchInvalid, MaxStatementMatchStatements)
	}
	if len(evidence) > MaxStatementMatchEvidence {
		return StatementMatchSet{}, fmt.Errorf("%w: evidence set exceeds %d entries", ErrStatementMatchInvalid, MaxStatementMatchEvidence)
	}
	index, err := newStatementMatchIndex(evidence)
	if err != nil {
		return StatementMatchSet{}, err
	}
	ordered, err := canonicalStatementMatchInput(statements)
	if err != nil {
		return StatementMatchSet{}, err
	}
	resolutions := make([]statementMatchResolution, len(ordered))
	for i := range ordered {
		resolution, err := index.matchStatement(ordered[i])
		if err != nil {
			return StatementMatchSet{}, err
		}
		resolutions[i] = resolution
	}
	resolveStatementMatchConflicts(resolutions, index)
	return buildStatementMatchSet(resolutions), nil
}

type statementMatchIndex struct {
	evidence      []StatementChargeEvidence
	byRef         map[string]int
	byChargeItem  map[string][]int
	byComponent   map[string][]int
	inclusiveEdge map[string][]string
}

func newStatementMatchIndex(evidence []StatementChargeEvidence) (*statementMatchIndex, error) {
	ordered := append([]StatementChargeEvidence(nil), evidence...)
	for i := range ordered {
		if err := ordered[i].Validate(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return statementMatchChargeRefKey(ordered[i].Ref) < statementMatchChargeRefKey(ordered[j].Ref)
	})
	index := &statementMatchIndex{
		evidence:      ordered,
		byRef:         make(map[string]int, len(ordered)),
		byChargeItem:  make(map[string][]int, len(ordered)),
		byComponent:   make(map[string][]int, len(ordered)),
		inclusiveEdge: make(map[string][]string),
	}
	for i := range ordered {
		key := statementMatchChargeRefKey(ordered[i].Ref)
		if i > 0 && key == statementMatchChargeRefKey(ordered[i-1].Ref) {
			return nil, fmt.Errorf("%w: duplicate retained charge evidence %q", ErrStatementMatchInvalid, key)
		}
		index.byRef[key] = i
		index.byChargeItem[ordered[i].Ref.ChargeItemID] = append(index.byChargeItem[ordered[i].Ref.ChargeItemID], i)
		if ordered[i].Component != nil {
			componentKey := ordered[i].Component.CanonicalKey()
			index.byComponent[componentKey] = append(index.byComponent[componentKey], i)
		}
	}
	for i := range ordered {
		parent := statementMatchChargeRefKey(ordered[i].Ref)
		for _, edge := range ordered[i].Covers {
			if edge.Relation != metering.CoverageInclusive {
				continue
			}
			index.inclusiveEdge[parent] = append(index.inclusiveEdge[parent], statementMatchChargeRefKey(edge.Ref))
		}
	}
	return index, nil
}

type statementMatchIdentityRevisions struct {
	statementID string
	count       int
	revisions   map[uint64]struct{}
}

// canonicalStatementMatchInput validates every statement, fails closed on
// duplicate revisions and on one statement identity appearing with more than
// one revision, then orders statements by immutable identity.
func canonicalStatementMatchInput(statements []NormalizedStatement) ([]NormalizedStatement, error) {
	ordered := append([]NormalizedStatement(nil), statements...)
	grouped := make(map[string]*statementMatchIdentityRevisions, len(ordered))
	for i := range ordered {
		if err := ordered[i].Validate(); err != nil {
			return nil, fmt.Errorf("%w: statement %d: %v", ErrStatementMatchInvalid, i, err)
		}
		identity := ordered[i].Identity
		unversioned := identity.StoreID + "\x00" + identity.ProviderAccountKey + "\x00" + identity.StatementID + "\x00" + identity.PeriodID
		group, exists := grouped[unversioned]
		if !exists {
			group = &statementMatchIdentityRevisions{statementID: identity.StatementID, revisions: make(map[uint64]struct{})}
			grouped[unversioned] = group
		}
		group.count++
		group.revisions[identity.Revision] = struct{}{}
	}
	var ambiguous []string
	for _, group := range grouped {
		if len(group.revisions) > 1 {
			ambiguous = append(ambiguous, group.statementID)
			continue
		}
		if group.count > 1 {
			return nil, fmt.Errorf("%w: duplicate retained statement revision %q", ErrStatementMatchInvalid, group.statementID)
		}
	}
	if len(ambiguous) > 0 {
		slices.Sort(ambiguous)
		return nil, &StatementMatchAmbiguityError{StatementIDs: slices.Compact(ambiguous)}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Identity.Key() < ordered[j].Identity.Key() })
	return ordered, nil
}

type statementMatchResolution struct {
	statement NormalizedStatement
	lines     []statementMatchLineResolution
}

type statementMatchLineResolution struct {
	line            NormalizedStatementLine
	status          StatementMatchStatus
	reason          StatementMatchReason
	unmatchedReason string
	link            *StatementCoverageLink
}

func (index *statementMatchIndex) matchStatement(statement NormalizedStatement) (statementMatchResolution, error) {
	resolution := statementMatchResolution{
		statement: statement,
		lines:     make([]statementMatchLineResolution, len(statement.Lines)),
	}
	tenant := statementMatchStatementTenant(statement)
	observations := statementMatchObservationIndex(statement)
	for i, line := range statement.Lines {
		if line.Line.Outcome == economics.StatementLineUnmatched {
			resolution.lines[i] = statementMatchLineResolution{
				line: line, status: StatementMatchStatusUnmatched,
				reason: StatementMatchReasonNoChargeLink, unmatchedReason: line.Line.UnmatchedReason,
			}
			continue
		}
		claim, err := statementMatchLineClaim(line, observations)
		if err != nil {
			return resolution, err
		}
		matched, err := index.matchClaim(statement, tenant, line, claim)
		if err != nil {
			return resolution, err
		}
		resolution.lines[i] = matched
	}
	return resolution, nil
}

func (index *statementMatchIndex) matchClaim(statement NormalizedStatement, tenant string, line NormalizedStatementLine, claim metering.ReportedCharge) (statementMatchLineResolution, error) {
	entry := statementMatchLineResolution{line: line}
	candidates := index.byChargeItem[claim.ChargeItemID]
	switch {
	case len(candidates) > 1:
		// A statement line carries no evidence revision, so more than one
		// retained revision of the same charge item cannot be disambiguated.
		entry.status = StatementMatchStatusConflict
		entry.reason = StatementMatchReasonCrossRevisionAmbiguity
		return entry, nil
	case len(candidates) == 1:
		return index.matchExplicitClaim(statement, tenant, entry, claim, index.evidence[candidates[0]]), nil
	}
	if claim.Kind != metering.ChargeKindAggregate {
		entry.status = StatementMatchStatusUnmatched
		entry.reason = StatementMatchReasonChargeNotFound
		return entry, nil
	}
	if claim.Component == nil {
		// A genuine account total remains account-scoped until an explicit
		// match or a separately labelled allocation exists.
		entry.status = StatementMatchStatusUnmatched
		entry.reason = StatementMatchReasonAccountScopedTotal
		return entry, nil
	}
	return index.matchAggregateClaim(statement, tenant, entry, claim)
}

func (index *statementMatchIndex) matchExplicitClaim(statement NormalizedStatement, tenant string, entry statementMatchLineResolution, claim metering.ReportedCharge, candidate StatementChargeEvidence) statementMatchLineResolution {
	if candidate.State == StatementEvidenceIneligible {
		entry.status = StatementMatchStatusUnmatched
		entry.reason = StatementMatchReasonEvidenceIneligible
		return entry
	}
	if reason := statementMatchScopeReason(statement, tenant, claim, candidate, true); reason != StatementMatchReasonNone {
		entry.status = StatementMatchStatusIncomparable
		entry.reason = reason
		return entry
	}
	link := index.newStatementMatchLink(statement, StatementMatchKindExplicitCharge,
		[]metering.ChargeRef{candidate.Ref}, []StatementChargeEvidence{candidate}, entry.line)
	entry.status = StatementMatchStatusMatched
	entry.reason = StatementMatchReasonExplicitCharge
	entry.link = &link
	return entry
}

func (index *statementMatchIndex) matchAggregateClaim(statement NormalizedStatement, tenant string, entry statementMatchLineResolution, claim metering.ReportedCharge) (statementMatchLineResolution, error) {
	if len(claim.Covers) > MaxStatementMatchCoverageRefs {
		return entry, fmt.Errorf("%w: statement coverage reference bound exceeded", ErrStatementMatchInvalid)
	}
	group := index.aggregateGroup(statement, claim)
	if len(group) == 0 {
		entry.status = StatementMatchStatusUnmatched
		entry.reason = StatementMatchReasonChargeNotFound
		return entry, nil
	}
	for _, candidateIndex := range group {
		candidate := index.evidence[candidateIndex]
		if reason := statementMatchScopeReason(statement, tenant, claim, candidate, false); reason != StatementMatchReasonNone {
			entry.status = StatementMatchStatusIncomparable
			entry.reason = reason
			return entry, nil
		}
	}

	if len(claim.Covers) > 0 {
		entry = index.matchAggregateCoverageReferences(statement, tenant, entry, claim, group)
		return entry, nil
	}

	if statementMatchGroupHasDuplicateChargeItem(index, group) {
		entry.status = StatementMatchStatusConflict
		entry.reason = StatementMatchReasonCrossRevisionAmbiguity
		return entry, nil
	}
	for _, candidateIndex := range group {
		if index.evidence[candidateIndex].State == StatementEvidenceIneligible {
			entry.status = StatementMatchStatusPartial
			entry.reason = StatementMatchReasonEvidenceIneligible
			return entry, nil
		}
	}
	charges := make([]metering.ChargeRef, 0, len(group))
	covered := make([]StatementChargeEvidence, 0, len(group))
	for _, candidateIndex := range group {
		charges = append(charges, index.evidence[candidateIndex].Ref)
		covered = append(covered, index.evidence[candidateIndex])
	}
	link := index.newStatementMatchLink(statement, StatementMatchKindAggregateSKU, charges, covered, entry.line)
	entry.status = StatementMatchStatusMatched
	entry.reason = StatementMatchReasonAggregateSKUCoverage
	entry.link = &link
	return entry, nil
}

func (index *statementMatchIndex) matchAggregateCoverageReferences(statement NormalizedStatement, tenant string, entry statementMatchLineResolution, claim metering.ReportedCharge, group []int) statementMatchLineResolution {
	groupSet := make(map[int]struct{}, len(group))
	for _, candidateIndex := range group {
		groupSet[candidateIndex] = struct{}{}
	}
	covered := make(map[int]struct{}, len(claim.Covers))
	dangling := false
	ineligible := false
	for _, edge := range claim.Covers {
		candidateIndex, found := index.byRef[statementMatchChargeRefKey(edge.Ref)]
		if !found {
			dangling = true
			continue
		}
		candidate := index.evidence[candidateIndex]
		if candidate.State == StatementEvidenceIneligible {
			ineligible = true
			continue
		}
		if reason := statementMatchScopeReason(statement, tenant, claim, candidate, false); reason != StatementMatchReasonNone {
			entry.status = StatementMatchStatusIncomparable
			entry.reason = reason
			return entry
		}
		if _, inGroup := groupSet[candidateIndex]; !inGroup {
			entry.status = StatementMatchStatusIncomparable
			entry.reason = StatementMatchReasonSKUMismatch
			return entry
		}
		covered[candidateIndex] = struct{}{}
	}
	switch {
	case dangling:
		entry.status = StatementMatchStatusPartial
		entry.reason = StatementMatchReasonDanglingCoverageRef
	case ineligible:
		entry.status = StatementMatchStatusPartial
		entry.reason = StatementMatchReasonEvidenceIneligible
	case len(covered) != len(group):
		entry.status = StatementMatchStatusPartial
		entry.reason = StatementMatchReasonPartialSKUCoverage
	default:
		if statementMatchGroupHasDuplicateChargeItem(index, group) {
			entry.status = StatementMatchStatusConflict
			entry.reason = StatementMatchReasonCrossRevisionAmbiguity
			return entry
		}
		charges := make([]metering.ChargeRef, 0, len(covered))
		evidence := make([]StatementChargeEvidence, 0, len(covered))
		for candidateIndex := range covered {
			charges = append(charges, index.evidence[candidateIndex].Ref)
			evidence = append(evidence, index.evidence[candidateIndex])
		}
		link := index.newStatementMatchLink(statement, StatementMatchKindAggregateSKU, charges, evidence, entry.line)
		entry.status = StatementMatchStatusMatched
		entry.reason = StatementMatchReasonAggregateSKUCoverage
		entry.link = &link
	}
	return entry
}

// aggregateGroup selects every retained charge that shares the statement
// store, provider account, period and exact SKU component, regardless of
// eligibility. Tenant/currency incompatibilities therefore surface as
// incomparable instead of being silently excluded from complete coverage.
func (index *statementMatchIndex) aggregateGroup(statement NormalizedStatement, claim metering.ReportedCharge) []int {
	candidates := index.byComponent[claim.Component.CanonicalKey()]
	group := make([]int, 0, len(candidates))
	for _, candidateIndex := range candidates {
		candidate := index.evidence[candidateIndex]
		if candidate.Ref.StoreID != statement.Identity.StoreID {
			continue
		}
		if candidate.ProviderAccountKey != statement.Identity.ProviderAccountKey {
			continue
		}
		if candidate.PeriodID != statement.Identity.PeriodID {
			continue
		}
		group = append(group, candidateIndex)
	}
	return group
}

func statementMatchGroupHasDuplicateChargeItem(index *statementMatchIndex, group []int) bool {
	seen := make(map[string]struct{}, len(group))
	for _, candidateIndex := range group {
		item := index.evidence[candidateIndex].Ref.ChargeItemID
		if _, exists := seen[item]; exists {
			return true
		}
		seen[item] = struct{}{}
	}
	return false
}

func statementMatchScopeReason(statement NormalizedStatement, tenant string, claim metering.ReportedCharge, candidate StatementChargeEvidence, requireKind bool) StatementMatchReason {
	if candidate.Ref.StoreID != statement.Identity.StoreID {
		return StatementMatchReasonStoreMismatch
	}
	if tenant != "" && candidate.TenantID != tenant {
		return StatementMatchReasonTenantMismatch
	}
	if candidate.ProviderAccountKey != statement.Identity.ProviderAccountKey {
		return StatementMatchReasonAccountMismatch
	}
	if candidate.PeriodID != statement.Identity.PeriodID {
		return StatementMatchReasonPeriodMismatch
	}
	if requireKind && claim.Kind != candidate.Kind {
		return StatementMatchReasonGranularityMismatch
	}
	equal, unitOnlyDifference := statementMatchComponentEqual(claim.Component, candidate.Component)
	if !equal {
		if unitOnlyDifference {
			return StatementMatchReasonUnitMismatch
		}
		return StatementMatchReasonSKUMismatch
	}
	if claim.Currency != "" && candidate.Currency != claim.Currency {
		return StatementMatchReasonCurrencyMismatch
	}
	return StatementMatchReasonNone
}

func (index *statementMatchIndex) newStatementMatchLink(statement NormalizedStatement, kind StatementMatchKind, charges []metering.ChargeRef, evidence []StatementChargeEvidence, line NormalizedStatementLine) StatementCoverageLink {
	link := StatementCoverageLink{
		StatementKey:      statement.Identity.Key(),
		StatementID:       statement.Identity.StatementID,
		StatementRevision: statement.Identity.Revision,
		Kind:              kind,
		LineKeys:          []string{line.Identity.Key()},
		LineIDs:           []string{line.Line.ID},
		Charges:           slices.Clone(charges),
		EvidenceIDs:       make([]string, 0, len(evidence)),
	}
	for _, entry := range evidence {
		if entry.ReconciliationID != "" {
			link.EvidenceIDs = append(link.EvidenceIDs, entry.ReconciliationID)
		}
	}
	slices.SortFunc(link.Charges, func(a, b metering.ChargeRef) int {
		return strings.Compare(statementMatchChargeRefKey(a), statementMatchChargeRefKey(b))
	})
	slices.Sort(link.EvidenceIDs)
	link.EvidenceIDs = slices.Compact(link.EvidenceIDs)
	return link
}

// statementMatchLineClaim resolves the exact normalized charge claim of one
// matched statement line from its included statement-line observation.
func statementMatchLineClaim(line NormalizedStatementLine, observations map[string]metering.Observation) (metering.ReportedCharge, error) {
	ref := line.Line.Observation
	observation, found := observations[statementMatchObservationRefKey(ref.StoreID, ref.ObservationID, ref.Revision)]
	if !found {
		return metering.ReportedCharge{}, fmt.Errorf("%w: statement line %q charge claim is not retained exactly", ErrStatementMatchInvalid, line.Line.ID)
	}
	if ref.PayloadHash != "" && observation.Fingerprint() != ref.PayloadHash {
		return metering.ReportedCharge{}, fmt.Errorf("%w: statement line %q charge claim is not retained exactly", ErrStatementMatchInvalid, line.Line.ID)
	}
	for _, charge := range observation.Charges {
		if charge.ChargeItemID == line.Line.ChargeItemID {
			return charge, nil
		}
	}
	return metering.ReportedCharge{}, fmt.Errorf("%w: statement line %q charge claim is not retained exactly", ErrStatementMatchInvalid, line.Line.ID)
}

func statementMatchObservationIndex(statement NormalizedStatement) map[string]metering.Observation {
	observations := make(map[string]metering.Observation, len(statement.Batch.Observations))
	for _, observation := range statement.Batch.Observations {
		key := statementMatchObservationRefKey(observation.Subject.StoreID, observation.ID, observation.Revision)
		observations[key] = observation
	}
	return observations
}

func statementMatchObservationRefKey(storeID, observationID string, revision uint64) string {
	return storeID + "\x00" + observationID + "\x00" + strconv.FormatUint(revision, 10)
}

func statementMatchStatementTenant(statement NormalizedStatement) string {
	if statement.Batch.Subject.TenantID != "" {
		return statement.Batch.Subject.TenantID
	}
	for _, observation := range statement.Batch.Observations {
		if observation.Subject.TenantID != "" {
			return observation.Subject.TenantID
		}
		if observation.Correlation.TenantID != "" {
			return observation.Correlation.TenantID
		}
	}
	return ""
}

type statementMatchLinkLocation struct {
	result int
	line   int
}

// resolveStatementMatchConflicts detects duplicate/overlapping coverage and
// duplicate parent+child inclusion across every tentative link. All links that
// participate in a duplicate become conflict outcomes; no arbitrary winner is
// chosen and no link is silently dropped.
func resolveStatementMatchConflicts(resolutions []statementMatchResolution, index *statementMatchIndex) {
	type linkState struct {
		counts map[string]int
		direct map[string]struct{}
	}
	states := make(map[statementMatchLinkLocation]linkState)
	linkOccurrences := make(map[string]int)
	directOccurrences := make(map[string]int)
	for resultIndex := range resolutions {
		for lineIndex := range resolutions[resultIndex].lines {
			link := resolutions[resultIndex].lines[lineIndex].link
			if link == nil {
				continue
			}
			counts := index.linkCoverageCounts(*link)
			direct := make(map[string]struct{}, len(link.Charges))
			for _, charge := range link.Charges {
				direct[statementMatchChargeRefKey(charge)] = struct{}{}
			}
			location := statementMatchLinkLocation{result: resultIndex, line: lineIndex}
			states[location] = linkState{counts: counts, direct: direct}
			for ref := range counts {
				linkOccurrences[ref]++
			}
			for ref := range direct {
				directOccurrences[ref]++
			}
		}
	}

	for location, state := range states {
		// Reason selection is order independent: a direct charge collision
		// (overlapping coverage) outranks an internal/implicit parent-child
		// duplicate, and neither depends on map iteration order.
		overlapping := false
		duplicate := false
		for ref, count := range state.counts {
			if count > 1 {
				duplicate = true
				continue
			}
			if linkOccurrences[ref] <= 1 {
				continue
			}
			if directOccurrences[ref] > 1 {
				overlapping = true
				continue
			}
			duplicate = true
		}
		reason := StatementMatchReasonNone
		switch {
		case overlapping:
			reason = StatementMatchReasonOverlappingCoverage
		case duplicate:
			reason = StatementMatchReasonDuplicateParentChild
		}
		if reason == StatementMatchReasonNone {
			continue
		}
		line := &resolutions[location.result].lines[location.line]
		line.link = nil
		line.status = StatementMatchStatusConflict
		line.reason = reason
	}
}

// linkCoverageCounts counts how many times each charge is economically
// included by one link: every directly covered charge counts once, and every
// charge reachable through inclusive retained coverage edges counts once per
// distinct directly covered ancestor that transitively includes it.
func (index *statementMatchIndex) linkCoverageCounts(link StatementCoverageLink) map[string]int {
	counts := make(map[string]int, len(link.Charges))
	for _, charge := range link.Charges {
		counts[statementMatchChargeRefKey(charge)]++
	}
	for _, charge := range link.Charges {
		seed := statementMatchChargeRefKey(charge)
		visited := map[string]struct{}{seed: {}}
		stack := []string{seed}
		for len(stack) > 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, child := range index.inclusiveEdge[current] {
				if _, seen := visited[child]; seen {
					continue
				}
				visited[child] = struct{}{}
				counts[child]++
				stack = append(stack, child)
			}
		}
	}
	return counts
}

func buildStatementMatchSet(resolutions []statementMatchResolution) StatementMatchSet {
	set := StatementMatchSet{Results: make([]StatementMatchResult, 0, len(resolutions))}
	for _, resolution := range resolutions {
		result := StatementMatchResult{
			StatementKey:      resolution.statement.Identity.Key(),
			StatementID:       resolution.statement.Identity.StatementID,
			StatementRevision: resolution.statement.Identity.Revision,
			Lines:             make([]StatementMatchLine, 0, len(resolution.lines)),
			Links:             make([]StatementCoverageLink, 0),
		}
		for _, line := range resolution.lines {
			entry := StatementMatchLine{
				LineID: line.line.Line.ID, LineKey: line.line.Identity.Key(), LineRevision: line.line.Line.Revision,
				Status: line.status, Reason: line.reason, UnmatchedReason: line.unmatchedReason,
			}
			if line.link != nil {
				entry.LinkKey = line.link.Key()
				result.Links = append(result.Links, line.link.Clone())
			}
			result.Lines = append(result.Lines, entry)
		}
		sort.Slice(result.Links, func(i, j int) bool { return result.Links[i].Key() < result.Links[j].Key() })
		set.Results = append(set.Results, result)
	}
	return set
}
