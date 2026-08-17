package archtest

import (
	"os"
	"strings"
	"testing"
)

func TestBillingFinalConvergenceDeletionTargetsHaveNonemptyEvidence(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	targetsToCheck := map[string]bool{
		"schema-usage-append-outbox":     true,
		"schema-turn-usage-records":      true,
		"schema-leg-usage-records":       true,
		"schema-usage-record-processing": true,
		"UsageAppendOutbox":              true,
		"UsageAppendWorker":              true,
		"RetryingCallUsageAppender":      true,
		"RetryingCallLegUsageAppender":   true,
	}

	for _, dt := range doc.DeletionTargets {
		if !targetsToCheck[dt.ID] {
			continue
		}

		// Every schema/outbox target must have nonempty rationale
		if dt.RetentionRationale == "" {
			t.Errorf("deletion target %q has empty retention_rationale", dt.ID)
		}

		// Check that we have evidence: either consumer, writer, or historical reader is nonempty
		if len(dt.CurrentConsumers) == 0 && len(dt.CurrentWriters) == 0 && len(dt.HistoricalReaders) == 0 {
			t.Errorf("deletion target %q has no consumer/writer/reader evidence", dt.ID)
		}
	}
}

func TestBillingFinalConvergenceInventoryIncludesMoneyUsageAuthority(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	gitFS, err := loadGitCommitFS(root, doc.BaselineSHA)
	if err != nil {
		t.Fatalf("load pinned baseline: %v", err)
	}
	decls, err := ComputeBillingFinalConvergenceSymbolInventoryFS(gitFS, doc)
	if err != nil {
		t.Fatalf("compute symbol inventory: %v", err)
	}
	found := false
	for _, decl := range decls {
		if decl.File != "internal/core/usageauthority/domain/amount.go" {
			continue
		}
		for _, name := range decl.DeclaredNames {
			if name == "AmountUnitMoneyNano" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("symbol inventory must include the money-capable UsageAuthority declaration outside the whole-file billing roots")
	}
}

func TestBillingFinalConvergenceCurrentTreeModificationRegression(t *testing.T) {
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	// Measure the pinned baseline once. This deliberately uses the in-memory
	// commit FS rather than the mutable working tree.
	pinnedFS, err := loadGitCommitFS(root, doc.BaselineSHA)
	if err != nil {
		t.Fatalf("load pinned baseline: %v", err)
	}
	m1, err := MeasureBillingFinalConvergenceDenominatorFS(pinnedFS, doc)
	if err != nil {
		t.Fatalf("first measure: %v", err)
	}

	// Simulate a working-tree change in a separate in-memory FS. The pinned
	// denominator must remain reproducible and must not observe this change.
	mutableFS := &gitCommitFS{sha: pinnedFS.sha, files: make(map[string][]byte, len(pinnedFS.files)+1)}
	for path, content := range pinnedFS.files {
		mutableFS.files[path] = append([]byte(nil), content...)
	}
	const dummyPath = "internal/core/billing/dummy_regression_test_file.go"
	mutableFS.files[dummyPath] = []byte("package billing\ntype Spend struct{}\n")
	if _, err := mutableFS.ReadFile(dummyPath); err != nil {
		t.Fatalf("add isolated mutable fixture: %v", err)
	}
	mutableMeasurement, err := MeasureBillingFinalConvergenceDenominatorFS(mutableFS, doc)
	if err != nil {
		t.Fatalf("measure isolated mutable fixture: %v", err)
	}
	if mutableMeasurement.DenominatorLOC == m1.DenominatorLOC {
		t.Fatalf("isolated mutable fixture did not change denominator LOC; regression setup is ineffective")
	}

	m2, err := MeasureBillingFinalConvergenceDenominatorFS(pinnedFS, doc)
	if err != nil {
		t.Fatalf("second measure: %v", err)
	}
	if m1.DenominatorLOC != m2.DenominatorLOC {
		t.Errorf("denominator LOC changed from %d to %d (expected isolated mutable-tree modification to be ignored)", m1.DenominatorLOC, m2.DenominatorLOC)
	}
}

// billingFinalConvergenceSplitEvidence splits a "path:symbol" evidence entry
// into its file path and symbol components. Entries without a colon return the
// whole string as the path with an empty symbol.
func billingFinalConvergenceSplitEvidence(entry string) (file, symbol string) {
	// All evidence paths are Unix-style relative paths; the only colon is the
	// separator between path and symbol.
	idx := strings.LastIndex(entry, ":")
	if idx == -1 {
		return entry, ""
	}
	return entry[:idx], entry[idx+1:]
}

// billingFinalConvergenceVerifyEvidence checks that every evidence entry uses
// a real pinned-tree path and a verifiable symbol. Nonempty strings alone are
// not evidence: every entry must be "path:symbol" where the path exists in the
// pinned tree and the symbol appears in the file content.
func billingFinalConvergenceVerifyEvidence(t *testing.T, gitFS *gitCommitFS, targetID, field string, entries []string) {
	t.Helper()
	for _, entry := range entries {
		file, symbol := billingFinalConvergenceSplitEvidence(entry)

		// Every evidence entry must use path:symbol format; a bare file path
		// is not machine-checkable evidence.
		if symbol == "" {
			t.Errorf("deletion target %q %s[%q]: bare path without symbol is not verifiable evidence",
				targetID, field, entry)
			continue
		}

		src, err := gitFS.ReadFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				t.Errorf("deletion target %q %s[%q]: file %q does not exist in pinned tree",
					targetID, field, entry, file)
			} else {
				t.Errorf("deletion target %q %s[%q]: read %q: %v",
					targetID, field, entry, file, err)
			}
			continue
		}

		if !strings.Contains(string(src), symbol) {
			t.Errorf("deletion target %q %s[%q]: symbol %q not found in %q",
				targetID, field, entry, symbol, file)
		}
	}
}

