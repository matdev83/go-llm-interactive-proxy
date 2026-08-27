package testkit

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMakefile_AuthorityPostgresPooledUsesNormalParallelism(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	assertMakefileTarget(t, text, "test-authority-postgres-pooled:", func(t *testing.T, rest string) {
		t.Helper()
		if !strings.Contains(rest, "$(GO_TEST_FLAGS)") {
			t.Fatalf("must use $$(GO_TEST_FLAGS) for normal -parallel=8")
		}
		if strings.Contains(rest, "-parallel=1") {
			t.Fatal("must not force -parallel=1 (pooled runtime correctness proof)")
		}
		if !strings.Contains(rest, "LIP_REQUIRE_POSTGRES_POOLER") {
			t.Fatal("must fail closed via LIP_REQUIRE_POSTGRES_POOLER")
		}
		if !strings.Contains(rest, `test "$${LIP_TEST_POSTGRES_RUNTIME_IS_POOLER:-}" = "1"`) {
			t.Fatal("must require exact =1 attestation on the Unix branch")
		}
		if !strings.Contains(rest, `GetEnvironmentVariable('LIP_TEST_POSTGRES_RUNTIME_IS_POOLER','Process') -ne '1'`) {
			t.Fatal("must require exact =1 attestation on the Windows branch")
		}
		// Must cover TestPostgresPooled_* plus Phase*_PostgresPooled_* / DirectPooled_*
		// (exact ^TestPostgresPooled_ alone orphans Phase3/Phase34 pooled contracts).
		hasBroad := strings.Contains(rest, `-run 'Pooled'`) || strings.Contains(rest, `-run "Pooled"`)
		hasExact := strings.Contains(rest, `-run '^TestPostgresPooled_'`) || strings.Contains(rest, `-run "^TestPostgresPooled_"`)
		hasPhaseOrDirect := strings.Contains(rest, "Phase3") || strings.Contains(rest, "Phase34") || strings.Contains(rest, "DirectPooled")
		if !hasBroad && (!hasExact || !hasPhaseOrDirect) {
			t.Fatal("pooled gate must select TestPostgresPooled_* and Phase*_PostgresPooled_* (use -run 'Pooled' or equivalent)")
		}
		if !strings.Contains(rest, "./internal/infra/runtimebundle") {
			t.Fatal("must include runtimebundle composition proof in pooled gate")
		}
		if !strings.Contains(rest, "./internal/infra/terminalwork/workstore") {
			t.Fatal("must include terminalwork/workstore pooled contracts in pooled gate")
		}
	})
}

func TestMakefile_AuthorityPostgresDirectSkipsPooled(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	assertMakefileTarget(t, string(body), "test-authority-postgres-direct:", func(t *testing.T, rest string) {
		t.Helper()
		if !strings.Contains(rest, "$(GO_TEST_FLAGS)") {
			t.Fatalf("must use $$(GO_TEST_FLAGS) for normal -parallel=8")
		}
		// Broad -skip 'Pooled' also excludes Phase*_PostgresPooled_* helpers that
		// do not match the TestPostgresPooled_ prefix.
		hasExact := strings.Contains(rest, `-skip '^TestPostgresPooled_'`) || strings.Contains(rest, `-skip "^TestPostgresPooled_"`)
		hasBroad := strings.Contains(rest, `-skip 'Pooled'`) || strings.Contains(rest, `-skip "Pooled"`)
		if !hasExact && !hasBroad {
			t.Fatal("must skip TestPostgresPooled_ (or broader Pooled) so direct and pooled gates stay separate")
		}
		if strings.Contains(rest, `-run '^TestPostgresPooled_'`) || strings.Contains(rest, `-run "^TestPostgresPooled_"`) {
			t.Fatal("direct gate must not select only pooled tests")
		}
		if !strings.Contains(rest, "./internal/infra/terminalwork/workstore") {
			t.Fatal("must include terminalwork/workstore direct contracts in direct gate")
		}
	})
}

