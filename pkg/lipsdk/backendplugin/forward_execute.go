package backendplugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

// OpenManagedStream opens an upstream managed event stream for one Execute attempt.
// ctx is the incoming plugin Execute stream context and must be respected for cancellation.
type OpenManagedStream func(ctx context.Context, inv Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error)

// ForwardExecute accepts the start frame, opens the upstream managed stream via open,
// and coordinates bidirectional execution:
//   - exactly one client-control reader owns ExecuteStream.Recv after START;
//   - exactly one upstream reader owns ManagedEventStream.Recv;
//   - exactly one sequencer/sender owns server-frame Sequence assignment and ExecuteStream.Send;
//   - in-band CANCEL is consumed against the active upstream stream, calling upstream.Cancel
//     with effective deadline and emitting a sequenced CancelOutcome with actual mode;
//   - CLOSE_INPUT remains distinct from CANCEL (no upstream cancellation);
//   - all goroutines cleanly terminate and join when execution completes.
func ForwardExecute(stream ExecuteStream, open OpenManagedStream) error {
	if open == nil {
		return fmt.Errorf("backendplugin: open is required")
	}
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	if start.Kind != ClientFrameStart || start.Invocation == nil {
		return fmt.Errorf("%w: expected start", ErrInvalidFrame)
	}
	if err := ValidateClientFrameBounds(start); err != nil {
		return err
	}
	if err := sendServerFrame(stream, ServerFrame{Kind: ServerFrameAccepted}); err != nil {
		return err
	}

	sequencer := newFrameSequencer(stream)
	call, err := CallFromInvocation(*start.Invocation)
	if err != nil {
		return err
	}

	ms, err := open(stream.Context(), *start.Invocation, call)
	if err != nil {
		if ms != nil {
			defer func() { _ = ms.Close() }()
			if evidenceErr := forwardAccountingEvidence(sequencer, ms); evidenceErr != nil {
				return evidenceErr
			}
		}
		return err
	}
	if ms == nil {
		return fmt.Errorf("backendplugin: open returned nil stream")
	}
	return forwardActiveExecute(stream, sequencer, ms)
}

