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
	Observer        *ReloadObserver
}

// Coordinator serializes explicit reload attempts (design Reload Coordinator; req 1.4, 3.x, 11.x, 13.x).
type Coordinator struct {
	source   StableConfigSource
	loader   EffectiveLoader
	classify func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
	compile  CandidateCompiler
	mgr      *Manager
	timeout  time.Duration
	observer *ReloadObserver

	mu             sync.Mutex
	busy           bool
	pendingSignal  bool
	coalesced      int64
	last           configreload.ReloadResult
	lastSuccess    configreload.ReloadResult
	lastFailure    configreload.ReloadResult
	sourcePosture  string
	modelGen       string
	attempts       atomic.Int64
	shutdown       atomic.Bool
	activeEff      *config.EffectiveConfig
	activeSource   *configsource.ActiveSourceVersion
	activeSourceMu sync.RWMutex

	// Host-owned attempt cancellation + idle barrier (req 13.7).
	// attemptCancel cancels the active Reload (including coalesced follow-ups).
	// attemptDone is non-nil while an attempt slot is armed; closed exactly once on release.
	attemptCancel context.CancelFunc
	attemptDone   chan struct{}
	attemptOnce   *sync.Once // closes attemptDone exactly once
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
		source:        deps.Source,
		loader:        deps.Loader,
		classify:      classify,
		compile:       deps.Compile,
		mgr:           deps.Manager,
		timeout:       timeout,
		observer:      deps.Observer,
		activeEff:     deps.ActiveEffective,
		activeSource:  cloneActiveSource(deps.ActiveSource),
		sourcePosture: "ok",
	}
	if active := deps.Manager.Active(); active != nil {
		c.last = configreload.ReloadResult{
			Category:         configreload.ResultPublished,
			ActiveGeneration: active.ID(),
		}
		c.lastSuccess = c.last
		if meta := active.Status().Meta; meta.PublicFingerprint != "" {
			c.modelGen = meta.PublicFingerprint
		}
	}
	return c, nil
}

// BeginShutdown rejects new attempts, cancels the host-owned reload context
// (including coalesced follow-ups), signals manager shutdown, and prevents late
// publication (req 1.9, 11.9, 13.7). It does not wait for rollback; callers that
// must close process services afterward should use [Coordinator.WaitForIdle].
func (c *Coordinator) BeginShutdown() {
	if c == nil {
		return
	}
	c.shutdown.Store(true)
	c.mu.Lock()
	c.pendingSignal = false
	cancel := c.attemptCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c.mgr != nil {
		c.mgr.BeginShutdown()
	}
}

// WaitForIdle blocks until no reload attempt (including coalesced follow-up) is
// in flight, or until ctx is done. It is safe after [Coordinator.BeginShutdown]
// and does not take request-path locks (req 13.7, 15.1).
func (c *Coordinator) WaitForIdle(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		done := c.attemptDone
		busy := c.busy
		c.mu.Unlock()
		if done == nil && !busy {
			return nil
		}
		if done == nil {
			// Narrow window: busy set but attempt not yet armed (or just released).
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
				continue
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			// Re-check: another coalesced attempt may have armed immediately.
		}
	}
}

// armAttempt registers cancel/done for the active Reload slot. If shutdown already
// began, cancel is invoked so BeginShutdown cannot miss the registration race.
// Caller must releaseAttempt exactly once for the returned done channel.
func (c *Coordinator) armAttempt(cancel context.CancelFunc) (done chan struct{}, shutdownAlready bool) {
	done = make(chan struct{})
	once := &sync.Once{}
	c.mu.Lock()
	c.attemptCancel = cancel
	c.attemptDone = done
	c.attemptOnce = once
	shutdownAlready = c.shutdown.Load()
	c.mu.Unlock()
	if shutdownAlready {
		cancel()
	}
	return done, shutdownAlready
}

