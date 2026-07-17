package standardplugins

import (
	corerepair "github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/codexclientcompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/partsnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/prerequestpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refparts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refsubmit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftoolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftraffictranscript"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refverifier"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refworkspaceguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolreactornoop"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
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

func featureSubmitNoop(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := submitnoop.DecodeHookConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	bundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SubmitHooks:   []sdk.SubmitHook{submitnoop.NewSubmitHookWithConfig(cfg)},
	}
	if cfg.LifecycleProbe {
		bundle.Lifecycles = append(bundle.Lifecycles, &submitnoop.LifecycleProbe{})
	}
	return bundle, nil
}

func featurePartsNoop(n yaml.Node) (lipfeature.FeatureBundle, error) {
	if err := requireEmptyFeatureYAML(partsnoop.ID, n); err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:     lipfeature.SchemaVersionV1,
		RequestPartHooks:  []sdk.RequestPartHook{partsnoop.NewRequestPartHook()},
		ResponsePartHooks: []sdk.ResponsePartHook{partsnoop.NewResponsePartHook()},
	}, nil
}

func featureToolReactorNoop(n yaml.Node) (lipfeature.FeatureBundle, error) {
	if err := requireEmptyFeatureYAML(toolreactornoop.ID, n); err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolReactors:  []sdk.ToolReactor{toolreactornoop.NewToolReactor()},
	}, nil
}

func featureRefSubmit(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refsubmit.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SubmitHooks:   []sdk.SubmitHook{refsubmit.NewSubmitHook(cfg)},
	}, nil
}

func featureRefParts(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refparts.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:     lipfeature.SchemaVersionV1,
		RequestPartHooks:  []sdk.RequestPartHook{refparts.NewRequestPartHook(cfg)},
		ResponsePartHooks: []sdk.ResponsePartHook{refparts.NewResponsePartHook(cfg)},
	}, nil
}

func featureRefTool(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := reftool.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolReactors:  []sdk.ToolReactor{reftool.NewToolReactor(cfg)},
	}, nil
}

func featureRefAutoappend(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refautoappend.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:     lipfeature.SchemaVersionV1,
		SessionOpeners:    []session.Opener{refautoappend.NewSessionOpener()},
		RequestTransforms: []request.Transform{refautoappend.NewRequestTransform(cfg)},
	}, nil
}

func featureRefToolPolicy(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := reftoolpolicy.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		ToolCatalogFilters: []toolcatalog.Filter{reftoolpolicy.NewToolCatalogFilter(cfg)},
		ToolCallPolicies:   []toolpolicy.Policy{reftoolpolicy.NewToolCallPolicy(cfg)},
		ToolReactors:       []sdk.ToolReactor{reftoolpolicy.NewToolReactor(cfg)},
	}, nil
}

func featureRefWorkspaceGuard(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refworkspaceguard.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		WorkspaceResolvers: []workspace.Resolver{refworkspaceguard.NewStaticResolver(cfg)},
		RequestTransforms:  []request.Transform{refworkspaceguard.NewSessionUnlockTransform(cfg)},
		ToolCatalogFilters: []toolcatalog.Filter{refworkspaceguard.NewCatalogFilter(cfg)},
		ToolReactors:       []sdk.ToolReactor{refworkspaceguard.NewHeatReactor(cfg)},
	}, nil
}

func featureRefTrafficTranscript(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := reftraffictranscript.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:    lipfeature.SchemaVersionV1,
		TrafficObservers: []sdktraffic.Observer{reftraffictranscript.NewTranscript()},
		UsageObservers:   []usage.Observer{reftraffictranscript.NewUsageLedger()},
		RawCaptureSinks:  []sdktraffic.RawCaptureSink{reftraffictranscript.NewRawLog()},
		TrafficRedactors: []sdktraffic.Redactor{reftraffictranscript.NewPatternRedactor(cfg)},
	}, nil
}

func featureRefVerifier(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := refverifier.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:   lipfeature.SchemaVersionV1,
		CompletionGates: []completion.Gate{refverifier.NewCompletionGate(cfg)},
	}, nil
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
	return lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		PreRequestHandlers: handlers,
	}, nil
}

func featureCodexClientCompat(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := codexclientcompat.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.FeatureBundle{
		SchemaVersion:    lipfeature.SchemaVersionV1,
		RequestPartHooks: []sdk.RequestPartHook{codexclientcompat.NewRequestPartHook(cfg)},
	}, nil
}

func featureSecretsGuard(n yaml.Node) (lipfeature.FeatureBundle, error) {
	cfg, err := secretsguard.DecodeConfig(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return secretsguard.FeatureBundle(cfg), nil
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
	return lipfeature.FeatureBundle{
		SchemaVersion:                    lipfeature.SchemaVersionV1,
		ToolCallFinalizers:               []toolcall.Finalizer{fin},
		ToolCallFinalizationMaxArgsBytes: cfg.MaxArgsBytes,
	}, nil
}
