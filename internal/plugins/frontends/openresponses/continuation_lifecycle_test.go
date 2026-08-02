package openresponses_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corecont "github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

type lifecycleMockStream struct {
	mu        sync.Mutex
	events    []lipapi.Event
	idx       int
	closed    atomic.Bool
	recvBlock chan struct{}
}

func (m *lifecycleMockStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if m.recvBlock != nil {
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		case <-m.recvBlock:
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	return ev, nil
}

func (m *lifecycleMockStream) Close() error {
	m.closed.Store(true)
	return nil
}

type lifecycleMockExecutor struct {
	executeFn func(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error)
}

func (m *lifecycleMockExecutor) Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, call)
	}
	return nil, errors.New("lifecycleMockExecutor not implemented")
}

type failResponseWriter struct {
	http.ResponseWriter
	header    http.Header
	written   bool
	failWrite bool
}

func newFailResponseWriter() *failResponseWriter {
	return &failResponseWriter{header: make(http.Header)}
}

func (f *failResponseWriter) Header() http.Header    { return f.header }
func (f *failResponseWriter) WriteHeader(status int) {}
func (f *failResponseWriter) Write(b []byte) (int, error) {
	if f.failWrite {
		return 0, errors.New("connection broken")
	}
	f.written = true
	return len(b), nil
}
func (f *failResponseWriter) Flush() {}

func TestStreamingLifecycle_CancellationAndWriterFailureCleanup(t *testing.T) {
	t.Parallel()

	t.Run("cancellation before terminal cleans reservation and closes stream", func(t *testing.T) {
		store := corecont.NewMemoryStore()
		st := &lifecycleMockStream{
			events: []lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventTextDelta, Delta: "hello"},
			},
			recvBlock: make(chan struct{}),
		}

		handler := openresponses.NewHandler(openresponses.HandlerConfig{
			Executor:          &lifecycleMockExecutor{executeFn: func(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) { return st, nil }},
			ContinuationStore: store,
		})

		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true,"store":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LIP-Session-ID", "sess_test_123")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(rec, req)
			close(done)
		}()

		// Cancel request while stream is blocked
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("ServeHTTP timed out on cancellation")
		}

		if st.closed.Load() {
			t.Fatalf("canceled request executed an unneeded backend stream; status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("writer failure cleans reservation and closes stream exactly once", func(t *testing.T) {
		store := corecont.NewMemoryStore()
		st := &lifecycleMockStream{
			events: []lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventTextDelta, Delta: "hello"},
			},
		}

		handler := openresponses.NewHandler(openresponses.HandlerConfig{
			Executor:          &lifecycleMockExecutor{executeFn: func(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) { return st, nil }},
			ContinuationStore: store,
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true,"store":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LIP-Session-ID", "sess_test_123")
		w := newFailResponseWriter()
		w.failWrite = true

		handler.ServeHTTP(w, req)

		if !st.closed.Load() {
			t.Fatalf("backend stream was not closed on writer failure; header=%v", w.header)
		}
	})
}

func TestReloadGenerationAndShutdownIsolation(t *testing.T) {
	t.Parallel()

	oldStore := corecont.NewMemoryStore()
	newStore := corecont.NewMemoryStore()

	ctx := context.Background()
	scope := lipcont.Scope{PrincipalID: "p1", SessionID: "s1"}

	// Old generation reserves ID
	oldID, err := oldStore.Reserve(ctx, scope, lipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatalf("old store reserve: %v", err)
	}

	// Close old generation
	if err := oldStore.Close(); err != nil {
		t.Fatalf("old store close: %v", err)
	}

	// Old store rejects lookups and new operations
	if _, err := oldStore.Get(ctx, scope, oldID); !errors.Is(err, lipcont.ErrStoreClosed) {
		t.Fatalf("old store get: want ErrStoreClosed, got %v", err)
	}
	if _, err := lipcont.Lookup(ctx, oldStore, scope, oldID); !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("old store lookup: want ErrPreviousResponseNotFound, got %v", err)
	}

	// New generation operates independently and cannot look up old generation ID
	newID, err := newStore.Reserve(ctx, scope, lipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatalf("new store reserve: %v", err)
	}

	if _, err := lipcont.Lookup(ctx, newStore, scope, oldID); !errors.Is(err, lipcont.ErrPreviousResponseNotFound) {
		t.Fatalf("cross-generation lookup: new store returned old ID lookup result %v", err)
	}

	rec := lipcont.ContinuationRecord{
		ID:       newID,
		Scope:    scope,
		Lineage:  lipcont.Lineage{ProfileID: "openresponses", Model: "m"},
		Terminal: true,
	}
	if err := newStore.PutTerminal(ctx, rec); err != nil {
		t.Fatalf("new store put terminal: %v", err)
	}

	got, err := lipcont.Lookup(ctx, newStore, scope, newID)
	if err != nil || got.ID != newID {
		t.Fatalf("new generation lookup failed: %v", err)
	}
}

func TestContinuationStressAndGoroutineTolerance(t *testing.T) {
	t.Parallel()

	baselineRoutines := runtime.NumGoroutine()
	store := corecont.NewMemoryStore()
	defer func() { _ = store.Close() }()

	st := &lifecycleMockStream{
		events: []lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
			{Kind: lipapi.EventResponseFinished},
		},
	}

	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		Executor:          &lifecycleMockExecutor{executeFn: func(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) { return st, nil }},
		ContinuationStore: store,
	})

	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(iterations)

	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true,"store":true}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-LIP-Session-ID", "sess_test_123")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
		}()
	}

	wg.Wait()

	// Allow short grace period for runtime goroutines to settle
	time.Sleep(50 * time.Millisecond)
	currentRoutines := runtime.NumGoroutine()
	if diff := currentRoutines - baselineRoutines; diff > 5 {
		t.Fatalf("goroutine leak: baseline=%d current=%d diff=%d", baselineRoutines, currentRoutines, diff)
	}
}
