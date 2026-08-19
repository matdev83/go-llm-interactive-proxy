package compactioncompose

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	corecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

func TestParentPortUsesTrustedParentForDetachedChild(t *testing.T) {
	port := newTestPort(t)
	parentCtx := execctx.WithViews(context.Background(), execctx.Views{
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("principal-parent")},
		Session:   session.SessionView{AuthoritativeSessionID: "session-parent", ALegID: "a-parent"},
		Principal: execview.PrincipalView{ID: "principal-parent"},
	})
	ctx := execctx.WithDetachedSession(parentCtx, execctx.DetachedSession{ParentSessionID: "session-parent", ParentALegID: "a-parent", ParentTraceID: "trace-parent"})
	parent, err := port.CaptureMeta(ctx, compaction.PreservationMeta{SessionID: "session-child", ALegID: "a-child", TraceID: "trace-child"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := corecontinuity.NewBranchKey("session-parent", "a-parent", "principal-parent")
	if err != nil {
		t.Fatal(err)
	}
	if parent.Binding != want.Binding() || parent.ALegID != "a-parent" || parent.TraceID != "trace-parent" {
		t.Fatalf("parent=%+v", parent)
	}
}

func TestParentPortRejectsUntrustedMetadata(t *testing.T) {
	port := newTestPort(t)
	_, err := port.CaptureMeta(context.Background(), compaction.PreservationMeta{SessionID: "forged", ALegID: "forged-a"})
	if !errors.Is(err, corecontinuity.ErrInvalidBranchKey) {
		t.Fatalf("err=%v", err)
	}
}

func TestParentPortCASAndBoundaryState(t *testing.T) {
	port := newTestPort(t)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Scope:   scope.PrincipalScopeView{PrincipalID: scope.Known("principal-boundary")},
		Session: session.SessionView{AuthoritativeSessionID: "session-boundary", ALegID: "a-boundary"},
	})
	parent, err := port.CaptureMeta(ctx, compaction.PreservationMeta{SessionID: "session-boundary", ALegID: "a-boundary"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := port.CommitCapsule(ctx, parent, 0, []byte(`{"capsule":1}`), [32]byte{1}, "wm")
	if err != nil || state.Revision != 1 {
		t.Fatalf("capsule=%+v err=%v", state, err)
	}
	state, err = port.RecordPreviewIntent(ctx, parent, featurecontinuity.PreviewIntent{Key: "intent", TargetSourceRevision: 1})
	if err != nil || state.PendingPreviewIntent == nil {
		t.Fatalf("preview=%+v err=%v", state, err)
	}
	if _, err := port.BindPreviewIntent(ctx, parent, "intent", ""); !errors.Is(err, corecontinuity.ErrInvalidTransaction) {
		t.Fatalf("empty tx=%v", err)
	}
	state, err = port.BindPreviewIntent(ctx, parent, "intent", "tx-1")
	if err != nil || state.LastCompactionTransaction != "tx-1" {
		t.Fatalf("bound=%+v err=%v", state, err)
	}
	for _, boundary := range []string{"boundary-a", "boundary-b"} {
		target := featurecontinuity.InjectionTarget{BoundaryKey: boundary, CapsuleRevision: 1}
		state, err = port.SetPendingInjection(ctx, parent, target)
		if err != nil || state.Revision != 1 || state.PendingInjection == nil {
			t.Fatalf("pending=%s state=%+v err=%v", boundary, state, err)
		}
		if _, err = port.ValidateInjection(ctx, parent, target); err != nil {
			t.Fatal(err)
		}
		state, err = port.CommitReleasedInjection(ctx, parent, featurecontinuity.InjectionWatermark{BranchBinding: parent.Binding, BoundaryKey: boundary, CapsuleRevision: 1})
		if err != nil || state.LastReleasedInjection == nil || state.LastReleasedInjection.BoundaryKey != boundary {
			t.Fatalf("release=%s state=%+v err=%v", boundary, state, err)
		}
	}
	wrong := parent
	wrong.Binding = "sha256:wrong"
	if _, err := port.SetPendingInjection(ctx, wrong, featurecontinuity.InjectionTarget{BoundaryKey: "wrong", CapsuleRevision: 1}); !errors.Is(err, corecontinuity.ErrBranchMismatch) {
		t.Fatalf("wrong binding=%v", err)
	}
}

func TestParentPortPropagatesCancellationToBlockingStatePersistence(t *testing.T) {
	t.Parallel()

	store := newBlockingPutStore()
	coordinator, err := corecontinuity.NewBranchCoordinator(context.Background(), corecontinuity.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewCompactionContinuityParentPort(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	base := execctx.WithViews(context.Background(), execctx.Views{
		Scope:   scope.PrincipalScopeView{PrincipalID: scope.Known("principal-cancel")},
		Session: session.SessionView{AuthoritativeSessionID: "session-cancel", ALegID: "a-cancel"},
	})
	ctx, cancel := context.WithCancel(base)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		_, callErr := port.CaptureMeta(ctx, compaction.PreservationMeta{SessionID: "session-cancel", ALegID: "a-cancel"})
		result <- callErr
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("CaptureMeta did not reach state persistence")
	}
	cancel()

	select {
	case callErr := <-result:
		if !errors.Is(callErr, context.Canceled) {
			t.Fatalf("CaptureMeta error = %v, want context.Canceled", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("CaptureMeta did not unblock after request cancellation")
	}
	if _, found, err := coordinator.Snapshot(context.Background(), mustBranchKey(t, "session-cancel", "a-cancel")); err != nil || found {
		t.Fatalf("canceled capture published state: found=%v err=%v", found, err)
	}
	store.mu.Lock()
	writes := store.writes
	store.mu.Unlock()
	if writes != 0 {
		t.Fatalf("canceled capture recorded %d store writes, want 0", writes)
	}
}

func mustBranchKey(t *testing.T, sessionID, aLegID string) corecontinuity.BranchKey {
	t.Helper()
	key, err := corecontinuity.NewBranchKey(sessionID, aLegID, "principal-cancel")
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type blockingPutStore struct {
	started   chan struct{}
	startOnce sync.Once
	mu        sync.Mutex
	value     any
	writes    int
}

func newBlockingPutStore() *blockingPutStore {
	return &blockingPutStore{started: make(chan struct{})}
}

func (s *blockingPutStore) Get(_ context.Context, _ lipstate.Scope, _, _ string, out any) (bool, error) {
	s.mu.Lock()
	value := s.value
	s.mu.Unlock()
	if value == nil {
		return false, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, out)
}

func (s *blockingPutStore) Put(ctx context.Context, _ lipstate.Scope, _, _ string, value any, _ time.Duration) error {
	s.startOnce.Do(func() { close(s.started) })
	<-ctx.Done()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.value = value
	s.writes++
	s.mu.Unlock()
	return nil
}

func (s *blockingPutStore) Delete(context.Context, lipstate.Scope, string, string) error {
	return nil
}

func (s *blockingPutStore) InspectTTL(context.Context, lipstate.Scope, string, string) (time.Duration, bool, error) {
	return 0, false, nil
}

func newTestPort(t *testing.T) *CompactionContinuityParentPort {
	t.Helper()
	coordinator, err := corecontinuity.NewBranchCoordinator(context.Background(), corecontinuity.Config{})
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewCompactionContinuityParentPort(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
