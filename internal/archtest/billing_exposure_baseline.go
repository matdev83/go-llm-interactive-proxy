package archtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BillingExposureBaselineRelPath is the machine-checkable 0.1 LOC/deletion lock.
const BillingExposureBaselineRelPath = "internal/archtest/testdata/architecture/billing_exposure_deletion_baseline.json"

// BillingExposureBaselineSchemaVersion is the JSON schema for the 0.1 freeze.
const BillingExposureBaselineSchemaVersion = 1

// BillingExposureSurface describes one production billing-convergence surface.
type BillingExposureSurface struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"` // package or glob
	Path          string `json:"path"`
	Match         string `json:"match,omitempty"`
	BaselineLines int    `json:"baseline_lines"`
}

// BillingExposureDeletionTarget records a brownfield symbol/file/schema that
// later phases must retire. Task 0.1 records present=true. Task 0.4 evaluates
// the same inventory as planned until ForbidHoldSymbols is true (task 7.1).
type BillingExposureDeletionTarget struct {
	ID                  string   `json:"id"`
	Kind                string   `json:"kind"` // type, const, func, ident, schema
	Package             string   `json:"package,omitempty"`
	Name                string   `json:"name,omitempty"`
	Files               []string `json:"files,omitempty"`
	Marker              string   `json:"marker,omitempty"`
	LegacyRecoveryFiles []string `json:"legacy_recovery_files,omitempty"`
	Present             bool     `json:"present"`
	Status              string   `json:"status"`
	Reason              string   `json:"reason"`
}

// BillingExposurePlannedRatchet is an end-state architecture forbid. Status
// "planned" asserts inventory until the named activation flag is flipped.
type BillingExposurePlannedRatchet struct {
	ID                     string   `json:"id"`
	Status                 string   `json:"status"` // planned | active
	ActivationFlag         string   `json:"activation_flag,omitempty"`
	ActivationTask         string   `json:"activation_task"`
	Requirements           []string `json:"requirements"`
	DeletionTargetIDs      []string `json:"deletion_target_ids,omitempty"`
	Files                  []string `json:"files,omitempty"`
	CurrentMarkers         []string `json:"current_markers,omitempty"`
	ForbiddenWhenActivated []string `json:"forbidden_when_activated,omitempty"`
	RequiredWhenActivated  []string `json:"required_when_activated,omitempty"`
	EndState               string   `json:"end_state"`
}

// BillingExposureBaselineFile is the committed inventory plus activated final ratchets.
type BillingExposureBaselineFile struct {
	SchemaVersion          int                             `json:"schema_version"`
	Feature                string                          `json:"feature"`
	Phase                  string                          `json:"phase"`
	RequireNetLOCReduction bool                            `json:"require_net_loc_reduction"`
	ForbidHoldSymbols      bool                            `json:"forbid_hold_symbols"`
	AuthoritativeFlow      string                          `json:"authoritative_flow"`
	CurrentFlow            string                          `json:"current_flow"`
	TargetFlow             string                          `json:"target_flow"`
	BaselineTotal          int                             `json:"baseline_total"`
	Surfaces               []BillingExposureSurface        `json:"surfaces"`
	DeletionTargets        []BillingExposureDeletionTarget `json:"deletion_targets"`
	PlannedRatchets        []BillingExposurePlannedRatchet `json:"planned_ratchets"`
}

// BillingExposureSurfaceMeasurement is one surface's current production LOC.
type BillingExposureSurfaceMeasurement struct {
	ID           string
	Kind         string
	Path         string
	Match        string
	CurrentLines int
}

// BillingExposureMeasurement is the live count for the locked surface set.
type BillingExposureMeasurement struct {
	Surfaces []BillingExposureSurfaceMeasurement
	Total    int
}

// DecodeBillingExposureBaseline parses a billing-exposure baseline document.
func DecodeBillingExposureBaseline(raw []byte) (BillingExposureBaselineFile, error) {
	var doc BillingExposureBaselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return BillingExposureBaselineFile{}, fmt.Errorf("decode billing exposure baseline: %w", err)
	}
	return doc, nil
}

// LoadBillingExposureBaseline reads the committed 0.1 JSON lock.
func LoadBillingExposureBaseline(root string) (BillingExposureBaselineFile, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(BillingExposureBaselineRelPath)))
	if err != nil {
		return BillingExposureBaselineFile{}, err
	}
	return DecodeBillingExposureBaseline(raw)
}

// MeasureBillingExposureBaseline recounts production lines for every locked surface.
func MeasureBillingExposureBaseline(root string) (BillingExposureMeasurement, error) {
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		return BillingExposureMeasurement{}, err
	}
	out := BillingExposureMeasurement{
		Surfaces: make([]BillingExposureSurfaceMeasurement, 0, len(doc.Surfaces)),
	}
	for _, surface := range doc.Surfaces {
		n, err := CountBillingExposureSurfaceLines(root, surface)
		if err != nil {
			return BillingExposureMeasurement{}, fmt.Errorf("%s: %w", surface.ID, err)
		}
		out.Surfaces = append(out.Surfaces, BillingExposureSurfaceMeasurement{
			ID:           surface.ID,
			Kind:         surface.Kind,
			Path:         surface.Path,
			Match:        surface.Match,
			CurrentLines: n,
		})
		out.Total += n
	}
	return out, nil
}

