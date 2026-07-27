package product

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	defaultRunStreamPending = 32
	maxRunStreamPending     = 256
)

var (
	errRunStreamClosed = errors.New("cursorsdk: run stream closed")
	errBridgeRunEnded  = BridgeExited(errors.New("bridge run ended before terminal"), "")
	errCancelTimeout   = errors.New("cursor_sdk_cancel_timeout")
)

type LeaseOwner interface {
	ReleaseReady(lease *AgentLease) error
	InvalidateLease(lease *AgentLease, cause InvalidationCause)
}

// GenerationKiller terminates one bridge process generation (identity-protected).
// Production Open wires the bridge process; tests may inject OnCancelTimeout instead.
type GenerationKiller interface {
	KillGeneration(ctx context.Context, gen int64) error
}

type RunStreamOpts struct {
	CancelTimeout    time.Duration
	MaxPending       int
	OnCancelTimeout  func(ctx context.Context) error
	GenerationKiller GenerationKiller
	APIKey           string
	Diag             *Diag
	Corr             DiagCorr
}

// runDiagOutcome is the once-state for a run's terminal diagnostic. Locked
// methods only record into it; slog emission happens after unlock via flushPending.
type runDiagOutcome struct {
	set        bool
	outcome    string
	phase      string
	code       FailureCode
	cancelMode string
}

func (o *runDiagOutcome) record(outcome, phase, cancelMode string, code FailureCode) bool {
	if o.set {
		return false
	}
	o.set = true
	o.outcome = outcome
	o.phase = phase
	o.code = code
	o.cancelMode = cancelMode
	return true
}

type streamPending struct {
	leaseInv bool
	invCause InvalidationCause
	emitRun  bool
}

type RunStream struct {
	mu sync.Mutex

	bridge  RunBridge
	owner   LeaseOwner
	lease   *AgentLease
	runID   string
	opts    RunStreamOpts
	maxPend int

	frames  <-chan *protocol.Frame
	unsub   func()
	termErr func() error

	pending         PendingEventQueue
	responseStarted bool
	messageStarted  bool
	after           bool
	expectSeq       int64
	recvErr         error
	closed          bool
	cancelOnce      sync.Once
	cancelResult    lipapi.CancelResult
	finalized       int
	committed       bool
	runOutcome      runDiagOutcome
	pendingWork     streamPending

	ctx    context.Context
	cancel context.CancelFunc
}

func NewRunStream(parent context.Context, bridge RunBridge, lease *AgentLease, owner LeaseOwner, opts RunStreamOpts) *RunStream {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	runID := ""
	if lease != nil {
		runID = lease.RunID
	}
	maxPend := opts.MaxPending
	if maxPend <= 0 {
		maxPend = defaultRunStreamPending
	}
	if maxPend > maxRunStreamPending {
		maxPend = maxRunStreamPending
	}
	s := &RunStream{
		bridge:    bridge,
		owner:     owner,
		lease:     lease,
		runID:     runID,
		opts:      opts,
		maxPend:   maxPend,
		pending:   NewPendingEventQueue(maxPend),
		expectSeq: 1,
		ctx:       ctx,
		cancel:    cancel,
	}
	if bridge != nil && runID != "" {
		ch, unsub, termErr := bridge.SubscribeRun(runID)
		s.frames = ch
		s.unsub = unsub
		s.termErr = termErr
	}
	return s
}

func (s *RunStream) pendingCap() int { return s.maxPend }

func (s *RunStream) OutputCommitted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committed
}

func (s *RunStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		_ = s.Cancel(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone})
		_ = s.Close()
		return lipapi.Event{}, err
	}
	for {
		s.mu.Lock()
		if ev, ok := s.pending.PopFront(); ok {
			if isClientVisibleEvent(ev) {
				s.committed = true
			}
			s.mu.Unlock()
			return ev, nil
		}
		if s.recvErr != nil {
			err := s.recvErr
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		if s.after {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		if s.closed {
			s.mu.Unlock()
			return lipapi.Event{}, errRunStreamClosed
		}
		frames := s.frames
		streamDone := s.ctx.Done()
		s.mu.Unlock()

		if frames == nil {
			s.failBridgeEnded()
			continue
		}

		select {
		case <-ctx.Done():
			_ = s.Cancel(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone})
			_ = s.Close()
			return lipapi.Event{}, ctx.Err()
		case <-streamDone:
			s.failClosedCancel()
			continue
		case f, ok := <-frames:
			if !ok {
				s.mu.Lock()
				if s.after {
					s.mu.Unlock()
					return lipapi.Event{}, io.EOF
				}
				s.mu.Unlock()
				if s.termErr != nil {
					if err := s.termErr(); err != nil {
						s.failRunTerminal(err)
						continue
					}
				}
				s.failBridgeEnded()
				continue
			}
			s.ingestFrame(f)
		}
	}
}

