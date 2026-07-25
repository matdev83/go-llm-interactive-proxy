package runtimebundle

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
	"go.opentelemetry.io/otel"
)

// GenerationCompiler is the canonical composition-root compiler (task 3.4).
type GenerationCompiler struct {
	Process *ProcessServices
	Compose HandlerComposer
}

func (c GenerationCompiler) Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (GenerationRuntime, error) {
	if c.Process == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if c.Compose == nil {
		return nil, fmt.Errorf("runtimebundle: nil HandlerComposer")
	}
	return CompileGeneration(ctx, GenerationCompileInput{
		Process: c.Process, Candidate: candidate, Compose: c.Compose, LiveFactoryKinds: liveFactoryKinds,
	})
}

type candidateCompilerAdapter struct{ inner GenerationCompiler }

func (a candidateCompilerAdapter) Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (runtimehost.PublishedRequestPlane, error) {
	return a.inner.Compile(ctx, candidate, liveFactoryKinds)
}

// ReloadHost is the process-owned composition binding manager, coordinator, and stable executor facade.
type ReloadHost struct {
	Coordinator         *runtimehost.Coordinator
	Manager             *runtimehost.Manager
	Process             *ProcessServices
	Executor            *runtimehost.GenerationExecutor
	Source              *configsource.FixedSource
	Effective           *config.EffectiveConfig
	Config              *config.Config
	Logger              *slog.Logger
	ActiveSource        *configsource.ActiveSourceVersion
	FixedStreamRecovery config.StreamRecoveryOverrides
	ShutdownTracing     func(context.Context) error

	dispatcher *runtimehost.GenerationDispatcher

	closeMu                      sync.Mutex
	closeAttempt                 *hostCloseAttempt
	closed                       bool
	processClosed, tracingClosed bool
}

type hostCloseAttempt struct {
	done chan struct{}
	err  error
}

type bindReloadHostInput struct {
	Manager             *runtimehost.Manager
	Process             *ProcessServices
	Compose             HandlerComposer
	Logger              *slog.Logger
	Config              *config.Config
	Effective           *config.EffectiveConfig
	ActiveSource        *configsource.ActiveSourceVersion
	FixedStreamRecovery config.StreamRecoveryOverrides
	ShutdownTracing     func(context.Context) error
}

func bindReloadHost(configPath string, in bindReloadHostInput) (*ReloadHost, error) {
	if in.Manager == nil {
		return nil, fmt.Errorf("runtimebundle: nil GenerationManager")
	}
	if in.Process == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if in.Compose == nil {
		return nil, fmt.Errorf("runtimebundle: nil HandlerComposer")
	}
	src, err := configsource.NewFixedSource(configPath, 0)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: fixed source: %w", err)
	}

	fixed := in.FixedStreamRecovery
	loader := runtimehost.FuncEffectiveLoader(func(ctx context.Context, raw []byte) (*config.EffectiveConfig, error) {
		merged := fixed
		return config.LoadEffective(ctx, raw, config.LoadEffectiveOptions{
			ConfigDir: filepath.Dir(src.AbsolutePath()), FixedStreamRecovery: &merged,
			InjectFeatures: injectStandardBootstrapFeatures, ExtraValidate: extraBootstrapValidate,
		})
	})

	obsDeps := runtimehost.ReloadObserverDeps{Logger: in.Logger, Tracer: otel.Tracer("lip.runtimehost.reload")}
	if in.Process.Metrics != nil {
		obsDeps.Metrics = in.Process.Metrics.Reload
	}
	observer := runtimehost.NewReloadObserver(obsDeps)

	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source: src, Loader: loader, Classify: configreload.ClassifyEffective,
		Compile: candidateCompilerAdapter{inner: GenerationCompiler{Process: in.Process, Compose: in.Compose}},
		Manager: in.Manager, Timeout: runtimehost.DefaultReloadTimeout,
		ActiveEffective: in.Effective, ActiveSource: in.ActiveSource, Observer: observer,
	})
	if err != nil {
		return nil, err
	}
	return &ReloadHost{
		Coordinator: coord, Manager: in.Manager, Process: in.Process,
		Executor: runtimehost.NewGenerationExecutor(in.Manager), dispatcher: runtimehost.NewGenerationDispatcher(in.Manager),
		Source: src, Effective: in.Effective, Config: in.Config, Logger: in.Logger,
		ActiveSource: in.ActiveSource, FixedStreamRecovery: in.FixedStreamRecovery, ShutdownTracing: in.ShutdownTracing,
	}, nil
}

