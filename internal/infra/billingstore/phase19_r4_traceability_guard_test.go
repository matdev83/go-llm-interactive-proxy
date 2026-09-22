package billingstore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Phase 19 review-blocker-4 mechanical traceability guard.
//
// It validates the Phase 19.1 lifecycle matrix
// (.kiro/specs/extensible-usage-economics-reconciliation/evidence/phase19-1-lifecycle-certification.tsv)
// against real test declarations and real assertion semantics — not
// self-referential strings:
//
//   - exactly 124 data rows remain (no silent row drop/add);
//   - every name in each evidence cell resolves to a real Go test function
//     declaration in the repository;
//   - the historically mismatched rows additionally prove assertion semantics:
//     4.5 sources partition a stream and compare canonical observations/
//     fingerprints (and the preflight-decline test is absent from that cell),
//     12.4 sources exercise the production tolerance policy with versioned,
//     zero-denominator and typed-decision assertions, 11.4 sources cover
//     pagination, and 11.5 sources cover rebuild/drift detection.
func TestPhase19R4TraceabilityMatrixAssertions(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: unavailable")
	}
	// This file lives at <root>/internal/infra/billingstore/.
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
	tsvPath := filepath.Join(root, ".kiro", "specs", "extensible-usage-economics-reconciliation", "evidence", "phase19-1-lifecycle-certification.tsv")
	raw, err := os.ReadFile(tsvPath)
	if err != nil {
		t.Fatalf("read matrix: %v", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	// Drop trailing empty line if present.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || lines[0] != "kind\tid\tsummary\tevidence\tintegrated_19_1" {
		t.Fatalf("matrix header = %q, want kind/id/summary/evidence/integrated_19_1", lines[0])
	}
	data := lines[1:]
	if len(data) != 124 {
		t.Fatalf("matrix data rows = %d, want exactly 124", len(data))
	}

	declarations := make(map[string]string)
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") == false {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "func Test") {
				continue
			}
			name := strings.TrimPrefix(trimmed, "func ")
			if index := strings.Index(name, "("); index >= 0 {
				name = name[:index]
			}
			if name == "" || strings.Contains(name, " ") {
				continue
			}
			if _, exists := declarations[name]; !exists {
				declarations[name] = path
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk declarations: %v", walkErr)
	}

	sourcesOf := func(names []string) string {
		var builder strings.Builder
		for _, name := range names {
			path, exists := declarations[name]
			if !exists {
				t.Fatalf("mapped test %q has no Go test declaration", name)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			builder.Write(content)
			builder.WriteByte('\n')
		}
		return builder.String()
	}

	cells := make(map[string][]string)
	for i, row := range data {
		columns := strings.Split(row, "\t")
		if len(columns) < 4 {
			t.Fatalf("row %d has %d columns, want >= 4: %q", i+2, len(columns), row)
		}
		key := columns[0] + "\x00" + columns[1]
		var names []string
		for _, field := range strings.Split(columns[3], ";") {
			name := strings.TrimSpace(field)
			if name == "" || name == "-" {
				continue
			}
			names = append(names, name)
		}
		cells[key] = names
		for _, name := range names {
			if _, exists := declarations[name]; !exists {
				t.Fatalf("row %d (%s/%s): mapped test %q has no Go test declaration", i+2, columns[0], columns[1], name)
			}
		}
	}

	// Row 49 / Requirement 4.5: sources must partition a stream and compare
	// canonical observations/fingerprints; the preflight-decline test alone is
	// explicitly rejected as 4.5 evidence.
	chunk := cells["req\x004.5"]
	for _, name := range chunk {
		if name == "TestWireComposition_PreflightInexactTokenizerSemantics_DynamicAssessmentDeclinesUnderSamePermit" {
			t.Fatalf("req 4.5 evidence must not cite the preflight-decline test (it never partitions a stream)")
		}
	}
	chunkSources := sourcesOf(chunk)
	for _, marker := range []string{"Fingerprint", "CanonicalJSON", "Delta"} {
		if !strings.Contains(chunkSources, marker) {
			t.Fatalf("req 4.5 sources lack assertion marker %q (partition + canonical/fingerprint compare required)", marker)
		}
	}

	// Row 96 / Requirement 12.4: sources must exercise the production tolerance
	// policy with versioned, zero-denominator and typed-decision assertions.
	tolerance := cells["req\x0012.4"]
	toleranceSources := sourcesOf(tolerance)
	for _, marker := range []string{
		"EvaluateReconciliationTolerance",
		"ZeroDenominator",
		"PolicyVersion",
		"WithinTolerance",
		"Incomparable",
		"AbsoluteDelta",
	} {
		if !strings.Contains(toleranceSources, marker) {
			t.Fatalf("req 12.4 sources lack assertion marker %q (versioned tolerance proof required)", marker)
		}
	}

	// Rows 90-91 / Requirements 11.4-11.5: pagination and rebuild/drift proof.
	paginationSources := sourcesOf(cells["req\x0011.4"])
	if !strings.Contains(paginationSources, "Pagina") {
		t.Fatalf("req 11.4 sources lack a pagination assertion")
	}
	rebuildSources := sourcesOf(cells["req\x0011.5"])
	if !strings.Contains(rebuildSources, "Rebuild") {
		t.Fatalf("req 11.5 sources lack a rebuild assertion")
	}
	if !(strings.Contains(rebuildSources, "drift") || strings.Contains(rebuildSources, "Drift") || strings.Contains(rebuildSources, "IdentityCollision")) {
		t.Fatalf("req 11.5 sources lack a drift-detection assertion")
	}
}
