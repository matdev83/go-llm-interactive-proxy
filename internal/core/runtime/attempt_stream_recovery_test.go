package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutorStreamRecovery_postOutputEOFEmitsWarningFinishNoRetry(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := &runtime.Executor{
		Store:          st,
		Bus:            hooks.New(hooks.Config{}),
		Rand:           routing.NewSeededRng(1),
		StreamRecovery: streamrecovery.Config{Enabled: true, EmitWarning: true},
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "partial"},
					}), nil
				},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var kinds []lipapi.EventKind
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		kinds = append(kinds, ev.Kind)
	}
	if opens.Load() != 1 {
		t.Fatalf("opens=%d want 1", opens.Load())
	}
	if len(kinds) < 2 || kinds[len(kinds)-2] != lipapi.EventWarning || kinds[len(kinds)-1] != lipapi.EventResponseFinished {
		t.Fatalf("tail kinds=%v", kinds)
	}
}

func TestExecutorStreamRecovery_preOutputIdleCancelsAndFailsOver(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slow := &idleBlockingStream{}
	var opens atomic.Int32
	ex := &runtime.Executor{
		Store:          st,
		Bus:            hooks.New(hooks.Config{}),
		Rand:           routing.NewSeededRng(1),
		StreamRecovery: streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond},
		Backends: map[string]execbackend.Backend{
			"slow": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return slow, nil
				},
			},
			"fast": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "slow:gpt-4|fast:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if opens.Load() != 2 {
		t.Fatalf("opens=%d want 2", opens.Load())
	}
	if !slow.cancelled.Load() || !slow.closed.Load() {
		t.Fatalf("slow stream cancelled=%v closed=%v, want both true", slow.cancelled.Load(), slow.closed.Load())
	}
}

func TestExecutorStreamRecovery_postOutputIdleEmitsWarningFinishNoRetry(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	idle := &idleAfterEventsStream{events: []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "partial"},
	}}
	var opens atomic.Int32
	ex := &runtime.Executor{
		Store:          st,
		Bus:            hooks.New(hooks.Config{}),
		Rand:           routing.NewSeededRng(1),
		StreamRecovery: streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond, EmitWarning: true},
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return idle, nil
				},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var kinds []lipapi.EventKind
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		kinds = append(kinds, ev.Kind)
	}
	if opens.Load() != 1 {
		t.Fatalf("opens=%d want 1", opens.Load())
	}
	if len(kinds) < 2 || kinds[len(kinds)-2] != lipapi.EventWarning || kinds[len(kinds)-1] != lipapi.EventResponseFinished {
		t.Fatalf("tail kinds=%v", kinds)
	}
	if !idle.cancelled.Load() || !idle.closed.Load() {
		t.Fatalf("idle stream cancelled=%v closed=%v, want both true", idle.cancelled.Load(), idle.closed.Load())
	}
}

type idleBlockingStream struct {
	cancelled atomic.Bool
	closed    atomic.Bool
}

func (s *idleBlockingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}

func (s *idleBlockingStream) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *idleBlockingStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	s.cancelled.Store(true)
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

type idleAfterEventsStream struct {
	events    []lipapi.Event
	i         int
	cancelled atomic.Bool
	closed    atomic.Bool
}

func (s *idleAfterEventsStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}

func (s *idleAfterEventsStream) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *idleAfterEventsStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	s.cancelled.Store(true)
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}
