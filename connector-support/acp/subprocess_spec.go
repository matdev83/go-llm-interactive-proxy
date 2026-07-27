package acp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// SubprocessConnectorSpec is the interface that concrete ACP CLI connectors
// (Cursor, Gemini, AGY) implement to provide vendor-specific configuration to
// the shared subprocess backend runner. The runner owns the runtime pool,
// stdio transport, handshake, history coordination, and event mapping; the
// spec provides only the vendor-specific pieces.
//
// This is the Go equivalent of Python's BaseAcpConnector abstract methods that
// each concrete connector overrides (VENDOR_PREFIX, _build_subprocess_command,
// _resolve_model, etc.).
type SubprocessConnectorSpec interface {
	// VendorID is the backend identifier (e.g. "cursorcliacp", "geminicliacp").
	VendorID() string

	// VendorPrefix is the model prefix for this connector (e.g. "cursor", "gemini").
	// Used for model ID resolution and vendor prefix stripping.
	VendorPrefix() string

	// BuildCommand returns the subprocess command args, working directory, and
	// environment for spawning the ACP agent. The model and workspace are
	// resolved by the runner before calling this.
	BuildCommand(model string, workspace string) (cmd []string, cwd string, env []string, err error)

	// HandshakeProfile returns the vendor-specific handshake configuration
	// (protocol version, skip-authenticate, client capabilities, etc.).
	HandshakeProfile() HandshakeProfile

	// CancelProfile returns the vendor-specific cancellation method list.
	CancelProfile() CancelProfile

	// ServerRequestHandler returns the vendor-specific handler for inbound
	// JSON-RPC requests from the agent (permissions, questions, plan approvals).
	// May return nil to use the default headless handler.
	ServerRequestHandler() ServerRequestHandler

	// RequiresExplicitWorkspace returns true if the connector requires an
	// explicit workspace directory (matching Python's requires_explicit_workspace).
	RequiresExplicitWorkspace() bool

	// ResolveModel strips the vendor prefix from the effective model and
	// returns the model identifier to use for subprocess pooling and command
	// building. If model is "auto" or empty, returns the connector's default.
	ResolveModel(effectiveModel string) string
}

// SubprocessBackendConfig configures the shared subprocess backend runner.
type SubprocessBackendConfig struct {
	// Protocol is the vendor-specific strategy that owns prompt-send and
	// stream-build. For ACP-session connectors (cursor/agy/gemini) use
	// NewACPProtocol(spec, log); for the Codex app-server use the
	// codexappserver package's protocol constructor.
	Protocol SubprocessProtocol

	// Workspace is the workspace resolution policy.
	Workspace WorkspacePolicy

	// Pool configures the subprocess lifecycle (idle reaping, stale kill).
	Pool RuntimePoolConfig

	// ProcessStarter spawns the subprocess. If nil, OSProcessStarter is used.
	ProcessStarter ProcessStarter

	// Log is optional for debug logging.
	Log *slog.Logger
}

// subprocessBackend is the shared ACP/Codex subprocess backend runner. It owns
// the lifecycle skeleton (workspace resolution, runtime pool, history
// divergence detection, ensure-process) and delegates the protocol-specific
// prompt-send and stream-build steps to a SubprocessProtocol.
//
// This is the Go equivalent of Python's BaseAcpConnector._acquire_runtime →
// _spawn_process → _initialize_runtime → _prepare_turn_request_locked →
// _stream_response_with_lock flow, generalized over both the ACP session
// protocol and the Codex app-server protocol.
type subprocessBackend struct {
	proto     SubprocessProtocol
	workspace WorkspacePolicy
	pool      *RuntimePool
	starter   ProcessStarter
	history   *TranscriptHistoryCoordinator
	log       *slog.Logger
}

// SubprocessEngine is the exported subprocess prompt-turn surface used by
// product connectors. Implementations own pool lifecycle and stream mapping.
type SubprocessEngine interface {
	Open(ctx context.Context, call *lipapi.Call) (lipapi.ManagedEventStream, error)
	Close() error
}

