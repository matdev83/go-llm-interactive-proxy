package backendplugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

// OpenManagedStream opens an upstream managed event stream for one Execute attempt.
// ctx is the incoming plugin Execute stream context and must be respected for cancellation.
type OpenManagedStream func(ctx context.Context, inv Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error)

// ForwardExecute accepts the start frame, opens the upstream managed stream via open,
// and pumps events to the plugin Execute stream with Codex-grade cancellation:
// stream context is observed, and Cancel is invoked on the managed stream when the
// client disconnects.
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
	seq := uint64(1)
	call, err := CallFromInvocation(*start.Invocation)
	if err != nil {
		return err
	}
	ms, err := open(stream.Context(), *start.Invocation, call)
	if err != nil {
		// An opening path may have incurred provider-only work before its first
		// canonical event. Preserve the original open error, but do not discard
		// accounting evidence already published by the managed stream.
		if ms != nil {
			defer func() { _ = ms.Close() }()
			if evidenceErr := forwardAccountingEvidence(stream, ms, &seq); evidenceErr != nil {
				return evidenceErr
			}
		}
		return err
	}
	if ms == nil {
		return fmt.Errorf("backendplugin: open returned nil stream")
	}
	var closeOnce sync.Once
	closeManaged := func() { closeOnce.Do(func() { _ = ms.Close() }) }
	defer closeManaged()
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-stream.Context().Done():
			_ = ms.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"})
			closeManaged()
		case <-stopWatch:
		}
	}()

	if source, ok := ms.(AccountingEvidenceSource); ok {
		// Evidence created while opening is sent before the first canonical frame.
		for _, evidence := range source.DrainAccountingEvidence() {
			if err := sendServerFrame(stream, ServerFrame{Kind: ServerFrameAccountingEvidence, Sequence: seq, Accounting: &evidence}); err != nil {
				return err
			}
			seq++
		}
	}
	for {
		if err := stream.Context().Err(); err != nil {
			return err
		}
		ev, err := ms.Recv(stream.Context())
		if errors.Is(err, io.EOF) {
			if err := forwardAccountingEvidence(stream, ms, &seq); err != nil {
				return err
			}
			// Prompt-cache observations are published only at successful
			// terminal eligibility. Failed/cancelled attempts never become
			// renewable targets by implication.
			if err := forwardPromptCacheObservations(stream, ms, &seq); err != nil {
				return err
			}
			return sendServerFrame(stream, ServerFrame{
				Kind: ServerFrameTerminal, Sequence: seq,
				Terminal: &Terminal{Status: TerminalSuccess},
			})
		}
		if err != nil {
			if evidenceErr := forwardAccountingEvidence(stream, ms, &seq); evidenceErr != nil {
				return evidenceErr
			}
			if stream.Context().Err() != nil {
				return stream.Context().Err()
			}
			return err
		}
		if err := forwardAccountingEvidence(stream, ms, &seq); err != nil {
			return err
		}
		if err := sendServerFrame(stream, ServerFrame{
			Kind: ServerFrameEvent, Sequence: seq,
			Event: CanonicalEventFromLipapi(ev),
		}); err != nil {
			return err
		}
		seq++
	}
}

func forwardAccountingEvidence(stream ExecuteStream, ms lipapi.ManagedEventStream, seq *uint64) error {
	source, ok := ms.(AccountingEvidenceSource)
	if !ok {
		return nil
	}
	for _, evidence := range source.DrainAccountingEvidence() {
		if err := sendServerFrame(stream, ServerFrame{Kind: ServerFrameAccountingEvidence, Sequence: *seq, Accounting: &evidence}); err != nil {
			return err
		}
		*seq = *seq + 1
	}
	return nil
}

func forwardPromptCacheObservations(stream ExecuteStream, ms lipapi.ManagedEventStream, seq *uint64) error {
	source, ok := ms.(promptcache.ObservationSource)
	if !ok {
		return nil
	}
	for _, observation := range source.DrainPromptCacheObservations() {
		if err := sendServerFrame(stream, ServerFrame{Kind: ServerFramePromptCacheObservation, Sequence: *seq, PromptCacheObservation: &observation}); err != nil {
			return err
		}
		*seq = *seq + 1
	}
	return nil
}

func sendServerFrame(stream ExecuteStream, frame ServerFrame) error {
	if err := ValidateServerFrameBounds(frame); err != nil {
		return err
	}
	return stream.Send(frame)
}
