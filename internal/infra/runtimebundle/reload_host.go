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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
	"go.opentelemetry.io/otel"
)

// GenerationCompiler is the canonical composition-root compiler: it returns
// only GenerationRuntime (task 3.4, req 2.2-2.8). It does not implement
// runtimehost.CandidateCompiler directly — Go lacks covariant return types, so
// candidateCompilerAdapter below is the one narrow, explicit adapter that
// invokes GenerationCompiler and republishes its result as
// runtimehost.PublishedRequestPlane (GenerationRuntime already embeds it).
type GenerationCompiler struct {
	Process *ProcessServices
	Compose HandlerComposer
}

// Compile builds one isolated generation runtime against the process.
func (c GenerationCompiler) Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (GenerationRuntime, error) {
	if c.Process == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if c.Compose == nil {
		return nil, fmt.Errorf("runtimebundle: nil HandlerComposer")
	}
	return CompileGeneration(ctx, GenerationCompileInput{
		Process:          c.Process,
		Candidate:        candidate,
		Compose:          c.Compose,
		LiveFactoryKinds: liveFactoryKinds,
	})
}

// candidateCompilerAdapter is the sole explicit boundary adapter satisfying
// runtimehost.CandidateCompiler by delegating to GenerationCompiler. It
// performs no compilation logic of its own — only the return-type widening
// Go's lack of covariant returns requires (task 3.4, req 2.2-2.8).
type candidateCompilerAdapter struct {
	inner GenerationCompiler
}

func (a candidateCompilerAdapter) Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (runtimehost.PublishedRequestPlane, error) {
	rt, err := a.inner.Compile(ctx, candidate, liveFactoryKinds)
	if err != nil {
		return nil, err
	}
	return rt, nil
}

// ReloadHost is the process-owned composition binding manager, coordinator,
// stable executor facade, and fixed-source loader (tasks 5.2 / 5.5–5.6).
// BuildHost returns a complete Host (= ReloadHost) that also carries focused
// startup state needed by serve and the public facade.
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

	closeMu sync.Mutex
	// closeAttempt is the single in-flight shutdown attempt, if any. Waiting
	// callers observe its completion instead of blocking on closeMu.
	closeAttempt *hostCloseAttempt
	closed       bool
	// Per-phase completion so a retry never repeats a phase that already
	// succeeded and tracing can never run twice.
	processClosed bool
	tracingClosed bool
}

// hostCloseAttempt is one serialized shutdown attempt whose result is shared
// with every caller that waited for it.
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

// bindReloadHost constructs the coordinator + stable executor on an already
// published generation 1 using the accepted snapshot (no second startup load).
// Reload-time LoadEffective lives here so config_load scanners exclude it with
// the reload_host path; BuildHost is the sole production caller.
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
			ConfigDir:           filepath.Dir(src.AbsolutePath()),
			FixedStreamRecovery: &merged,
			InjectFeatures:      injectStandardBootstrapFeatures,
			ExtraValidate:       extraBootstrapValidate,
		})
	})

	obsDeps := runtimehost.ReloadObserverDeps{
		Logger: in.Logger,
		Tracer: otel.Tracer("lip.runtimehost.reload"),
	}
	if in.Process.Metrics != nil {
		obsDeps.Metrics = in.Process.Metrics.Reload
	}
	observer := runtimehost.NewReloadObserver(obsDeps)

	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Classify:        configreload.ClassifyEffective,
		Compile:         candidateCompilerAdapter{inner: GenerationCompiler{Process: in.Process, Compose: in.Compose}},
		Manager:         in.Manager,
		Timeout:         runtimehost.DefaultReloadTimeout,
		ActiveEffective: in.Effective,
		ActiveSource:    in.ActiveSource,
		Observer:        observer,
	})
	if err != nil {
		return nil, err
	}
	return &ReloadHost{
		Coordinator:         coord,
		Manager:             in.Manager,
		Process:             in.Process,
		Executor:            runtimehost.NewGenerationExecutor(in.Manager),
		dispatcher:          runtimehost.NewGenerationDispatcher(in.Manager),
		Source:              src,
		Effective:           in.Effective,
		Config:              in.Config,
		Logger:              in.Logger,
		ActiveSource:        in.ActiveSource,
		FixedStreamRecovery: in.FixedStreamRecovery,
		ShutdownTracing:     in.ShutdownTracing,
	}, nil
}

// HTTPHandler returns the process-stable generation dispatcher serving
// adapters mount. It is bound once and survives every reload, so adapters
// never need the Manager to build a data plane.
func (h *ReloadHost) HTTPHandler() http.Handler {
	if h == nil || h.dispatcher == nil {
		return nil
	}
	return h.dispatcher
}

