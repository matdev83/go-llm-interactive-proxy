package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

type interleavedPhase int

const (
	interleavedPhaseUnknown interleavedPhase = iota
	interleavedPhaseThinker
	interleavedPhaseExecutor
)

// interleavedContinuationStream sequences thinker capture and executor continuation
// within one logical A-leg. Hidden mode drains thinker output; visible mode surfaces
// sanitized reasoning deltas before executor output.
//
// Recv is single-consumer: callers must not invoke Recv concurrently on the same stream.
type interleavedContinuationStream struct {
	thinker  *retryRecvStream
	executor *retryRecvStream
	phase    interleavedPhase

	recorder *interleavedthinking.Recorder
	state    interleavedstate.State

	surfaceVisible   bool
	visibleCommitted bool
	responseStarted  bool
	pending          []lipapi.Event

	mu                 sync.Mutex
	finished           bool
	memoPersisted      bool
	transitionInFlight bool
	cancelPending      bool
	closePending       bool
}

var (
	_                          lipapi.EventStream        = (*interleavedContinuationStream)(nil)
	_                          lipapi.ManagedEventStream = (*interleavedContinuationStream)(nil)
	errUnknownInterleavedPhase                           = errors.New("runtime: unknown interleaved phase")
)

type hiddenInterleavedStream = interleavedContinuationStream

func newHiddenInterleavedStream(thinker *retryRecvStream, recorder *interleavedthinking.Recorder, state interleavedstate.State) *hiddenInterleavedStream {
	if thinker != nil {
		thinker.holdALegEnd = true
	}
	return &interleavedContinuationStream{
		thinker:  thinker,
		phase:    interleavedPhaseThinker,
		recorder: recorder,
		state:    state,
	}
}

func newVisibleInterleavedStream(thinker *retryRecvStream, recorder *interleavedthinking.Recorder, state interleavedstate.State) *interleavedContinuationStream {
	s := newHiddenInterleavedStream(thinker, recorder, state)
	s.surfaceVisible = true
	return s
}

func (s *interleavedContinuationStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s == nil {
		return lipapi.Event{}, errNilRetryRecvStream
	}
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return lipapi.Event{}, io.EOF
	}
	phase := s.phase
	s.mu.Unlock()

	switch phase {
	case interleavedPhaseThinker:
		return s.recvThinker(ctx)
	case interleavedPhaseExecutor:
		return s.recvExecutor(ctx)
	default:
		return lipapi.Event{}, errUnknownInterleavedPhase
	}
}

func (s *interleavedContinuationStream) popPending() (lipapi.Event, bool) {
	if len(s.pending) == 0 {
		return lipapi.Event{}, false
	}
	ev := s.pending[0]
	s.pending[0] = lipapi.Event{} // zero out for GC before advancing
	s.pending = s.pending[1:]
	if len(s.pending) == 0 {
		s.pending = nil
	}
	if ev.Kind == lipapi.EventReasoningDelta {
		s.recordVisibleOutput(ev)
	}
	return ev, true
}

func (s *interleavedContinuationStream) recvThinker(ctx context.Context) (lipapi.Event, error) {
	for {
		if ev, ok := s.popPending(); ok {
			return ev, nil
		}
		ev, err := s.thinker.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) && s.thinker.isFinished() {
				// Only response_finished completion sets the request terminal's
				// accounting-finalized claim.
				// Truncated EOF / cancel / error terminals must not open an executor
				// continuation (that would race a second request/call closure).
				if s.thinker.terminal == nil || !s.thinker.terminal.accountingFinalized() {
					s.finishWithCleanup(ctx)
					return lipapi.Event{}, io.EOF
				}
				if s.surfaceVisible {
					for _, visible := range s.recorder.FlushVisibleSanitizer() {
						s.enqueueVisibleReasoning(visible)
					}
					if out, ok := s.popPending(); ok {
						return out, nil
					}
				}
				return s.beginExecutorContinuation(ctx)
			}
			if _, persistErr := s.captureAndPersistThinkerMemo(ctx, true); persistErr != nil {
				s.finishWithCleanup(ctx)
				return lipapi.Event{}, persistErr
			}
			s.finishWithCleanup(ctx)
			return lipapi.Event{}, err
		}
		if ev.Kind == lipapi.EventError {
			if _, persistErr := s.captureAndPersistThinkerMemo(ctx, true); persistErr != nil {
				s.finishWithCleanup(ctx)
				return lipapi.Event{}, persistErr
			}
			s.finishWithCleanup(ctx)
			return ev, nil
		}
		for _, visible := range s.recorder.Observe(ev) {
			if !s.surfaceVisible {
				continue
			}
			if visible.Kind != lipapi.EventReasoningDelta {
				continue
			}
			s.enqueueVisibleReasoning(visible)
			if out, ok := s.popPending(); ok {
				return out, nil
			}
		}
	}
}

