package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/osenv"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

type BuildHostInput struct {
	ConfigPath              string
	Mandatory               []lipsdk.Requirement
	LogWriter               io.Writer
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	HandlerComposer         HandlerComposer
	Production              ProductionOptions
	EnforceMultiUserCLIGate bool
	MultiUser               *bool
}

func BuildHost(ctx context.Context, in BuildHostInput) (*Host, error) {
	return buildHost(ctx, hostBuildInput(in), defaultHostBuildOps(), osenv.Process{})
}

type hostBuildInput = BuildHostInput

func buildHost(ctx context.Context, in hostBuildInput, ops hostBuildOps, secretEnv coresg.Environment) (*Host, error) {
	if ctx == nil {
		return nil, fmt.Errorf("runtimebundle: nil context")
	}
	if ops.load == nil || ops.tracing == nil || ops.process == nil || ops.compile == nil || ops.publisher == nil {
		return nil, fmt.Errorf("runtimebundle: incomplete host build operations")
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

	effective, activeSource, fixedStreamRecovery, err := ops.load(ctx, path, in.StreamRecoveryOverrides)
	if err != nil {
		return nil, err
	}
	if effective == nil || effective.Config == nil {
		return nil, fmt.Errorf("runtimebundle: nil effective config")
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

	traceRes, err := ops.tracing(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: tracing init: %w", err)
	}
	traceShutdown := traceRes.Shutdown
	shutTracing := func(ctx context.Context) error {
		if traceShutdown == nil {
			return nil
		}
		return traceShutdown(context.WithoutCancel(ctx))
	}

	logger, err := logging.NewLogger(cfg.Logging, logOut,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: logger init: %w", err), nil, nil, shutTracing)
	}

	reg, _, err := installRegistryAndRegistrations(cfg, in.Mandatory)
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, err, nil, nil, shutTracing)
	}

	discInstall, err := installDiscoveredBackendExports(cfg, reg)
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, err, nil, nil, shutTracing)
	}
	var pluginHost *processhost.Host
	var pluginStaging string
	var pluginArtifacts []*trust.VerifiedArtifact
	if discInstall != nil {
		pluginHost, pluginStaging = discInstall.Host, discInstall.StagingDir
		pluginArtifacts = discInstall.Artifacts
	}

	ps, err := ops.process(ctx, processBuildInput{
		Cfg: cfg, Logger: logger, Registry: reg, SecretEnv: secretEnv, Production: in.Production,
		Tracing:          ProcessTracing{Shutdown: traceShutdown, Active: traceRes.Active},
		PluginHost:       pluginHost,
		PluginArtifacts:  pluginArtifacts,
		PluginStagingDir: pluginStaging,
	})
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: process services: %w", err), nil, nil, shutTracing)
	}
	if !ps.Tracing.Active && traceRes.Active {
		ps.Tracing.Active = true
	}
	closeProcess := ps.Close

	bundle, err := ops.compile(ctx, ps, cfg, in.HandlerComposer)
	if err != nil {
		return nil, joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: compile initial generation: %w", err), nil, closeProcess, shutTracing)
	}

	mgr, _, err := ops.publisher(ctx, initialPublishInput{Process: ps, Bundle: bundle, Effective: effective})
	if err != nil {
		_ = bundle.Quiesce(context.WithoutCancel(ctx))
		_ = bundle.Close()
		return nil, joinInitialFailureCleanup(ctx, err, nil, closeProcess, shutTracing)
	}

	host, err := bindHost(path, bindHostInput{
		Manager:             mgr,
		Process:             ps,
		Compose:             in.HandlerComposer,
		Logger:              logger,
		Config:              cfg,
		Effective:           effective,
		ActiveSource:        activeSource,
		FixedStreamRecovery: fixedStreamRecovery,
		ShutdownTracing:     traceShutdown,
	})
	if err == nil && ops.afterBind != nil {
		err = ops.afterBind()
	}
	if err != nil {
		if host != nil {
			if cleanupErr := omitSoleAlreadyClosed(host.Close(context.WithoutCancel(ctx))); cleanupErr != nil {
				return nil, errors.Join(err, cleanupErr)
			}
			return nil, err
		}
		return nil, joinInitialFailureCleanup(ctx, err, func() error {
			return mgr.ShutdownDetached(context.WithoutCancel(ctx))
		}, closeProcess, shutTracing)
	}
	return host, nil
}

func buildHostWithEnv(ctx context.Context, in hostBuildInput, loadEffective bootstrapEffectiveLoader, secretEnv coresg.Environment, _ any) (*Host, error) {
	ops := defaultHostBuildOps()
	if loadEffective != nil {
		ops.load = loadEffective
	}
	return buildHost(ctx, in, ops, secretEnv)
}

type (
	effectiveLoader      = bootstrapEffectiveLoader
	tracingInitializer   func(ctx context.Context, cfg *config.Config) (tracing.Result, error)
	processBuilder       func(ctx context.Context, in processBuildInput) (*ProcessServices, error)
	generationCompilerOp func(ctx context.Context, ps *ProcessServices, cfg *config.Config, compose HandlerComposer) (GenerationRuntime, error)
	initialPublisher     func(ctx context.Context, in initialPublishInput) (*runtimehost.Manager, *runtimehost.Generation, error)
	registryInstaller    func(cfg *config.Config, mandatory []lipsdk.Requirement) (*pluginreg.Registry, []lipsdk.Registration, error)
)

