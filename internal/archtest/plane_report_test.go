package archtest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExtensionPlanesManifestStatus(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	status, err := MeasureManifestStatus(root)
	if err != nil {
		t.Fatalf("MeasureManifestStatus failed: %v", err)
	}

	if status.PlaneCount != 25 {
		t.Fatalf("expected 25 declared planes, got %d", status.PlaneCount)
	}

	if !status.IsGeneratedUpToDate {
		t.Fatalf("expected plane_generated.go to be up to date, got: %s", status.GeneratedOutputCurrency)
	}

	// Verify essential planes are present in manifest
	expectedPlanes := []string{
		"submit_hooks",
		"request_part_hooks",
		"response_part_hooks",
		"tool_reactors",
		"traffic_observers",
		"request_transforms",
		"tool_catalog_filters",
		"secret_guards",
		"terminal_decision_provider",
	}
	for _, expected := range expectedPlanes {
		if !slices.Contains(status.PlaneIDs, expected) {
			t.Errorf("expected plane %q in manifest status plane IDs, got %v", expected, status.PlaneIDs)
		}
	}
}

func TestExtensionPlanesMirrorMeasurements(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	waves, totalRemaining, activeForbidden, err := MeasureWaveMirrors(root)
	if err != nil {
		t.Fatalf("MeasureWaveMirrors failed: %v", err)
	}

	if activeForbidden != 0 {
		t.Fatalf("expected 0 active forbidden mirrors at Wave0 baseline, got %d", activeForbidden)
	}

	if totalRemaining == 0 {
		t.Fatalf("expected non-zero hand-authored mirrors before migration waves, got 0")
	}

	if len(waves) != 8 {
		t.Fatalf("expected 8 wave definitions (W0-W5c), got %d", len(waves))
	}

	// Verify Wave 0 has 0 active forbidden
	if waves[0].ActiveForbidden != 0 {
		t.Errorf("Wave 0 active forbidden = %d, expected 0", waves[0].ActiveForbidden)
	}

	// Verify Wave 1 has 4 planes (excluding tool_reactor_error_policy)
	if waves[1].PlaneCount != 4 {
		t.Errorf("Wave 1 plane count = %d, expected 4", waves[1].PlaneCount)
	}
}

func TestExtensionPlanesPathClassifications(t *testing.T) {
	t.Parallel()
	paths := StandardExtensionPlanePaths()

	if len(paths.Generated) != 1 {
		t.Fatalf("expected exactly 1 generated path (plane_generated.go), got %d", len(paths.Generated))
	}
	if paths.Generated[0].Path != "pkg/lipsdk/feature/plane_generated.go" {
		t.Errorf("expected generated path to be pkg/lipsdk/feature/plane_generated.go, got %s", paths.Generated[0].Path)
	}
	if len(paths.HandAuthored) == 0 {
		t.Fatalf("expected hand-authored paths to be populated")
	}

	// Verify generated paths have generated category
	for _, p := range paths.Generated {
		if p.Category != "generated" {
			t.Errorf("expected category 'generated' for %s, got %s", p.Path, p.Category)
		}
	}

	// Verify hand-authored paths have hand_authored category
	for _, p := range paths.HandAuthored {
		if p.Category != "hand_authored" {
			t.Errorf("expected category 'hand_authored' for %s, got %s", p.Path, p.Category)
		}
	}

	// Verify scripts/generate-feature-planes.go is in hand_authored
	foundGenerator := false
	for _, p := range paths.HandAuthored {
		if p.Path == "scripts/generate-feature-planes.go" {
			foundGenerator = true
			break
		}
	}
	if !foundGenerator {
		t.Errorf("expected scripts/generate-feature-planes.go in hand_authored paths")
	}
}

func TestExtensionPlanesReportFormatting(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	section, err := FormatExtensionPlanesReport(root)
	if err != nil {
		t.Fatalf("FormatExtensionPlanesReport failed: %v", err)
	}

	// Verify key headers and tables exist
	requiredSubstrings := []string{
		"## Extension plane declaration and generation status (Req 2, Req 8)",
		"Declared extension planes:",
		"Generated-output currency:",
		"## Extension plane mirror measurements (Req 5, Req 8)",
		"Active migration wave:",
		"Active forbidden mirror violations:",
		"## Extension plane path classification (Req 2.1, Req 3.1, Req 8.2)",
		"### Generated paths (no manual edits permitted)",
		"### Hand-authored declaration and integration paths",
		"`pkg/lipsdk/feature/plane_generated.go`",
		"`pkg/lipsdk/feature/plane_manifest.go`",
		"`scripts/generate-feature-planes.go`",
	}

	for _, req := range requiredSubstrings {
		if !strings.Contains(section, req) {
			t.Errorf("report output missing required substring: %q", req)
		}
	}
}

