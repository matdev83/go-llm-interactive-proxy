package archtest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CriticalFileBudget caps the non-test line count for hotspot files.
type CriticalFileBudget struct {
	Path string
	Max  int
}

// CriticalFileBudgets is the single source of truth for hotspot ceilings
// (guardrails tests + make arch-report). Values are measured ratchets + 25 lines headroom.
var CriticalFileBudgets = []CriticalFileBudget{
	{Path: "internal/core/runtime/executor.go", Max: 249},
	{Path: "internal/infra/runtimebundle/options.go", Max: 253},
	{Path: "internal/standardplugins/standard_table.go", Max: 211},
	{Path: "internal/pluginreg/reg.go", Max: 452},
	{Path: "internal/stdhttp/server.go", Max: 33},
	{Path: "internal/infra/runtimehost/coordinator.go", Max: 317},
	{Path: "internal/infra/runtimehost/generation.go", Max: 341},
	{Path: "internal/infra/runtimebundle/candidate_compile.go", Max: 284},
	{Path: "internal/infra/runtimebundle/handler_composer.go", Max: 50},
	// Reasoning compression adds one generation-local auxiliary binding before
	// feature merge; measured 360 after extraction, retain 25-line headroom.
	{Path: "internal/infra/runtimebundle/compile_generation.go", Max: 388},
	{Path: "internal/stdhttp/request_plane.go", Max: 90},
	{Path: "internal/infra/runtimebundle/process_services.go", Max: 317},
	{Path: "pkg/lipruntime/build.go", Max: 121},
	{Path: "pkg/lipruntime/host.go", Max: 93},
	{Path: "pkg/lipruntime/facade.go", Max: 97},
	{Path: "cmd/lipstd/command.go", Max: 458},
	{Path: "pkg/lipruntime/reload.go", Max: 114},
	{Path: "pkg/lipruntime/reload_aliases.go", Max: 60},
	// Previously unbudgeted 1k+ hotspots, split by concern; ratchet measured+25.
	{Path: "pkg/lipsdk/backendplugin/convert.go", Max: 422},
	{Path: "pkg/lipsdk/backendplugin/convert_frames.go", Max: 517},
	{Path: "internal/core/securesession/adapters/bunstore/store.go", Max: 341},
	{Path: "internal/core/securesession/adapters/bunstore/store_evidence.go", Max: 307},
	{Path: "internal/core/runtime/authority_lifecycle.go", Max: 336},
	{Path: "internal/core/runtime/authority_lifecycle_settle.go", Max: 439},
	{Path: "internal/core/runtime/authority_lifecycle_release.go", Max: 355},
	{Path: "internal/plugins/protocols/openresponses/state_machine.go", Max: 708},
	{Path: "internal/plugins/protocols/openresponses/state_machine_event_handlers.go", Max: 515},
	{Path: "internal/plugins/frontends/frontendpipe/pipe.go", Max: 383},
	{Path: "internal/core/keepwarm/manager.go", Max: 450},
	{Path: "internal/core/keepwarm/scheduler.go", Max: 450},
}

// PackageTreeBudget caps recursive non-test .go lines for a package tree.
type PackageTreeBudget struct {
	Tree string
	Max  int
}

// PackageTreeBudgets locks measured convergence tree ceilings (+25 lines headroom).
var PackageTreeBudgets = []PackageTreeBudget{
	// Reasoning semantic compression adds explicit generation composition; the
	// dedicated overlay keeps it out of legacy shrinkage while this tree ratchet
	// is measured at 12721 on current main plus 25 lines headroom.
	{Tree: "internal/infra/runtimebundle", Max: 12933},
	{Tree: "internal/stdhttp", Max: 6693},
	{Tree: "cmd/lipstd", Max: 979},
	{Tree: "pkg/lipruntime", Max: 720},
}

// LineBudget caps recursive non-test lines for broader architectural layers.
type LineBudget struct {
	Dir string
	Max int
}

