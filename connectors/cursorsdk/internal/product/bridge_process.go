package product

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
)

const bridgeImplVersion = "go-cursorsdk/0.1.0"

type BridgeInfo struct {
	SchemaVersion    int
	ImplVersion      string
	SDKVersion       string
	NodeVersion      string
	Capabilities     []string
	SandboxSupported bool
	Generation       int64
}

type bridgeOpts struct {
	Starter                ProcessStarter
	HostEnv                []string
	Log                    *slog.Logger
	Diag                   *Diag
	Inspector              processInspector
	OnBridgeGenerationDead OnBridgeGenerationDead
}

const defaultReapTimeout = 5 * time.Second

type bridgeState int

const (
	bridgeIdle bridgeState = iota
	bridgeReady
	bridgeFailed
	bridgeClosing
	bridgeClosed
)

type pendingCall struct {
	ch     chan callResult
	method string
}

type callResult struct {
	frame *protocol.Frame
	err   error
}

type startFlight struct {
	done chan struct{}
	info BridgeInfo
	err  error
}

type bridgeProcess struct {
	cfg                    Config
	starter                ProcessStarter
	hostEnv                []string
	log                    *slog.Logger
	diag                   *Diag
	inspector              processInspector
	onBridgeGenerationDead OnBridgeGenerationDead

	idSeq atomic.Uint64

	mu          sync.Mutex
	state       bridgeState
	gen         int64
	info        BridgeInfo
	proc        Process
	identity    processIdentity
	pending     map[string]*pendingCall
	runs        map[string]*runSub
	stderrBuf   []byte
	waitDone    chan struct{}
	waitErr     error
	startFlight *startFlight
	writeMu     sync.Mutex
	closed      atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

func newBridgeProcess(cfg Config, opts bridgeOpts) *bridgeProcess {
	starter := opts.Starter
	if starter == nil {
		starter = OSProcessStarter{}
	}
	hostEnv := opts.HostEnv
	if hostEnv == nil {
		hostEnv = os.Environ()
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	ins := opts.Inspector
	if ins.StartTime == nil && ins.ExePath == nil {
		ins = defaultProcessInspector()
	}
	return &bridgeProcess{
		cfg:                    cfg,
		starter:                starter,
		hostEnv:                hostEnv,
		log:                    log,
		diag:                   opts.Diag,
		inspector:              ins,
		onBridgeGenerationDead: opts.OnBridgeGenerationDead,
		pending:                make(map[string]*pendingCall),
		runs:                   make(map[string]*runSub),
		state:                  bridgeIdle,
	}
}

func (b *bridgeProcess) statusSnapshot() (BridgeInfo, string) {
	if b == nil {
		return BridgeInfo{}, "closed"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.info, bridgeStateName(b.state)
}

func (b *bridgeProcess) EnsureReady(ctx context.Context) (BridgeInfo, error) {
	if err := ctx.Err(); err != nil {
		return BridgeInfo{}, err
	}
	if b.closed.Load() {
		return BridgeInfo{}, errors.New("cursorsdk: bridge closed")
	}

	b.mu.Lock()
	if b.state == bridgeReady && b.proc != nil {
		info := b.info
		b.mu.Unlock()
		return info, nil
	}
	if b.state == bridgeClosed || b.state == bridgeClosing {
		b.mu.Unlock()
		return BridgeInfo{}, errors.New("cursorsdk: bridge closed")
	}
	fl := b.startFlight
	if fl == nil {
		fl = &startFlight{done: make(chan struct{})}
		b.startFlight = fl
		go b.runStartFlight(fl)
	}
	b.mu.Unlock()

	select {
	case <-ctx.Done():
		return BridgeInfo{}, ctx.Err()
	case <-fl.done:
		if fl.err != nil {
			return BridgeInfo{}, fl.err
		}
		return fl.info, nil
	}
}

func (b *bridgeProcess) runStartFlight(fl *startFlight) {
	defer func() {
		b.mu.Lock()
		if b.startFlight == fl {
			b.startFlight = nil
		}
		b.mu.Unlock()
		close(fl.done)
	}()

	timeout := b.cfg.BridgeStartTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	startCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fl.info, fl.err = b.startAndHandshake(startCtx)
}

func (b *bridgeProcess) startAndHandshake(ctx context.Context) (BridgeInfo, error) {
	if err := b.reapCurrentLocked(); err != nil {
		return BridgeInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return BridgeInfo{}, err
	}
	argv := b.cfg.BridgeArgv()
	if len(argv) == 0 {
		return BridgeInfo{}, NewBridgeFault(CodeBridgeMissing, ErrBridgeMissing, "bridge executable is empty")
	}
	env := SelectHostEnv(b.hostEnv, b.cfg.BridgeEnvAllowlist)

	started := make(chan startResult, 1)
	go func() {
		proc, err := b.starter.Start(argv, "", env)
		started <- startResult{proc: proc, err: err}
	}()

	var proc Process
	select {
	case <-ctx.Done():
		go b.reapLateStart(started)
		return BridgeInfo{}, ctx.Err()
	case out := <-started:
		if out.err != nil {
			return BridgeInfo{}, wrapStartError(out.err)
		}
		proc = out.proc
	}

	b.mu.Lock()
	if b.closed.Load() || b.state == bridgeClosing || b.state == bridgeClosed {
		b.mu.Unlock()
		_ = proc.Kill()
		_ = proc.Wait()
		return BridgeInfo{}, errors.New("cursorsdk: bridge closed")
	}
	b.gen++
	gen := b.gen
	b.proc = proc
	b.identity = b.inspector.capture(proc, argv[0])
	b.waitDone = make(chan struct{})
	b.waitErr = nil
	b.stderrBuf = nil
	b.state = bridgeIdle
	identity := b.identity
	done := b.waitDone
	b.mu.Unlock()

	go b.readStdout(proc, gen)
	go b.readStderr(proc, gen)
	go b.waitProc(proc, gen, done)

	frame, err := b.callOnProc(ctx, proc, gen, protocol.MethodInitialize, mustJSON(protocol.InitializeParams{
		ImplVersion: bridgeImplVersion,
	}))
	if err != nil {
		b.failGeneration(gen, err)
		_ = b.killAndReapGeneration(gen, identity)
		return BridgeInfo{}, err
	}
	if frame.Error != nil {
		err := BridgeProtocolFault(frame.Error.Code, frame.Error.Message)
		b.failGeneration(gen, err)
		_ = b.killAndReapGeneration(gen, identity)
		return BridgeInfo{}, err
	}
	var init protocol.InitializeResult
	if err := json.Unmarshal(frame.Result, &init); err != nil {
		err = NewBridgeFault(CodeBridgeProtocol, fmt.Errorf("%w: initialize decode: %v", ErrBridgeProtocol, err), "")
		b.failGeneration(gen, err)
		_ = b.killAndReapGeneration(gen, identity)
		return BridgeInfo{}, err
	}
	if init.SchemaVersion != protocol.SchemaVersion {
		err := BridgeProtocolFault(protocol.ErrorIncompatibleVersion,
			fmt.Sprintf("schemaVersion %d != %d", init.SchemaVersion, protocol.SchemaVersion))
		b.failGeneration(gen, err)
		_ = b.killAndReapGeneration(gen, identity)
		return BridgeInfo{}, err
	}

	info := BridgeInfo{
		SchemaVersion:    init.SchemaVersion,
		ImplVersion:      init.ImplVersion,
		SDKVersion:       init.SDKVersion,
		NodeVersion:      init.NodeVersion,
		Capabilities:     append([]string(nil), init.Capabilities...),
		SandboxSupported: init.SandboxSupported,
		Generation:       gen,
	}
	b.mu.Lock()
	if b.closed.Load() || b.state == bridgeClosing || b.state == bridgeClosed {
		b.mu.Unlock()
		_ = b.killAndReapGeneration(gen, identity)
		return BridgeInfo{}, errors.New("cursorsdk: bridge closed")
	}
	if b.gen != gen || b.proc != proc {
		b.mu.Unlock()
		_ = b.killAndReapGeneration(gen, identity)
		return BridgeInfo{}, errors.New("cursorsdk: bridge generation changed during initialize")
	}
	b.info = info
	b.state = bridgeReady
	b.mu.Unlock()
	if b.diag != nil {
		b.diag.LogBridge(ctx, "ready", gen, "", DiagCorr{})
	}
	return info, nil
}

type startResult struct {
	proc Process
	err  error
}

func (b *bridgeProcess) Call(ctx context.Context, method string, params json.RawMessage) (*protocol.Frame, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.state != bridgeReady || b.proc == nil {
		b.mu.Unlock()
		return nil, errors.New("cursorsdk: bridge not ready")
	}
	proc := b.proc
	gen := b.gen
	b.mu.Unlock()
	return b.callOnProc(ctx, proc, gen, method, params)
}

// cancelRun issues run/cancel pinned to generation. When generation > 0 and the
// live bridge has advanced, returns nil without writing to the newer process.
func (b *bridgeProcess) cancelRun(ctx context.Context, runID string, generation int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	b.mu.Lock()
	if b.state != bridgeReady || b.proc == nil {
		b.mu.Unlock()
		return errors.New("cursorsdk: bridge not ready")
	}
	if generation > 0 && b.gen != generation {
		b.mu.Unlock()
		return nil
	}
	proc := b.proc
	gen := b.gen
	b.mu.Unlock()
	frame, err := b.callOnProc(ctx, proc, gen, protocol.MethodRunCancel, mustJSON(protocol.RunCancelParams{RunID: runID}))
	if err != nil {
		return err
	}
	if frame != nil && frame.Error != nil {
		return fmt.Errorf("cursorsdk: run/cancel: %s: %s", frame.Error.Code, frame.Error.Message)
	}
	return nil
}

func (b *bridgeProcess) callOnProc(ctx context.Context, proc Process, gen int64, method string, params json.RawMessage) (*protocol.Frame, error) {
	if params == nil {
		params = json.RawMessage(`{}`)
	}
	id := fmt.Sprintf("c%d", b.idSeq.Add(1))
	ch := make(chan callResult, 1)
	b.mu.Lock()
	if b.gen != gen || b.proc != proc || b.state == bridgeClosed || b.state == bridgeFailed {
		b.mu.Unlock()
		return nil, errors.New("cursorsdk: bridge generation invalid")
	}
	if b.state == bridgeClosing && method != protocol.MethodBridgeShutdown {
		b.mu.Unlock()
		return nil, errors.New("cursorsdk: bridge closing")
	}
	b.pending[id] = &pendingCall{ch: ch, method: method}
	b.mu.Unlock()

	frame := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeRequest,
		ID:            id,
		Method:        method,
		Params:        params,
	}
	if err := b.writeFrame(proc, gen, frame); err != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, ctx.Err()
	case res := <-ch:
		return res.frame, res.err
	}
}

func (b *bridgeProcess) writeFrame(proc Process, gen int64, frame *protocol.Frame) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.mu.Lock()
	if b.gen != gen || b.proc != proc {
		b.mu.Unlock()
		return errors.New("cursorsdk: bridge generation invalid")
	}
	stdin := proc.Stdin()
	b.mu.Unlock()
	return protocol.WriteFrame(stdin, frame)
}

