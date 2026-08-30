package qa

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
)

var backtickIdentRegex = regexp.MustCompile("`([a-z0-9-]+)`")

func extractMarkdownSection(content, heading string) (string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	startIdx := -1
	headingLevel := 0

	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == heading {
			startIdx = i
			for headingLevel < len(heading) && heading[headingLevel] == '#' {
				headingLevel++
			}
			break
		}
	}
	if startIdx == -1 {
		return "", fmt.Errorf("missing exact heading %q", heading)
	}

	var sectionLines []string
	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasPrefix(trimmed, "#") {
			lvl := 0
			for lvl < len(trimmed) && trimmed[lvl] == '#' {
				lvl++
			}
			if lvl > 0 && lvl <= headingLevel && len(trimmed) > lvl && trimmed[lvl] == ' ' {
				break
			}
		}
		sectionLines = append(sectionLines, line)
	}

	return strings.Join(sectionLines, "\n"), nil
}

func extractCatalogComponentIDs(sectionText, marker string) ([]string, int, error) {
	markerIdx := strings.Index(sectionText, marker)
	if markerIdx < 0 {
		return nil, 0, fmt.Errorf("missing marker %q in section", marker)
	}

	var count int
	beforeMarker := sectionText[:markerIdx]
	words := strings.Fields(beforeMarker)
	for i := len(words) - 1; i >= 0 && i >= len(words)-5; i-- {
		var n int
		if _, err := fmt.Sscanf(words[i], "%d", &n); err == nil && n > 0 {
			count = n
			break
		}
	}

	sentenceRest := sectionText[markerIdx:]
	endIdx := len(sentenceRest)
	if periodIdx := strings.Index(sentenceRest, ".\n"); periodIdx >= 0 {
		endIdx = periodIdx + 1
	} else if closeParenIdx := strings.Index(sentenceRest, ")\n"); closeParenIdx >= 0 {
		endIdx = closeParenIdx + 1
	} else if periodIdx := strings.Index(sentenceRest, "."); periodIdx >= 0 {
		endIdx = periodIdx + 1
	}
	declSentence := sentenceRest[:endIdx]

	matches := backtickIdentRegex.FindAllStringSubmatch(declSentence, -1)
	if len(matches) == 0 {
		return nil, count, fmt.Errorf("no backtick-enclosed component IDs found after marker %q", marker)
	}

	var ids []string
	for _, m := range matches {
		ids = append(ids, m[1])
	}
	return ids, count, nil
}

func validateComponentInventoryBijection(docName string, extractedIDs []string, declaredCount int, catalog dbparity.Catalog) []string {
	var violations []string
	expectedIDs := catalog.ComponentIDs()
	expectedCount := len(catalog.Components)

	if declaredCount != expectedCount {
		violations = append(violations, fmt.Sprintf("%s: documented component family count is %d, expected %d", docName, declaredCount, expectedCount))
	}

	for _, id := range expectedIDs {
		if !slices.Contains(extractedIDs, id) {
			violations = append(violations, fmt.Sprintf("%s: missing catalog component ID %q in documented component list", docName, id))
		}
	}

	for _, id := range extractedIDs {
		if !slices.Contains(expectedIDs, id) {
			violations = append(violations, fmt.Sprintf("%s: found unexpected or stale component ID %q in documented component list", docName, id))
		}
	}

	if len(extractedIDs) != expectedCount {
		violations = append(violations, fmt.Sprintf("%s: extracted %d component IDs, expected exactly %d", docName, len(extractedIDs), expectedCount))
	}

	return violations
}

