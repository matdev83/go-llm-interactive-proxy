package auxreq_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	corestate "github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

type countingSubmit struct {
	id   string
	n    atomic.Int32
	fail sdk.FailureMode
}

func (c *countingSubmit) ID() string                   { return c.id }
func (c *countingSubmit) FailureMode() sdk.FailureMode { return c.fail }
func (c *countingSubmit) Order() int                   { return 0 }
func (c *countingSubmit) Handle(context.Context, *lipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	c.n.Add(1)
	return sdk.SubmitDecision{}, nil
}

func TestClient_suppressedSubmitSkipped(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingSubmit{id: "watch-me", fail: sdk.FailOpen}
	bus := hooks.New(hooks.Config{
		SubmitHooks: []sdk.SubmitHook{counter},
	})
	var ex *runtime.Executor
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	mgr := mustSecureManager(t, st)
	ex = &runtime.Executor{
		Store:                   st,
		Bus:                     bus,
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		SyntheticLocalPrincipal: true,
		SessionDenialMapper:     lipapidenial.MapToSessionDenial,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}

	ctx := context.Background()
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
	}

	primary := lipapi.CloneCall(*call)
	_, err = ex.Execute(ctx, &primary)
	if err != nil {
		t.Fatal(err)
	}
	if counter.n.Load() != 1 {
		t.Fatalf("primary submit hooks want 1 got %d", counter.n.Load())
	}

	counter.n.Store(0)
	aux := snap.Aux()
	ac := lipapi.CloneCall(*call)
	_, err = aux.Stream(ctx, auxiliary.Request{
		Call:           &ac,
		DisablePlugins: []string{"watch-me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if counter.n.Load() != 0 {
		t.Fatalf("suppressed auxiliary path should skip submit hook, got %d", counter.n.Load())
	}
}

func TestClient_Stream_auxiliaryDepthIncremented(t *testing.T) {
	t.Parallel()
	var depthSeen atomic.Int32
	var ex *runtime.Executor
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	lineageStore := mustMemStore(t)
	mgr := mustSecureManager(t, lineageStore)
	ex = &runtime.Executor{
		Store:                   lineageStore,
		Bus:                     bus,
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		SyntheticLocalPrincipal: true,
		SessionDenialMapper:     lipapidenial.MapToSessionDenial,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					depthSeen.Store(int32(execctx.AuxiliaryDepth(ctx)))
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(2),
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
	}
	_, err := snap.Aux().Stream(context.Background(), auxiliary.Request{Call: call})
	if err != nil {
		t.Fatal(err)
	}
	if depthSeen.Load() != 1 {
		t.Fatalf("auxiliary depth want 1 got %d", depthSeen.Load())
	}
}

func TestClient_lineageExtension(t *testing.T) {
	t.Parallel()
	var ex *runtime.Executor
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	var captured lipapi.Call
	lineageStore := mustMemStore(t)
	mgr := mustSecureManager(t, lineageStore)
	ex = &runtime.Executor{
		Store:                   lineageStore,
		Bus:                     bus,
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		SyntheticLocalPrincipal: true,
		SessionDenialMapper:     lipapidenial.MapToSessionDenial,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					captured = call
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(3),
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
	}
	_, err := snap.Aux().Stream(context.Background(), auxiliary.Request{
		Call:          call,
		Role:          "verifier",
		ParentTraceID: "p-trace",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := captured.Extensions["lip.aux.lineage.v1"]
	if !ok || len(raw) == 0 {
		t.Fatalf("missing lineage extension: %#v", captured.Extensions)
	}
	s := string(raw)
	if !strings.Contains(s, "verifier") || !strings.Contains(s, "p-trace") {
		t.Fatalf("lineage payload unexpected: %s", s)
	}
}

func mustMemStore(t *testing.T) b2bua.Store {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func mustSecureManager(t *testing.T, lineageStore b2bua.Store) *app.Manager {
	t.Helper()
	fk := testkit.SecureSessionTestFingerprintKey()
	st := memory.New(memory.Options{SimulateDurable: true})
	mgr, err := app.NewManager(
		st,
		app.NewRandGenerator(fk),
		b2bualineage.New(lineageStore),
		app.ManagerConfig{
			FingerprintKey: fk,
			StoreDurable:   true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}
