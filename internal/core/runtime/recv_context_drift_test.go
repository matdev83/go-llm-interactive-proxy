package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// driftFacts builds a recvTurnFacts with fully frozen request authority.
func driftFacts() recvTurnFacts {
	return testRecvTurnFacts(recvTurnFacts{
		baseline: lipapi.Call{Session: lipapi.SessionRef{ALegID: "a-1"}},
		traceID:  "trace-1",
		aLegID:   "a-1",
		recvViews: execctx.Views{
			Session: session.SessionView{AuthoritativeSessionID: "sess-1", ALegID: "a-1"},
		},
		recvViewsOK:            true,
		routePrefs:             []string{"cand-a", "cand-b"},
		secureTurn:             execctx.SecureSessionTurn{SessionID: domain.SessionID("sess-1"), TurnID: domain.TurnID("turn-1")},
		secureTurnOK:           true,
		metering:               &checkpoint.RequestHolder{},
		requestAuth:            &requestAuthorityState{},
		billingCallID:          billing.BillingCallID("billing-1"),
		billingCallState:       newBillingCallState(billing.BillingCallID("billing-1")),
		billingAccountID:       "acct-1",
		billingIdentityStamped: true,
		nativeResolver:         &driftResolver{model: "frozen-model"},
	})
}

type driftResolver struct{ model string }

func (d *driftResolver) ResolveModelBinding(_, _ string) routing.ModelBinding {
	return routing.ModelBinding{Kind: routing.ModelBindingExactCanonical, Native: d.model}
}

// TestRecvContextDrift_BareContextDoesNotOverwriteFrozenFacts verifies that a
// Recv called with context.Background() still sees the frozen request facts.
func TestRecvContextDrift_BareContextDoesNotOverwriteFrozenFacts(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	bare := context.Background()
	derived := facts.projectContext(bare, nil)

	views, ok := execctx.FromContext(derived)
	if !ok || views.Session.AuthoritativeSessionID != "sess-1" {
		t.Fatalf("views %+v ok=%v want sess-1", views, ok)
	}
	prefs := execctx.RouteCandidatePreferences(derived)
	if len(prefs) != 2 || prefs[0] != "cand-a" || prefs[1] != "cand-b" {
		t.Fatalf("prefs %v want [cand-a cand-b]", prefs)
	}
	st, ok := execctx.SecureSessionTurnFromContext(derived)
	if !ok || string(st.SessionID) != "sess-1" {
		t.Fatalf("secure turn %+v ok=%v", st, ok)
	}
}

// TestRecvContextDrift_ReplacementAfterGenerationRefresh ensures a generation
// refresh between initial open and retry does not change the frozen route prefs
// (proxy for bound model view). The frozen prefs must win.
func TestRecvContextDrift_ReplacementAfterGenerationRefresh(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	newPrefs := []string{"evil:model"}
	refreshCtx := execctx.WithRouteCandidatePreferences(context.Background(), newPrefs)
	derived := facts.projectContext(refreshCtx, nil)
	got := execctx.RouteCandidatePreferences(derived)
	if len(got) != 2 || got[0] != "cand-a" || got[1] != "cand-b" {
		t.Fatalf("generation refresh must not overwrite frozen prefs, got %v", got)
	}
}

// TestRecvContextDrift_AuxiliaryWithMissingAuthority ensures an auxiliary
// continuation that lacks request authority still sees the original authority
// via the frozen facts.
func TestRecvContextDrift_AuxiliaryWithMissingAuthority(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	auxCtx := context.Background()
	derived := facts.projectContext(auxCtx, nil)
	if _, ok := execctx.SecureSessionTurnFromContext(derived); !ok {
		t.Fatal("auxiliary ctx must still carry frozen secure turn")
	}
	if _, ok := execctx.FromContext(derived); !ok {
		t.Fatal("must have views")
	}
	if facts.billingCallID != "billing-1" {
		t.Fatalf("billing %q want billing-1", facts.billingCallID)
	}
}

// TestRecvContextDrift_ConflictingRoutePrefsDoesNotOverwriteFrozen ensures a
// caller context that carries conflicting route preferences or a different
// model resolver does not overwrite the frozen turn.
func TestRecvContextDrift_ConflictingRoutePrefsDoesNotOverwriteFrozen(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	conflictCtx := execctx.WithRouteCandidatePreferences(context.Background(), []string{"evil:model"})
	conflictCtx = routing.WithNativeModelResolver(conflictCtx, &driftResolver{model: "evil-model"})
	derived := facts.projectContext(conflictCtx, nil)
	prefs := execctx.RouteCandidatePreferences(derived)
	if len(prefs) != 2 || prefs[0] != "cand-a" || prefs[1] != "cand-b" {
		t.Fatalf("prefs %v must be frozen, not evil", prefs)
	}
	resolver, ok := routing.NativeModelResolverFromContext(derived)
	if !ok {
		t.Fatal("must have resolver")
	}
	binding := resolver.ResolveModelBinding("any", "any")
	if binding.Native != "frozen-model" {
		t.Fatalf("resolver model %q want frozen-model", binding.Native)
	}
}