func validateDatabasePersistenceDoc(content string, catalog dbparity.Catalog) []string {
	var violations []string

	section, err := extractMarkdownSection(content, "## Maintainer guide: dual-dialect parity & enforcement")
	if err != nil {
		return []string{"docs/database-persistence.md: " + err.Error()}
	}

	markers := []string{
		"internal/testkit/dbparity",
		"dbparity.DefaultCatalog()",
		"make test-db-parity-sqlite",
		"make test-db-parity-postgres-direct",
		"make test-db-parity",
		"repo-hygiene",
		"always() && needs.db-parity.result != 'success'",
		"scripts/ci-scope.sh",
		"internal/archtest/database_parity_test.go",
		"TestDBParity_SQLite",
		"TestDBParity_PostgresDirect",
	}
	for _, m := range markers {
		if !strings.Contains(section, m) {
			violations = append(violations, "docs/database-persistence.md: maintainer section missing marker '"+m+"'")
		}
	}

	ids, count, err := extractCatalogComponentIDs(section, "component families:")
	if err != nil {
		violations = append(violations, "docs/database-persistence.md: "+err.Error())
	} else {
		violations = append(violations, validateComponentInventoryBijection("docs/database-persistence.md", ids, count, catalog)...)
	}

	return violations
}

func validateReleaseGatesDoc(content string, catalog dbparity.Catalog) []string {
	var violations []string

	section, err := extractMarkdownSection(content, "## Database dialect parity (persistence gates)")
	if err != nil {
		return []string{"docs/release-gates.md: " + err.Error()}
	}

	markers := []string{
		"internal/testkit/dbparity",
		"dbparity.DefaultCatalog()",
		"make test-db-parity-sqlite",
		"make test-db-parity-postgres-direct",
		"make test-db-parity",
		"repo-hygiene",
		"scripts/ci-scope.sh",
		"go test ./internal/archtest -run DatabaseParity",
		"go test ./internal/qa -run DatabaseParity",
	}
	for _, m := range markers {
		if !strings.Contains(section, m) {
			violations = append(violations, "docs/release-gates.md: persistence gates section missing marker '"+m+"'")
		}
	}

	ids, count, err := extractCatalogComponentIDs(section, "component families (")
	if err != nil {
		violations = append(violations, "docs/release-gates.md: "+err.Error())
	} else {
		violations = append(violations, validateComponentInventoryBijection("docs/release-gates.md", ids, count, catalog)...)
	}

	return violations
}

func validateSteeringAndAgentsDocs(testingContent, techContent, agentsContent string, catalog dbparity.Catalog) []string {
	var violations []string
	expectedCount := len(catalog.Components)

	// 1. .kiro/steering/testing.md
	testingSection, err := extractMarkdownSection(testingContent, "## Build Tag & Environment Gating Rules")
	if err != nil {
		violations = append(violations, ".kiro/steering/testing.md: "+err.Error())
	} else {
		countNeedle := fmt.Sprintf("%d component families", expectedCount)
		testingMarkers := []string{
			"internal/testkit/dbparity",
			"dbparity.DefaultCatalog()",
			countNeedle,
			"make test-db-parity",
			"make test-db-parity-sqlite",
			"make test-db-parity-postgres-direct",
			"repo-hygiene",
			"db-parity",
			"always() && needs.db-parity.result != 'success'",
			"scripts/ci-scope.sh",
		}
		for _, m := range testingMarkers {
			if !strings.Contains(testingSection, m) {
				violations = append(violations, ".kiro/steering/testing.md: section missing marker '"+m+"'")
			}
		}
	}

	// 2. .kiro/steering/tech.md
	techStandardsSection, err := extractMarkdownSection(techContent, "## Database & PgBouncer Standards")
	if err != nil {
		violations = append(violations, ".kiro/steering/tech.md: "+err.Error())
	} else {
		countNeedle := fmt.Sprintf("All %d production persistence families", expectedCount)
		techStandardsMarkers := []string{
			"internal/testkit/dbparity",
			"dbparity.DefaultCatalog()",
			countNeedle,
			"make test-db-parity",
			"make test-db-parity-postgres-direct",
		}
		for _, m := range techStandardsMarkers {
			if !strings.Contains(techStandardsSection, m) {
				violations = append(violations, ".kiro/steering/tech.md: standards section missing marker '"+m+"'")
			}
		}
	}

	techCommandsSection, err := extractMarkdownSection(techContent, "## Canonical Verification Commands")
	if err != nil {
		violations = append(violations, ".kiro/steering/tech.md: "+err.Error())
	} else {
		techCommandsMarkers := []string{
			"make test-db-parity",
			"make test-db-parity-sqlite",
			"make test-db-parity-postgres-direct",
		}
		for _, m := range techCommandsMarkers {
			if !strings.Contains(techCommandsSection, m) {
				violations = append(violations, ".kiro/steering/tech.md: commands section missing marker '"+m+"'")
			}
		}
	}

	// 3. AGENTS.md
	agentsSection, err := extractMarkdownSection(agentsContent, "## Architecture Guardrails")
	if err != nil {
		violations = append(violations, "AGENTS.md: "+err.Error())
	} else {
		agentsMarkers := []string{
			"internal/testkit/dbparity",
			"dbparity.DefaultCatalog()",
			"make test-db-parity",
		}
		for _, m := range agentsMarkers {
			if !strings.Contains(agentsSection, m) {
				violations = append(violations, "AGENTS.md: guardrails section missing marker '"+m+"'")
			}
		}
	}

	return violations
}