func (s *interleavedContinuationStream) enqueueVisibleReasoning(ev lipapi.Event) {
	if !s.responseStarted {
		s.pending = append(
			s.pending,
			lipapi.Event{Kind: lipapi.EventResponseStarted},
			lipapi.Event{Kind: lipapi.EventMessageStarted},
		)
		s.responseStarted = true
	}
	s.pending = append(s.pending, ev)
}

func (s *interleavedContinuationStream) recordVisibleOutput(ev lipapi.Event) {
	if s.thinker == nil {
		return
	}
	s.thinker.markOutputCommitted(ev)
	s.thinker.attempt.require().accounting.observeClientEvent(s.thinker.now(), ev)
	if s.thinker.recoverPolicy != nil {
		s.thinker.recoverPolicy.ObserveClientEvent(ev, s.thinker.now())
	}
	s.visibleCommitted = true
}

func (s *interleavedContinuationStream) captureAndPersistThinkerMemo(ctx context.Context, interrupted bool) (interleavedstate.State, error) {
	s.mu.Lock()
	if s.memoPersisted {
		state := s.state
		s.mu.Unlock()
		return state, nil
	}
	if s.phase != interleavedPhaseThinker || s.recorder == nil || s.thinker == nil || s.thinker.executor == nil {
		s.mu.Unlock()
		return s.state, nil
	}
	s.memoPersisted = true
	state := s.state
	s.mu.Unlock()

	persistCtx := ctx
	if interrupted {
		var cleanupCancel context.CancelFunc
		persistCtx, cleanupCancel = detachedCleanupContext(ctx, cancelLosersTimeout)
		defer cleanupCancel()
	}

	memo := s.recorder.Finish(interrupted)
	if strings.TrimSpace(memo.Memo) == "" {
		// Differentiated skip reasons mirror the Python recorder: an interrupted
		// stream wins over empty content; otherwise content that was observed but
		// normalized to nothing is empty_memo, and a stream that never produced
		// content is no_extractable_memo.
		reason := "empty_memo"
		if interrupted {
			reason = "stream_interrupted"
		} else if !s.recorder.HadContent() {
			reason = "no_extractable_memo"
		}
		s.mu.Lock()
		s.memoPersisted = false
		s.mu.Unlock()
		if s.thinker != nil && s.thinker.executor != nil {
			s.thinker.executor.logInterleavedMemoStoreSkipped(persistCtx, s.thinker.facts.traceID, reason, interrupted)
		}
		return s.state, nil
	}
	memo.VisibleToClient = s.visibleCommitted
	s.thinker.executor.logInterleavedMemoCaptured(persistCtx, s.thinker.facts.traceID, memo)
	if !interrupted {
		s.thinker.executor.logInterleavedPhaseTransition(persistCtx, s.thinker.facts.traceID)
	}
	state, err := s.thinker.executor.persistCapturedMemo(persistCtx, s.thinker.facts.aLegID, state, memo)
	if err != nil {
		s.mu.Lock()
		s.memoPersisted = false
		s.mu.Unlock()
		return s.state, err
	}
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
	return state, nil
}

