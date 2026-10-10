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

func TestContractSelection_RegistrationSurfaceRunsFamilyContracts(t *testing.T) {
	t.Parallel()
	tests, groups, reasons := selectRegistrationContracts([]string{"internal/standardplugins/standard_contributions.go"})
	for _, want := range []string{"TestOfficialBackendsHaveLifecycleContractTests", "TestNonCartesianScale_ThousandProfilesDoNotMultiplyCartesianPairs"} {
		if !slices.Contains(tests, want) {
			t.Fatalf("%s not selected: %v", want, tests)
		}
	}
	wantGroups := map[string]string{
		"./internal/testkit/contract/backend": "TestStandardComposition_CertifiesEveryInProcessFamily",
		"./internal/stdhttp/selfdefense":      "TestBuiltInImpossiblePathSetNeverMakesAPublishedRouteUnreachable",
	}
	for pkg, name := range wantGroups {
		found := false
		for _, group := range groups {
			if group.Package == pkg && slices.Contains(group.Tests, name) {
				found = true
			}
		}
		if !found {
			t.Fatalf("group %s/%s not selected: %+v", pkg, name, groups)
		}
	}
	if !slices.Contains(reasons, "plugin registration surface") {
		t.Fatalf("registration reason missing: %v", reasons)
	}
	for _, surface := range []string{
		"internal/pluginreg/reg.go",
		"internal/plugins/backends/example/backend.go",
		"internal/plugins/frontends/example/decode.go",
		"internal/providerprofiles/compiler.go",
	} {
		if selected, _, _ := selectRegistrationContracts([]string{surface}); len(selected) == 0 {
			t.Errorf("registration surface %s selected no contracts", surface)
		}
	}
	for _, ordinary := range []string{"internal/plugins/features/example/body.go", "internal/core/runtime/attempt.go", "docs/guide.md"} {
		if selected, groups, _ := selectRegistrationContracts([]string{ordinary}); len(selected) != 0 || len(groups) != 0 {
			t.Errorf("ordinary edit %s selected registration contracts: %v %+v", ordinary, selected, groups)
		}
	}
}

func TestAutomationSelection_UsesExistingScriptAndConsumerContracts(t *testing.T) {
	t.Parallel()
	qa, scripts := selectAutomation([]string{"scripts/race-check.sh", ".github/workflows/race-fuzz-nightly.yml"})
	if !slices.Contains(qa, "TestRaceCheckStagedScanPartitionsArchtestFromOrdinaryScopes") || !slices.Contains(scripts, "scripts/test-race-check.sh") {
		t.Fatalf("automation consumer/self-test missing: %v %v", qa, scripts)
	}
	if !slices.Contains(scripts, "scripts/check-workflows.sh") {
		t.Fatalf("workflow lint missing for a workflow edit: %v", scripts)
	}
	for _, workflow := range []string{".github/workflows/release.yml", ".github/workflows/backend-plugin-release-gates.yml"} {
		qa, scripts = selectAutomation([]string{workflow})
		if !slices.Contains(qa, "TestRaceCheckStagedScanPartitionsArchtestFromOrdinaryScopes") || !slices.Contains(scripts, "scripts/test-race-check.sh") {
			t.Fatalf("race partition self-test missing for %s: %v %v", workflow, qa, scripts)
		}
	}
	qa, scripts = selectAutomation([]string{"docs/guide.md", "internal/plugins/features/example/body.go"})
	if len(qa) != 0 || len(scripts) != 0 {
		t.Fatalf("ordinary edits broadened: %v %v", qa, scripts)
	}
}
