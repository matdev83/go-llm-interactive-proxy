package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestExecutor_backendSeamRegression locks introduce-hexagonal-architecture task 3.3:
// executor policy (capability gate, pre-output recovery, no post-output failover) with
// backends supplied only as [execbackend.Backend] test doubles — no concrete backend plugins.
func TestExecutor_backendSeamRegression(t *testing.T) {
	t.Parallel()

	t.Run("capability reject before backend open", func(t *testing.T) {
		t.Parallel()
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var opens int32
		ex := &runtime.Executor{
			Store: st,
			Bus:   hooks.New(hooks.Config{}),
			Rand:  routing.NewSeededRng(1),
			Backends: map[string]execbackend.Backend{
				"nope": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						atomic.AddInt32(&opens, 1)
						return nil, errors.New("must not open")
					},
				},
			},
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
		_, err = ex.Execute(context.Background(), call)
		if err == nil {
			t.Fatal("expected error")
		}
		if !lipapi.IsReject(err) {
			t.Fatalf("expected capability reject, got %T %v", err, err)
		}
		if atomic.LoadInt32(&opens) != 0 {
			t.Fatalf("backend must not open, opens=%d", opens)
		}
	})

	t.Run("pre-output recovery uses second backend", func(t *testing.T) {
		t.Parallel()
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ex := &runtime.Executor{
			Store: st,
			Bus:   hooks.New(hooks.Config{}),
			Rand:  routing.NewSeededRng(1),
			Backends: map[string]execbackend.Backend{
				"bad": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						return nil, lipapi.RecoverablePreOutputError(errors.New("temp"))
					},
				},
				"ok": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
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
			Route: lipapi.RouteIntent{Selector: "bad:m|ok:m"},
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hi")},
			}},
		}
		s, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lipapi.Collect(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no second backend open after output started", func(t *testing.T) {
		t.Parallel()
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var opens int32
		ex := &runtime.Executor{
			Store: st,
			Bus:   hooks.New(hooks.Config{}),
			Rand:  routing.NewSeededRng(1),
			Backends: map[string]execbackend.Backend{
				"one": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						atomic.AddInt32(&opens, 1)
						return &deltaThenErrStream{n: 0}, nil
					},
				},
				"two": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						atomic.AddInt32(&opens, 1)
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
		for range 3 {
			if _, err := stream.Recv(ctx); err != nil {
				t.Fatalf("unexpected recv error: %v", err)
			}
		}
		if _, err := stream.Recv(ctx); err == nil {
			t.Fatal("expected error after committed output")
		}
		if lipapi.IsRecoverablePreOutput(err) {
			t.Fatal("post-output failure must not classify as recoverable pre-output for retry")
		}
		if atomic.LoadInt32(&opens) != 1 {
			t.Fatalf("expected no failover backend open, opens=%d", opens)
		}
		_ = stream.Close()
	})
}
