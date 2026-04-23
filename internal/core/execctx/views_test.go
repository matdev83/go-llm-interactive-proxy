package execctx_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func TestFromContext_missing(t *testing.T) {
	t.Parallel()
	_, ok := execctx.FromContext(context.Background())
	if ok {
		t.Fatal("want no views without WithViews")
	}
}

func TestFromContext_nilContext(t *testing.T) {
	t.Parallel()
	_, ok := execctx.FromContext(nil) //nolint:staticcheck // SA1012: intentional nil context contract
	if ok {
		t.Fatal("want false for nil context")
	}
}

func TestWithViews_nilParent(t *testing.T) {
	t.Parallel()
	ctx := execctx.WithViews(nil, execctx.Views{}) //nolint:staticcheck // SA1012: intentional nil parent contract
	if ctx != nil {
		t.Fatalf("want nil parent unchanged, got %v", ctx)
	}
}

func TestWithViews_roundTrip(t *testing.T) {
	t.Parallel()
	want := execctx.Views{
		Principal: execview.PrincipalView{
			ID: "u1", DisplayName: "User",
			Roles:  []string{"admin"},
			Claims: map[string]string{"tenant": "a"},
		},
		Session: session.SessionView{
			SessionID: "s1", ALegID: "a1", IsNew: true,
			Labels: map[string]string{"k": "v"},
		},
		Attempt: execview.AttemptView{
			TraceID: "tr", BLegID: "b1", AttemptSeq: 2,
			BackendID: "openai", RouteRole: "primary",
		},
		Workspace: workspace.WorkspaceView{
			ProjectRoot: "/repo", DirtyTree: true,
			Markers: []string{"go.mod"},
			Labels:  map[string]string{"kind": "git"},
		},
		Annotations: map[string]string{"note": "x"},
	}
	ctx := execctx.WithViews(context.Background(), want)
	got, ok := execctx.FromContext(ctx)
	if !ok {
		t.Fatal("want views present")
	}
	if got.Principal.ID != want.Principal.ID || got.Session.SessionID != want.Session.SessionID {
		t.Fatalf("principal/session mismatch: %+v vs %+v", got, want)
	}
	if got.Attempt.BLegID != want.Attempt.BLegID || got.Workspace.ProjectRoot != want.Workspace.ProjectRoot {
		t.Fatalf("attempt/workspace mismatch")
	}
	if got.Annotations["note"] != "x" {
		t.Fatalf("annotations: %v", got.Annotations)
	}
}

func TestWithViews_mapIsolation(t *testing.T) {
	t.Parallel()
	ann := map[string]string{"a": "1"}
	ctx := execctx.WithViews(context.Background(), execctx.Views{Annotations: ann})
	ann["a"] = "mutated"
	got, _ := execctx.FromContext(ctx)
	if got.Annotations["a"] != "1" {
		t.Fatalf("context annotations should be a copy, got %q", got.Annotations["a"])
	}
}
