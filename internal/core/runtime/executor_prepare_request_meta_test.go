package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// sessionMetaSpy records SessionView seen by request-wide transforms (extension RequestMeta).
type sessionMetaSpy struct {
	seen []session.SessionView
}

func (s *sessionMetaSpy) ID() string                        { return "test-session-meta-spy" }
func (s *sessionMetaSpy) Order() int                        { return 0 }
func (s *sessionMetaSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (s *sessionMetaSpy) Handle(_ context.Context, _ *lipapi.Call, meta request.RequestMeta, _ request.Services) error {
	if s == nil {
		return nil
	}
	s.seen = append(s.seen, meta.Session)
	return nil
}

type prereqOrderSpy struct {
	order *[]string
}

func (s prereqOrderSpy) ID() string                        { return "test-prereq-order" }
func (s prereqOrderSpy) Order() int                        { return 0 }
func (s prereqOrderSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (s prereqOrderSpy) Handle(_ context.Context, call *lipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	*s.order = append(*s.order, "pre")
	call.Route.Selector = "set-by-pre"
	return prerequest.Allow(), nil
}

type routeHintOrderSpy struct {
	order *[]string
	route string
}

func (s *routeHintOrderSpy) ID() string                        { return "test-route-hint-order" }
func (s *routeHintOrderSpy) Order() int                        { return 0 }
func (s *routeHintOrderSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s *routeHintOrderSpy) Hint(_ context.Context, in routehint.Input) (routehint.Result, error) {
	*s.order = append(*s.order, "hint")
	if in.Call != nil {
		s.route = in.Call.Route.Selector
	}
	return routehint.Result{}, nil
}

func TestExecutor_prepareSubmitAndALeg_preRequestRunsBeforeRouteHint(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	var order []string
	hint := &routeHintOrderSpy{order: &order}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:          workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
		PreRequestHandlers: []prerequest.Handler{prereqOrderSpy{order: &order}},
		RouteHintProviders: []routehint.Provider{hint},
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:              b2,
		Bus:                bus,
		RuntimeSnapshot:    snap,
		SecureSession:      mgr,
		Now:                func() time.Time { return time.Unix(4200, 0).UTC() },
		SessionAuditPolicy: testSessionAuditPolicy(),
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-pre-route"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "pre-route-hint"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, baseline, _, _, err := ex.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "pre" || order[1] != "hint" {
		t.Fatalf("order: %#v", order)
	}
	if hint.route != "set-by-pre" {
		t.Fatalf("route hint saw route %q", hint.route)
	}
	if baseline.Route.Selector != "set-by-pre" {
		t.Fatalf("baseline route selector: %q", baseline.Route.Selector)
	}
}

func TestExecutor_prepareSubmitAndALeg_requestMetaSession_propagatesIsNewForNewTurn(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	spy := &sessionMetaSpy{}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:         workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
		RequestTransforms: []request.Transform{spy},
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:              b2,
		Bus:                bus,
		RuntimeSnapshot:    snap,
		SecureSession:      mgr,
		Now:                func() time.Time { return time.Unix(4000, 0).UTC() },
		SessionAuditPolicy: testSessionAuditPolicy(),
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-meta-new"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "meta-new-hint"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.seen) != 1 {
		t.Fatalf("transform runs: want 1 got %d", len(spy.seen))
	}
	got := spy.seen[0]
	if !got.IsNew {
		t.Fatalf("new turn: want SessionView.IsNew true got %+v", got)
	}
	if !got.ResumeEligible {
		t.Fatalf("new turn: want SessionView.ResumeEligible true got %+v", got)
	}
	if got.AuthoritativeSessionID == "" {
		t.Fatal("want AuthoritativeSessionID set for extension meta")
	}
	if got.TurnID == "" {
		t.Fatal("want TurnID set for extension meta")
	}
}

func TestExecutor_prepareSubmitAndALeg_requestMetaSession_resumeTurnIsNotNew(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	spy := &sessionMetaSpy{}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:         workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
		RequestTransforms: []request.Transform{spy},
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:              b2,
		Bus:                bus,
		RuntimeSnapshot:    snap,
		SecureSession:      mgr,
		Now:                func() time.Time { return time.Unix(4100, 0).UTC() },
		SessionAuditPolicy: testSessionAuditPolicy(),
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-meta-resume"})
	call1 := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "meta-resume-hint"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("first")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, bus, call1)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.seen) != 1 || !spy.seen[0].IsNew {
		t.Fatalf("first turn meta: %+v", spy.seen)
	}
	resumeTok := call1.Session.ResumeToken
	if resumeTok == "" {
		t.Fatal("want resume token after first turn")
	}
	spy.seen = spy.seen[:0]
	call2 := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID:        "meta-resume-hint",
			AuthoritativeSessionID: call1.Session.AuthoritativeSessionID,
			ResumeToken:            resumeTok,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("second")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, bus, call2)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.seen) != 1 {
		t.Fatalf("second prepare transform runs: want 1 got %d (%+v)", len(spy.seen), spy.seen)
	}
	if spy.seen[0].IsNew {
		t.Fatalf("resume turn: want SessionView.IsNew false got %+v", spy.seen[0])
	}
}
