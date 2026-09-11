package runtimebundle

import (
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routeoverride"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
)

// buildProcessSpoolLedger constructs and binds the process-owned large payload spool ledger.
func buildProcessSpoolLedger(cfg *config.Config, b *metrics.Bundle) (*largebody.SpoolLedger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	memSpool := cfg.Server.LargePayloadFastPath.EffectiveMemorySpoolBytes()
	maxInflight := cfg.Server.LargePayloadFastPath.EffectiveMaxInflightSpoolBytes()
	spoolLedger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      memSpool,
		MaxInflightSpoolBytes: maxInflight,
	})
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: spool ledger: %w", err)
	}
	if b != nil {
		spoolLedger.SetObserver(b.LargePayloadDiagnostics())
	}
	return spoolLedger, nil
}

// largeBodyAssessorInput captures the dependencies needed to assemble the production
// LargeBodyAssessor in runtimebundle (Phase 4).
type largeBodyAssessorInput struct {
	Cfg            *config.Config
	Bctx           buildContext
	Opts           *BuildOptions
	In             executorBuildInput
	RoutingRT      runtime.RoutingRuntime
	AccountingRT   runtime.AccountingRuntime
	Prod           ProductionOptions
	DefBE          string
	AliasResolver  *routing.AliasResolver
	ExecResolver   routing.BackendExecutionResolver
	ExecPolicy     config.ExecutionCompositionPolicy
	OverrideReader routeoverride.Reader
}

// buildLargeBodyAssessor compiles the generation wire eligibility summary, builds
// the authority, initial, and route-override gates, and unifies them into a ProductionLargeBodyAssessor.
func buildLargeBodyAssessor(in largeBodyAssessorInput) (*runtime.ProductionLargeBodyAssessor, string, error) {
	genID := ""
	if in.In.SnapshotGeneration != nil {
		if cur := in.In.SnapshotGeneration.Current(); cur != nil {
			genID = fmt.Sprintf("gen-%d", cur.ID)
		}
	}
	if genID == "" {
		nowUnix := time.Now().UnixNano()
		if in.In.NowFn != nil {
			nowUnix = in.In.NowFn().UnixNano()
		}
		genID = fmt.Sprintf("gen-%d", nowUnix)
	}

	wireBackendMap := make(largebody.WireBackendMap, len(in.In.Model.Backends))
	for id, be := range in.In.Model.Backends {
		wireBackendMap[id] = be.AsWireBackend()
	}

	initialGate := largebody.NewInitialRouteAssessmentGate(
		in.AliasResolver,
		in.DefBE,
		in.ExecResolver,
		in.ExecPolicy,
		nil,
		wireBackendMap,
	)

	knownBackends := make(map[string]struct{}, len(in.In.Model.Backends))
	for id := range in.In.Model.Backends {
		knownBackends[id] = struct{}{}
	}
	validator := routing.NewGenerationSelectorValidator(
		in.AliasResolver,
		in.DefBE,
		knownBackends,
		in.ExecResolver,
		in.ExecPolicy,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(
		in.OverrideReader,
		validator,
		wireBackendMap,
	)

	wireProofGate := largebody.NewBackendWireProofGate(
		initialGate,
		overrideGate,
		nil,
		wireBackendMap,
	)

	census := largebody.NewStandardDependencyCensus(genID)
	hasTrafficPlanes := false
	if in.Opts != nil && !in.Opts.FeaturePlanes.IsZero() {
		contribs := in.Opts.FeaturePlanes.ToContributions()
		for i := range census.Planes {
			if contribs.Has(census.Planes[i].ID) {
				census.Planes[i].Occupied = true
			}
		}
		hasTrafficPlanes = contribs.Has("traffic_observers") ||
			contribs.Has("raw_capture_sinks") ||
			contribs.Has("traffic_redactors")
	}
	if in.Bctx.Bus != nil {
		s, r, resp, tl := in.Bctx.Bus.HookChainLengths()
		census.Hooks.SubmitOccupied = s > 0
		census.Hooks.RequestPartOccupied = r > 0
		census.Hooks.ResponsePartOccupied = resp > 0
		census.Hooks.ToolOccupied = tl > 0
	}

	census.Ports.BackendsEmpty = len(in.In.Model.Backends) == 0
	census.Ports.ConversationViewReaderOccupied = in.In.ConversationReader != nil
	census.Ports.ConversationViewTaggerOccupied = in.In.ConversationStore != nil
	census.Ports.SteeringWriterFactoryOccupied = in.In.ConversationStore != nil
	census.Ports.ExposureAdmissionOccupied = in.Prod.BillingExposureAdmission != nil
	census.Ports.BillingIdentityCustomCallbacks = in.Prod.BillingIdentity.HasCustomCallCallbacks()
	census.Ports.CapsResolverOccupied = in.RoutingRT.CapsResolver != nil
	census.Ports.CatalogResolverOccupied = in.RoutingRT.CatalogResolver != nil
	census.Ports.EligibilityResolverOccupied = in.RoutingRT.EligibilityResolver != nil
	census.Ports.RequestTokenEstimatorOccupied = in.RoutingRT.RequestTokenEstimator != nil
	if in.AccountingRT.Preflight != nil {
		census.Ports.PreflightEnabled = true
	}
	census.Ports.StreamUsageOccupied = in.AccountingRT.StreamUsage != nil
	census.Ports.AdminCountServiceOccupied = in.AccountingRT.AdminCountService != nil
	census.Ports.InterleavedProcessorOccupied = in.In.InterleavedProcessor != nil
	census.Ports.CompactionDetectorOccupied = in.In.CompactionDetector != nil
	census.Ports.TrafficCapturing = len(in.Prod.TrafficObservers) > 0 || hasTrafficPlanes

	// TwoPhaseExecutorAvailable is true because buildExecutorRuntime always instantiates
	// *runtime.Executor, which natively implements largebody.LargeBodyWireExecutor (ExecuteLargeBody)
	// and largebody.LargeBodyAssessor (AssessLargeBody). The runtime bundle composition root
	// therefore unconditionally supplies a two-phase capable executor.
	census.TwoPhaseExecutorAvailable = true

	maxFactBytes := in.Cfg.Server.LargePayloadFastPath.EffectiveMaxSemanticFactBytes()
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: census.TwoPhaseExecutorAvailable,
	}, maxFactBytes)
	if err != nil {
		return nil, "", fmt.Errorf("runtimebundle: compile wire eligibility summary: %w", err)
	}

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)

	assessor := runtime.NewProductionLargeBodyAssessor(
		genID,
		genID,
		authGate,
		wireProofGate,
		runtime.StandardLaneDomainPolicies(),
	)

	return assessor, genID, nil
}
