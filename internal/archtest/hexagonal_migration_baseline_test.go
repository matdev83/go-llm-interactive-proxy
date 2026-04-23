package archtest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	hexagonalBaselineRelPath = "testdata/architecture/hexagonal_migration_baseline.json"
	internalCoreImportPrefix = "github.com/matdev83/go-llm-interactive-proxy/internal/core/"
)

type hexagonalBaselineFile struct {
	SchemaVersion     int                      `json:"schema_version"`
	RetiredExceptions []string                 `json:"retired_exceptions"`
	Packages          []hexagonalBaselineEntry `json:"packages"`
}

type hexagonalBaselineEntry struct {
	GoListPattern              string   `json:"go_list_pattern"`
	Classification             string   `json:"classification"`
	Justification              string   `json:"justification"`
	RetirementTrigger          string   `json:"retirement_trigger"`
	AllowedInternalCoreImports []string `json:"allowed_internal_core_imports"`
}

// TestHexagonalMigrationBaselineIncludesAllClassifications ensures the migration
// register still models aligned, extract, and exception coexistence (introduce-
// hexagonal-architecture task 7.2); shrinking to a single class is a deliberate
// doc+test change, not silent drift.
func TestHexagonalMigrationBaselineIncludesAllClassifications(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, hexagonalBaselineRelPath))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var doc hexagonalBaselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if doc.SchemaVersion != 2 {
		t.Fatalf("unsupported schema_version %d (expected 2)", doc.SchemaVersion)
	}
	var aligned, extract, exception int
	for _, row := range doc.Packages {
		switch row.Classification {
		case "aligned":
			aligned++
		case "extract":
			extract++
		case "exception":
			exception++
		default:
			t.Fatalf("unexpected classification %q for %s", row.Classification, row.GoListPattern)
		}
	}
	if aligned == 0 || extract == 0 || exception == 0 {
		t.Fatalf("baseline must include at least one aligned, one extract, and one exception package (got aligned=%d extract=%d exception=%d)",
			aligned, extract, exception)
	}
}

// TestHexagonalMigrationBaselineMatchesGoList locks the migration register from
// introduce-hexagonal-architecture: direct internal/core imports per listed package
// must match the committed baseline (intentional edits only).
func TestHexagonalMigrationBaselineMatchesGoList(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, hexagonalBaselineRelPath))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	var doc hexagonalBaselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if doc.SchemaVersion != 2 {
		t.Fatalf("unsupported schema_version %d (expected 2)", doc.SchemaVersion)
	}

	for _, row := range doc.Packages {
		row := row
		t.Run(strings.TrimPrefix(row.GoListPattern, "./"), func(t *testing.T) {
			t.Parallel()
			if row.GoListPattern == "" {
				t.Fatal("empty go_list_pattern")
			}
			validClass := map[string]struct{}{
				"aligned":   {},
				"extract":   {},
				"exception": {},
			}
			if _, ok := validClass[row.Classification]; !ok {
				t.Fatalf("invalid classification %q for %s", row.Classification, row.GoListPattern)
			}
			if row.Classification == "exception" {
				if strings.TrimSpace(row.RetirementTrigger) == "" {
					t.Fatalf("%s: classification exception requires non-empty retirement_trigger", row.GoListPattern)
				}
			}

			got := directInternalCoreImports(t, root, row.GoListPattern)
			want := slices.Clone(row.AllowedInternalCoreImports)
			slices.Sort(want)

			if !slices.Equal(got, want) {
				t.Fatalf("%s (%s): internal/core direct import mismatch\n  got:  %v\n  want: %v",
					row.GoListPattern, row.Classification, got, want)
			}
		})
	}
}

func directInternalCoreImports(t *testing.T, repoRootDir, listPattern string) []string {
	t.Helper()

	cmd := exec.Command("go", "list", "-json", "-test=false", listPattern)
	cmd.Dir = repoRootDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v", listPattern, err)
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	if !dec.More() {
		t.Fatalf("go list %s: empty output", listPattern)
	}
	var meta struct {
		ImportPath string   `json:"ImportPath"`
		Imports    []string `json:"Imports"`
	}
	if err := dec.Decode(&meta); err != nil {
		t.Fatalf("decode go list json for %s: %v", listPattern, err)
	}
	if dec.More() {
		t.Fatalf("go_list_pattern %q matched multiple packages; baseline register expects one main module package per row (use a non-recursive path or split rows)", listPattern)
	}

	var core []string
	for _, imp := range meta.Imports {
		if strings.HasPrefix(imp, internalCoreImportPrefix) {
			core = append(core, imp)
		}
	}
	slices.Sort(core)
	return core
}
