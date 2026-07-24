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
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/osenv"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// BootstrapMode selects BuildBootstrap assembly. Only [BootstrapServe] remains
// (check-config until Task 5.4); routes/inventory use Inspect entrypoints.
type BootstrapMode int

const (
	BootstrapUnspecified BootstrapMode = iota
	BootstrapServe
)

// BuildBootstrapInput configures [BuildBootstrap] for check-config (BootstrapServe).
type BuildBootstrapInput struct {
	ConfigPath              string
	Mode                    BootstrapMode
	Mandatory               []lipsdk.Requirement
	LogWriter               io.Writer // nil means [os.Stdout]
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	Production              ProductionOptions
	HandlerComposer         HandlerComposer // required for BootstrapServe
}

// BootstrapResult is the BootstrapServe/check-config output (no App/FeatureSurface).
type BootstrapResult struct {
	Config              *config.Config
	Logger              *slog.Logger
	Registry            *pluginreg.Registry
	Registrations       []lipsdk.Registration
	ProcessServices     *ProcessServices
	GenerationManager   *runtimehost.Manager
	InitialGeneration   *runtimehost.Generation
	Effective           *config.EffectiveConfig
	ActiveSource        *configsource.ActiveSourceVersion
	FixedStreamRecovery config.StreamRecoveryOverrides
	ShutdownTracing     func(context.Context) error
	OutboundTracing     bool
}

func shutdownTracing(ctx context.Context, shutdown func(context.Context) error) {
	if shutdown == nil {
		return
	}
	_ = shutdown(context.WithoutCancel(ctx))
}

func initProcessTracing(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
	return tracing.Init(ctx, cfg)
}

// BuildBootstrap is the check-config composition path until Task 5.4. Serve/public
// Build use [BuildHost]; routes/inventory use [InspectRoutes]/[InspectInventory].
func BuildBootstrap(ctx context.Context, in BuildBootstrapInput) (BootstrapResult, error) {
	return buildBootstrap(ctx, in, osenv.Process{}, LoadBootstrapEffectiveWithSource)
}

// installRegistryAndRegistrations is the sole production InstallStandardBundleOn
// owner shared by BuildBootstrap, BuildHost, and Inspect entrypoints.
func installRegistryAndRegistrations(cfg *config.Config, mandatory []lipsdk.Requirement) (*pluginreg.Registry, []lipsdk.Registration, error) {
	reg := pluginreg.NewRegistry()
	apiKeys := standardplugins.ResolveUpstreamAPIKeysFromEnv()
	if err := standardplugins.InstallStandardBundleOn(reg, apiKeys); err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: plugin registration: %w", err)
	}
	if len(mandatory) > 0 {
		if err := reg.ValidateBundledFactories(mandatory); err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: registry factory validation: %w", err)
		}
	}
	regs := config.RegistrationsFromConfig(cfg)
	if _, err := featuresg.EnabledRegistrations(regs); err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: secrets-guard composition: %w", err)
	}
	return reg, regs, nil
}

func buildBootstrap(ctx context.Context, in BuildBootstrapInput, secretEnv coresg.Environment, loadEffective bootstrapEffectiveLoader) (BootstrapResult, error) {
	var out BootstrapResult
	if ctx == nil {
		return out, fmt.Errorf("runtimebundle: nil context")
	}
	if loadEffective == nil {
		loadEffective = LoadBootstrapEffectiveWithSource
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return out, fmt.Errorf("runtimebundle: empty config path")
	}
	if in.Mode != BootstrapServe {
		return out, fmt.Errorf("runtimebundle: bootstrap mode must be serve")
	}
	logOut := in.LogWriter
	if logOut == nil {
		logOut = os.Stdout
	}

	effective, activeSource, fixedStreamRecovery, err := loadEffective(ctx, path, in.StreamRecoveryOverrides)
	if err != nil {
		return out, err
	}
	cfg := effective.Config
	out.Effective = effective
	out.ActiveSource = activeSource
	out.FixedStreamRecovery = fixedStreamRecovery

	traceRes, err := initProcessTracing(ctx, cfg)
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

	reg, regs, err := installRegistryAndRegistrations(cfg, in.Mandatory)
	if err != nil {
		shutdownTracing(ctx, traceRes.Shutdown)
		return out, err
	}
	out.Config = cfg
	out.Registry = reg
	out.Registrations = regs

	if in.HandlerComposer == nil {
		shutdownTracing(ctx, traceRes.Shutdown)
		return out, fmt.Errorf("runtimebundle: BootstrapServe requires HandlerComposer")
	}
	return publishInitialGeneration(ctx, out, publishInitialGenerationInput{
		Cfg:           cfg,
		Effective:     effective,
		Logger:        logger,
		Registry:      reg,
		SecretEnv:     secretEnv,
		Production:    in.Production,
		Compose:       in.HandlerComposer,
		TraceActive:   traceRes.Active,
		TraceShutdown: traceRes.Shutdown,
	})
}
