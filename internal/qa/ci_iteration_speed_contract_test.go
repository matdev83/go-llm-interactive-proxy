package qa

import (
	"os"
	"strings"
	"testing"
)

func TestCIIterationSpeed_ModuleTidyUsesBoundedParallelism(t *testing.T) {
	body, err := os.ReadFile(repositoryFile(t, "scripts", "tidy-all-modules.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, needle := range []string{
		"LIP_MODULE_CHECK_JOBS",
		"xargs -r -P\"$JOBS\"",
		"go build -o \"$DISCOVER_MODULES_BIN\" ./tools/backendplugin/discover_modules",
		"GOWORK=off go mod tidy",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("tidy-all-modules.sh missing bounded/reused helper contract %q", needle)
		}
	}
}

func TestCIIterationSpeed_WorkflowConcurrencyAndCaches(t *testing.T) {
	for _, name := range []string{
		"ci.yml",
		"qa.yml",
		"security.yml",
		"release.yml",
		"race-fuzz-nightly.yml",
		"codeql.yml",
	} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		if !strings.Contains(text, "github.head_ref || github.ref_name") && name != "release.yml" && name != "race-fuzz-nightly.yml" {
			t.Errorf("%s does not share branch/PR concurrency identity", name)
		}
	}

	qa := readRepositoryFile(t, ".github", "workflows", "qa.yml")
	if !strings.Contains(qa, "CI owns the portable cmd/lipstd test/build matrix") {
		t.Fatal("QA ownership documentation is missing")
	}
	if strings.Contains(qa, "go test -timeout=5m ./cmd/lipstd") {
		t.Fatal("QA must not duplicate the CI cmd/lipstd test")
	}
	ci := readRepositoryFile(t, ".github", "workflows", "ci.yml")
	for _, needle := range []string{"go test -timeout=8m ${{ matrix.packages }}", "go build -trimpath ./cmd/lipstd"} {
		if !strings.Contains(ci, needle) {
			t.Fatalf("CI no longer owns portable cmd/lipstd evidence %q", needle)
		}
	}

	for _, name := range []string{
		"security.yml",
		"release.yml",
		"race-fuzz-nightly.yml",
		"optional-gosec.yml",
		"benchmarks.yml",
		"modernize-monthly.yml",
		"reasoning-e2e-soak-nightly.yml",
	} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		for _, needle := range []string{
			"connectors/**/go.sum",
			"connector-support/**/go.sum",
			"testdata/enterprise_module/go.sum",
			"tools/**/go.sum",
		} {
			if !strings.Contains(text, needle) {
				t.Errorf("%s cache-dependency-path missing %q", name, needle)
			}
		}
	}
}
