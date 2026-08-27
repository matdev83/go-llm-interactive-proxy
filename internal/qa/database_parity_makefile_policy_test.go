package qa

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const (
	canonicalBlockSQLite = "test-db-parity-sqlite:\n" +
		"\t$(GO) run ./internal/testkit/dbparity/cmd sqlite"

	canonicalBlockPostgresDirect = "test-db-parity-postgres-direct:\n" +
		"ifeq ($(OS),Windows_NT)\n" +
		"\t@powershell -NoProfile -Command \"[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES','1','Process'); if ([Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process')) { [Environment]::SetEnvironmentVariable('LIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; & '$(GO)' run ./internal/testkit/dbparity/cmd postgres-direct\"\n" +
		"else\n" +
		"\t@LIP_REQUIRE_POSTGRES=1 LIP_TEST_POSTGRES_DSN=\"$${LIP_TEST_POSTGRES_ADMIN_DSN:-$$LIP_TEST_POSTGRES_DSN}\" $(GO) run ./internal/testkit/dbparity/cmd postgres-direct\n" +
		"endif"

	canonicalBlockDBParity = "test-db-parity:\n" +
		"ifeq ($(OS),Windows_NT)\n" +
		"\t@powershell -NoProfile -Command \"[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES','1','Process'); if ([Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process')) { [Environment]::SetEnvironmentVariable('LIP_TEST_POSTGRES_DSN',[Environment]::GetEnvironmentVariable('LIP_TEST_POSTGRES_ADMIN_DSN','Process'),'Process') }; & '$(GO)' run ./internal/testkit/dbparity/cmd all\"\n" +
		"else\n" +
		"\t@LIP_REQUIRE_POSTGRES=1 LIP_TEST_POSTGRES_DSN=\"$${LIP_TEST_POSTGRES_DSN:-$$LIP_TEST_POSTGRES_ADMIN_DSN}\" $(GO) run ./internal/testkit/dbparity/cmd all\n" +
		"endif"
)

func isColumnZeroTargetDecl(line string) bool {
	if len(line) == 0 || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
		return false
	}
	colonIdx := strings.Index(line, ":")
	if colonIdx <= 0 {
		return false
	}
	if strings.HasPrefix(line[colonIdx:], ":=") {
		return false
	}
	if eqIdx := strings.Index(line, "="); eqIdx >= 0 && eqIdx < colonIdx {
		return false
	}
	fields := strings.Fields(line)
	if len(fields) > 0 {
		first := fields[0]
		if strings.HasPrefix(first, "ifeq") || strings.HasPrefix(first, "ifneq") ||
			strings.HasPrefix(first, "ifdef") || strings.HasPrefix(first, "ifndef") ||
			first == "else" || first == "endif" || first == "include" || first == "define" || first == "endef" {
			return false
		}
	}
	return true
}

func extractTargetBlock(content, targetName string) (string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	targetPrefix := targetName + ":"

	startIdx := -1
	for i, line := range lines {
		trimmedRight := strings.TrimRight(line, " \t")
		if strings.HasPrefix(trimmedRight, targetPrefix) && isColumnZeroTargetDecl(trimmedRight) {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return "", fmt.Errorf("missing Makefile target '%s:'", targetName)
	}

	var blockLines []string
	blockLines = append(blockLines, strings.TrimRight(lines[startIdx], " \t"))

	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		trimmedRight := strings.TrimRight(line, " \t")
		if isColumnZeroTargetDecl(trimmedRight) {
			break
		}
		blockLines = append(blockLines, trimmedRight)
	}

	for len(blockLines) > 1 {
		last := strings.TrimSpace(blockLines[len(blockLines)-1])
		if last == "" || strings.HasPrefix(last, "#") {
			blockLines = blockLines[:len(blockLines)-1]
		} else {
			break
		}
	}

	return strings.Join(blockLines, "\n"), nil
}

