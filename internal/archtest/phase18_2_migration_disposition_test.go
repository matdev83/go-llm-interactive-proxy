package archtest

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestPhase182MigrationDispositionClosesCensus is the Task 18.2 RED contract
// (req 5.4, 15.2, 15.6, 17.1, 17.6, 18.1; C1-C7; Migration Strategy step 8).
//
// Every Task 1 census producer/consumer must have one explicit final
// disposition against the final tree: native V2, explicit safe one-way
// historical/nonfinancial projection, deliberately unsupported strict offer
// with documented error, or removed superseded path.
//
// Beyond row counts and label allowlists, this guard joins every disposition
// row to the exact original census identity (census_line + path +
// symbol_anchor plus the census relation fields), resolves every
// replacement/owner file reference to a current file/symbol, verifies removed
// anchors are absent with replacements present, unsupported-strict error
// paths are real, native-V2 consumer evidence is real, and financially
// significant rows are paired to named behavior tests. Arbitrary labels and
// self-referential-only evidence strings are rejected.
func TestPhase182MigrationDispositionClosesCensus(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	census := loadPhase182Census(t, root)
	disposition := loadPhase182Disposition(t, root)

	if len(census) != 69 {
		t.Fatalf("census data rows = %d, want 69", len(census))
	}
	if len(disposition) != 69 {
		t.Fatalf("18.2 disposition rows = %d, want 69 covering Task 1 census", len(disposition))
	}

	allowed := map[string]struct{}{
		"native-v2": {}, "safe-projection": {}, "unsupported-strict": {}, "removed": {},
	}
	// Exact join: every disposition row must match its census row on
	// census_line identity, path, and symbol anchor, with census relation
	// fields confirming uniqueness.
	for _, row := range disposition {
		cen, ok := census[row.line]
		if !ok {
			t.Fatalf("disposition census_line %d has no census row", row.line)
		}
		if row.path != cen.path {
			t.Fatalf("disposition line %d path %q does not join census path %q",
				row.line, row.path, cen.path)
		}
		if row.symbol != cen.anchor {
			t.Fatalf("disposition line %d symbol %q does not join census anchor %q",
				row.line, row.symbol, cen.anchor)
		}
		if strings.TrimSpace(cen.boundary) == "" {
			t.Fatalf("census line %d relation field is blank", row.line)
		}
		for _, field := range []string{cen.category, cen.family, cen.owner} {
			if strings.TrimSpace(field) == "" {
				t.Fatalf("census line %d has blank identity field", row.line)
			}
		}
		if _, ok := allowed[row.final]; !ok {
			t.Fatalf("disposition line %d final %q is not one of native-v2/safe-projection/unsupported-strict/removed",
				row.line, row.final)
		}
		if strings.TrimSpace(row.evidence) == "" {
			t.Fatalf("disposition line %d evidence is blank", row.line)
		}
		// Reject arbitrary labels: evidence must name a resolvable repo file,
		// not a bare label or a self-description without a path.
		refs := phase182GoRefs(row.evidence)
		if len(refs) == 0 {
			t.Fatalf("disposition line %d evidence %q names no resolvable .go file",
				row.line, row.evidence)
		}
		for _, ref := range refs {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref))); err != nil {
				t.Fatalf("disposition line %d evidence references missing file %q",
					row.line, ref)
			}
		}
		// Reject self-referential-only evidence where the review requires a
		// separate V2 owner: corrected retained-V1-projection rows must name
		// their distinct V2 owner/replacement file, not just restate the own
		// anchor. (Other safe-projection rows legitimately own same-file
		// hooks and are covered by file resolution plus anchor presence.)
		if phase182RequiresDistinctOwner(row.line) {
			distinct := false
			for _, ref := range refs {
				if ref != row.path {
					distinct = true
					break
				}
			}
			if !distinct {
				t.Fatalf("disposition line %d safe-projection evidence names no distinct V2 owner/replacement file (self-referential)",
					row.line)
			}
		}
		// Own anchor must be live for retained rows.
		if row.final != "removed" {
			ownSrc, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(row.path)))
			if err != nil {
				t.Fatalf("disposition line %d own file %q unreadable: %v", row.line, row.path, err)
			}
			if !strings.Contains(string(ownSrc), row.symbol) {
				t.Fatalf("disposition line %d anchor %q absent from own file %q",
					row.line, row.symbol, row.path)
			}
		}
	}

	// Removed rows: anchor absent, explicit retired marker, replacement
	// symbol resolved to a current file.
	assertPhase182RemovedRow(t, root, disposition, 39,
		"internal/core/runtime/billing_leg.go", "mergeStreamCostOntoLeg",
		"projectV1BillingEvidence")

	// Unsupported-strict rows: error path must resolve to a real error
	// symbol, not a bare label.
	assertPhase182UnsupportedRow(t, root, disposition, 44,
		"internal/plugins/features/compactioncontinuity/plugin_response.go",
		"AfterResponseRelease",
		[]string{"ErrEstimateUnbounded", "ErrEstimateInvalid"})

	// Financially significant corrected rows must be paired to named
	// behavior tests: every cited _test.go file must exist.
	for _, line := range []int{7, 8, 33, 34, 37, 52, 68} {
		row := dispositionByLine(disposition, line)
		testRefs := phase182TestRefs(row.evidence)
		if len(testRefs) == 0 {
			t.Fatalf("disposition line %d financially significant row cites no behavior test file", line)
		}
		for _, ref := range testRefs {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref))); err != nil {
				t.Fatalf("disposition line %d cites missing behavior test %q", line, ref)
			}
		}
	}

	// Targeted semantic checks for the corrected rows: minimal
	// source/behavior verification reusing existing test contracts.
	assertPhase182Row7AggregateProjection(t, root)
	assertPhase182Row8ReconcileProjection(t, root)
	assertPhase182Row33FinalizeProjection(t, root)
	assertPhase182Row34SidebandProjection(t, root)
	assertPhase182Row37CollectorProjection(t, root)
	assertPhase182Row52OwnerAwareRating(t, root)
	assertPhase182Row68ReportProjection(t, root)

	// Stale Task 1 line 40 anchor must stay retired (row 39 replacement).
	assertAbsentIdent(t, root, "internal/core/runtime/billing_leg.go", "mergeStreamCostOntoLeg",
		"Task 1 line 40 mergeStreamCostOntoLeg must stay retired; use projectV1BillingEvidence")
	assertPresentIdent(t, root, "internal/core/runtime/billing_leg.go", "projectV1BillingEvidence",
		"explicit one-way V1 projection must remain for historical readers")

	// Proven orphan conversion/alias hooks must be removed.
	assertAbsentIdent(t, root, "internal/core/billing/rating.go", "SelectRetailLegs",
		"orphan alias SelectRetailLegs has zero callers; remove stale compatibility code")
	assertAbsentString(t, root, "internal/core/billing/cost_selection.go", "func AttributeOperatorCost(",
		"orphan alias AttributeOperatorCost has zero callers; remove stale compatibility code")
	assertAbsentString(t, root, "internal/core/billing/cost_selection.go", "func AttributeOperatorCostWithAllocations(",
		"orphan alias AttributeOperatorCostWithAllocations has zero callers; remove stale compatibility code")
	assertAbsentString(t, root, "internal/core/billing/rating.go", "func (s OperatorRateSet) Resolve(",
		"orphan conversion OperatorRateSet.Resolve has zero callers after 18.1 retirement; remove stale hook")

	// Historical operator-rate body lookup must not serve live money.
	assertOperatorRateBodyHasNoLiveMoneyCaller(t, root)
	assertContainsString(t, root, "internal/infra/billingcompose/catalog.go", "historical",
		"catalog operator-rate godoc must document historical-only retention and operator migration")
	assertContainsString(t, root, "internal/core/billing/rating.go", "historical",
		"rating operator-rate godoc must document historical-only retention and V2 estimate ownership")
}

