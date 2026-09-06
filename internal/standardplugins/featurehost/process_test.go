package featurehost_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

type dummyRunner struct{}

func (dummyRunner) Execute(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	return lipapi.NewFixedEventStream(nil), nil
}

func newTestScheduler(t *testing.T) *auxreq.BackgroundScheduler {
	t.Helper()
	s, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return dummyRunner{}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}
	return s
}

func TestProcess_SuccessfulClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	in := featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	}

	rt, err := featurehost.NewProcess(ctx, in)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil Runtime")
	}

	if rt.Closed() {
		t.Fatal("expected runtime not closed initially")
	}

	if err := rt.Close(); err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}

	if !rt.Closed() {
		t.Fatal("expected runtime closed after Close()")
	}
}

func TestProcess_CloseIdempotency(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	in := featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	}

	rt, err := featurehost.NewProcess(ctx, in)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	// Close twice: second Close must be idempotent and error-free.
	if err := rt.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !rt.Closed() {
		t.Fatal("expected runtime closed after Close()")
	}
}

func TestProcess_BorrowedResourcesNotClosed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	in := featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	}

	rt, err := featurehost.NewProcess(ctx, in)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	if err := rt.Close(); err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}

	// sched must NOT be closed by Runtime.Close()
	// SubmitCollect must succeed and not return ErrSchedulerClosed.
	_, err = sched.SubmitCollect(ctx, auxiliary.Request{Call: &lipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "test-key"})
	if errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatal("borrowed BackgroundAux was closed by Runtime.Close()")
	}
}

func TestProcess_NilLoggerRejected(t *testing.T) {
	t.Parallel()

	_, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{
		Logger: nil,
	})
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
}

func TestProcess_DoesNotImplementIoCloserOnProcessInput(t *testing.T) {
	t.Parallel()

	in := featurehost.ProcessInput{}
	if _, ok := any(in).(io.Closer); ok {
		t.Fatal("ProcessInput must not implement io.Closer")
	}
}
