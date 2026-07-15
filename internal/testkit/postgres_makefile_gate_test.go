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
		if !strings.Contains(rest, `-run '^TestPostgresPooled_'`) && !strings.Contains(rest, `-run "^TestPostgresPooled_"`) {
			t.Fatal("must select only TestPostgresPooled_ via fail-closed -run regex")
		}
		if !strings.Contains(rest, "./internal/infra/runtimebundle") {
			t.Fatal("must include runtimebundle composition proof in pooled gate")
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
		if !strings.Contains(rest, `-skip '^TestPostgresPooled_'`) && !strings.Contains(rest, `-skip "^TestPostgresPooled_"`) {
			t.Fatal("must skip TestPostgresPooled_ so direct and pooled gates stay separate")
		}
		if strings.Contains(rest, `-run '^TestPostgresPooled_'`) || strings.Contains(rest, `-run "^TestPostgresPooled_"`) {
			t.Fatal("direct gate must not select only pooled tests")
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
