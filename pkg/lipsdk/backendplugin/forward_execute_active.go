package backendplugin

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type controlCancelReq struct {
	reason     CancelReason
	deadlineMS int64
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

	var canceled atomic.Bool

	var closerDone chan struct{}
	if closer, ok := stream.(OptionalExecuteStreamCloser); ok {
		closerDone = make(chan struct{})
		go func() {
			defer close(closerDone)
			<-execCtx.Done()
			_ = closer.Close()
		}()
	}

	controlObsCh := make(chan controlObservation, 8)
	upstreamObsCh := make(chan upstreamObservation, 16)
	cancelResCh := make(chan cancelWorkerResult, 1)

	var wg sync.WaitGroup
	var cancelWG sync.WaitGroup

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
				canceled.Store(true) // Receipt-time cancel visibility
				select {
				case controlObsCh <- controlObservation{
					cancel: &controlCancelReq{
						reason:     frame.CancelReason,
						deadlineMS: frame.CancelDeadlineUnixMS,
					},
				}:
				case <-execCtx.Done():
					return
				}
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

	var cancelSpawned bool
	var inBandCancelRunning bool
	var inBandCancelResolved bool
	var execErr error

	// Helper to spawn bounded in-band cancel worker
	spawnInBandCancelWorker := func(req *controlCancelReq) {
		canceled.Store(true)
		cancelSpawned = true
		inBandCancelRunning = true
		cancelWG.Go(func() {
			cancelCtx := context.Background()
			var cancelFn context.CancelFunc = func() {}
			if req.deadlineMS > 0 {
				d := time.UnixMilli(req.deadlineMS)
				if streamDeadline, ok := streamCtx.Deadline(); ok && streamDeadline.Before(d) {
					d = streamDeadline
				}
				cancelCtx, cancelFn = context.WithDeadline(cancelCtx, d)
			} else if streamDeadline, ok := streamCtx.Deadline(); ok {
				cancelCtx, cancelFn = context.WithDeadline(cancelCtx, streamDeadline)
			}
			defer cancelFn()

			cause := CancelCauseFromCancelReason(req.reason)
			res := ms.Cancel(cancelCtx, cause)
			cancelResCh <- cancelWorkerResult{
				res:    res,
				reason: req.reason,
			}
		})
	}

	// Helper to spawn fallback cancel worker
	spawnFallbackCancelWorker := func() {
		canceled.Store(true)
		cancelSpawned = true
		cancelWG.Go(func() {
			cancelCtx, cancelFn := fallbackCancelContext(streamCtx)
			defer cancelFn()
			_ = ms.Cancel(cancelCtx, lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"})
			closeManaged()
		})
	}

	// Helper to handle in-band cancel worker result
	handleInBandCancelResult := func(res cancelWorkerResult) {
		inBandCancelResolved = true
		outcome := &CancelOutcome{
			Acknowledged: res.res.Err == nil,
			Reason:       res.reason,
			Mode:         res.res.Mode,
		}
		if res.res.Err != nil {
			outcome.Detail = sanitizeCancelOutcomeDetail(res.res.Err.Error())
		}
		_ = sequencer.Send(ServerFrame{
			Kind:          ServerFrameCancelOutcome,
			CancelOutcome: outcome,
		})
	}

	// Helper to ensure in-band cancel is resolved before sending terminal
	resolveInBandCancelIfPending := func() {
		if inBandCancelRunning && !inBandCancelResolved {
			res := <-cancelResCh
			handleInBandCancelResult(res)
		}
	}

	sendCancelledTerminal := func() {
		resolveInBandCancelIfPending()
		_ = sequencer.Send(ServerFrame{
			Kind:     ServerFrameTerminal,
			Terminal: &Terminal{Status: TerminalCancelled},
		})
	}

	streamCtxDoneCh := streamCtx.Done()

	// Coordinator select loop
coordinatorLoop:
	for {
		select {
		case obs := <-controlObsCh:
			if obs.cancel != nil {
				if !cancelSpawned {
					spawnInBandCancelWorker(obs.cancel)
				}
			}
			if obs.err != nil {
				if !canceled.Load() && execCtx.Err() == nil && streamCtx.Err() != nil {
					spawnFallbackCancelWorker()
					execCancel()
				}
			}

		case res := <-cancelResCh:
			handleInBandCancelResult(res)

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
				_ = forwardAccountingEvidence(sequencer, ms)
				if !canceled.Load() {
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
				_ = forwardAccountingEvidence(sequencer, ms)
				if canceled.Load() || errors.Is(obs.err, context.Canceled) {
					sendCancelledTerminal()
					execCancel()
					break coordinatorLoop
				}
				if streamCtx.Err() != nil {
					sendCancelledTerminal()
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
			if !canceled.Load() {
				spawnFallbackCancelWorker()
				execCancel()
			}
		}
	}

	execCancel()
	if closerDone != nil {
		<-closerDone
	}
	wg.Wait()
	cancelWG.Wait()
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
