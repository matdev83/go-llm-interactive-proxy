package runtimebundle

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/osenv"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// Host is the process-owned complete host returned by [BuildHost].
type Host = ReloadHost

// BuildHostInput configures the one-snapshot [BuildHost] transaction.
type BuildHostInput struct {
	ConfigPath              string
	Mandatory               []lipsdk.Requirement
	LogWriter               io.Writer
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	HandlerComposer         HandlerComposer
	Production              ProductionOptions
	// EnforceMultiUserCLIGate enables the serve-only --multi-user CLI consistency
	// gate (req 4.3). Public Build leaves this false; cmd/lipstd serve sets true.
	EnforceMultiUserCLIGate bool
	MultiUser               *bool
}

// BuildHost constructs one complete process-owned Host from one accepted
// effective snapshot, or returns nil after internal rollback (req 4.1-4.8).
func BuildHost(ctx context.Context, in BuildHostInput) (*Host, error) {
	return buildHost(ctx, hostBuildInput(in), LoadBootstrapEffectiveWithSource, nil)
}

type hostBuildInput struct {
	ConfigPath              string
	Mandatory               []lipsdk.Requirement
	LogWriter               io.Writer
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	HandlerComposer         HandlerComposer
	Production              ProductionOptions
	EnforceMultiUserCLIGate bool
	MultiUser               *bool
}

// hostBuildStageName identifies a BuildHost transaction stage for the optional
// call-scoped probe (tests only; production passes nil).
type hostBuildStageName string

const (
	hostBuildStageNameLoader      hostBuildStageName = "loader"
	hostBuildStageNameTracing     hostBuildStageName = "tracing"
	hostBuildStageNameProcess     hostBuildStageName = "process"
	hostBuildStageNameCompile     hostBuildStageName = "compile"
	hostBuildStageNamePublish     hostBuildStageName = "publish"
	hostBuildStageNameCoordinator hostBuildStageName = "coordinator"
)

// hostBuildProbeEvent distinguishes acquisition from cleanup evidence.
type hostBuildProbeEvent string

const (
	hostBuildProbeAcquired hostBuildProbeEvent = "acquired"
	hostBuildProbeCleaned  hostBuildProbeEvent = "cleaned"
)

// hostBuildProbe is a per-invocation observer/fault injector. Production
// BuildHost passes nil. Returning a non-nil error from an "acquired" event
// injects failure after that stage's real resource exists so rollback exercises
// the same production cleanup branches.
type hostBuildProbe func(stage hostBuildStageName, event hostBuildProbeEvent) error

func buildHost(ctx context.Context, in hostBuildInput, loadEffective bootstrapEffectiveLoader, probe hostBuildProbe) (*ReloadHost, error) {
	return buildHostWithEnv(ctx, in, loadEffective, osenv.Process{}, probe)
}

