package acp

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// RuntimeKey identifies one ACP subprocess pool entry by workspace, model, and
// client session (parity with Python's _build_runtime_key tuple).
type RuntimeKey struct {
	Workspace     string
	Model         string
	ClientSession string
}

// SubprocessRuntime holds the state for one live ACP subprocess connection.
// It is the Go equivalent of Python's ACPProcessRuntime.
type SubprocessRuntime struct {
	mu sync.Mutex

	key       RuntimeKey
	proc      Process
	identity  ProcessIdentity
	transport Transport

	// sessionID is the ACP session id from session/new, or empty if not yet
	// initialized (or if the session was reset).
	sessionID string

	// lastActivity is updated on each prompt turn for idle reaping decisions.
	lastActivity time.Time

	// inUse is true while a prompt turn is active; prevents idle reaping.
	inUse bool

	// initialized is true after a successful handshake (initialize + authenticate).
	initialized bool

	// historyState tracks the conversation prefix for divergence detection.
	historyState historyState
}

// historyState tracks the message count and prefix hash for divergence detection.
type historyState struct {
	messageCount int
	prefixHash   string
}

// RuntimePoolConfig configures the subprocess lifecycle pool.
type RuntimePoolConfig struct {
	// IdleTimeout is how long a runtime may sit idle before it's eligible for
	// reaping on the next acquire. Zero disables idle reaping.
	IdleTimeout time.Duration
	// StaleKillDelay is how long after a prompt turn completes before the stale
	// kill timer fires. Zero disables stale kills.
	StaleKillDelay time.Duration
	// Log is optional for debug logging.
	Log *slog.Logger
}

// RuntimePool manages a pool of ACP subprocesses keyed by workspace+model+session.
// It handles idle reaping, stale killing with PID-reuse hardening, and concurrent
// access safety. This is the Go equivalent of Python's BaseAcpConnector runtime pool.
type RuntimePool struct {
	cfg   RuntimePoolConfig
	log   *slog.Logger
	mu    sync.Mutex
	pools map[RuntimeKey]*SubprocessRuntime

	// staleKillTimers tracks active stale-kill timers per runtime for cancellation.
	staleKillMu     sync.Mutex
	staleKillTimers map[RuntimeKey]*time.Timer

	ensureGroup singleflight.Group
}

// NewRuntimePool creates a new subprocess lifecycle pool.
// closeTransportQuietly closes transport, logging close errors via the pool
// logger. Lifecycle-cleanup paths have no primary error to attach, so the
// single-handling rule routes the close error to Debug instead of discarding
// with `_`. Safe to call with a nil transport or nil log.
func closeTransportQuietly(log *slog.Logger, key RuntimeKey, phase string, transport Transport) {
	if transport == nil {
		return
	}
	if err := transport.Close(); err != nil && log != nil {
		log.Debug("acp pool: transport close failed", "key", key, "phase", phase, "error", err)
	}
}

// killAndWaitQuietly performs Kill+Wait on proc, logging each non-nil result via
// the pool logger. Same lifecycle-cleanup rationale as closeTransportQuietly.
// Sequential order: Kill first (terminate), Wait second (reap, closes pipes).
func killAndWaitQuietly(log *slog.Logger, key RuntimeKey, phase string, proc Process) {
	if proc == nil {
		return
	}
	if err := proc.Kill(); err != nil && log != nil {
		log.Debug("acp pool: proc kill failed", "key", key, "phase", phase, "error", err)
	}
	if err := proc.Wait(); err != nil && log != nil {
		log.Debug("acp pool: proc wait failed", "key", key, "phase", phase, "error", err)
	}
}

func NewRuntimePool(cfg RuntimePoolConfig) *RuntimePool {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &RuntimePool{
		cfg:             cfg,
		log:             log,
		pools:           make(map[RuntimeKey]*SubprocessRuntime),
		staleKillTimers: make(map[RuntimeKey]*time.Timer),
	}
}

// Acquire returns the runtime for key, creating a fresh entry if none exists.
// If the existing runtime's process is idle past IdleTimeout, it is killed and
// replaced with a fresh slot. The caller must call Release when done with the
// runtime (typically after the prompt turn completes).
func (p *RuntimePool) Acquire(key RuntimeKey) (*SubprocessRuntime, error) {
	p.mu.Lock()
	rt, exists := p.pools[key]
	if !exists {
		rt = &SubprocessRuntime{key: key}
		p.pools[key] = rt
	}
	p.mu.Unlock()

	if p.cfg.IdleTimeout > 0 {
		if replaced := p.tryReapIdle(key, rt); replaced != nil {
			rt = replaced
		}
	}
	return rt, nil
}