// releaseAttempt clears the armed slot when it still matches done and closes
// done exactly once so WaitForIdle observers wake.
func (c *Coordinator) releaseAttempt(done chan struct{}, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	c.mu.Lock()
	once := c.attemptOnce
	if c.attemptDone == done {
		c.attemptCancel = nil
		c.attemptDone = nil
		c.attemptOnce = nil
	}
	c.mu.Unlock()
	if once != nil {
		once.Do(func() { close(done) })
	} else {
		// Slot already cleared by a prior release; still ensure done is closed.
		select {
		case <-done:
		default:
			close(done)
		}
	}
}

// Status returns a bounded safe snapshot (req 13.1-13.2, 14.1, 14.8).
func (c *Coordinator) Status() configreload.ReloadStatus {
	if c == nil {
		return configreload.ReloadStatus{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var activeID int64
	retained := 0
	pressure := false
	if c.mgr != nil {
		if g := c.mgr.Active(); g != nil {
			activeID = g.ID()
		}
		snap := c.mgr.ObservabilitySnapshot()
		retained = snap.Retired
		pressure = snap.RetentionWouldBlock
	}
	path := ""
	if c.source != nil {
		path = c.source.AbsolutePath()
	}
	var history []configreload.HistoryEntry
	if c.observer != nil && c.observer.History() != nil {
		history = c.observer.History().Snapshot()
	}
	var current *configreload.ReloadResult
	if c.busy {
		cur := c.last
		current = &cur
	}
	controlDegraded := c.lastFailure.Category != "" &&
		c.lastFailure.Category != configreload.ResultPublished &&
		c.lastFailure.Category != configreload.ResultNoop
	posture := c.sourcePosture
	if posture == "" {
		posture = "unknown"
	}
	return configreload.ReloadStatus{
		ActiveGeneration:    activeID,
		CurrentAttempt:      current,
		LastResult:          c.last,
		LastSuccess:         c.lastSuccess,
		LastFailure:         c.lastFailure,
		SourceIntegrity:     posture,
		RetainedGenerations: retained,
		RetentionPressure:   pressure,
		ControlDegraded:     controlDegraded,
		ModelGeneration:     c.modelGen,
		History:             history,
		Busy:                c.busy,
		FixedSourcePath:     path,
		PendingSignal:       c.pendingSignal,
		CoalescedSignals:    c.coalesced,
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

	// Parent cancel covers the first attempt and every coalesced follow-up so
	// BeginShutdown can abort the whole Reload slot (req 13.7).
	parentBase := context.WithoutCancel(ctx)
	if parentBase == nil {
		parentBase = context.Background()
	}
	parentCtx, parentCancel := context.WithCancel(parentBase)
	done, shut := c.armAttempt(parentCancel)
	defer c.releaseAttempt(done, parentCancel)
	if shut {
		c.mu.Lock()
		c.pendingSignal = false
		c.busy = false
		c.mu.Unlock()
		return c.terminal(configreload.ReloadResult{
			Category:       configreload.ResultCanceled,
			ReasonCategory: configreload.StageShutdown,
		}, false)
	}

	hostCtx, cancel := context.WithTimeout(parentCtx, c.timeout)
	defer cancel()

	first := c.runAttempt(hostCtx, trigger)
	c.recordTerminal(first)
	c.mu.Lock()
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

		followCtx, followCancel := context.WithTimeout(parentCtx, c.timeout)
		follow := c.runAttempt(followCtx, configreload.ReloadTrigger{
			Kind:       configreload.TriggerSIGHUP,
			AcceptedAt: time.Now().UTC(),
			SafeActor:  "coalesced-sighup",
		})
		followCancel()
		follow.CoalescedSignals = coal
		c.recordTerminal(follow)

		c.mu.Lock()
		// loop: another SIGHUP may have reserved pending during follow-up
	}
}

func (c *Coordinator) terminal(res configreload.ReloadResult, record bool) configreload.ReloadResult {
	if res.ActiveGeneration == 0 {
		res.ActiveGeneration = c.activeGenerationID()
	}
	if record {
		c.recordTerminal(res)
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

func (c *Coordinator) recordTerminal(res configreload.ReloadResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = res
	switch res.Category {
	case configreload.ResultPublished:
		c.lastSuccess = res
		c.sourcePosture = "ok"
		if c.mgr != nil {
			if g := c.mgr.Active(); g != nil {
				if fp := g.Status().Meta.PublicFingerprint; fp != "" {
					c.modelGen = fp
				}
			}
		}
	case configreload.ResultNoop:
		c.lastFailure = res // most recent failed/no-op per req 14.1
		c.sourcePosture = "ok"
	case configreload.ResultSourceIntegrity:
		c.lastFailure = res
		c.sourcePosture = "failed"
	case configreload.ResultBusy:
		// busy is not a completed attempt outcome for last-failure tracking
	default:
		c.lastFailure = res
	}
	if c.observer != nil {
		c.observer.RefreshGauges(c.mgr)
	}
}

func (c *Coordinator) runAttempt(ctx context.Context, trigger configreload.ReloadTrigger) (res configreload.ReloadResult) {
	attempt := c.attempts.Add(1)
	activeBefore := c.activeGenerationID()
	res = configreload.ReloadResult{
		AttemptID:        attempt,
		ActiveGeneration: activeBefore,
	}

	endAttempt := func(configreload.ReloadResult) {}
	if c.observer != nil {
		ctx, endAttempt = c.observer.BeginAttempt(ctx, trigger, attempt, activeBefore)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			res = configreload.ReloadResult{
				Category:         configreload.ResultInternalFailed,
				AttemptID:        attempt,
				ActiveGeneration: c.activeGenerationID(),
				ReasonCategory:   configreload.StagePanic,
			}
			_ = configreload.SanitizePanicValue(recovered)
		}
		if res.AttemptID == 0 {
			res.AttemptID = attempt
		}
		if res.ActiveGeneration == 0 {
			res.ActiveGeneration = c.activeGenerationID()
		}
		endAttempt(res)
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

	stageCtx, endStage := ctx, func(string) {}
	if c.observer != nil {
		stageCtx, endStage = c.observer.BeginStage(ctx, configreload.StageRead)
	}
	snap, atomicRes, err := c.source.ReadStable(stageCtx, activeSrc)
	if err != nil {
		if c.shutdown.Load() || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			res.Category = configreload.ResultCanceled
			res.ReasonCategory = configreload.StageShutdown
			endStage(string(res.Category))
			return res
		}
		cat, reason := configreload.MapLoadFailure(err)
		if srcCat, ok := configsource.CategoryOf(err); ok {
			cat, reason = configreload.MapLoadCategory(string(srcCat))
		}
		res.Category = cat
		res.ReasonCategory = reason
		endStage(string(cat))
		return res
	}
	endStage("ok")
	if atomicRes == configsource.AtomicNoop {
		res.Category = configreload.ResultNoop
		res.ReasonCategory = configreload.StageNoop
		return res
	}

	if c.observer != nil {
		stageCtx, endStage = c.observer.BeginStage(ctx, configreload.StageLoad)
	} else {
		stageCtx, endStage = ctx, func(string) {}
	}
	eff, err := c.loader.LoadEffective(stageCtx, snap.Bytes)
	if err != nil {
		if c.shutdown.Load() || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			res.Category = configreload.ResultCanceled
			res.ReasonCategory = configreload.StageShutdown
			endStage(string(res.Category))
			return res
		}
		cat, reason := configreload.MapLoadFailure(err)
		if srcCat, ok := configsource.CategoryOf(err); ok {
			cat, reason = configreload.MapLoadCategory(string(srcCat))
		}
		res.Category = cat
		res.ReasonCategory = reason
		endStage(string(cat))
		return res
	}
	endStage("ok")

	c.activeSourceMu.RLock()
	activeEff := c.activeEff
	c.activeSourceMu.RUnlock()
	if activeEff != nil && activeEff.Identity.PrivateDigest == eff.Identity.PrivateDigest {
		// AtomicEligible may have landed a new inode whose effective identity matches
		// the active generation. Advance the source baseline without publishing so a
		// later in-place rewrite of that inode is rejected as non-atomic (req 2.9).
		c.activeSourceMu.Lock()
		c.activeSource = &configsource.ActiveSourceVersion{
			HandleIdentity: snap.HandleIdentity,
			PrivateDigest:  snap.PrivateDigest,
		}
		c.activeSourceMu.Unlock()
		res.Category = configreload.ResultNoop
		res.ReasonCategory = configreload.StageNoop
		return res
	}

	if activeEff != nil {
		if c.observer != nil {
			stageCtx, endStage = c.observer.BeginStage(ctx, configreload.StageClassify)
		} else {
			stageCtx, endStage = ctx, func(string) {}
		}
		_, err := c.classify(activeEff, eff)
		_ = stageCtx
		if err != nil {
			var rr *configreload.RestartRequiredError
			if errors.As(err, &rr) {
				res.Category = configreload.ResultRestartRequired
				res.ReasonCategory = configreload.StageClassify
				if rr != nil {
					res.RestartFields = append([]string(nil), rr.RestartRequiredFields...)
					res.RestartFieldCount = rr.TotalBlocked
				}
				endStage(string(res.Category))
				return res
			}
			res.Category = configreload.ResultInvalid
			res.ReasonCategory = configreload.StageClassify
			endStage(string(res.Category))
			return res
		}
		endStage("ok")
	}

	if c.shutdown.Load() || (c.mgr != nil && c.mgr.ShuttingDown()) {
		res.Category = configreload.ResultCanceled
		res.ReasonCategory = configreload.StageShutdown
		return res
	}

	liveKinds := c.collectLiveFactoryKinds()
	if c.observer != nil {
		stageCtx, endStage = c.observer.BeginStage(ctx, configreload.StageCompile)
	} else {
		stageCtx, endStage = ctx, func(string) {}
	}
	plane, compileErr := c.compileIsolated(stageCtx, eff.Config, liveKinds)
	if compileErr != nil {
		var rr *configreload.RestartRequiredError
		if errors.As(compileErr, &rr) {
			res.Category = configreload.ResultRestartRequired
			res.ReasonCategory = configreload.StageCompile
			if rr != nil {
				res.RestartFields = append([]string(nil), rr.RestartRequiredFields...)
				res.RestartFieldCount = rr.TotalBlocked
			}
			endStage(string(res.Category))
			return res
		}
		if c.shutdown.Load() || errors.Is(compileErr, context.Canceled) || errors.Is(compileErr, context.DeadlineExceeded) {
			res.Category = configreload.ResultCanceled
			res.ReasonCategory = configreload.StageShutdown
			endStage(string(res.Category))
			return res
		}
		if errors.Is(compileErr, errCompilePanic) {
			res.Category = configreload.ResultInternalFailed
			res.ReasonCategory = configreload.StagePanic
			endStage(string(res.Category))
			return res
		}
		res.Category = configreload.ResultPreparationFailed
		res.ReasonCategory = configreload.StageCompile
		endStage(string(res.Category))
		return res
	}
	endStage("ok")
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

	if c.observer != nil {
		_, endStage = c.observer.BeginStage(ctx, configreload.StagePrepare)
	} else {
		endStage = func(string) {}
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
	endStage("ok")

	if c.shutdown.Load() || c.mgr.ShuttingDown() {
		_ = gen.Discard()
		res.Category = configreload.ResultCanceled
		res.ReasonCategory = configreload.StageShutdown
		return res
	}

	if c.observer != nil {
		_, endStage = c.observer.BeginStage(ctx, configreload.StagePublish)
	} else {
		endStage = func(string) {}
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
		endStage(string(res.Category))
		return res
	}
	endStage(string(configreload.ResultPublished))

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
			err = fmt.Errorf("%w: %s", errCompilePanic, configreload.SanitizePanicValue(recovered))
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
