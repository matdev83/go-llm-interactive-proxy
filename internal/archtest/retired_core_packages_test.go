package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProductionTreeRetiredCorePackagesAbsent asserts that zero production .go files exist
// under internal/core/toolcallrepair, internal/core/secretguard, or internal/core/compactiondetect
// (retired absences, Requirement 7.3).
func TestProductionTreeRetiredCorePackagesAbsent(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	retiredDirs := []string{
		"internal/core/toolcallrepair",
		"internal/core/secretguard",
		"internal/core/compactiondetect",
	}

	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		for _, retired := range retiredDirs {
			if pkg == retired || strings.HasPrefix(pkg, retired+"/") {
				violations = append(violations, fmt.Sprintf("%s: belongs to retired package %s", rel, retired))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("production tree contains files under retired core packages (%d) (Requirement 7.3):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}

	// Verify catalog-level detection consistency via ScanRetiredPackages
	findings, err := ScanRetiredPackages(root)
	if err != nil {
		t.Fatalf("ScanRetiredPackages: %v", err)
	}
	if len(findings) > 0 {
		var msgs []string
		for _, f := range findings {
			msgs = append(msgs, f.String())
		}
		t.Fatalf("ScanRetiredPackages reported resurrected packages (%d) (Requirement 7.3):\n%s",
			len(findings), strings.Join(msgs, "\n"))
	}
}

// TestProductionTreeRetiredCorePackages_RenamedOrNestedResurrectionRejected verifies that
// root files, renamed files, or deeply nested subpackages under retired core packages
// cannot bypass the retired package absence check, while legitimate packages pass (Requirement 7.3).
func TestProductionTreeRetiredCorePackages_RenamedOrNestedResurrectionRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		relPath    string
		wantReject bool
	}{
		// Root resurrection
		{
			name:       "toolcallrepair root file resurrection",
			relPath:    "internal/core/toolcallrepair/engine.go",
			wantReject: true,
		},
		{
			name:       "secretguard root file resurrection",
			relPath:    "internal/core/secretguard/catalog.go",
			wantReject: true,
		},
		{
			name:       "compactiondetect root file resurrection",
			relPath:    "internal/core/compactiondetect/detector.go",
			wantReject: true,
		},
		// Renamed file resurrection
		{
			name:       "toolcallrepair renamed file resurrection",
			relPath:    "internal/core/toolcallrepair/renamed_repair.go",
			wantReject: true,
		},
		{
			name:       "secretguard renamed file resurrection",
			relPath:    "internal/core/secretguard/renamed_guard.go",
			wantReject: true,
		},
		{
			name:       "compactiondetect renamed file resurrection",
			relPath:    "internal/core/compactiondetect/renamed_detector.go",
			wantReject: true,
		},
		// Nested subpackage resurrection
		{
			name:       "toolcallrepair nested subpackage resurrection",
			relPath:    "internal/core/toolcallrepair/nested/sub/bypass.go",
			wantReject: true,
		},
		{
			name:       "toolcallrepair deeply nested jsonshape resurrection",
			relPath:    "internal/core/toolcallrepair/repair/jsonshape/deep.go",
			wantReject: true,
		},
		{
			name:       "secretguard nested subpackage resurrection",
			relPath:    "internal/core/secretguard/nested/sub/bypass.go",
			wantReject: true,
		},
		{
			name:       "secretguard deeply nested engine resurrection",
			relPath:    "internal/core/secretguard/engine/nested/deep.go",
			wantReject: true,
		},
		{
			name:       "compactiondetect nested subpackage resurrection",
			relPath:    "internal/core/compactiondetect/nested/sub/bypass.go",
			wantReject: true,
		},
		{
			name:       "compactiondetect deeply nested detector resurrection",
			relPath:    "internal/core/compactiondetect/deep/nested/detector.go",
			wantReject: true,
		},
		// Legitimate packages (must NOT be rejected)
		{
			name:       "toolcallrepair feature bundle allowed",
			relPath:    "internal/plugins/features/toolcallrepair/bundle.go",
			wantReject: false,
		},
		{
			name:       "toolcallrepair feature repair engine allowed",
			relPath:    "internal/plugins/features/toolcallrepair/repair/engine.go",
			wantReject: false,
		},
		{
			name:       "secretguard feature guard allowed",
			relPath:    "internal/plugins/features/secretguard/guard.go",
			wantReject: false,
		},
		{
			name:       "secretguard feature engine catalog allowed",
			relPath:    "internal/plugins/features/secretguard/engine/catalog.go",
			wantReject: false,
		},
		{
			name:       "secretguardcompose dedicated adapter allowed",
			relPath:    "internal/infra/secretguardcompose/compose.go",
			wantReject: false,
		},
		{
			name:       "compactiondetect infra detector allowed",
			relPath:    "internal/infra/compactiondetect/detector.go",
			wantReject: false,
		},
		{
			name:       "compactiondetect infra nested helper allowed",
			relPath:    "internal/infra/compactiondetect/nested/helper.go",
			wantReject: false,
		},
		{
			name:       "compactioncompose dedicated adapter allowed",
			relPath:    "internal/infra/compactioncompose/parent_port.go",
			wantReject: false,
		},
		{
			name:       "core runtime service allowed",
			relPath:    "internal/core/runtime/service.go",
			wantReject: false,
		},
		{
			name:       "core routing router allowed",
			relPath:    "internal/core/routing/router.go",
			wantReject: false,
		},
		{
			name:       "core billing quote allowed",
			relPath:    "internal/core/billing/quote.go",
			wantReject: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ScanFileRetiredPackage(tc.relPath)
			isRejected := f != nil
			if isRejected != tc.wantReject {
				t.Fatalf("ScanFileRetiredPackage(%q): got rejected=%v, want %v (finding: %v)",
					tc.relPath, isRejected, tc.wantReject, f)
			}
		})
	}
}

