package backendplugin

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type controlCancelReq struct {
	reason     CancelReason
	deadlineMS int64
	outcome    bool
}

type controlObservation struct {
	cancel *controlCancelReq
	err    error
}

type upstreamObsKind int

const (
	upstreamObsEvent upstreamObsKind = iota
	upstreamObsEOF
	upstreamObsError
)

type upstreamObservation struct {
	kind  upstreamObsKind
	event lipapi.Event
	err   error
}

type cancelWorkerResult struct {
	res    lipapi.CancelResult
	reason CancelReason
}

type cancellationTiming struct {
	deadline time.Time
}

type cancelObserver interface {
	OnCancelObserved()
}

// cancellationLifecycle is the one source of truth for a negotiated cancel.
// The control reader records receipt before queueing the observation, so an
// already-received CANCEL cannot be lost when an upstream EOF wins a select.
// The coordinator remains the only owner of starting the lifecycle and sending
// its outcome; the worker owns only the physical adapter call and its forced
// close fallback.
type cancellationLifecycle struct {
	mu          sync.Mutex
	request     *controlCancelReq
	started     bool
	outcomeSent bool
	done        chan struct{}
	result      cancelWorkerResult
}

func newCancellationLifecycle() *cancellationLifecycle {
	return &cancellationLifecycle{done: make(chan struct{})}
}

func (c *cancellationLifecycle) observe(req controlCancelReq) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.request == nil {
		reqCopy := req
		c.request = &reqCopy
	}
}

func (c *cancellationLifecycle) requested() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.request != nil
}

func (c *cancellationLifecycle) requestValue() (controlCancelReq, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.request == nil {
		return controlCancelReq{}, false
	}
	return *c.request, true
}

func (c *cancellationLifecycle) needsOutcome() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.request != nil && c.request.outcome
}

func (c *cancellationLifecycle) start(run func(controlCancelReq) cancelWorkerResult) bool {
	c.mu.Lock()
	if c.started || c.request == nil {
		c.mu.Unlock()
		return false
	}
	c.started = true
	req := *c.request
	c.mu.Unlock()

	go func() {
		result := run(req)
		c.mu.Lock()
		c.result = result
		close(c.done)
		c.mu.Unlock()
	}()
	return true
}

func (c *cancellationLifecycle) wait() cancelWorkerResult {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

func (c *cancellationLifecycle) doneCh() <-chan struct{} { return c.done }

func (c *cancellationLifecycle) sendOutcome(sequencer *frameSequencer) error {
	result := c.wait()
	c.mu.Lock()
	if c.outcomeSent {
		c.mu.Unlock()
		return nil
	}
	c.outcomeSent = true
	c.mu.Unlock()

	outcome := &CancelOutcome{
		Acknowledged: result.res.Err == nil,
		Reason:       result.reason,
		Mode:         result.res.Mode,
	}
	if result.res.Err != nil {
		outcome.Detail = genericCancelOutcomeDetail
	}
	return sequencer.Send(ServerFrame{
		Kind:          ServerFrameCancelOutcome,
		CancelOutcome: outcome,
	})
}

// runManagedCancellation establishes the termination contract for adapters:
// Cancel must return when its effective context is done or when Close is
// called. We force Close at the effective deadline and then join Cancel before
// reporting completion, so this coordinator never leaves an unjoinable worker.
func runManagedCancellation(streamCtx context.Context, ms lipapi.ManagedEventStream, closeManaged func(), req controlCancelReq, timing cancellationTiming) cancelWorkerResult {
	cancelCtx, cancelFn := context.WithDeadline(context.WithoutCancel(streamCtx), timing.deadline)
	defer cancelFn()

	resultCh := make(chan lipapi.CancelResult, 1)
	go func() {
		cause := CancelCauseFromCancelReason(req.reason)
		if !req.outcome {
			cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"}
		}
		resultCh <- invokeManagedCancel(cancelCtx, ms, cause)
	}()

	var result lipapi.CancelResult
	select {
	case result = <-resultCh:
	case <-cancelCtx.Done():
		closeManaged()
		// ManagedEventStream.Cancel is required to become joinable after Close.
		result = <-resultCh
	}
	return cancelWorkerResult{res: result, reason: req.reason}
}

func invokeManagedCancel(ctx context.Context, ms lipapi.ManagedEventStream, cause lipapi.CancelCause) (result lipapi.CancelResult) {
	defer func() {
		if recover() != nil {
			result = lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: errors.New("backendplugin: managed stream cancel panicked")}
		}
	}()
	return ms.Cancel(ctx, cause)
}

