package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
)

// DefaultReloadTimeout is the host-owned reload attempt bound when unset.
const DefaultReloadTimeout = time.Minute

// StableConfigSource is the fixed-path source seam (typically configsource.FixedSource).
// Callers never supply a path or YAML through ReloadTrigger.
type StableConfigSource interface {
	AbsolutePath() string
	ReadStable(ctx context.Context, active *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error)
}

// EffectiveLoader runs the shared strict effective-load pipeline on accepted bytes.
type EffectiveLoader interface {
	LoadEffective(ctx context.Context, raw []byte) (*config.EffectiveConfig, error)
}

// CandidateCompiler builds one isolated immutable request-plane candidate.
// It must not mutate active generations or process-service ownership.
type CandidateCompiler interface {
	Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (PublishedRequestPlane, error)
}

// BackendFactoryKindCounter is optionally implemented by published request planes
// so the coordinator can populate LiveFactoryKinds from active/retained generations
// (tasks.md Implementation Notes; req 8.8).
type BackendFactoryKindCounter interface {
	BackendFactoryKindCounts() map[string]int
}

// CoordinatorDeps wires production or test seams for the serialized reload coordinator.
type CoordinatorDeps struct {
	Source          StableConfigSource
	Loader          EffectiveLoader
	Classify        func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
	Compile         CandidateCompiler
	Manager         *Manager
	Timeout         time.Duration
	ActiveEffective *config.EffectiveConfig
	ActiveSource    *configsource.ActiveSourceVersion
}

// Coordinator serializes explicit reload attempts (design Reload Coordinator; req 1.4, 3.x, 11.x, 13.x).
type Coordinator struct {
	source   StableConfigSource
	loader   EffectiveLoader
	classify func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
	compile  CandidateCompiler
	mgr      *Manager
	timeout  time.Duration

	mu             sync.Mutex
	busy           bool
	pendingSignal  bool
	coalesced      int64
	last           configreload.ReloadResult
	attempts       atomic.Int64
	shutdown       atomic.Bool
	activeEff      *config.EffectiveConfig
	activeSource   *configsource.ActiveSourceVersion
	activeSourceMu sync.RWMutex
}

// NewCoordinator constructs a production serialized reload coordinator.
func NewCoordinator(deps CoordinatorDeps) (*Coordinator, error) {
	if deps.Source == nil {
		return nil, fmt.Errorf("runtimehost: nil StableConfigSource")
	}
	if deps.Loader == nil {
		return nil, fmt.Errorf("runtimehost: nil EffectiveLoader")
	}
	if deps.Compile == nil {
		return nil, fmt.Errorf("runtimehost: nil CandidateCompiler")
	}
	if deps.Manager == nil {
		return nil, fmt.Errorf("runtimehost: nil Manager")
	}
	classify := deps.Classify
	if classify == nil {
		classify = configreload.ClassifyEffective
	}
	timeout := deps.Timeout
	if timeout <= 0 {
		timeout = DefaultReloadTimeout
	}
	c := &Coordinator{
		source:       deps.Source,
		loader:       deps.Loader,
		classify:     classify,
		compile:      deps.Compile,
		mgr:          deps.Manager,
		timeout:      timeout,
		activeEff:    deps.ActiveEffective,
		activeSource: cloneActiveSource(deps.ActiveSource),
	}
	if active := deps.Manager.Active(); active != nil {
		c.last = configreload.ReloadResult{
			Category:         configreload.ResultPublished,
			ActiveGeneration: active.ID(),
		}
	}
	return c, nil
}

// BeginShutdown rejects new attempts, cancels candidate work via manager shutdown
// signalling, and prevents late publication (req 1.9, 11.9, 13.7).
func (c *Coordinator) BeginShutdown() {
	if c == nil {
		return
	}
	c.shutdown.Store(true)
	if c.mgr != nil {
		c.mgr.BeginShutdown()
	}
	c.mu.Lock()
	c.pendingSignal = false
	c.mu.Unlock()
}

