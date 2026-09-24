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
// overlays). Keep 25 lines of ratchet headroom over the measured 340-line overlay.
// Usage-economics reconciliation extends ComposeBilling in billing_compose.go (revision
// workers/readers, cutover fence, unit ledger, pass-through settlement); the file stays
// with its original overlay (no marker churn). Re-measured 403, reset to 428 with
// 25 headroom.
const BillingHostCompositionOverlayMax = 428

// AtomicOwnedResourceLifecycleOverlayMax is the measured atomic-owned-resource-lifecycle
// overlay ratchet (the new private process-ownership and generation-loop primitive
// files, excluded from the legacy Req 11.5 convergence delta). Keep 16 lines of
// ratchet headroom over the measured 76-line overlay.
const AtomicOwnedResourceLifecycleOverlayMax = 92

// KeepwarmOrchestrationOverlayMax ratchets the generation/admin composition
// additions for this feature independently from the legacy convergence delta.
const KeepwarmOrchestrationOverlayMax = 650

// GeoIPIngressOverlayMax ratchets the early GeoIP ingress policy, resolver,
// process composition, and middleware-boundary additions independently from
// the historical convergence delta.
const GeoIPIngressOverlayMax = 700

// BackendResourcePoolOverlayMax is the measured backend-connector-resource-
// reconciliation overlay for the private resource-pool implementation. Keep
// 25 lines of ratchet headroom over the measured 356-line overlay.
const BackendResourcePoolOverlayMax = 381

// ReasoningSemanticCompressionOverlayMax is the measured reasoning semantic
// compression host-composition overlay. Keep 25 lines of ratchet headroom over
// the measured 341-line overlay.
const ReasoningSemanticCompressionOverlayMax = 366

// TerminalDecisionFeatureExtensionOverlayMax ratchets the provider-neutral
// policy endpoint files independently from the legacy convergence delta.
// The measured overlay is 597 lines; retain 25 lines of headroom.
const TerminalDecisionFeatureExtensionOverlayMax = 622

// LargePayloadHostCompositionOverlayMax is the measured large-payload streaming
// fast-path host-composition overlay. Keep 25 lines of ratchet headroom over
// the measured 266-line overlay.
const LargePayloadHostCompositionOverlayMax = 291

// UsageEconomicsOverlayMax caps the extensible usage-economics reconciliation
// growth allowance: only lines above each allowlisted file's locked baseline enter
// the allowance, so pre-existing baseline code can never enter. Seven files are new
// (baseline 0); production_options.go and process_billing.go carry approved economics
// composition growth above their merge-base lines. PR #659 adversarial repair
// (R1-R10) extends observation_economic_bridge.go; the allowance re-measured
// 1,396 lines, reset to 1,421 with 25 headroom.
const UsageEconomicsOverlayMax = 1421

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

var geoIPIngressOverlayPathMarkers = []string{
	"/stdhttp/geoip/",
	"/runtimebundle/geoip_process.go",
	"/stdhttp/middleware.go",
}

var reasoningSemanticCompressionOverlayPathMarkers = []string{
	"/reasoning_preservation_compression",
	"/lipruntime/reasoning_compression.go",
}

var terminalDecisionFeatureExtensionOverlayPathMarkers = []string{
	"/runtimebundle/terminal_policy_http.go",
	"/stdhttp/terminal_decision_policy_mount.go",
	"/stdhttp/terminalpolicy/handler.go",
	"/stdhttp/contract/terminal_decision_policy_input.go",
}

var largePayloadHostCompositionOverlayPathMarkers = []string{
	"/runtimebundle/build_large_body_assessor.go",
	"/stdhttp/contract/large_payload_input.go",
}

// usageEconomicsGrowthFile is one allowlisted production file with its locked
// merge-base line count. Only lines above the baseline enter the allowance;
// baseline lines can never enter, and deletions only shrink the credit.
type usageEconomicsGrowthFile struct {
	path     string
	baseline int
}

// usageEconomicsGrowthFiles is the single allowlist of economics growth surfaces.
// All paths live inside the Req 11.5 convergence surfaces and outside every other
// overlay; the drift test pins this table exactly, so broadening requires an
// explicit table edit that review must approve.
var usageEconomicsGrowthFiles = []usageEconomicsGrowthFile{
	{path: "internal/infra/runtimebundle/accounting_recovery.go", baseline: 0},
	{path: "internal/infra/runtimebundle/external_billing.go", baseline: 0},
	{path: "internal/infra/runtimebundle/observation_economic_bridge.go", baseline: 0},
	{path: "internal/infra/runtimebundle/operator_reports.go", baseline: 0},
	{path: "internal/infra/runtimebundle/shadow_v2_compose.go", baseline: 0},
	{path: "internal/stdhttp/admin/billing/operator.go", baseline: 0},
	{path: "pkg/lipruntime/billing.go", baseline: 0},
	{path: "internal/infra/runtimebundle/production_options.go", baseline: 80},
	{path: "internal/infra/runtimebundle/process_billing.go", baseline: 75},
}

// measureUsageEconomicsGrowthOverlay credits only per-file growth above the locked
// baselines. Files already claimed by another overlay are skipped (never double
// counted; the disjointness test flags any such overlap loudly). Missing files
// credit zero, so deletions always pass.
func measureUsageEconomicsGrowthOverlay(root string, exclude map[string]struct{}) (OverlayMeasurement, error) {
	m := OverlayMeasurement{Name: "Usage economics", Max: UsageEconomicsOverlayMax}
	for _, f := range usageEconomicsGrowthFiles {
		if _, skip := exclude[f.path]; skip {
			continue
		}
		n, err := countTreeFileLines(filepath.Join(root, filepath.FromSlash(f.path)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return OverlayMeasurement{}, err
		}
		if credit := n - f.baseline; credit > 0 {
			m.Lines += credit
			m.Files = append(m.Files, f.path)
		}
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
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
	{name: "GeoIP ingress", max: GeoIPIngressOverlayMax, markers: geoIPIngressOverlayPathMarkers},
	{name: "Reasoning semantic compression", max: ReasoningSemanticCompressionOverlayMax, markers: reasoningSemanticCompressionOverlayPathMarkers},
	{name: "Terminal decision feature extension", max: TerminalDecisionFeatureExtensionOverlayMax, markers: terminalDecisionFeatureExtensionOverlayPathMarkers},
	{name: "Large payload host composition", max: LargePayloadHostCompositionOverlayMax, markers: largePayloadHostCompositionOverlayPathMarkers},
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