func effectiveCancellationTiming(streamCtx context.Context, req controlCancelReq) cancellationTiming {
	grace := max(0, fallbackCancelGrace)
	deadline := time.Now().Add(grace)
	if req.deadlineMS > 0 {
		if peerDeadline := time.UnixMilli(req.deadlineMS); peerDeadline.Before(deadline) {
			deadline = peerDeadline
		}
	}
	if streamDeadline, ok := streamCtx.Deadline(); ok && streamDeadline.Before(deadline) {
		deadline = streamDeadline
	}
	return cancellationTiming{deadline: deadline}
}

// forwardActiveExecute coordinates bidirectional execution for a negotiated stream.
// The calling goroutine acts as coordinator and is the sole sender through sequencer.
func forwardActiveExecute(stream ExecuteStream, sequencer *frameSequencer, ms lipapi.ManagedEventStream) error {
	// Initial accounting evidence created while opening is sent before any canonical frames.
	if err := forwardAccountingEvidence(sequencer, ms); err != nil {
		return err
	}

	var closeOnce sync.Once
	closeManaged := func() { closeOnce.Do(func() { _ = ms.Close() }) }
	defer closeManaged()

	streamCtx := stream.Context()
	execCtx, execCancel := context.WithCancel(streamCtx)
	defer execCancel()

	var closerDone chan struct{}
	if closer, ok := stream.(OptionalExecuteStreamCloser); ok {
		closerDone = make(chan struct{})
		go func() {
			defer close(closerDone)
			<-execCtx.Done()
			_ = closer.Close()
		}()
	}

	controlObsCh := make(chan controlObservation, 1)
	upstreamObsCh := make(chan upstreamObservation, 16)
	cancellation := newCancellationLifecycle()

	var wg sync.WaitGroup
	var cancellationGraceTimer *time.Timer
	var cancellationGraceCh <-chan time.Time

	// 1. Client-control reader: owns ExecuteStream.Recv post-START.
	wg.Go(func() {
		for {
			if execCtx.Err() != nil {
				return
			}

			frame, recvErr := stream.Recv()
			if recvErr != nil {
				select {
				case controlObsCh <- controlObservation{err: recvErr}:
				case <-execCtx.Done():
					return
				}
				return
			}

			switch frame.Kind {
			case ClientFrameCancel:
				req := controlCancelReq{
					reason:     frame.CancelReason,
					deadlineMS: frame.CancelDeadlineUnixMS,
					outcome:    true,
				}
				// Record receipt before queueing. This is intentionally not
				// guarded by execCtx: cancellation is the terminal control fact.
				cancellation.observe(req)
				if observer, ok := stream.(cancelObserver); ok {
					observer.OnCancelObserved()
				}
				controlObsCh <- controlObservation{cancel: &req}
				return
			case ClientFrameCloseInput:
				// CLOSE_INPUT remains distinct from CANCEL: do not cancel upstream.
			default:
			}
		}
	})

	// 2. Upstream reader: owns ManagedEventStream.Recv.
	wg.Go(func() {
		for {
			if execCtx.Err() != nil {
				return
			}

			ev, recvErr := ms.Recv(execCtx)
			if errors.Is(recvErr, io.EOF) {
				upstreamObsCh <- upstreamObservation{kind: upstreamObsEOF}
				return
			}
			if recvErr != nil {
				upstreamObsCh <- upstreamObservation{kind: upstreamObsError, err: recvErr}
				return
			}

			select {
			case upstreamObsCh <- upstreamObservation{kind: upstreamObsEvent, event: ev}:
			case <-execCtx.Done():
				return
			}
		}
	})

	startCancellation := func() {
		req, ok := cancellation.requestValue()
		if !ok {
			return
		}
		timing := effectiveCancellationTiming(streamCtx, req)
		started := cancellation.start(func(req controlCancelReq) cancelWorkerResult {
			return runManagedCancellation(streamCtx, ms, closeManaged, req, timing)
		})
		if started && cancellation.needsOutcome() {
			cancellationGraceTimer = time.NewTimer(max(0, time.Until(timing.deadline)))
			cancellationGraceCh = cancellationGraceTimer.C
		}
	}
	startFallbackCancellation := func() {
		if cancellation.requested() {
			startCancellation()
			return
		}
		cancellation.observe(controlCancelReq{reason: CancelReasonUnspecified})
		startCancellation()
	}

	sendCancelledTerminal := func() {
		startCancellation()
		// Cancellation can resolve before the upstream reader reports its
		// terminal error. Drain any provider accounting evidence before the
		// terminal so an early forced close cannot strand it.
		_ = forwardAccountingEvidence(sequencer, ms)
		if cancellation.needsOutcome() {
			_ = cancellation.sendOutcome(sequencer)
		}
		_ = sequencer.Send(ServerFrame{
			Kind:     ServerFrameTerminal,
			Terminal: &Terminal{Status: TerminalCancelled},
		})
	}

	streamCtxDoneCh := streamCtx.Done()
	cancellationDoneCh := cancellation.doneCh()
	var execErr error

	// Coordinator select loop.
coordinatorLoop:
	for {
		select {
		case obs := <-controlObsCh:
			if obs.cancel != nil {
				// Receipt was recorded by the reader; observe again is harmless
				// and keeps this path correct for future control sources.
				cancellation.observe(*obs.cancel)
				startCancellation()
			}
			if obs.err != nil && streamCtx.Err() != nil {
				startFallbackCancellation()
				if cancellation.needsOutcome() {
					continue
				}
				execCancel()
				break coordinatorLoop
			}

		case <-cancellationDoneCh:
			cancellationDoneCh = nil
			if !cancellation.needsOutcome() {
				execCancel()
				break coordinatorLoop
			}
			// Acknowledgement is a distinct phase from upstream terminal
			// observation. Publish it promptly, but keep the B-leg reader alive
			// so final usage evidence and EOF can still be drained and ordered.
			_ = cancellation.sendOutcome(sequencer)

		case <-cancellationGraceCh:
			cancellationGraceCh = nil
			// If the B-leg has not produced its terminal observation by the
			// bounded grace, force-close it and let its terminal observation
			// complete the ordering. Cancel itself is already joined or will be
			// joined by the lifecycle worker.
			closeManaged()
			execCancel()

		case obs := <-upstreamObsCh:
			switch obs.kind {
			case upstreamObsEvent:
				_ = forwardAccountingEvidence(sequencer, ms)
				if sendErr := sequencer.Send(ServerFrame{
					Kind:  ServerFrameEvent,
					Event: CanonicalEventFromLipapi(obs.event),
				}); sendErr != nil {
					execCancel()
					break coordinatorLoop
				}

			case upstreamObsEOF:
				select {
				case cObs := <-controlObsCh:
					if cObs.cancel != nil {
						cancellation.observe(*cObs.cancel)
					}
				default:
				}
				_ = forwardAccountingEvidence(sequencer, ms)
				if !cancellation.requested() {
					_ = forwardPromptCacheObservations(sequencer, ms)
					_ = sequencer.Send(ServerFrame{
						Kind:     ServerFrameTerminal,
						Terminal: &Terminal{Status: TerminalSuccess},
					})
				} else {
					sendCancelledTerminal()
				}
				execCancel()
				break coordinatorLoop

			case upstreamObsError:
				select {
				case cObs := <-controlObsCh:
					if cObs.cancel != nil {
						cancellation.observe(*cObs.cancel)
					}
				default:
				}
				_ = forwardAccountingEvidence(sequencer, ms)
				if cancellation.requested() || errors.Is(obs.err, context.Canceled) {
					if !cancellation.requested() {
						startFallbackCancellation()
					}
					sendCancelledTerminal()
					execCancel()
					break coordinatorLoop
				}
				if streamCtx.Err() != nil {
					startFallbackCancellation()
					execCancel()
					break coordinatorLoop
				}

				// Non-cancellation upstream error: ensure terminal is sent before
				// unblocking the control reader via host CloseSend. Use a constant
				// non-sensitive bounded error for the public protocol; the original
				// error is preserved only as the internal return value.
				_ = sequencer.Send(ServerFrame{
					Kind: ServerFrameTerminal,
					Terminal: &Terminal{
						Status: TerminalFailure,
						Error: &PluginError{
							Code:      ErrorCodeInternal,
							Message:   "upstream execution failed",
							Retryable: false,
						},
					},
				})
				execErr = obs.err
				execCancel()
				break coordinatorLoop
			}

		case <-streamCtxDoneCh:
			streamCtxDoneCh = nil
			if cancellation.needsOutcome() {
				startCancellation()
				// The context cancellation will cause the upstream reader to
				// report its terminal observation; do not synthesize one here.
				continue
			}
			startFallbackCancellation()
			execCancel()
			break coordinatorLoop
		}
	}

	// A receipt can race the final upstream observation. Start it before
	// teardown so every cancellation request is physically processed.
	startCancellation()
	execCancel()
	if closerDone != nil {
		<-closerDone
	}
	wg.Wait()
	if cancellation.requested() {
		_ = cancellation.wait()
	}
	if cancellationGraceTimer != nil {
		cancellationGraceTimer.Stop()
	}
	closeManaged()

	if sequencer.Err() != nil {
		return sequencer.Err()
	}
	if execErr != nil {
		return execErr
	}
	if streamCtx.Err() != nil {
		return streamCtx.Err()
	}
	return nil
}