// tryReapIdle checks if the runtime is idle past the timeout and replaces it
// with a fresh slot. Returns the replacement if one was made, nil otherwise.
func (p *RuntimePool) tryReapIdle(key RuntimeKey, rt *SubprocessRuntime) *SubprocessRuntime {
	rt.mu.Lock()
	if rt.inUse {
		rt.mu.Unlock()
		return nil
	}
	if rt.proc == nil {
		// Process already gone; re-read the pool in case another goroutine
		// already reaped and replaced this entry.
		rt.mu.Unlock()
		p.mu.Lock()
		canonical := p.pools[key]
		p.mu.Unlock()
		if canonical != nil && canonical != rt {
			return canonical
		}
		return nil
	}
	if rt.lastActivity.IsZero() {
		rt.mu.Unlock()
		return nil
	}
	if time.Since(rt.lastActivity) < p.cfg.IdleTimeout {
		rt.mu.Unlock()
		return nil
	}
	// Kill the stale process. Close the transport first to release stdin and
	// unblock the readLoop/drainStderr goroutines before killing the process.
	// transport.Close() already calls proc.Kill() + proc.Wait(), so we only
	// kill/wait manually when there's no transport (shouldn't happen in practice
	// since proc and transport are always set together, but guards defensively).
	proc := rt.proc
	transport := rt.transport
	rt.proc = nil
	rt.transport = nil
	rt.initialized = false
	rt.sessionID = ""
	rt.mu.Unlock()

	p.cancelStaleKill(key)
	if transport != nil {
		closeTransportQuietly(p.log, key, "reap", transport)
	} else {
		killAndWaitQuietly(p.log, key, "reap", proc)
	}
	if p.log != nil {
		p.log.Debug("acp: idle runtime reaped", "key", key)
	}

	// Replace the pool entry with a fresh slot.
	p.mu.Lock()
	current := p.pools[key]
	if current == rt {
		replacement := &SubprocessRuntime{key: key}
		p.pools[key] = replacement
		p.mu.Unlock()
		return replacement
	}
	p.mu.Unlock()
	return current
}

// SetProcess attaches a spawned process and transport to the runtime and marks
// it as initialized. Called after a successful subprocess spawn + handshake.
// cmdFirstArg is the first element of the spawn command (e.g. "cursor-agent"),
// used as a fallback for the executable path in PID-reuse hardening.
func (p *RuntimePool) SetProcess(key RuntimeKey, proc Process, transport Transport, sessionID string, cmdFirstArg string) {
	p.mu.Lock()
	rt, exists := p.pools[key]
	if !exists {
		rt = &SubprocessRuntime{key: key}
		p.pools[key] = rt
	}
	p.mu.Unlock()

	rt.mu.Lock()
	rt.proc = proc
	rt.identity = captureProcessIdentity(proc, cmdFirstArg)
	rt.transport = transport
	rt.sessionID = sessionID
	rt.initialized = true
	rt.lastActivity = time.Now()
	rt.mu.Unlock()
}

// MarkInUse marks the runtime as actively processing a prompt turn.
func (p *RuntimePool) MarkInUse(key RuntimeKey) {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.inUse = true
	p.cancelStaleKill(key)
	rt.mu.Unlock()
}

// Release marks the runtime as no longer in use, updates lastActivity, and
// schedules a stale-kill timer if configured.
func (p *RuntimePool) Release(key RuntimeKey) {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.inUse = false
	rt.lastActivity = time.Now()
	rt.mu.Unlock()

	if p.cfg.StaleKillDelay > 0 {
		p.scheduleStaleKill(key)
	}
}

// Get returns the runtime for key without side effects, or nil if none exists.
func (p *RuntimePool) Get(key RuntimeKey) *SubprocessRuntime {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pools[key]
}