// LineBudgets covers core/pluginreg plus the convergence trees (kept in sync
// with PackageTreeBudgets for overlapping entries).
var LineBudgets = []LineBudget{
	// Routing-override admin, billing host composition, and tool-call
	// classification. Keep the measured-plus-25 ratchet. The compaction
	// detector and continuity infrastructure add bounded recognition, branch
	// coordination, preservation dispatch, and auxiliary worker ownership.
	// Current-main integration measures 83038 non-test lines: the ownership
	// refactor's 1540-line increase over the 80936 d606 merge-base plus current
	// main's independently ratcheted 562-line increase. The accepted ownership
	// cost replaces flattened shared state with five explicit lifetime owners
	// and typed evidence seams; retain 25 lines of ratchet headroom.
	// P1 fix for post-transfer assemble abort adds 72 lines for the single
	// AbortBeforeReturn owner and B-leg release; bump to 83160 (83135+25).
	// Phase 2 ownership convergence adds readiness preparation and streamAssemblyTx; bump to 83345 (83320+25).
	// Phase 3 ownership convergence adds TerminalizeAttempt single-owner terminalization (approx 300 lines); bump to 83700 (83649+51 headroom).
	// Phase 5.1 isolated parallel workers outcome convergence adds coordinator goroutine and outcomes mapping; bump to 83900 (83830+70 headroom).
	// Phase 6 ownership convergence final certification verifies 5-owner facade, fan-out 1, cross-owner 14, state-copy 6, cleanup 7.
	// R1+R2 debt remediation (spec runtime-attempt-publication-ownership-convergence Debt Plan R1-R4): 5 shims deleted (AbortBeforeReturn/finishAsReplaced/Rollback/Abort/RollbackParallelLoser) + 4 session-owned methods added (drainSidebandEvidence/detachStream/closeDetached/hasInner), measured 84098, bump to 84200 with 102 headroom.
	// Reviewer Blocker 2 parallel outcome ownership convergence: explicit outcome handoff/ack protocol, worker-owned attemptTx self-cleanup, isolated arm context cancellation, and serial reducer affinity delta; measured 84484, bump to 84525 with 41 headroom.
	// Reviewer Blocker 3 attempt publication and physical cancel/close ownership convergence: attemptSession sole physical owner, attemptLifecycleHandle delegation on ALeg registration, turnTerminal closeClose sequence inversion, Recv loop detachStream elimination; measured 84615, bump to 84650 with 35 headroom.
	// Reviewer Blocker publication ownership convergence: unpublished readyAttempt lifecycle handle, ready cancellation disposal state machine, and linearizable cancel vs consume coordination; measured 85021, bump to 85060 with 39 headroom.
	// Non-forwardable conversation content semantic identity/anchor contract adds internal/core/conversationview; measured 85751, bump to 85776 with 25 headroom.
	// Non-forwardable conversation content snapshot/store contract (Reader/Tagger/SteeringStore + ReferenceStore) adds store value objects and contract suite; measured 86586, bump to 86611 with 25 headroom.
	// Non-forwardable conversation content projection/placement/cache-prefix invariants (pure deterministic Project + ResolveAfterIngressTailAnchor); measured 87176, bump to 87201 with 25 headroom.
	// Non-forwardable conversation content provenance (D14 request-local provenance for final reassertion); measured 87281, bump to 87306 with 25 headroom.
	// Non-forwardable conversation content memory A-leg conversation-view state (B2BUA MemoryStore capability + extracted contract suite with tightenings); measured 88729, bump to 88754 with 25 headroom.
	// Non-forwardable conversation content Bun SQLite/PostgreSQL persistence (A-leg-owned migrations + deterministic Bun adapter + SQLite/PG contract tests); measured 89658, bump to 89683 with 25 headroom.
	// Non-forwardable conversation content sdkadapter services relocation (capability-resolving helpers moved from runtimebundle to sdkadapter, deterministic fail-closed FromStore/Services); measured 89919 (core) / 12591 (runtimebundle), bump to 89944/12616 with 25 headroom.
	// Non-forwardable conversation content Task 3.1 pre-B-leg seam (authoritative A-leg/secret/submit/CTP ordering, deep-cloned ingress vs backend working call isolation, seam before inference transforms/billing/route); measured 90002, bump to 90027 with 25 headroom.
	// Non-forwardable conversation content Task 3.2 early backend-effective projection (snapshot once after A-leg, frozen Snapshot+ProjectionEvidence before pre-request/billing/routing, fail-closed bounded evidence); measured 90095, bump to 90120 with 25 headroom.
	// Non-forwardable conversation content Task 3.2 remediation (secure seam, MemoryStore prepareRequest coverage, backend Open reuse, failure counters, bounded summary without OverlayID/plaintext); measured 90145, bump to 90170 with 25 headroom.
	// Non-forwardable conversation content Task 3.3 generic two-phase local-turn stage (frozen ordered Handler list in snapshot, tag-before-handle/reply, finite EventStream, no B-leg/billing, panic recovery, fail-open/closed, cancellation/Close finite no goroutine); measured 90450, bump to 90475 with 25 headroom.
	// Non-forwardable conversation content Task 3.4 generic canonical local stream helper/factory and bounded frontend contract/continuation-visibility slice (streaming + non-streaming official frontends, legacy full-history and OpenResponses materialized-history filtering, no B-leg/usage); measured 90511, bump to 90536 with 25 headroom.
	// Non-forwardable conversation content Task 4.1 final conversation-view reassertion at shared candidate-open choke point (pure Reassert with provenance, frozen snapshot/provenance, no store read, PTB from reasserted call, candidate adaptation integrity, fail-closed anchor); measured 90833, bump to 90858 with 25 headroom.
	// Non-forwardable conversation content Task 4.1 precision fixes (placement-aware provenance with Injected* indices, FilterNeverBackend helper, filtered baseline frozen, per-identity extra handling, full VerifyAdaptationPreservesProjection with never_backend/order/placement); measured 91661, bump to 91686 with 25 headroom.
	// Non-forwardable conversation content Task 4.1 adversarial harden (item_reference cleanup, same-slice collision fail-closed, provenance without synthetic ID scan, insertion-shift handling); measured 91760, bump to 91785 with 25 headroom.
	// Non-forwardable conversation content Task 4.3 cache regression (bounded CacheDiscontinuityKind/Placement in SteeringState for create/replace/move/deactivate, MemoryStore/Bun parity, stable_prefix and fixed activation ordering U_N,STEER,A_N,U_N+1 across 3 turns, moving-tail negative, anchor fallback/fail_closed); measured 91840, bump to 91865 with 25 headroom.
	// Non-forwardable conversation content Task 5.2 bounded diagnostics and security guards (content-free observer seam for filter/injection/fallback/failure/mutation, SDK Writer observer wiring, runtime early/final projection summary emission, Prometheus bounded labels, docs hidden-steering not secrecy, reason-code/secret-guard ordering tests); measured 92021, bump to 92046 with 25 headroom.
	// Remediation round 1: panic-isolated SafeObserver wrapper, anchor failure only on ErrAnchorMissing/ErrAnchorNotFound, stage label on OnAnchorFallback (stage+policy), production compose helper internal/infra/metrics NewConversationViewServicesWithMetrics; measured 92070, bump to 92095 with 25 headroom.
	// Conversation-view follow-ups: atomic after_message anchor registration invariant (ErrSteeringAnchorExcluded checked inside Reference/Memory/Bun PutSteering under lock/Tx, RegistersNewAfterMessageAnchor helper, unsafe trailing-survivor rejection in ResolveAfterIngressTailAnchor, storecontract AnchorExcludedRegistration + sdkadapter TOCTOU regression, re-runnable PG harness cleanup); measured 92223, bump to 92248 with 25 headroom.
	// Conversation-view follow-up review hardening: pin same after_message anchor exempt branch and valid reasoning trajectory with Validate; measured 92263, bump to 92288 with 25 headroom.
	// Reasoning-preservation semantic compression adds bounded auxiliary workload classification and orchestration; current-main integration measured 92340, bump to 92365 with 25 headroom.
	// Production service wiring and bounded cleanup add 17 core lines; current-main integration measured 92357, bump to 92382 with 25 headroom.
	// Post-review lifecycle simplification and shared Collected cloning reduce the final current-main integration to 92153; ratchet to 92178 with 25 headroom.
	// aleg-cancellation-bleg-termination-hardening: single-use B-leg launch permit, concurrent bounded A-leg cancel fan-out, truthful physical CancelResult propagation, bounded attempt-owned sideband evidence accumulator, terminal stream drain, exactly-once terminal B-leg billing precedence, and bounded cancellation telemetry; measured 92771, bump to 92796 with 25 headroom.
	{Dir: "internal/core", Max: 94498},
	{Dir: "internal/pluginreg", Max: 1174},
	{Dir: "internal/stdhttp", Max: 6693},
	{Dir: "internal/infra/runtimebundle", Max: 12933},
	{Dir: "cmd/lipstd", Max: 979},
	{Dir: "pkg/lipruntime", Max: 720},
}

