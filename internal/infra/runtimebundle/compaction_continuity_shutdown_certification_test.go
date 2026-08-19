package runtimebundle_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

func TestCompactionContinuityShutdownCertification_ProcessOwnsAndClosesResources(t *testing.T) {
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), nil, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:           processServicesCoordinatorConfig(),
		Log:           testkit.DiscardLogger(),
		Opts:          &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		BackgroundAux: scheduler,
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.BackgroundAux != scheduler {
		t.Fatal("ProcessServices did not adopt the supplied process scheduler")
	}
	if ps.BranchCoordinator == nil {
		t.Fatal("ProcessServices did not construct the process branch coordinator")
	}

	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close is not idempotent: %v", err)
	}
	_, err = scheduler.SubmitCollect(context.Background(), auxiliary.Request{Call: &lipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "after-process-close"})
	if !errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatalf("submission after ProcessServices.Close=%v want ErrSchedulerClosed", err)
	}
}

func TestCompactionContinuityShutdownCertification_BranchAndPreviewBounds(t *testing.T) {
	clock := &certificationClock{now: time.Unix(100, 0)}
	coordinator, err := compactioncontinuity.NewBranchCoordinator(context.Background(), compactioncontinuity.Config{
		MaxEntries:        2,
		MaxPreviewIntents: 1,
		TTL:               time.Minute,
		Now:               clock.Now,
	})
	if err != nil {
		t.Fatalf("NewBranchCoordinator: %v", err)
	}
	first := certificationBranchKey("first")
	second := certificationBranchKey("second")
	third := certificationBranchKey("third")
	if _, err := coordinator.Capture(context.Background(), first); err != nil {
		t.Fatalf("Capture first: %v", err)
	}
	if _, err := coordinator.Capture(context.Background(), second); err != nil {
		t.Fatalf("Capture second: %v", err)
	}
	if _, err := coordinator.Capture(context.Background(), third); !errors.Is(err, compactioncontinuity.ErrBranchLimit) {
		t.Fatalf("third branch error=%v want ErrBranchLimit", err)
	}
	if _, err := coordinator.RecordPreviewIntent(context.Background(), first, compactioncontinuity.PreviewIntent{Key: "preview-first", TargetSourceRevision: 1}); err != nil {
		t.Fatalf("RecordPreviewIntent first: %v", err)
	}
	if _, err := coordinator.RecordPreviewIntent(context.Background(), second, compactioncontinuity.PreviewIntent{Key: "preview-second", TargetSourceRevision: 1}); !errors.Is(err, compactioncontinuity.ErrPreviewIntentLimit) {
		t.Fatalf("second preview intent error=%v want ErrPreviewIntentLimit", err)
	}

	// TTL cleanup is lazy and bounded; no cleanup goroutine is needed to make
	// room for a subsequent branch.
	clock.Advance(2 * time.Minute)
	if _, found, err := coordinator.Snapshot(context.Background(), first); err != nil || found {
		t.Fatalf("expired first branch snapshot found=%v err=%v", found, err)
	}
	if _, err := coordinator.Capture(context.Background(), third); err != nil {
		t.Fatalf("Capture after bounded expiry: %v", err)
	}
}

type certificationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *certificationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *certificationClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func certificationBranchKey(id string) compactioncontinuity.BranchKey {
	key, err := compactioncontinuity.NewBranchKey("session-certification", "a-"+id, "principal-certification")
	if err != nil {
		panic(err)
	}
	return key
}