func forwardActiveExecute(stream ExecuteStream, sequencer *frameSequencer, ms lipapi.ManagedEventStream) error {

	var closeOnce sync.Once
	closeManaged := func() { closeOnce.Do(func() { _ = ms.Close() }) }
	defer closeManaged()

	// Initial accounting evidence created while opening is sent before any canonical frames.
	if err := forwardAccountingEvidence(sequencer, ms); err != nil {
		return err
	}

	handshakeNegotiated := true
	if ns, ok := stream.(OptionalNegotiatedStream); ok {
		handshakeNegotiated = CancellationHandshakeNegotiated(ns.Negotiation())
	}
	if !handshakeNegotiated {
		return forwardLegacyExecute(stream, sequencer, ms)
	}

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

	var canceled atomic.Bool
	var cancelOnce sync.Once
	cancelOutcomeDone := make(chan struct{})
	var execErrMu sync.Mutex
	var execErr error

	recordExecErr := func(err error) {
		execErrMu.Lock()
		defer execErrMu.Unlock()
		if execErr == nil {
			execErr = err
		}
	}

	var wg sync.WaitGroup
	controlDone := make(chan struct{})
	upstreamDone := make(chan struct{})

	// 1. Context watcher: observes external stream context cancellation and drives fallback cancel.
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-streamCtx.Done():
			cancelOnce.Do(func() {
				canceled.Store(true)
				_ = ms.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"})
				close(cancelOutcomeDone)
				closeManaged()
			})
			execCancel()
		case <-execCtx.Done():
		}
	}()

	// 2. Client-control reader: owns ExecuteStream.Recv post-START.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(controlDone)
		for {
			select {
			case <-execCtx.Done():
				return
			default:
			}

			frame, recvErr := stream.Recv()
			if recvErr != nil {
				if !canceled.Load() && execCtx.Err() == nil && streamCtx.Err() != nil {
					cancelOnce.Do(func() {
						canceled.Store(true)
						_ = ms.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"})
						close(cancelOutcomeDone)
					})
				}
				return
			}

			switch frame.Kind {
			case ClientFrameCancel:
				cancelOnce.Do(func() {
					canceled.Store(true)

					cancelCtx := context.Background()
					var cancelFn context.CancelFunc = func() {}
					if frame.CancelDeadlineUnixMS > 0 {
						d := time.UnixMilli(frame.CancelDeadlineUnixMS)
						if streamDeadline, ok := streamCtx.Deadline(); ok && streamDeadline.Before(d) {
							d = streamDeadline
						}
						cancelCtx, cancelFn = context.WithDeadline(cancelCtx, d)
					} else if streamDeadline, ok := streamCtx.Deadline(); ok {
						cancelCtx, cancelFn = context.WithDeadline(cancelCtx, streamDeadline)
					}
					defer cancelFn()

					cause := CancelCauseFromCancelReason(frame.CancelReason)
					res := ms.Cancel(cancelCtx, cause)

					if handshakeNegotiated {
						outcome := &CancelOutcome{
							Acknowledged: true,
							Reason:       frame.CancelReason,
							Mode:         res.Mode,
						}
						if res.Err != nil && outcome.Detail == "" {
							outcome.Detail = res.Err.Error()
						}
						_ = sequencer.Send(ServerFrame{
							Kind:          ServerFrameCancelOutcome,
							CancelOutcome: outcome,
						})
					}
					close(cancelOutcomeDone)
				})
			case ClientFrameCloseInput:
				// CLOSE_INPUT remains distinct from CANCEL: do not cancel upstream.
			default:
			}
		}
	}()

	// 3. Upstream reader: owns ManagedEventStream.Recv.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(upstreamDone)
		for {
			select {
			case <-execCtx.Done():
				return
			default:
			}

			ev, recvErr := ms.Recv(execCtx)
			if errors.Is(recvErr, io.EOF) {
				_ = forwardAccountingEvidence(sequencer, ms)
				if !canceled.Load() {
					_ = forwardPromptCacheObservations(sequencer, ms)
					_ = sequencer.Send(ServerFrame{
						Kind:     ServerFrameTerminal,
						Terminal: &Terminal{Status: TerminalSuccess},
					})
				} else {
					if handshakeNegotiated {
						select {
						case <-cancelOutcomeDone:
						case <-time.After(100 * time.Millisecond):
						}
					}
					_ = sequencer.Send(ServerFrame{
						Kind:     ServerFrameTerminal,
						Terminal: &Terminal{Status: TerminalCancelled},
					})
				}
				execCancel()
				return
			}

			if recvErr != nil {
				_ = forwardAccountingEvidence(sequencer, ms)
				if canceled.Load() || errors.Is(recvErr, context.Canceled) {
					if canceled.Load() && handshakeNegotiated {
						select {
						case <-cancelOutcomeDone:
						case <-time.After(100 * time.Millisecond):
						}
					}
					_ = sequencer.Send(ServerFrame{
						Kind:     ServerFrameTerminal,
						Terminal: &Terminal{Status: TerminalCancelled},
					})
					execCancel()
					return
				}
				if streamCtx.Err() != nil {
					execCancel()
					return
				}
				recordExecErr(recvErr)
				execCancel()
				return
			}

			_ = forwardAccountingEvidence(sequencer, ms)
			if sendErr := sequencer.Send(ServerFrame{
				Kind:  ServerFrameEvent,
				Event: CanonicalEventFromLipapi(ev),
			}); sendErr != nil {
				execCancel()
				return
			}
		}
	}()

	<-upstreamDone
	execCancel()
	if closerDone != nil {
		<-closerDone
	}
	if closerDone != nil {
		<-controlDone
	}
	wg.Wait()
	closeManaged()

	if sequencer.Err() != nil {
		return sequencer.Err()
	}
	execErrMu.Lock()
	if execErr != nil {
		err := execErr
		execErrMu.Unlock()
		return err
	}
	execErrMu.Unlock()

	if streamCtx.Err() != nil {
		return streamCtx.Err()
	}
	return nil
}