// KillRuntime kills the process for the given key and resets the runtime state.
// Safe to call even if the process has already exited. The transport is closed
// (which kills the process, closes stdin, and unblocks goroutines) unless
// PID-reuse hardening detects the original process has already exited and the
// PID may have been reused — in that case, killing would target the wrong process.
func (p *RuntimePool) KillRuntime(key RuntimeKey) error {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return nil
	}
	p.cancelStaleKill(key)
	rt.mu.Lock()
	proc := rt.proc
	transport := rt.transport
	identity := rt.identity
	rt.proc = nil
	rt.transport = nil
	rt.initialized = false
	rt.sessionID = ""
	rt.mu.Unlock()

	if proc == nil {
		// Orphaned-runtime edge case: process is already gone, close the cached transport.
		closeTransportQuietly(p.log, key, "orphan", transport)
		return nil
	}
	// PID-reuse hardening: verify the process is still the one we started.
	// If the PID has been reused, the original process already exited — don't
	// kill (would target the wrong process). The transport's goroutines will
	// exit on stdout/stderr EOF since the process is already dead.
	if identity.PID > 0 && !stillSameProcess(proc, identity) {
		if p.log != nil {
			p.log.Debug("acp: stale kill skipped — process identity mismatch", "key", key, "pid", identity.PID)
		}
		return nil
	}
	// PID matches: close transport (kills process, closes stdin, cleans up
	// goroutines) or kill/wait directly if no transport exists.
	if transport != nil {
		closeTransportQuietly(p.log, key, "kill", transport)
	} else {
		killAndWaitQuietly(p.log, key, "kill", proc)
	}
	return nil
}

// Close kills all runtimes in the pool and clears it. Used during shutdown.
func (p *RuntimePool) Close() error {
	p.mu.Lock()
	keys := make([]RuntimeKey, 0, len(p.pools))
	for k := range p.pools {
		keys = append(keys, k)
	}
	p.mu.Unlock()

	for _, k := range keys {
		_ = p.KillRuntime(k)
	}

	p.mu.Lock()
	p.pools = make(map[RuntimeKey]*SubprocessRuntime)
	p.mu.Unlock()

	p.staleKillMu.Lock()
	for k, timer := range p.staleKillTimers {
		timer.Stop()
		delete(p.staleKillTimers, k)
	}
	p.staleKillMu.Unlock()
	return nil
}

// scheduleStaleKill arms a timer to kill the runtime after StaleKillDelay.
// If a timer is already armed, it is cancelled first.
func (p *RuntimePool) scheduleStaleKill(key RuntimeKey) {
	p.cancelStaleKill(key)

	timer := time.AfterFunc(p.cfg.StaleKillDelay, func() {
		p.mu.Lock()
		rt := p.pools[key]
		p.mu.Unlock()
		if rt == nil {
			return
		}
		rt.mu.Lock()
		inUse := rt.inUse
		proc := rt.proc
		rt.mu.Unlock()

		if inUse {
			return
		}
		if proc == nil {
			return
		}
		// KillRuntime handles PID-reuse hardening internally and clears state
		// regardless of whether the kill is skipped (identity mismatch) or executed.
		if err := p.KillRuntime(key); err != nil {
			if p.log != nil {
				p.log.Debug("acp: stale kill failed", "key", key, "error", err)
			}
		}
	})

	p.staleKillMu.Lock()
	p.staleKillTimers[key] = timer
	p.staleKillMu.Unlock()
}

// cancelStaleKill stops the stale-kill timer for key if one is armed.
func (p *RuntimePool) cancelStaleKill(key RuntimeKey) {
	p.staleKillMu.Lock()
	timer, ok := p.staleKillTimers[key]
	if ok {
		timer.Stop()
		delete(p.staleKillTimers, key)
	}
	p.staleKillMu.Unlock()
}

// String returns a debug-friendly representation of the pool state.
func (k RuntimeKey) String() string {
	return fmt.Sprintf("{workspace=%s model=%s session=%s}", k.Workspace, k.Model, k.ClientSession)
}

// SessionID returns the ACP session id for the runtime, or empty if not initialized.
func (r *SubprocessRuntime) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID
}

// IsInitialized returns true if the runtime has completed a successful handshake.
func (r *SubprocessRuntime) IsInitialized() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initialized
}

// HasProcess returns true if the runtime has a live subprocess.
func (r *SubprocessRuntime) HasProcess() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proc != nil
}

// Transport returns the transport for the runtime, or nil if the process has
// not been spawned or has been killed/reaped. The transport and process are
// always set together by SetProcess and cleared together by KillRuntime/reap,
// so HasProcess() == true implies Transport() != nil.
func (r *SubprocessRuntime) Transport() Transport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.transport
}

// HistoryState returns the current history state for divergence detection.
func (r *SubprocessRuntime) HistoryState() historyState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.historyState
}

// FreshHistoryState returns a zero history state representing a fresh agent
// process with no prior conversation. Cross-package callers use it as the
// starting state when no runtime entry exists yet; same-package callers can
// use the historyState zero value directly.
func FreshHistoryState() historyState { return historyState{} }

// SetHistoryState updates the history state after a successful prompt turn.
func (r *SubprocessRuntime) SetHistoryState(state historyState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.historyState = state
}
