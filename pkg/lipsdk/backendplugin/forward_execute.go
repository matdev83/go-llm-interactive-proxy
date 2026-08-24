package backendplugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

const maxCancelOutcomeDetailLen = 256

func sanitizeCancelOutcomeDetail(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > maxCancelOutcomeDetailLen {
		return string(runes[:maxCancelOutcomeDetailLen])
	}
	return string(runes)
}

var fallbackCancelGrace = 2 * time.Second

// OpenManagedStream opens an upstream managed event stream for one Execute attempt.
// ctx is the incoming plugin Execute stream context and must be respected for cancellation.
type OpenManagedStream func(ctx context.Context, inv Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error)

// ForwardExecute accepts the start frame, opens the upstream managed stream via open,
// and coordinates bidirectional execution:
//   - exactly one client-control reader owns ExecuteStream.Recv after START;
//   - exactly one upstream reader owns ManagedEventStream.Recv;
//   - the calling goroutine acts as coordinator and is the sole sender through frameSequencer;
//   - in-band CANCEL is consumed against the active upstream stream, calling upstream.Cancel
//     with effective deadline and emitting a sequenced CancelOutcome before the cancelled terminal;
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

	handshakeNegotiated := false
	if ns, ok := stream.(OptionalNegotiatedStream); ok {
		handshakeNegotiated = CancellationHandshakeNegotiated(ns.Negotiation())
	}
	if !handshakeNegotiated {
		return forwardLegacyExecute(stream, sequencer, ms)
	}
	return forwardActiveExecute(stream, sequencer, ms)
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
			cancelCtx, cancelFn := fallbackCancelContext(ctx)
			defer cancelFn()
			_ = ms.Cancel(cancelCtx, lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"})
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

func fallbackCancelContext(streamCtx context.Context) (context.Context, context.CancelFunc) {
	grace := fallbackCancelGrace
	if d, ok := streamCtx.Deadline(); ok {
		grace = max(0, min(grace, time.Until(d)))
	}
	return context.WithTimeout(context.WithoutCancel(streamCtx), grace)
}

// frameSequencer coordinates server-frame sequence assignment and ExecuteStream.Send.
// In the negotiated active execution path, the coordinator is the sole sender;
// the mutex is retained for defense-in-depth and legacy stream support.
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