func forwardLegacyExecute(stream ExecuteStream, sequencer *frameSequencer, ms lipapi.ManagedEventStream) error {
	ctx := stream.Context()
	var closeOnce sync.Once
	closeManaged := func() { closeOnce.Do(func() { _ = ms.Close() }) }
	defer closeManaged()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = ms.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"})
			closeManaged()
		case <-stopWatch:
		}
	}()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ev, err := ms.Recv(ctx)
		if errors.Is(err, io.EOF) {
			if err := forwardAccountingEvidence(sequencer, ms); err != nil {
				return err
			}
			if err := forwardPromptCacheObservations(sequencer, ms); err != nil {
				return err
			}
			return sequencer.Send(ServerFrame{Kind: ServerFrameTerminal, Terminal: &Terminal{Status: TerminalSuccess}})
		}
		if err != nil {
			if evidenceErr := forwardAccountingEvidence(sequencer, ms); evidenceErr != nil {
				return evidenceErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err := forwardAccountingEvidence(sequencer, ms); err != nil {
			return err
		}
		if err := sequencer.Send(ServerFrame{Kind: ServerFrameEvent, Event: CanonicalEventFromLipapi(ev)}); err != nil {
			return err
		}
	}
}

// CancelCauseFromCancelReason maps wire CancelReason to canonical lipapi.CancelCause.
func CancelCauseFromCancelReason(reason CancelReason) lipapi.CancelCause {
	switch reason {
	case CancelReasonClient:
		return lipapi.CancelCause{Kind: lipapi.CancelExplicit, Detail: "client"}
	case CancelReasonHost:
		return lipapi.CancelCause{Kind: lipapi.CancelExplicit, Detail: "host"}
	case CancelReasonDeadline:
		return lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "deadline"}
	case CancelReasonShutdown:
		return lipapi.CancelCause{Kind: lipapi.CancelExplicit, Detail: "shutdown"}
	default:
		return lipapi.CancelCause{Kind: lipapi.CancelExplicit, Detail: string(reason)}
	}
}

type frameSequencer struct {
	mu           sync.Mutex
	stream       ExecuteStream
	seq          uint64
	terminalSent bool
	err          error
}

func newFrameSequencer(stream ExecuteStream) *frameSequencer {
	return &frameSequencer{
		stream: stream,
		seq:    1,
	}
}

func (s *frameSequencer) Send(frame ServerFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.terminalSent {
		return nil
	}
	if frame.Kind != ServerFrameAccepted {
		frame.Sequence = s.seq
		s.seq++
	}
	if err := sendServerFrame(s.stream, frame); err != nil {
		s.err = err
		return err
	}
	if frame.Kind == ServerFrameTerminal {
		s.terminalSent = true
	}
	return nil
}

func (s *frameSequencer) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *frameSequencer) TerminalSent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalSent
}

func forwardAccountingEvidence(sequencer *frameSequencer, ms lipapi.ManagedEventStream) error {
	source, ok := ms.(AccountingEvidenceSource)
	if !ok {
		return nil
	}
	for _, evidence := range source.DrainAccountingEvidence() {
		evCopy := evidence
		if err := sequencer.Send(ServerFrame{
			Kind:       ServerFrameAccountingEvidence,
			Accounting: &evCopy,
		}); err != nil {
			return err
		}
	}
	return nil
}

func forwardPromptCacheObservations(sequencer *frameSequencer, ms lipapi.ManagedEventStream) error {
	source, ok := ms.(promptcache.ObservationSource)
	if !ok {
		return nil
	}
	for _, observation := range source.DrainPromptCacheObservations() {
		obsCopy := observation
		if err := sequencer.Send(ServerFrame{
			Kind:                   ServerFramePromptCacheObservation,
			PromptCacheObservation: &obsCopy,
		}); err != nil {
			return err
		}
	}
	return nil
}

func sendServerFrame(stream ExecuteStream, frame ServerFrame) error {
	if err := ValidateServerFrameBounds(frame); err != nil {
		return err
	}
	return stream.Send(frame)
}
