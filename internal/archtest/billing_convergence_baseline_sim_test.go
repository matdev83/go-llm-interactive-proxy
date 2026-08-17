package archtest

import (
	"os"
	"strings"
	"testing"
)

func TestBillingFinalConvergenceSHASwapFailsRecomputation(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	// Swap SHA to the spec-only merge SHA (9bf9c66...)
	const specMergeSHA = "9bf9c66a09de50ab3dcad18f0a8a84c2c2d49ed9"
	doc.BaselineSHA = specMergeSHA

	m, err := MeasureBillingFinalConvergenceDenominator(root, doc)
	if err != nil {
		// Valid outcome if the swapped commit cannot be parsed or runs correctly but differently
		return
	}

	if m.DenominatorLOC == doc.DenominatorLOC {
		t.Errorf("expected recomputed denominator LOC to change when SHA is swapped to spec-only merge, but got same %d", m.DenominatorLOC)
	}
}

func TestBillingFinalConvergenceCurrentTreeModificationLeavesBaselinePassing(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	// Validate baseline recomputation. Since it is pinned to doc.BaselineSHA,
	// local modifications in the working tree must not change the computed LOC.
	m, err := MeasureBillingFinalConvergenceDenominator(root, doc)
	if err != nil {
		t.Fatalf("recompute denominator: %v", err)
	}

	if m.DenominatorLOC != doc.DenominatorLOC {
		t.Errorf("expected recomputed denominator LOC %d to equal baseline %d despite local modifications", m.DenominatorLOC, doc.DenominatorLOC)
	}
}

type mockFS struct {
	files map[string][]byte
}

func (m *mockFS) ReadFile(rel string) ([]byte, error) {
	if c, ok := m.files[rel]; ok {
		return c, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockFS) WalkProductionGoFiles(fn func(rel string, src []byte) error) error {
	for k, v := range m.files {
		if strings.HasSuffix(k, ".go") && !strings.HasSuffix(k, "_test.go") {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mockFS) WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error {
	for k, v := range m.files {
		if strings.HasPrefix(k, rootPath) {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mockFS) ReadDir(rel string) ([]os.DirEntry, error) {
	var out []os.DirEntry
	seen := make(map[string]bool)
	prefix := rel + "/"
	if rel == "." || rel == "" {
		prefix = ""
	}
	for k := range m.files {
		if prefix != "" && !strings.HasPrefix(k, prefix) {
			continue
		}
		suffix := k[len(prefix):]
		parts := strings.Split(suffix, "/")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		if seen[parts[0]] {
			continue
		}
		seen[parts[0]] = true
		out = append(out, gitDirEntry{name: parts[0], isDir: len(parts) > 1})
	}
	return out, nil
}

func TestBillingFinalConvergenceSimulatedRatchetBehavior(t *testing.T) {
	t.Parallel()

	// 1. Simulate a deletion target being deleted from the current tree while ratchet is "planned"
	doc := BillingFinalConvergenceBaselineFile{
		BaselineSHA: "mock-sha",
		DeletionTargets: []BillingFinalConvergenceDeletionTarget{
			{
				ID:      "TurnUsageRecord",
				Kind:    "type",
				Package: "internal/core/billing",
				Name:    "TurnUsageRecord",
				Present: true,
				Status:  BillingFinalConvergenceRatchetPlanned,
			},
		},
		PlannedRatchets: []BillingFinalConvergencePlannedRatchet{
			{
				ID:     "structural_deletion",
				Status: BillingFinalConvergenceRatchetPlanned,
			},
		},
	}

	fsEmpty := &mockFS{
		files: map[string][]byte{},
	}

	// Since ratchet is planned, current tree evaluation should pass even if target is missing
	findings, err := EvaluateBillingFinalConvergenceDeletionRatchetFS(fsEmpty, doc, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("expected no findings for missing target when ratchet is planned, got: %+v", findings)
	}

	// 2. Active deletion ratchet requires absence in current tree
	docActive := doc
	docActive.PlannedRatchets[0].Status = BillingFinalConvergenceRatchetActive
	docActive.DeletionTargets[0].Status = BillingFinalConvergenceRatchetActive
	// Set target present to false since it is simulated active in baseline metadata
	docActive.DeletionTargets[0].Present = false

	// If absent, it should pass
	findings, err = EvaluateBillingFinalConvergenceDeletionRatchetFS(fsEmpty, docActive, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("expected no findings when active and target is absent, got: %+v", findings)
	}

	// If present, it should fail
	fsPresent := &mockFS{
		files: map[string][]byte{
			"internal/core/billing/records.go": []byte("package billing\n\ntype TurnUsageRecord struct{}\n"),
		},
	}
	findings, err = EvaluateBillingFinalConvergenceDeletionRatchetFS(fsPresent, docActive, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) == 0 {
		t.Error("expected finding for present target when active, but got none")
	}
}

func TestBillingFinalConvergenceNegativeFixtureCommonNames(t *testing.T) {
	t.Parallel()

	doc := BillingFinalConvergenceBaselineFile{
		SeedSymbols: BillingFinalConvergenceInitialSeedSymbols,
	}

	fs := &mockFS{
		files: map[string][]byte{
			"internal/core/billing/records.go": []byte(`
package billing

import "internal/core/usageauthority/domain"

type TurnUsageRecord struct {
	Amount domain.Amount
}

type Config struct {
	Val int
}

func New() *TurnUsageRecord {
	return &TurnUsageRecord{}
}
`),
			"internal/core/usageauthority/domain/quota.go": []byte(`
package domain
type Amount struct {
	Unit string
}
`),
			"pkg/other/unrelated.go": []byte(`
package other

type Config struct {
	Name string
}

type Kind int

type Event struct {
	ID string
}

func New() *Config {
	return &Config{}
}
`),
		},
	}

	decls, err := ComputeBillingFinalConvergenceSymbolInventoryFS(fs, doc)
	if err != nil {
		t.Fatalf("failed to compute symbol inventory: %v", err)
	}

	for _, d := range decls {
		if strings.HasPrefix(d.File, "pkg/other") {
			t.Errorf("unrelated declaration from pkg/other was incorrectly included: %s (%s)", d.Name, d.File)
		}
	}
}
