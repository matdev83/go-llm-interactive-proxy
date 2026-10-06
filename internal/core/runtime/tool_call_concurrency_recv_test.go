package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

type recvMandatoryRewrite struct{ tcrRewriteFinalizer }

func (recvMandatoryRewrite) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return toolcall.BufferingSpec{MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject}
}

var errRecvLaterFinalizer = errors.New("later finalizer failed")

type recvLaterFailure struct{}

func (recvLaterFailure) ID() string { return "later-failure" }
func (recvLaterFailure) Order() int { return 1 }
func (recvLaterFailure) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, errRecvLaterFinalizer
}

func TestRetryRecvStream_PreservedCallDrainsBeforeLaterError(t *testing.T) {
	t.Parallel()
	backend := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"location":"original"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1"},
		{Kind: lipapi.EventResponseFinished},
	})
	var opens atomic.Int32
	ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backend),
	}, extensions.SnapshotOptions{})
	want := `{"location":"safe"}`
	ex.SetToolCallFinalizers([]toolcall.Finalizer{
		recvMandatoryRewrite{tcrRewriteFinalizer{args: []byte(want)}}, recvLaterFailure{},
	}, 64*1024)
	stream, err := ex.Execute(principalCtx("user-preserved-drain"), tcrWeatherCall())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var events []lipapi.Event
	for range 10 {
		ev, recvErr := stream.Recv(t.Context())
		if recvErr != nil {
			if !errors.Is(recvErr, errRecvLaterFinalizer) {
				t.Fatalf("wrong terminal error: %v", recvErr)
			}
			tools := tcrToolLifecycle(events)
			if len(tools) != 3 || tools[1].Delta != want || tools[2].Kind != lipapi.EventToolCallFinished {
				t.Fatalf("safe lifecycle was not delivered before error: events=%d", len(tools))
			}
			return
		}
		events = append(events, ev)
	}
	t.Fatal("later error was lost")
}

func TestRetryRecvStream_IncompleteMandatoryCallCannotFinishSuccessfully(t *testing.T) {
	for _, boundary := range []string{"response_finished", "eof"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			events := []lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
				{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{`},
			}
			if boundary == "response_finished" {
				events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})
			}
			var opens atomic.Int32
			ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
				"openai": recordingBackend("openai", &opens, lipapi.NewFixedEventStream(events)),
			}, extensions.SnapshotOptions{})
			ex.SetToolCallFinalizers([]toolcall.Finalizer{recvMandatoryRewrite{}}, 64*1024)
			stream, err := ex.Execute(principalCtx("user-incomplete-mandatory"), tcrWeatherCall())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stream.Close() })
			for range 10 {
				ev, recvErr := stream.Recv(t.Context())
				if recvErr != nil {
					if errors.Is(recvErr, io.EOF) || !errors.Is(recvErr, runtime.ErrMandatoryBuffering) {
						t.Fatalf("incomplete mandatory call lost its refusal: %v", recvErr)
					}
					return
				}
				if ev.Kind == lipapi.EventResponseFinished || ev.Kind == lipapi.EventToolCallArgsDelta {
					t.Fatal("incomplete call released or finished")
				}
			}
			t.Fatal("missing refusal")
		})
	}
}
