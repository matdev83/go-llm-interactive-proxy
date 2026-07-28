package backendplugin

import (
	"context"
	"fmt"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
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
	if err := stream.Send(ServerFrame{Kind: ServerFrameAccepted}); err != nil {
		return err
	}
	call, err := CallFromInvocation(*start.Invocation)
	if err != nil {
		return err
	}
	ms, err := open(stream.Context(), *start.Invocation, call)
	if err != nil {
		return err
	}
	defer func() { _ = ms.Close() }()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-stream.Context().Done():
			_ = ms.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"})
			_ = ms.Close()
		case <-stopWatch:
		}
	}()

	seq := uint64(1)
	for {
		if err := stream.Context().Err(); err != nil {
			return err
		}
		ev, err := ms.Recv(stream.Context())
		if err == io.EOF {
			return stream.Send(ServerFrame{
				Kind: ServerFrameTerminal, Sequence: seq,
				Terminal: &Terminal{Status: TerminalSuccess},
			})
		}
		if err != nil {
			if stream.Context().Err() != nil {
				return stream.Context().Err()
			}
			return err
		}
		if err := stream.Send(ServerFrame{
			Kind: ServerFrameEvent, Sequence: seq,
			Event: CanonicalEventFromLipapi(ev),
		}); err != nil {
			return err
		}
		seq++
	}
}
