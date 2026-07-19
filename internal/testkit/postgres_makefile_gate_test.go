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