type phase182CensusRow struct {
	line                                                         int
	category, path, anchor, boundary, family, owner, disposition string
}

type phase182DispositionRow struct {
	line                          int
	path, symbol, final, evidence string
}

func loadPhase182Census(t *testing.T, root string) map[int]phase182CensusRow {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(".kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/phase1-producer-consumer-census.tsv"))
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open census: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close census: %v", err)
		}
	}()
	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read census: %v", err)
	}
	out := map[int]phase182CensusRow{}
	for i, row := range rows[1:] {
		if len(row) != 7 {
			t.Fatalf("census line %d has %d columns, want 7", i+2, len(row))
		}
		for j, v := range row {
			if strings.TrimSpace(v) == "" {
				t.Fatalf("census line %d column %d is blank", i+2, j+1)
			}
		}
		out[i+1] = phase182CensusRow{
			line: i + 1, category: strings.TrimSpace(row[0]),
			path: strings.TrimSpace(row[1]), anchor: strings.TrimSpace(row[2]),
			boundary: strings.TrimSpace(row[3]), family: strings.TrimSpace(row[4]),
			owner: strings.TrimSpace(row[5]), disposition: strings.TrimSpace(row[6]),
		}
	}
	return out
}

func loadPhase182Disposition(t *testing.T, root string) []phase182DispositionRow {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(".kiro/specs/extensible-usage-economics-reconciliation/evidence/phase18-2-producer-consumer-disposition.tsv"))
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("18.2 disposition artifact missing: %s: %v (create final V2/projection/unsupported/removed mapping for all 69 census rows)", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close disposition: %v", err)
		}
	}()
	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []phase182DispositionRow
	seen := map[int]struct{}{}
	for i, row := range rows[1:] {
		if len(row) != 5 {
			t.Fatalf("disposition line %d has %d columns, want 5 (census_line, path, symbol, final, evidence)", i+2, len(row))
		}
		for j, v := range row {
			if strings.TrimSpace(v) == "" {
				t.Fatalf("disposition line %d column %d is blank", i+2, j+1)
			}
		}
		line, err := strconv.Atoi(strings.TrimSpace(row[0]))
		if err != nil || line < 1 || line > 69 {
			t.Fatalf("disposition line %d census_line %q is not in 1..69", i+2, row[0])
		}
		if _, dup := seen[line]; dup {
			t.Fatalf("disposition duplicates census line %d", line)
		}
		seen[line] = struct{}{}
		out = append(out, phase182DispositionRow{
			line: line, path: strings.TrimSpace(row[1]),
			symbol: strings.TrimSpace(row[2]), final: strings.TrimSpace(row[3]),
			evidence: strings.TrimSpace(row[4]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

func dispositionByLine(rows []phase182DispositionRow, line int) phase182DispositionRow {
	for _, row := range rows {
		if row.line == line {
			return row
		}
	}
	return phase182DispositionRow{}
}

// phase182GoRefs extracts repo-relative .go file references from an evidence
// string. Only tokens containing "/" and ending at ".go" qualify, so bare
// labels cannot pass as references.
func phase182GoRefs(evidence string) []string {
	var out []string
	seen := map[string]struct{}{}
	for field := range strings.FieldsSeq(evidence) {
		trimmed := strings.Trim(field, " \t\n\r\"'(),;:.`")
		idx := strings.Index(trimmed, ".go")
		if idx < 0 {
			continue
		}
		candidate := trimmed[:idx+3]
		if !strings.Contains(candidate, "/") {
			continue
		}
		// Drop leading punctuation the field split retained.
		candidate = strings.TrimLeft(candidate, "\"'(`")
		if _, dup := seen[candidate]; dup {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func phase182TestRefs(evidence string) []string {
	var out []string
	for _, ref := range phase182GoRefs(evidence) {
		if strings.HasSuffix(ref, "_test.go") {
			out = append(out, ref)
		}
	}
	return out
}

// phase182RequiresDistinctOwner lists corrected retained-V1-projection rows
// whose V2 owner/replacement provably lives in a separate file. Their
// evidence must cite that distinct file; self-referential-only strings are
// rejected for these lines.
func phase182RequiresDistinctOwner(line int) bool {
	switch line {
	case 7, 8, 33, 34, 37, 68:
		return true
	}
	return false
}

func assertPhase182RemovedRow(t *testing.T, root string, rows []phase182DispositionRow, line int, path, absentAnchor, replacement string) {
	t.Helper()
	row := dispositionByLine(rows, line)
	if row.path != path || row.symbol != absentAnchor {
		t.Fatalf("disposition line %d must join removed anchor %s %s", line, path, absentAnchor)
	}
	if row.final != "removed" {
		t.Fatalf("disposition line %d final = %q, want removed", line, row.final)
	}
	if !strings.Contains(strings.ToLower(row.evidence), "retir") &&
		!strings.Contains(strings.ToLower(row.evidence), "remov") &&
		!strings.Contains(strings.ToLower(row.evidence), "history") {
		t.Fatalf("disposition line %d removed evidence must carry a retired/removed/history marker", line)
	}
	if !strings.Contains(row.evidence, replacement) {
		t.Fatalf("disposition line %d removed evidence must name replacement %q", line, replacement)
	}
	assertAbsentIdent(t, root, path, absentAnchor,
		"removed anchor must stay absent")
	assertPresentIdent(t, root, path, replacement,
		"explicit replacement must remain for historical readers")
}

func assertPhase182UnsupportedRow(t *testing.T, root string, rows []phase182DispositionRow, line int, path, symbol string, errors []string) {
	t.Helper()
	row := dispositionByLine(rows, line)
	if row.path != path || row.symbol != symbol {
		t.Fatalf("disposition line %d must join unsupported anchor %s %s", line, path, symbol)
	}
	if row.final != "unsupported-strict" {
		t.Fatalf("disposition line %d final = %q, want unsupported-strict", line, row.final)
	}
	assertPresentIdent(t, root, path, symbol, "unsupported anchor must remain as the strict offer point")
	resolved := false
	cited := map[string]struct{}{path: {}}
	for _, ref := range phase182GoRefs(row.evidence) {
		cited[ref] = struct{}{}
	}
	// The named error must resolve to a real error symbol in the row's own
	// file, any cited file, or the owning billing error contracts.
	searchRoots := []string{}
	for ref := range cited {
		searchRoots = append(searchRoots, ref)
	}
	searchRoots = append(searchRoots,
		"internal/core/billing/estimate.go",
		"internal/core/billing/errors.go",
	)
	for _, want := range errors {
		if !strings.Contains(row.evidence, want) {
			continue
		}
		for _, ref := range searchRoots {
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ref)))
			if err != nil {
				continue
			}
			if strings.Contains(string(src), want) {
				resolved = true
				break
			}
		}
	}
	if !resolved {
		t.Fatalf("disposition line %d unsupported-strict error path does not resolve to a real error symbol in %q", line, strings.Join(errors, "/"))
	}
}

func assertAbsentIdent(t *testing.T, root, rel, ident, msg string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	// Parse-level check: identifier must not appear as a FuncDecl name nor as
	// a call site. A substring scan is sufficient for these distinctive names.
	if strings.Contains(string(src), ident) {
		t.Fatalf("%s: %s", rel, msg)
	}
}

func assertPresentIdent(t *testing.T, root, rel, ident, msg string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), ident) {
		t.Fatalf("%s lacks %q: %s", rel, ident, msg)
	}
}

func assertAbsentString(t *testing.T, root, rel, substr, msg string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), substr) {
		t.Fatalf("%s: %s", rel, msg)
	}
}

func assertContainsString(t *testing.T, root, rel, substr, msg string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(src)), strings.ToLower(substr)) {
		t.Fatalf("%s: %s", rel, msg)
	}
}

func assertOperatorRateBodyHasNoLiveMoneyCaller(t *testing.T, root string) {
	t.Helper()
	// The retired scalar fallback must not be reachable through the catalog
	// body lookup from any live production file. Allowed: catalog.go
	// definition, *_test.go callers, and archtest guards.
	var offenders []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "internal/infra/billingcompose/catalog.go" {
			return nil
		}
		if strings.HasPrefix(rel, "internal/archtest/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), ".OperatorRate(") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("live production caller of catalog.OperatorRate body remains (retired scalar path):\n%s", strings.Join(offenders, "\n"))
	}
}
