//go:build precommit

package runtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestExecutor_weightedFirstBranch_persistsConsumed(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(99),
		Backends: map[string]runtime.Backend{
			"cheap": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
			"expensive": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					t.Fatal("expensive backend must not open when [first] cheap arm succeeds")
					return nil, errors.New("unexpected")
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "wf-first"},
		Route:   lipapi.RouteIntent{Selector: "[first]cheap:m^[weight=100]expensive:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	leg, err := st.ResolveALeg(context.Background(), "wf-first")
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.FetchALeg(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WeightedFirstConsumed {
		t.Fatal("expected WeightedFirstConsumed persisted on A-leg after [first] branch open")
	}
}

type storeAssertRecordWithoutCancel struct {
	*b2bua.MemoryStore
	t *testing.T
}

func (s *storeAssertRecordWithoutCancel) RecordAttempt(ctx context.Context, rec lipapi.AttemptRecord) error {
	if ctx.Err() != nil {
		s.t.Fatalf("RecordAttempt must receive non-canceled context (WithoutCancel); got %v", ctx.Err())
	}
	return s.MemoryStore.RecordAttempt(ctx, rec)
}

func TestExecutor_recordAttempt_usesWithoutCancelOnCanceledRequestCtx(t *testing.T) {
	t.Parallel()
	base, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := &storeAssertRecordWithoutCancel{MemoryStore: base, t: t}
	ctx, cancel := context.WithCancel(context.Background())
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Backends: map[string]runtime.Backend{
			"slow": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					_ = call
					_ = cand
					return &cancelWaitStream{ctx: ctx}, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "wc-sess"},
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
}

type captureRouteObserver struct {
	mu  sync.Mutex
	got []routeObs
}

type routeObs struct {
	decision string
	detail   string
}

func (o *captureRouteObserver) ObserveRouteDecision(_ context.Context, _ string, decision, detail string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, routeObs{decision: decision, detail: detail})
}

func TestExecutor_plan_candidate_observedAndTraced(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureRouteObserver{}
	rt := diag.NewRouteTraceBuffer(8)
	ex := &runtime.Executor{
		Store:         st,
		Bus:           hooks.New(hooks.Config{}),
		Rand:          routing.NewSeededRng(2),
		RouteObserver: obs,
		RouteTrace:    rt,
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{
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
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	obs.mu.Lock()
	var found bool
	for _, e := range obs.got {
		if e.decision == "plan_candidate" && e.detail == "openai:gpt-4" {
			found = true
			break
		}
	}
	obs.mu.Unlock()
	if !found {
		t.Fatalf("expected plan_candidate observation, got %#v", obs.got)
	}
	snap := rt.Snapshot()
	var traced bool
	for _, e := range snap {
		if e.Decision == "plan_candidate" && e.Detail == "openai:gpt-4" {
			traced = true
			break
		}
	}
	if !traced {
		t.Fatalf("expected route trace plan_candidate, got %#v", snap)
	}
}

var _ lipsdk.RouteObserver = (*captureRouteObserver)(nil)
