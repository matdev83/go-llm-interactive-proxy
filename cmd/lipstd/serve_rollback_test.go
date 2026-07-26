package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

type orderRecorder struct {
	mu    sync.Mutex
	steps []string
}

func (r *orderRecorder) add(step string) {
	r.mu.Lock()
	r.steps = append(r.steps, step)
	r.mu.Unlock()
}

func (r *orderRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.steps))
	copy(out, r.steps)
	return out
}

type recordingMgmt struct {
	rec    *orderRecorder
	closed atomic.Bool
	err    error
}

func (m *recordingMgmt) Shutdown(context.Context) error {
	m.rec.add("mgmt")
	m.closed.Store(true)
	return m.err
}

type recordingHost struct {
	rec *orderRecorder
}

func (h *recordingHost) BeginShutdown() {
	h.rec.add("coordinator")
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

//nolint:paralleltest // mutates package-level rollback hooks
func TestServeStartupRollback_ExactCloseOrder(t *testing.T) {
	rec := &orderRecorder{}
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("g1", &nopCloser{})
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}

	origRetire := retireServeGenerations
	origClose := closeServeProcessServices
	retireServeGenerations = func(ctx context.Context, mgr *runtimehost.Manager) error {
		rec.add("generations")
		return origRetire(ctx, mgr)
	}
	closeServeProcessServices = func(*runtimebundle.ProcessServices) error {
		rec.add("process")
		return nil
	}
	t.Cleanup(func() {
		retireServeGenerations = origRetire
		closeServeProcessServices = origClose
	})

	res := &runtimebundle.BootstrapResult{
		GenerationManager: m,
		ProcessServices:   &runtimebundle.ProcessServices{},
	}
	err := serveStartupRollback(context.Background(), res, &recordingHost{rec: rec}, &recordingMgmt{rec: rec})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got := rec.snapshot()
	want := []string{"coordinator", "generations", "mgmt", "process"}
	if len(got) != len(want) {
		t.Fatalf("steps=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps=%v want %v", got, want)
		}
	}
	if _, ok := m.Acquire(); ok {
		t.Fatal("manager must reject acquire after rollback")
	}
}

//nolint:paralleltest // mutates package-level rollback hooks
func TestServeStartupRollback_SkipsProcessWhileOpenGenerations(t *testing.T) {
	rec := &orderRecorder{}
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("open", &nopCloser{})
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}

	origRetire := retireServeGenerations
	origClose := closeServeProcessServices
	var processCloses atomic.Int32
	retireServeGenerations = func(context.Context, *runtimehost.Manager) error {
		rec.add("generations")
		// Leave generations open (simulates pin/drain incomplete).
		return nil
	}
	closeServeProcessServices = func(*runtimebundle.ProcessServices) error {
		processCloses.Add(1)
		rec.add("process")
		return nil
	}
	t.Cleanup(func() {
		retireServeGenerations = origRetire
		closeServeProcessServices = origClose
		_ = origRetire(context.Background(), m)
	})

	res := &runtimebundle.BootstrapResult{
		GenerationManager: m,
		ProcessServices:   &runtimebundle.ProcessServices{},
	}
	if err := serveStartupRollback(context.Background(), res, &recordingHost{rec: rec}, &recordingMgmt{rec: rec}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if processCloses.Load() != 0 {
		t.Fatalf("process closes=%d want 0 while generations remain open", processCloses.Load())
	}
	got := rec.snapshot()
	want := []string{"coordinator", "generations", "mgmt"}
	if len(got) != len(want) {
		t.Fatalf("steps=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps=%v want %v", got, want)
		}
	}
}

//nolint:paralleltest // mutates package-level rollback hooks
func TestServeStartupRollback_JoinsCleanupErrorWithoutHiding(t *testing.T) {
	rec := &orderRecorder{}
	origRetire := retireServeGenerations
	origClose := closeServeProcessServices
	retireServeGenerations = func(context.Context, *runtimehost.Manager) error {
		rec.add("generations")
		return nil
	}
	closeServeProcessServices = func(*runtimebundle.ProcessServices) error {
		rec.add("process")
		return errors.New("process close boom")
	}
	t.Cleanup(func() {
		retireServeGenerations = origRetire
		closeServeProcessServices = origClose
	})

	m := runtimehost.NewManager(2, nil)
	res := &runtimebundle.BootstrapResult{
		GenerationManager: m,
		ProcessServices:   &runtimebundle.ProcessServices{},
	}
	mgmtErr := errors.New("mgmt close boom")
	err := serveStartupRollback(context.Background(), res, &recordingHost{rec: rec}, &recordingMgmt{rec: rec, err: mgmtErr})
	if err == nil {
		t.Fatal("expected joined cleanup errors")
	}
	if !errors.Is(err, mgmtErr) {
		t.Fatalf("missing mgmt err: %v", err)
	}
	if !strings.Contains(err.Error(), "process close boom") {
		t.Fatalf("missing process err: %v", err)
	}
}