// CountBillingExposureSurfaceLines counts non-test production lines for one surface.
func CountBillingExposureSurfaceLines(root string, surface BillingExposureSurface) (int, error) {
	switch surface.Kind {
	case "package":
		return CountBillingExposurePackageLines(root, surface.Path)
	case "glob":
		return countBillingExposureGlobLines(root, surface.Path, surface.Match)
	default:
		return 0, fmt.Errorf("unknown surface kind %q", surface.Kind)
	}
}

// CountBillingExposurePackageLines counts non-test .go lines under rel, skipping
// .worktrees, vendor, testdata, and test files.
func CountBillingExposurePackageLines(root, rel string) (int, error) {
	dir := filepath.Join(root, filepath.FromSlash(rel))
	var total int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".worktrees", "vendor", "testdata", "node_modules":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Timestamped Bun migrations are immutable historical DDL. They are
		// retained as explicit migration evidence and are not current runtime
		// production surfaces for the activated convergence LOC gate.
		if isBillingFinalConvergenceMigrationName(info.Name()) {
			return nil
		}
		n, err := CountFileLines(path)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	return total, err
}

func countBillingExposureGlobLines(root, rel, match string) (int, error) {
	if match == "" {
		return 0, fmt.Errorf("glob surface %s requires match", rel)
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var total int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if isBillingFinalConvergenceMigrationName(name) {
			continue
		}
		ok, err := filepath.Match(match, name)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		n, err := CountFileLines(filepath.Join(dir, name))
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// BillingExposureDeletionTargetPresent reports whether a recorded brownfield
// target still exists in production source.
func BillingExposureDeletionTargetPresent(root string, target BillingExposureDeletionTarget) (bool, error) {
	switch target.Kind {
	case "type", "const", "func":
		return billingExposureDeclPresent(root, target)
	case "ident", "schema":
		return billingExposureMarkerPresent(root, target)
	default:
		return false, fmt.Errorf("unknown deletion target kind %q", target.Kind)
	}
}

func billingExposureDeclPresent(root string, target BillingExposureDeletionTarget) (bool, error) {
	if target.Package == "" || target.Name == "" {
		return false, fmt.Errorf("%s: package and name are required", target.ID)
	}
	kind := SymbolType
	switch target.Kind {
	case "const":
		kind = SymbolConst
	case "func":
		kind = SymbolFunc
	}
	rule := ForbiddenDeclRule{Package: target.Package, Kind: kind, Name: target.Name}
	dir := filepath.Join(root, filepath.FromSlash(target.Package))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		abs := filepath.Join(dir, entry.Name())
		src, err := os.ReadFile(abs)
		if err != nil {
			return false, err
		}
		_, file, err := ParseGoSource(abs, src)
		if err != nil {
			return false, err
		}
		if DeclExists(file, rule) {
			return true, nil
		}
	}
	return false, nil
}

func billingExposureMarkerPresent(root string, target BillingExposureDeletionTarget) (bool, error) {
	if target.Marker == "" || len(target.Files) == 0 {
		return false, fmt.Errorf("%s: files and marker are required", target.ID)
	}
	files, err := billingExposureMarkerScanFiles(root, target)
	if err != nil {
		return false, err
	}
	allow := make(map[string]struct{}, len(target.LegacyRecoveryFiles))
	for _, rel := range target.LegacyRecoveryFiles {
		allow[filepath.ToSlash(rel)] = struct{}{}
	}
	for _, rel := range files {
		rel = filepath.ToSlash(rel)
		if _, ok := allow[rel]; ok {
			continue
		}
		if target.Kind == "schema" && isBillingExposureMigrationFile(rel) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if strings.Contains(string(src), target.Marker) {
			return true, nil
		}
	}
	return false, nil
}

// billingExposureMarkerScanFiles expands schema targets that only list migration
// DDL so activated forbids still inspect production package ownership.
func billingExposureMarkerScanFiles(root string, target BillingExposureDeletionTarget) ([]string, error) {
	nonMigration := make([]string, 0, len(target.Files))
	var packageDir string
	for _, rel := range target.Files {
		rel = filepath.ToSlash(rel)
		if target.Kind == "schema" && isBillingExposureMigrationFile(rel) {
			if packageDir == "" {
				packageDir = pathDirSlash(rel)
			}
			continue
		}
		nonMigration = append(nonMigration, rel)
	}
	if target.Kind != "schema" || len(nonMigration) > 0 || packageDir == "" {
		if len(nonMigration) == 0 {
			return target.Files, nil
		}
		return nonMigration, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(packageDir)))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(packageDir, name))
		if isBillingExposureMigrationFile(rel) {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}

func pathDirSlash(rel string) string {
	dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	if dir == "." {
		return ""
	}
	return dir
}

func isBillingExposureMigrationFile(rel string) bool {
	base := filepath.Base(filepath.FromSlash(rel))
	if len(base) < 14 {
		return false
	}
	for _, r := range base[:14] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
