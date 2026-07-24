package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// DefaultMaxRetainedGenerations is the process retention budget used by the
// initial-generation host until reload configuration exposes a tunable.
const DefaultMaxRetainedGenerations = 8

type publishInitialGenerationInput struct {
	Cfg           *config.Config
	Effective     *config.EffectiveConfig
	Logger        *slog.Logger
	Registry      *pluginreg.Registry
	SecretEnv     coresg.Environment
	Production    ProductionOptions
	Compose       HandlerComposer
	TraceActive   bool
	TraceShutdown func(context.Context) error
	// Probe is optional; production BuildBootstrap passes nil. BuildHost may
	// pass a call-scoped probe so process/compile/publish stages share one
	// ownership engine with the PartialCleanup matrix.
	Probe hostBuildProbe
}

// joinInitialFailureCleanup tears down initial-generation bootstrap ownership in
// order: candidate/generation → ProcessServices → tracing. Primary is preserved
// for errors.Is; cleanup errors are joined without secret-bearing wrapping.
func joinInitialFailureCleanup(
	ctx context.Context,
	primary error,
	genRollback func() error,
	processClose func() error,
	traceShutdown func(context.Context) error,
) error {
	var cleanup error
	if genRollback != nil {
		if err := genRollback(); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
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

func publishInitialGeneration(ctx context.Context, out BootstrapResult, in publishInitialGenerationInput) (BootstrapResult, error) {
	note := func(stage hostBuildStageName, event hostBuildProbeEvent) error {
		if in.Probe == nil {
			return nil
		}
		return in.Probe(stage, event)
	}
	traceShutdown := in.TraceShutdown
	if in.Probe != nil && traceShutdown != nil {
		inner := traceShutdown
		traceShutdown = func(ctx context.Context) error {
			_ = note(hostBuildStageNameTracing, hostBuildProbeCleaned)
			return inner(ctx)
		}
	}

	fail := func(err error, genRollback, processClose func() error) (BootstrapResult, error) {
		// Failure paths own tracing teardown; clear the projection so callers
		// (and the success-path outer defer) do not double-close.
		out.ShutdownTracing = nil
		return out, joinInitialFailureCleanup(ctx, err, genRollback, processClose, traceShutdown)
	}

	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg: in.Cfg,
		Log: in.Logger,
		Opts: &BuildOptions{
			PluginRegistry: in.Registry,
			Infra: InfraOptions{
				OutboundTracing: in.TraceActive,
				ProcessTracing: ProcessTracing{
					Shutdown: in.TraceShutdown,
					Active:   in.TraceActive,
				},
			},
			Extensions: ExtensionsOptions{
				SecretGuardEnvironment: in.SecretEnv,
			},
			Production: in.Production,
		},
		Tracing: ProcessTracing{
			Shutdown: in.TraceShutdown,
			Active:   in.TraceActive,
		},
	})
	if err != nil {
		return fail(fmt.Errorf("runtimebundle: process services: %w", err), nil, nil)
	}
	if !ps.Tracing.Active && in.TraceActive {
		ps.Tracing.Active = true
	}
	if err := note(hostBuildStageNameProcess, hostBuildProbeAcquired); err != nil {
		_ = note(hostBuildStageNameProcess, hostBuildProbeCleaned)
		return fail(err, nil, ps.Close)
	}
	closeProcess := func() error {
		_ = note(hostBuildStageNameProcess, hostBuildProbeCleaned)
		return ps.Close()
	}

	// Candidate feature lifecycles are derived once inside CompileGeneration from
	// the candidate config surface. Do not overlay bootstrap-merged instances —
	// that double-registers Start/Stop on the generation ledger.
	bundle, err := CompileGeneration(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: in.Cfg,
		Compose:   in.Compose,
	})
	if err != nil {
		return fail(fmt.Errorf("runtimebundle: compile initial generation: %w", err), nil, closeProcess)
	}
	if err := note(hostBuildStageNameCompile, hostBuildProbeAcquired); err != nil {
		_ = note(hostBuildStageNameCompile, hostBuildProbeCleaned)
		_ = bundle.Quiesce(context.WithoutCancel(ctx))
		_ = bundle.Close()
		return fail(err, nil, closeProcess)
	}

	mgr := runtimehost.NewManager(DefaultMaxRetainedGenerations, nil)
	gen := mgr.PrepareRequestPlane("startup", bundle)
	hints := runtimehost.MetaHints{
		TriggerKind: "startup",
		LoadedAt:    time.Now().UTC(),
	}
	if in.Effective != nil {
		hints.PublicFingerprint = in.Effective.Identity.PublicFingerprint
		if !in.Effective.LoadedAt.IsZero() {
			hints.LoadedAt = in.Effective.LoadedAt
		}
	}
	gen.SetMetaHints(hints)
	if err := mgr.Publish(gen); err != nil {
		return fail(fmt.Errorf("runtimebundle: publish initial generation: %w", err), gen.Discard, closeProcess)
	}
	if gen.ID() != 1 {
		return fail(fmt.Errorf("runtimebundle: initial generation id=%d want 1", gen.ID()), func() error {
			return mgr.ShutdownDetached(context.WithoutCancel(ctx), runtimehost.NewLifecycleWorker())
		}, closeProcess)
	}
	if err := note(hostBuildStageNamePublish, hostBuildProbeAcquired); err != nil {
		_ = note(hostBuildStageNamePublish, hostBuildProbeCleaned)
		_ = note(hostBuildStageNameCompile, hostBuildProbeCleaned)
		return fail(err, func() error {
			return mgr.ShutdownDetached(context.WithoutCancel(ctx), runtimehost.NewLifecycleWorker())
		}, closeProcess)
	}

	out.ProcessServices = ps
	out.GenerationManager = mgr
	out.InitialGeneration = gen
	if ps.terminalWorkRT != nil {
		ps.terminalWorkRT.BindGenerationManager(mgr)
	}
	return out, nil
}