func (h *ReloadHost) HTTPHandler() http.Handler {
	if h == nil || h.dispatcher == nil {
		return nil
	}
	return h.dispatcher
}

func (h *ReloadHost) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.closeMu.Lock()
	if h.closed {
		h.closeMu.Unlock()
		return nil
	}
	if attempt := h.closeAttempt; attempt != nil {
		done := attempt.done
		h.closeMu.Unlock()
		select {
		case <-done:
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	attempt := &hostCloseAttempt{done: make(chan struct{})}
	h.closeAttempt = attempt
	h.closeMu.Unlock()

	err := h.runCloseAttempt(ctx)

	h.closeMu.Lock()
	attempt.err = err
	if err == nil {
		h.closed = true
	}
	close(attempt.done)
	h.closeAttempt = nil
	h.closeMu.Unlock()
	return err
}

func hostCoordinator(h *ReloadHost) *runtimehost.Coordinator {
	if h == nil {
		return nil
	}
	return h.Coordinator
}

func (h *ReloadHost) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if c := hostCoordinator(h); c != nil {
		return c.Reload(ctx, trigger)
	}
	return sdkreload.Result{Category: sdkreload.ResultInternalFailed, ReasonCategory: "nil-coordinator"}
}

func (h *ReloadHost) Status() sdkreload.Status {
	if c := hostCoordinator(h); c != nil {
		return c.Status()
	}
	return sdkreload.Status{}
}

func (h *ReloadHost) FixedSourcePath() string {
	if c := hostCoordinator(h); c != nil {
		return c.FixedSourcePath()
	}
	return ""
}

func (h *ReloadHost) runCloseAttempt(ctx context.Context) error {
	h.BeginShutdown()
	if err := h.WaitForIdle(ctx); err != nil {
		return err
	}
	if h.Manager != nil {
		if err := h.Manager.ShutdownDetached(ctx); err != nil {
			return err
		}
		if h.Manager.HasOpenGenerations() {
			return fmt.Errorf("runtimebundle: generations remain open after shutdown")
		}
	}
	if err := h.closeProcessOnce(); err != nil {
		return err
	}
	return h.shutdownTracingOnce(ctx)
}

func (h *ReloadHost) runCloseOnce(done *bool, skip bool, closeFn func() error, after func()) error {
	h.closeMu.Lock()
	already := *done
	h.closeMu.Unlock()
	if already || skip {
		return nil
	}
	if err := closeFn(); err != nil {
		return err
	}
	h.closeMu.Lock()
	*done = true
	if after != nil {
		after()
	}
	h.closeMu.Unlock()
	return nil
}

func (h *ReloadHost) closeProcessOnce() error {
	return h.runCloseOnce(&h.processClosed, h.Process == nil, func() error { return h.Process.Close() }, nil)
}

func (h *ReloadHost) shutdownTracingOnce(ctx context.Context) error {
	h.closeMu.Lock()
	fn := h.ShutdownTracing
	h.closeMu.Unlock()
	return h.runCloseOnce(&h.tracingClosed, fn == nil, func() error { return fn(ctx) }, func() { h.ShutdownTracing = nil })
}

func (h *ReloadHost) BeginShutdown() {
	if c := hostCoordinator(h); c != nil {
		c.BeginShutdown()
	}
}

func (h *ReloadHost) WaitForIdle(ctx context.Context) error {
	if c := hostCoordinator(h); c != nil {
		return c.WaitForIdle(ctx)
	}
	return nil
}
