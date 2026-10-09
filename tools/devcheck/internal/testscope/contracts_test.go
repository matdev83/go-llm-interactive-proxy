package testscope

import (
	"slices"
	"testing"
)

func TestContractSelection_SurfaceCoverageAndNarrowEdits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path, want string
	}{
		{"internal/standardplugins/featurehost/inputs.go", "TestGenerationFacadeShape"},
		{"internal/infra/runtimebundle/build.go", "TestRuntimeBundleFeatureIdentifierBoundary"},
		{"internal/core/runtime/attempt.go", "TestClosedPlaneArchitectureRatchets_ProductionClean"},
		{"internal/core/execctx/views.go", "TestClosedPlaneArchitectureRatchets_ProductionClean"},
		{"internal/infra/conversationview/store.go", "TestDatabaseParity_StoreContractsCompileTimeAssertions"},
		{"internal/testkit/dbparity/catalog.go", "TestDatabaseParity_CatalogIntegrity"},
		{"pkg/lipsdk/feature/plane.go", "TestStructuralABIMutationGuards"},
		{"pkg/lipapi/request.go", "TestStructuralABIMutationGuards"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			tests, _ := selectContracts([]string{tc.path})
			if !slices.Contains(tests, tc.want) {
				t.Fatalf("%s not selected: %v", tc.want, tests)
			}
		})
	}
	for _, name := range []string{"docs/guide.md", "internal/plugins/features/example/body.go", "internal/core/runtime/attempt_test.go"} {
		if tests, _ := selectContracts([]string{name}); len(tests) != 0 {
			t.Errorf("ordinary edit %s selected %v", name, tests)
		}
	}
}

func TestAutomationSelection_UsesExistingScriptAndConsumerContracts(t *testing.T) {
	t.Parallel()
	qa, scripts := selectAutomation([]string{"scripts/race-check.sh", ".github/workflows/race-fuzz-nightly.yml"})
	if !slices.Contains(qa, "TestRaceCheckStagedScanPartitionsArchtestFromOrdinaryScopes") || !slices.Contains(scripts, "scripts/test-race-check.sh") {
		t.Fatalf("automation consumer/self-test missing: %v %v", qa, scripts)
	}
	qa, scripts = selectAutomation([]string{"docs/guide.md", "internal/plugins/features/example/body.go"})
	if len(qa) != 0 || len(scripts) != 0 {
		t.Fatalf("ordinary edits broadened: %v %v", qa, scripts)
	}
}