// CountNonTestGoLines recursively counts physical lines in non-test .go files.
func CountNonTestGoLines(dir string) (int, error) {
	var total int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		n, err := countTreeFileLines(path)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	return total, err
}

func countTreeFileLines(path string) (n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// CountFileLines counts physical lines in one file.
func CountFileLines(path string) (int, error) {
	return countTreeFileLines(path)
}

// FormatRuntimeConvergencePackageBudgets renders the advisory Markdown section.
func FormatRuntimeConvergencePackageBudgets(root string) (string, error) {
	var b strings.Builder
	fmt.Fprintln(&b, "## Runtime-convergence package budgets")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Tree | Non-test lines | Budget |")
	fmt.Fprintln(&b, "| --- | --- | --- |")
	for _, budget := range PackageTreeBudgets {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(budget.Tree)))
		if err != nil {
			fmt.Fprintf(&b, "| `%s` | (missing: %v) | %d |\n", budget.Tree, err, budget.Max)
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %d |\n", budget.Tree, n, budget.Max)
	}
	fmt.Fprintln(&b)
	return b.String(), nil
}

// RuntimeConvergenceShrinkageBaselineSHA is the reviewed production baseline for Req 11.5.
const RuntimeConvergenceShrinkageBaselineSHA = "efe4624909cea318c7211d5cb3734059d3210802"

