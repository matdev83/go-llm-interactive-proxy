package qa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase73_AlertDocAndEconomicControlReadyEvidence(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc := filepath.Join(root, "docs", "dual-plane-readiness-alerts.md")
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read alerts doc: %v", err)
	}
	text := string(b)
	for _, needle := range []string{
		"EconomicControlReady",
		"terminal_work_backlog",
		"lease_sets.uncertain",
		"BenchmarkMemoryAcquireSetFiveSlotHundredContenders",
		"benchstat",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("dual-plane-readiness-alerts.md missing %q", needle)
		}
	}
	readySrc := filepath.Join(root, "pkg", "lipsdk", "controlplane", "economic_control_ready.go")
	if _, err := os.Stat(readySrc); err != nil {
		t.Fatalf("missing EconomicControlReady source: %v", err)
	}
}