// TestBillingFinalConvergenceEvidencePathsValid verifies that every evidence
// entry in deletion_targets uses a real pinned-tree path and a verifiable
// path:symbol representation. Nonempty strings alone are not evidence.
func TestBillingFinalConvergenceEvidencePathsValid(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	gitFS, err := loadGitCommitFS(root, doc.BaselineSHA)
	if err != nil {
		t.Fatalf("load pinned baseline: %v", err)
	}

	for _, dt := range doc.DeletionTargets {
		// Verify files paths exist in the pinned tree.
		for _, f := range dt.Files {
			if _, err := gitFS.ReadFile(f); err != nil {
				if os.IsNotExist(err) {
					t.Errorf("deletion target %q files[%q]: does not exist in pinned tree", dt.ID, f)
				} else {
					t.Errorf("deletion target %q files[%q]: %v", dt.ID, f, err)
				}
			}
		}

		billingFinalConvergenceVerifyEvidence(t, gitFS, dt.ID, "historical_readers", dt.HistoricalReaders)
		billingFinalConvergenceVerifyEvidence(t, gitFS, dt.ID, "current_consumers", dt.CurrentConsumers)
		billingFinalConvergenceVerifyEvidence(t, gitFS, dt.ID, "current_writers", dt.CurrentWriters)
	}
}

// TestBillingFinalConvergenceRequiredTargetsHaveWritersAndConsumers verifies
// that required schema/outbox/type deletion targets have non-empty writers and
// consumers with verifiable evidence.
func TestBillingFinalConvergenceRequiredTargetsHaveWritersAndConsumers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	// Targets that must have both writers and consumers.
	requiredWritersConsumers := map[string]bool{
		"TurnUsageRecord":                true,
		"LegUsageRecord":                 true,
		"RatingInput":                    true,
		"calculateCustomerCharge":        true,
		"schema-turn-usage-records":      true,
		"schema-leg-usage-records":       true,
		"schema-usage-record-processing": true,
		"ReservedNano":                   true,
		"JournalBookLegacyAuthorization": true,
		"schema-reserved-nano":           true,
		"AmountUnitMoneyNano":            true,
		"RuleKindBudget":                 true,
		"RuleKindSpendCap":               true,
		"Spend":                          true,
		"FinalCost":                      true,
		"EstimatedCost":                  true,
		"RetryingCallUsageAppender":      true,
		"RetryingCallLegUsageAppender":   true,
		"UsageAppendWorker":              true,
		"UsageAppendOutbox":              true,
		"schema-usage-append-outbox":     true,
		"BillingAuthoritative":           true,
	}

	for _, dt := range doc.DeletionTargets {
		if !requiredWritersConsumers[dt.ID] {
			continue
		}
		if len(dt.CurrentWriters) == 0 {
			t.Errorf("deletion target %q: must have non-empty current_writers", dt.ID)
		}
		if len(dt.CurrentConsumers) == 0 {
			t.Errorf("deletion target %q: must have non-empty current_consumers", dt.ID)
		}
	}
}