type hostBuildOps struct {
	load      effectiveLoader
	tracing   tracingInitializer
	process   processBuilder
	compile   generationCompilerOp
	publisher initialPublisher
	// afterBind is an optional leaf fault hook (no owner authority); nil in production.
	afterBind func() error
}

type processBuildInput struct {
	Cfg              *config.Config
	Logger           *slog.Logger
	Registry         *pluginreg.Registry
	SecretEnv        coresg.Environment
	Production       ProductionOptions
	Tracing          ProcessTracing
	PluginHost       *processhost.Host
	PluginArtifacts  []*trust.VerifiedArtifact
	PluginStagingDir string
}
type initialPublishInput struct {
	Process   *ProcessServices
	Bundle    GenerationRuntime
	Effective *config.EffectiveConfig
}

func defaultHostBuildOps() hostBuildOps {
	return hostBuildOps{
		load: LoadBootstrapEffectiveWithSource, tracing: initProcessTracing,
		process: buildProcessServicesOp, compile: compileInitialGenerationOp,
		publisher: publishStartupGenerationOp,
	}
}

func buildProcessServicesOp(ctx context.Context, in processBuildInput) (*ProcessServices, error) {
	return NewProcessServices(ctx, ProcessServicesInput{
		Cfg: in.Cfg, Log: in.Logger,
		Opts: &BuildOptions{
			PluginRegistry: in.Registry,
			Infra:          InfraOptions{OutboundTracing: in.Tracing.Active, ProcessTracing: in.Tracing},
			Extensions:     ExtensionsOptions{SecretGuardEnvironment: in.SecretEnv},
			Production:     in.Production,
		},
		Tracing:          in.Tracing,
		PluginHost:       in.PluginHost,
		PluginArtifacts:  in.PluginArtifacts,
		PluginStagingDir: in.PluginStagingDir,
	})
}

func compileInitialGenerationOp(ctx context.Context, ps *ProcessServices, cfg *config.Config, compose HandlerComposer) (GenerationRuntime, error) {
	return CompileGeneration(ctx, GenerationCompileInput{Process: ps, Candidate: cfg, Compose: compose})
}

func publishStartupGenerationOp(ctx context.Context, in initialPublishInput) (*runtimehost.Manager, *runtimehost.Generation, error) {
	if in.Process == nil {
		return nil, nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if in.Bundle == nil {
		return nil, nil, fmt.Errorf("runtimebundle: nil generation bundle")
	}
	mgr := runtimehost.NewManager(DefaultMaxRetainedGenerations, nil)
	gen := mgr.PrepareRequestPlane("startup", in.Bundle)
	hints := runtimehost.MetaHints{TriggerKind: "startup", LoadedAt: time.Now().UTC()}
	if in.Effective != nil {
		hints.PublicFingerprint = in.Effective.Identity.PublicFingerprint
		if !in.Effective.LoadedAt.IsZero() {
			hints.LoadedAt = in.Effective.LoadedAt
		}
	}
	gen.SetMetaHints(hints)
	if err := mgr.Publish(gen); err != nil {
		_ = gen.Discard()
		return nil, nil, fmt.Errorf("runtimebundle: publish initial generation: %w", err)
	}
	if gen.ID() != 1 {
		_ = mgr.ShutdownDetached(context.WithoutCancel(ctx))
		return nil, nil, fmt.Errorf("runtimebundle: initial generation id=%d want 1", gen.ID())
	}
	if in.Process.terminalWorkRT != nil {
		in.Process.terminalWorkRT.BindGenerationManager(mgr)
	}
	return mgr, gen, nil
}

func installRegistryOp(cfg *config.Config, mandatory []lipsdk.Requirement) (*pluginreg.Registry, []lipsdk.Registration, error) {
	return installRegistryAndRegistrations(cfg, mandatory)
}

const DefaultMaxRetainedGenerations = 8

func joinInitialFailureCleanup(ctx context.Context, primary error, genRollback, processClose func() error, traceShutdown func(context.Context) error) error {
	var cleanup error
	if genRollback != nil {
		if err := omitSoleAlreadyClosed(genRollback()); err != nil {
			cleanup = errors.Join(cleanup, err)
		}
	}
	if processClose != nil {
		if err := processClose(); err != nil {
			cleanup = errors.Join(cleanup, err)
		}
	}
	if traceShutdown != nil {
		if err := traceShutdown(context.WithoutCancel(ctx)); err != nil {
			cleanup = errors.Join(cleanup, err)
		}
	}
	if cleanup != nil {
		return errors.Join(primary, cleanup)
	}
	return primary
}

func omitSoleAlreadyClosed(err error) error {
	if err == nil || err == runtimehost.ErrAlreadyClosed {
		return nil
	}
	if !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		return err
	}
	if m, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range m.Unwrap() {
			if omitSoleAlreadyClosed(e) != nil {
				return err
			}
		}
		return nil
	}
	u := errors.Unwrap(err)
	if u == nil || omitSoleAlreadyClosed(u) == nil {
		return nil
	}
	return err
}