func buildHostWithEnv(
	ctx context.Context,
	in hostBuildInput,
	loadEffective bootstrapEffectiveLoader,
	secretEnv coresg.Environment,
	probe hostBuildProbe,
) (*ReloadHost, error) {
	note := func(stage hostBuildStageName, event hostBuildProbeEvent) error {
		if probe == nil {
			return nil
		}
		return probe(stage, event)
	}

	if ctx == nil {
		return nil, fmt.Errorf("runtimebundle: nil context")
	}
	if loadEffective == nil {
		return nil, fmt.Errorf("runtimebundle: nil effective loader")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return nil, fmt.Errorf("runtimebundle: empty config path")
	}
	if in.HandlerComposer == nil {
		return nil, fmt.Errorf("runtimebundle: BuildHost requires HandlerComposer")
	}
	logOut := in.LogWriter
	if logOut == nil {
		logOut = os.Stdout
	}

	effective, activeSource, fixedStreamRecovery, err := loadEffective(ctx, path, in.StreamRecoveryOverrides)
	if err != nil {
		return nil, err
	}
	if effective == nil || effective.Config == nil {
		return nil, fmt.Errorf("runtimebundle: nil effective config")
	}
	if err := note(hostBuildStageNameLoader, hostBuildProbeAcquired); err != nil {
		return nil, err
	}
	cfg := effective.Config

	mode, err := cfg.EffectiveAccessMode()
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: access mode: %w", err)
	}
	if in.EnforceMultiUserCLIGate {
		if err := accessmode.ValidateServeModeGate(mode, in.MultiUser); err != nil {
			return nil, err
		}
	}

	traceRes, err := initProcessTracing(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: tracing init: %w", err)
	}
	traceShutdown := traceRes.Shutdown
	if err := note(hostBuildStageNameTracing, hostBuildProbeAcquired); err != nil {
		_ = note(hostBuildStageNameTracing, hostBuildProbeCleaned)
		shutdownTracing(ctx, traceShutdown)
		return nil, err
	}

	cleanupTracing := func() {
		_ = note(hostBuildStageNameTracing, hostBuildProbeCleaned)
		shutdownTracing(ctx, traceShutdown)
	}

	logger, err := logging.NewLogger(cfg.Logging, logOut,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		cleanupTracing()
		return nil, fmt.Errorf("runtimebundle: logger init: %w", err)
	}

	reg, err := installStandardHostRegistry(in.Mandatory)
	if err != nil {
		cleanupTracing()
		return nil, err
	}
	regs := config.RegistrationsFromConfig(cfg)
	if _, err := featuresg.EnabledRegistrations(regs); err != nil {
		cleanupTracing()
		return nil, fmt.Errorf("runtimebundle: secrets-guard composition: %w", err)
	}

	boot := BootstrapResult{
		Config:              cfg,
		Logger:              logger,
		Registry:            reg,
		Registrations:       regs,
		Effective:           effective,
		ActiveSource:        activeSource,
		FixedStreamRecovery: fixedStreamRecovery,
		ShutdownTracing:     traceShutdown,
		OutboundTracing:     traceRes.Active,
	}
	boot, err = publishInitialGeneration(ctx, boot, publishInitialGenerationInput{
		Cfg:           cfg,
		Effective:     effective,
		Logger:        logger,
		Registry:      reg,
		SecretEnv:     secretEnv,
		Production:    in.Production,
		Compose:       in.HandlerComposer,
		TraceActive:   traceRes.Active,
		TraceShutdown: traceShutdown,
		Probe:         probe,
	})
	if err != nil {
		return nil, err
	}

	host, err := bindReloadHost(path, bindReloadHostInput{
		Manager:             boot.GenerationManager,
		Process:             boot.ProcessServices,
		Compose:             in.HandlerComposer,
		Logger:              logger,
		Config:              cfg,
		Effective:           effective,
		ActiveSource:        activeSource,
		FixedStreamRecovery: fixedStreamRecovery,
		ShutdownTracing:     traceShutdown,
	})
	if err != nil {
		_ = note(hostBuildStageNamePublish, hostBuildProbeCleaned)
		_ = note(hostBuildStageNameCompile, hostBuildProbeCleaned)
		_ = note(hostBuildStageNameProcess, hostBuildProbeCleaned)
		return nil, joinInitialFailureCleanup(ctx, err, func() error {
			return boot.GenerationManager.ShutdownDetached(context.WithoutCancel(ctx), runtimehost.NewLifecycleWorker())
		}, boot.ProcessServices.Close, func(ctx context.Context) error {
			_ = note(hostBuildStageNameTracing, hostBuildProbeCleaned)
			return traceShutdown(ctx)
		})
	}
	if err := note(hostBuildStageNameCoordinator, hostBuildProbeAcquired); err != nil {
		_ = note(hostBuildStageNameCoordinator, hostBuildProbeCleaned)
		_ = note(hostBuildStageNamePublish, hostBuildProbeCleaned)
		_ = note(hostBuildStageNameCompile, hostBuildProbeCleaned)
		_ = note(hostBuildStageNameProcess, hostBuildProbeCleaned)
		host.BeginShutdown()
		return nil, joinInitialFailureCleanup(ctx, err, func() error {
			return host.Manager.ShutdownDetached(context.WithoutCancel(ctx), runtimehost.NewLifecycleWorker())
		}, host.Process.Close, func(ctx context.Context) error {
			_ = note(hostBuildStageNameTracing, hostBuildProbeCleaned)
			return traceShutdown(ctx)
		})
	}
	return host, nil
}
