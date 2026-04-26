package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type failingSubmitHook struct{}

func (failingSubmitHook) ID() string                        { return "submit-fail" }
func (failingSubmitHook) Order() int                        { return 0 }
func (failingSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (failingSubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, errors.New("submit boom")
}

type captureSessionOpener struct {
	seen   session.OpenInput
	labels map[string]string
}

func (o *captureSessionOpener) ID() string { return "capture-session" }

func (o *captureSessionOpener) Open(_ context.Context, in session.OpenInput) (session.OpenResult, error) {
	o.seen = in
	return session.OpenResult{SessionLabelUpserts: o.labels}, nil
}

func TestExecutor_prepareSubmitAndALeg_preservesTraceOnSubmitError(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	bus := hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{failingSubmitHook{}},
	})
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:           b2,
		Bus:             bus,
		RuntimeSnapshot: snap,
		SecureSession:   mgr,
		Now:             func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) },
	})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "client-1",
			ContinuityKey:   "ck-1",
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "u1"})
	traceID, _, _, outCtx, err := ex.prepareSubmitAndALeg(ctx, bus, call)
	if err == nil {
		t.Fatal("expected submit error")
	}
	if traceID != "" {
		t.Fatalf("trace id return on error: want empty got %q", traceID)
	}
	if call.ID == "" {
		t.Fatal("expected helper to assign call id")
	}
	if got := diag.TraceID(outCtx); got != call.ID {
		t.Fatalf("returned context trace id: want %q got %q", call.ID, got)
	}
	if got := diag.ALegID(outCtx); got != "" {
		t.Fatalf("returned context aleg id: want empty got %q", got)
	}
}

func TestExecutor_prepareSubmitAndALeg_sessionOpenHintsNotTrustedAsAuthority(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	opener := &captureSessionOpener{
		labels: map[string]string{"opened": "yes"},
	}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		SessionOpeners: []session.Opener{opener},
		Workspace:      workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:           b2,
		RuntimeSnapshot: snap,
		Now:             func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) },
		SecureSession:   mgr,
	})
	bus := hooks.New(hooks.Config{})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "client-2",
			ContinuityKey:   "ck-2",
			ALegID:          "client-forged-aleg",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "u2"})
	traceID, _, aLeg, outCtx, err := ex.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if aLeg.ALegID == "" || aLeg.ALegID == "client-forged-aleg" {
		t.Fatalf("unexpected a-leg %q", aLeg.ALegID)
	}
	if call.Session.ALegID != aLeg.ALegID {
		t.Fatalf("call session aleg id: want %q got %q", aLeg.ALegID, call.Session.ALegID)
	}
	if opener.seen.Session.ClientSessionHint != "client-2" {
		t.Fatalf("opener saw client hint: want client-2 got %q", opener.seen.Session.ClientSessionHint)
	}
	if opener.seen.Session.ALegID != "" {
		t.Fatalf("opener must not see client-provided aleg id before BeginTurn, got %q", opener.seen.Session.ALegID)
	}

	views, ok := execctx.FromContext(outCtx)
	if !ok {
		t.Fatal("expected execctx views on returned context")
	}
	if views.Session.ALegID != aLeg.ALegID {
		t.Fatalf("views aleg id: want %q got %q", aLeg.ALegID, views.Session.ALegID)
	}
	if views.Session.Labels["opened"] != "yes" {
		t.Fatalf("views session labels: %v", views.Session.Labels)
	}
	if got := diag.TraceID(outCtx); got != traceID {
		t.Fatalf("returned context trace id: want %q got %q", traceID, got)
	}
}
