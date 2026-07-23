package runtimebundle

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
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
// stable executor facade, and fixed-source loader (tasks 5.5–5.6).
type ReloadHost struct {
	Coordinator *runtimehost.Coordinator
	Manager     *runtimehost.Manager
	Process     *ProcessServices
	Executor    *runtimehost.GenerationExecutor
	Source      *configsource.FixedSource
	Effective   *config.EffectiveConfig
}

// AttachReloadHost binds a production Coordinator and stable GenerationExecutor
// onto an already-published initial generation (BootstrapServe + HandlerComposer).
// Stream-recovery overrides come from res.FixedStreamRecovery (captured once at
// BuildBootstrap); this function must not reread process environment.
func AttachReloadHost(
	_ context.Context,
	res BootstrapResult,
	configPath string,
	compose HandlerComposer,
) (*ReloadHost, error) {
	if res.GenerationManager == nil {
		return nil, fmt.Errorf("runtimebundle: nil GenerationManager")
	}
	if res.ProcessServices == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if compose == nil {
		return nil, fmt.Errorf("runtimebundle: nil HandlerComposer")
	}
	src, err := configsource.NewFixedSource(configPath, 0)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: fixed source: %w", err)
	}

	fixed := res.FixedStreamRecovery
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
		Logger: res.Logger,
		Tracer: otel.Tracer("lip.runtimehost.reload"),
	}
	if res.ProcessServices.Metrics != nil {
		obsDeps.Metrics = res.ProcessServices.Metrics.Reload
	}
	observer := runtimehost.NewReloadObserver(obsDeps)

	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Classify:        configreload.ClassifyEffective,
		Compile:         candidateCompilerAdapter{inner: GenerationCompiler{Process: res.ProcessServices, Compose: compose}},
		Manager:         res.GenerationManager,
		Timeout:         runtimehost.DefaultReloadTimeout,
		ActiveEffective: res.Effective,
		ActiveSource:    res.ActiveSource,
		Observer:        observer,
	})
	if err != nil {
		return nil, err
	}
	return &ReloadHost{
		Coordinator: coord,
		Manager:     res.GenerationManager,
		Process:     res.ProcessServices,
		Executor:    runtimehost.NewGenerationExecutor(res.GenerationManager),
		Source:      src,
		Effective:   res.Effective,
	}, nil
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
