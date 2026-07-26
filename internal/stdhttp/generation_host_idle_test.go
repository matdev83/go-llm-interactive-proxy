package stdhttp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

type orderCoord struct {
	begin   atomic.Int32
	idle    atomic.Int32
	idleErr error
	block   chan struct{}
}

func (c *orderCoord) BeginShutdown() { c.begin.Add(1) }
func (c *orderCoord) WaitForIdle(ctx context.Context) error {
	c.idle.Add(1)
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.idleErr
}

type nopGenCloser struct{}

func (nopGenCloser) Close() error { return nil }

//nolint:paralleltest // mutates package-level closeProcessServices
func TestShutdownGenerationHost_AwaitsIdleBeforeProcessClose(t *testing.T) {
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("g1", nopGenCloser{})
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}

	var processAt atomic.Int32
	coord := &orderCoord{}
	orig := closeProcessServices
	closeProcessServices = func(*runtimebundle.ProcessServices) error {
		processAt.Store(int32(coord.idle.Load()))
		return nil
	}
	t.Cleanup(func() { closeProcessServices = orig })

	err := shutdownGenerationHost(context.Background(), GenerationHostInput{
		Manager:     m,
		Process:     &runtimebundle.ProcessServices{},
		Coordinator: coord,
	}, time.Second)
	if err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if coord.begin.Load() != 1 {
		t.Fatalf("BeginShutdown calls=%d", coord.begin.Load())
	}
	if coord.idle.Load() != 1 {
		t.Fatalf("WaitForIdle calls=%d", coord.idle.Load())
	}
	if processAt.Load() != 1 {
		t.Fatal("ProcessServices.Close must run only after WaitForIdle")
	}
}

//nolint:paralleltest // mutates package-level closeProcessServices
func TestShutdownGenerationHost_SkipsProcessCloseWhenIdleTimesOut(t *testing.T) {
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("g1", nopGenCloser{})
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}

	block := make(chan struct{})
	coord := &orderCoord{block: block}
	var processCloses atomic.Int32
	orig := closeProcessServices
	closeProcessServices = func(*runtimebundle.ProcessServices) error {
		processCloses.Add(1)
		return nil
	}
	t.Cleanup(func() {
		close(block)
		closeProcessServices = orig
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := shutdownGenerationHost(ctx, GenerationHostInput{
		Manager:     m,
		Process:     &runtimebundle.ProcessServices{},
		Coordinator: coord,
	}, 30*time.Millisecond)
	if err == nil {
		t.Fatal("expected idle timeout")
	}
	if processCloses.Load() != 0 {
		t.Fatalf("process closes=%d want 0 while candidate idle wait fails", processCloses.Load())
	}
}
