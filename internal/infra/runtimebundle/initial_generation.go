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
	// Probe is optional; production BuildHost passes nil except when a
	// call-scoped test probe is threaded through for the PartialCleanup matrix.
	Probe hostBuildProbe
}

// joinInitialFailureCleanup tears down initial-generation ownership in order:
// candidate/generation → ProcessServices → tracing. Primary is preserved for
// errors.Is; cleanup errors are joined without secret-bearing wrapping. Sole
// ErrAlreadyClosed from gen rollback is omitted; mixed joins are kept.
func joinInitialFailureCleanup(
	ctx context.Context,
	primary error,
	genRollback func() error,
	processClose func() error,
	traceShutdown func(context.Context) error,
) error {
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

// omitSoleAlreadyClosed drops sole ErrAlreadyClosed; mixed joins stay intact.
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

// publishInitialGeneration constructs ProcessServices, compiles, and publishes
// generation 1 as one owned transaction, returning explicit focused values
// (never a broad result aggregate). On error it rolls back everything it
// acquired and returns nils.
func publishInitialGeneration(ctx context.Context, in publishInitialGenerationInput) (*ProcessServices, *runtimehost.Manager, *runtimehost.Generation, error) {
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

	fail := func(err error, genRollback, processClose func() error) (*ProcessServices, *runtimehost.Manager, *runtimehost.Generation, error) {
		return nil, nil, nil, joinInitialFailureCleanup(ctx, err, genRollback, processClose, traceShutdown)
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
	// the candidate config surface. Do not overlay merged instances — that
	// double-registers Start/Stop on the generation ledger.
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
			return mgr.ShutdownDetached(context.WithoutCancel(ctx))
		}, closeProcess)
	}
	if err := note(hostBuildStageNamePublish, hostBuildProbeAcquired); err != nil {
		_ = note(hostBuildStageNamePublish, hostBuildProbeCleaned)
		_ = note(hostBuildStageNameCompile, hostBuildProbeCleaned)
		return fail(err, func() error {
			return mgr.ShutdownDetached(context.WithoutCancel(ctx))
		}, closeProcess)
	}

	if ps.terminalWorkRT != nil {
		ps.terminalWorkRT.BindGenerationManager(mgr)
	}
	return ps, mgr, gen, nil
}