func parsePhonyTargets(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var phony []string

	defineDepth := 0
	condDepth := 0
	unbalanced := false
	inPhony := false

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t\r")

		// Track non-recipe define / endef / conditional directives
		if !strings.HasPrefix(rawLine, "\t") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				fields := strings.Fields(trimmed)
				if len(fields) > 0 {
					first := fields[0]
					if first == "define" || strings.HasPrefix(first, "define") {
						defineDepth++
						inPhony = false
						continue
					}
					if first == "endef" {
						defineDepth--
						inPhony = false
						if defineDepth < 0 {
							unbalanced = true
						}
						continue
					}
					if defineDepth == 0 {
						if first == "ifeq" || strings.HasPrefix(first, "ifeq") ||
							first == "ifneq" || strings.HasPrefix(first, "ifneq") ||
							first == "ifdef" || strings.HasPrefix(first, "ifdef") ||
							first == "ifndef" || strings.HasPrefix(first, "ifndef") {
							condDepth++
							inPhony = false
							continue
						}
						if first == "else" || strings.HasPrefix(first, "else") {
							inPhony = false
							if condDepth <= 0 {
								unbalanced = true
							}
							continue
						}
						if first == "endif" || strings.HasPrefix(first, "endif") {
							condDepth--
							inPhony = false
							if condDepth < 0 {
								unbalanced = true
							}
							continue
						}
					}
				}
			}
		}

		if defineDepth > 0 || condDepth > 0 {
			continue
		}

		if inPhony {
			if isColumnZeroTargetDecl(line) {
				inPhony = false
			}
		}

		if inPhony {
			trimmed := strings.TrimSpace(line)
			hasCont := strings.HasSuffix(trimmed, "\\")
			if hasCont {
				trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "\\"))
			} else {
				inPhony = false
			}

			if commentIdx := strings.Index(trimmed, "#"); commentIdx >= 0 {
				trimmed = strings.TrimSpace(trimmed[:commentIdx])
			}

			if trimmed != "" {
				phony = append(phony, strings.Fields(trimmed)...)
			}
			continue
		}

		// Only parse genuine column-zero .PHONY: declarations
		if strings.HasPrefix(rawLine, ".PHONY:") {
			trimmed := strings.TrimSpace(strings.TrimPrefix(rawLine, ".PHONY:"))
			hasCont := strings.HasSuffix(trimmed, "\\")
			if hasCont {
				trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "\\"))
				inPhony = true
			}
			if commentIdx := strings.Index(trimmed, "#"); commentIdx >= 0 {
				trimmed = strings.TrimSpace(trimmed[:commentIdx])
			}
			if trimmed != "" {
				phony = append(phony, strings.Fields(trimmed)...)
			}
		}
	}

	if defineDepth != 0 || condDepth != 0 || unbalanced {
		return nil
	}

	return phony
}

// validateMakefileDatabaseParity inspects Makefile content against Task 5.2 / requirements 7.2-7.4, 9.1-9.5
// and ensures canonical targets exist with exact line sequences and .PHONY registration.
func validateMakefileDatabaseParity(content string) []string {
	var violations []string

	// 1. Check .PHONY membership
	phony := parsePhonyTargets(content)
	for _, target := range []string{"test-db-parity-sqlite", "test-db-parity-postgres-direct", "test-db-parity"} {
		if !slices.Contains(phony, target) {
			violations = append(violations, "Makefile .PHONY declaration must include canonical database parity target '"+target+"'")
		}
	}

	// 2. Exact normalized line sequence for the 3 target blocks
	canonicalBlocks := map[string]string{
		"test-db-parity-sqlite":          canonicalBlockSQLite,
		"test-db-parity-postgres-direct": canonicalBlockPostgresDirect,
		"test-db-parity":                 canonicalBlockDBParity,
	}

	for _, target := range []string{"test-db-parity-sqlite", "test-db-parity-postgres-direct", "test-db-parity"} {
		expected := canonicalBlocks[target]
		block, err := extractTargetBlock(content, target)
		if err != nil {
			violations = append(violations, "missing Makefile target '"+target+":'")
			continue
		}
		if block != expected {
			violations = append(violations, fmt.Sprintf("target %q recipe block does not match canonical definition", target))
		}
	}

	// 3. Help target entries
	helpBlock, err := extractTargetBlock(content, "help")
	if err != nil {
		violations = append(violations, "missing Makefile target 'help:'")
	} else {
		helpLines := strings.Split(helpBlock, "\n")
		requiredHelpLines := []struct {
			label string
			line  string
		}{
			{
				label: "make test-db-parity-sqlite",
				line:  "\t@echo \"  make test-db-parity-sqlite - canonical SQLite database parity tests across all registered components\"",
			},
			{
				label: "make test-db-parity-postgres-direct",
				line:  "\t@echo \"  make test-db-parity-postgres-direct - repository-wide fail-closed direct PostgreSQL parity (DSN; Make sets LIP_REQUIRE_POSTGRES=1)\"",
			},
			{
				label: "make test-db-parity ",
				line:  "\t@echo \"  make test-db-parity  - sequential repository-wide SQLite and direct PostgreSQL parity gate\"",
			},
			{
				label: "make test-authority-postgres-direct",
				line:  "\t@echo \"  make test-authority-postgres-direct - direct PostgreSQL runtime proof (DSN; Make sets require flag)\"",
			},
			{
				label: "make test-authority-postgres-pooled",
				line:  "\t@echo \"  make test-authority-postgres-pooled - transaction-pooled runtime proof (requires LIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1)\"",
			},
			{
				label: "make test-postgres-migrations",
				line:  "\t@echo \"  make test-postgres-migrations - apply and verify dual-plane PostgreSQL migrations\"",
			},
			{
				label: "make billing-convergence-certify",
				line:  "\t@echo \"  make billing-convergence-certify - fail-fast final billing architecture, integration, quality, docs, and race certification\"",
			},
		}
		for _, req := range requiredHelpLines {
			if !slices.Contains(helpLines, req.line) {
				violations = append(violations, "Makefile help missing entry "+req.label)
			}
		}
	}

	return violations
}

