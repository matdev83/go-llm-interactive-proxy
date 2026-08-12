package runtimehost_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

type publishedStartPlane struct {
	starts atomic.Int32
}

func (p *publishedStartPlane) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (p *publishedStartPlane) Close() error { return nil }

func (p *publishedStartPlane) Quiesce(context.Context) error { return nil }

func (p *publishedStartPlane) StartPublished(context.Context) error {
	p.starts.Add(1)
	return nil
}

func TestManager_PublishStartsPublishedWorkAfterSwap(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	plane := &publishedStartPlane{}
	g := m.PrepareRequestPlane("boot", plane)
	if plane.starts.Load() != 0 {
		t.Fatal("StartPublished ran before Publish")
	}
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	if plane.starts.Load() != 1 {
		t.Fatalf("starts=%d want 1 after Publish", plane.starts.Load())
	}
}

func TestManager_RejectedPublishDoesNotStartPublishedWork(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	boot := m.PrepareRequestPlane("boot", &publishedStartPlane{})
	if err := m.Publish(boot); err != nil {
		t.Fatal(err)
	}
	m.BeginShutdown()
	plane := &publishedStartPlane{}
	g := m.PrepareRequestPlane("late", plane)
	if err := m.Publish(g); !errors.Is(err, runtimehost.ErrHostShuttingDown) {
		t.Fatalf("Publish after shutdown: %v", err)
	}
	if plane.starts.Load() != 0 {
		t.Fatalf("rejected candidate started published work: %d", plane.starts.Load())
	}
}
