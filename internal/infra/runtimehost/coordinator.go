package runtimehost

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// DefaultReloadTimeout is the host-owned reload attempt bound when unset.
const DefaultReloadTimeout = time.Minute

// StableConfigSource is the fixed-path source seam (typically configsource.FixedSource).
// Callers never supply a path or YAML through the canonical Trigger envelope.
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
// It orchestrates the gate, runner, and observer without directly
// implementing detailed source/load/classification/compile branches (req 6.4);
// source is kept only for FixedSourcePath (AbsolutePath), never for reload
// execution.
type Coordinator struct {
	source   StableConfigSource
	mgr      *Manager
	timeout  time.Duration
	observer *ReloadObserver
	gate     *attemptGate
	runner   *attemptRunner

	mu             sync.Mutex
	last           sdkreload.Result
	lastSuccess    sdkreload.Result
	lastFailure    sdkreload.Result
	sourcePosture  string
	modelGen       string
	attempts       atomic.Int64
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
	timeout := deps.Timeout
	if timeout <= 0 {
		timeout = DefaultReloadTimeout
	}
	gate := newAttemptGate()
	c := &Coordinator{
		source:        deps.Source,
		mgr:           deps.Manager,
		timeout:       timeout,
		observer:      deps.Observer,
		gate:          gate,
		activeEff:     deps.ActiveEffective,
		activeSource:  cloneActiveSource(deps.ActiveSource),
		sourcePosture: "ok",
	}
	c.runner = newAttemptRunner(attemptRunnerDeps{
		Source:       deps.Source,
		Loader:       deps.Loader,
		Classify:     deps.Classify,
		Compile:      deps.Compile,
		Manager:      deps.Manager,
		Observer:     deps.Observer,
		ShuttingDown: gate.shuttingDown,
	})
	if active := deps.Manager.Active(); active != nil {
		c.last = sdkreload.Result{
			Category:         sdkreload.ResultPublished,
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
	if c.gate != nil {
		c.gate.BeginShutdown()
	}
	if c.mgr != nil {
		c.mgr.BeginShutdown()
	}
}

// WaitForIdle blocks until no reload attempt (including coalesced follow-up) is
// in flight, or until ctx is done. It is safe after [Coordinator.BeginShutdown]
// and does not take request-path locks (req 13.7, 15.1).
func (c *Coordinator) WaitForIdle(ctx context.Context) error {
	if c == nil || c.gate == nil {
		return nil
	}
	return c.gate.WaitForIdle(ctx)
}

// Status returns a bounded safe snapshot (req 13.1-13.2, 14.1, 14.8).
// The snapshot is secret-safe and must not include filesystem paths.
// Management adapters that need the fixed startup source should call
// FixedSourcePath and map it into transport DTOs only
// (canonical Status stays path-free).
func (c *Coordinator) Status() sdkreload.Status {
	if c == nil {
		return sdkreload.Status{}
	}
	// Gate snapshot first so Coordinator and gate locks are never held together.
	var gateSnap attemptGateSnapshot
	if c.gate != nil {
		gateSnap = c.gate.Snapshot()
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
	var history []sdkreload.HistoryEntry
	if c.observer != nil && c.observer.History() != nil {
		history = c.observer.History().Snapshot()
	}
	var current *sdkreload.Result
	if gateSnap.Busy {
		cur := c.last
		current = &cur
	}
	controlDegraded := c.lastFailure.Category != "" &&
		c.lastFailure.Category != sdkreload.ResultPublished &&
		c.lastFailure.Category != sdkreload.ResultNoop
	posture := c.sourcePosture
	if posture == "" {
		posture = "unknown"
	}
	return sdkreload.Status{
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
		Busy:                gateSnap.Busy,
		PendingSignal:       gateSnap.PendingSignal,
		CoalescedSignals:    gateSnap.CoalescedSignals,
	}.Clone()
}

// Reload runs one serialized attempt. API callers receive Busy when an attempt is
// active; SIGHUP coalesces into at most one pending follow-up (req 11.4-11.6).
// The attempt uses a host-owned timeout independent of API client cancel (req 12.9).
func (c *Coordinator) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if c == nil {
		return sdkreload.Result{Category: sdkreload.ResultInternalFailed, ReasonCategory: "nil-coordinator"}
	}
	if c.gate == nil {
		return sdkreload.Result{Category: sdkreload.ResultInternalFailed, ReasonCategory: "nil-attempt-gate"}
	}
	if c.shuttingDown() {
		return c.terminal(sdkreload.Result{
			Category:       sdkreload.ResultCanceled,
			ReasonCategory: configreload.StageShutdown,
		}, false)
	}

	admission := c.gate.TryStart(ctx, trigger)
	switch admission.Kind {
	case admissionRejectedShutdown:
		return c.terminal(sdkreload.Result{
			Category:       sdkreload.ResultCanceled,
			ReasonCategory: configreload.StageShutdown,
		}, false)
	case admissionBusyAPI:
		return sdkreload.Result{
			Category:         sdkreload.ResultBusy,
			ActiveGeneration: c.activeGenerationID(),
			ReasonCategory:   configreload.StageBusy,
		}
	case admissionPendingHUP, admissionCoalescedHUP:
		return sdkreload.Result{
			Category:         sdkreload.ResultBusy,
			ActiveGeneration: c.activeGenerationID(),
			ReasonCategory:   configreload.StageCoalesce,
			CoalescedSignals: admission.CoalescedSignals,
		}
	case admissionAdmitted:
		// proceed
	default:
		return c.terminal(sdkreload.Result{
			Category:       sdkreload.ResultInternalFailed,
			ReasonCategory: "unknown-admission",
		}, false)
	}

	lease := admission.Lease
	if lease == nil {
		return c.terminal(sdkreload.Result{
			Category:       sdkreload.ResultInternalFailed,
			ReasonCategory: "nil-lease",
		}, false)
	}
	// Gate-owned cleanup: abandon the current lease variable on any exit path
	// (including panic outside runner.Run). After Complete advances to a
	// follow-up, lease is reassigned so this defer targets the active token.
	// After a normal final Complete, Abandon is an idempotent no-op.
	defer func() { lease.Abandon() }()

	hostCtx, cancel := context.WithTimeout(lease.Context(), c.timeout)
	first := c.applyOutcome(c.runner.Run(hostCtx, c.newAttemptInput(trigger)))
	cancel()
	c.recordTerminal(first)

	for {
		// Manager-only shutdown must fold into the gate so Complete clears
		// pending follow-up and rejects further progression.
		if c.mgr != nil && c.mgr.ShuttingDown() {
			c.gate.BeginShutdown()
		}
		fin := lease.Complete()
		switch fin.Kind {
		case finishReleasedIdle, finishAlreadyCompleted:
			return first
		case finishFollowUpClaimed:
			if fin.FollowUpLease == nil {
				return first
			}
			lease = fin.FollowUpLease
			followCtx, followCancel := context.WithTimeout(lease.Context(), c.timeout)
			followTrigger := sdkreload.Trigger{
				Kind:       sdkreload.TriggerSIGHUP,
				AcceptedAt: time.Now().UTC(),
				SafeActor:  "coalesced-sighup",
			}
			follow := c.applyOutcome(c.runner.Run(followCtx, c.newAttemptInput(followTrigger)))
			followCancel()
			follow.CoalescedSignals = fin.CoalescedSignals
			c.recordTerminal(follow)
			// loop: another SIGHUP may have reserved pending during follow-up
		default:
			return first
		}
	}
}

// newAttemptInput allocates the next attempt ID and snapshots the current
// active effective/source state for one runner.Run transaction. Cloning the
// mutable ActiveSource at this boundary keeps attemptRunner's input immutable
// for the duration of the call (req 6.2, 6.10-6.11).
func (c *Coordinator) newAttemptInput(trigger sdkreload.Trigger) attemptInput {
	attempt := c.attempts.Add(1)
	activeBefore := c.activeGenerationID()
	c.activeSourceMu.RLock()
	activeEff := c.activeEff
	activeSrc := cloneActiveSource(c.activeSource)
	c.activeSourceMu.RUnlock()
	return attemptInput{
		Trigger:          trigger,
		AttemptID:        attempt,
		ActiveGeneration: activeBefore,
		ActiveEffective:  activeEff,
		ActiveSource:     activeSrc,
	}
}

// applyOutcome commits a runner-returned immutable outcome's state updates
// (if any) before returning its canonical result for terminal recording.
// Neither field is set on a failed attempt; SourceUpdate alone marks an
// effective-identity no-op baseline advance; both are set on publication.
func (c *Coordinator) applyOutcome(outcome attemptOutcome) sdkreload.Result {
	if outcome.EffectiveUpdate != nil || outcome.SourceUpdate != nil {
		c.activeSourceMu.Lock()
		if outcome.EffectiveUpdate != nil {
			c.activeEff = outcome.EffectiveUpdate
		}
		if outcome.SourceUpdate != nil {
			c.activeSource = outcome.SourceUpdate
		}
		c.activeSourceMu.Unlock()
	}
	return outcome.Result
}

func (c *Coordinator) terminal(res sdkreload.Result, record bool) sdkreload.Result {
	if res.ActiveGeneration == 0 {
		res.ActiveGeneration = c.activeGenerationID()
	}
	if record {
		c.recordTerminal(res)
	}
	return res
}

// shuttingDown reports gate-owned shutdown truth plus Manager shutdown where
// currently required for late-publication checks.
func (c *Coordinator) shuttingDown() bool {
	if c == nil {
		return true
	}
	if c.gate != nil && c.gate.shuttingDown() {
		return true
	}
	return c.mgr != nil && c.mgr.ShuttingDown()
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

func (c *Coordinator) recordTerminal(res sdkreload.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = res
	switch res.Category {
	case sdkreload.ResultPublished:
		c.lastSuccess = res
		c.sourcePosture = "ok"
		if c.mgr != nil {
			if g := c.mgr.Active(); g != nil {
				if fp := g.Status().Meta.PublicFingerprint; fp != "" {
					c.modelGen = fp
				}
			}
		}
	case sdkreload.ResultNoop:
		c.lastFailure = res // most recent failed/no-op per req 14.1
		c.sourcePosture = "ok"
	case sdkreload.ResultSourceIntegrity:
		c.lastFailure = res
		c.sourcePosture = "failed"
	case sdkreload.ResultBusy:
		// busy is not a completed attempt outcome for last-failure tracking
	default:
		c.lastFailure = res
	}
	if c.observer != nil {
		c.observer.RefreshGauges(c.mgr)
	}
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
