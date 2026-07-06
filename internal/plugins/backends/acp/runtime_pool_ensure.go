package acp

import (
	"context"
	"fmt"
	"log/slog"
)

// SpawnHandshakeStrategy parameterizes the shared process-ensuring flow so the
// ACP CLI subprocess backends and the Codex app-server backend can reuse common
// reuse/spawn/register scaffolding while keeping their distinct handshake and
// command-building protocols.
//
// Spawn returns the command (first element used as a fallback executable path
// for PID-reuse hardening in SetProcess), the started process, and the transport.
// Handshake runs the vendor-specific handshake over the transport and returns the
// session/thread id to store in the runtime pool. Both callbacks may wrap their
// errors with vendor-specific context so callers see protocol-specific messages.
type SpawnHandshakeStrategy struct {
	// Spawn produces a fresh subprocess + transport. It must not perform the
	// handshake; that is Handshake's responsibility, so the shared scaffolding can
	// close the transport uniformly on handshake failure.
	Spawn func() (cmd []string, proc Process, transport Transport, err error)

	// Handshake runs the vendor-specific initialize/session-or-thread exchange over
	// the transport and returns the session/thread id to register in the pool.
	Handshake func(ctx context.Context, transport Transport) (sessionID string, err error)

	// Log receives debug lifecycle messages; may be nil.
	Log *slog.Logger

	// LogPrefix prefixes debug log messages (e.g. "acp subprocess", "codex app-server").
	LogPrefix string
}

// EnsureProcessResult is the outcome of a shared ensure-process call. The
// transport is the live transport for the runtime; callers wrap it in their own
// vendor-specific client. SessionID is the ACP session id or Codex thread id.
type EnsureProcessResult struct {
	Transport Transport
	SessionID string
}

// EnsureProcess returns a live transport and session id for the runtime
// identified by key, reusing an existing initialized process when present, or
// spawning a fresh subprocess via strategy.Spawn and running strategy.Handshake
// otherwise. A stale or uninitialized runtime is killed first so goroutines and
// pipes from any prior process exit cleanly before a new one is spawned.
//
// On handshake failure the freshly created transport is closed (single-handling
// rule: the close error is logged at Debug, the originating handshake error is
// returned). On success the process/transport/sessionID are registered via
// SetProcess so the pool owns their lifecycle (idle reaping, stale kill, PID-reuse
// hardening).
func (p *RuntimePool) EnsureProcess(ctx context.Context, key RuntimeKey, strategy SpawnHandshakeStrategy) (EnsureProcessResult, error) {
	if rt := p.Get(key); rt != nil && rt.HasProcess() && rt.IsInitialized() {
		if t := rt.Transport(); t != nil {
			return EnsureProcessResult{Transport: t, SessionID: rt.SessionID()}, nil
		}
	}

	// We use singleflight to serialize concurrent ensure attempts for the same key.
	skey := fmt.Sprintf("%s\x00%s\x00%s", key.Workspace, key.Model, key.ClientSession)
	val, err, _ := p.ensureGroup.Do(skey, func() (any, error) {
		// Re-check after acquiring the singleflight lock.
		if rt := p.Get(key); rt != nil && rt.HasProcess() && rt.IsInitialized() {
			if t := rt.Transport(); t != nil {
				return EnsureProcessResult{Transport: t, SessionID: rt.SessionID()}, nil
			}
		}

		// Kill any stale process. KillRuntime closes the old transport before killing,
		// so the prior process's readLoop/drainStderr goroutines exit cleanly.
		_ = p.KillRuntime(key)

		cmd, proc, transport, err := strategy.Spawn()
		if err != nil {
			return EnsureProcessResult{}, err
		}

		sid, err := strategy.Handshake(ctx, transport)
		if err != nil {
			// Single-handling rule: log the close failure (recovery-path cleanup) and
			// return the originating handshake error. Pass ctx to DebugContext so the
			// cleanup log line is correlated with the same trace as the handshake.
			if closeErr := transport.Close(); closeErr != nil && strategy.Log != nil {
				strategy.Log.DebugContext(ctx, strategy.LogPrefix+": transport cleanup after handshake failure", "error", closeErr)
			}
			return EnsureProcessResult{}, err
		}

		cmdFirst := ""
		if len(cmd) > 0 {
			cmdFirst = cmd[0]
		}
		p.SetProcess(key, proc, transport, sid, cmdFirst)
		return EnsureProcessResult{Transport: transport, SessionID: sid}, nil
	})

	if err != nil {
		return EnsureProcessResult{}, err
	}
	got, ok := val.(EnsureProcessResult)
	if !ok {
		return EnsureProcessResult{}, fmt.Errorf("internal: ensure cache value of unexpected type %T for key %s", val, key)
	}
	return got, nil
}
