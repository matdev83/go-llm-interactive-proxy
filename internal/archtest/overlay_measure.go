package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GenericCompatibleBackendOverlayMax is the measured generic-compatible-backend-modes
// overlay ratchet (production files selected by path, excluding connector overlay).
// Keep 25 lines of ratchet headroom over the measured 921-line overlay.
const GenericCompatibleBackendOverlayMax = 946

// BillingHostCompositionOverlayMax is the measured billing-host-composition overlay
// ratchet (production files selected by path, excluding connector and generic-compatible
// overlays). Keep 25 lines of ratchet headroom over the measured 310-line overlay.
const BillingHostCompositionOverlayMax = 335

// AtomicOwnedResourceLifecycleOverlayMax is the measured atomic-owned-resource-lifecycle
// overlay ratchet (the new private process-ownership and generation-loop primitive
// files, excluded from the legacy Req 11.5 convergence delta). Keep 16 lines of
// ratchet headroom over the measured 76-line overlay.
const AtomicOwnedResourceLifecycleOverlayMax = 92

var genericCompatibleBackendOverlayPathMarkers = []string{
	"/core/concurrencyauthority/compatible/",
	"/compatible_admission.go",
	"/compatible_ownership.go",
	"/validate_structural.go",
	"/inventory_live.go",
	"/compatible_admission_limits.go",
}

// billingHostCompositionOverlayPathMarkers selects the new production files the
// billing-host-composition feature adds to the convergence surfaces.
var billingHostCompositionOverlayPathMarkers = []string{
	"/billing_compose.go",
	"/admin/billing/commands.go",
}

// atomicOwnedResourceLifecycleOverlayPathMarkers selects the new private
// ownership-primitive production files the atomic-owned-resource-lifecycle
// feature adds to the convergence surfaces.
var atomicOwnedResourceLifecycleOverlayPathMarkers = []string{
	"/process_owner.go",
	"/generation_loop.go",
}

// pathMarkerOverlaySpec is one path-marker overlay allowance: a feature's new
// production files are selected by path and ratcheted separately from the legacy
// Req 11.5 convergence delta.
type pathMarkerOverlaySpec struct {
	name    string
	max     int
	markers []string
}

// pathMarkerOverlaySpecs is the single table of path-marker overlays. Adding a
// feature here requires no changes to the measurement, formatting, or pass logic.
var pathMarkerOverlaySpecs = []pathMarkerOverlaySpec{
	{name: "Generic compatible", max: GenericCompatibleBackendOverlayMax, markers: genericCompatibleBackendOverlayPathMarkers},
	{name: "Billing host composition", max: BillingHostCompositionOverlayMax, markers: billingHostCompositionOverlayPathMarkers},
	{name: "Atomic owned resource lifecycle", max: AtomicOwnedResourceLifecycleOverlayMax, markers: atomicOwnedResourceLifecycleOverlayPathMarkers},
}

// measurePathMarkerOverlays measures every path-marker overlay in table order.
// priorFiles (the connector overlay's files) seed the exclusion set so a file is
// never counted by two overlays.
func measurePathMarkerOverlays(root string, priorFiles []string) ([]OverlayMeasurement, error) {
	exclude := make(map[string]struct{}, len(priorFiles))
	for _, f := range priorFiles {
		exclude[f] = struct{}{}
	}
	out := make([]OverlayMeasurement, 0, len(pathMarkerOverlaySpecs))
	for _, spec := range pathMarkerOverlaySpecs {
		base, err := measureOverlayByPathMarkers(root, spec.markers, spec.max, exclude)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.name, err)
		}
		out = append(out, OverlayMeasurement{
			Name:  spec.name,
			Files: base.Files,
			Lines: base.Lines,
			Max:   base.Max,
			Pass:  base.Pass,
		})
		for _, f := range base.Files {
			exclude[f] = struct{}{}
		}
	}
	return out, nil
}

func measureOverlayByPathMarkers(root string, pathMarkers []string, maxBytes int, exclude map[string]struct{}) (OverlayMeasurement, error) {
	m := OverlayMeasurement{Max: maxBytes}
	// Preserve the root-wide metric semantics, but do not descend into sibling
	// worktrees. The main checkout may contain hundreds of megabytes of agent
	// worktrees here.
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".worktrees" {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if _, skip := exclude[rel]; skip {
			return nil
		}
		hit := false
		for _, marker := range pathMarkers {
			if strings.Contains(rel, marker) {
				hit = true
				break
			}
		}
		if !hit {
			return nil
		}
		n, err := countTreeFileLines(path)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, rel)
		m.Lines += n
		return nil
	})
	if err != nil {
		return OverlayMeasurement{}, err
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}