// Status returns a bounded safe snapshot (req 13.1-13.2, 14.1).
func (c *Coordinator) Status() configreload.ReloadStatus {
	if c == nil {
		return configreload.ReloadStatus{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var activeID int64
	if c.mgr != nil {
		if g := c.mgr.Active(); g != nil {
			activeID = g.ID()
		}
	}
	path := ""
	if c.source != nil {
		path = c.source.AbsolutePath()
	}
	return configreload.ReloadStatus{
		ActiveGeneration: activeID,
		LastResult:       c.last,
		Busy:             c.busy,
		FixedSourcePath:  path,
		PendingSignal:    c.pendingSignal,
		CoalescedSignals: c.coalesced,
	}
}

// Reload runs one serialized attempt. API callers receive Busy when an attempt is
// active; SIGHUP coalesces into at most one pending follow-up (req 11.4-11.6).
// The attempt uses a host-owned timeout independent of API client cancel (req 12.9).
func (c *Coordinator) Reload(ctx context.Context, trigger configreload.ReloadTrigger) configreload.ReloadResult {
	if c == nil {
		return configreload.ReloadResult{Category: configreload.ResultInternalFailed, ReasonCategory: "nil-coordinator"}
	}
	if c.shutdown.Load() || (c.mgr != nil && c.mgr.ShuttingDown()) {
		return c.terminal(configreload.ReloadResult{
			Category:       configreload.ResultCanceled,
			ReasonCategory: configreload.StageShutdown,
		}, false)
	}

	c.mu.Lock()
	if c.busy {
		if trigger.Kind == configreload.TriggerSIGHUP {
			if c.pendingSignal {
				c.coalesced++
			} else {
				c.pendingSignal = true
			}
			coal := c.coalesced
			activeID := c.activeGenerationIDLocked()
			c.mu.Unlock()
			return configreload.ReloadResult{
				Category:         configreload.ResultBusy,
				ActiveGeneration: activeID,
				ReasonCategory:   configreload.StageCoalesce,
				CoalescedSignals: coal,
			}
		}
		activeID := c.activeGenerationIDLocked()
		c.mu.Unlock()
		return configreload.ReloadResult{
			Category:         configreload.ResultBusy,
			ActiveGeneration: activeID,
			ReasonCategory:   configreload.StageBusy,
		}
	}
	c.busy = true
	c.mu.Unlock()

	hostCtx := context.WithoutCancel(ctx)
	if hostCtx == nil {
		hostCtx = context.Background()
	}
	var cancel context.CancelFunc
	hostCtx, cancel = context.WithTimeout(hostCtx, c.timeout)
	defer cancel()

	first := c.runAttempt(hostCtx, trigger)
	c.mu.Lock()
	c.last = first
	// Keep busy until pending is claimed or confirmed absent so API callers
	// cannot start a concurrent candidate build (req 11.4).
	for {
		if c.shutdown.Load() || (c.mgr != nil && c.mgr.ShuttingDown()) {
			c.pendingSignal = false
			c.busy = false
			c.mu.Unlock()
			return first
		}
		if !c.pendingSignal {
			c.busy = false
			c.mu.Unlock()
			return first
		}
		c.pendingSignal = false
		coal := c.coalesced
		c.coalesced = 0
		c.mu.Unlock()

		followCtx, followCancel := context.WithTimeout(context.WithoutCancel(context.Background()), c.timeout)
		follow := c.runAttempt(followCtx, configreload.ReloadTrigger{
			Kind:       configreload.TriggerSIGHUP,
			AcceptedAt: time.Now().UTC(),
			SafeActor:  "coalesced-sighup",
		})
		followCancel()
		follow.CoalescedSignals = coal

		c.mu.Lock()
		c.last = follow
		// loop: another SIGHUP may have reserved pending during follow-up
	}
}

func (c *Coordinator) terminal(res configreload.ReloadResult, record bool) configreload.ReloadResult {
	if res.ActiveGeneration == 0 {
		res.ActiveGeneration = c.activeGenerationID()
	}
	if record {
		c.mu.Lock()
		c.last = res
		c.mu.Unlock()
	}
	return res
}

func (c *Coordinator) activeGenerationID() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeGenerationIDLocked()
}

func (c *Coordinator) activeGenerationIDLocked() int64 {
	if c.mgr == nil {
		return 0
	}
	if g := c.mgr.Active(); g != nil {
		return g.ID()
	}
	return 0
}

func (c *Coordinator) runAttempt(ctx context.Context, trigger configreload.ReloadTrigger) (res configreload.ReloadResult) {
	attempt := c.attempts.Add(1)
	activeBefore := c.activeGenerationID()
	res = configreload.ReloadResult{
		AttemptID:        attempt,
		ActiveGeneration: activeBefore,
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			res = configreload.ReloadResult{
				Category:         configreload.ResultInternalFailed,
				AttemptID:        attempt,
				ActiveGeneration: c.activeGenerationID(),
				ReasonCategory:   configreload.StagePanic,
			}
		}
		if res.AttemptID == 0 {
			res.AttemptID = attempt
		}
		if res.ActiveGeneration == 0 {
			res.ActiveGeneration = c.activeGenerationID()
		}
	}()

	if c.shutdown.Load() || (c.mgr != nil && c.mgr.ShuttingDown()) {
		res.Category = configreload.ResultCanceled
		res.ReasonCategory = configreload.StageShutdown
		return res
	}
	if err := ctx.Err(); err != nil {
		res.Category = configreload.ResultCanceled
		res.ReasonCategory = configreload.StageShutdown
		return res
	}

	c.activeSourceMu.RLock()
	activeSrc := cloneActiveSource(c.activeSource)
	c.activeSourceMu.RUnlock()

	snap, atomicRes, err := c.source.ReadStable(ctx, activeSrc)
	if err != nil {
		cat, reason := configreload.MapLoadFailure(err)
		if srcCat, ok := configsource.CategoryOf(err); ok {
			cat, reason = configreload.MapLoadCategory(string(srcCat))
		}
		res.Category = cat
		res.ReasonCategory = reason
		return res
	}
	if atomicRes == configsource.AtomicNoop {
		res.Category = configreload.ResultNoop
		res.ReasonCategory = configreload.StageNoop
		return res
	}

	eff, err := c.loader.LoadEffective(ctx, snap.Bytes)
	if err != nil {
		cat, reason := configreload.MapLoadFailure(err)
		if srcCat, ok := configsource.CategoryOf(err); ok {
			cat, reason = configreload.MapLoadCategory(string(srcCat))
		}
		res.Category = cat
		res.ReasonCategory = reason
		return res
	}

	c.activeSourceMu.RLock()
	activeEff := c.activeEff
	c.activeSourceMu.RUnlock()
	if activeEff != nil && activeEff.Identity.PrivateDigest == eff.Identity.PrivateDigest {
		res.Category = configreload.ResultNoop
		res.ReasonCategory = configreload.StageNoop
		return res
	}

	if activeEff != nil {
		_, err := c.classify(activeEff, eff)
		if err != nil {
			var rr *configreload.RestartRequiredError
			if errors.As(err, &rr) {
				res.Category = configreload.ResultRestartRequired
				res.ReasonCategory = configreload.StageClassify
				if rr != nil {
					res.RestartFields = append([]string(nil), rr.RestartRequiredFields...)
					res.RestartFieldCount = rr.TotalBlocked
				}
				return res
			}
			res.Category = configreload.ResultInvalid
			res.ReasonCategory = configreload.StageClassify
			return res
		}
	}

	if c.shutdown.Load() || (c.mgr != nil && c.mgr.ShuttingDown()) {
		res.Category = configreload.ResultCanceled
		res.ReasonCategory = configreload.StageShutdown
		return res
	}

	liveKinds := c.collectLiveFactoryKinds()
	plane, compileErr := c.compileIsolated(ctx, eff.Config, liveKinds)
	if compileErr != nil {
		var rr *configreload.RestartRequiredError
		if errors.As(compileErr, &rr) {
			res.Category = configreload.ResultRestartRequired
			res.ReasonCategory = configreload.StageCompile
			if rr != nil {
				res.RestartFields = append([]string(nil), rr.RestartRequiredFields...)
				res.RestartFieldCount = rr.TotalBlocked
			}
			return res
		}
		if c.shutdown.Load() || errors.Is(compileErr, context.Canceled) || errors.Is(compileErr, context.DeadlineExceeded) {
			res.Category = configreload.ResultCanceled
			res.ReasonCategory = configreload.StageShutdown
			return res
		}
		if errors.Is(compileErr, errCompilePanic) {
			res.Category = configreload.ResultInternalFailed
			res.ReasonCategory = configreload.StagePanic
			return res
		}
		res.Category = configreload.ResultPreparationFailed
		res.ReasonCategory = configreload.StageCompile
		return res
	}
	if plane == nil {
		res.Category = configreload.ResultPreparationFailed
		res.ReasonCategory = configreload.StageCompile
		return res
	}

	rollback := func() {
		if plane == nil {
			return
		}
		_ = plane.Close()
		plane = nil
	}

	if c.shutdown.Load() || (c.mgr != nil && c.mgr.ShuttingDown()) {
		rollback()
		res.Category = configreload.ResultCanceled
		res.ReasonCategory = configreload.StageShutdown
		return res
	}
	if err := ctx.Err(); err != nil {
		rollback()
		res.Category = configreload.ResultCanceled
		res.ReasonCategory = configreload.StageShutdown
		return res
	}

	label := string(trigger.Kind)
	if label == "" {
		label = "reload"
	}
	gen := c.mgr.PrepareRequestPlane(label, plane)
	gen.SetMetaHints(MetaHints{
		PublicFingerprint: eff.Identity.PublicFingerprint,
		TriggerKind:       string(trigger.Kind),
		LoadedAt:          eff.LoadedAt,
	})

	if c.shutdown.Load() || c.mgr.ShuttingDown() {
		_ = gen.Discard()
		res.Category = configreload.ResultCanceled
		res.ReasonCategory = configreload.StageShutdown
		return res
	}

	if err := c.mgr.Publish(gen); err != nil {
		// Publish already Discards on retention/shutdown rejection.
		switch {
		case errors.Is(err, ErrRetentionBlocked):
			res.Category = configreload.ResultRetentionBlocked
			res.ReasonCategory = configreload.StageRetention
		case errors.Is(err, ErrHostShuttingDown):
			res.Category = configreload.ResultCanceled
			res.ReasonCategory = configreload.StageShutdown
		default:
			res.Category = configreload.ResultPreparationFailed
			res.ReasonCategory = configreload.StagePublish
		}
		res.ActiveGeneration = c.activeGenerationID()
		return res
	}

	c.activeSourceMu.Lock()
	c.activeEff = eff
	c.activeSource = &configsource.ActiveSourceVersion{
		HandleIdentity: snap.HandleIdentity,
		PrivateDigest:  snap.PrivateDigest,
	}
	c.activeSourceMu.Unlock()

	res.Category = configreload.ResultPublished
	res.PreviousGeneration = activeBefore
	res.ActiveGeneration = gen.ID()
	res.ReasonCategory = configreload.StagePublish
	return res
}

