package archtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ExtensionPlanesBaselineRelPath is the repository-relative path to the extension planes baseline JSON artifact.
const ExtensionPlanesBaselineRelPath = "testdata/architecture/extension_planes_baseline.json"

// ExtensionPlaneManifestStatus captures declaration count, plane IDs, and generated-output currency.
type ExtensionPlaneManifestStatus struct {
	PlaneCount              int      `json:"plane_count"`
	PlaneIDs                []string `json:"plane_ids"`
	GeneratedOutputCurrency string   `json:"generated_output_currency"`
	IsGeneratedUpToDate     bool     `json:"is_generated_up_to_date"`
	ManifestPath            string   `json:"manifest_path"`
	GeneratedPath           string   `json:"generated_path"`
}

// WaveMirrorFamily contains measured mirror metrics for one migration wave family.
type WaveMirrorFamily struct {
	Wave            MigrationWave `json:"wave"`
	WaveName        string        `json:"wave_name"`
	FamilyName      string        `json:"family_name"`
	Description     string        `json:"description"`
	PlaneCount      int           `json:"plane_count"`
	Planes          []string      `json:"planes"`
	MeasuredMirrors int           `json:"measured_mirrors"`
	ActiveForbidden int           `json:"active_forbidden"`
}

// ExtensionPlanePathEntry classifies one repository path in the extension plane architecture.
type ExtensionPlanePathEntry struct {
	Path        string `json:"path"`
	Category    string `json:"category"` // "generated" or "hand_authored"
	Role        string `json:"role"`
	Description string `json:"description,omitempty"`
}

// ExtensionPlanesBaselineDocument is the serializable baseline artifact structure.
type ExtensionPlanesBaselineDocument struct {
	SchemaVersion          int                          `json:"schema_version"`
	Description            string                       `json:"description"`
	ActiveWave             string                       `json:"active_wave"`
	ActiveWaveOrdinal      int                          `json:"active_wave_ordinal"`
	TotalPlanes            int                          `json:"total_planes"`
	TotalRemainingMirrors  int                          `json:"total_remaining_mirrors"`
	ActiveForbiddenMirrors int                          `json:"active_forbidden_mirrors"`
	Manifest               ExtensionPlaneManifestStatus `json:"manifest"`
	WaveMeasurements       []WaveMirrorFamily           `json:"wave_measurements"`
	PathClassifications    ExtensionPlanePathManifest   `json:"path_classifications"`
}

// ExtensionPlanePathManifest holds segregated generated and hand-authored paths.
type ExtensionPlanePathManifest struct {
	Generated    []ExtensionPlanePathEntry `json:"generated"`
	HandAuthored []ExtensionPlanePathEntry `json:"hand_authored"`
}

// StandardWaveDefinitions returns the ordered metadata for all migration wave families.
func StandardWaveDefinitions() []WaveMirrorFamily {
	return []WaveMirrorFamily{
		{Wave: Wave0_Baseline, WaveName: "W0_Baseline", FamilyName: "Baseline", Description: "Baseline before wave migration (0 planes migrating)", PlaneCount: 0, Planes: []string{}},
		{Wave: Wave1_HookBus, WaveName: "W1_HookBus", FamilyName: "HookBus", Description: "Hook-bus family (submit, request-part, response-part, tool-reactors)", PlaneCount: 4, Planes: []string{"submit_hooks", "request_part_hooks", "response_part_hooks", "tool_reactors"}},
		{Wave: Wave2_Observers, WaveName: "W2_Observers", FamilyName: "Observers", Description: "Observer and telemetry family (traffic, usage, raw capture, redactors, stream observers)", PlaneCount: 5, Planes: []string{"traffic_observers", "usage_observers", "raw_capture_sinks", "traffic_redactors", "stream_observer_factories"}},
		{Wave: Wave3_RequestShaping, WaveName: "W3_RequestShaping", FamilyName: "RequestShaping", Description: "Request shaping family (transforms, pre-request, route hints, gates, attempts, sessions, workspaces)", PlaneCount: 7, Planes: []string{"request_transforms", "pre_request_handlers", "route_hint_providers", "completion_gates", "attempt_transforms", "session_openers", "workspace_resolvers"}},
		{Wave: Wave4_Tools, WaveName: "W4_Tools", FamilyName: "Tools", Description: "Tool execution and policy family (filters, policies, finalizers, arg limits)", PlaneCount: 4, Planes: []string{"tool_catalog_filters", "tool_call_policies", "tool_call_finalizers", "tool_call_finalization_max_args_bytes"}},
		{Wave: Wave5a_GuardsCompaction, WaveName: "W5a_GuardsCompaction", FamilyName: "GuardsCompaction", Description: "Secret guard and compaction family (guards, compaction observers, compaction preservers)", PlaneCount: 3, Planes: []string{"secret_guards", "compaction_observers", "compaction_preservers"}},
		{Wave: Wave5b_LocalTurnTerminal, WaveName: "W5b_LocalTurnTerminal", FamilyName: "LocalTurnTerminal", Description: "Local turn and terminal decision family (local turns, terminal decision provider)", PlaneCount: 2, Planes: []string{"local_turn_handlers", "terminal_decision_provider"}},
		{Wave: Wave5c_Residual, WaveName: "W5c_Residual", FamilyName: "Residual", Description: "Residual struct cleanups and change-surface verification", PlaneCount: 0, Planes: []string{}},
	}
}

