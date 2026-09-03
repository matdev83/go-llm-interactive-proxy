package archtest

import (
	"fmt"
	"strings"
	"testing"
)

// TestForbiddenImports_RuntimeBundleConcreteFeaturesRuleEnforced verifies that ForbiddenImports
// contains a permanent rule forbidding any production imports from internal/infra/runtimebundle
// to internal/plugins/features/*, with zero whitelist / exceptions (Requirements 5.2, 7.2, 7.8).
func TestForbiddenImports_RuntimeBundleConcreteFeaturesRuleEnforced(t *testing.T) {
	t.Parallel()

	var matched []ForbiddenImportRule
	for _, rule := range ForbiddenImports {
		if rule.SourcePattern == "internal/infra/runtimebundle" &&
			rule.TargetPattern == "/internal/plugins/features/" {
			matched = append(matched, rule)
		}
	}
	if len(matched) == 0 {
		t.Fatal("ForbiddenImports missing rule forbidding internal/infra/runtimebundle -> /internal/plugins/features/")
	}
	if len(matched) > 1 {
		t.Fatalf("expected exactly 1 runtimebundle -> features rule, got %d", len(matched))
	}
	rule := matched[0]
	if len(rule.ExceptPrefix) != 0 {
		t.Fatalf("runtimebundle forbidden feature rule must have zero whitelist exceptions, got %v", rule.ExceptPrefix)
	}
}

// TestForbiddenImports_RuntimeBundleConcreteFeaturesRenamedOrNestedBypassRejected verifies that
// renamed files, nested subpackages, or indirect filenames in runtimebundle cannot bypass the rule.
func TestForbiddenImports_RuntimeBundleConcreteFeaturesRenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "runtimebundle root imports toolcallrepair feature",
			relPath:    "internal/infra/runtimebundle/renamed.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair",
			wantForbid: true,
		},
		{
			name:       "runtimebundle root imports secretguard feature",
			relPath:    "internal/infra/runtimebundle/renamed.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard",
			wantForbid: true,
		},
		{
			name:       "runtimebundle root imports compactioncontinuity feature",
			relPath:    "internal/infra/runtimebundle/compaction_continuity_result_adapter.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/resultmerge",
			wantForbid: true,
		},
		{
			name:       "runtimebundle root imports reasoningpreservation feature",
			relPath:    "internal/infra/runtimebundle/renamed.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation",
			wantForbid: true,
		},
		{
			name:       "runtimebundle nested subpackage imports feature",
			relPath:    "internal/infra/runtimebundle/nested/sub/bypass.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard",
			wantForbid: true,
		},
		{
			name:       "runtimebundle deeply nested subpackage imports feature",
			relPath:    "internal/infra/runtimebundle/a/b/c/deep.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/partsnoop",
			wantForbid: true,
		},
		{
			name:       "runtimebundle imports compactioncompose adapter (allowed)",
			relPath:    "internal/infra/runtimebundle/compaction_continuity_generation.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose",
			wantForbid: false,
		},
		{
			name:       "runtimebundle imports reasoningcompose adapter (allowed)",
			relPath:    "internal/infra/runtimebundle/reasoning_preservation_compression.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/infra/reasoningcompose",
			wantForbid: false,
		},
		{
			name:       "runtimebundle imports secretguardcompose adapter (allowed)",
			relPath:    "internal/infra/runtimebundle/secret_guard_runtime.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretguardcompose",
			wantForbid: false,
		},
		{
			name:       "runtimebundle imports standardplugins distribution (allowed)",
			relPath:    "internal/infra/runtimebundle/bootstrap_effective.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins",
			wantForbid: false,
		},
		{
			name:       "compactioncompose dedicated adapter imports feature (allowed)",
			relPath:    "internal/infra/compactioncompose/parent_port.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity",
			wantForbid: false,
		},
		{
			name:       "reasoningcompose dedicated adapter imports feature (allowed)",
			relPath:    "internal/infra/reasoningcompose/bind.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation",
			wantForbid: false,
		},
		{
			name:       "secretguardcompose dedicated adapter imports feature (allowed)",
			relPath:    "internal/infra/secretguardcompose/compose.go",
			importPath: "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard",
			wantForbid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf("package test\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports(%q): %v", tc.relPath, err)
			}
			isForbidden := len(findings) > 0
			if isForbidden != tc.wantForbid {
				t.Fatalf("ScanFileForbiddenImports(%q, %q): got forbidden=%v, want %v (findings: %v)",
					tc.relPath, tc.importPath, isForbidden, tc.wantForbid, findings)
			}
		})
	}
}

// TestProductionRuntimeBundleHasZeroConcreteFeatureImports scans the live production tree
// of internal/infra/runtimebundle and asserts zero imports of internal/plugins/features/*.
func TestProductionRuntimeBundleHasZeroConcreteFeatureImports(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if !MatchPathPrefix(pkg, "internal/infra/runtimebundle") {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, imp := range FileImportPaths(f) {
			if strings.Contains(imp, "/internal/plugins/features/") {
				violations = append(violations, fmt.Sprintf("%s: imports %s", rel, imp))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("production internal/infra/runtimebundle has forbidden feature imports (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}
