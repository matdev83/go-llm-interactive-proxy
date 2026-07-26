package service

import (
	"context"
	"fmt"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	if start.Kind != backendplugin.ClientFrameStart || start.Invocation == nil {
		return fmt.Errorf("%w: expected start", backendplugin.ErrInvalidFrame)
	}
	if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		return err
	}
	call, err := backendplugin.CallFromInvocation(*start.Invocation)
	if err != nil {
		return err
	}
	resolved, err := i.resolveModel(stream.Context(), *start.Invocation)
	if err != nil {
		return err
	}
	es, err := i.router.Open(stream.Context(), call, resolved)
	if err != nil {
		return err
	}
	defer func() { _ = es.Close() }()

	seq := uint64(1)
	for {
		if err := stream.Context().Err(); err != nil {
			_ = es.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "plugin_cancel"})
			return err
		}
		ev, err := es.Recv(stream.Context())
		if err == io.EOF {
			return stream.Send(backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
				Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
			})
		}
		if err != nil {
			return err
		}
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq,
			Event: backendplugin.CanonicalEventFromLipapi(ev),
		}); err != nil {
			return err
		}
		seq++
	}
}