// StandardExtensionPlanePaths returns standard classified generated and hand-authored paths.
func StandardExtensionPlanePaths() ExtensionPlanePathManifest {
	return ExtensionPlanePathManifest{
		Generated: []ExtensionPlanePathEntry{
			{Path: "pkg/lipsdk/feature/plane_generated.go", Category: "generated", Role: "Generated typed storage, dispatch, and metadata adapters", Description: "Emitted by generate-feature-planes.go from plane_manifest.go; zero reflection/unsafe"},
		},
		HandAuthored: []ExtensionPlanePathEntry{
			{Path: "pkg/lipsdk/feature/plane.go", Category: "hand_authored", Role: "Plane[T] contracts and descriptors", Description: "Core generic plane types, multiplicity, nil policy, and diagnostics descriptors"},
			{Path: "pkg/lipsdk/feature/plane_manifest.go", Category: "hand_authored", Role: "Single-site canonical plane declaration manifest", Description: "The single hand-authored declaration table defining all standard feature planes"},
			{Path: "pkg/lipsdk/feature/contributions.go", Category: "hand_authored", Role: "ContributionSet builder contract", Description: "Type-safe contribution accumulation with error attribution"},
			{Path: "pkg/lipsdk/feature/frozen.go", Category: "hand_authored", Role: "FrozenPlaneSet contracts and generic accessors", Description: "Immutable published snapshot store with package-level Get/Contribute"},
			{Path: "pkg/lipsdk/feature/bundle.go", Category: "hand_authored", Role: "FeatureBundle SDK integration", Description: "Plugin SDK bundle carrying ContributionSet and lifecycle registrations"},
			{Path: "internal/featurebundle/merge_surface.go", Category: "hand_authored", Role: "MergedFeatureSurface composition engine", Description: "Registration-order merge over catalog; conflict rejection and deterministic reduce"},
			{Path: "internal/infra/runtimebundle/build_extension.go", Category: "hand_authored", Role: "ExtensionsOptions composition", Description: "Generic projection of frozen candidate into executor options and host pseudo-contributions"},
			{Path: "internal/infra/runtimebundle/build_feature_hooks.go", Category: "hand_authored", Role: "Hook-bus view projection", Description: "Catalog-driven derivation of hooks.Config from frozen candidate"},
			{Path: "internal/infra/runtimebundle/generation_bundle.go", Category: "hand_authored", Role: "Generation operations freeze", Description: "Immutable generation operations holding FrozenPlaneSet"},
			{Path: "internal/core/extensions/snapshot.go", Category: "hand_authored", Role: "RequestRuntimeSnapshot freeze and stage accessors", Description: "Pinned request snapshot delegating stage reads to FrozenPlaneSet"},
			{Path: "internal/core/diag/inventory_extensions.go", Category: "hand_authored", Role: "Diagnostics inventory projection", Description: "Catalog-driven stage occupancy and privilege projection"},
			{Path: "internal/archtest/plane_rules.go", Category: "hand_authored", Role: "Architecture mirror ratchets and gates", Description: "AST scanner rejecting forbidden hand-authored mirror shapes past their migration wave"},
			{Path: "scripts/generate-feature-planes.go", Category: "hand_authored", Role: "Deterministic code generator for extension planes", Description: "Parses manifest, derives SDK imports, generates typed storage and dispatch adapters"},
		},
	}
}

