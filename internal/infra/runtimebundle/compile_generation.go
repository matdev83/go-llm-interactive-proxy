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
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
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
	genRunner, boundClient, boundPoller, err := newReasoningCompressionGenerationRunner(ps)
	if err != nil {
		return nil, err
	}
	var host featurebundle.HostContributions
	if ps.opts != nil {
		host = featurebundle.HostContributions{TrafficObservers: slices.Clone(ps.opts.Production.TrafficObservers), UsageObservers: slices.Clone(ps.opts.Production.UsageObservers)}
	}
	var extraBundles []lipfeature.FeatureBundle
	genMerged, err := featurebundle.MergeFeatureSurfacesWithHost(ps.FactoryCatalog, regs, host, extraBundles...)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: feature surface: %w", err)
	}
	if in.CandidateOpts != nil && !in.CandidateOpts.FeaturePlanes.IsZero() {
		genMerged, err = genMerged.MergeCandidatePlanes(in.CandidateOpts.FeaturePlanes)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: candidate feature planes: %w", err)
		}
	}
	accessMode, err := frozen.EffectiveAccessMode()
	if err != nil {
		return nil, err
	}
	lifecycles := append([]lipplugin.Lifecycle(nil), genMerged.Lifecycles...)
	ext := extensionsFromProcessOptions(ps.opts)
	if in.CandidateOpts != nil {
		lifecycles = append(lifecycles, in.CandidateOpts.FeatureLifecycles...)
		overlayExtensions(&ext, in.CandidateOpts.Extensions)
	}
	nowFn := time.Now
	if ps.opts != nil && ps.opts.Testing.Clock != nil {
		nowFn = ps.opts.Testing.Clock
	}
	var kwAccounting billing.ProviderMaintenanceUsageObserver
	if ps.opts != nil {
		kwAccounting = ps.opts.Production.KeepwarmAccounting
	}
	if in.CandidateOpts != nil && in.CandidateOpts.Production.KeepwarmAccounting != nil {
		kwAccounting = in.CandidateOpts.Production.KeepwarmAccounting
	}
	featOut, err := ps.StandardFeatures.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations:      regs,
		MergeSurface:       genMerged,
		Planes:             genMerged.Frozen,
		Lifecycles:         lifecycles,
		BackgroundClient:   boundClient,
		BackgroundPoller:   boundPoller,
		ReasoningProdOpts:  reasoningCompressionProductionOptions(ps),
		ReasoningTestOpts:  reasoningCompressionTestingOptions(ps),
		AccessMode:         accessMode,
		ConfigInterleaved:  frozen.Interleaved,
		ConfigDir:          frozen.ConfigDir,
		SecretEnv:          ext.SecretGuardEnvironment,
		SecretInputs:       ext.SecretGuardInputs,
		DecisionObserver:   ext.SecretDecisionObserver,
		NowFn:              nowFn,
		KeepwarmAccounting: kwAccounting,
	})
	if err != nil {
		return nil, err
	}
	if ps.StandardFeatures != nil {
		ext.SecretGuard = &featOut.SecretGuard
		ext.SecretGuardInventory = featOut.SecretGuardInventory
	}
	toolReactorErrorPolicy := config.ParseToolReactorErrorPolicy(frozen.Hooks.ToolReactorErrorPolicy)
	bus := in.Bus
	if bus == nil {
		bus = hooks.New(lipfeature.ProjectHookConfig(featOut.Planes, toolReactorErrorPolicy))
	}
	cand, err := compileCandidate(ctx, GenerationCompileInput{
		Process: ps, Bus: bus, Candidate: frozen,
		CandidateOpts: &BuildOptions{
			FeatureLifecycles:       featOut.Lifecycles,
			Extensions:              ext,
			FeaturePlanes:           featOut.Planes,
			CorePorts:               featOut.CorePorts,
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
	cand.execution.executor.PromptCacheMaintenance = featOut.CorePorts.PromptCacheMaintenance
	if cand.process.metrics != nil && cand.process.metrics.Keepwarm != nil && featOut.KeepwarmManager != nil {
		cand.process.metrics.Keepwarm.SetManager(featOut.KeepwarmManager)
	}
	if retired, ok := cand.execution.executor.Store.(b2bua.ALegRetirementObserver); ok {
		retired.SetALegRetirementObserver(func(aLegID string) {
			if cand.execution.executor.PromptCacheMaintenance != nil {
				cand.execution.executor.PromptCacheMaintenance.EndSession(aLegID)
			}
			if deleter, ok := cand.execution.executor.ConversationViewTagger.(interface{ DeleteALeg(context.Context, string) error }); ok {
				_ = deleter.DeleteALeg(context.Background(), aLegID)
			}
		})
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
		// Publish the facade-composed planes as the generation's canonical
		// frozen surface (Task 2.4, Requirement 8.3): facade-added/replaced
		// planes must be visible to request-time bundle readers.
		frozen:          featOut.Planes,
		readiness:       cand.operations.readinessReport,
		keepwarmQuiesce: featOut.KeepwarmQuiesce,
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
			TerminalDecisionPolicy: featurehost.TerminalDecisionPolicyHTTPProjection(cand.process.standardFeatures, cand.security.runtimeSnapshot, httpHeaders, maxBody, cand.security.secureSessionStore),
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

func extensionsFromProcessOptions(processOpts *BuildOptions) ExtensionsOptions {
	if processOpts == nil {
		return ExtensionsOptions{}
	}
	return cloneExtensionsOptions(processOpts.Extensions)
}

func overlayExtensions(dst *ExtensionsOptions, src ExtensionsOptions) {
	if dst == nil {
		return
	}
	if src.SecretGuardEnvironment != nil {
		dst.SecretGuardEnvironment = src.SecretGuardEnvironment
	}
	if src.SecretDecisionObserver != nil {
		dst.SecretDecisionObserver = src.SecretDecisionObserver
	}
	if src.SecretGuard != nil {
		dst.SecretGuard = src.SecretGuard
	}
	if src.SecretGuardInventory != nil {
		dst.SecretGuardInventory = src.SecretGuardInventory
	}
}

func validateTerminalDecisionProvider(provider terminaldecision.Provider) error {
	if provider != nil {
		_, err := terminaldecision.ProviderIdentity(provider)
		return err
	}
	return nil
}
