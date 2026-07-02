package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func testFingerprintKey32(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func testSecureManager(t *testing.T, st app.Store, b2 b2bua.Store) *app.Manager {
	t.Helper()
	m, err := app.NewManager(st, app.NewRandGenerator(testFingerprintKey32(t)), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: testFingerprintKey32(t),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// setSecureSessionDenialMapper applies the production lipapi session-denial shape for tests that
// exercise the secure prepare path (composition root wires the same in runtimebundle).
func setSecureSessionDenialMapper(ex *Executor) *Executor {
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	return ex
}

type voidWS struct{}

func (voidWS) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{}, nil
}

type errWorkspaceResolver struct{}

func (errWorkspaceResolver) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{}, errors.New("workspace resolver unavailable")
}

type countSubmitHook struct{ n *atomic.Int32 }

func (c *countSubmitHook) ID() string                        { return "count_submit" }
func (c *countSubmitHook) Order() int                        { return 0 }
func (c *countSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (c *countSubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	if c.n != nil {
		c.n.Add(1)
	}
	return sdkhooks.SubmitDecision{}, nil
}

func TestPrincipalRefFromScope_usesScopeTenant(t *testing.T) {
	t.Parallel()

	ref := principalRefFromScope(
		execview.PrincipalView{
			ID:     "user-1",
			Claims: map[string]string{"issuer": "issuer-1", "tenant": "legacy-tenant"},
		},
		scope.PrincipalScopeView{TenantID: scope.Known("scope-tenant")},
	)
	if ref.ID != "user-1" {
		t.Fatalf("ID: got %q want user-1", ref.ID)
	}
	if ref.Issuer != "issuer-1" {
		t.Fatalf("Issuer: got %q want issuer-1", ref.Issuer)
	}
	if ref.Tenant != "scope-tenant" {
		t.Fatalf("Tenant: got %q want scope-tenant", ref.Tenant)
	}
}

func TestExecutor_prepareSubmitAndALeg_secure_newSession_replacesForgedALeg(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:           b2,
		Bus:             hooks.New(hooks.Config{}),
		RuntimeSnapshot: snap,
		SecureSession:   mgr,
		Now:             func() time.Time { return time.Unix(1700, 0) },
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "hint",
			ALegID:          "forged-aleg-should-not-stick",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, baseline, aLeg, outCtx, err := ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if aLeg.ALegID == "" || aLeg.ALegID == "forged-aleg-should-not-stick" {
		t.Fatalf("unexpected a-leg %q", aLeg.ALegID)
	}
	if call.Session.ALegID != aLeg.ALegID {
		t.Fatalf("call session aleg: want %q got %q", aLeg.ALegID, call.Session.ALegID)
	}
	if baseline.Session.ResumeToken != "" {
		t.Fatalf("baseline must not carry resume token to backends, got %q", baseline.Session.ResumeToken)
	}
	if call.Session.ResumeToken == "" {
		t.Fatal("expected issued resume token on client call for new secure session")
	}
	attempt := lipapi.CloneCall(baseline)
	if attempt.Session.ResumeToken != "" {
		t.Fatalf("backend attempt must not include resume bearer, got %q", attempt.Session.ResumeToken)
	}
	if attempt.Session.AuthoritativeSessionID != call.Session.AuthoritativeSessionID {
		t.Fatalf("attempt sid %q vs call sid %q", attempt.Session.AuthoritativeSessionID, call.Session.AuthoritativeSessionID)
	}
	v, ok := execctx.FromContext(outCtx)
	if !ok {
		t.Fatal("expected views")
	}
	if v.Session.AuthoritativeSessionID == "" || v.Session.AuthoritativeSessionID == "forged-aleg-should-not-stick" {
		t.Fatalf("authoritative session id: %q", v.Session.AuthoritativeSessionID)
	}
	if v.Session.TurnID == "" {
		t.Fatal("expected turn id on session view")
	}
	st, ok := execctx.SecureSessionTurnFromContext(outCtx)
	if !ok || st.SessionID == "" || st.TurnID == "" {
		t.Fatalf("secure turn binding: ok=%v sid=%q tid=%q", ok, st.SessionID, st.TurnID)
	}
}

func TestExecutor_prepareSubmitAndALeg_secure_requireWorkspaceID_denies(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:                           b2,
		Bus:                             hooks.New(hooks.Config{}),
		RuntimeSnapshot:                 snap,
		SecureSession:                   mgr,
		SecureSessionRequireWorkspaceID: true,
		Now:                             func() time.Time { return time.Unix(1900, 0) },
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "hint"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err == nil {
		t.Fatal("expected error")
	}
	var sd *lipapi.SessionDenialError
	if !errors.As(err, &sd) {
		t.Fatalf("want session denial, got %T %v", err, err)
	}
	if sd.Code() != lipapi.SessionDeniedPolicyUnavailable {
		t.Fatalf("want policy unavailable, got %v", sd.Code())
	}
}

func TestExecutor_prepareSubmitAndALeg_secure_workspaceFailClosed_skipsSubmitHooks(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	var submitCount atomic.Int32
	bus := hooks.New(hooks.Config{SubmitHooks: []sdkhooks.SubmitHook{&countSubmitHook{n: &submitCount}}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewStrictChain([]lipworkspace.Resolver{errWorkspaceResolver{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:                                   b2,
		Bus:                                     bus,
		RuntimeSnapshot:                         snap,
		SecureSession:                           mgr,
		SecureSessionWorkspaceResolveFailClosed: true,
		Now:                                     func() time.Time { return time.Unix(1950, 0) },
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "hint"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial, got %T %v", err, err)
	}
	if submitCount.Load() != 0 {
		t.Fatalf("submit hooks should not run after fail-closed workspace, count=%d", submitCount.Load())
	}
}

func TestExecutor_prepareSubmitAndALeg_secure_invalidResumeIsDenial(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:           b2,
		Bus:             hooks.New(hooks.Config{}),
		RuntimeSnapshot: snap,
		SecureSession:   mgr,
		Now:             func() time.Time { return time.Unix(1800, 0) },
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "hint",
			ResumeToken:     "not-a-valid-resume-token",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial, got %T %v", err, err)
	}
}

func TestExecutor_secureSession_failoverTwoOpens_memoryLatestTraceReflectsSecondModel(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	clock := time.Unix(2000, 0).UTC()
	var capturedAuthoritative string
	ex := setSecureSessionDenialMapper(&Executor{
		Store:           b2,
		Bus:             hooks.New(hooks.Config{}),
		RuntimeSnapshot: snap,
		SecureSession:   mgr,
		Now:             func() time.Time { return clock },
		Rand:            routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"bad": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return nil, lipapi.RecoverablePreOutputError(errors.New("temp"))
				},
			},
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					if v, ok := execctx.FromContext(ctx); ok && v.Session.AuthoritativeSessionID != "" {
						capturedAuthoritative = v.Session.AuthoritativeSessionID
					}
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-trace-2"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "hint-trace-2"},
		Route:   lipapi.RouteIntent{Selector: "bad:g1|ok:g2"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if capturedAuthoritative == "" {
		t.Fatal("expected authoritative session id captured from backend open context")
	}
	rec, err := memSS.LoadByID(context.Background(), domain.SessionID(capturedAuthoritative))
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.LatestAttemptTrace.ResolvedModel; got != "g2" {
		t.Fatalf("latest trace resolved model: want g2 got %q", got)
	}
	if rec.LatestAttemptTrace.AttemptSeq != 2 {
		t.Fatalf("expected second b-leg seq on successful open, got %d", rec.LatestAttemptTrace.AttemptSeq)
	}
}

func TestExecutor_secureSession_Open_recordsOutcomeMemory(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	var capturedAuthoritative string
	ex := setSecureSessionDenialMapper(&Executor{
		Store:           b2,
		Bus:             hooks.New(hooks.Config{}),
		RuntimeSnapshot: snap,
		SecureSession:   mgr,
		Now:             func() time.Time { return time.Unix(2100, 0) },
		Rand:            routing.NewSeededRng(3),
		Backends: map[string]execbackend.Backend{
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					if v, ok := execctx.FromContext(ctx); ok {
						capturedAuthoritative = v.Session.AuthoritativeSessionID
					}
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-outcome"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "hint-o"},
		Route:   lipapi.RouteIntent{Selector: "ok:model-x"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if capturedAuthoritative == "" {
		t.Fatal("expected authoritative session id")
	}
	rec, err := memSS.LoadByID(context.Background(), domain.SessionID(capturedAuthoritative))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.LatestAttemptOutcome.Success || rec.LatestAttemptOutcome.SurfaceState != domain.SurfaceSurfaced {
		t.Fatalf("outcome: %#v", rec.LatestAttemptOutcome)
	}
}

func TestExecutor_prepareSubmitAndALeg_syntheticLocalPrincipalWhenEnabled(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:                   b2,
		Bus:                     hooks.New(hooks.Config{}),
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		SyntheticLocalPrincipal: true,
		Now:                     func() time.Time { return time.Unix(2200, 0) },
	})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "c1",
			ALegID:          "forged-aleg",
			ContinuityKey:   "forged-ck",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, outCtx, err := ex.prepareSubmitAndALeg(context.Background(), ex.Bus, call)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := execctx.FromContext(outCtx)
	if !ok {
		t.Fatal("expected views")
	}
	if v.Principal.ID != syntheticLocalPrincipalID {
		t.Fatalf("principal id: want %q got %q", syntheticLocalPrincipalID, v.Principal.ID)
	}
	if v.Principal.Claims["issuer"] != syntheticLocalPrincipalIssuer {
		t.Fatalf("issuer: want %q got %q", syntheticLocalPrincipalIssuer, v.Principal.Claims["issuer"])
	}
	if v.Session.ALegID == "" || v.Session.ALegID == "forged-aleg" {
		t.Fatalf("unexpected aleg %q", v.Session.ALegID)
	}
}

func TestExecutor_prepareSubmitAndALeg_missingPrincipalWithoutSynthetic(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:                   b2,
		Bus:                     hooks.New(hooks.Config{}),
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		SyntheticLocalPrincipal: false,
		Now:                     func() time.Time { return time.Unix(2210, 0) },
	})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "c1"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(context.Background(), ex.Bus, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial got %T %v", err, err)
	}
}

func TestExecutor_Execute_unauthenticatedSyntheticPrincipal_reachesBackend(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	var opens atomic.Int32
	ex := setSecureSessionDenialMapper(&Executor{
		Store:                   b2,
		Bus:                     hooks.New(hooks.Config{}),
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		SyntheticLocalPrincipal: true,
		Now:                     func() time.Time { return time.Unix(2300, 0) },
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	})
	ctx := context.Background()
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "unsynth"},
		Route:   lipapi.RouteIntent{Selector: "ok:g"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens: want 1 got %d", opens.Load())
	}
}

func TestExecutor_Execute_unauthenticatedNoSynthetic_deniedWithoutBackendOpen(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	var opens atomic.Int32
	ex := setSecureSessionDenialMapper(&Executor{
		Store:                   b2,
		Bus:                     hooks.New(hooks.Config{}),
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		SyntheticLocalPrincipal: false,
		Now:                     func() time.Time { return time.Unix(2310, 0) },
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	})
	ctx := context.Background()
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "nosynth"},
		Route:   lipapi.RouteIntent{Selector: "ok:g"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(ctx, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial got %T %v", err, err)
	}
	if opens.Load() != 0 {
		t.Fatalf("backend opens: want 0 got %d", opens.Load())
	}
}

// compileRecorderFake ensures [testkit.FakeSecureSessionRecorder] matches [SecureSessionRecorder] at compile time.
var _ SecureSessionRecorder = (*recorderFake)(nil)

type recorderFake struct{}

func (recorderFake) RecordClientTurnAfterGate(context.Context, app.ClientTurnRecordInput) error {
	return nil
}

func (recorderFake) RecordPostHookStreamEvent(context.Context, app.StreamEventRecordInput) error {
	return nil
}

func TestExecutor_secureSessionRecorder_fieldAssignable(t *testing.T) {
	t.Parallel()
	ex := &Executor{SecureSessionRecorder: recorderFake{}}
	if ex.SecureSessionRecorder == nil {
		t.Fatal("expected recorder")
	}
}
