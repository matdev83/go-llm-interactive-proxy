package runtimebundle

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// CompileGeneration builds one complete immutable generation bundle: isolated
// candidate runtime (executor/backends/features/ledger) plus a standard request-
// plane http.Handler with no listener bind (design Generation Compiler).
//
// Each compile consumes an isolated candidate effective configuration and rebuilds
// registrations/feature surface from the process-owned factory catalog. It does
// not mutate ProcessServices, an active generation, or a prior candidate.
func CompileGeneration(ctx context.Context, in GenerationCompileInput) (*GenerationBundle, error) {
	if in.Process == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if in.Compose == nil {
		return nil, fmt.Errorf("runtimebundle: nil HandlerComposer")
	}
	ps := in.Process
	if ps.Closed() {
		return nil, fmt.Errorf("runtimebundle: ProcessServices is closed")
	}

	src := in.Candidate
	if src == nil {
		src = ps.cfg
	}
	if src == nil {
		return nil, fmt.Errorf("runtimebundle: nil candidate config")
	}
	frozen, err := freezeConfig(src)
	if err != nil {
		return nil, err
	}
	if err := standardplugins.ValidateCustomCompatibleBackendPrefixes(frozen.Plugins.Backends); err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}

	regs := freezeRegistrations(config.RegistrationsFromConfig(frozen))
	merged, err := featurebundle.MergeFeatureSurface(ps.FactoryCatalog, regs)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: feature surface: %w", err)
	}
	merged.ToolReactorErrorPolicy = config.ParseToolReactorErrorPolicy(frozen.Hooks.ToolReactorErrorPolicy)

	lifecycles := append([]lipplugin.Lifecycle(nil), merged.Lifecycles...)
	ext := extensionsFromMerged(merged, ps.opts)
	if in.CandidateOpts != nil {
		lifecycles = append(lifecycles, in.CandidateOpts.FeatureLifecycles...)
		overlayExtensions(&ext, in.CandidateOpts.Extensions)
	}

	bus := in.Bus
	if bus == nil {
		bus = hooks.New(hooksConfigFromMerged(merged))
	}

	cand, err := CompileCandidate(ctx, GenerationCompileInput{
		Process:   ps,
		Bus:       bus,
		Candidate: frozen,
		CandidateOpts: &BuildOptions{
			FeatureLifecycles:       lifecycles,
			Extensions:              ext,
			ReplaceCandidateSurface: true,
		},
		LiveFactoryKinds: in.LiveFactoryKinds,
		FaultInject:      in.FaultInject,
	})
	if err != nil {
		return nil, err
	}

	fail := func(err error) (*GenerationBundle, error) {
		if rollErr := cand.Close(); rollErr != nil {
			return nil, errors.Join(err, rollErr)
		}
		return nil, err
	}

	if err := injectCandidateFault(in.FaultInject, "handler"); err != nil {
		return fail(err)
	}

	wireModel := ps.opts.WireModel
	if wireModel == nil {
		wireModel = standardplugins.DefaultWireModel
	}
	route := cand.EffectiveDefaultRoute
	if route == "" {
		route = config.EffectiveDefaultRouteSelector(frozen, wireModel)
	}
	authProviders := append([]httpauth.Provider(nil), cand.HTTPAuthProviders...)

	plane := RequestPlane{
		log:             ps.Logger,
		frozen:          frozen,
		registrations:   freezeRegistrations(regs),
		route:           FrozenRoutingView{DefaultRoute: route, RoutePrefixes: append([]string(nil), cand.RoutePrefixes...)},
		executor:        cand.Executor,
		store:           cand.Store,
		upstreamHTTP:    cand.UpstreamHTTP,
		decodeAdmission: cand.DecodeAdmission,
		pluginRegistry:  cand.PluginRegistry,
		metrics:         cand.Metrics,
		runtimeSnap:     cand.RuntimeSnapshot,
		httpAuth:        authProviders,
		secureSessions:  cand.SecureSessionStore,
		authEvents:      cand.AuthEventDispatcher,
		catalog:         cand.CatalogRuntime,
		modelRegistry:   cand.ModelRegistry,
		modelRuntime:    cand.ModelRegistryRuntime,
		tokenAdmin:      cand.TokenAccountingAdmin,
		cpQueries:       cand.ControlPlaneQueries,
		cpStatus:        cand.ControlPlaneStatus,
		cpRetention:     cand.ControlPlaneRetention,
		usageAuthority:  cand.UsageAuthority,
		concurrency:     cand.ConcurrencyAuthority,
		snapshots:       cand.SnapshotGeneration,
		snapshotCtrl:    cand.SnapshotController,
		meteringQuerier: cand.MeteringQuerier,
		readiness:       cand.ReadinessReport,
		secretGuardInv:  cand.SecretGuardInventory,
		terminalProc:    cand.TerminalWorkProcessor,
		terminalReg:     cand.TerminalWorkRegistry,
		terminalQueries: cand.TerminalWorkQueries,
		terminalMetrics: cand.TerminalWorkMetrics,
	}

	handler, err := in.Compose(ctx, plane)
	if err != nil {
		return fail(fmt.Errorf("runtimebundle: compose request plane: %w", err))
	}
	if handler == nil {
		return fail(fmt.Errorf("runtimebundle: handler composer returned nil handler"))
	}

	return &GenerationBundle{
		handler:           handler,
		executor:          cand.Executor,
		routing:           FrozenRoutingView{DefaultRoute: route, RoutePrefixes: append([]string(nil), cand.RoutePrefixes...)},
		frontends:         freezePluginConfigs(frozen.Plugins.Frontends),
		registrations:     freezeRegistrations(regs),
		httpAuth:          append([]httpauth.Provider(nil), authProviders...),
		models:            cand.ModelRegistryRuntime,
		catalog:           cand.CatalogRuntime,
		backendIDs:        backendIDsOf(cand.Executor),
		ledger:            cand.Ledger,
		owner:             cand,
		terminalProviders: terminalworkapp.SnapshotTerminalProviders(cand.TerminalWorkRegistry),
	}, nil
}

