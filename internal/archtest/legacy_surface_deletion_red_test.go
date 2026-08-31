//go:build red

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// LegacyReferenceFinding records an occurrence of an obsolete legacy merge symbol scheduled for deletion.
type LegacyReferenceFinding struct {
	Category string
	File     string
	Line     int
	Symbol   string
}

var legacySymbolsToPurge = map[string]struct{}{
	"MergedFeatureSurface":            {},
	"MergeBundles":                    {},
	"MergeBundlesChecked":             {},
	"MergeFeatureSurface":             {},
	"MergeFeatureSurfaces":            {},
	"MergeBundlesViaGenerated":        {},
	"MergeFeatureSurfaceViaGenerated": {},
	"ToMergedFeatureSurface":          {},
	"AssertMergedSurfacesEqual":       {},
	"AssertDualPathParity":            {},
	"extensions" + "FromMerged":       {},
}

func classifyLegacyFinding(relPath string) string {
	isTest := strings.HasSuffix(relPath, "_test.go")
	normalized := filepath.ToSlash(relPath)
	if strings.HasPrefix(normalized, "internal/testkit/") {
		return "testkit"
	}
	if strings.HasPrefix(normalized, "internal/featurebundle/") {
		if isTest {
			return "package test"
		}
		return "production"
	}
	if strings.HasPrefix(normalized, "internal/infra/runtimebundle/") {
		if isTest {
			return "runtime test"
		}
		return "runtime composition"
	}
	if strings.HasPrefix(normalized, "internal/infra/") {
		if isTest {
			return "infra test"
		}
		return "infra production"
	}
	if strings.HasPrefix(normalized, "internal/plugins/") || strings.HasPrefix(normalized, "internal/standardplugins/") {
		if isTest {
			return "plugin test"
		}
		return "plugin production"
	}
	if strings.HasPrefix(normalized, "internal/pluginreg/") {
		if isTest {
			return "pluginreg test"
		}
		return "pluginreg production"
	}
	if strings.HasPrefix(normalized, "internal/core/") {
		if isTest {
			return "core test"
		}
		return "core production"
	}
	if strings.HasPrefix(normalized, "internal/archtest/") {
		if isTest {
			return "arch test"
		}
		return "arch production"
	}
	if isTest {
		return "internal test"
	}
	return "internal production"
}

// TestRED_LegacySurfaceDeletionGate characterizes Requirements 4.1, 4.4, 4.5 (Task 1.4):
// The production composition surface, generation compiler, and testkit must not expose or depend on
// MergedFeatureSurface or its lifecycle-only dual-path helpers.
//
// This deletion gate recursively walks the entire internal/ tree, parses all repository-owned Go files
// (including _test.go and red-tagged files), skips generated files structurally (via IsGeneratedFile,
// preserving handwritten merge_generated.go), and detects exact AST identifier occurrences of legacy symbols:
// - MergedFeatureSurface (struct type)
// - MergeBundles / MergeBundlesChecked / MergeFeatureSurface / MergeFeatureSurfaces
// - MergeBundlesViaGenerated / MergeFeatureSurfaceViaGenerated
// - GeneratedMergeSurface.ToMergedFeatureSurface
// - AssertMergedSurfacesEqual / AssertDualPathParity
//
// On the review baseline (Phase 1), this test fails and provides an exhaustive, classified inventory
// of all legacy references to be removed in Task 4.1-4.3.
// Once Task 4.1-4.3 removes legacy surfaces and migrations complete, this test naturally passes (turns GREEN).
func TestRED_LegacySurfaceDeletionGate(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	var findings []LegacyReferenceFinding

	internalDir := filepath.Join(root, "internal")
	err := filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		// Self-exclusion: skip the deletion gate's own test file to prevent its purge symbol map from self-triggering.
		if relPath == "internal/archtest/legacy_surface_deletion_red_test.go" {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}

		// Structurally skip generated files while preserving handwritten merge files
		if IsGeneratedFile(relPath, src, f) && filepath.Base(relPath) != "merge_generated.go" {
			return nil
		}

		seenOnLine := make(map[string]struct{})
		ast.Inspect(f, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if _, isLegacy := legacySymbolsToPurge[ident.Name]; !isLegacy {
				return true
			}
			pos := fset.Position(ident.Pos())
			key := fmt.Sprintf("%d:%s", pos.Line, ident.Name)
			if _, seen := seenOnLine[key]; seen {
				return true
			}
			seenOnLine[key] = struct{}{}

			findings = append(findings, LegacyReferenceFinding{
				Category: classifyLegacyFinding(relPath),
				File:     relPath,
				Line:     pos.Line,
				Symbol:   ident.Name,
			})
			return true
		})
		return nil
	})
	require.NoError(t, err, "failed scanning internal directory")

	slices.SortFunc(findings, func(a, b LegacyReferenceFinding) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Symbol, b.Symbol)
	})

	if len(findings) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("\n=== LEGACY MERGED FEATURE SURFACE DELETION GATE ===\nFound %d legacy references across scanned packages:\n\n", len(findings)))
		for i, f := range findings {
			b.WriteString(fmt.Sprintf("%3d. [%s] %s:%d (symbol: %s)\n", i+1, f.Category, f.File, f.Line, f.Symbol))
		}
		b.WriteString("\nAll above legacy symbols must be removed in Task 4.1-4.3 to pass this deletion gate.\n")
		t.Log(b.String())
	}

	require.Empty(t, findings, "production packages and testkit must not contain legacy MergedFeatureSurface references after Task 4.1-4.3")
}

func TestRED_LegacySymbolsToPurge_ExtensionsFromMergedReconstructedAndCaught(t *testing.T) {
	t.Parallel()

	expected := "extensions" + "FromMerged"
	require.Contains(t, legacySymbolsToPurge, expected)

	syntheticSrc := "package synthetic\nfunc " + expected + "() {}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", syntheticSrc, 0)
	require.NoError(t, err)

	var detected []string
	ast.Inspect(f, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok {
			if _, isLegacy := legacySymbolsToPurge[ident.Name]; isLegacy {
				detected = append(detected, ident.Name)
			}
		}
		return true
	})
	require.Contains(t, detected, expected)
}