func (b *bridgeProcess) SubscribeRun(runID string) (<-chan *protocol.Frame, func(), func() error) {
	runID = strings.TrimSpace(runID)
	noopErr := func() error { return nil }
	var stale *runSub
	b.mu.Lock()
	gen := b.gen
	if existing, ok := b.runs[runID]; ok {
		if existing.generation != gen {
			delete(b.runs, runID)
			stale = existing
		} else if existing.isClosed() {
			// Preserve closed same-generation terminal faults (BridgeExited /
			// protocol overflow) even with zero buffered frames so a late
			// SubscribeRun observes the fault instead of resurrecting an open
			// channel that nothing will ever close.
			if existing.buffered() > 0 || existing.TerminalErr() != nil {
				existing.markClaimed()
				b.mu.Unlock()
				return existing.ch, b.runSubCancel(runID, existing), existing.TerminalErr
			}
			delete(b.runs, runID)
		} else if existing.isClaimed() {
			b.mu.Unlock()
			return nil, func() {}, noopErr
		} else {
			existing.markClaimed()
			b.mu.Unlock()
			return existing.ch, b.runSubCancel(runID, existing), existing.TerminalErr
		}
	}
	sub := newRunSub(gen)
	sub.markClaimed()
	b.runs[runID] = sub
	b.mu.Unlock()
	if stale != nil {
		stale.close()
	}
	return sub.ch, b.runSubCancel(runID, sub), sub.TerminalErr
}