func injectCandidateFault(fi CandidateFaultInject, boundary string) error {
	if fi.After != boundary {
		return nil
	}
	if fi.Hook != nil {
		fi.Hook()
	}
	return fmt.Errorf("%w: after %s", ErrCandidateFaultInjected, boundary)
}

// extensionsFromMerged builds candidate feature extensions from the newly merged
// candidate surface plus process-fixed enterprise Production injections and
// required secret environment/decision seams. It does not append startup-merged
// feature observers from ProcessServices.opts.Extensions (those already contain
// the bootstrap merged surface and would leak/duplicate into later candidates).
func extensionsFromMerged(merged featurebundle.MergedFeatureSurface, processOpts *BuildOptions) ExtensionsOptions {
	ext := ExtensionsOptions{
		SessionOpeners:                   append(merged.SessionOpeners[:0:0], merged.SessionOpeners...),
		WorkspaceResolvers:               append(merged.WorkspaceResolvers[:0:0], merged.WorkspaceResolvers...),
		ToolCatalogFilters:               append(merged.ToolCatalogFilters[:0:0], merged.ToolCatalogFilters...),
		ToolCallPolicies:                 append(merged.ToolCallPolicies[:0:0], merged.ToolCallPolicies...),
		ToolCallFinalizers:               append(merged.ToolCallFinalizers[:0:0], merged.ToolCallFinalizers...),
		ToolCallFinalizationMaxArgsBytes: merged.ToolCallFinalizationMaxArgsBytes,
		RequestTransforms:                append(merged.RequestTransforms[:0:0], merged.RequestTransforms...),
		PreRequestHandlers:               append(merged.PreRequestHandlers[:0:0], merged.PreRequestHandlers...),
		RouteHintProviders:               append(merged.RouteHintProviders[:0:0], merged.RouteHintProviders...),
		CompletionGates:                  append(merged.CompletionGates[:0:0], merged.CompletionGates...),
		AttemptTransforms:                append(merged.AttemptTransforms[:0:0], merged.AttemptTransforms...),
		StreamObserverFactories:          append(merged.StreamObserverFactories[:0:0], merged.StreamObserverFactories...),
		TrafficObservers:                 append(merged.TrafficObservers[:0:0], merged.TrafficObservers...),
		UsageObservers:                   append(merged.UsageObservers[:0:0], merged.UsageObservers...),
		RawCaptureSinks:                  append(merged.RawCaptureSinks[:0:0], merged.RawCaptureSinks...),
		TrafficRedactors:                 append(merged.TrafficRedactors[:0:0], merged.TrafficRedactors...),
		SecretGuards:                     append(merged.SecretGuards[:0:0], merged.SecretGuards...),
	}
	if processOpts != nil {
		ext.TrafficObservers = append(ext.TrafficObservers, processOpts.Production.TrafficObservers...)
		ext.UsageObservers = append(ext.UsageObservers, processOpts.Production.UsageObservers...)
		ext.SecretGuardEnvironment = processOpts.Extensions.SecretGuardEnvironment
		ext.SecretGuardInputs = processOpts.Extensions.SecretGuardInputs
		ext.SecretDecisionObserver = processOpts.Extensions.SecretDecisionObserver
	}
	return ext
}