func isClientVisibleEvent(ev lipapi.Event) bool {
	switch ev.Kind {
	case lipapi.EventResponseStarted, lipapi.EventMessageStarted,
		lipapi.EventTextDelta, lipapi.EventReasoningDelta,
		lipapi.EventUsageDelta, lipapi.EventWarning,
		lipapi.EventResponseFinished, lipapi.EventError:
		return true
	default:
		return false
	}
}

func (s *RunStream) failBridgeEnded() {
	s.failRunTerminal(errBridgeRunEnded)
}

func (s *RunStream) failRunTerminal(err error) {
	s.mu.Lock()
	committed := s.committed
	apiKey := s.opts.APIKey
	mapped := ClassifyAndMap(err, committed, apiKey)
	s.queueFailLocked(mapped, InvalidateBridge)
	s.mu.Unlock()
	s.flushPending()
}

func (s *RunStream) failClosedCancel() {
	s.mu.Lock()
	s.queueFailLocked(errRunStreamClosed, InvalidateCancel)
	s.mu.Unlock()
	s.flushPending()
}

func (s *RunStream) ingestFrame(f *protocol.Frame) {
	s.mu.Lock()
	if s.recvErr != nil || s.after {
		s.mu.Unlock()
		return
	}
	res, next := mapBridgeEvent(f, s.runID, s.expectSeq, s.opts.APIKey)
	if res.err != nil {
		s.queueFailLocked(ClassifyAndMap(res.err, s.committed, s.opts.APIKey), InvalidateBridge)
		s.mu.Unlock()
		s.flushPending()
		return
	}
	s.expectSeq = next

	if res.terminal {
		res = s.finishTerminalLocked(res)
	}

	if err := s.enqueueMappedLocked(res); err != nil {
		s.pending = NewPendingEventQueue(s.maxPend)
		s.responseStarted = false
		s.messageStarted = false
		s.queueFailLocked(ClassifyAndMap(err, s.committed, s.opts.APIKey), InvalidateBridge)
		s.mu.Unlock()
		s.flushPending()
		return
	}
	s.mu.Unlock()
	s.flushPending()
}

func (s *RunStream) finishTerminalLocked(res mapResult) mapResult {
	s.after = true
	unsub := s.unsub
	s.unsub = nil
	s.frames = nil
	wantSuccess := res.success
	bridgeError := len(res.events) > 0 && res.events[0].Kind == lipapi.EventError

	s.mu.Unlock()
	if unsub != nil {
		unsub()
	}
	s.mu.Lock()

	if wantSuccess {
		if s.finalized != 0 {
			return mapResult{
				events: []lipapi.Event{{
					Kind:         lipapi.EventError,
					ErrorCode:    "cursor_sdk_canceled",
					ErrorMessage: "run canceled before terminal commit",
				}},
				terminal: true,
				success:  false,
			}
		}
		owner, lease := s.owner, s.lease
		apiKey := s.opts.APIKey
		committed := s.committed
		s.mu.Unlock()
		var releaseErr error
		if owner != nil {
			releaseErr = owner.ReleaseReady(lease)
		}
		s.mu.Lock()
		if s.finalized != 0 {
			return mapResult{
				events: []lipapi.Event{{
					Kind:         lipapi.EventError,
					ErrorCode:    "cursor_sdk_canceled",
					ErrorMessage: "run canceled before terminal commit",
				}},
				terminal: true,
				success:  false,
			}
		}
		if releaseErr != nil {
			s.scheduleInvalidateLocked(InvalidateUncommitted)
			cf := ClassifyFailure(releaseErr, committed, apiKey)
			code, phase := CodeRunFailed, string(lipapi.PhasePreOutput)
			if cf != nil {
				code, phase = cf.Code, string(cf.Phase)
			}
			s.recordOutcomeLocked("error", phase, "", code)
			return mapResult{
				events: []lipapi.Event{{
					Kind:         lipapi.EventError,
					ErrorCode:    "cursor_sdk_commit_required",
					ErrorMessage: sanitizeWarningMessage(releaseErr.Error(), apiKey),
				}},
				terminal: true,
				success:  false,
			}
		}
		s.finalized = 1
		s.recordOutcomeLocked("success", "", "", "")
		return res
	}
	if bridgeError {
		s.scheduleInvalidateLocked(InvalidateRunError)
		// Terminal KindError is client-visible (EventError), so classify as post-output.
		cf := classifyBridgeErrorEvent(res.events[0], true, s.opts.APIKey)
		code, phase := CodeRunFailed, string(lipapi.PhasePostOutput)
		if cf != nil {
			code, phase = cf.Code, string(cf.Phase)
		}
		s.recordOutcomeLocked("error", phase, "", code)
	} else if !s.runOutcome.set {
		s.scheduleInvalidateLocked(InvalidateCancel)
		s.recordOutcomeLocked("cancel", string(lipapi.PhasePostOutput), string(lipapi.CancelModeProvider), "")
	} else {
		s.scheduleInvalidateLocked(InvalidateCancel)
	}
	return res
}