// RuntimeConvergenceMinNetLineReduction is the Requirement 11.5 floor for the
// legacy convergence component (after subtracting the ADR 0008 overlay).
const RuntimeConvergenceMinNetLineReduction = 800

// ConnectorArchitectureOverlayMax is the exact-measured ADR 0008 connector
// architecture overlay ratchet (non-test lines in structurally selected files).
// The reliability work adds explicit discovered-plugin artifact ownership and
// cleanup to the connector composition path. Keep 25 lines of ratchet headroom
// over the reviewed 2,275-line overlay.
const ConnectorArchitectureOverlayMax = 2300

// AffectedSurfaceBaseline locks one Req 11.5 surface baseline.
type AffectedSurfaceBaseline struct {
	Tree          string
	BaselineLines int
}

// RuntimeConvergenceAffectedSurfaces is the Requirement 11.5 inventory.
var RuntimeConvergenceAffectedSurfaces = []AffectedSurfaceBaseline{
	{Tree: "internal/infra/runtimebundle", BaselineLines: 9898},
	{Tree: "internal/infra/runtimehost", BaselineLines: 3056},
	{Tree: "internal/stdhttp", BaselineLines: 4666},
	{Tree: "cmd/lipstd", BaselineLines: 985},
	{Tree: "pkg/lipruntime", BaselineLines: 1037},
}