func TestMakefile_DBParityTargetsDelegation(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	forbiddenPkgSubstrings := []string{
		"usageauthority",
		"concurrencyauthority",
		"journalstore",
		"workstore",
		"internal/infra",
		"internal/core/continuity",
	}

	// 1. test-db-parity-sqlite delegates to runner in sqlite mode without hardcoded package lists
	assertMakefileTarget(t, text, "test-db-parity-sqlite:", func(t *testing.T, rest string) {
		t.Helper()
		if !strings.Contains(rest, "./internal/testkit/dbparity/cmd sqlite") {
			t.Fatalf("test-db-parity-sqlite must delegate to dbparity runner with 'sqlite' mode, got:\n%s", rest)
		}
		for _, forbidden := range forbiddenPkgSubstrings {
			if strings.Contains(rest, forbidden) {
				t.Fatalf("test-db-parity-sqlite must not hardcode package list (found %q in %s)", forbidden, rest)
			}
		}
	})

	// 2. test-db-parity-postgres-direct sets LIP_REQUIRE_POSTGRES=1 and delegates to runner in postgres-direct mode
	assertMakefileTarget(t, text, "test-db-parity-postgres-direct:", func(t *testing.T, rest string) {
		t.Helper()
		if !strings.Contains(rest, "./internal/testkit/dbparity/cmd postgres-direct") {
			t.Fatalf("test-db-parity-postgres-direct must delegate to dbparity runner with 'postgres-direct' mode, got:\n%s", rest)
		}
		if !strings.Contains(rest, "LIP_REQUIRE_POSTGRES=1") {
			t.Fatalf("test-db-parity-postgres-direct must set LIP_REQUIRE_POSTGRES=1 on POSIX branch")
		}
		if !strings.Contains(rest, `[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES','1','Process')`) {
			t.Fatalf("test-db-parity-postgres-direct must set LIP_REQUIRE_POSTGRES=1 on Windows branch")
		}
		if !strings.Contains(rest, "LIP_TEST_POSTGRES_ADMIN_DSN") {
			t.Fatalf("test-db-parity-postgres-direct must support LIP_TEST_POSTGRES_ADMIN_DSN fallback")
		}
		for _, forbidden := range forbiddenPkgSubstrings {
			if strings.Contains(rest, forbidden) {
				t.Fatalf("test-db-parity-postgres-direct must not hardcode package list (found %q in %s)", forbidden, rest)
			}
		}
	})

	// 3. test-db-parity delegates to canonical runner in 'all' mode with LIP_REQUIRE_POSTGRES=1 wiring per branch
	assertMakefileTarget(t, text, "test-db-parity:", func(t *testing.T, rest string) {
		t.Helper()
		assertTargetNoPrerequisites(t, rest, "test-db-parity:")

		winBranch, posixBranch := splitMakefileBranches(t, rest)

		// Windows branch: delegates with 'all' mode and fail-closed env wiring
		if !strings.Contains(winBranch, "./internal/testkit/dbparity/cmd all") {
			t.Fatalf("test-db-parity Windows branch must invoke dbparity runner with 'all' mode, got:\n%s", winBranch)
		}
		if !strings.Contains(winBranch, `[Environment]::SetEnvironmentVariable('LIP_REQUIRE_POSTGRES','1','Process')`) {
			t.Fatalf("test-db-parity Windows branch must set LIP_REQUIRE_POSTGRES=1 via SetEnvironmentVariable, got:\n%s", winBranch)
		}
		if !strings.Contains(winBranch, "LIP_TEST_POSTGRES_ADMIN_DSN") || !strings.Contains(winBranch, `SetEnvironmentVariable('LIP_TEST_POSTGRES_DSN'`) {
			t.Fatalf("test-db-parity Windows branch must support LIP_TEST_POSTGRES_ADMIN_DSN fallback, got:\n%s", winBranch)
		}

		// POSIX branch: delegates with 'all' mode and fail-closed env wiring
		if !strings.Contains(posixBranch, "./internal/testkit/dbparity/cmd all") {
			t.Fatalf("test-db-parity POSIX branch must invoke dbparity runner with 'all' mode, got:\n%s", posixBranch)
		}
		if !strings.Contains(posixBranch, "LIP_REQUIRE_POSTGRES=1") {
			t.Fatalf("test-db-parity POSIX branch must set LIP_REQUIRE_POSTGRES=1 prefix, got:\n%s", posixBranch)
		}
		if !strings.Contains(posixBranch, `LIP_TEST_POSTGRES_DSN="$${LIP_TEST_POSTGRES_DSN:-$$LIP_TEST_POSTGRES_ADMIN_DSN}"`) {
			t.Fatalf("test-db-parity POSIX branch must contain LIP_TEST_POSTGRES_DSN fallback expression, got:\n%s", posixBranch)
		}

		for _, forbidden := range forbiddenPkgSubstrings {
			if strings.Contains(rest, forbidden) {
				t.Fatalf("test-db-parity must not hardcode package list (found %q in %s)", forbidden, rest)
			}
		}
	})
}