func (c *Coordinator) collectLiveFactoryKinds() map[string]int {
	if c == nil || c.mgr == nil {
		return nil
	}
	out := make(map[string]int)
	add := func(g *Generation) {
		if g == nil {
			return
		}
		plane := g.RequestPlane()
		counter, ok := plane.(BackendFactoryKindCounter)
		if !ok || counter == nil {
			return
		}
		for k, n := range counter.BackendFactoryKindCounts() {
			if k == "" || n <= 0 {
				continue
			}
			out[k] += n
		}
	}
	add(c.mgr.Active())
	for _, g := range c.mgr.SnapshotRetained() {
		add(g)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var errCompilePanic = errors.New("runtimehost: candidate compile panic")

func (c *Coordinator) compileIsolated(ctx context.Context, cfg *config.Config, liveKinds map[string]int) (plane PublishedRequestPlane, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			plane = nil
			err = fmt.Errorf("%w: %v", errCompilePanic, recovered)
		}
	}()
	return c.compile.Compile(ctx, cfg, liveKinds)
}

func cloneActiveSource(in *configsource.ActiveSourceVersion) *configsource.ActiveSourceVersion {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

// FuncEffectiveLoader adapts a function to EffectiveLoader.
type FuncEffectiveLoader func(ctx context.Context, raw []byte) (*config.EffectiveConfig, error)

// LoadEffective implements EffectiveLoader.
func (f FuncEffectiveLoader) LoadEffective(ctx context.Context, raw []byte) (*config.EffectiveConfig, error) {
	return f(ctx, raw)
}

// FuncCompiler adapts a function to CandidateCompiler.
type FuncCompiler func(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (PublishedRequestPlane, error)

// Compile implements CandidateCompiler.
func (f FuncCompiler) Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (PublishedRequestPlane, error) {
	return f(ctx, candidate, liveFactoryKinds)
}
