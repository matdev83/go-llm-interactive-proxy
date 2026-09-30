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
	ctx := execctx.WithViews(nil, execctx.Views{}) //nolint:staticcheck // SA1012: exercise nil-parent hardening
	if ctx == nil {
		t.Fatal("want non-nil context (nil parent uses context.TODO)")
	}
	got, ok := execctx.FromContext(ctx)
	if !ok {
		t.Fatal("want views attached")
	}
	if got.Principal.ID != "" || len(got.Annotations) != 0 {
		t.Fatalf("want empty views, got %+v", got)
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
			AuthoritativeSessionID: "auth-s1",
			ClientSessionHint:      "s1",
			ALegID:                 "a1",
			IsNew:                  true,
			TurnID:                 "turn-1",
			Labels:                 map[string]string{"k": "v"},
			Classification: session.Classification{
				Kind:       session.KindCodingAgent,
				Source:     session.SourceLocalIdentity,
				Confidence: session.ConfidenceHigh,
				Evidence:   session.EvidenceCode("client.codex"),
				Revision:   2,
			},
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
	if got.Principal.ID != want.Principal.ID || got.Session.AuthoritativeSessionID != want.Session.AuthoritativeSessionID || got.Session.ClientSessionHint != want.Session.ClientSessionHint || got.Session.ALegID != want.Session.ALegID || got.Session.IsNew != want.Session.IsNew || got.Session.TurnID != want.Session.TurnID || got.Session.Labels["k"] != want.Session.Labels["k"] || got.Session.Classification != want.Session.Classification {
		t.Fatalf("principal/session mismatch: %+v vs %+v", got, want)
	}
	if got.Attempt.BLegID != want.Attempt.BLegID || got.Workspace.ProjectRoot != want.Workspace.ProjectRoot {
		t.Fatalf("attempt/workspace mismatch")
	}
	if got.Annotations["note"] != "x" {
		t.Fatalf("annotations: %v", got.Annotations)
	}
}

func TestWithViewsSessionClassificationSnapshotsAreIndependent(t *testing.T) {
	t.Parallel()

	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceRemote,
		Confidence: session.ConfidenceHigh,
		Evidence:   session.EvidenceCode("remote.rule_1"),
		Revision:   7,
	}
	input := execctx.Views{Session: session.SessionView{Classification: want}}
	ctx := execctx.WithViews(context.Background(), input)
	input.Session.Classification.Kind = session.KindUnknown

	got, ok := execctx.FromContext(ctx)
	if !ok || got.Session.Classification != want {
		t.Fatalf("stored classification = %+v ok=%v, want %+v", got.Session.Classification, ok, want)
	}
	got.Session.Classification.Evidence = "mutated.internal.snapshot"

	public, ok := session.SessionViewFromContext(ctx)
	if !ok || public.Classification != want {
		t.Fatalf("public classification = %+v ok=%v, want %+v", public.Classification, ok, want)
	}
	public.Classification.Revision++

	gotAgain, ok := execctx.FromContext(ctx)
	if !ok || gotAgain.Session.Classification != want {
		t.Fatalf("classification changed through returned snapshots: %+v ok=%v", gotAgain.Session.Classification, ok)
	}
}

func TestWithViews_mapIsolation(t *testing.T) {
	t.Parallel()
	ann := map[string]string{"a": "1"}
	sessionLabels := map[string]string{"policy": "trusted"}
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Session:     session.SessionView{Labels: sessionLabels},
		Annotations: ann,
	})
	ann["a"] = "mutated"
	sessionLabels["policy"] = "mutated"
	got, _ := execctx.FromContext(ctx)
	if got.Annotations["a"] != "1" {
		t.Fatalf("context annotations should be a copy, got %q", got.Annotations["a"])
	}
	if got.Session.Labels["policy"] != "trusted" {
		t.Fatalf("session labels should be copied on attach, got %q", got.Session.Labels["policy"])
	}
	got.Session.Labels["policy"] = "mutated-again"
	gotAgain, _ := execctx.FromContext(ctx)
	if gotAgain.Session.Labels["policy"] != "trusted" {
		t.Fatalf("session labels should be copied on read, got %q", gotAgain.Session.Labels["policy"])
	}
}

func TestWithViews_ProjectsAuthoritativeSessionToPublicSDKContext(t *testing.T) {
	t.Parallel()

	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Session: session.SessionView{
			AuthoritativeSessionID: "session-authoritative",
			Labels:                 map[string]string{"policy": "trusted"},
		},
	})
	got, ok := session.SessionViewFromContext(ctx)
	if !ok || got.AuthoritativeSessionID != "session-authoritative" || got.Labels["policy"] != "trusted" {
		t.Fatalf("public session view = %+v ok=%v", got, ok)
	}
}

func TestWithViews_DetachedChildMasksParentSessionAuthority(t *testing.T) {
	t.Parallel()

	parent := execctx.WithViews(context.Background(), execctx.Views{
		Session: session.SessionView{AuthoritativeSessionID: "parent-session"},
	})
	child := execctx.WithDetachedSession(parent, execctx.DetachedSession{ParentSessionID: "parent-session"})
	child = execctx.WithViews(child, execctx.Views{Session: session.SessionView{ALegID: "child-a-leg"}})
	got, ok := session.SessionViewFromContext(child)
	if !ok {
		t.Fatal("detached child session view missing")
	}
	if got.AuthoritativeSessionID != "" {
		t.Fatalf("detached child inherited primary session authority: %+v", got)
	}
}

func TestWithViews_DetachedChildMasksPrimarySecureTurnPolicy(t *testing.T) {
	t.Parallel()

	parent := execctx.WithViews(context.Background(), execctx.Views{
		Session: session.SessionView{AuthoritativeSessionID: "parent-session"},
	})
	parent = session.WithSecureTurnPolicy(parent, session.SecureTurnPolicyView{TranscriptEnabled: true})
	child := execctx.WithDetachedSession(parent, execctx.DetachedSession{ParentSessionID: "parent-session"})
	child = execctx.WithViews(child, execctx.Views{Session: session.SessionView{ALegID: "child-a-leg"}})
	if _, ok := session.SecureTurnPolicyFromContext(child); ok {
		t.Fatal("detached child inherited primary secure-turn policy")
	}
}
