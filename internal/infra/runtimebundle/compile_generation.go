package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	cpadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/controlplane"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
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
		if ps.cfg == nil {
			return nil, fmt.Errorf("runtimebundle: nil candidate config")
		}
		src = ps.cfg
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
	if err := validateCompactionContinuityGeneration(ps, regs); err != nil {
		return nil, err
	}
	genRunner, boundClient, boundPoller, err := newReasoningCompressionGenerationRunner(ps)
	if err != nil {
		return nil, err
	}
	if err := validateReasoningPreservationCompressionGeneration(ps, regs, boundClient, boundPoller); err != nil {
		return nil, err
	}
	var host featurebundle.HostContributions
	if ps.opts != nil {
		host = featurebundle.HostContributions{TrafficObservers: slices.Clone(ps.opts.Production.TrafficObservers), UsageObservers: slices.Clone(ps.opts.Production.UsageObservers)}
	}
	var extraBundles []lipfeature.FeatureBundle
	if in.CandidateOpts != nil {
		if c := in.CandidateOpts.Extensions; len(c.TrafficObservers) > 0 || len(c.UsageObservers) > 0 || len(c.RawCaptureSinks) > 0 || len(c.TrafficRedactors) > 0 {
			extraBundles = append(extraBundles, lipfeature.FeatureBundle{
				SchemaVersion:    lipfeature.SchemaVersionV1,
				TrafficObservers: slices.Clone(c.TrafficObservers),
				UsageObservers:   slices.Clone(c.UsageObservers),
				RawCaptureSinks:  slices.Clone(c.RawCaptureSinks),
				TrafficRedactors: slices.Clone(c.TrafficRedactors),
			})
		}
	}
	merged, genMerged, err := featurebundle.MergeFeatureSurfacesWithHost(ps.FactoryCatalog, regs, host, extraBundles...)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: feature surface: %w", err)
	}
	if merged, err = bindCompactionContinuity(merged, ps, regs); err != nil {
		return nil, err
	}
	if merged, err = bindReasoningPreservationCompression(merged, ps, regs, boundClient, boundPoller); err != nil {
		return nil, err
	}
	toolReactorErrorPolicy := config.ParseToolReactorErrorPolicy(frozen.Hooks.ToolReactorErrorPolicy)
	lifecycles := append([]lipplugin.Lifecycle(nil), merged.Lifecycles...)
	ext := extensionsFromMerged(merged, genMerged, ps.opts)
	if in.CandidateOpts != nil {
		lifecycles = append(lifecycles, in.CandidateOpts.FeatureLifecycles...)
		overlayExtensions(&ext, in.CandidateOpts.Extensions)
	}
	if err := validateTerminalDecisionProvider(ext.TerminalDecisionProvider); err != nil {
		return nil, fmt.Errorf("runtimebundle: terminal decision provider: %w", err)
	}
	bus := in.Bus
	if bus == nil {
		bus = hooks.New(HooksConfigFromGenerated(genMerged, toolReactorErrorPolicy))
	}
	cand, err := compileCandidate(ctx, GenerationCompileInput{
		Process: ps, Bus: bus, Candidate: frozen,
		CandidateOpts: &BuildOptions{
			FeatureLifecycles:       lifecycles,
			Extensions:              ext,
			ReplaceCandidateSurface: true,
		},
		LiveFactoryKinds: in.LiveFactoryKinds,
		FaultInject:      in.FaultInject,
		GenerationRunner: genRunner,
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
	genCtx, genCancel := context.WithCancel(context.Background())
	if cand.ledger != nil {
		cand.ledger.AddClose("openresponses-generation-lifecycle", PhaseQuiesce, func() error { genCancel(); return nil })
	}
	failWithGenCtx := func(err error) (GenerationRuntime, error) { genCancel(); return failBeforeTransfer(err) }
	nowFn := time.Now
	if ps.opts != nil && ps.opts.Testing.Clock != nil {
		nowFn = ps.opts.Testing.Clock
	}
	adminHandler, err := bindGenerationRouteOverride(ps, frozen, cand.execution.executor, nowFn)
	if err != nil {
		return failWithGenCtx(err)
	}
	httpInput := buildStandardHTTPInput(genCtx, cand, frozen, regs, route)
	httpInput.Operations.RouteOverrideAdmin = adminHandler
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
	keepwarmManager, keepwarmID, err := buildKeepwarmGeneration(frozen, nowFn, cand.process.keepwarmRegistry, cand.process.keepwarmPolicy, cand.operations.keepwarmAccounting)
	if err != nil {
		return failWithGenCtx(err)
	}
	if cand.process.metrics != nil && cand.process.metrics.Keepwarm != nil {
		cand.process.metrics.Keepwarm.SetManager(keepwarmManager)
	}
	cand.execution.executor.Keepwarm = keepwarm.NewOrchestrator(keepwarmManager, cand.process.keepwarmPolicy)
	if retired, ok := cand.execution.executor.Store.(b2bua.ALegRetirementObserver); ok {
		retired.SetALegRetirementObserver(cand.execution.executor.Keepwarm.EndSession)
	}
	bundle := newGenerationBundle(generationBundleInput{
		handler:                  handler,
		executor:                 cand.execution.executor,
		routing:                  FrozenRoutingView{DefaultRoute: route, RoutePrefixes: append([]string(nil), cand.execution.routePrefixes...)},
		frontends:                frozen.Plugins.Frontends,
		registrations:            regs,
		httpAuth:                 authProviders,
		models:                   cand.models.registryRuntime,
		catalog:                  cand.models.catalog,
		backendIDs:               backendIDsOf(cand.execution.executor),
		ledger:                   ledger,
		terminalProviders:        terminalworkapp.SnapshotTerminalProviders(cand.operations.terminalRegistry),
		terminalDecisionProvider: ext.TerminalDecisionProvider,
		readiness:                cand.operations.readinessReport,
		keepwarm:                 keepwarmManager,
		keepwarmRegistry:         cand.process.keepwarmRegistry,
		keepwarmID:               keepwarmID,
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

func buildStandardHTTPInput(genCtx context.Context, cand *candidateAssembly, frozen *config.Config, regs []lipsdk.Registration, route string) httpcontract.StandardHTTPInput {
	var (
		billingReports          billing.ReportingStore
		billingReportsPath      string
		billingProvisioner      billing.AccountProvisioner
		billingExposureRecovery billing.ExposureRecovery
	)
	if cand != nil {
		billingReports, billingReportsPath, billingProvisioner, billingExposureRecovery = cand.operations.billingReports, cand.operations.billingReportsPath, cand.operations.billingProvisioner, cand.operations.billingExposureRecovery
	}
	var (
		maxBody     int64
		preKA       lipsdk.FrontendKeepaliveConfig
		geoInput    httpcontract.GeoIPSecurityInput
		httpHeaders lipsdk.HTTPHeaders
		streamKA    time.Duration
	)
	if frozen != nil {
		maxBody = frozen.Server.EffectiveMaxRequestBodyBytes()
		ka := frozen.Server.EffectivePreRequestKeepalive()
		preKA = lipsdk.FrontendKeepaliveConfig{Enabled: ka.Enabled, Interval: ka.Interval}
		httpHeaders = frozen.HTTPHeaders.Effective()
		if eff, err := config.EffectiveStreamRecoveryAutoResume(frozen, config.StreamRecoveryOverrides{}); err == nil {
			streamKA = eff.KeepaliveInterval
		}
	}
	if cand != nil && cand.security.geoip != nil && cand.security.geoip.Policy() != nil {
		var geoObs httpcontract.GeoIPObserver
		if cand.process.metrics != nil {
			geoObs = cand.process.metrics.GeoIP
		}
		geoInput = httpcontract.GeoIPSecurityInput{
			Policy: cand.security.geoip.Policy(), Lookup: cand.process.geoip, Observer: geoObs,
			Resolver: httpcontract.GeoIPResolverConfig{
				Source:         cand.security.geoip.ClientIPSource(),
				TrustedProxies: cand.security.geoip.TrustedProxies(),
			},
		}
	}
	var plugins []config.PluginConfig
	if frozen != nil {
		plugins = frozen.Plugins.Frontends
	}
	keepwarmAdmin, keepwarmAdminEnabled := keepwarmAdminProjection(cand.process)
	return httpcontract.StandardHTTPInput{
		Core: httpcontract.HTTPCoreInput{Executor: cand.execution.executor},
		Security: httpcontract.HTTPSecurityInput{
			HTTPAuthProviders:    httpcontract.CloneHTTPAuthProviders(cand.security.httpAuth),
			SecureSessionStore:   cand.security.secureSessionStore,
			UsageAuthority:       cpadmin.AdaptAccountingAuthorityQueries(cand.process.usageAuthority),
			ConcurrencyAuthority: cpadmin.AdaptConcurrencyAuthorityQueries(cand.process.concurrencyAuthority),
			GeoIP:                geoInput,
		},
		Operations: httpcontract.HTTPOperationsInput{
			BillingReports: billingReports, BillingReportsPath: billingReportsPath,
			BillingProvisioner: billingProvisioner, BillingExposureRecovery: billingExposureRecovery,
			Metrics:              cand.process.metrics,
			Store:                cand.process.store,
			SecretGuardInventory: cand.operations.secretGuardInventory,
			ControlPlaneQueries:  cpadmin.AdaptControlPlaneQueries(cand.process.controlPlaneQueries),
			ReadinessReport:      cpadmin.AdaptReadinessReport(cand.operations.readinessReport),
			TokenAccountingAdmin: adminaccounting.AdaptCountCallService(cand.operations.tokenAccountingAdmin),
			KeepwarmAdmin:        keepwarmAdmin, KeepwarmAdminEnabled: keepwarmAdminEnabled,
			Registrations:          httpcontract.CloneRegistrations(regs),
			TerminalDecisionPolicy: terminalDecisionPolicyHTTPProjection(cand.process, cand.security.runtimeSnapshot, httpHeaders, maxBody),
		},
		Models: httpcontract.HTTPModelInput{
			CatalogRuntime: cand.models.catalog, ModelRegistryRuntime: cand.models.registryRuntime,
		},
		Frontends: httpcontract.HTTPFrontendInput{
			Executor:                  cand.execution.executor,
			Registry:                  cand.process.pluginRegistry,
			DefaultRouteSelector:      route,
			RoutePrefixes:             httpcontract.CloneStrings(cand.execution.routePrefixes),
			Plugins:                   httpcontract.ClonePluginConfigs(plugins),
			MaxRequestBodyBytes:       maxBody,
			DecodeAdmission:           cand.execution.decodeAdmission,
			TrafficPorts:              httpcontract.TrafficPortsFromSnapshot(cand.security.runtimeSnapshot),
			PreRequestKeepalive:       preKA,
			HTTPHeaders:               httpHeaders,
			StreamKeepaliveInterval:   streamKA,
			GenerationContext:         genCtx,
			ContinuationWiringFactory: standardplugins.StandardContinuationWiringFactory(frozen),
			FrontendRouteClaims:       standardplugins.StandardFrontendRouteClaims(),
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

func extensionsFromMerged(merged featurebundle.MergedFeatureSurface, genMerged featurebundle.GeneratedMergeSurface, processOpts *BuildOptions) ExtensionsOptions {
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
		TrafficObservers:                 lipfeature.Get(genMerged.Frozen, lipfeature.PlaneTrafficObservers),
		UsageObservers:                   lipfeature.Get(genMerged.Frozen, lipfeature.PlaneUsageObservers),
		CompactionObservers:              append(merged.CompactionObservers[:0:0], merged.CompactionObservers...),
		RawCaptureSinks:                  lipfeature.Get(genMerged.Frozen, lipfeature.PlaneRawCaptureSinks),
		TrafficRedactors:                 lipfeature.Get(genMerged.Frozen, lipfeature.PlaneTrafficRedactors),
		SecretGuards:                     append(merged.SecretGuards[:0:0], merged.SecretGuards...),
		LocalTurnHandlers:                append(merged.LocalTurnHandlers[:0:0], merged.LocalTurnHandlers...),
		TerminalDecisionProvider:         merged.TerminalDecisionProvider,
	}
	if processOpts != nil {
		ext.SecretGuardEnvironment, ext.SecretGuardInputs, ext.SecretDecisionObserver = processOpts.Extensions.SecretGuardEnvironment, processOpts.Extensions.SecretGuardInputs, processOpts.Extensions.SecretDecisionObserver
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
	dst.SecretGuards = append(dst.SecretGuards, src.SecretGuards...)
	dst.LocalTurnHandlers = append(dst.LocalTurnHandlers, src.LocalTurnHandlers...)
	if dst.TerminalDecisionProvider == nil {
		dst.TerminalDecisionProvider = src.TerminalDecisionProvider
	}
	if src.SecretGuardEnvironment != nil {
		dst.SecretGuardEnvironment = src.SecretGuardEnvironment
	}
	if src.SecretDecisionObserver != nil {
		dst.SecretDecisionObserver = src.SecretDecisionObserver
	}
}

func validateTerminalDecisionProvider(provider terminaldecision.Provider) error {
	if provider != nil {
		_, err := terminaldecision.ProviderIdentity(provider)
		return err
	}
	return nil
}
