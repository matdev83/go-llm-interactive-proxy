package runtime_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

type testReplaceGate struct{}

func (testReplaceGate) ID() string                        { return "replace-all" }
func (testReplaceGate) Order() int                        { return 0 }
func (testReplaceGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testReplaceGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "replaced"},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}

func TestExecute_completionGateReplacesStream(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{testReplaceGate{}},
	})
	ex := &runtime.Executor{
		Store:           st,
		Bus:             bus,
		RuntimeSnapshot: snap,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "orig"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream close: %v", err)
		}
	})
	col, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "replaced" {
		t.Fatalf("aggregated text: %q", got)
	}
	if !col.FinishReceived {
		t.Fatal("expected finish")
	}
}

type partialThenEOFStream struct {
	evs []lipapi.Event
	i   int
}

func (s *partialThenEOFStream) Recv(context.Context) (lipapi.Event, error) {
	if s.i < len(s.evs) {
		ev := s.evs[s.i]
		s.i++
		return ev, nil
	}
	return lipapi.Event{}, io.EOF
}

func (*partialThenEOFStream) Close() error { return nil }

func TestExecute_completionGate_truncatedUpstreamNoSyntheticSuccess(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{testReplaceGate{}},
	})
	ex := &runtime.Executor{
		Store:           st,
		Bus:             bus,
		RuntimeSnapshot: snap,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return &partialThenEOFStream{evs: []lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "partial"},
					}}, nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "trunc-gate"},
		Route:   lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream close: %v", err)
		}
	})
	if _, err := lipapi.Collect(context.Background(), stream); err == nil {
		t.Fatal("expected truncated stream collect error")
	}
	leg, err := st.ResolveALeg(context.Background(), "trunc-gate")
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) == 0 {
		t.Fatal("expected attempt records")
	}
	last := atts[len(atts)-1]
	if last.Outcome != lipapi.AttemptSurfacedFailure {
		t.Fatalf("want AttemptSurfacedFailure got %s (%s)", last.Outcome, last.Reason)
	}
	if !strings.Contains(last.Reason, "response_finished") {
		t.Fatalf("reason: %q", last.Reason)
	}
}

type testPassGate struct{}

func (testPassGate) ID() string                        { return "pass" }
func (testPassGate) Order() int                        { return 0 }
func (testPassGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testPassGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func TestExecute_completionGateOverflowLivePassthrough(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{testPassGate{}},
	})
	ex := &runtime.Executor{
		Store:           st,
		Bus:             bus,
		RuntimeSnapshot: snap,
		CompletionBufferLimits: completion.BufferLimits{
			MaxEvents: 2,
		},
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "a"},
						{Kind: lipapi.EventTextDelta, Delta: "b"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream close: %v", err)
		}
	})
	col, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "ab" {
		t.Fatalf("aggregated text: %q", got)
	}
	if !col.FinishReceived {
		t.Fatal("expected finish")
	}
}
