package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Phase-0 billing-final-convergence baseline tests.
//
// These tests validate the canonical artifact (design Section 12) against the
// pinned baseline commit: the baseline SHA matches the verified predecessor implementation
// recorded in spec.json, the denominator recomputes deterministically, scanner
// version/rules are locked, planned ratchets remain non-activated, and every
// planned deletion target is actually present in production source.

// specDependency is the minimal subset of spec.json needed for SHA verification.
type specDependency struct {
	FeatureName           string `json:"feature_name"`
	SpecPR                int    `json:"spec_pr"`
	SpecMergeSHA          string `json:"spec_merge_sha"`
	RequiredState         string `json:"required_state"`
	ImplementationMainSHA string `json:"implementation_main_sha"`
	VerificationStatus    string `json:"verification_status"`
}

type specFile struct {
	FeatureName            string           `json:"feature_name"`
	ReadyForImplementation bool             `json:"ready_for_implementation"`
	Dependencies           []specDependency `json:"dependencies"`
}

func loadSpecJSON(t *testing.T) specFile {
	t.Helper()
	root := repoRoot(t)
	rel := filepath.FromSlash(".kiro/specs/billing-architecture-final-convergence/spec.json")
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read spec.json: %v", err)
	}
	var sf specFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		t.Fatalf("decode spec.json: %v", err)
	}
	return sf
}

func TestBillingFinalConvergenceBaselineFileExists(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(BillingFinalConvergenceBaselineRelPath))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("billing final-convergence baseline missing: %v", err)
	}
}

func TestBillingFinalConvergenceBaselineSchemaLocked(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if doc.SchemaVersion != BillingFinalConvergenceSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", doc.SchemaVersion, BillingFinalConvergenceSchemaVersion)
	}
	if doc.CountingMethod != BillingFinalConvergenceCountingMethod {
		t.Fatalf("counting_method = %q, want %q", doc.CountingMethod, BillingFinalConvergenceCountingMethod)
	}
	if doc.SymbolFollowingVersion != BillingFinalConvergenceSymbolFollowingVersion {
		t.Fatalf("symbol_following_version = %d, want %d", doc.SymbolFollowingVersion, BillingFinalConvergenceSymbolFollowingVersion)
	}
}

func TestBillingFinalConvergenceBaselineSHAMatchesSpec(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	sf := loadSpecJSON(t)
	if len(sf.Dependencies) != 1 {
		t.Fatalf("spec.json must have exactly 1 dependency, got %d", len(sf.Dependencies))
	}
	dep := sf.Dependencies[0]
	if doc.BaselineSHA != dep.ImplementationMainSHA {
		t.Fatalf("baseline_sha = %q, want spec.json implementation_main_sha %q",
			doc.BaselineSHA, dep.ImplementationMainSHA)
	}
	if doc.BaselineSHA != BillingFinalConvergenceVerifiedSHA {
		t.Fatalf("baseline_sha = %q, want code constant %q",
			doc.BaselineSHA, BillingFinalConvergenceVerifiedSHA)
	}
	if dep.VerificationStatus != "verified_phase_0" {
		t.Fatalf("verification_status = %q, want verified_phase_0", dep.VerificationStatus)
	}
	if !sf.ReadyForImplementation {
		t.Fatal("spec.json ready_for_implementation must be true after Phase 0 verification")
	}
}

func TestBillingFinalConvergenceBaselineSHANotSpecMergeSHA(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	const specMergeSHA = "9bf9c66a09de50ab3dcad18f0a8a84c2c2d49ed9"
	if doc.BaselineSHA == specMergeSHA {
		t.Fatal("baseline_sha must be the implementation SHA, not the spec-only merge SHA 9bf9c66...")
	}
}