func (b *bridgeProcess) runSubCancel(runID string, sub *runSub) func() {
	return func() {
		b.mu.Lock()
		cur, ok := b.runs[runID]
		if ok && cur == sub {
			delete(b.runs, runID)
		}
		b.mu.Unlock()
		sub.close()
	}
}

func (b *bridgeProcess) armRunForSendLocked(runID string) error {
	gen := b.gen
	if cur, ok := b.runs[runID]; ok {
		if cur.generation != gen {
			delete(b.runs, runID)
			cur.close()
		} else if cur.isClosed() {
			sub := newRunSub(gen)
			sub.markSendBound()
			b.runs[runID] = sub
			return nil
		} else if cur.isSendBound() {
			return errRunIDConflict
		} else {
			cur.markSendBound()
			return nil
		}
	}
	sub := newRunSub(gen)
	sub.markSendBound()
	b.runs[runID] = sub
	return nil
}

func (b *bridgeProcess) Close() error {
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		reapTimeout := b.reapTimeout()
		if reapTimeout <= 0 {
			reapTimeout = defaultReapTimeout
		}
		flightTimeout := b.cfg.BridgeStartTimeout
		if flightTimeout <= 0 {
			flightTimeout = 30 * time.Second
		}

		b.mu.Lock()
		b.state = bridgeClosing
		fl := b.startFlight
		// Unblock any in-flight RPC (initialize handshake) so the start flight can exit.
		b.failPendingLocked(errors.New("cursorsdk: bridge closing"))
		interruptProc := b.proc
		interruptGen := b.gen
		interruptID := b.identity
		interruptDone := b.waitDone
		if interruptProc != nil && fl != nil {
			// Own the handle so Close can interrupt handshake; flight cleanup becomes a no-op.
			b.proc = nil
		}
		b.mu.Unlock()

		var errs []error
		if fl != nil && interruptProc != nil {
			if err := b.killAndReapOwned(interruptProc, interruptID, interruptDone, reapTimeout, true); err != nil {
				errs = append(errs, err)
			}
			_ = interruptGen
		}
		if fl != nil {
			select {
			case <-fl.done:
			case <-time.After(flightTimeout):
				errs = append(errs, errors.New("cursorsdk: start flight did not finish"))
			}
		}

		b.mu.Lock()
		proc := b.proc
		gen := b.gen
		identity := b.identity
		done := b.waitDone
		b.mu.Unlock()

		if proc != nil {
			shutdownErr := make(chan error, 1)
			shutdownDone := make(chan struct{})
			go func() {
				defer close(shutdownDone)
				shutdownCtx, cancel := context.WithTimeout(context.Background(), reapTimeout)
				defer cancel()
				_, err := b.callOnProc(shutdownCtx, proc, gen, protocol.MethodBridgeShutdown, json.RawMessage(`{}`))
				if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
					shutdownErr <- err
				}
			}()
			select {
			case <-shutdownDone:
			case <-time.After(reapTimeout):
			}
			if done != nil {
				select {
				case <-done:
				case <-time.After(50 * time.Millisecond):
					if err := b.killAndReapGeneration(gen, identity); err != nil {
						errs = append(errs, err)
					}
				}
			} else if err := b.killAndReapHandle(proc, nil, reapTimeout); err != nil {
				errs = append(errs, err)
			}
			select {
			case <-shutdownDone:
			case <-time.After(reapTimeout):
				errs = append(errs, errors.New("cursorsdk: shutdown write did not finish"))
			}
			select {
			case err := <-shutdownErr:
				errs = append(errs, err)
			default:
			}
		}

		b.mu.Lock()
		if b.proc != nil {
			leftover := b.proc
			leftoverDone := b.waitDone
			b.proc = nil
			b.mu.Unlock()
			if err := b.killAndReapHandle(leftover, leftoverDone, reapTimeout); err != nil {
				errs = append(errs, err)
			}
			b.mu.Lock()
		}
		b.state = bridgeClosed
		b.failPendingLocked(errors.New("cursorsdk: bridge closed"))
		b.closeRunsLocked()
		b.mu.Unlock()
		b.closeErr = errors.Join(errs...)
	})
	return b.closeErr
}