func TestExtensionPlanesBaselineGeneration_Determinism(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	json1, err := GenerateExtensionPlanesBaselineJSON(root)
	if err != nil {
		t.Fatalf("GenerateExtensionPlanesBaselineJSON run 1 failed: %v", err)
	}

	json2, err := GenerateExtensionPlanesBaselineJSON(root)
	if err != nil {
		t.Fatalf("GenerateExtensionPlanesBaselineJSON run 2 failed: %v", err)
	}

	if !bytes.Equal(json1, json2) {
		t.Fatalf("baseline JSON generation is not deterministic between two runs")
	}

	var doc ExtensionPlanesBaselineDocument
	if err := json.Unmarshal(json1, &doc); err != nil {
		t.Fatalf("unmarshal generated baseline JSON failed: %v", err)
	}

	if doc.SchemaVersion != 1 {
		t.Errorf("expected schema_version 1, got %d", doc.SchemaVersion)
	}
	if doc.TotalPlanes != 25 {
		t.Errorf("expected total_planes 25, got %d", doc.TotalPlanes)
	}
	if doc.ActiveForbiddenMirrors != 0 {
		t.Errorf("expected 0 active forbidden mirrors, got %d", doc.ActiveForbiddenMirrors)
	}
}

func TestExtensionPlanesBaselineArtifact_MatchesDisk(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	baselinePath := filepath.Join(root, filepath.FromSlash(ExtensionPlanesBaselineRelPath))
	data, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read baseline file %s failed: %v", baselinePath, err)
	}

	expected, err := GenerateExtensionPlanesBaselineJSON(root)
	if err != nil {
		t.Fatalf("GenerateExtensionPlanesBaselineJSON failed: %v", err)
	}
	expected = append(expected, '\n')

	if !bytes.Equal(data, expected) {
		t.Errorf("extension_planes_baseline.json on disk differs from generated baseline; run 'make arch-report' to update")
	}

	var doc ExtensionPlanesBaselineDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("invalid JSON in baseline file: %v", err)
	}

	if doc.SchemaVersion != 1 {
		t.Errorf("expected schema_version 1, got %d", doc.SchemaVersion)
	}
	if doc.TotalPlanes != 25 {
		t.Errorf("expected total_planes 25, got %d", doc.TotalPlanes)
	}
	if doc.Manifest.PlaneCount != 25 {
		t.Errorf("expected manifest plane_count 25, got %d", doc.Manifest.PlaneCount)
	}
	if !doc.Manifest.IsGeneratedUpToDate {
		t.Errorf("expected is_generated_up_to_date to be true, got %v (%s)", doc.Manifest.IsGeneratedUpToDate, doc.Manifest.GeneratedOutputCurrency)
	}
	if len(doc.PathClassifications.Generated) != 1 {
		t.Errorf("expected exactly 1 generated path, got %d", len(doc.PathClassifications.Generated))
	}
}

func TestMeasureManifestStatus_StaleDetection(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	// Create temp directory structure mimicking repo
	tmpDir := t.TempDir()
	manifestDir := filepath.Join(tmpDir, "pkg", "lipsdk", "feature")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir temp dir failed: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(root, "pkg", "lipsdk", "feature", "plane_manifest.go"))
	if err != nil {
		t.Fatalf("read real manifest failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(manifestDir, "plane_manifest.go"), manifestBytes, 0o644); err != nil {
		t.Fatalf("write temp manifest failed: %v", err)
	}

	// 1. Missing generated file
	statusMissing, err := MeasureManifestStatus(tmpDir)
	if err != nil {
		t.Fatalf("MeasureManifestStatus on missing generated file failed: %v", err)
	}
	if statusMissing.IsGeneratedUpToDate {
		t.Errorf("expected IsGeneratedUpToDate false for missing generated file")
	}
	if !strings.Contains(statusMissing.GeneratedOutputCurrency, "missing") {
		t.Errorf("expected missing status currency, got %q", statusMissing.GeneratedOutputCurrency)
	}

	// 2. Stale generated file (corrupted content)
	if err := os.WriteFile(filepath.Join(manifestDir, "plane_generated.go"), []byte("// corrupted"), 0o644); err != nil {
		t.Fatalf("write corrupted generated file failed: %v", err)
	}
	statusStale, err := MeasureManifestStatus(tmpDir)
	if err != nil {
		t.Fatalf("MeasureManifestStatus on stale generated file failed: %v", err)
	}
	if statusStale.IsGeneratedUpToDate {
		t.Errorf("expected IsGeneratedUpToDate false for stale generated file")
	}
	if !strings.Contains(statusStale.GeneratedOutputCurrency, "stale") {
		t.Errorf("expected stale status currency, got %q", statusStale.GeneratedOutputCurrency)
	}

	// 3. Up-to-date generated file
	generatedBytes, err := GenerateFeaturePlanesCode(manifestBytes)
	if err != nil {
		t.Fatalf("GenerateFeaturePlanesCode failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plane_generated.go"), generatedBytes, 0o644); err != nil {
		t.Fatalf("write valid generated file failed: %v", err)
	}
	statusValid, err := MeasureManifestStatus(tmpDir)
	if err != nil {
		t.Fatalf("MeasureManifestStatus on valid generated file failed: %v", err)
	}
	if !statusValid.IsGeneratedUpToDate {
		t.Errorf("expected IsGeneratedUpToDate true for valid generated file, got %v (%s)", statusValid.IsGeneratedUpToDate, statusValid.GeneratedOutputCurrency)
	}
}