func (s *interleavedContinuationStream) beginExecutorContinuation(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	s.transitionInFlight = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.transitionInFlight = false
		s.mu.Unlock()
	}()

	state, err := s.captureAndPersistThinkerMemo(ctx, false)
	if err != nil {
		s.finishWithCleanup(ctx)
		return lipapi.Event{}, err
	}
	execStream, err := s.thinker.executor.openInterleavedExecutorContinuation(ctx, s.thinker, state)
	if err != nil {
		s.finishWithCleanup(ctx)
		return lipapi.Event{}, err
	}
	if abortErr := s.handoffAborted(ctx); abortErr != nil {
		s.abortExecutorHandoff(ctx, execStream, abortErr)
		return lipapi.Event{}, abortErr
	}
	s.closeThinkerInner(ctx)
	if s.visibleCommitted {
		execStream.markCommitted()
		if execStream.ttft != nil {
			execStream.ttft.markCommitted()
		}
	}
	s.mu.Lock()
	if s.cancelPending || s.closePending || s.finished {
		var abortErr error
		switch {
		case s.cancelPending:
			abortErr = context.Canceled
		case s.closePending:
			abortErr = errors.New("client closed")
		default:
			abortErr = io.EOF
		}
		// Clear transitionInFlight before unlock so concurrent Cancel/Close take the
		// post-assignment path if abort races with a late observer; abort owns finished.
		s.transitionInFlight = false
		s.mu.Unlock()
		s.abortExecutorHandoff(ctx, execStream, abortErr)
		return lipapi.Event{}, abortErr
	}
	s.executor = execStream
	s.phase = interleavedPhaseExecutor
	s.state = state
	// Clear atomically with executor/phase assignment before first executor Recv so
	// Cancel/Close during that Recv cancel the opened continuation (not pending-only).
	s.transitionInFlight = false
	s.mu.Unlock()
	return s.recvExecutor(ctx)
}

func (s *interleavedContinuationStream) handoffAborted(ctx context.Context) error {
	s.mu.Lock()
	finished := s.finished
	cancelPending := s.cancelPending
	closePending := s.closePending
	s.mu.Unlock()
	// Pending cancel/close from transition Cancel/Close must win over a generic
	// finished bit so abortExecutorHandoff picks CommandCancel vs CommandClose.
	if cancelPending {
		return context.Canceled
	}
	if closePending {
		return errors.New("client closed")
	}
	if finished {
		return io.EOF
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.thinker != nil && s.thinker.aScope != nil {
		if err := s.thinker.aScope.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *interleavedContinuationStream) closeThinkerInner(ctx context.Context) {
	if s.thinker == nil {
		return
	}
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()
	thinkerAttempt := s.thinker.attempt.snapshot()
	if thinkerAttempt != nil {
		if inner := thinkerAttempt.takeInner(); inner != nil {
			s.thinker.cancelAndCloseInner(cleanupCtx, inner, leglifecycle.CancelCause{
				Kind:   lipapi.CancelContextDone,
				Detail: "interleaved thinker handoff",
			})
		}
	}
	if thinkerAttempt != nil && s.thinker.aScope != nil && thinkerAttempt.bleg.BLegID != "" {
		s.thinker.aScope.ReleaseBLeg(thinkerAttempt.bleg.BLegID)
	}
}

func (s *interleavedContinuationStream) closeActiveInner(ctx context.Context) {
	s.mu.Lock()
	phase := s.phase
	thinker := s.thinker
	executor := s.executor
	s.mu.Unlock()

	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()

	if phase == interleavedPhaseExecutor && executor != nil {
		if executorAttempt := executor.attempt.snapshot(); executorAttempt != nil {
			if inner := executorAttempt.takeInner(); inner != nil {
				executor.cancelAndCloseInner(cleanupCtx, inner, leglifecycle.CancelCause{
					Kind:   lipapi.CancelContextDone,
					Detail: "interleaved executor finished",
				})
			}
		}
		return
	}
	if thinker != nil {
		thinkerAttempt := thinker.attempt.snapshot()
		if thinkerAttempt != nil {
			if inner := thinkerAttempt.takeInner(); inner != nil {
				thinker.cancelAndCloseInner(cleanupCtx, inner, leglifecycle.CancelCause{
					Kind:   lipapi.CancelContextDone,
					Detail: "interleaved thinker finished",
				})
			}
		}
		if thinkerAttempt != nil && thinker.aScope != nil && thinkerAttempt.bleg.BLegID != "" {
			thinker.aScope.ReleaseBLeg(thinkerAttempt.bleg.BLegID)
		}
	}
}

func (s *interleavedContinuationStream) finalizeThinkerAuthority(ctx context.Context, kind authorityapp.ReleaseKind) {
	if s == nil || s.thinker == nil {
		return
	}
	attempt := s.thinker.attempt.snapshot()
	if attempt == nil || attempt.authority.Settled() {
		return
	}
	attempt.authority.finalizeIncurredOrRelease(ctx, kind, s.thinker.operatorUsageForFinalize())
}

func (s *interleavedContinuationStream) finishWithCleanup(ctx context.Context) {
	s.closeActiveInner(ctx)
	if s.thinker != nil {
		s.mu.Lock()
		thinkerPhase := s.phase == interleavedPhaseThinker
		closePending := s.closePending
		cancelPending := s.cancelPending
		s.mu.Unlock()
		if thinkerPhase {
			cmd := sdkterminal.CommandPartialError
			if closePending {
				cmd = sdkterminal.CommandClose
			} else if cancelPending {
				cmd = sdkterminal.CommandCancel
			} else if s.thinker.aScope != nil {
				if err := s.thinker.aScope.Err(); err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						cmd = sdkterminal.CommandTimeout
					} else {
						cmd = sdkterminal.CommandCancel
					}
				}
			}
			_ = s.thinker.runStreamTerminal(ctx, cmd, func(cctx context.Context) error {
				s.finalizeThinkerAuthority(cctx, authorityapp.ReleaseKindLosing)
				return nil
			})
			// If another owner already won the request terminal (e.g. truncated
			// CommandEOF), effects above are skipped — still release/settle once.
			s.finalizeThinkerAuthority(ctx, authorityapp.ReleaseKindLosing)
		}
	}
	s.markFinished()
}