// MeasureManifestStatus parses plane_manifest.go and checks generated output currency.
func MeasureManifestStatus(root string) (ExtensionPlaneManifestStatus, error) {
	manifestRel, generatedRel := filepath.Join("pkg", "lipsdk", "feature", "plane_manifest.go"), filepath.Join("pkg", "lipsdk", "feature", "plane_generated.go")
	manifestAbs, generatedAbs := filepath.Join(root, manifestRel), filepath.Join(root, generatedRel)

	manifestBytes, err := os.ReadFile(manifestAbs)
	if err != nil {
		return ExtensionPlaneManifestStatus{}, fmt.Errorf("read manifest: %w", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, manifestAbs, manifestBytes, parser.ParseComments)
	if err != nil {
		return ExtensionPlaneManifestStatus{}, fmt.Errorf("parse manifest: %w", err)
	}

	planeIDsSet := make(map[string]bool)
	ast.Inspect(f, func(n ast.Node) bool {
		if kv, ok := n.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "ID" {
				if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if id := strings.Trim(lit.Value, `"`); id != "" {
						planeIDsSet[id] = true
					}
				}
			}
		}
		return true
	})

	var planeIDs []string
	for id := range planeIDsSet {
		planeIDs = append(planeIDs, id)
	}
	slices.Sort(planeIDs)

	generatedBytes, err := os.ReadFile(generatedAbs)
	isUpToDate, currencyStr := false, "missing"
	if err == nil {
		expectedBytes, genErr := GenerateFeaturePlanesCode(manifestBytes)
		if genErr != nil {
			currencyStr = fmt.Sprintf("generation error: %v", genErr)
		} else if bytes.Equal(generatedBytes, expectedBytes) {
			isUpToDate, currencyStr = true, "up to date (in sync with manifest)"
		} else {
			currencyStr = "stale (generated output differs from manifest)"
		}
	} else if os.IsNotExist(err) {
		currencyStr = "missing (plane_generated.go not found)"
	} else {
		currencyStr = fmt.Sprintf("read error: %v", err)
	}

	return ExtensionPlaneManifestStatus{
		PlaneCount: len(planeIDs), PlaneIDs: planeIDs,
		GeneratedOutputCurrency: currencyStr, IsGeneratedUpToDate: isUpToDate,
		ManifestPath: filepath.ToSlash(manifestRel), GeneratedPath: filepath.ToSlash(generatedRel),
	}, nil
}

// MeasureWaveMirrors scans the repository and collects mirror counts for each wave family.
func MeasureWaveMirrors(root string) ([]WaveMirrorFamily, int, int, error) {
	waves := StandardWaveDefinitions()
	allFindings, err := ScanForbiddenMirrors(root, Wave5c_Residual)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("scan all mirrors: %w", err)
	}
	activeFindings, err := ScanForbiddenMirrors(root, ActiveMigrationWave)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("scan active mirrors: %w", err)
	}

	findingsByWave, activeByWave := make(map[MigrationWave][]MirrorFinding), make(map[MigrationWave][]MirrorFinding)
	for _, f := range allFindings {
		findingsByWave[f.Wave] = append(findingsByWave[f.Wave], f)
	}
	for _, f := range activeFindings {
		activeByWave[f.Wave] = append(activeByWave[f.Wave], f)
	}

	for i := range waves {
		w := waves[i].Wave
		waves[i].MeasuredMirrors = len(findingsByWave[w])
		waves[i].ActiveForbidden = len(activeByWave[w])
	}
	return waves, len(allFindings), len(activeFindings), nil
}

