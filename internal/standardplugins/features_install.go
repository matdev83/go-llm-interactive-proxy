package standardplugins

import (
	"fmt"

	corerepair "github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/codexclientcompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/partsnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/prerequestpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refparts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refsubmit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftoolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftraffictranscript"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refverifier"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refworkspaceguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolreactornoop"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"gopkg.in/yaml.v3"
)

func featureAgentLoopGuard(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := agentloopguard.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	if !cfg.Enabled {
		return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}, nil
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, agentloopguard.ID, agentloopguard.NewProvider(cfg)); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", agentloopguard.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureSubmitNoop(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := submitnoop.DecodeHookConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, submitnoop.ID, []sdk.SubmitHook{submitnoop.NewSubmitHookWithConfig(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", submitnoop.ID, err)
	}
	var lifecycles []lipplugin.Lifecycle
	if cfg.LifecycleProbe {
		lifecycles = append(lifecycles, submitnoop.NewLifecycleProbeForConfig())
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), lifecycles), nil
}

func featurePartsNoop(n yaml.Node) (lipfeature.FeatureBundle, error) {
	if err := requireEmptyFeatureYAML(partsnoop.ID, n); err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, partsnoop.ID, []sdk.RequestPartHook{partsnoop.NewRequestPartHook()}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", partsnoop.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, partsnoop.ID, []sdk.ResponsePartHook{partsnoop.NewResponsePartHook()}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", partsnoop.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureToolReactorNoop(n yaml.Node) (lipfeature.FeatureBundle, error) {
	if err := requireEmptyFeatureYAML(toolreactornoop.ID, n); err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, toolreactornoop.ID, []sdk.ToolReactor{toolreactornoop.NewToolReactor()}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", toolreactornoop.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefSubmit(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refsubmit.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, refsubmit.ID, []sdk.SubmitHook{refsubmit.NewSubmitHook(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refsubmit.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefParts(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refparts.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, refparts.ID, []sdk.RequestPartHook{refparts.NewRequestPartHook(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refparts.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, refparts.ID, []sdk.ResponsePartHook{refparts.NewResponsePartHook(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refparts.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefTool(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := reftool.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, reftool.ID, []sdk.ToolReactor{reftool.NewToolReactor(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftool.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefAutoappend(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refautoappend.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneSessionOpeners, refautoappend.ID, []session.Opener{refautoappend.NewSessionOpener()}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refautoappend.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestTransforms, refautoappend.ID, []request.Transform{refautoappend.NewRequestTransform(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refautoappend.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefToolPolicy(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := reftoolpolicy.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCatalogFilters, reftoolpolicy.ID, []toolcatalog.Filter{reftoolpolicy.NewToolCatalogFilter(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftoolpolicy.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallPolicies, reftoolpolicy.ID, []toolpolicy.Policy{reftoolpolicy.NewToolCallPolicy(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftoolpolicy.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, reftoolpolicy.ID, []sdk.ToolReactor{reftoolpolicy.NewToolReactor(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftoolpolicy.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefWorkspaceGuard(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refworkspaceguard.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneWorkspaceResolvers, refworkspaceguard.ID, []workspace.Resolver{refworkspaceguard.NewStaticResolver(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refworkspaceguard.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestTransforms, refworkspaceguard.ID, []request.Transform{refworkspaceguard.NewSessionUnlockTransform(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refworkspaceguard.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCatalogFilters, refworkspaceguard.ID, []toolcatalog.Filter{refworkspaceguard.NewCatalogFilter(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refworkspaceguard.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, refworkspaceguard.ID, []sdk.ToolReactor{refworkspaceguard.NewHeatReactor(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refworkspaceguard.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefTrafficTranscript(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := reftraffictranscript.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneTrafficObservers, reftraffictranscript.ID, []sdktraffic.Observer{reftraffictranscript.NewTranscript()}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftraffictranscript.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneUsageObservers, reftraffictranscript.ID, []usage.Observer{reftraffictranscript.NewUsageLedger()}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftraffictranscript.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneRawCaptureSinks, reftraffictranscript.ID, []sdktraffic.RawCaptureSink{reftraffictranscript.NewRawLog()}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftraffictranscript.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneTrafficRedactors, reftraffictranscript.ID, []sdktraffic.Redactor{reftraffictranscript.NewPatternRedactor(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftraffictranscript.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefVerifier(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refverifier.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneCompletionGates, refverifier.ID, []completion.Gate{refverifier.NewCompletionGate(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refverifier.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featurePreRequestPolicy(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := prerequestpolicy.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	handlers, err := prerequestpolicy.NewHandlers(cfg)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlanePreRequestHandlers, prerequestpolicy.ID, handlers); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", prerequestpolicy.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureCodexClientCompat(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := codexclientcompat.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, codexclientcompat.ID, []sdk.RequestPartHook{codexclientcompat.NewRequestPartHook(cfg)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", codexclientcompat.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureSecretGuard(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := secretguard.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return secretguard.FeatureBundle(cfg), nil
}

func featureReasoningOutputPreservation(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := reasoningpreservation.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	if cfg.Compression.Enabled {
		// Configuration factory must not fail closed on generation-bound capabilities.
		// Enabled compression is validated for config correctness here, but the
		// actual AttemptTransform/StreamObserver is injected at CompileGeneration
		// via bindReasoningPreservationCompression using process BackgroundAux
		// and trusted egress/matcher resolvers.
		return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}, nil
	}
	return reasoningpreservation.FeatureBundleWithCompanionPolicy(cfg, codexCompanionPolicy())
}

func featureCompactionContinuity(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := compactioncontinuity.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return compactioncontinuity.FeatureBundle(cfg), nil
}

func featureToolCallRepair(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := toolcallrepair.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	fin := corerepair.NewFinalizer(corerepair.FinalizerPolicy{
		ID:             toolcallrepair.ID,
		MaxArgsBytes:   cfg.MaxArgsBytes,
		OnUnrepairable: cfg.OnUnrepairable,
		Order:          cfg.FinalizerOrder(),
		Schema: corerepair.SchemaLimits{
			MaxSchemaBytes:   cfg.Schema.MaxSchemaBytes,
			MaxNestingDepth:  cfg.Schema.MaxNestingDepth,
			MaxNodes:         cfg.Schema.MaxNodes,
			MaxProperties:    cfg.Schema.MaxProperties,
			MaxLocalRefDepth: cfg.Schema.MaxLocalRefDepth,
			MaxCacheEntries:  cfg.Schema.MaxCacheEntries,
			MaxCacheBytes:    cfg.Schema.MaxCacheBytes,
		},
	})
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, toolcallrepair.ID, []toolcall.Finalizer{fin}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", toolcallrepair.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, toolcallrepair.ID, cfg.MaxArgsBytes); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", toolcallrepair.ID, err)
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}
