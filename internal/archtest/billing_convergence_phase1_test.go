package archtest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBillingFinalConvergencePhase1BridgeForbidIsActive(t *testing.T) {
	t.Parallel()
	if !BillingFinalConvergencePhase1BridgeForbid {
		t.Fatal("Phase 1 bridge-forbid architecture guard must be active")
	}
	findings, err := EvaluateBillingFinalConvergencePhase1BridgeForbid(repoRoot(t))
	if err != nil {
		t.Fatalf("evaluate bridge-forbid guard: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("current production must not contain the deleted customer rating bridge:\n%s", formatRatchetFindings(findings))
	}
}

func TestBillingFinalConvergencePhase1BridgeForbidDetectsLegacyIdentifier(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "internal", "core", "billing", "bridge.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const source = `package billing

func bridge() {
	_ = LegUsageRecord{}
}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := EvaluateBillingFinalConvergencePhase1BridgeForbid(root)
	if err != nil {
		t.Fatalf("evaluate fixture: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("bridge-forbid guard must detect a legacy rating identifier")
	}
}
