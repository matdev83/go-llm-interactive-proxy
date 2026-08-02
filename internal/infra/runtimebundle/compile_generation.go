package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	cpadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/controlplane"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

func CompileGeneration(ctx context.Context, in GenerationCompileInput) (GenerationRuntime, error) {
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
	var pluginReg *pluginreg.Registry
	if ps.opts != nil {
		pluginReg = ps.opts.PluginRegistry
	}
	if err := validateCandidateManifestOwnership(frozen, pluginReg); err != nil {
		return nil, err
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

	cand, err := compileCandidate(ctx, GenerationCompileInput{
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

	failBeforeTransfer := func(err error) (GenerationRuntime, error) {
		if rollErr := cand.RollbackUnpublished(); rollErr != nil {
			return nil, errors.Join(err, rollErr)
		}
		return nil, err
	}

	if err := injectCandidateFault(in.FaultInject, "handler"); err != nil {
		return failBeforeTransfer(err)
	}

	wireModel := ps.opts.WireModel
	if wireModel == nil {
		wireModel = standardplugins.DefaultWireModel
	}
	route := cand.execution.effectiveDefaultRoute
	if route == "" {
		route = config.EffectiveDefaultRouteSelector(frozen, wireModel)
	}
	authProviders := append([]httpauth.Provider(nil), cand.security.httpAuth...)

	// The generation lifecycle context is the runtime shutdown signal for
	// long-lived transport state mounted into this generation (WebSocket
	// sessions). The ledger cancels it at PhaseQuiesce on reload/shutdown;
	// failure paths below also cancel it before rolling the ledger back.
	genCtx, genCancel := context.WithCancel(context.Background())
	if cand.ledger != nil {
		cand.ledger.AddClose("openresponses-generation-lifecycle", PhaseQuiesce, func() error { genCancel(); return nil })
	}
	failWithGenCtx := func(err error) (GenerationRuntime, error) { genCancel(); return failBeforeTransfer(err) }

	httpInput := buildStandardHTTPInput(cand, frozen, regs, route, genCtx)
	if err := injectCandidateFault(in.FaultInject, "composer-clone"); err != nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: composer config clone: %w", err))
	}
	composerCfg, err := freezeConfig(frozen)
	if err != nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: composer config clone: %w", err))
	}

	handler, err := composeStandardHTTPIsolated(ctx, in.Compose, composerCfg, ps.Logger, httpInput)
	if err != nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: compose request plane: %w", err))
	}
	if handler == nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: handler composer returned nil handler"))
	}

	if err := injectCandidateFault(in.FaultInject, "ledger-transfer"); err != nil {
		_ = cand.claimLifecycleLedger()
	}
	ledger := cand.transferLedgerOwnership()
	if ledger == nil {
		return failWithGenCtx(fmt.Errorf("runtimebundle: candidate resource ledger unavailable for transfer"))
	}
	bundle := newGenerationBundle(generationBundleInput{
		handler:           handler,
		executor:          cand.execution.executor,
		routing:           FrozenRoutingView{DefaultRoute: route, RoutePrefixes: append([]string(nil), cand.execution.routePrefixes...)},
		frontends:         frozen.Plugins.Frontends,
		registrations:     regs,
		httpAuth:          authProviders,
		models:            cand.models.registryRuntime,
		catalog:           cand.models.catalog,
		backendIDs:        backendIDsOf(cand.execution.executor),
		ledger:            ledger,
		terminalProviders: terminalworkapp.SnapshotTerminalProviders(cand.operations.terminalRegistry),
		readiness:         cand.operations.readinessReport,
	})
	return bundle, nil
}

func composeStandardHTTPIsolated(ctx context.Context, compose HandlerComposer, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (handler http.Handler, err error) {
	defer func() {
		if p := recover(); p != nil {
			handler = nil
			err = fmt.Errorf("runtimebundle: compose panic: %s", configreload.SanitizePanicValue(p))
		}
	}()
	return compose(ctx, cfg, log, in)
}

func buildStandardHTTPInput(cand *candidateAssembly, frozen *config.Config, regs []lipsdk.Registration, route string, genCtx context.Context) httpcontract.StandardHTTPInput {
	var maxBody int64
	var preKA lipsdk.FrontendKeepaliveConfig
	if frozen != nil {
		maxBody = frozen.Server.EffectiveMaxRequestBodyBytes()
		ka := frozen.Server.EffectivePreRequestKeepalive()
		preKA = lipsdk.FrontendKeepaliveConfig{Enabled: ka.Enabled, Interval: ka.Interval}
	}
	var plugins []config.PluginConfig
	if frozen != nil {
		plugins = frozen.Plugins.Frontends
	}
	return httpcontract.StandardHTTPInput{
		Core: httpcontract.HTTPCoreInput{Executor: cand.execution.executor},
		Security: httpcontract.HTTPSecurityInput{
			HTTPAuthProviders:    httpcontract.CloneHTTPAuthProviders(cand.security.httpAuth),
			SecureSessionStore:   cand.security.secureSessionStore,
			UsageAuthority:       cpadmin.AdaptAccountingAuthorityQueries(cand.process.usageAuthority),
			ConcurrencyAuthority: cpadmin.AdaptConcurrencyAuthorityQueries(cand.process.concurrencyAuthority),
		},
		Operations: httpcontract.HTTPOperationsInput{
			Metrics:              cand.process.metrics,
			Store:                cand.process.store,
			SecretGuardInventory: cand.operations.secretGuardInventory,
			ControlPlaneQueries:  cpadmin.AdaptControlPlaneQueries(cand.process.controlPlaneQueries),
			ReadinessReport:      cpadmin.AdaptReadinessReport(cand.operations.readinessReport),
			TokenAccountingAdmin: adminaccounting.AdaptCountCallService(cand.operations.tokenAccountingAdmin),
			Registrations:        httpcontract.CloneRegistrations(regs),
		},
		Models: httpcontract.HTTPModelInput{
			CatalogRuntime:       cand.models.catalog,
			ModelRegistryRuntime: cand.models.registryRuntime,
		},
		Frontends: httpcontract.HTTPFrontendInput{
			Executor:             cand.execution.executor,
			Registry:             cand.process.pluginRegistry,
			DefaultRouteSelector: route,
			RoutePrefixes:        httpcontract.CloneStrings(cand.execution.routePrefixes),
			Plugins:              httpcontract.ClonePluginConfigs(plugins),
			MaxRequestBodyBytes:  maxBody,
			DecodeAdmission:      cand.execution.decodeAdmission,
			TrafficPorts:         httpcontract.TrafficPortsFromSnapshot(cand.security.runtimeSnapshot),
			PreRequestKeepalive:  preKA,
			GenerationContext:    genCtx,
			FrontendRouteClaims:  standardplugins.StandardFrontendRouteClaims(),
		},
	}
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