func (b *bridgeProcess) Generation() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen
}

func (b *bridgeProcess) Ready() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == bridgeReady && b.proc != nil
}

func (b *bridgeProcess) RetainedStderr() string {
	b.mu.Lock()
	raw := append([]byte(nil), b.stderrBuf...)
	key := b.cfg.APIKey
	b.mu.Unlock()
	return sanitizeBridgeDiag(raw, key)
}

func (b *bridgeProcess) readStdout(proc Process, gen int64) {
	sc := bufio.NewScanner(proc.Stdout())
	sc.Buffer(make([]byte, 64*1024), protocol.MaxFrameBytes+1)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) > protocol.MaxFrameBytes {
			b.failGeneration(gen, BridgeProtocolFault(protocol.ErrorFrameTooLarge, ""))
			return
		}
		f, err := protocol.DecodeLine(line)
		if err != nil {
			var pe *protocol.ProtocolError
			if errors.As(err, &pe) && pe.Class == protocol.ErrorFrameTooLarge {
				b.failGeneration(gen, BridgeProtocolFault(protocol.ErrorFrameTooLarge, pe.Message))
				return
			}
			if b.diag != nil {
				b.diag.LogBridge(context.Background(), "failed", gen, string(CodeBridgeProtocol), DiagCorr{})
			}
			continue
		}
		b.routeFrame(gen, f)
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			b.failGeneration(gen, BridgeProtocolFault(protocol.ErrorFrameTooLarge, ""))
			return
		}
		if b.diag != nil {
			b.diag.LogBridge(context.Background(), "failed", gen, string(CodeBridgeProtocol), DiagCorr{})
		}
	}
}

