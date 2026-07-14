package qa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDualPlaneReleaseGateEvidencePresent pins Phase 12.3 / req 17.9 evidence
// surfaces so release documentation and dual-plane contract tests stay discoverable.
func TestDualPlaneReleaseGateEvidencePresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	docPath := filepath.Join(root, "docs", "release-gates.md")
	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read release-gates.md: %v", err)
	}
	body := string(doc)
	for _, needle := range []string{
		"Dual-plane economics",
		"make parity-checks",
		"make release-gates",
		"make test-race",
		"make test-authority-postgres",
		"LIP_TEST_POSTGRES_DSN",
		"SharedCheckpointAcrossFrontend",
		"BenchmarkParallelRaceLegsAuthority",
		"15.9",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("docs/release-gates.md missing dual-plane gate marker %q", needle)
		}
	}

	evidence := []string{
		"internal/core/authoritycoord/provider_isolation_test.go",
		"internal/core/runtime/dual_plane_cross_frontend_checkpoint_test.go",
		"internal/core/runtime/parallel_race_legs_bench_test.go",
		"internal/core/usageauthority/app/privacy_test.go",
		"internal/core/usageauthority/app/stage_metrics_observation_test.go",
		"internal/core/metering/reconcile/reconcile_test.go",
		"internal/infra/metering/journalstore/memory_test.go",
		"testdata/enterprise_module/main_test.go",
		"testdata/migration/README.md",
	}
	for _, rel := range evidence {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing dual-plane release evidence %s: %v", rel, err)
		}
	}
}
