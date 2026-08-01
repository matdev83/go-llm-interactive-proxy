package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/osenv"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

type ValidateDistributionInput struct {
	ConfigPath              string
	Mandatory               []lipsdk.Requirement
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	Production              ProductionOptions
	HandlerComposer         HandlerComposer
}

type validateDistributionOps struct {
	hostBuildOps
	registry registryInstaller
}

func defaultValidateDistributionOps() validateDistributionOps {
	return validateDistributionOps{hostBuildOps: defaultHostBuildOps(), registry: installRegistryOp}
}

func ValidateDistribution(ctx context.Context, in ValidateDistributionInput) error {
	return validateDistribution(ctx, in, osenv.Process{}, defaultValidateDistributionOps())
}

func validateDistribution(
	ctx context.Context,
	in ValidateDistributionInput,
	secretEnv coresg.Environment,
	ops validateDistributionOps,
) error {
	if ctx == nil {
		return fmt.Errorf("runtimebundle: nil context")
	}
	if ops.load == nil || ops.tracing == nil || ops.registry == nil || ops.process == nil || ops.compile == nil {
		return fmt.Errorf("runtimebundle: incomplete validate distribution operations")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return fmt.Errorf("runtimebundle: empty config path")
	}
	if in.HandlerComposer == nil {
		return fmt.Errorf("runtimebundle: ValidateDistribution requires HandlerComposer")
	}

	effective, _, _, err := ops.load(ctx, path, in.StreamRecoveryOverrides)
	if err != nil {
		return err
	}
	if effective == nil || effective.Config == nil {
		return fmt.Errorf("runtimebundle: nil effective config")
	}
	cfg := effective.Config

	var shutTracing func(context.Context) error
	fail := func(err error, rollback, closeProcess func() error) error {
		return joinInitialFailureCleanup(ctx, err, rollback, closeProcess, shutTracing)
	}

	traceRes, err := ops.tracing(ctx, cfg)
	if err != nil {
		return fmt.Errorf("runtimebundle: tracing init: %w", err)
	}
	traceShutdownRaw := traceRes.Shutdown
	shutTracing = func(ctx context.Context) error {
		if traceShutdownRaw == nil {
			return nil
		}
		return traceShutdownRaw(ctx)
	}

	reg, _, err := ops.registry(cfg, in.Mandatory)
	if err != nil {
		return fail(err, nil, nil)
	}

	discInstall, err := installDiscoveredBackendExports(cfg, reg)
	if err != nil {
		return fail(err, nil, nil)
	}
	var pluginHost *processhost.Host
	var pluginStaging string
	var pluginArtifacts []*trust.VerifiedArtifact
	if discInstall != nil {
		pluginHost, pluginStaging = discInstall.Host, discInstall.StagingDir
		pluginArtifacts = discInstall.Artifacts
	}

	logger, err := logging.NewLogger(cfg.Logging, io.Discard,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		if discInstall != nil {
			discInstall.release()
		}
		return fail(fmt.Errorf("runtimebundle: logger init: %w", err), nil, nil)
	}

	ps, err := ops.process(ctx, processBuildInput{
		Cfg: cfg, Logger: logger, Registry: reg, SecretEnv: secretEnv, Production: in.Production,
		Tracing:          ProcessTracing{Shutdown: traceShutdownRaw, Active: traceRes.Active},
		PluginHost:       pluginHost,
		PluginArtifacts:  pluginArtifacts,
		PluginStagingDir: pluginStaging,
	})
	if err != nil {
		// NewProcessServices adopts (or releases) host/staging on entry.
		return fail(fmt.Errorf("runtimebundle: process services: %w", err), nil, nil)
	}
	closeProcess := ps.Close

	bundle, err := ops.compile(ctx, ps, cfg, in.HandlerComposer)
	var rollback func() error
	if bundle != nil {
		b := bundle
		rollback = func() error {
			return errors.Join(b.Quiesce(context.WithoutCancel(ctx)), omitSoleAlreadyClosed(b.Close()))
		}
	}
	if err != nil {
		return fail(fmt.Errorf("runtimebundle: compile validation generation: %w", err), rollback, closeProcess)
	}
	return fail(nil, rollback, closeProcess)
}
