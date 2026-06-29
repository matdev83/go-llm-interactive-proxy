package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type ignoreCtxBlockingStream struct {
	release chan struct{}
}

func newIgnoreCtxBlockingStream() *ignoreCtxBlockingStream {
	return &ignoreCtxBlockingStream{release: make(chan struct{})}
}

func (s *ignoreCtxBlockingStream) Recv(context.Context) (lipapi.Event, error) {
	<-s.release
	return lipapi.Event{}, io.EOF
}

func (s *ignoreCtxBlockingStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (s *ignoreCtxBlockingStream) Close() error { return nil }

func blockingRecvBackend(track *[]*ignoreCtxBlockingStream, mu *sync.Mutex) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			s := newIgnoreCtxBlockingStream()
			mu.Lock()
			*track = append(*track, s)
			mu.Unlock()
			return s, nil
		},
	}
}

func TestParallelRace_ParentContextCancelReturnsPromptlyWhileLegsBlock(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var (
		streamsMu sync.Mutex
		streams   []*ignoreCtxBlockingStream
	)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"a": blockingRecvBackend(&streams, &streamsMu),
			"b": blockingRecvBackend(&streams, &streamsMu),
		},
		Rand: routing.NewSeededRng(1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := ex.Execute(ctx, parallelCall("a:model!b:model"))
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute blocked past deadline after parent context canceled")
	}

	streamsMu.Lock()
	for _, s := range streams {
		close(s.release)
	}
	streamsMu.Unlock()
	time.Sleep(100 * time.Millisecond)
}