func TestMakefile_DBParityHelpDistinction(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	assertMakefileTarget(t, text, "help:", func(t *testing.T, rest string) {
		t.Helper()
		requiredHelpEntries := []string{
			"make test-db-parity-sqlite",
			"make test-db-parity-postgres-direct",
			"make test-db-parity",
			"make test-authority-postgres-direct",
			"make test-authority-postgres-pooled",
			"make test-postgres-migrations",
			"make billing-convergence-certify",
		}
		for _, entry := range requiredHelpEntries {
			if !strings.Contains(rest, entry) {
				t.Errorf("Makefile help must document %q", entry)
			}
		}
	})
}

func assertMakefileTarget(t *testing.T, text, target string, check func(*testing.T, string)) {
	t.Helper()
	idx := strings.Index(text, target)
	if idx < 0 {
		t.Fatalf("Makefile missing %s", target)
	}
	rest := text[idx:]
	if end := strings.Index(rest[len(target):], "\n\n"); end >= 0 {
		rest = rest[:len(target)+end]
	}
	check(t, rest)
}

func assertTargetNoPrerequisites(t *testing.T, recipe, targetPrefix string) {
	t.Helper()
	normalized := strings.ReplaceAll(recipe, "\r\n", "\n")
	firstLine := strings.Split(normalized, "\n")[0]
	if !strings.HasPrefix(firstLine, targetPrefix) {
		t.Fatalf("target line does not start with %q: %q", targetPrefix, firstLine)
	}
	prereqs := strings.TrimSpace(strings.TrimPrefix(firstLine, targetPrefix))
	if prereqs != "" {
		t.Fatalf("target %q must have no prerequisites on declaration line, found: %q", targetPrefix, prereqs)
	}
}

func splitMakefileBranches(t *testing.T, recipe string) (winBranch, posixBranch string) {
	t.Helper()
	normalized := strings.ReplaceAll(recipe, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var winLines, posixLines []string
	inWin := false
	inPosix := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ifeq ($(OS),Windows_NT)") {
			inWin = true
			inPosix = false
			continue
		}
		if trimmed == "else" {
			inWin = false
			inPosix = true
			continue
		}
		if trimmed == "endif" {
			inWin = false
			inPosix = false
			continue
		}
		if inWin {
			winLines = append(winLines, line)
		} else if inPosix {
			posixLines = append(posixLines, line)
		}
	}
	if len(winLines) == 0 || len(posixLines) == 0 {
		t.Fatalf("failed to extract both Windows and POSIX branches from recipe:\n%s", recipe)
	}
	return strings.Join(winLines, "\n"), strings.Join(posixLines, "\n")
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
