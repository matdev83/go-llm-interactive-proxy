package runtimebundle

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg/standardbundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// BootstrapMode selects how much runtime assembly [BuildBootstrap] performs.
type BootstrapMode int

const (
	// BootstrapUnspecified is the zero value; callers must set [BuildBootstrapInput.Mode] to
	// [BootstrapInspect] or [BootstrapServe].
	BootstrapUnspecified BootstrapMode = iota
	// BootstrapInspect loads config, installs the standard registry, merges feature hooks, and
	// constructs the core app without calling [Build] (no executor, no listener).
	BootstrapInspect
	// BootstrapServe performs the inspect steps and then calls [Build] for stdhttp serving.
	BootstrapServe
)

// BuildBootstrapInput configures [BuildBootstrap] for the standard distribution composition root.
type BuildBootstrapInput struct {
	ConfigPath string
	Mode       BootstrapMode
	Mandatory  []lipsdk.Requirement
	// LogWriter receives logger output; nil means [os.Stdout].
	LogWriter               io.Writer
	StreamRecoveryOverrides config.StreamRecoveryOverrides
}

// BootstrapResult is the shared output of [BuildBootstrap] for inspect and serve commands.
type BootstrapResult struct {
	Config          *config.Config
	Logger          *slog.Logger
	Registry        *pluginreg.Registry
	Registrations   []lipsdk.Registration
	FeatureSurface  pluginreg.MergedFeatureSurface
	App             *BootstrapApp
	Built           *Built
	ShutdownTracing func(context.Context) error
	OutboundTracing bool
}

// BuildBootstrap centralizes standard-distribution startup used by lipstd inspect and serve paths.
func BuildBootstrap(ctx context.Context, in BuildBootstrapInput) (BootstrapResult, error) {
	var out BootstrapResult
	if ctx == nil {
		return out, fmt.Errorf("runtimebundle: nil context")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return out, fmt.Errorf("runtimebundle: empty config path")
	}
	if in.Mode != BootstrapInspect && in.Mode != BootstrapServe {
		return out, fmt.Errorf("runtimebundle: bootstrap mode must be inspect or serve")
	}
	logOut := in.LogWriter
	if logOut == nil {
		logOut = os.Stdout
	}

	cfg, err := config.LoadFile(path)
	if err != nil {
		return out, err
	}
	envOverrides, err := config.StreamRecoveryOverridesFromEnv()
	if err != nil {
		return out, err
	}
	mergedOverrides := mergeStreamRecoveryOverrides(envOverrides, in.StreamRecoveryOverrides)
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, mergedOverrides)
	if err != nil {
		return out, err
	}
	applyEffectiveStreamRecovery(cfg, eff)

	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		return out, err
	}

	traceRes, err := tracing.Init(ctx, cfg)
	if err != nil {
		return out, fmt.Errorf("runtimebundle: tracing init: %w", err)
	}
	out.ShutdownTracing = traceRes.Shutdown
	out.OutboundTracing = traceRes.Active

	logger, err := logging.NewLogger(cfg.Logging, logOut,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		_ = traceRes.Shutdown(context.Background())
		return out, fmt.Errorf("runtimebundle: logger init: %w", err)
	}
	out.Logger = logger

	reg := pluginreg.NewRegistry()
	apiKeys := pluginreg.ResolveUpstreamAPIKeysFromEnv()
	if err := standardbundle.InstallOn(reg, apiKeys); err != nil {
		_ = traceRes.Shutdown(context.Background())
		return out, fmt.Errorf("runtimebundle: plugin registration: %w", err)
	}
	if len(in.Mandatory) > 0 {
		if err := reg.ValidateBundledFactories(in.Mandatory); err != nil {
			_ = traceRes.Shutdown(context.Background())
			return out, fmt.Errorf("runtimebundle: registry factory validation: %w", err)
		}
	}

	regs := config.RegistrationsFromConfig(cfg)
	merged, err := reg.MergeFeatureSurface(regs)
	if err != nil {
		_ = traceRes.Shutdown(context.Background())
		return out, fmt.Errorf("runtimebundle: hook composition: %w", err)
	}
	merged.Hooks.ToolReactorErrorPolicy = config.ParseToolReactorErrorPolicy(cfg.Hooks.ToolReactorErrorPolicy)

	app, err := NewBootstrapApp(BootstrapOptions{
		Config:        cfg,
		Logger:        logger,
		Registrations: regs,
		Mandatory:     in.Mandatory,
		Hooks:         merged.Hooks,
		Lifecycles:    merged.Lifecycles,
	})
	if err != nil {
		_ = traceRes.Shutdown(context.Background())
		return out, fmt.Errorf("runtimebundle: runtime wiring: %w", err)
	}

	out.Config = cfg
	out.Registry = reg
	out.Registrations = regs
	out.FeatureSurface = merged
	out.App = app

	if in.Mode == BootstrapServe {
		built, err := Build(cfg, app.HookBus(), logger, &BuildOptions{
			PluginRegistry:     reg,
			OutboundTracing:    traceRes.Active,
			SessionOpeners:     merged.SessionOpeners,
			WorkspaceResolvers: merged.WorkspaceResolvers,
			ToolCatalogFilters: merged.ToolCatalogFilters,
			ToolCallPolicies:   merged.ToolCallPolicies,
			RequestTransforms:  merged.RequestTransforms,
			PreRequestHandlers: merged.PreRequestHandlers,
			RouteHintProviders: merged.RouteHintProviders,
			CompletionGates:    merged.CompletionGates,
			TrafficObservers:   merged.TrafficObservers,
			UsageObservers:     merged.UsageObservers,
			RawCaptureSinks:    merged.RawCaptureSinks,
			TrafficRedactors:   merged.TrafficRedactors,
		})
		if err != nil {
			_ = traceRes.Shutdown(context.Background())
			return out, fmt.Errorf("runtimebundle: runtime assembly: %w", err)
		}
		out.Built = built
	}

	return out, nil
}
