package testscope

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// ContractPlan selects existing cross-package checks and direct-package lint.
// It deliberately does not expand reverse dependencies or certify delivery.
type ContractPlan struct {
	Base    string
	Changed []string
	Tests   []string
	Reasons []string
	Lint    []Module
	QA      []string
	Scripts []string
}

// BuildContracts includes branch, index, working-tree and untracked changes.
// Unlike default-test planning, an unresolved base is an error, not a full run.
func BuildContracts(ctx context.Context, root, base string) (ContractPlan, error) {
	if base == "" {
		base = "origin/main"
	}
	paths, comparison, err := changedPaths(ctx, root, base)
	if err != nil {
		return ContractPlan{}, err
	}
	modules, err := discoverModules(root)
	if err != nil {
		return ContractPlan{}, err
	}
	plan := ContractPlan{Base: comparison, Changed: paths}
	plan.Tests, plan.Reasons = selectContracts(paths)
	plan.QA, plan.Scripts = selectAutomation(paths)
	packages := make(map[string][]string)
	for _, name := range paths {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		dir := path.Dir(name)
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); errors.Is(err, os.ErrNotExist) {
			plan.Reasons = append(plan.Reasons, "removed package has no lint target: "+dir)
			continue
		} else if err != nil {
			return ContractPlan{}, err
		}
		sources, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.go"))
		if err != nil {
			return ContractPlan{}, err
		}
		if len(sources) == 0 {
			plan.Reasons = append(plan.Reasons, "removed package has no Go sources to lint: "+dir)
			continue
		}
		module := owningModule(name, modules)
		rel, err := filepath.Rel(filepath.Join(root, module), filepath.Join(root, dir))
		if err != nil {
			return ContractPlan{}, err
		}
		packages[module] = append(packages[module], "./"+filepath.ToSlash(rel))
	}
	for _, module := range modules {
		selected := packages[module]
		if len(selected) == 0 {
			continue
		}
		slices.Sort(selected)
		plan.Lint = append(plan.Lint, Module{Directory: module, Packages: slices.Compact(selected)})
	}
	return plan, nil
}

// Known automation inputs reuse their existing self-tests and consumer contracts.
// This is routing, not another source scanner or a full-suite fallback.
func selectAutomation(paths []string) ([]string, []string) {
	var qa, scripts []string
	for _, name := range paths {
		switch name {
		case "scripts/race-check.sh", "scripts/test-race-check.sh", "scripts/go-dev-guard.sh", "scripts/test-go-dev-guard.sh", ".github/workflows/race-fuzz-nightly.yml":
			qa = append(qa, "TestRaceCheckDevHostGuard", "TestRaceCheckStagedScanPartitionsArchtestFromOrdinaryScopes")
			scripts = append(scripts, "scripts/test-race-check.sh")
		case "scripts/quality-checks.sh", "scripts/quality-gate.sh", "scripts/test-quality-checks.sh", "scripts/hooks/pre-commit", "scripts/staged-commit-checks.sh", ".githooks/pre-commit":
			scripts = append(scripts, "scripts/test-quality-checks.sh")
		}
		if strings.HasPrefix(name, ".github/workflows/") || name == "scripts/ci-scope.sh" {
			scripts = append(scripts, "scripts/ci-scope.sh --self-test")
			qa = append(qa, "TestQAFastPreflight_MainPushLaneScopes")
		}
	}
	slices.Sort(qa)
	slices.Sort(scripts)
	return slices.Compact(qa), slices.Compact(scripts)
}

func selectContracts(paths []string) ([]string, []string) {
	var tests, reasons []string
	add := func(reason string, names ...string) {
		reasons = append(reasons, reason)
		tests = append(tests, names...)
	}
	for _, name := range paths {
		if !strings.HasSuffix(name, ".go") || isTestFile(name) {
			continue
		}
		if strings.HasPrefix(name, "internal/core/") || strings.HasPrefix(name, "internal/infra/") || strings.HasPrefix(name, "pkg/") || strings.HasPrefix(name, "internal/standardplugins/") {
			add("shared boundary", "TestForbiddenImportsAbsent", "TestCriticalFileBudgets", "TestLineComplexityBudgets", "TestClosedPlaneArchitectureRatchets_ProductionClean")
		}
		if strings.HasPrefix(name, "internal/core/") || strings.HasPrefix(name, "internal/infra/runtime") {
			add("request state/ownership", "TestRequestAttemptStateRatchetsPassOnCurrentCode")
		}
		if strings.HasPrefix(name, "internal/standardplugins/") || strings.HasPrefix(name, "internal/pluginreg/") || strings.HasPrefix(name, "internal/infra/runtime") || strings.HasPrefix(name, "internal/stdhttp/") || strings.HasPrefix(name, "cmd/lipstd/") || strings.HasPrefix(name, "pkg/lipruntime/") {
			add("composition facade", "TestGenerationFacadeShape", "TestFacadeNamedTypesStable", "TestRuntimeBundleFeatureIdentifierBoundary", "TestRecvFacadeRatchet", "TestShrinkage_NetReductionMeetsRequirement115")
		}
		// Persistence implementations live throughout core and infra; select the
		// existing catalog scanner conservatively instead of guessing store names.
		if strings.HasPrefix(name, "internal/core/") || strings.HasPrefix(name, "internal/infra/") || strings.HasPrefix(name, "internal/testkit/dbparity/") {
			add("persistence capabilities/catalog", "TestDatabaseParity_CatalogIntegrity", "TestDatabaseParity_DiscoveredMigrationRootsMatchCatalog", "TestDatabaseParity_DialectSensitiveSourcesAreCataloged", "TestDatabaseParity_StoreContractsCompileTimeAssertions", "TestDatabaseParity_StableWrapperCoverage", "TestDatabaseParity_CapabilityEvidenceAnchors")
		}
		if strings.HasPrefix(name, "pkg/") || strings.HasPrefix(name, "api/backendplugin/") {
			add("public contract", "TestStructuralABIMutationGuards")
		}
	}
	slices.Sort(tests)
	slices.Sort(reasons)
	return slices.Compact(tests), slices.Compact(reasons)
}