// connectorArchitectureOverlayImportMarkers selects ADR 0008 host/discovery
// production files by import graph (no maintained connector kind lists).
var connectorArchitectureOverlayImportMarkers = []string{
	"/backendplugins/discovery",
	"/backendplugins/catalog",
	"/backendplugins/trust",
	"/backendplugins/diagnostics",
	"/lipsdk/backendplugin",
}

// AffectedSurfaceMeasurement is one surface's baseline-versus-current delta.
type AffectedSurfaceMeasurement struct {
	Tree          string
	BaselineLines int
	CurrentLines  int
	Delta         int
}

// OverlayMeasurement is one architecture-overlay allowance: the files a feature
// added to the convergence surfaces, their measured non-test lines, the ratchet
// cap, and whether the cap holds.
type OverlayMeasurement struct {
	Name  string
	Files []string
	Lines int
	Max   int
	Pass  bool
}

// ShrinkageMeasurement is the Requirement 11.5 aggregate plus architecture overlays.
type ShrinkageMeasurement struct {
	BaselineSHA      string
	Surfaces         []AffectedSurfaceMeasurement
	BaselineTotal    int
	CurrentTotal     int
	Delta            int // raw current-baseline (includes overlay lines)
	Connector        OverlayMeasurement
	PathOverlays     []OverlayMeasurement
	ConvergenceDelta int // Delta - overlay lines (legacy Req 11.5 component)
	RequiredMax      int
	Pass             bool
}

// MeasureConnectorArchitectureOverlay counts non-test lines in affected-surface
// production files that import connector discovery/trust/catalog/ABI packages.
func MeasureConnectorArchitectureOverlay(root string) (OverlayMeasurement, error) {
	m, err := measureOverlayByMarkers(root, connectorArchitectureOverlayImportMarkers, ConnectorArchitectureOverlayMax, nil)
	if err != nil {
		return OverlayMeasurement{}, err
	}
	m.Name = "Connector"
	return m, nil
}

func measureOverlayByMarkers(root string, markers []string, maxBytes int, exclude map[string]struct{}) (OverlayMeasurement, error) {
	m := OverlayMeasurement{Max: maxBytes}
	for _, s := range RuntimeConvergenceAffectedSurfaces {
		dir := filepath.Join(root, filepath.FromSlash(s.Tree))
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
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
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(src)
			hit := false
			for _, marker := range markers {
				if strings.Contains(text, marker) {
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
			return OverlayMeasurement{}, fmt.Errorf("%s: %w", s.Tree, err)
		}
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}

// MeasureRuntimeConvergenceShrinkage compares current lines to the locked baseline
// and separates the architecture overlays from the legacy convergence component.
func MeasureRuntimeConvergenceShrinkage(root string) (ShrinkageMeasurement, error) {
	m := ShrinkageMeasurement{
		BaselineSHA: RuntimeConvergenceShrinkageBaselineSHA,
		Surfaces:    make([]AffectedSurfaceMeasurement, 0, len(RuntimeConvergenceAffectedSurfaces)),
		RequiredMax: -RuntimeConvergenceMinNetLineReduction,
	}
	for _, s := range RuntimeConvergenceAffectedSurfaces {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(s.Tree)))
		if err != nil {
			return ShrinkageMeasurement{}, fmt.Errorf("%s: %w", s.Tree, err)
		}
		delta := n - s.BaselineLines
		m.Surfaces = append(m.Surfaces, AffectedSurfaceMeasurement{
			Tree:          s.Tree,
			BaselineLines: s.BaselineLines,
			CurrentLines:  n,
			Delta:         delta,
		})
		m.BaselineTotal += s.BaselineLines
		m.CurrentTotal += n
		m.Delta += delta
	}
	connector, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		return ShrinkageMeasurement{}, err
	}
	m.Connector = connector
	pathOverlays, err := measurePathMarkerOverlays(root, connector.Files)
	if err != nil {
		return ShrinkageMeasurement{}, err
	}
	m.PathOverlays = pathOverlays
	overlayLines := m.Connector.Lines
	m.Pass = m.Connector.Pass
	for _, o := range m.PathOverlays {
		overlayLines += o.Lines
		m.Pass = m.Pass && o.Pass
	}
	m.ConvergenceDelta = m.Delta - overlayLines
	m.Pass = m.Pass && m.ConvergenceDelta <= m.RequiredMax
	return m, nil
}