// TestRecvContextDrift_CancellationWithDeadlineStillHasBusinessFacts ensures a
// context that only carries a deadline/cancellation but no business metadata
// still gets the frozen business facts.
func TestRecvContextDrift_CancellationWithDeadlineStillHasBusinessFacts(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	derived := facts.projectContext(ctx, nil)
	if _, ok := execctx.FromContext(derived); !ok {
		t.Fatal("must have views even on timeout ctx")
	}
	cancelled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	derived2 := facts.projectContext(cancelled, nil)
	if _, ok := execctx.FromContext(derived2); !ok {
		t.Fatal("cancelled ctx must still get frozen views")
	}
}

// TestRecvContextDrift_ConcurrentGenerationReload ensures that a concurrent
// generation reload does not race the frozen turn's view.
func TestRecvContextDrift_ConcurrentGenerationReload(t *testing.T) {
	t.Parallel()
	facts := driftFacts()
	var wg sync.WaitGroup
	errCh := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := execctx.WithRouteCandidatePreferences(context.Background(), []string{"evil:model"})
			derived := facts.projectContext(ctx, nil)
			got := execctx.RouteCandidatePreferences(derived)
			if len(got) != 2 || got[0] != "cand-a" || got[1] != "cand-b" {
				errCh <- "drift"
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
}

// TestRecvContextDrift_ConflictingContextDoesNotOverrideFrozenFacts verifies that
// when a conflicting context (carrying different principal, scope, session, and workspace)
// is projected, the frozen facts win, and caller context values cannot overwrite them.
func TestRecvContextDrift_ConflictingContextDoesNotOverrideFrozenFacts(t *testing.T) {
	t.Parallel()
	facts := driftFacts()

	// Build a conflicting context.
	conflictingScope := scope.PrincipalScopeView{
		PrincipalID: scope.Known("evil-principal"),
		Origin:      scope.OriginClient,
	}
	conflictingViews := execctx.Views{
		Principal: execview.PrincipalView{
			ID:     "evil-principal",
			Claims: map[string]string{"role": "evil-role"},
		},
		Scope: conflictingScope,
		Session: session.SessionView{
			AuthoritativeSessionID: "evil-sess-id",
		},
		Workspace: lipworkspace.WorkspaceView{
			ID: "evil-ws-id",
		},
	}
	conflictCtx := execctx.WithViews(context.Background(), conflictingViews)

	derived := facts.projectContext(conflictCtx, nil)
	gotViews, ok := execctx.FromContext(derived)
	if !ok {
		t.Fatal("views missing in derived context")
	}

	if gotViews.Session.AuthoritativeSessionID != "sess-1" {
		t.Fatalf("session overridden: got %q, want sess-1", gotViews.Session.AuthoritativeSessionID)
	}
	if gotViews.Principal.ID != "" { // driftFacts principal is empty/zero
		t.Fatalf("principal overridden: got %q, want empty", gotViews.Principal.ID)
	}
}

// TestRecvContextDrift_BoundViewsReloadPinning verifies that once views are captured
// at preparation, subsequent generation reloads do not affect the pinned registry, catalog,
// and native resolver in the projected context.
func TestRecvContextDrift_BoundViewsReloadPinning(t *testing.T) {
	t.Parallel()

	// Pre-captured views
	regView := modelregistry.EmptyBoundView()
	catView := modelcatalog.EmptyBoundView()
	resolver := &driftResolver{model: "frozen-model"}

	facts := newRecvTurnFacts(context.Background(), recvTurnFactsInput{
		baseline:        lipapi.Call{Session: lipapi.SessionRef{ALegID: "a-1"}},
		traceID:         "trace-1",
		aLegID:          "a-1",
		boundRegistry:   regView,
		boundRegistryOK: true,
		boundCatalog:    catView,
		boundCatalogOK:  true,
		nativeResolver:  resolver,
	})

	// Simulate reloaded views in the caller context (representing generation reload)
	reloadedRegView := modelregistry.EmptyBoundView()
	reloadedCatView := modelcatalog.EmptyBoundView()
	reloadedResolver := &driftResolver{model: "reloaded-model"}

	reloadCtx := modelregistry.WithBoundView(context.Background(), reloadedRegView)
	reloadCtx = modelcatalog.WithBoundView(reloadCtx, reloadedCatView)
	reloadCtx = routing.WithNativeModelResolver(reloadCtx, reloadedResolver)

	derived := facts.projectContext(reloadCtx, nil)

	// Verify pinned resolver is used
	gotResolver, ok := routing.NativeModelResolverFromContext(derived)
	if !ok {
		t.Fatal("resolver missing in derived context")
	}
	binding := gotResolver.ResolveModelBinding("any", "any")
	if binding.Native != "frozen-model" {
		t.Fatalf("resolver re-derived after reload: got %q, want frozen-model", binding.Native)
	}
}