func overlayExtensions(dst *ExtensionsOptions, src ExtensionsOptions) {
	if dst == nil {
		return
	}
	dst.SessionOpeners = append(dst.SessionOpeners, src.SessionOpeners...)
	dst.WorkspaceResolvers = append(dst.WorkspaceResolvers, src.WorkspaceResolvers...)
	dst.ToolCatalogFilters = append(dst.ToolCatalogFilters, src.ToolCatalogFilters...)
	dst.ToolCallPolicies = append(dst.ToolCallPolicies, src.ToolCallPolicies...)
	// Local names avoid ownership closer scanner false-positives on "*Finalizer*".
	curTCF := dst.ToolCallFinalizers
	addTCF := src.ToolCallFinalizers
	dst.ToolCallFinalizers = append(curTCF, addTCF...)
	if src.ToolCallFinalizationMaxArgsBytes > 0 {
		dst.ToolCallFinalizationMaxArgsBytes = src.ToolCallFinalizationMaxArgsBytes
	}
	dst.RequestTransforms = append(dst.RequestTransforms, src.RequestTransforms...)
	dst.PreRequestHandlers = append(dst.PreRequestHandlers, src.PreRequestHandlers...)
	dst.RouteHintProviders = append(dst.RouteHintProviders, src.RouteHintProviders...)
	dst.CompletionGates = append(dst.CompletionGates, src.CompletionGates...)
	dst.AttemptTransforms = append(dst.AttemptTransforms, src.AttemptTransforms...)
	dst.StreamObserverFactories = append(dst.StreamObserverFactories, src.StreamObserverFactories...)
	dst.TrafficObservers = append(dst.TrafficObservers, src.TrafficObservers...)
	dst.UsageObservers = append(dst.UsageObservers, src.UsageObservers...)
	dst.RawCaptureSinks = append(dst.RawCaptureSinks, src.RawCaptureSinks...)
	dst.TrafficRedactors = append(dst.TrafficRedactors, src.TrafficRedactors...)
	dst.SecretGuards = append(dst.SecretGuards, src.SecretGuards...)
	if src.SecretGuardEnvironment != nil {
		dst.SecretGuardEnvironment = src.SecretGuardEnvironment
	}
	if src.SecretDecisionObserver != nil {
		dst.SecretDecisionObserver = src.SecretDecisionObserver
	}
}