func TestDatabaseParity_MaintainerDocsCatalogDrift(t *testing.T) {
	t.Parallel()

	catalog := dbparity.DefaultCatalog()

	persistenceDoc := readRepositoryFile(t, "docs", "database-persistence.md")
	if violations := validateDatabasePersistenceDoc(persistenceDoc, catalog); len(violations) > 0 {
		t.Fatalf("FAIL-CLOSED: docs/database-persistence.md drift violations:\n  - %s",
			strings.Join(violations, "\n  - "))
	}

	releaseGatesDoc := readRepositoryFile(t, "docs", "release-gates.md")
	if violations := validateReleaseGatesDoc(releaseGatesDoc, catalog); len(violations) > 0 {
		t.Fatalf("FAIL-CLOSED: docs/release-gates.md drift violations:\n  - %s",
			strings.Join(violations, "\n  - "))
	}
}

func TestDatabaseParity_MaintainerDocsFailClosedPolicy(t *testing.T) {
	t.Parallel()

	catalog := dbparity.DefaultCatalog()
	persistenceDoc := readRepositoryFile(t, "docs", "database-persistence.md")
	releaseGatesDoc := readRepositoryFile(t, "docs", "release-gates.md")

	if v := validateDatabasePersistenceDoc(persistenceDoc, catalog); len(v) != 0 {
		t.Fatalf("expected baseline persistenceDoc to have 0 violations, got: %v", v)
	}
	if v := validateReleaseGatesDoc(releaseGatesDoc, catalog); len(v) != 0 {
		t.Fatalf("expected baseline releaseGatesDoc to have 0 violations, got: %v", v)
	}

	persistenceNegativeCases := []struct {
		name       string
		mutate     func(*testing.T, string) string
		wantSubstr string
	}{
		{
			name: "missing catalog component ID billing",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "`billing`", "`something-else`")
			},
			wantSubstr: "missing catalog component ID \"billing\"",
		},
		{
			name: "stale or extra component ID in list",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "`billing`", "`billing`, `stale-legacy-store`")
			},
			wantSubstr: "found unexpected or stale component ID \"stale-legacy-store\"",
		},
		{
			name: "incorrect component count in persistence doc",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "It captures 8 production component families:", "It captures 7 production component families:")
			},
			wantSubstr: "documented component family count is 7, expected 8",
		},
		{
			name: "missing package path in persistence doc",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutateAll(t, s, "internal/testkit/dbparity", "internal/wrong/path")
			},
			wantSubstr: "maintainer section missing marker 'internal/testkit/dbparity'",
		},
		{
			name: "missing symbol in persistence doc",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "dbparity.DefaultCatalog()", "dbparity.OtherCatalog()")
			},
			wantSubstr: "maintainer section missing marker 'dbparity.DefaultCatalog()'",
		},
		{
			name: "missing canonical command in persistence doc",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "make test-db-parity-sqlite", "make test-sqlite-unit")
			},
			wantSubstr: "maintainer section missing marker 'make test-db-parity-sqlite'",
		},
		{
			name: "missing CI fail-closed condition in persistence doc",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "always() && needs.db-parity.result != 'success'", "needs.db-parity.result == 'failure'")
			},
			wantSubstr: "maintainer section missing marker 'always() && needs.db-parity.result != 'success''",
		},
		{
			name: "missing maintainer section header in persistence doc",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "## Maintainer guide: dual-dialect parity & enforcement", "## Other guide")
			},
			wantSubstr: "missing exact heading \"## Maintainer guide: dual-dialect parity & enforcement\"",
		},
	}

	for _, tc := range persistenceNegativeCases {
		t.Run("PersistenceDoc_"+tc.name, func(t *testing.T) {
			mutated := tc.mutate(t, persistenceDoc)
			violations := validateDatabasePersistenceDoc(mutated, catalog)
			joined := strings.Join(violations, "; ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("expected violation containing %q, got: %q", tc.wantSubstr, joined)
			}
		})
	}

	releaseGatesNegativeCases := []struct {
		name       string
		mutate     func(*testing.T, string) string
		wantSubstr string
	}{
		{
			name: "missing catalog component ID continuity",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "`continuity`", "`unknown-component`")
			},
			wantSubstr: "missing catalog component ID \"continuity\"",
		},
		{
			name: "stale extra component ID in release gates",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "`continuity`", "`continuity`, `extra-stale-store`")
			},
			wantSubstr: "found unexpected or stale component ID \"extra-stale-store\"",
		},
		{
			name: "missing section header in release gates",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "## Database dialect parity (persistence gates)", "## Persistence")
			},
			wantSubstr: "missing exact heading \"## Database dialect parity (persistence gates)\"",
		},
		{
			name: "missing QA verification command in release gates",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "go test ./internal/qa -run DatabaseParity", "go test ./internal/qa -run Other")
			},
			wantSubstr: "persistence gates section missing marker 'go test ./internal/qa -run DatabaseParity'",
		},
		{
			name: "missing archtest verification command in release gates",
			mutate: func(t *testing.T, s string) string {
				t.Helper()
				return mustMutate(t, s, "go test ./internal/archtest -run DatabaseParity", "go test ./internal/archtest -run Other")
			},
			wantSubstr: "persistence gates section missing marker 'go test ./internal/archtest -run DatabaseParity'",
		},
	}

	for _, tc := range releaseGatesNegativeCases {
		t.Run("ReleaseGatesDoc_"+tc.name, func(t *testing.T) {
			t.Parallel()
			mutated := tc.mutate(t, releaseGatesDoc)
			violations := validateReleaseGatesDoc(mutated, catalog)
			joined := strings.Join(violations, "; ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("expected violation containing %q, got: %q", tc.wantSubstr, joined)
			}
		})
	}
}

