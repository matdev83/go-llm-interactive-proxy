package openaicompat

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// ExecuteOpts configures ForwardExecute model/flavor resolution.
type ExecuteOpts struct {
	DefaultModel  string
	ResolveModel  func(inv backendplugin.Invocation, call lipapi.Call) string
	ResolveFlavor func(call lipapi.Call) Flavor
	Open          func(ctx context.Context, call lipapi.Call, model string, flavor Flavor) (lipapi.ManagedEventStream, error)
}

// ForwardExecute drives a plugin Execute stream from an OpenAI-compatible Open.
func ForwardExecute(stream backendplugin.ExecuteStream, opts ExecuteOpts) error {
	if opts.Open == nil {
		return fmt.Errorf("openaicompat: Open is required")
	}
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
	model := strings.TrimSpace(start.Invocation.CanonicalModelID)
	if opts.ResolveModel != nil {
		model = opts.ResolveModel(*start.Invocation, call)
	}
	if model == "" {
		model = opts.DefaultModel
	}
	flavor := FlavorChat
	if opts.ResolveFlavor != nil {
		flavor = opts.ResolveFlavor(call)
	}
	ctx := context.Background()
	es, err := opts.Open(ctx, call, model, flavor)
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
