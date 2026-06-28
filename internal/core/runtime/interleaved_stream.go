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
)

type interleavedPhase int

const (
	interleavedPhaseThinker interleavedPhase = iota
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

	mu            sync.Mutex
	finished      bool
	memoPersisted bool
}

var (
	_ lipapi.EventStream        = (*interleavedContinuationStream)(nil)
	_ lipapi.ManagedEventStream = (*interleavedContinuationStream)(nil)
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
	default:
		return s.recvExecutor(ctx)
	}
}

func (s *interleavedContinuationStream) popPending() (lipapi.Event, bool) {
	if len(s.pending) == 0 {
		return lipapi.Event{}, false
	}
	ev := s.pending[0]
	s.pending = s.pending[1:]
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
				return s.beginExecutorContinuation(ctx)
			}
			if _, persistErr := s.captureAndPersistThinkerMemo(ctx, true); persistErr != nil {
				s.finishWithCleanup(ctx)
				return lipapi.Event{}, persistErr
			}
			s.finishWithCleanup(ctx)
			return lipapi.Event{}, err
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
		s.pending = append(s.pending,
			lipapi.Event{Kind: lipapi.EventResponseStarted},
			lipapi.Event{Kind: lipapi.EventMessageStarted},
		)
		s.responseStarted = true
	}
	s.recordVisibleOutput(ev)
	s.pending = append(s.pending, ev)
}

func (s *interleavedContinuationStream) recordVisibleOutput(ev lipapi.Event) {
	if s.thinker == nil {
		return
	}
	s.thinker.markOutputCommitted(ev)
	s.thinker.accounting.observeClientEvent(s.thinker.now(), ev)
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
		if ctx == nil {
			persistCtx = context.Background()
		} else {
			persistCtx = context.WithoutCancel(ctx)
		}
	}

	memo := s.recorder.Finish(interrupted)
	if interrupted && strings.TrimSpace(memo.Memo) == "" {
		s.mu.Lock()
		s.memoPersisted = false
		s.mu.Unlock()
		return s.state, nil
	}
	memo.VisibleToClient = s.visibleCommitted
	s.thinker.executor.logInterleavedMemoCaptured(persistCtx, s.thinker.traceID, memo)
	if !interrupted {
		s.thinker.executor.logInterleavedPhaseTransition(persistCtx, s.thinker.traceID)
	}
	state, err := s.thinker.executor.persistCapturedMemo(persistCtx, s.thinker.aLegID, state, memo)
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
		s.abortExecutorHandoff(ctx, execStream)
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
	s.executor = execStream
	s.phase = interleavedPhaseExecutor
	s.state = state
	s.mu.Unlock()
	return s.recvExecutor(ctx)
}

func (s *interleavedContinuationStream) handoffAborted(ctx context.Context) error {
	s.mu.Lock()
	finished := s.finished
	s.mu.Unlock()
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
	if inner := s.thinker.takeAndNilInner(); inner != nil {
		s.thinker.cancelAndCloseInner(cleanupCtx, inner, leglifecycle.CancelCause{
			Kind:   lipapi.CancelContextDone,
			Detail: "interleaved thinker handoff",
		})
	}
	if s.thinker.aScope != nil && s.thinker.bleg.BLegID != "" {
		s.thinker.aScope.ReleaseBLeg(s.thinker.bleg.BLegID)
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
		if inner := executor.takeAndNilInner(); inner != nil {
			executor.cancelAndCloseInner(cleanupCtx, inner, leglifecycle.CancelCause{
				Kind:   lipapi.CancelContextDone,
				Detail: "interleaved executor finished",
			})
		}
		return
	}
	if thinker != nil {
		if inner := thinker.takeAndNilInner(); inner != nil {
			thinker.cancelAndCloseInner(cleanupCtx, inner, leglifecycle.CancelCause{
				Kind:   lipapi.CancelContextDone,
				Detail: "interleaved thinker finished",
			})
		}
		if thinker.aScope != nil && thinker.bleg.BLegID != "" {
			thinker.aScope.ReleaseBLeg(thinker.bleg.BLegID)
		}
	}
}

func (s *interleavedContinuationStream) finishWithCleanup(ctx context.Context) {
	s.closeActiveInner(ctx)
	s.markFinished()
}

func (s *interleavedContinuationStream) abortExecutorHandoff(ctx context.Context, exec *retryRecvStream) {
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()
	if exec != nil {
		if inner := exec.takeAndNilInner(); inner != nil {
			exec.cancelAndCloseInner(cleanupCtx, inner, leglifecycle.CancelCause{
				Kind:   lipapi.CancelContextDone,
				Detail: "interleaved executor handoff aborted",
			})
		}
		exec.markFinished()
	}
	s.closeThinkerInner(ctx)
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

func (s *interleavedContinuationStream) activeRecv() *retryRecvStream {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == interleavedPhaseExecutor && s.executor != nil {
		return s.executor
	}
	return s.thinker
}

func (s *interleavedContinuationStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if s == nil {
		return lipapi.CancelResult{}
	}
	active := s.activeRecv()
	if active == nil {
		return lipapi.CancelResult{}
	}
	var res lipapi.CancelResult
	if !active.isFinished() {
		if active.aScope != nil {
			_ = active.aScope.Cancel(ctx, cause)
			res = lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
		} else if inner := active.loadInner(); inner != nil {
			res = inner.Cancel(ctx, cause)
		}
	}
	s.mu.Lock()
	thinkerPhase := s.phase == interleavedPhaseThinker
	s.mu.Unlock()
	if thinkerPhase {
		_, _ = s.captureAndPersistThinkerMemo(ctx, true)
	}
	s.markFinished()
	return res
}

func (s *interleavedContinuationStream) Close() error {
	if s == nil {
		return nil
	}
	active := s.activeRecv()
	var err error
	if active != nil {
		err = active.Close()
	}
	s.mu.Lock()
	thinkerPhase := s.phase == interleavedPhaseThinker
	s.mu.Unlock()
	if thinkerPhase {
		_, _ = s.captureAndPersistThinkerMemo(context.Background(), true)
	}
	s.markFinished()
	return err
}