// NewSubprocessBackend creates a shared subprocess backend from the given config.
func NewSubprocessBackend(cfg SubprocessBackendConfig) SubprocessEngine {
	log := cfg.Log
	if log == nil {
		log = cfg.Pool.Log
	}
	if log == nil {
		log = slog.Default()
	}
	starter := cfg.ProcessStarter
	if starter == nil {
		starter = OSProcessStarter{}
	}
	poolCfg := cfg.Pool
	if poolCfg.Log == nil {
		poolCfg.Log = log
	}
	pool := NewRuntimePool(poolCfg)
	return &subprocessBackend{
		proto:     cfg.Protocol,
		workspace: cfg.Workspace,
		pool:      pool,
		starter:   starter,
		history:   &TranscriptHistoryCoordinator{},
		log:       log,
	}
}

// Close shuts down the backend: closes the pool (which kills all subprocesses
// and closes their transports).
func (b *subprocessBackend) Close() error {
	return b.pool.Close()
}

// runtimeKey builds a RuntimeKey from the call's workspace, model, and client session.
func (b *subprocessBackend) runtimeKey(workspace, model, clientSession string) RuntimeKey {
	return RuntimeKey{
		Workspace:     workspace,
		Model:         model,
		ClientSession: clientSession,
	}
}

// resolveWorkspaceFromCall extracts workspace hints from the call and resolves
// the workspace directory using the configured policy.
func (b *subprocessBackend) resolveWorkspaceFromCall(call *lipapi.Call) (string, error) {
	return b.workspace.ResolveWorkspace(WorkspaceHintsFromCall(call))
}

// ensureProcess spawns and initializes the subprocess if not already running.
// Returns the live transport and session/thread id; the caller (orchestrator)
// passes these to the protocol's BindSession, which wraps them in a
// vendor-specific client. The model parameter should already be resolved
// (vendor prefix stripped) by the caller.
func (b *subprocessBackend) ensureProcess(ctx context.Context, key RuntimeKey, model, workspace, processConfig string) (EnsureProcessResult, error) {
	res, err := b.pool.EnsureProcess(ctx, key, SpawnHandshakeStrategy{
		Spawn: func() ([]string, Process, Transport, error) {
			cmd, cwd, env, err := b.proto.BuildSpawnCommand(model, workspace, processConfig)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s: build command: %w", b.proto.Label(), err)
			}
			if len(cmd) == 0 {
				return nil, nil, nil, fmt.Errorf("%s: empty command", b.proto.Label())
			}
			proc, err := b.starter.Start(cmd, cwd, env)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s: start: %w", b.proto.Label(), err)
			}
			return cmd, proc, newStdioTransport(proc, b.log), nil
		},
		Handshake: func(ctx context.Context, transport Transport) (string, error) {
			sid, err := b.proto.Handshake(ctx, transport, model, workspace)
			if err != nil {
				return "", fmt.Errorf("%s: %w", b.proto.Label(), err)
			}
			return sid, nil
		},
		Log:              b.log,
		LogPrefix:        b.proto.Label(),
		ProcessConfigKey: processConfig,
	})
	if err != nil {
		return EnsureProcessResult{}, err
	}
	return res, nil
}