// Close is the sole process shutdown coordinator (design Process Shutdown; req
// 8.6-8.8): reject reload triggers, wait for candidate work, retire and drain
// generations, close process services, then tracing last.
//
// At most one attempt mutates phases at a time. Concurrent waiters share that
// attempt's published result (they never silently start attempt N+1). A caller
// that arrives only after a failed attempt has fully published may retry the
// incomplete phases. A successful Close is idempotent.
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
			// attempt.err is assigned and done is closed under closeMu before
			// the attempt is cleared for retry; return that exact shared result
			// rather than starting a fresh attempt.
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	attempt := &hostCloseAttempt{done: make(chan struct{})}
	h.closeAttempt = attempt
	h.closeMu.Unlock()

	err := h.runCloseAttempt(ctx)

	// Publish atomically under closeMu so a later caller cannot enter attempt
	// N+1 before waiters on attempt N are notified:
	// 1) assign err  2) terminal closed on success  3) close(done)
	// 4) clear closeAttempt (retry-available)  5) unlock.
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

// Reload delegates to the host-owned coordinator. Nil-safe to match the
// canonical Coordinator contract so CLI/management never extract Coordinator.
func (h *ReloadHost) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if h == nil || h.Coordinator == nil {
		return sdkreload.Result{Category: sdkreload.ResultInternalFailed, ReasonCategory: "nil-coordinator"}
	}
	return h.Coordinator.Reload(ctx, trigger)
}

// Status delegates to the host-owned coordinator.
func (h *ReloadHost) Status() sdkreload.Status {
	if h == nil || h.Coordinator == nil {
		return sdkreload.Status{}
	}
	return h.Coordinator.Status()
}

// FixedSourcePath delegates to the host-owned coordinator.
func (h *ReloadHost) FixedSourcePath() string {
	if h == nil || h.Coordinator == nil {
		return ""
	}
	return h.Coordinator.FixedSourcePath()
}

// runCloseAttempt executes the ownership order once, without holding closeMu
// across any phase so status callbacks cannot deadlock the host.
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

// closeProcessOnce closes process services at most once across retries.
func (h *ReloadHost) closeProcessOnce() error {
	h.closeMu.Lock()
	done := h.processClosed
	h.closeMu.Unlock()
	if done || h.Process == nil {
		return nil
	}
	if err := h.Process.Close(); err != nil {
		return err
	}
	h.closeMu.Lock()
	h.processClosed = true
	h.closeMu.Unlock()
	return nil
}

// shutdownTracingOnce runs tracing last and exactly once; a failure leaves the
// phase incomplete so the provider can be retried.
func (h *ReloadHost) shutdownTracingOnce(ctx context.Context) error {
	h.closeMu.Lock()
	done := h.tracingClosed
	fn := h.ShutdownTracing
	h.closeMu.Unlock()
	if done || fn == nil {
		return nil
	}
	if err := fn(ctx); err != nil {
		return err
	}
	h.closeMu.Lock()
	h.tracingClosed = true
	h.ShutdownTracing = nil // no caller can re-enter a finished provider
	h.closeMu.Unlock()
	return nil
}

// BeginShutdown rejects triggers and prohibits late publication.
func (h *ReloadHost) BeginShutdown() {
	if h == nil || h.Coordinator == nil {
		return
	}
	h.Coordinator.BeginShutdown()
}

// WaitForIdle awaits in-flight reload/candidate rollback after BeginShutdown
// (req 13.7). Safe to call when Coordinator is nil.
func (h *ReloadHost) WaitForIdle(ctx context.Context) error {
	if h == nil || h.Coordinator == nil {
		return nil
	}
	return h.Coordinator.WaitForIdle(ctx)
}

// ActiveHasProductionMetering reports whether the active generation executor
// has a production metering recorder (facade visibility; req 12.4).
func (h *ReloadHost) ActiveHasProductionMetering() bool {
	ex := h.activeExecutor()
	return ex != nil && ex.MeteringRecorder != nil
}

// ActiveHasProductionRater reports whether the active generation executor has
// an operator economics rater attached.
func (h *ReloadHost) ActiveHasProductionRater() bool {
	ex := h.activeExecutor()
	return ex != nil && ex.EconomicsRater != nil
}

func (h *ReloadHost) activeExecutor() *runtime.Executor {
	if h == nil || h.Manager == nil {
		return nil
	}
	g := h.Manager.Active()
	if g == nil {
		return nil
	}
	provider, ok := g.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		return nil
	}
	view := provider.ExecutorView()
	ex, ok := view.(*runtime.Executor)
	if !ok {
		return nil
	}
	return ex
}

// DryRunCompile runs the shared generation compiler without publishing
// (check-config parity; design ValidationDryRun).
func DryRunCompile(ctx context.Context, process *ProcessServices, candidate *config.Config, compose HandlerComposer) error {
	if process == nil || candidate == nil || compose == nil {
		return fmt.Errorf("runtimebundle: dry-run compile: nil input")
	}
	bundle, err := CompileGeneration(ctx, GenerationCompileInput{
		Process:   process,
		Candidate: candidate,
		Compose:   compose,
	})
	if err != nil {
		return err
	}
	if bundle == nil {
		return fmt.Errorf("runtimebundle: dry-run compile: nil bundle")
	}
	_ = bundle.Quiesce(context.WithoutCancel(ctx))
	return bundle.Close()
}