func (b *bridgeProcess) routeFrame(gen int64, f *protocol.Frame) {
	switch f.Type {
	case protocol.TypeResponse:
		b.mu.Lock()
		if b.gen != gen {
			b.mu.Unlock()
			return
		}
		p, ok := b.pending[f.ID]
		if ok {
			delete(b.pending, f.ID)
		}
		if ok && p.method == protocol.MethodAgentSend && f.Error == nil {
			var out protocol.AgentSendResult
			if json.Unmarshal(f.Result, &out) == nil {
				if runID := strings.TrimSpace(out.RunID); runID != "" {
					if err := b.armRunForSendLocked(runID); err != nil {
						b.mu.Unlock()
						select {
						case p.ch <- callResult{err: err}:
						default:
						}
						return
					}
				}
			}
		}
		b.mu.Unlock()
		if !ok {
			return
		}
		select {
		case p.ch <- callResult{frame: f}:
		default:
		}
	case protocol.TypeEvent:
		b.mu.Lock()
		if b.gen != gen {
			b.mu.Unlock()
			return
		}
		sub, ok := b.runs[f.RunID]
		if ok && sub.generation != gen {
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()
		if !ok {
			return
		}
		if err := sub.deliver(f); err != nil {
			if b.diag != nil {
				b.diag.LogBridge(context.Background(), "ready", gen, string(CodeBridgeProtocol), DiagCorr{})
			}
		}
	}
}

func (b *bridgeProcess) readStderr(proc Process, gen int64) {
	buf := make([]byte, 4096)
	for {
		n, err := proc.Stderr().Read(buf)
		if n > 0 {
			b.mu.Lock()
			if b.gen == gen {
				b.stderrBuf = append(b.stderrBuf, buf[:n]...)
				if len(b.stderrBuf) > MaxStderrRetainBytes {
					b.stderrBuf = b.stderrBuf[len(b.stderrBuf)-MaxStderrRetainBytes:]
				}
			}
			b.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (b *bridgeProcess) waitProc(proc Process, gen int64, done chan struct{}) {
	err := proc.Wait()
	b.mu.Lock()
	diag := ""
	if b.gen == gen {
		diag = sanitizeBridgeDiag(b.stderrBuf, b.cfg.APIKey)
		if diag != "" {
			diag = "stderr=" + diag
		}
	}
	exitFault := BridgeExited(err, diag)
	b.closeRunsForGenerationLocked(gen, exitFault)
	notify := false
	if b.gen == gen {
		b.waitErr = err
		if b.proc == proc {
			b.proc = nil
		}
		if b.state != bridgeClosing && b.state != bridgeClosed {
			b.state = bridgeFailed
			b.failPendingLocked(exitFault)
			notify = true
		}
	} else if b.state != bridgeClosing && b.state != bridgeClosed {
		notify = true
	}
	hook := b.onBridgeGenerationDead
	b.mu.Unlock()
	if notify && hook != nil {
		hook(gen)
	}
	if done != nil {
		select {
		case <-done:
		default:
			close(done)
		}
	}
}

func (b *bridgeProcess) failGeneration(gen int64, err error) {
	b.mu.Lock()
	b.closeRunsForGenerationLocked(gen, err)
	notify := false
	if b.gen == gen {
		if b.state != bridgeClosing && b.state != bridgeClosed {
			b.state = bridgeFailed
			notify = true
		}
		b.failPendingLocked(err)
	} else if b.state != bridgeClosing && b.state != bridgeClosed {
		notify = true
	}
	hook := b.onBridgeGenerationDead
	b.mu.Unlock()
	if notify && hook != nil {
		hook(gen)
	}
}

func (b *bridgeProcess) failPendingLocked(err error) {
	for id, p := range b.pending {
		delete(b.pending, id)
		select {
		case p.ch <- callResult{err: err}:
		default:
		}
	}
}

func (b *bridgeProcess) closeRunsLocked() {
	subs := make([]*runSub, 0, len(b.runs))
	for id, sub := range b.runs {
		delete(b.runs, id)
		subs = append(subs, sub)
	}
	for _, sub := range subs {
		sub.close()
	}
}

func (b *bridgeProcess) closeRunsForGenerationLocked(gen int64, terminal error) {
	subs := make([]*runSub, 0, len(b.runs))
	for id, sub := range b.runs {
		if sub.generation != gen {
			continue
		}
		// When stamping a terminal process/protocol fault, keep the closed
		// runSub in the map so SubscribeRun can hand it to the first claimant
		// (including zero-buffer BridgeExited after agent/send). Plain close
		// without a fault still removes the entry.
		if terminal == nil {
			delete(b.runs, id)
		}
		subs = append(subs, sub)
	}
	for _, sub := range subs {
		if terminal != nil {
			sub.closeWithErr(terminal)
		} else {
			sub.close()
		}
	}
}

func (b *bridgeProcess) currentIdentity() processIdentity {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.identity
}

func (b *bridgeProcess) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

func (b *bridgeProcess) hasRunSub(runID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.runs[strings.TrimSpace(runID)]
	return ok
}

func (b *bridgeProcess) runSubClosed(runID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub, ok := b.runs[strings.TrimSpace(runID)]
	return ok && sub.isClosed()
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