// TestProductionTreeRetiredCorePackages_AdversarialTreeResurrectionSelfTest creates a simulated
// production repository tree with adversarial root, renamed, and nested files across all three
// retired packages and proves the production tree walk deterministically detects and rejects
// each resurrected file while ignoring legitimate packages (Requirement 7.3).
func TestProductionTreeRetiredCorePackages_AdversarialTreeResurrectionSelfTest(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	adversarialFiles := map[string]string{
		// ToolCallRepair resurrections
		"internal/core/toolcallrepair/engine.go":                "package toolcallrepair\n",
		"internal/core/toolcallrepair/renamed_engine.go":        "package toolcallrepair\n",
		"internal/core/toolcallrepair/nested/sub/bypass.go":     "package bypass\n",
		"internal/core/toolcallrepair/repair/jsonshape/deep.go": "package deep\n",
		// SecretGuard resurrections
		"internal/core/secretguard/catalog.go":           "package secretguard\n",
		"internal/core/secretguard/renamed_catalog.go":   "package secretguard\n",
		"internal/core/secretguard/nested/sub/bypass.go": "package bypass\n",
		"internal/core/secretguard/engine/deep/guard.go": "package deep\n",
		// CompactionDetect resurrections
		"internal/core/compactiondetect/detector.go":          "package compactiondetect\n",
		"internal/core/compactiondetect/renamed_detector.go":  "package compactiondetect\n",
		"internal/core/compactiondetect/nested/sub/bypass.go": "package bypass\n",
		"internal/core/compactiondetect/sub/deep/detector.go": "package deep\n",
		// Legitimate packages (must NOT trigger findings)
		"internal/plugins/features/toolcallrepair/bundle.go": "package toolcallrepair\n",
		"internal/plugins/features/secretguard/guard.go":     "package secretguard\n",
		"internal/infra/compactiondetect/detector.go":        "package compactiondetect\n",
		"internal/infra/secretguardcompose/compose.go":       "package secretguardcompose\n",
		"internal/core/runtime/service.go":                   "package runtime\n",
	}

	for rel, content := range adversarialFiles {
		abs := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// 1. Test via ScanRetiredPackages
	findings, err := ScanRetiredPackages(tmp)
	if err != nil {
		t.Fatalf("ScanRetiredPackages: %v", err)
	}

	expectedResurrections := map[string]bool{
		"internal/core/toolcallrepair/engine.go":                true,
		"internal/core/toolcallrepair/renamed_engine.go":        true,
		"internal/core/toolcallrepair/nested/sub/bypass.go":     true,
		"internal/core/toolcallrepair/repair/jsonshape/deep.go": true,
		"internal/core/secretguard/catalog.go":                  true,
		"internal/core/secretguard/renamed_catalog.go":          true,
		"internal/core/secretguard/nested/sub/bypass.go":        true,
		"internal/core/secretguard/engine/deep/guard.go":        true,
		"internal/core/compactiondetect/detector.go":            true,
		"internal/core/compactiondetect/renamed_detector.go":    true,
		"internal/core/compactiondetect/nested/sub/bypass.go":   true,
		"internal/core/compactiondetect/sub/deep/detector.go":   true,
	}

	detected := make(map[string]bool)
	for _, f := range findings {
		detected[f.Path] = true
		if !expectedResurrections[f.Path] {
			t.Errorf("unexpected finding for legitimate path %s: %v", f.Path, f)
		}
	}

	for exp := range expectedResurrections {
		if !detected[exp] {
			t.Errorf("expected adversarial resurrection %s to be detected by ScanRetiredPackages, but was missed", exp)
		}
	}

	if len(findings) != len(expectedResurrections) {
		t.Errorf("ScanRetiredPackages: got %d findings, want exactly %d", len(findings), len(expectedResurrections))
	}

	// 2. Test via direct WalkProductionGoFiles check
	retiredDirs := []string{
		"internal/core/toolcallrepair",
		"internal/core/secretguard",
		"internal/core/compactiondetect",
	}
	var walkViolations []string
	err = WalkProductionGoFiles(tmp, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		for _, retired := range retiredDirs {
			if pkg == retired || strings.HasPrefix(pkg, retired+"/") {
				walkViolations = append(walkViolations, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}

	walkDetected := make(map[string]bool)
	for _, v := range walkViolations {
		walkDetected[v] = true
		if !expectedResurrections[v] {
			t.Errorf("WalkProductionGoFiles unexpected violation for %s", v)
		}
	}
	for exp := range expectedResurrections {
		if !walkDetected[exp] {
			t.Errorf("expected adversarial resurrection %s to be detected by WalkProductionGoFiles, but was missed", exp)
		}
	}
	if len(walkViolations) != len(expectedResurrections) {
		t.Errorf("WalkProductionGoFiles: got %d violations, want exactly %d", len(walkViolations), len(expectedResurrections))
	}
}