func TestDatabaseParity_MakefileWiring(t *testing.T) {
	t.Parallel()

	content := readRepositoryFile(t, "Makefile")
	violations := validateMakefileDatabaseParity(content)
	if len(violations) > 0 {
		t.Fatalf("FAIL-CLOSED: Makefile database parity wiring violations:\n  - %s",
			strings.Join(violations, "\n  - "))
	}
}

func TestDatabaseParity_MakefileFailClosedPolicy(t *testing.T) {
	t.Parallel()

	makefileContent := readRepositoryFile(t, "Makefile")
	if v := validateMakefileDatabaseParity(makefileContent); len(v) != 0 {
		t.Fatalf("expected repository Makefile to have 0 violations, got: %v", v)
	}

	negativeCases := []struct {
		name       string
		mutate     func(*testing.T, string) string
		wantSubstr string
	}{
		{
			name: "missing test-db-parity-sqlite target",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "test-db-parity-sqlite:\n\t$(GO) run ./internal/testkit/dbparity/cmd sqlite", "")
			},
			wantSubstr: "missing Makefile target 'test-db-parity-sqlite:'",
		},
		{
			name: "missing test-db-parity-postgres-direct target",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "test-db-parity-postgres-direct:\n", "# removed postgres-direct\n_disabled_target:\n")
			},
			wantSubstr: "missing Makefile target 'test-db-parity-postgres-direct:'",
		},
		{
			name: "missing test-db-parity target",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "test-db-parity:\n", "# removed test-db-parity\n_disabled_target:\n")
			},
			wantSubstr: "missing Makefile target 'test-db-parity:'",
		},
		{
			name: "missing target in .PHONY",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, ".PHONY: test-db-parity-sqlite", ".PHONY: other-target")
			},
			wantSubstr: "Makefile .PHONY declaration must include canonical database parity target 'test-db-parity-sqlite'",
		},
		{
			name: "recipe drift in test-db-parity-sqlite",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "./internal/testkit/dbparity/cmd sqlite", "./internal/testkit/dbparity/cmd sqlite ./internal/infra/billingstore")
			},
			wantSubstr: "target \"test-db-parity-sqlite\" recipe block does not match canonical definition",
		},
		{
			name: "recipe drift in test-db-parity",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "./internal/testkit/dbparity/cmd all", "./internal/testkit/dbparity/cmd all ./internal/core/continuity")
			},
			wantSubstr: "target \"test-db-parity\" recipe block does not match canonical definition",
		},
		{
			name: "missing LIP_REQUIRE_POSTGRES on POSIX in test-db-parity-postgres-direct",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "@LIP_REQUIRE_POSTGRES=1 LIP_TEST_POSTGRES_DSN=\"$${LIP_TEST_POSTGRES_ADMIN_DSN:-$$LIP_TEST_POSTGRES_DSN}\" $(GO) run ./internal/testkit/dbparity/cmd postgres-direct", "@LIP_TEST_POSTGRES_DSN=\"$${LIP_TEST_POSTGRES_ADMIN_DSN:-$$LIP_TEST_POSTGRES_DSN}\" $(GO) run ./internal/testkit/dbparity/cmd postgres-direct")
			},
			wantSubstr: "target \"test-db-parity-postgres-direct\" recipe block does not match canonical definition",
		},
		{
			name: "missing Windows SetEnvironmentVariable in test-db-parity",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "test-db-parity:\n")
				if idx < 0 {
					t.Fatalf("test-db-parity target not found")
				}
				return s[:idx] + mustMutate(t, s[idx:], "[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES','1','Process'); ", "")
			},
			wantSubstr: "target \"test-db-parity\" recipe block does not match canonical definition",
		},
		{
			name: "target prerequisites on test-db-parity",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "test-db-parity:\n", "test-db-parity: test-db-parity-sqlite test-db-parity-postgres-direct\n")
			},
			wantSubstr: "target \"test-db-parity\" recipe block does not match canonical definition",
		},
		{
			name: "missing help entry for test-db-parity",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "make test-db-parity ", "make other ")
			},
			wantSubstr: "Makefile help missing entry make test-db-parity ",
		},
		{
			name: "false conditional in test-db-parity-postgres-direct (ifeq (1,0))",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "test-db-parity-postgres-direct:\nifeq ($(OS),Windows_NT)", "test-db-parity-postgres-direct:\nifeq (1,0)")
			},
			wantSubstr: "target \"test-db-parity-postgres-direct\" recipe block does not match canonical definition",
		},
		{
			name: "powershell using -File instead of -Command in test-db-parity",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "test-db-parity:\n")
				if idx < 0 {
					t.Fatalf("test-db-parity target not found")
				}
				return s[:idx] + mustMutate(t, s[idx:], "-Command", "-File script.ps1")
			},
			wantSubstr: "target \"test-db-parity\" recipe block does not match canonical definition",
		},
		{
			name: "reordered runner argv in test-db-parity-sqlite",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "./internal/testkit/dbparity/cmd sqlite", "sqlite ./internal/testkit/dbparity/cmd")
			},
			wantSubstr: "target \"test-db-parity-sqlite\" recipe block does not match canonical definition",
		},
		{
			name: "continued .PHONY followed by comment listing canonical targets",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s,
					".PHONY: test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity",
					".PHONY: other-target \\\n# test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity",
				)
			},
			wantSubstr: "Makefile .PHONY declaration must include canonical database parity target 'test-db-parity-sqlite'",
		},
		{
			name: "tab-indented .PHONY inside recipe cannot satisfy membership",
			mutate: func(t *testing.T, s string) string {
				s = mustMutate(t, s,
					".PHONY: test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity\n",
					"",
				)
				return mustMutate(t, s,
					"help:\n",
					"help:\n\t.PHONY: test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity\n",
				)
			},
			wantSubstr: "Makefile .PHONY declaration must include canonical database parity target 'test-db-parity-sqlite'",
		},
		{
			name: "commented-out help entry with tab recipe comment",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s,
					"\t@echo \"  make test-db-parity-sqlite",
					"\t# @echo \"  make test-db-parity-sqlite",
				)
			},
			wantSubstr: "Makefile help missing entry make test-db-parity-sqlite",
		},
		{
			name: "commented-out help entry with non-recipe comment",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s,
					"\t@echo \"  make test-db-parity-postgres-direct",
					"# make test-db-parity-postgres-direct",
				)
			},
			wantSubstr: "Makefile help missing entry make test-db-parity-postgres-direct",
		},
		{
			name: "non-output @true command in help recipe cannot satisfy entry requirement",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s,
					"\t@echo \"  make test-db-parity-sqlite",
					"\t@true \"  make test-db-parity-sqlite",
				)
			},
			wantSubstr: "Makefile help missing entry make test-db-parity-sqlite",
		},
		{
			name: "canonical .PHONY inside define body is inert and rejected",
			mutate: func(t *testing.T, s string) string {
				s = mustMutate(t, s,
					".PHONY: test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity\n",
					"",
				)
				return mustMutate(t, s,
					"help:\n",
					"define INERT_MACRO\n.PHONY: test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity\nendef\n\nhelp:\n",
				)
			},
			wantSubstr: "Makefile .PHONY declaration must include canonical database parity target 'test-db-parity-sqlite'",
		},
		{
			name: "exact help recipe line redirected to >NUL cannot satisfy entry requirement",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s,
					"\t@echo \"  make test-db-parity-sqlite - canonical SQLite database parity tests across all registered components\"",
					"\t@echo \"  make test-db-parity-sqlite - canonical SQLite database parity tests across all registered components\" >NUL",
				)
			},
			wantSubstr: "Makefile help missing entry make test-db-parity-sqlite",
		},
		{
			name: "canonical .PHONY inside ifeq conditional is inert and rejected",
			mutate: func(t *testing.T, s string) string {
				s = mustMutate(t, s,
					".PHONY: test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity\n",
					"",
				)
				return mustMutate(t, s,
					"help:\n",
					"ifeq (1,0)\n.PHONY: test-db-parity-sqlite test-db-parity-postgres-direct test-db-parity\nendif\n\nhelp:\n",
				)
			},
			wantSubstr: "Makefile .PHONY declaration must include canonical database parity target 'test-db-parity-sqlite'",
		},
	}

	for _, tc := range negativeCases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := tc.mutate(t, makefileContent)
			violations := validateMakefileDatabaseParity(mutated)
			joined := strings.Join(violations, "; ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("expected violation containing %q, got: %q", tc.wantSubstr, joined)
			}
		})
	}
}
