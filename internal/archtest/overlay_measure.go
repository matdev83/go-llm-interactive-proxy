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
// files, excluded from the legacy Req 11.5 convergence delta). Keep 25 lines of
// ratchet headroom over the measured 67-line overlay.
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

// AtomicOwnedResourceLifecycleOverlayMeasurement is the atomic-owned-resource-lifecycle allowance.
type AtomicOwnedResourceLifecycleOverlayMeasurement struct {
	Files []string
	Lines int
	Max   int
	Pass  bool
}

// MeasureGenericCompatibleBackendOverlay counts non-test lines for the
// generic-compatible-backend-modes feature, excluding connector overlay files.
func MeasureGenericCompatibleBackendOverlay(root string, exclude map[string]struct{}) (GenericCompatibleOverlayMeasurement, error) {
	base, err := measureOverlayByPathMarkers(root, genericCompatibleBackendOverlayPathMarkers, GenericCompatibleBackendOverlayMax, exclude)
	if err != nil {
		return GenericCompatibleOverlayMeasurement{}, err
	}
	return GenericCompatibleOverlayMeasurement(base), nil
}

// MeasureBillingHostCompositionOverlay counts non-test lines for the
// billing-host-composition feature, excluding connector and generic-compatible
// overlay files.
func MeasureBillingHostCompositionOverlay(root string, exclude map[string]struct{}) (BillingHostCompositionOverlayMeasurement, error) {
	base, err := measureOverlayByPathMarkers(root, billingHostCompositionOverlayPathMarkers, BillingHostCompositionOverlayMax, exclude)
	if err != nil {
		return BillingHostCompositionOverlayMeasurement{}, err
	}
	return BillingHostCompositionOverlayMeasurement(base), nil
}

// MeasureAtomicOwnedResourceLifecycleOverlay counts non-test lines for the
// atomic-owned-resource-lifecycle feature, excluding connector, generic-compatible,
// and billing-host-composition overlay files.
func MeasureAtomicOwnedResourceLifecycleOverlay(root string, exclude map[string]struct{}) (AtomicOwnedResourceLifecycleOverlayMeasurement, error) {
	base, err := measureOverlayByPathMarkers(root, atomicOwnedResourceLifecycleOverlayPathMarkers, AtomicOwnedResourceLifecycleOverlayMax, exclude)
	if err != nil {
		return AtomicOwnedResourceLifecycleOverlayMeasurement{}, err
	}
	return AtomicOwnedResourceLifecycleOverlayMeasurement(base), nil
}

func measureOverlayByPathMarkers(root string, pathMarkers []string, maxBytes int, exclude map[string]struct{}) (ConnectorOverlayMeasurement, error) {
	m := ConnectorOverlayMeasurement{Max: maxBytes}
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
		if exclude != nil {
			if _, skip := exclude[rel]; skip {
				return nil
			}
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
		return ConnectorOverlayMeasurement{}, err
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}