func TestDatabaseParity_SteeringAndAgentsDocsDrift(t *testing.T) {
	t.Parallel()

	catalog := dbparity.DefaultCatalog()
	testingDoc := readRepositoryFile(t, ".kiro", "steering", "testing.md")
	techDoc := readRepositoryFile(t, ".kiro", "steering", "tech.md")
	agentsDoc := readRepositoryFile(t, "AGENTS.md")

	violations := validateSteeringAndAgentsDocs(testingDoc, techDoc, agentsDoc, catalog)
	if len(violations) > 0 {
		t.Fatalf("FAIL-CLOSED: Steering / AGENTS.md database parity drift violations:\n  - %s",
			strings.Join(violations, "\n  - "))
	}
}

func TestDatabaseParity_SteeringAndAgentsDocsFailClosedPolicy(t *testing.T) {
	t.Parallel()

	catalog := dbparity.DefaultCatalog()
	testingDoc := readRepositoryFile(t, ".kiro", "steering", "testing.md")
	techDoc := readRepositoryFile(t, ".kiro", "steering", "tech.md")
	agentsDoc := readRepositoryFile(t, "AGENTS.md")

	if v := validateSteeringAndAgentsDocs(testingDoc, techDoc, agentsDoc, catalog); len(v) != 0 {
		t.Fatalf("expected baseline steering and agents docs to have 0 violations, got: %v", v)
	}

	negativeCases := []struct {
		name       string
		mutate     func(*testing.T, string, string, string) (string, string, string)
		wantSubstr string
	}{
		{
			name: "missing package path in testing.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return mustMutateAll(t, testingDoc, "internal/testkit/dbparity", "internal/other"), techDoc, agentsDoc
			},
			wantSubstr: ".kiro/steering/testing.md: section missing marker 'internal/testkit/dbparity'",
		},
		{
			name: "missing symbol in testing.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return mustMutateAll(t, testingDoc, "dbparity.DefaultCatalog()", "dbparity.Other()"), techDoc, agentsDoc
			},
			wantSubstr: ".kiro/steering/testing.md: section missing marker 'dbparity.DefaultCatalog()'",
		},
		{
			name: "missing count in testing.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return mustMutate(t, testingDoc, "8 component families", "6 component families"), techDoc, agentsDoc
			},
			wantSubstr: ".kiro/steering/testing.md: section missing marker",
		},
		{
			name: "missing CI fail-closed condition in testing.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return mustMutate(t, testingDoc, "always() && needs.db-parity.result != 'success'", "needs.db-parity.result == 'failure'"), techDoc, agentsDoc
			},
			wantSubstr: ".kiro/steering/testing.md: section missing marker 'always() && needs.db-parity.result != 'success''",
		},
		{
			name: "missing package path in tech.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return testingDoc, mustMutateAll(t, techDoc, "internal/testkit/dbparity", "internal/other"), agentsDoc
			},
			wantSubstr: ".kiro/steering/tech.md: standards section missing marker 'internal/testkit/dbparity'",
		},
		{
			name: "missing symbol in tech.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return testingDoc, mustMutateAll(t, techDoc, "dbparity.DefaultCatalog()", "dbparity.Other()"), agentsDoc
			},
			wantSubstr: ".kiro/steering/tech.md: standards section missing marker 'dbparity.DefaultCatalog()'",
		},
		{
			name: "missing count in tech.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return testingDoc, mustMutate(t, techDoc, "All 8 production persistence families", "All 5 production persistence families"), agentsDoc
			},
			wantSubstr: ".kiro/steering/tech.md: standards section missing marker",
		},
		{
			name: "missing package path in AGENTS.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return testingDoc, techDoc, mustMutateAll(t, agentsDoc, "internal/testkit/dbparity", "internal/other")
			},
			wantSubstr: "AGENTS.md: guardrails section missing marker 'internal/testkit/dbparity'",
		},
		{
			name: "missing symbol in AGENTS.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return testingDoc, techDoc, mustMutateAll(t, agentsDoc, "dbparity.DefaultCatalog()", "dbparity.Other()")
			},
			wantSubstr: "AGENTS.md: guardrails section missing marker 'dbparity.DefaultCatalog()'",
		},
		{
			name: "missing canonical command in AGENTS.md",
			mutate: func(t *testing.T, testingDoc, techDoc, agentsDoc string) (string, string, string) {
				t.Helper()
				return testingDoc, techDoc, mustMutateAll(t, agentsDoc, "make test-db-parity", "make test-unit")
			},
			wantSubstr: "AGENTS.md: guardrails section missing marker 'make test-db-parity'",
		},
	}

	for _, tc := range negativeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mTesting, mTech, mAgents := tc.mutate(t, testingDoc, techDoc, agentsDoc)
			violations := validateSteeringAndAgentsDocs(mTesting, mTech, mAgents, catalog)
			joined := strings.Join(violations, "; ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("expected violation containing %q, got: %q", tc.wantSubstr, joined)
			}
		})
	}
}
