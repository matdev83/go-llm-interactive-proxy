package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutor_happyPath_collectNonStreaming(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					_ = call
					_ = ctx
					_ = cand
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "ok"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: rand.New(rand.NewSource(3)),
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
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("opens: %d", opens)
	}
	col, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestExecutor_capabilityRejectBeforeBackendOpen(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]runtime.Backend{
			"nope": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					return nil, errors.New("should not open")
				},
			},
		},
		Rand: rand.New(rand.NewSource(1)),
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "nope:g"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:      lipapi.PartImageRef,
				ImageRef:  "https://example.com/x.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	_, err := ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsReject(err) {
		t.Fatalf("expected capability reject, got %T %v", err, err)
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("backend should not open, opens=%d", opens)
	}
}

func TestExecutor_preOutputRecoverableSwallowsAndLineage(t *testing.T) {
	t.Parallel()
	clock := time.Unix(1700, 0).UTC()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{Now: func() time.Time { return clock }})
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Now:   func() time.Time { return clock },
		Rand:  rand.New(rand.NewSource(1)),
		Backends: map[string]runtime.Backend{
			"bad": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return nil, lipapi.RecoverablePreOutputError(errors.New("temp"))
				},
			},
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "sess-7.2"},
		Route:   lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	leg, err := st.ResolveALeg(context.Background(), "sess-7.2")
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts: want 2 got %d %#v", len(atts), atts)
	}
	if atts[0].BLegID == "" || atts[1].BLegID == "" || atts[0].BLegID == atts[1].BLegID {
		t.Fatalf("expected distinct B-leg ids, got %#v and %#v", atts[0].BLegID, atts[1].BLegID)
	}
	if atts[0].Seq <= 0 || atts[1].Seq <= 0 || atts[0].Seq >= atts[1].Seq {
		t.Fatalf("expected monotonic increasing B-leg seq, got seq %d then %d", atts[0].Seq, atts[1].Seq)
	}
	if atts[0].Outcome != lipapi.AttemptSwallowedFailure {
		t.Fatalf("attempt1 outcome: %s", atts[0].Outcome)
	}
	if atts[1].Outcome != lipapi.AttemptSuccess {
		t.Fatalf("attempt2 outcome: %s", atts[1].Outcome)
	}
}

func TestExecutor_preOutputMultiOpenFailuresThenSuccess(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(1)),
		Backends: map[string]runtime.Backend{
			"bad": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					return nil, lipapi.RecoverablePreOutputError(errors.New("temp"))
				},
			},
			"bad2": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					return nil, lipapi.RecoverablePreOutputError(errors.New("temp"))
				},
			},
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "done"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "bad:m|bad2:m|ok:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&opens) != 3 {
		t.Fatalf("expected 3 backend opens, got %d", opens)
	}
	_, err = lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecutor_postOutputNoSecondBackendOpen(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(1)),
		Backends: map[string]runtime.Backend{
			"one": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					return &deltaThenErrStream{n: 0}, nil
				},
			},
			"two": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "one:m|two:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(ctx)
	if err == nil {
		t.Fatal("expected error after committed output")
	}
	if lipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-output failure must not classify as recoverable pre-output for retry")
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("expected no failover backend open, opens=%d", opens)
	}
	_ = stream.Close()
}

type deltaThenErrStream struct{ n int }

func (d *deltaThenErrStream) Recv(context.Context) (lipapi.Event, error) {
	d.n++
	if d.n == 1 {
		return lipapi.Event{Kind: lipapi.EventResponseStarted}, nil
	}
	if d.n == 2 {
		return lipapi.Event{Kind: lipapi.EventMessageStarted}, nil
	}
	if d.n == 3 {
		return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"}, nil
	}
	return lipapi.Event{}, lipapi.RecoverablePreOutputError(errors.New("late"))
}

func (d *deltaThenErrStream) Close() error { return nil }

func TestExecutor_cancellationRecordsAttempt(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(1)),
		Backends: map[string]runtime.Backend{
			"slow": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					return &cancelWaitStream{ctx: ctx}, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "cancel-sess"},
		Route:   lipapi.RouteIntent{Selector: "slow:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	_ = stream.Close()
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("opens: %d", opens)
	}
	leg, err := st.ResolveALeg(context.Background(), "cancel-sess")
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].Outcome != lipapi.AttemptCancelled {
		t.Fatalf("attempts: %#v", atts)
	}
}

type cancelWaitStream struct {
	ctx context.Context
}

func (c *cancelWaitStream) Recv(ctx context.Context) (lipapi.Event, error) {
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-c.ctx.Done():
		return lipapi.Event{}, c.ctx.Err()
	}
}

func (c *cancelWaitStream) Close() error { return nil }

func TestExecutor_applyNegotiatedDowngradesReasoning(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var seenReasoning string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(1)),
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.EventStream, error) {
					seenReasoning = call.Options.ReasoningEffort
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if seenReasoning != "" {
		t.Fatalf("expected downgrade clearing reasoning, got %q", seenReasoning)
	}
	_, err = lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecutor_backendOpen_contextCarriesTraceAndALeg(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var openTrace, openALeg string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(2)),
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.EventStream, error) {
					openTrace = diag.TraceID(ctx)
					openALeg = diag.ALegID(ctx)
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "x"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
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
	if openTrace != diag.StableCallID(call) {
		t.Fatalf("trace = %q, want %q", openTrace, diag.StableCallID(call))
	}
	if openALeg == "" {
		t.Fatal("expected non-empty a_leg_id in backend Open context")
	}
	_, _ = lipapi.Collect(context.Background(), stream)
}

func TestExecutor_traceUsesCallIDWhenPresent(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var openTrace string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(2)),
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.EventStream, error) {
					openTrace = diag.TraceID(ctx)
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		ID:    "client-req-42",
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
	if openTrace != "client-req-42" {
		t.Fatalf("trace = %q, want client-req-42", openTrace)
	}
	_, _ = lipapi.Collect(context.Background(), stream)
}

func TestExecutor_decisionLog_backendOpened(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Log:   log,
		Rand:  rand.New(rand.NewSource(2)),
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
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
	_, _ = lipapi.Collect(context.Background(), stream)
	dec := buf.Bytes()
	if !bytes.Contains(dec, []byte(`"msg":"backend_stream_opened"`)) {
		t.Fatalf("log missing backend_stream_opened: %s", string(dec))
	}
	var found bool
	for _, line := range bytes.Split(dec, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m["msg"] == "backend_stream_opened" && m["trace_id"] != nil && m["a_leg_id"] != nil {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no structured backend_stream_opened with trace: %s", string(dec))
	}
}

func TestExecutor_routeQueryMergesIntoGenerationOptions(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var captured lipapi.GenerationOptions
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(1)),
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.EventStream, error) {
					captured = call.Options
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4?temperature=0.2&reasoning_effort=high"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Temperature == nil || *captured.Temperature != 0.2 {
		t.Fatalf("temperature from route: %#v", captured.Temperature)
	}
	if captured.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort from route: %q", captured.ReasoningEffort)
	}
}

func TestExecutor_routeQueryDoesNotOverrideExplicitCallOptions(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var captured lipapi.GenerationOptions
	temp := 0.11
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(1)),
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.EventStream, error) {
					captured = call.Options
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4?temperature=0.99"},
		Options: lipapi.GenerationOptions{
			Temperature: &temp,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Temperature == nil || *captured.Temperature != 0.11 {
		t.Fatalf("explicit call temperature must win over route, got %#v", captured.Temperature)
	}
}
