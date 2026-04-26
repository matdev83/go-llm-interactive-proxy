package runtime_test

import (
	"context"
	"errors"
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
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
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

type panicCompletionGate struct{}

func (panicCompletionGate) ID() string                        { return "panic-g" }
func (panicCompletionGate) Order() int                        { return 0 }
func (panicCompletionGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (panicCompletionGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	panic("completion gate boom")
}

// preOutputPanicOnlyGate always panics in Handle. Used with a no-delta stream so
// [lipapi.OutputCommitted] is false when ResponseFinished is processed (pre-output
// path for the completion-gate buffer).
type preOutputPanicOnlyGate struct{}

func (preOutputPanicOnlyGate) ID() string                        { return "pre-panic-only" }
func (preOutputPanicOnlyGate) Order() int                        { return 0 }
func (preOutputPanicOnlyGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (preOutputPanicOnlyGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	panic("pre-output completion gate")
}

func TestExecute_completionGatePanic_preOutput_recoverableWithoutCommittedOutput(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{preOutputPanicOnlyGate{}},
	})
	ex := &runtime.Executor{
		Store:           st,
		Bus:             bus,
		RuntimeSnapshot: snap,
		Backends: map[string]execbackend.Backend{
			"minstream": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					// No text/tool deltas: pre-output; completion gate panics on ResponseFinished.
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "gate-panic-pre-recv"},
		Route:   lipapi.RouteIntent{Selector: "minstream:m"},
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
	_, err = lipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected collect error after pre-output completion-gate panic")
	}
	if !lipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("pre-output completion-gate panic should map to recoverable pre-output, got %v", err)
	}
	// A second run with a fresh executor does not re-use a poisoned buffer (gateBuf cleared on panic).
	ex2 := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		RuntimeSnapshot: extensions.NewRequestRuntimeSnapshot(
			hooks.New(hooks.Config{}), extensions.SnapshotOptions{
				CompletionGates: []completion.Gate{testPassGate{}},
			}),
		Backends: map[string]execbackend.Backend{
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "x"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	call2 := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "gate-ok-pre-panic-sanity"},
		Route:   lipapi.RouteIntent{Selector: "ok:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s2, err := ex2.Execute(context.Background(), call2)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	t.Cleanup(func() {
		if err := s2.Close(); err != nil {
			t.Errorf("s2 close: %v", err)
		}
	})
	if col, err := lipapi.Collect(context.Background(), s2); err != nil {
		t.Fatalf("second collect: %v", err)
	} else if col.Text.String() != "x" {
		t.Fatalf("second run text: %q", col.Text.String())
	}
}

func TestExecute_completionGatePanic_postCommittedNotRecoverable(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{panicCompletionGate{}},
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
						{Kind: lipapi.EventTextDelta, Delta: "x"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "gate-panic-post"},
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
	_, err = lipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected collect error after completion gate panic")
	}
	if lipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-commit completion gate panic must not be recoverable pre-output")
	}
	var uf *lipapi.UpstreamFailure
	if !errors.As(err, &uf) || uf.Phase != lipapi.PhasePostOutput {
		t.Fatalf("want post-output upstream failure, got %v", err)
	}
}
