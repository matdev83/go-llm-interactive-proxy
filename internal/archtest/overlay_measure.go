package archtest

import (
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

// KeepwarmOrchestrationOverlayMax ratchets the generation/admin composition
// additions for this feature independently from the legacy convergence delta.
const KeepwarmOrchestrationOverlayMax = 650

// BackendResourcePoolOverlayMax is the measured backend-connector-resource-
// reconciliation overlay for the private resource-pool implementation. Keep
// 25 lines of ratchet headroom over the measured 356-line overlay.
const BackendResourcePoolOverlayMax = 381

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

var keepwarmOrchestrationOverlayPathMarkers = []string{
	"/keepwarm_generation.go",
	"/keepwarm_http.go",
	"/generation_bundle.go",
	"/admin/keepwarm/handler.go",
}

var backendResourcePoolOverlayPathMarkers = []string{
	"/backend_resource_pool.go",
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
	{name: "Keep-warm orchestration", max: KeepwarmOrchestrationOverlayMax, markers: keepwarmOrchestrationOverlayPathMarkers},
	{name: "Backend resource pool", max: BackendResourcePoolOverlayMax, markers: backendResourcePoolOverlayPathMarkers},
}

// measurePathMarkerOverlays measures every path-marker overlay in table order.
// priorFiles (the connector overlay's files) seed the exclusion set so a file is
// never counted by two overlays. A single root walk evaluates all table specs.
func measurePathMarkerOverlays(root string, priorFiles []string) ([]OverlayMeasurement, error) {
	exclude := make(map[string]struct{}, len(priorFiles))
	for _, f := range priorFiles {
		exclude[f] = struct{}{}
	}
	measurements := make([]OverlayMeasurement, len(pathMarkerOverlaySpecs))
	for i, spec := range pathMarkerOverlaySpecs {
		measurements[i] = OverlayMeasurement{
			Name: spec.name,
			Max:  spec.max,
		}
	}

	// Preserve root-wide metric semantics, but do not descend into sibling worktrees.
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
		// Match in table order; first matching overlay claims the file.
		for i, spec := range pathMarkerOverlaySpecs {
			hit := false
			for _, marker := range spec.markers {
				if strings.Contains(rel, marker) {
					hit = true
					break
				}
			}
			if hit {
				n, err := countTreeFileLines(path)
				if err != nil {
					return err
				}
				measurements[i].Files = append(measurements[i].Files, rel)
				measurements[i].Lines += n
				exclude[rel] = struct{}{}
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range measurements {
		sort.Strings(measurements[i].Files)
		measurements[i].Pass = measurements[i].Lines <= measurements[i].Max
	}
	return measurements, nil
}