func TestBillingFinalConvergenceBaselineSeedSymbolsMatch(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if len(doc.SeedSymbols) != len(BillingFinalConvergenceInitialSeedSymbols) {
		t.Fatalf("seed_symbols count = %d, want %d", len(doc.SeedSymbols), len(BillingFinalConvergenceInitialSeedSymbols))
	}
	for i, want := range BillingFinalConvergenceInitialSeedSymbols {
		if doc.SeedSymbols[i] != want {
			t.Fatalf("seed_symbols[%d] = %q, want %q", i, doc.SeedSymbols[i], want)
		}
	}
}

func TestBillingFinalConvergenceLOCRecomputesFromArtifact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	m, err := MeasureBillingFinalConvergenceDenominator(root, doc)
	if err != nil {
		t.Fatalf("measure denominator: %v", err)
	}
	if m.DenominatorLOC != doc.DenominatorLOC {
		t.Fatalf("recomputed denominator %d != locked baseline %d (scanner-rule change requires version bump)",
			m.DenominatorLOC, doc.DenominatorLOC)
	}
	if m.DenominatorLOC != m.RootLines+m.FileLines+m.DeclarationLines {
		t.Fatalf("denominator %d != root %d + file %d + decl %d = %d",
			m.DenominatorLOC, m.RootLines, m.FileLines, m.DeclarationLines,
			m.RootLines+m.FileLines+m.DeclarationLines)
	}
}