func (s *interleavedContinuationStream) abortExecutorHandoff(ctx context.Context, exec *retryRecvStream, abortErr error) {
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()
	if exec != nil {
		execAttempt := exec.attempt.require()
		if inner := execAttempt.takeInner(); inner != nil {
			exec.cancelAndCloseInner(cleanupCtx, inner, leglifecycle.CancelCause{
				Kind:   lipapi.CancelContextDone,
				Detail: "interleaved executor handoff aborted",
			})
		}
		// Finalize the executor-leg usage-authority reservation. On this abort path
		// s.executor is still nil (it is only assigned on the success branch of
		// beginExecutorContinuation), so the normal closeActiveInner/finishWithCleanup
		// executor cleanup never runs for this exec stream and the freshly admitted
		// exec.authority would otherwise leak. Incurred work settles; never-opened
		// admissions release. Mirrors sibling L1/L8 sites with ReleaseKindSwallowed
		// posture since the aborted attempt produced no client-facing output.
		execAttempt.authority.finalizeIncurredOrRelease(cleanupCtx, authorityapp.ReleaseKindSwallowed, exec.operatorUsageForFinalize())

		// Record the continuation B-leg terminal row:
		started := execAttempt.accounting.requestStartedAt
		if started.IsZero() {
			started = exec.now()
		}
		exec.executor.appendIndependentTerminalLeg(cleanupCtx, exec.facts.billingCallState, exec.facts.aLegID, execAttempt.bleg, execAttempt.cand.Primary, started, exec.now(), billing.LegOutcomeCanceled)

		exec.markFinished()
	}
	s.closeThinkerInner(ctx)

	s.mu.Lock()
	closePending := s.closePending
	s.mu.Unlock()

	// Terminalize the logical request owner:
	cmd := sdkterminal.CommandCancel
	if closePending {
		cmd = sdkterminal.CommandClose
	} else if errors.Is(abortErr, context.DeadlineExceeded) || (s.thinker != nil && s.thinker.aScope != nil && errors.Is(s.thinker.aScope.Err(), context.DeadlineExceeded)) {
		cmd = sdkterminal.CommandTimeout
	}
	if s.thinker != nil {
		_ = s.thinker.runStreamTerminal(cleanupCtx, cmd, func(cctx context.Context) error {
			s.finalizeThinkerAuthority(cctx, authorityapp.ReleaseKindLosing)
			return nil
		})
		s.finalizeThinkerAuthority(cleanupCtx, authorityapp.ReleaseKindLosing)
	}

	s.markFinished()
}

func (s *interleavedContinuationStream) recvExecutor(ctx context.Context) (lipapi.Event, error) {
	if ev, ok := s.popPending(); ok {
		return ev, nil
	}
	if s.executor == nil {
		s.finishWithCleanup(ctx)
		return lipapi.Event{}, io.EOF
	}
	for {
		ev, err := s.executor.Recv(ctx)
		if err != nil {
			s.finishWithCleanup(ctx)
			return ev, err
		}
		if s.responseStarted && (ev.Kind == lipapi.EventResponseStarted || ev.Kind == lipapi.EventMessageStarted) {
			continue
		}
		return ev, nil
	}
}

