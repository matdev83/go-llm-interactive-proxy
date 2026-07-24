package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/osenv"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// ValidateDistributionInput configures [ValidateDistribution]. No LogWriter:
// validation returns only a secret-safe error or nil.
type ValidateDistributionInput struct {
	ConfigPath              string
	Mandatory               []lipsdk.Requirement
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	Production              ProductionOptions
	HandlerComposer         HandlerComposer
}

// ValidateDistribution is one unpublished dry-run (req 5.1-5.6): one strict
// effective load, ProcessServices, and [CompileGeneration] via the serve/reload
// composer path, then generation rollback and process/tracing close — never a
// Manager, generation ID, active pointer, listener, or retirement worker.
func ValidateDistribution(ctx context.Context, in ValidateDistributionInput) error {
	return validateDistribution(ctx, in, osenv.Process{}, LoadBootstrapEffectiveWithSource, nil)
}

type validateDistributionStage string

const (
	validateStageLoader       validateDistributionStage = "loader"
	validateStageTracing      validateDistributionStage = "tracing"
	validateStageRegistry     validateDistributionStage = "registry"
	validateStageProcess      validateDistributionStage = "process"
	validateStageCompile      validateDistributionStage = "compile"
	validateStageRollback     validateDistributionStage = "rollback"
	validateStageProcessClose validateDistributionStage = "process_close"
	validateStageTracingClose validateDistributionStage = "tracing_close"
)

type validateDistributionProbeEvent string

const (
	validateProbeAcquired validateDistributionProbeEvent = "acquired"
	validateProbeCleaned  validateDistributionProbeEvent = "cleaned"
)

// validateDistributionProbe is a test-only observer/fault injector (mirrors
// [hostBuildProbe]). "acquired" faults after real acquire; "cleaned" joins with
// real close/rollback (never replaces) so the action always runs once.
type validateDistributionProbe func(stage validateDistributionStage, event validateDistributionProbeEvent) error

func validateDistribution(
	ctx context.Context,
	in ValidateDistributionInput,
	secretEnv coresg.Environment,
	loadEffective bootstrapEffectiveLoader,
	probe validateDistributionProbe,
) error {
	note := func(stage validateDistributionStage, event validateDistributionProbeEvent) error {
		if probe == nil {
			return nil
		}
		return probe(stage, event)
	}
	if ctx == nil {
		return fmt.Errorf("runtimebundle: nil context")
	}
	if loadEffective == nil {
		return fmt.Errorf("runtimebundle: nil effective loader")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return fmt.Errorf("runtimebundle: empty config path")
	}
	if in.HandlerComposer == nil {
		return fmt.Errorf("runtimebundle: ValidateDistribution requires HandlerComposer")
	}

	effective, _, _, err := loadEffective(ctx, path, in.StreamRecoveryOverrides)
	if err != nil {
		return err
	}
	if effective == nil || effective.Config == nil {
		return fmt.Errorf("runtimebundle: nil effective config")
	}
	if err := note(validateStageLoader, validateProbeAcquired); err != nil {
		return err
	}
	cfg := effective.Config

	traceRes, err := initProcessTracing(ctx, cfg)
	if err != nil {
		return fmt.Errorf("runtimebundle: tracing init: %w", err)
	}
	traceShutdownRaw := traceRes.Shutdown
	shutTracing := func(ctx context.Context) error {
		return validateRunCleanupStage(note, validateStageTracingClose, func() error {
			if traceShutdownRaw == nil {
				return nil
			}
			return traceShutdownRaw(ctx)
		})
	}
	if err := note(validateStageTracing, validateProbeAcquired); err != nil {
		return joinInitialFailureCleanup(ctx, err, nil, nil, shutTracing)
	}

	reg, _, err := installRegistryAndRegistrations(cfg, in.Mandatory)
	if err != nil {
		return joinInitialFailureCleanup(ctx, err, nil, nil, shutTracing)
	}
	if err := note(validateStageRegistry, validateProbeAcquired); err != nil {
		return joinInitialFailureCleanup(ctx, err, nil, nil, shutTracing)
	}

	logger, err := logging.NewLogger(cfg.Logging, io.Discard,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		return joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: logger init: %w", err), nil, nil, shutTracing)
	}

	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg: cfg,
		Log: logger,
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Infra: InfraOptions{
				OutboundTracing: traceRes.Active,
				ProcessTracing:  ProcessTracing{Shutdown: traceShutdownRaw, Active: traceRes.Active},
			},
			Extensions: ExtensionsOptions{SecretGuardEnvironment: secretEnv},
			Production: in.Production,
		},
		Tracing: ProcessTracing{Shutdown: traceShutdownRaw, Active: traceRes.Active},
	})
	if err != nil {
		return joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: process services: %w", err), nil, nil, shutTracing)
	}
	closeProcess := func() error {
		return validateRunCleanupStage(note, validateStageProcessClose, ps.Close)
	}
	if err := note(validateStageProcess, validateProbeAcquired); err != nil {
		return joinInitialFailureCleanup(ctx, err, nil, closeProcess, shutTracing)
	}

	bundle, err := CompileGeneration(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   in.HandlerComposer,
	})
	if err != nil {
		return joinInitialFailureCleanup(ctx, fmt.Errorf("runtimebundle: compile validation generation: %w", err), nil, closeProcess, shutTracing)
	}
	rollback := func() error {
		return validateRunCleanupStage(note, validateStageRollback, func() error {
			if bundle == nil {
				return nil
			}
			return errors.Join(bundle.Quiesce(context.WithoutCancel(ctx)), omitSoleAlreadyClosed(bundle.Close()))
		})
	}
	if err := note(validateStageCompile, validateProbeAcquired); err != nil {
		return joinInitialFailureCleanup(ctx, err, rollback, closeProcess, shutTracing)
	}

	return joinInitialFailureCleanup(ctx, nil, rollback, closeProcess, shutTracing)
}

// validateRunCleanupStage runs real cleanup first, then joins any probe fault.
func validateRunCleanupStage(note validateDistributionProbe, stage validateDistributionStage, action func() error) error {
	actionErr := action()
	probeErr := note(stage, validateProbeCleaned)
	if probeErr != nil {
		return errors.Join(actionErr, probeErr)
	}
	return actionErr
}