// FormatExtensionPlanesManifestSection renders the markdown section for manifest declaration & generation status.
func FormatExtensionPlanesManifestSection(root string) (string, error) {
	status, err := MeasureManifestStatus(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintln(&b, "## Extension plane declaration and generation status (Req 2, Req 8)")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Canonical declaration manifest: `%s`\n", status.ManifestPath)
	fmt.Fprintf(&b, "- Declared extension planes: **%d planes**\n", status.PlaneCount)
	fmt.Fprintf(&b, "- Generated typed adapters: `%s`\n", status.GeneratedPath)
	fmt.Fprintf(&b, "- Generated-output currency: **%s**\n", status.GeneratedOutputCurrency)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Plane ID | Wave Family | Multiplicity |")
	fmt.Fprintln(&b, "| --- | --- | --- |")

	for _, id := range status.PlaneIDs {
		waveFamily, multiplicity := "(unassigned)", "ordered"
		if id == "terminal_decision_provider" {
			multiplicity = "exclusive"
		}
		for _, w := range StandardWaveDefinitions() {
			if slices.Contains(w.Planes, id) {
				waveFamily = fmt.Sprintf("%s (%s)", w.WaveName, w.FamilyName)
				break
			}
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", id, waveFamily, multiplicity)
	}
	fmt.Fprintln(&b)
	return b.String(), nil
}

// FormatExtensionPlanesMirrorSection renders the markdown section for mirror measurements per wave.
func FormatExtensionPlanesMirrorSection(root string) (string, error) {
	waves, totalRemaining, activeForbidden, err := MeasureWaveMirrors(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintln(&b, "## Extension plane mirror measurements (Req 5, Req 8)")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Active migration wave: **%s** (baseline)\n", ActiveMigrationWave)
	fmt.Fprintf(&b, "- Active forbidden mirror violations: **%d** (zero-tolerance)\n", activeForbidden)
	fmt.Fprintf(&b, "- Total hand-authored mirror instances across repository: **%d**\n", totalRemaining)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Wave | Family / Description | Planes | Hand-Authored Mirrors | Active Forbidden |")
	fmt.Fprintln(&b, "| --- | --- | ---: | ---: | ---: |")
	for _, w := range waves {
		fmt.Fprintf(&b, "| `%s` | %s | %d | %d | %d |\n", w.WaveName, w.Description, w.PlaneCount, w.MeasuredMirrors, w.ActiveForbidden)
	}
	fmt.Fprintln(&b)
	return b.String(), nil
}

// FormatExtensionPlanesPathSection renders the markdown section for path classification.
func FormatExtensionPlanesPathSection() string {
	paths := StandardExtensionPlanePaths()
	var b strings.Builder
	fmt.Fprintln(&b, "## Extension plane path classification (Req 2.1, Req 3.1, Req 8.2)")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "### Generated paths (no manual edits permitted)")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Generated Path | Role | Description |")
	fmt.Fprintln(&b, "| --- | --- | --- |")
	for _, p := range paths.Generated {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", p.Path, p.Role, p.Description)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "### Hand-authored declaration and integration paths")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Integration Path | Role | Description |")
	fmt.Fprintln(&b, "| --- | --- | --- |")
	for _, p := range paths.HandAuthored {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", p.Path, p.Role, p.Description)
	}
	fmt.Fprintln(&b)
	return b.String()
}

// FormatExtensionPlanesReport renders all extension-plane architecture report sections.
func FormatExtensionPlanesReport(root string) (string, error) {
	var b strings.Builder
	manifestSec, err := FormatExtensionPlanesManifestSection(root)
	if err != nil {
		return "", err
	}
	b.WriteString(manifestSec)
	mirrorSec, err := FormatExtensionPlanesMirrorSection(root)
	if err != nil {
		return "", err
	}
	b.WriteString(mirrorSec)
	b.WriteString(FormatExtensionPlanesPathSection())
	return b.String(), nil
}

// BuildExtensionPlanesBaselineDocument constructs the baseline artifact data.
func BuildExtensionPlanesBaselineDocument(root string) (*ExtensionPlanesBaselineDocument, error) {
	manifest, err := MeasureManifestStatus(root)
	if err != nil {
		return nil, fmt.Errorf("measure manifest: %w", err)
	}
	waves, totalRemaining, activeForbidden, err := MeasureWaveMirrors(root)
	if err != nil {
		return nil, fmt.Errorf("measure mirrors: %w", err)
	}
	totalPlanes := 0
	for _, w := range waves {
		totalPlanes += w.PlaneCount
	}
	return &ExtensionPlanesBaselineDocument{
		SchemaVersion: 1, Description: "Extension plane declaration, generation currency, and progressive mirror migration baseline.",
		ActiveWave: ActiveMigrationWave.String(), ActiveWaveOrdinal: int(ActiveMigrationWave),
		TotalPlanes: totalPlanes, TotalRemainingMirrors: totalRemaining, ActiveForbiddenMirrors: activeForbidden,
		Manifest: manifest, WaveMeasurements: waves, PathClassifications: StandardExtensionPlanePaths(),
	}, nil
}

// GenerateExtensionPlanesBaselineJSON generates formatted baseline JSON for extension planes.
func GenerateExtensionPlanesBaselineJSON(root string) ([]byte, error) {
	doc, err := BuildExtensionPlanesBaselineDocument(root)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}

// WriteExtensionPlanesBaseline writes the baseline JSON artifact to testdata/architecture/extension_planes_baseline.json.
func WriteExtensionPlanesBaseline(root string) error {
	data, err := GenerateExtensionPlanesBaselineJSON(root)
	if err != nil {
		return fmt.Errorf("generate baseline json: %w", err)
	}
	outPath := filepath.Join(root, filepath.FromSlash(ExtensionPlanesBaselineRelPath))
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(outPath), err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write baseline file %s: %w", outPath, err)
	}
	return nil
}
