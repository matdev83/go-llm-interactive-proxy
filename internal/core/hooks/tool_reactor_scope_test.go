package hooks_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// TestApplyToolReactors_PropagatesScopeFromExecctx asserts the runner copies the
// authoritative Scope (and Principal/Session/Workspace) from the request-scoped
// execctx views onto hooks.ToolMeta so stream-stage reactors see proxy-validated
// attribution. This fails if ApplyToolReactors does not set meta.Scope from views.
func TestApplyToolReactors_PropagatesScopeFromExecctx(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	views := execctx.Views{
		Scope: scope.PrincipalScopeView{
			SubjectKind: scope.SubjectHuman,
			PrincipalID: scope.Known("auth-principal"),
		},
		Session:   session.SessionView{AuthoritativeSessionID: "s1"},
		Workspace: workspace.WorkspaceView{ID: "w1"},
	}
	ctx := execctx.WithViews(context.Background(), views)
	var captured sdk.ToolMeta
	b := corehooks.New(corehooks.Config{
		ToolReactors: []sdk.ToolReactor{
			&stubTool{id: "r-capture", order: 1, fn: func(_ context.Context, _ lipapi.ToolEvent, meta sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
				captured = meta
				return sdk.ToolPass, lipapi.ToolEvent{}, nil
			}},
		},
	})
	out := b.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
	if !out.Emit {
		t.Fatalf("expected emit, got %#v", out)
	}
	if captured.Scope.PrincipalID.String() != "auth-principal" {
		t.Fatalf("ToolMeta.Scope.PrincipalID: got %q want auth-principal", captured.Scope.PrincipalID.String())
	}
	if captured.Session.AuthoritativeSessionID != "s1" {
		t.Fatalf("ToolMeta.Session: got %q want s1", captured.Session.AuthoritativeSessionID)
	}
	if captured.Workspace.ID != "w1" {
		t.Fatalf("ToolMeta.Workspace.ID: got %q want w1", captured.Workspace.ID)
	}
}