func classifyBridgeErrorEvent(ev lipapi.Event, committed bool, apiKey string) *ClassifiedFailure {
	code := FailureCode(ev.ErrorCode)
	switch code {
	case CodeConfigInvalid, CodeKeyMissing, CodeAuthFailed, CodeBridgeMissing, CodeNodeMissing,
		CodeBridgeStartFailed, CodeBridgeIncompatible, CodeBridgeProtocol, CodeBridgeExited,
		CodeModelUnknown, CodeInventoryUnavailable, CodeCapabilityUnsupported, CodeAgentBusy,
		CodeAgentLimit, CodeAgentCreateFailed, CodeRunFailed, CodeCancelTimeout, CodeShutdownFailed:
		return ClassifyFailure(NewBridgeFault(code, errors.New(ev.ErrorMessage), ""), committed, apiKey)
	default:
		return ClassifyFailure(fmt.Errorf("%s: %s", ev.ErrorCode, ev.ErrorMessage), committed, apiKey)
	}
}

func (s *RunStream) enqueueMappedLocked(res mapResult) error {
	for _, ev := range res.events {
		switch ev.Kind {
		case lipapi.EventTextDelta, lipapi.EventReasoningDelta:
			if err := s.ensureResponseStartedLocked(); err != nil {
				return err
			}
			if err := s.ensureMessageStartedLocked(); err != nil {
				return err
			}
		case lipapi.EventError, lipapi.EventResponseFinished, lipapi.EventWarning, lipapi.EventUsageDelta:
			if err := s.ensureResponseStartedLocked(); err != nil {
				return err
			}
		}
		if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
			return err
		}
		if err := s.pending.Push(ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *RunStream) ensureResponseStartedLocked() error {
	if s.responseStarted {
		return nil
	}
	ev := lipapi.Event{Kind: lipapi.EventResponseStarted}
	if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
		return err
	}
	if err := s.pending.Push(ev); err != nil {
		return err
	}
	s.responseStarted = true
	return nil
}

func (s *RunStream) ensureMessageStartedLocked() error {
	if s.messageStarted {
		return nil
	}
	ev := lipapi.Event{Kind: lipapi.EventMessageStarted}
	if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
		return err
	}
	if err := s.pending.Push(ev); err != nil {
		return err
	}
	s.messageStarted = true
	return nil
}

func (s *RunStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	unsub := s.unsub
	s.unsub = nil
	s.frames = nil
	cancelFn := s.cancel
	s.mu.Unlock()

	// Best-effort provider cancel (bounded); cancelOnce makes this idempotent with Cancel().
	_ = s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelClientGone})

	if cancelFn != nil {
		cancelFn()
	}
	if unsub != nil {
		unsub()
	}
	s.flushPending()
	return nil
}