// Open opens a canonical event stream for one route candidate via the
// subprocess. This is the single lifecycle skeleton shared by the ACP session
// protocol (cursor/agy/gemini) and the Codex app-server protocol; the
// protocol-specific prompt-send and stream-build steps are delegated to
// [SubprocessProtocol].
func (b *subprocessBackend) Open(ctx context.Context, call *lipapi.Call) (lipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%s: %w", b.proto.Label(), lipapi.ErrNilContext)
	}
	if err := b.proto.ValidateCall(call); err != nil {
		return nil, err
	}

	// Resolve workspace.
	workspace, err := b.resolveWorkspaceFromCall(call)
	if err != nil {
		return nil, fmt.Errorf("%s: workspace: %w", b.proto.Label(), err)
	}

	// Resolve model (protocol-specific prefix stripping + defaults).
	model := b.proto.ResolveModel(call)

	// Resolve client session.
	clientSession := CallClientSession(call)

	key := b.runtimeKey(workspace, model, clientSession)

	// Acquire runtime from pool (handles idle reaping) before claiming the turn.
	if _, err := b.pool.Acquire(key); err != nil {
		return nil, fmt.Errorf("%s: acquire: %w", b.proto.Label(), err)
	}

	// Claim the runtime for this turn. Claiming is atomic with the process-scoped
	// config reset: a single stdio subprocess cannot carry two concurrent turns,
	// and killing an in-use transport would corrupt the in-flight turn, so a
	// concurrent peer is rejected (busy) and this call fails explicitly instead
	// of killing it. On a successful claim, a live child spawned with a different
	// process config (Codex model_verbosity) is killed and its transcript marker
	// reset so the new child receives a complete replay below.
	processConfig := b.proto.ResolveProcessConfig(call)
	rt, busy := b.pool.ClaimForTurn(key, processConfig)
	if busy {
		return nil, fmt.Errorf("%s: process config %q cannot be applied: a turn is in flight on runtime %s", b.proto.Label(), processConfig, key)
	}

	// Compute the transcript-based prompt from the runtime's history state.
	// On divergence (edited/truncated history), reset the agent process so it
	// re-receives the full transcript; ensureProcess spawns a fresh one.
	state := historyState{}
	if rt != nil {
		state = rt.HistoryState()
	}
	result := b.history.ComputeHistoryAndUserMessage(call.Messages, state)
	if result.ResetNeeded {
		_ = b.pool.KillRuntime(key)
	}

	// Ensure process is running (spawn + handshake). The transport is stored
	// internally for lifecycle management; the protocol binds it into a session.
	res, err := b.ensureProcess(ctx, key, model, workspace, processConfig)
	if err != nil {
		b.pool.Release(key)
		return nil, err
	}

	// Bind the live transport + session id into a protocol session that owns
	// the prompt-send and stream-build steps.
	session := b.proto.BindSession(res.Transport, res.SessionID)

	// Send the prompt/turn. The session owns the request body construction
	// (session/prompt with transcript blocks, or turn/start with text input).
	body, rpcID, err := session.SendPrompt(ctx, model, result.UserMessage, call)
	if err != nil {
		b.pool.Release(key)
		return nil, fmt.Errorf("%s: %w", b.proto.Label(), err)
	}

	// Commit the new history state after the prompt was accepted. On a client
	// retry with the same messages, ComputeHistoryAndUserMessage hits the
	// "same messages as last turn" branch and re-sends just the last user
	// message (parity with Python's _compute_history_and_user_message).
	if rt := b.pool.Get(key); rt != nil {
		rt.SetHistoryState(result.HistoryState)
	}

	// Build the protocol-specific event stream, wrapped to release the pool on
	// close (and flush tool summaries for ACP).
	return session.BuildStream(ctx, body, rpcID, b.pool, key, call.MaxPendingWireEvents)
}

// poolManagedStream wraps a promptStream to release the runtime pool on Close
// and flush pending tool summaries when the stream ends (EOF).
type poolManagedStream struct {
	inner    *promptStream
	pool     *RuntimePool
	key      RuntimeKey
	toolSink *toolSummarySink
	closed   bool
	flushed  bool
	mu       sync.Mutex
}

var _ lipapi.ManagedEventStream = (*poolManagedStream)(nil)

func (s *poolManagedStream) Recv(ctx context.Context) (lipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	// When the inner stream returns EOF, flush incomplete tool summaries
	// as text deltas BEFORE returning EOF so the consumer sees them.
	if err != nil && !s.flushed {
		s.mu.Lock()
		if !s.flushed && s.toolSink != nil {
			s.flushed = true
			evts := s.toolSink.FlushIncomplete()
			s.mu.Unlock()
			// If there are flushed events, return the first one now and
			// buffer the rest for subsequent Recv calls. The inner stream
			// is at EOF, so we inject these as the final events.
			if len(evts) > 0 {
				for i := 1; i < len(evts); i++ {
					_ = s.inner.PushPendingLocked(evts[i])
				}
				return evts[0], nil
			}
		} else {
			s.mu.Unlock()
		}
	}
	return ev, err
}

func (s *poolManagedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.pool.Release(s.key)
	return s.inner.Close()
}

func (s *poolManagedStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return s.inner.Cancel(ctx, cause)
}