// TestBillingFinalConvergenceEmptyHistoricalReadersRationale ensures that
// when a deletion target has no historical_readers, its reason explicitly
// states that no isolated historical reader exists rather than implying
// readers exist.
func TestBillingFinalConvergenceEmptyHistoricalReadersRationale(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	for _, dt := range doc.DeletionTargets {
		if len(dt.HistoricalReaders) > 0 {
			continue
		}
		// If no historical readers exist, the reason must explicitly say so
		// (e.g., "no isolated historical reader exists") rather than implying
		// readers exist.
		reasonLower := strings.ToLower(dt.Reason)
		if strings.Contains(reasonLower, "historical reader") &&
			!strings.Contains(reasonLower, "no ") &&
			!strings.Contains(reasonLower, "not ") {
			t.Errorf("deletion target %q: reason claims historical readers but historical_readers is empty", dt.ID)
		}
	}
}

// TestBillingFinalConvergenceHistoricalReadersRejectTestsAndLiveStores verifies
// that test-only files and live current-record/outbox store operations cannot be
// classified as isolated historical readers. A production compatibility reader
// remains a valid shape when one exists in the pinned tree.
func TestBillingFinalConvergenceHistoricalReadersRejectTestsAndLiveStores(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	liveStoreReaders := map[string]bool{
		"internal/infra/billingstore/call_usage_store.go:ListCallUsage":                       true,
		"internal/infra/billingstore/call_usage_store.go:ListCallLegUsage":                    true,
		"internal/infra/billingstore/usage_append_outbox_store.go:ListPendingUsageAppendWork": true,
	}
	for _, tc := range []struct {
		entry    string
		rejected bool
	}{
		{entry: "internal/infra/billingstore/call_usage_store_test.go:HistoricalReader", rejected: true},
		{entry: "internal/infra/billingstore/call_usage_store_test_helpers.go:HistoricalReader", rejected: true},
		{entry: "internal/infra/billingstore/call_usage_store.go:ListCallUsage", rejected: true},
		{entry: "internal/infra/billingstore/call_usage_store.go:DecodeHistoricalReader", rejected: false},
	} {
		file, _ := billingFinalConvergenceSplitEvidence(tc.entry)
		rejected := strings.HasSuffix(file, "_test.go") || strings.HasSuffix(file, "_test_helpers.go") || liveStoreReaders[tc.entry]
		if rejected != tc.rejected {
			t.Errorf("historical reader classification for %q = %v, want rejected=%v", tc.entry, rejected, tc.rejected)
		}
	}
	for _, dt := range doc.DeletionTargets {
		for _, entry := range dt.HistoricalReaders {
			file, _ := billingFinalConvergenceSplitEvidence(entry)
			if strings.HasSuffix(file, "_test.go") || strings.HasSuffix(file, "_test_helpers.go") {
				t.Errorf("deletion target %q historical_readers[%q]: test-only source is not an isolated production reader", dt.ID, entry)
			}
			if liveStoreReaders[entry] {
				t.Errorf("deletion target %q historical_readers[%q]: live current-record/outbox store operation is not a historical reader", dt.ID, entry)
			}
		}
	}
}

func TestBillingFinalConvergenceHistoricalReadersAreProductionSource(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	for _, dt := range doc.DeletionTargets {
		// Historical reader evidence must be production source; _test.go
		// regression tests are not isolated historical production readers.
		for _, entry := range dt.HistoricalReaders {
			file, _ := billingFinalConvergenceSplitEvidence(entry)
			if strings.HasSuffix(file, "_test.go") {
				t.Errorf("deletion target %q historical_readers[%q]: test file %q is not production historical reader evidence",
					dt.ID, entry, file)
			}
		}

		// An empty historical reader list must carry a truthful no-reader
		// rationale: if the reason mentions historical readers it must
		// explicitly deny their existence rather than imply readers exist.
		if len(dt.HistoricalReaders) == 0 {
			reasonLower := strings.ToLower(dt.Reason)
			if strings.Contains(reasonLower, "historical reader") &&
				!strings.Contains(reasonLower, "no ") &&
				!strings.Contains(reasonLower, "not ") {
				t.Errorf("deletion target %q: reason implies historical readers exist but historical_readers is empty", dt.ID)
			}
		}
	}
}