func (s *interleavedContinuationStream) persistInterruptedThinkerMemo(ctx context.Context) {
	if _, persistErr := s.captureAndPersistThinkerMemo(ctx, true); persistErr != nil {
		if s.thinker != nil && s.thinker.executor != nil {
			s.thinker.executor.logInterleavedMemoPersistFailed(ctx, s.thinker.facts.traceID, persistErr)
		}
	}
}

func (s *interleavedContinuationStream) markFinished() {
	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()
	if s.thinker != nil && s.thinker.aScope != nil {
		s.thinker.endOnce.Do(func() {
			s.thinker.aScope.End()
		})
	}
}

func (s *interleavedContinuationStream) activeRecvLocked() *retryRecvStream {
	if s == nil {
		return nil
	}
	if s.phase == interleavedPhaseExecutor && s.executor != nil {
		return s.executor
	}
	return s.thinker
}

func (s *interleavedContinuationStream) activeRecv() *retryRecvStream {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRecvLocked()
}

func (s *interleavedContinuationStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if s == nil {
		return lipapi.CancelResult{}
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return lipapi.CancelResult{}
	}
	if s.transitionInFlight {
		// Pending flags are owned by abort/cleanup (do not set finished here).
		s.cancelPending = true
		thinker := s.thinker
		executor := s.executor
		s.mu.Unlock()
		// Cancel an already-opened continuation when assigned; otherwise cancel the
		// shared A-leg scope so handoffAborted/open sees cancellation.
		if executor != nil && executor.aScope != nil {
			_ = executor.aScope.Cancel(ctx, cause)
		} else if thinker != nil && thinker.aScope != nil {
			_ = thinker.aScope.Cancel(ctx, cause)
		}
		return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
	}
	active := s.activeRecvLocked()
	thinkerPhase := s.phase == interleavedPhaseThinker
	s.mu.Unlock()
	if active == nil {
		return lipapi.CancelResult{}
	}

	var res lipapi.CancelResult
	if !active.isFinished() {
		if active.aScope != nil {
			_ = active.aScope.Cancel(ctx, cause)
			res = lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
		} else if activeAttempt := active.attempt.snapshot(); activeAttempt != nil {
			if inner := activeAttempt.loadInner(); inner != nil {
				res = inner.Cancel(ctx, cause)
			}
		}
	}

	if thinkerPhase {
		s.persistInterruptedThinkerMemo(ctx)
		if s.thinker != nil {
			_ = s.thinker.runStreamTerminal(ctx, sdkterminal.CommandCancel, func(cctx context.Context) error {
				s.finalizeThinkerAuthority(cctx, authorityapp.ReleaseKindLosing)
				return nil
			})
			s.finalizeThinkerAuthority(ctx, authorityapp.ReleaseKindLosing)
		}
	}
	s.markFinished()
	return res
}

func (s *interleavedContinuationStream) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return nil
	}
	if s.transitionInFlight {
		// Pending flags are owned by abort/cleanup (do not set finished here).
		s.closePending = true
		thinker := s.thinker
		executor := s.executor
		s.mu.Unlock()
		parent := context.Background()
		if thinker != nil {
			if cached := thinker.cachedExecContext(); cached != nil {
				parent = context.WithoutCancel(cached)
			}
		}
		if executor != nil && executor.aScope != nil {
			_ = executor.aScope.Cancel(parent, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		} else if thinker != nil && thinker.aScope != nil {
			_ = thinker.aScope.Cancel(parent, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		}
		return nil
	}
	active := s.activeRecvLocked()
	thinkerPhase := s.phase == interleavedPhaseThinker
	s.mu.Unlock()

	var err error
	if active != nil {
		err = active.Close()
	}

	if thinkerPhase {
		// Close has no caller context; reuse the thinker's last Recv parent when one
		// exists so memo persistence keeps request-scoped values. The interrupted path
		// detaches cancellation via detachedCleanupContext, so persistence still
		// outlives request cancellation. Background only when no parent exists.
		parent := context.Background()
		if s.thinker != nil {
			if cached := s.thinker.cachedExecContext(); cached != nil {
				parent = cached
			}
		}
		s.persistInterruptedThinkerMemo(parent)
		if s.thinker != nil {
			_ = s.thinker.runStreamTerminal(parent, sdkterminal.CommandClose, func(cctx context.Context) error {
				s.finalizeThinkerAuthority(cctx, authorityapp.ReleaseKindLosing)
				return nil
			})
			s.finalizeThinkerAuthority(parent, authorityapp.ReleaseKindLosing)
		}
	}
	s.markFinished()
	return err
}