func TestBillingFinalConvergenceLOCRatchetActive(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	var m BillingFinalConvergenceDenominatorMeasurement
	if billingFinalConvergenceLOCRatchetActive(doc) {
		m, err = MeasureBillingFinalConvergenceCurrentDenominator(root, doc)
	} else {
		m, err = MeasureBillingFinalConvergenceDenominator(root, doc)
	}
	if err != nil {
		t.Fatalf("measure denominator: %v", err)
	}
	findings := EvaluateBillingFinalConvergenceLOCRatchet(doc, m)
	if len(findings) > 0 {
		var b strings.Builder
		for _, f := range findings {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("LOC ratchet must be non-activated (planned) and denominator must match:\n%s", b.String())
	}
}

func TestBillingFinalConvergenceBaselineRootLinesMatch(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	m, err := MeasureBillingFinalConvergenceDenominator(root, doc)
	if err != nil {
		t.Fatalf("measure denominator: %v", err)
	}
	if len(m.Roots) != len(doc.IncludedRoots) {
		t.Fatalf("measured roots = %d, want %d", len(m.Roots), len(doc.IncludedRoots))
	}
	for i, want := range doc.IncludedRoots {
		got := m.Roots[i]
		if got.ID != want.ID || got.Path != want.Path {
			t.Fatalf("root[%d] identity drifted: got %+v want %+v", i, got, want)
		}
		if got.BaselineLines != want.BaselineLines {
			t.Fatalf("root %s baseline_lines = %d, want %d", want.ID, got.BaselineLines, want.BaselineLines)
		}
		if got.CurrentLines != want.BaselineLines {
			t.Fatalf("root %s measured %d != locked %d", want.ID, got.CurrentLines, want.BaselineLines)
		}
	}
}

func TestBillingFinalConvergenceBaselineFileLinesMatch(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	m, err := MeasureBillingFinalConvergenceDenominator(root, doc)
	if err != nil {
		t.Fatalf("measure denominator: %v", err)
	}
	if len(m.Files) != len(doc.IncludedFiles) {
		t.Fatalf("measured files = %d, want %d", len(m.Files), len(doc.IncludedFiles))
	}
	for i, want := range doc.IncludedFiles {
		got := m.Files[i]
		if got.ID != want.ID || got.Path != want.Path {
			t.Fatalf("file[%d] identity drifted: got %+v want %+v", i, got, want)
		}
		if got.BaselineLines != want.BaselineLines {
			t.Fatalf("file %s baseline_lines = %d, want %d", want.ID, got.BaselineLines, want.BaselineLines)
		}
		if got.CurrentLines != want.BaselineLines {
			t.Fatalf("file %s measured %d != locked %d", want.ID, got.CurrentLines, want.BaselineLines)
		}
	}
}

func TestBillingFinalConvergenceSymbolInventoryDeterministic(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	gitFS, err := loadGitCommitFS(root, doc.BaselineSHA)
	if err != nil {
		t.Fatalf("load git commit FS: %v", err)
	}
	// Run the symbol-following fixed-point twice and prove identical output.
	first, err := ComputeBillingFinalConvergenceSymbolInventoryFS(gitFS, doc)
	if err != nil {
		t.Fatalf("first compute: %v", err)
	}
	second, err := ComputeBillingFinalConvergenceSymbolInventoryFS(gitFS, doc)
	if err != nil {
		t.Fatalf("second compute: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("symbol inventory non-deterministic: first=%d second=%d declarations", len(first), len(second))
	}
	for i := range first {
		if !reflect.DeepEqual(first[i], second[i]) {
			t.Fatalf("symbol inventory non-deterministic at index %d:\nfirst=%+v\nsecond=%+v", i, first[i], second[i])
		}
	}
	// The recomputed declarations must match the recorded included_declarations.
	if len(first) != len(doc.IncludedDeclarations) {
		t.Fatalf("recomputed declarations = %d, want recorded %d", len(first), len(doc.IncludedDeclarations))
	}
	for i, want := range doc.IncludedDeclarations {
		got := first[i]
		if got.File != want.File || got.Name != want.Name || got.Kind != want.Kind ||
			got.StartLine != want.StartLine || got.EndLine != want.EndLine ||
			got.Loc != want.Loc || got.Cause != want.Cause || got.CausedBy != want.CausedBy ||
			!reflect.DeepEqual(got.DeclaredNames, want.DeclaredNames) {
			t.Fatalf("declaration[%d] drifted:\n  got=%+v\n  want=%+v", i, got, want)
		}
	}
}

func TestBillingFinalConvergenceDeletionTargetsPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if len(doc.DeletionTargets) == 0 {
		t.Fatal("deletion targets must record brownfield billing-convergence inventory")
	}
	findings, err := EvaluateBillingFinalConvergenceDeletionRatchet(root, doc)
	if err != nil {
		t.Fatalf("evaluate deletion ratchet: %v", err)
	}
	if len(findings) > 0 {
		var b strings.Builder
		for _, f := range findings {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("planned deletion targets must all be present=true and found in production source:\n%s", b.String())
	}
}

func TestBillingFinalConvergenceDeletionTargetsCoverRequiredConcepts(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	// The deletion inventory must cover every concept the design identifies as
	// a structural deletion target (Req 10.5).
	requiredIDs := []string{
		"TurnUsageRecord",                // legacy TUR domain model
		"LegUsageRecord",                 // legacy LUR domain model
		"RetryingCallUsageAppender",      // central append fallback layering
		"RetryingCallLegUsageAppender",   // central append fallback layering
		"UsageAppendWorker",              // central outbox worker
		"UsageAppendOutbox",              // central outbox interface
		"ReservedNano",                   // reserved-balance field
		"JournalBookLegacyAuthorization", // authorization-book const
		"AmountUnitMoneyNano",            // money UsageAuthority unit
		"schema-usage-append-outbox",     // central outbox table
		"schema-turn-usage-records",      // legacy TUR table
		"schema-leg-usage-records",       // legacy LUR table
		"schema-usage-record-processing", // legacy processing table
	}
	byID := make(map[string]bool, len(doc.DeletionTargets))
	for _, dt := range doc.DeletionTargets {
		byID[dt.ID] = true
	}
	for _, id := range requiredIDs {
		if !byID[id] {
			t.Errorf("required deletion target %q missing from inventory", id)
		}
	}
}

func TestBillingFinalConvergenceRatchetsPlanned(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	findings := ValidateBillingFinalConvergencePlannedRatchets(doc)
	if len(findings) > 0 {
		var b strings.Builder
		for _, f := range findings {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("planned ratchets must be non-activated (planned) with correct activation task/flag:\n%s", b.String())
	}
}

func TestBillingFinalConvergenceScannerRulesLocked(t *testing.T) {
	t.Parallel()
	// The counting method, schema version, and symbol-following version are
	// compile-time constants. Changing them requires an explicit version bump
	// and design review, not a silent denominator change.
	if BillingFinalConvergenceCountingMethod != "physical-go-lines-v1" {
		t.Fatalf("counting method = %q, want physical-go-lines-v1", BillingFinalConvergenceCountingMethod)
	}
	if BillingFinalConvergenceSchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1", BillingFinalConvergenceSchemaVersion)
	}
	if BillingFinalConvergenceSymbolFollowingVersion != 1 {
		t.Fatalf("symbol following version = %d, want 1", BillingFinalConvergenceSymbolFollowingVersion)
	}
}

func TestBillingFinalConvergenceExcludedGlobsPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	// The artifact must explicitly classify historical-only migration files.
	found := false
	for _, g := range doc.ExcludedGlobs {
		if strings.Contains(g, "billingstore") && strings.Contains(g, "2*") {
			found = true
		}
	}
	if !found {
		t.Fatal("excluded_globs must classify billingstore timestamped migration files as historical-only")
	}
}

func TestBillingFinalConvergenceNoGenericCauses(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	gitFS, err := loadGitCommitFS(root, doc.BaselineSHA)
	if err != nil {
		t.Fatalf("load git commit FS: %v", err)
	}
	decls, err := ComputeBillingFinalConvergenceSymbolInventoryFS(gitFS, doc)
	if err != nil {
		t.Fatalf("compute symbol inventory: %v", err)
	}
	for _, d := range decls {
		if d.CausedBy == "_" || d.CausedBy == "Config" || d.CausedBy == "ID" ||
			strings.HasSuffix(d.CausedBy, ".Now") || strings.Contains(d.File, "_test_helpers.go") {
			t.Errorf("declaration %s:%s has generic/test-only caused_by or source: %q", d.File, d.Name, d.CausedBy)
		}
	}
}

func TestGenerateBillingFinalConvergenceBaseline(t *testing.T) {
	if os.Getenv("GENERATE_BASELINE") != "1" {
		t.Skip("skipping baseline generation; set GENERATE_BASELINE=1 to run")
	}
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	// Audit the generator: must retain baseline_sha == spec.json.dependencies[0].implementation_main_sha
	sf := loadSpecJSON(t)
	if len(sf.Dependencies) != 1 {
		t.Fatalf("spec.json must have exactly 1 dependency, got %d", len(sf.Dependencies))
	}
	dep := sf.Dependencies[0]
	if doc.BaselineSHA != dep.ImplementationMainSHA {
		t.Fatalf("generator baseline_sha = %q, want spec.json implementation_main_sha %q (fail closed on mismatch)",
			doc.BaselineSHA, dep.ImplementationMainSHA)
	}

	m, err := MeasureBillingFinalConvergenceDenominator(root, doc)
	if err != nil {
		t.Fatalf("measure denominator: %v", err)
	}

	for i := range doc.IncludedRoots {
		doc.IncludedRoots[i].BaselineLines = m.Roots[i].CurrentLines
	}
	for i := range doc.IncludedFiles {
		doc.IncludedFiles[i].BaselineLines = m.Files[i].CurrentLines
	}

	doc.DenominatorLOC = m.DenominatorLOC
	doc.IncludedDeclarations = m.SymbolDeclarations

	path := filepath.Join(root, filepath.FromSlash(BillingFinalConvergenceBaselineRelPath))
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	t.Logf("Successfully wrote updated baseline to %s with denominator %d and %d declarations", path, doc.DenominatorLOC, len(doc.IncludedDeclarations))
}
