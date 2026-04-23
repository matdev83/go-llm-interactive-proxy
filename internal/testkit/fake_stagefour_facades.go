package testkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	corestate "github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// FakeStateStore is an in-memory [lipstate.Store] backed by the core mem implementation for
// deterministic TTL and scope behavior (tasks 4.3, 6, 6.1).
type FakeStateStore struct {
	lipstate.Store
}

// NewFakeStateStore returns a fake store using wall clock time.
func NewFakeStateStore() *FakeStateStore {
	return &FakeStateStore{Store: corestate.NewMem(time.Now)}
}

// NewFakeStateStoreAt returns a fake store with an injectable clock (deterministic TTL tests).
func NewFakeStateStoreAt(now func() time.Time) *FakeStateStore {
	if now == nil {
		now = time.Now
	}
	return &FakeStateStore{Store: corestate.NewMem(now)}
}

// FakeAuxClient returns empty results without calling a backend (task 4.3).
type FakeAuxClient struct{}

func (FakeAuxClient) Collect(_ context.Context, req auxiliary.Request) (lipapi.Collected, error) {
	if req.Call == nil {
		return lipapi.Collected{}, fmt.Errorf("testkit.FakeAuxClient: nil Call")
	}
	return lipapi.Collected{}, nil
}

func (FakeAuxClient) Stream(_ context.Context, req auxiliary.Request) (lipapi.EventStream, error) {
	if req.Call == nil {
		return nil, fmt.Errorf("testkit.FakeAuxClient: nil Call")
	}
	return lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}

// FakeWorkspaceResolver returns a fixed [workspace.WorkspaceView] (task 4.3).
type FakeWorkspaceResolver struct {
	View workspace.WorkspaceView
	Err  error
}

func (f FakeWorkspaceResolver) Resolve(context.Context) (workspace.WorkspaceView, error) {
	if f.Err != nil {
		return workspace.WorkspaceView{}, f.Err
	}
	return f.View, nil
}

// RecordingTrafficObserver appends observations for assertions (task 4.3).
type RecordingTrafficObserver struct {
	mu   sync.Mutex
	Seen []traffic.Observation
}

func (r *RecordingTrafficObserver) OnObservation(_ context.Context, ev traffic.Observation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Seen = append(r.Seen, ev)
	return nil
}

// RecordingRawCaptureSink records privileged raw writes (task 10.1).
type RecordingRawCaptureSink struct {
	mu   sync.Mutex
	Seen []traffic.CaptureMeta
	Data [][]byte
}

func (r *RecordingRawCaptureSink) WriteRaw(_ context.Context, leg traffic.Leg, meta traffic.CaptureMeta, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta2 := meta
	r.Seen = append(r.Seen, meta2)
	r.Data = append(r.Data, append([]byte(nil), payload...))
	_ = leg
	return nil
}