// escalateCancelTimeout kills the lease's bridge generation after CancelRun times out.
// OnCancelTimeout is a test/override hook; production Open wires GenerationKiller only.
func (s *RunStream) escalateCancelTimeout(ctx context.Context) {
	if s.opts.OnCancelTimeout != nil {
		_ = s.opts.OnCancelTimeout(ctx)
		return
	}
	if s.opts.GenerationKiller == nil || s.lease == nil {
		return
	}
	gen := s.lease.ProcessGeneration()
	if gen <= 0 {
		return
	}
	// Reap uses bridge-owned bounds; do not abort on a cancelled parent ctx.
	_ = s.opts.GenerationKiller.KillGeneration(context.WithoutCancel(ctx), gen)
}

// generationBoundCanceller cancels a run only while the given bridge generation
// is still live. Implemented by *bridgeAgentClient; optional for test fakes.
type generationBoundCanceller interface {
	CancelRunForGeneration(ctx context.Context, runID string, generation int64) error
}

func (s *RunStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	_ = cause
	s.cancelOnce.Do(func() {
		timeout := s.opts.CancelTimeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		if ctx == nil {
			ctx = context.Background()
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var err error
		if s.bridge != nil && s.runID != "" {
			boundGen := int64(0)
			if s.lease != nil {
				boundGen = s.lease.ProcessGeneration()
			}
			if gb, ok := s.bridge.(generationBoundCanceller); ok && boundGen > 0 {
				err = gb.CancelRunForGeneration(cctx, s.runID, boundGen)
			} else {
				err = s.bridge.CancelRun(cctx, s.runID)
			}
		}
		if err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(cctx.Err(), context.DeadlineExceeded)) {
			s.escalateCancelTimeout(ctx)
			s.cancelResult = lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: fmt.Errorf("%w: %v", errCancelTimeout, err)}
		} else if err != nil {
			s.cancelResult = lipapi.CancelResult{Mode: lipapi.CancelModeProvider, Err: err}
		} else {
			s.cancelResult = lipapi.CancelResult{Mode: lipapi.CancelModeProvider}
		}
		mode := string(s.cancelResult.Mode)
		code := FailureCode("")
		if s.cancelResult.Err != nil && errors.Is(s.cancelResult.Err, errCancelTimeout) {
			code = CodeCancelTimeout
		}
		s.mu.Lock()
		s.recordOutcomeLocked("cancel", "", mode, code)
		s.scheduleInvalidateLocked(InvalidateCancel)
		s.mu.Unlock()
		s.flushPending()
	})
	return s.cancelResult
}

func (s *RunStream) queueFailLocked(err error, cause InvalidationCause) {
	if s.recvErr != nil {
		return
	}
	s.recvErr = err
	s.scheduleInvalidateLocked(cause)
	if cause == InvalidateCancel && errors.Is(err, errRunStreamClosed) {
		s.recordOutcomeLocked("cancel", "", "", "")
		return
	}
	cf := ClassifyFailure(err, s.committed, s.opts.APIKey)
	code, phase := CodeRunFailed, string(lipapi.PhasePreOutput)
	if cf != nil {
		code, phase = cf.Code, string(cf.Phase)
	}
	s.recordOutcomeLocked("error", phase, "", code)
}

func (s *RunStream) recordOutcomeLocked(outcome, phase, cancelMode string, code FailureCode) {
	if !s.runOutcome.record(outcome, phase, cancelMode, code) {
		return
	}
	s.pendingWork.emitRun = true
}

func (s *RunStream) scheduleInvalidateLocked(cause InvalidationCause) {
	if s.finalized != 0 || s.owner == nil {
		return
	}
	s.finalized = 2
	s.pendingWork.leaseInv = true
	s.pendingWork.invCause = cause
}

func (s *RunStream) flushPending() {
	s.mu.Lock()
	p := s.pendingWork
	s.pendingWork = streamPending{}
	run := s.runOutcome
	owner, lease := s.owner, s.lease
	diag, corr, ctx := s.opts.Diag, s.opts.Corr, s.ctx
	s.mu.Unlock()

	if p.leaseInv && owner != nil {
		owner.InvalidateLease(lease, p.invCause)
	}
	if p.emitRun && run.set && diag != nil {
		diag.LogRun(ctx, run.outcome, run.phase, run.code, run.cancelMode, corr)
	}
}
