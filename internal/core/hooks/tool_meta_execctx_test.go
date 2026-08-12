package hooks_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type metaCaptureReactor struct {
	got sdkhooks.ToolMeta
}

func (m *metaCaptureReactor) ID() string { return "cap" }
func (m *metaCaptureReactor) Order() int { return 0 }
func (m *metaCaptureReactor) HandleToolEvent(ctx context.Context, te lipapi.ToolEvent, meta sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	_ = ctx
	_ = te
	m.got = meta
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

func TestApplyToolReactors_enrichesMetaFromExecctxViews(t *testing.T) {
	t.Parallel()
	capture := &metaCaptureReactor{}
	bus := hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{capture}})
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Session:   session.SessionView{ClientSessionHint: "s1"},
		Workspace: lipworkspace.WorkspaceView{ProjectRoot: "/root"},
	})
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted}
	base := sdkhooks.ToolMeta{TraceID: "t", ALegID: "a", BLegID: "b", AttemptSeq: 1}
	_ = bus.ApplyToolReactors(ctx, te, base)
	if capture.got.Session.ClientSessionHint != "s1" {
		t.Fatalf("session %+v", capture.got.Session)
	}
	if capture.got.Workspace.ProjectRoot != "/root" {
		t.Fatalf("workspace %+v", capture.got.Workspace)
	}
	if capture.got.TraceID != "t" {
		t.Fatalf("trace %q", capture.got.TraceID)
	}
}
