package runtimebundle

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/osenv"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
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
	// Production carries first-class enterprise injection seams (requirement 12.4).
	Production ProductionOptions
}

// BootstrapResult is the shared output of [BuildBootstrap] for inspect and serve commands.
type BootstrapResult struct {
	Config          *config.Config
	Logger          *slog.Logger
	Registry        *pluginreg.Registry
	Registrations   []lipsdk.Registration
	FeatureSurface  featurebundle.MergedFeatureSurface
	App             *BootstrapApp
	Built           *Built
	ShutdownTracing func(context.Context) error
	OutboundTracing bool
}

// shutdownTracing invokes the provided shutdown func with a value-preserving
// downstream ctx detached from caller cancellation. Used in BuildBootstrap
// error paths so tracing teardown completes even when the request is canceled
// mid-startup. Resolves golang-context rule 7 (context.Background only at
// top-level), rule 8 (never create Background mid-request), and rule 11
// (use context.WithoutCancel for background work that outlives the parent).
func shutdownTracing(ctx context.Context, shutdown func(context.Context) error) {
	if shutdown == nil {
		return
	}
	_ = shutdown(context.WithoutCancel(ctx))
}

// BuildBootstrap centralizes standard-distribution startup used by lipstd inspect and serve paths.
func BuildBootstrap(ctx context.Context, in BuildBootstrapInput) (BootstrapResult, error) {
	return buildBootstrap(ctx, in, osenv.Process{})
}

func buildBootstrap(ctx context.Context, in BuildBootstrapInput, secretEnv coresg.Environment) (BootstrapResult, error) {
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

	effective, err := LoadBootstrapEffective(ctx, path, in.StreamRecoveryOverrides)
	if err != nil {
		return out, err
	}
	cfg := effective.Config

	traceRes, err := tracing.Init(ctx, cfg)
	if err != nil {
		return out, fmt.Errorf("runtimebundle: tracing init: %w", err)
	}
	out.ShutdownTracing = traceRes.Shutdown
	out.OutboundTracing = traceRes.Active

	logger, err := logging.NewLogger(cfg.Logging, logOut,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		shutdownTracing(ctx, traceRes.Shutdown)
		return out, fmt.Errorf("runtimebundle: logger init: %w", err)
	}
	out.Logger = logger

	reg := pluginreg.NewRegistry()
	apiKeys := standardplugins.ResolveUpstreamAPIKeysFromEnv()
	if err := standardplugins.InstallStandardBundleOn(reg, apiKeys); err != nil {
		shutdownTracing(ctx, traceRes.Shutdown)
		return out, fmt.Errorf("runtimebundle: plugin registration: %w", err)
	}
	if len(in.Mandatory) > 0 {
		if err := reg.ValidateBundledFactories(in.Mandatory); err != nil {
			shutdownTracing(ctx, traceRes.Shutdown)
			return out, fmt.Errorf("runtimebundle: registry factory validation: %w", err)
		}
	}

	regs := config.RegistrationsFromConfig(cfg)
	// Secrets-guard uniqueness is owned by the feature package; enforce it at the
	// composition root so inspect and serve both fail closed before merge/build.
	if _, err := featuresg.EnabledRegistrations(regs); err != nil {
		shutdownTracing(ctx, traceRes.Shutdown)
		return out, fmt.Errorf("runtimebundle: secrets-guard composition: %w", err)
	}
	merged, err := featurebundle.MergeFeatureSurface(reg, regs)
	if err != nil {
		shutdownTracing(ctx, traceRes.Shutdown)
		return out, fmt.Errorf("runtimebundle: hook composition: %w", err)
	}
	merged.ToolReactorErrorPolicy = config.ParseToolReactorErrorPolicy(cfg.Hooks.ToolReactorErrorPolicy)

	app, err := NewBootstrapApp(BootstrapOptions{
		Config:        cfg,
		Logger:        logger,
		Registrations: regs,
		Mandatory:     in.Mandatory,
		Hooks:         hooksConfigFromMerged(merged),
		Lifecycles:    merged.Lifecycles,
	})
	if err != nil {
		shutdownTracing(ctx, traceRes.Shutdown)
		return out, fmt.Errorf("runtimebundle: runtime wiring: %w", err)
	}

	out.Config = cfg
	out.Registry = reg
	out.Registrations = regs
	out.FeatureSurface = merged
	out.App = app

	if in.Mode == BootstrapServe {
		built, err := Build(cfg, app.HookBus(), logger, &BuildOptions{
			PluginRegistry: reg,
			Infra: InfraOptions{
				OutboundTracing: traceRes.Active,
				ProcessTracing: ProcessTracing{
					Shutdown: traceRes.Shutdown,
					Active:   traceRes.Active,
				},
			},
			Extensions: ExtensionsOptions{
				SessionOpeners:                   merged.SessionOpeners,
				WorkspaceResolvers:               merged.WorkspaceResolvers,
				ToolCatalogFilters:               merged.ToolCatalogFilters,
				ToolCallPolicies:                 merged.ToolCallPolicies,
				ToolCallFinalizers:               merged.ToolCallFinalizers,
				ToolCallFinalizationMaxArgsBytes: merged.ToolCallFinalizationMaxArgsBytes,
				RequestTransforms:                merged.RequestTransforms,
				PreRequestHandlers:               merged.PreRequestHandlers,
				RouteHintProviders:               merged.RouteHintProviders,
				CompletionGates:                  merged.CompletionGates,
				AttemptTransforms:                merged.AttemptTransforms,
				StreamObserverFactories:          merged.StreamObserverFactories,
				TrafficObservers:                 append(append([]traffic.Observer(nil), merged.TrafficObservers...), in.Production.TrafficObservers...),
				UsageObservers:                   append(append([]usage.Observer(nil), merged.UsageObservers...), in.Production.UsageObservers...),
				RawCaptureSinks:                  merged.RawCaptureSinks,
				TrafficRedactors:                 merged.TrafficRedactors,
				SecretGuards:                     merged.SecretGuards,
				SecretGuardEnvironment:           secretEnv,
			},
			Production: in.Production,
		})
		if err != nil {
			shutdownTracing(ctx, traceRes.Shutdown)
			return out, fmt.Errorf("runtimebundle: runtime assembly: %w", err)
		}
		out.Built = built
	}

	return out, nil
}
