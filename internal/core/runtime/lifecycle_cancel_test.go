package runtime_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutor_CancelALegCancelsActiveBLeg(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inner := newExplicitCancelBlockingStream()
	ex := &runtime.Executor{
		Store:         st,
		Bus:           hooks.New(hooks.Config{}),
		Rand:          routing.NewSeededRng(1),
		ALegLifecycle: leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second}),
		Backends: map[string]execbackend.Backend{
			"managed": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return inner, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "explicit-cancel"},
		Route:   lipapi.RouteIntent{Selector: "managed:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.Recv(context.Background())
		done <- err
	}()
	<-inner.ready

	if err := ex.CancelALeg(context.Background(), lipapi.ALegCancelRequest{ALegID: call.Session.ALegID}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, leglifecycle.ErrALegCanceled) {
			t.Fatalf("Recv after explicit cancel = %v want ErrALegCanceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Recv remained blocked after explicit A-leg cancellation")
	}
	if got, want := inner.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed B-leg calls = %v want %v", got, want)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after stream finished = %v want EOF", err)
	}
}

func TestExecutor_CancelALegCancelsActiveBLegWithDefaultLifecycle(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inner := newExplicitCancelBlockingStream()
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"managed": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return inner, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "default-lifecycle-cancel"},
		Route:   lipapi.RouteIntent{Selector: "managed:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if call.Session.ALegID == "" {
		t.Fatal("executor did not assign A-leg id")
	}

	if err := ex.CancelALeg(context.Background(), lipapi.ALegCancelRequest{ALegID: call.Session.ALegID}); err != nil {
		t.Fatalf("CancelALeg with default lifecycle: %v", err)
	}

	if got, want := inner.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed B-leg calls = %v want %v", got, want)
	}
}

type explicitCancelBlockingStream struct {
	ready  chan struct{}
	closed chan struct{}
	readyO sync.Once
	closeO sync.Once
	mu     sync.Mutex
	log    []string
}

func newExplicitCancelBlockingStream() *explicitCancelBlockingStream {
	return &explicitCancelBlockingStream{ready: make(chan struct{}), closed: make(chan struct{})}
}

func (s *explicitCancelBlockingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	s.readyO.Do(func() { close(s.ready) })
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-s.closed:
		return lipapi.Event{}, leglifecycle.ErrALegCanceled
	}
}

func (s *explicitCancelBlockingStream) Cancel(_ context.Context, cause leglifecycle.CancelCause) leglifecycle.CancelResult {
	s.mu.Lock()
	s.log = append(s.log, "cancel:"+string(cause.Kind))
	s.mu.Unlock()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (s *explicitCancelBlockingStream) Close() error {
	s.closeO.Do(func() { close(s.closed) })
	s.mu.Lock()
	s.log = append(s.log, "close")
	s.mu.Unlock()
	return nil
}

func (s *explicitCancelBlockingStream) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}
