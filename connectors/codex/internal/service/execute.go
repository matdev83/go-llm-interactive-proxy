package service

import (
	"context"
	"fmt"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/routingstub"
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
	model := resolveRouteModel(i.kind, *start.Invocation)
	if model != "" {
		call.Route.Selector = i.kind + ":" + model
	}
	cand := routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: model}}

	var ms lipapi.ManagedEventStream
	switch {
	case i.http != nil:
		ms, err = i.http.Open(stream.Context(), &call, cand)
	case i.app != nil:
		ms, err = i.app.Open(stream.Context(), &call)
	default:
		return fmt.Errorf("codex connector: not configured")
	}
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
			return stream.Send(backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
				Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
			})
		}
		if err != nil {
			if stream.Context().Err() != nil {
				return stream.Context().Err()
			}
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

var (
	_ backendplugin.Service            = (*Service)(nil)
	_ backendplugin.ConfiguredInstance = (*instance)(nil)
)
