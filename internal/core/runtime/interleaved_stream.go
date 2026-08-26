package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
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
	if thinker != nil && thinker.terminal != nil {
		// This construction-time handoff is one-way: the outer wrapper owns the
		// shared A-leg end for the combined thinker/executor turn.
		thinker.terminal.deferALegEndToOuter()
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
			if errors.Is(err, io.EOF) {
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
	s.thinker.terminal.markOutputCommittedForAttempt(ev, s.thinker.attempt.snapshot(), s.thinker.recovery)
	s.thinker.attempt.require().accounting.observeClientEvent(s.thinker.responsePipeline.nowTime(), ev)
	if s.thinker.recovery != nil && s.thinker.recovery.recoverPolicy != nil {
		s.thinker.recovery.recoverPolicy.ObserveClientEvent(ev, s.thinker.responsePipeline.nowTime())
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
	if s.phase != interleavedPhaseThinker || s.recorder == nil || s.thinker == nil || s.thinker.recovery == nil {
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
		if s.thinker != nil && s.thinker.recovery != nil {
			s.thinker.recovery.logMemoStoreSkipped(persistCtx, s.thinker.facts.traceID, reason, interrupted)
		}
		return s.state, nil
	}
	memo.VisibleToClient = s.visibleCommitted
	s.thinker.recovery.logMemoCaptured(persistCtx, s.thinker.facts.traceID, memo)
	if !interrupted {
		s.thinker.recovery.logPhaseTransition(persistCtx, s.thinker.facts.traceID)
	}
	state, err := s.thinker.recovery.persistCapturedMemo(persistCtx, s.thinker.facts.aLegID, state, memo, capturedMemoSource{
		TraceID:  s.thinker.facts.traceID,
		Ingress:  s.thinker.facts.ingressCall,
		Snapshot: s.thinker.facts.conversationSnapshot,
	})
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
	execStream, err := s.thinker.recovery.openInterleavedContinuation(ctx, s.thinker, state)
	if err != nil {
		s.finishWithCleanup(ctx)
		return lipapi.Event{}, err
	}
	if abortErr := s.handoffAborted(ctx); abortErr != nil {
		s.abortExecutorHandoff(ctx, execStream, abortErr)
		return lipapi.Event{}, abortErr
	}
	if s.visibleCommitted {
		execStream.terminal.markCommitted(execStream.attempt.snapshot())
		if execStream.recovery != nil && execStream.recovery.ttft != nil {
			execStream.recovery.ttft.markCommitted()
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
	if s.thinker != nil && s.thinker.terminal != nil && s.thinker.terminal.hasALeg() {
		if err := s.thinker.terminal.aLegErr(); err != nil {
			return err
		}
	}
	return nil
}

func (s *interleavedContinuationStream) finishWithCleanup(ctx context.Context) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	phase := s.phase
	thinker := s.thinker
	executor := s.executor
	closePending := s.closePending
	cancelPending := s.cancelPending
	s.mu.Unlock()

	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()

	if phase == interleavedPhaseThinker && thinker != nil {
		attempt := thinker.attempt.snapshot()
		if closePending {
			thinker.terminal.closeClose(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
		} else if cancelPending {
			thinker.terminal.terminalizeCancellation(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline, "canceled", false)
		} else if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			thinker.terminal.terminalizeTimeout(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
		} else if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
			thinker.terminal.terminalizeCancellation(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline, "canceled", false)
		} else if thinker.terminal != nil && thinker.terminal.hasALeg() && errors.Is(thinker.terminal.aLegErr(), context.DeadlineExceeded) {
			thinker.terminal.terminalizeTimeout(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
		} else if thinker.terminal != nil && thinker.terminal.hasALeg() && errors.Is(thinker.terminal.aLegErr(), leglifecycle.ErrALegCanceled) {
			thinker.terminal.terminalizeCancellation(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline, "canceled", false)
		} else if thinker.terminal != nil && !thinker.terminal.finished() {
			if !thinker.terminal.accountingFinalized() {
				thinker.terminal.terminalizeEOF(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
			} else {
				thinker.terminal.terminalizePartialFailure(cleanupCtx, thinker.responsePipeline, thinker.facts.terminalFacts(), attempt, sdkterminal.CommandPartialError, "interleaved continuation failure", errors.New("interleaved continuation failure"))
			}
		}
	} else if phase == interleavedPhaseExecutor {
		if executor != nil && executor.terminal != nil && !executor.terminal.finished() {
			execAttempt := executor.attempt.snapshot()
			if closePending {
				executor.terminal.closeClose(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline)
			} else if cancelPending {
				executor.terminal.terminalizeCancellation(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline, "canceled", false)
			} else if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				executor.terminal.terminalizeTimeout(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline)
			} else if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
				executor.terminal.terminalizeCancellation(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline, "canceled", false)
			} else {
				executor.terminal.terminalizeEOF(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline)
			}
		}
		if thinker != nil && thinker.terminal != nil && !thinker.terminal.finished() {
			attempt := thinker.attempt.snapshot()
			thinker.terminal.terminalizeEOF(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
		}
	}
	s.markFinished()
}

func (s *interleavedContinuationStream) abortExecutorHandoff(ctx context.Context, exec *retryRecvStream, abortErr error) {
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()

	s.mu.Lock()
	closePending := s.closePending
	cancelPending := s.cancelPending
	s.mu.Unlock()

	if exec != nil {
		execAttempt := exec.attempt.closePublicationAndSnapshot()
		if execAttempt == nil {
			execAttempt = exec.attempt.snapshot()
		}
		if execAttempt != nil {
			reason := "interleaved executor handoff aborted"
			if abortErr != nil {
				reason = abortErr.Error()
			}
			execAttempt.terminalizeSwallowed(cleanupCtx, exec.facts, exec.responsePipeline, false, reason, abortErr)
			exec.terminal.finishResponse(exec.responsePipeline, execAttempt)
		}
	}

	if s.thinker != nil {
		attempt := s.thinker.attempt.snapshot()
		if closePending {
			s.thinker.terminal.closeClose(cleanupCtx, s.thinker.facts.terminalFacts(), attempt, s.thinker.responsePipeline)
		} else {
			timeout := errors.Is(abortErr, context.DeadlineExceeded) || (s.thinker.terminal != nil && s.thinker.terminal.hasALeg() && errors.Is(s.thinker.terminal.aLegErr(), context.DeadlineExceeded))
			reason := "canceled"
			aLegCanceled := s.thinker.terminal != nil && s.thinker.terminal.hasALeg() && errors.Is(s.thinker.terminal.aLegErr(), leglifecycle.ErrALegCanceled)
			if !cancelPending && !errors.Is(abortErr, context.Canceled) && !aLegCanceled {
				if abortErr != nil {
					reason = abortErr.Error()
				} else {
					reason = "interleaved executor handoff aborted"
				}
			}
			s.thinker.terminal.terminalizeCancellation(cleanupCtx, s.thinker.facts.terminalFacts(), attempt, s.thinker.responsePipeline, reason, timeout)
		}
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
		if s.thinker != nil && s.thinker.recovery != nil {
			s.thinker.recovery.logMemoPersistFailed(ctx, s.thinker.facts.traceID, persistErr)
		}
	}
}

func (s *interleavedContinuationStream) markFinished() {
	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()
	if s.thinker != nil && s.thinker.terminal != nil {
		s.thinker.terminal.endALeg(aLegEndOuter)
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
		if executor != nil && executor.terminal != nil && executor.terminal.hasALeg() {
			_ = executor.terminal.cancelALeg(ctx, cause)
		} else if thinker != nil && thinker.terminal != nil && thinker.terminal.hasALeg() {
			_ = thinker.terminal.cancelALeg(ctx, cause)
		}
		return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
	}
	active := s.activeRecvLocked()
	phase := s.phase
	s.mu.Unlock()
	if active == nil {
		return lipapi.CancelResult{}
	}

	var res lipapi.CancelResult
	if !active.terminal.finished() {
		if active.terminal != nil && active.terminal.hasALeg() {
			_ = active.terminal.cancelALeg(ctx, cause)
			res = lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
		}
	}

	switch phase {
	case interleavedPhaseThinker:
		s.persistInterruptedThinkerMemo(ctx)
		if s.thinker != nil && s.thinker.terminal != nil && !s.thinker.terminal.finished() {
			s.thinker.terminal.terminalizeCancellation(ctx, s.thinker.facts.terminalFacts(), s.thinker.attempt.snapshot(), s.thinker.responsePipeline, cause.Detail, false)
		}
	case interleavedPhaseExecutor:
		if s.executor != nil && s.executor.terminal != nil && !s.executor.terminal.finished() {
			s.executor.terminal.terminalizeCancellation(ctx, s.executor.facts.terminalFacts(), s.executor.attempt.snapshot(), s.executor.responsePipeline, cause.Detail, false)
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
			parent = thinker.responsePipeline.withDecisionEvidence(thinker.facts.projectContext(parent, thinker.responsePipeline.log), thinker.terminal)
		}
		if executor != nil && executor.terminal != nil && executor.terminal.hasALeg() {
			_ = executor.terminal.cancelALeg(parent, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		} else if thinker != nil && thinker.terminal != nil && thinker.terminal.hasALeg() {
			_ = thinker.terminal.cancelALeg(parent, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		}
		return nil
	}
	phase := s.phase
	s.mu.Unlock()

	var err error
	if phase == interleavedPhaseThinker {
		parent := context.Background()
		if s.thinker != nil {
			parent = s.thinker.responsePipeline.withDecisionEvidence(s.thinker.facts.projectContext(parent, s.thinker.responsePipeline.log), s.thinker.terminal)
		}
		s.persistInterruptedThinkerMemo(parent)
		if s.thinker != nil {
			err = s.thinker.Close()
		}
	} else if phase == interleavedPhaseExecutor && s.executor != nil {
		err = s.executor.Close()
	}
	s.markFinished()
	return err
}