// overlays returns the connector overlay followed by the path-marker overlays in
// table order, for uniform report formatting.
func (m ShrinkageMeasurement) overlays() []OverlayMeasurement {
	out := make([]OverlayMeasurement, 0, 1+len(m.PathOverlays))
	out = append(out, m.Connector)
	out = append(out, m.PathOverlays...)
	return out
}

// FormatRuntimeConvergenceShrinkage renders the machine-checkable Markdown section.
func FormatRuntimeConvergenceShrinkage(root string) (string, ShrinkageMeasurement, error) {
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		return "", ShrinkageMeasurement{}, err
	}
	var b strings.Builder
	fmt.Fprintln(&b, "## Runtime-convergence net shrinkage (Req 11.5)")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Baseline SHA: `%s`\n\n", m.BaselineSHA)
	fmt.Fprintln(&b, "Method: recursive `CountNonTestGoLines` (non-test `.go` physical lines, including build-tag alternates). Moving unchanged logic between packages is not shrinkage (Req 11.6).")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "ADR 0008 connector-architecture overlay: approved public host/discovery additions are measured structurally (import markers for discovery/catalog/trust/diagnostics/backendplugin ABI). Additional feature overlays select new production files by path. Each overlay and the legacy convergence component are ratcheted separately.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Surface | Baseline | Current | Delta |")
	fmt.Fprintln(&b, "| --- | ---: | ---: | ---: |")
	for _, s := range m.Surfaces {
		fmt.Fprintf(&b, "| `%s` | %d | %d | %+d |\n", s.Tree, s.BaselineLines, s.CurrentLines, s.Delta)
	}
	fmt.Fprintf(&b, "| **TOTAL** | **%d** | **%d** | **%+d** |\n", m.BaselineTotal, m.CurrentTotal, m.Delta)
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Raw delta (includes overlays): `%+d`\n\n", m.Delta)
	for _, o := range m.overlays() {
		fmt.Fprintf(&b, "%s overlay lines: `%d` (cap `%d`; files: `%s`)\n\n", o.Name, o.Lines, o.Max, strings.Join(o.Files, "`, `"))
	}
	fmt.Fprintf(&b, "Convergence delta (raw − overlays): `%+d`\n\n", m.ConvergenceDelta)
	fmt.Fprintf(&b, "Required: convergence delta ≤ %+d (remove ≥ %d lines after overlays)", m.RequiredMax, RuntimeConvergenceMinNetLineReduction)
	for _, o := range m.overlays() {
		fmt.Fprintf(&b, "; %s overlay ≤ %d", strings.ToLower(o.Name), o.Max)
	}
	fmt.Fprintln(&b, ".")
	fmt.Fprintln(&b)
	if m.Pass {
		fmt.Fprintln(&b, "Verdict: **PASS**")
	} else {
		var reasons []string
		if m.ConvergenceDelta > m.RequiredMax {
			reasons = append(reasons, fmt.Sprintf("convergence short by %d lines to reach ≤ %+d", m.ConvergenceDelta-m.RequiredMax, m.RequiredMax))
		}
		for _, o := range m.overlays() {
			if !o.Pass {
				reasons = append(reasons, fmt.Sprintf("%s overlay measured %d exceeds cap %d", strings.ToLower(o.Name), o.Lines, o.Max))
			}
		}
		fmt.Fprintf(&b, "Verdict: **FAIL** (%s).\n", strings.Join(reasons, "; "))
	}
	fmt.Fprintln(&b)
	return b.String(), m, nil
}
