package testkit

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func stubToolPrefixEvents(call lipapi.Call) []lipapi.Event {
	if len(call.Tools) == 0 {
		return nil
	}
	name := strings.TrimSpace(call.Tools[0].Name)
	if name == "" {
		name = "stub_tool"
	}
	return []lipapi.Event{
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_stub1", ToolName: name},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_stub1", Delta: `{"q":"ok"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_stub1"},
	}
}

// WireConformanceExecutorSecureSession attaches an in-memory secure-session manager and
// lipapi denial mapping for conformance harness executors (and other tests that construct
// [*runtime.Executor] outside package runtime).
func WireConformanceExecutorSecureSession(tb testing.TB, ex *runtime.Executor) {
	wireStubExecutorSecureSession(tb, ex)
}

func wireStubExecutorSecureSession(tb testing.TB, ex *runtime.Executor) {
	tb.Helper()
	if ex.SecureSession != nil {
		return
	}
	if ex.Store == nil {
		tb.Fatal("stub executor requires store")
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := SecureSessionTestFingerprintKey()
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(ex.Store), app.ManagerConfig{
		FingerprintKey: fk,
		StoreDurable:   true,
	})
	if err != nil {
		tb.Fatal(err)
	}
	ex.SecureSession = mgr
	if ex.SessionDenialMapper == nil {
		ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	}
	ex.SyntheticLocalPrincipal = true
}

func NewStubExecutorWithDeltas(t *testing.T, caps lipapi.BackendCaps, deltas []string, capture *sync.Map) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(42),
		Backends: map[string]execbackend.Backend{
			"stub": {
				Caps: caps,
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					if capture != nil {
						capture.Store("last", call)
					}
					_ = ctx
					_ = cand
					prefix := stubToolPrefixEvents(call)
					evs := make([]lipapi.Event, 0, 2+len(prefix)+len(deltas)+1)
					evs = append(evs,
						lipapi.Event{Kind: lipapi.EventResponseStarted},
						lipapi.Event{Kind: lipapi.EventMessageStarted},
					)
					evs = append(evs, prefix...)
					for _, d := range deltas {
						evs = append(evs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: d})
					}
					evs = append(evs, lipapi.Event{Kind: lipapi.EventResponseFinished})
					return lipapi.NewFixedEventStream(evs), nil
				},
			},
		},
	}
	wireStubExecutorSecureSession(t, ex)
	return ex
}

func NewStubExecutor(t *testing.T, caps lipapi.BackendCaps, text string, capture *sync.Map) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(42),
		Backends: map[string]execbackend.Backend{
			"stub": {
				Caps: caps,
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					if capture != nil {
						capture.Store("last", call)
					}
					_ = ctx
					_ = cand
					prefix := stubToolPrefixEvents(call)
					evs := make([]lipapi.Event, 0, 2+len(prefix)+2)
					evs = append(evs,
						lipapi.Event{Kind: lipapi.EventResponseStarted},
						lipapi.Event{Kind: lipapi.EventMessageStarted},
					)
					evs = append(evs, prefix...)
					evs = append(evs,
						lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text},
						lipapi.Event{Kind: lipapi.EventResponseFinished},
					)
					return lipapi.NewFixedEventStream(evs), nil
				},
			},
		},
	}
	wireStubExecutorSecureSession(t, ex)
	return ex
}
