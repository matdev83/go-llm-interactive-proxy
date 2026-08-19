package compactioncompose

import (
	"context"
	"errors"
	"testing"

	corecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
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

func newTestPort(t *testing.T) *CompactionContinuityParentPort {
	t.Helper()
	coordinator, err := corecontinuity.NewBranchCoordinator(corecontinuity.Config{})
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewCompactionContinuityParentPort(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
