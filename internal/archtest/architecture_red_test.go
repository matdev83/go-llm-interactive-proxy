//go:build architecture_red

package archtest

import (
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/conformance"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/scale"
)

// TestRED_ContinuationAuthorities_DuplicateStructs asserts that duplicate MemoryStore
// and StreamRecorder concrete struct authorities across pkg/lipsdk/continuation
// and internal/core/continuation are detected and fail under the RED build target.
func TestRED_ContinuationAuthorities_DuplicateStructs(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	duplicates, err := DetectDuplicateContinuationStructs(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect continuation structs: %v", err)
	}

	if len(duplicates) > 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Discovered duplicate continuation struct authorities across pkg/lipsdk/continuation and internal/core/continuation: %v", duplicates)
	}
}

// TestRED_ContributionRegistries_ParallelAuthoritativeViews asserts that parallel authoritative
// contribution registries across internal/pluginreg and internal/standardplugins fail under RED target.
func TestRED_ContributionRegistries_ParallelAuthoritativeViews(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	registries, err := DetectDuplicateAuthoritativeRegistries(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect contribution registries: %v", err)
	}

	if len(registries) > 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Discovered parallel authoritative contribution registries across internal packages: %+v", registries)
	}
}

// TestRED_RouteKinds_CentralProtocolSpecificKinds asserts that central protocol-specific route kinds
// in internal/stdhttp/contract fail under RED target until generalized in Phase 4.
func TestRED_RouteKinds_CentralProtocolSpecificKinds(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	routeKinds, err := DetectCentralProtocolRouteKinds(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect central route kinds: %v", err)
	}

	if len(routeKinds) > 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Discovered central protocol-specific route kinds in internal/stdhttp/contract: %+v", routeKinds)
	}
}

// TestRED_Diagnostics_CentralProtocolSpecificDebt asserts that central protocol-specific
// diagnostic DTO rows, switches, and projectors fail under RED target until generalized in Phase 4.
func TestRED_Diagnostics_CentralProtocolSpecificDebt(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	diagDebt, err := DetectCentralProtocolDiagnosticsDebt(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect central diagnostics debt: %v", err)
	}

	if len(diagDebt) > 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Discovered central protocol-specific diagnostic DTO rows and switches: %+v", diagDebt)
	}
}

// TestRED_NonCartesianScale_CartesianCellDebt asserts that current 45 Cartesian cells
// exceed the target non-Cartesian sentinel threshold of <= 15 cells and fail under RED target.
func TestRED_NonCartesianScale_CartesianCellDebt(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	report, err := scale.ScanCurrentCartesianDebt(repoRoot)
	if err != nil {
		t.Fatalf("failed to scan current Cartesian debt: %v", err)
	}

	cells := conformance.AllCells()
	targetSentinelMaxThreshold := 15

	if len(cells) > targetSentinelMaxThreshold {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Current baseline has %d Cartesian cells, exceeding target non-Cartesian sentinel threshold of %d (discovered %d AllCells calls, %d nested loops, %d central table entries)",
			len(cells), targetSentinelMaxThreshold, report.AllCellsCallsDiscovered, report.NestedLoopsDiscovered, report.CentralTableEntriesCount)
	}
}
