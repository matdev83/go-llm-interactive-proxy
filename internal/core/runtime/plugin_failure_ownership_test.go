package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// pluginFailStream emits optional committed text then a terminal recv error.
type pluginFailStream struct {
	committed bool
	err       error
	sent      bool
}

func (s *pluginFailStream) Recv(context.Context) (lipapi.Event, error) {
	if !s.sent && s.committed {
		s.sent = true
		return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"}, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return lipapi.Event{}, err
	}
	return lipapi.Event{}, io.EOF
}
func (s *pluginFailStream) Close() error { return nil }
func (s *pluginFailStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeTransport}
}

func TestPreOutput_ClassifiedErrorFailsoverOnce(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, lipapi.RecoverablePreOutputError(errors.New("plugin crash pre-output"))
			},
		},
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "ok"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	col, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text=%q", col.Text.String())
	}
	if opens.Load() != 2 {
		t.Fatalf("opens=%d want 2 (one swallowed pre-output + success)", opens.Load())
	}
}

func TestPostOutput_NoReplayAfterCommit(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"one": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return &pluginFailStream{
					committed: true,
					err:       errors.New("provider fail after commit"),
				}, nil
			},
		},
		"two": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventTextDelta, Delta: "replay"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "one:m|two:m"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ev, err := stream.Recv(context.Background())
	if err != nil || !lipapi.OutputCommitted(ev) {
		t.Fatalf("want committed text: %+v %v", ev, err)
	}
	_, err = stream.Recv(context.Background())
	if err == nil {
		t.Fatal("expected post-output failure")
	}
	if lipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-output must not be recoverable for host/core replay")
	}
	if opens.Load() != 1 {
		t.Fatalf("host/core must not open second attempt after commit: opens=%d", opens.Load())
	}
}

func TestCrash_PreOutputInvalidatesOnlyThatAttempt(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.MaxAttempts = 3
	ex.Backends = map[string]execbackend.Backend{
		"crash": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return &pluginFailStream{err: lipapi.RecoverablePreOutputError(errors.New("crash"))}, nil
			},
		},
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "alive"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "crash:m|ok:m"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	col, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "alive" {
		t.Fatalf("%q", col.Text.String())
	}
	if opens.Load() < 2 {
		t.Fatalf("expected crash attempt then later core-selected open, opens=%d", opens.Load())
	}
}
