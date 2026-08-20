package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func primaryBackend(sel *routing.Selector) string {
	if sel == nil || len(sel.Alternatives) == 0 || sel.Alternatives[0].Primary == nil {
		return ""
	}
	return sel.Alternatives[0].Primary.Backend
}

func testPreparedRequest(traceID string, call lipapi.Call, aLeg b2bua.ALegRecord, recvViews execctx.Views, recvViewsOK bool) *preparedRequest {
	if traceID == "" {
		traceID = "trace-test"
	}
	if aLeg.ALegID == "" {
		aLeg.ALegID = "a-test"
	}
	call.Session.ALegID = aLeg.ALegID
	preSession := session.SessionView{
		ALegID: aLeg.ALegID,
	}
	ibt, err := newIdentityBoundTurn(
		traceID,
		&call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		lipworkspace.WorkspaceView{},
		aLeg,
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSession,
	)
	if err != nil {
		panic(err)
	}
	return &preparedRequest{
		identity: ibt,
		call:     ibt.call,
		recvTurnFacts: recvTurnFacts{
			recvViews:   recvViews,
			recvViewsOK: recvViewsOK,
		},
	}
}

func TestBuildRoutePlan_selectorAliasRewritesBeforeParse(t *testing.T) {
	t.Parallel()
	ar, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^alias$`, Replacement: "backendB:model-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.SelectorAliases = ar
	ex.DefaultBackend = "backendA"
	prep := testPreparedRequest("trace-1", lipapi.Call{Route: lipapi.RouteIntent{Selector: "alias"}}, b2bua.ALegRecord{}, execctx.Views{}, false)
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if got := primaryBackend(plan.sel); got != "backendB" {
		t.Fatalf("alias rewrite: want backendB, got %q", got)
	}
}

func TestBuildRoutePlan_modelOnlyAppliesDefaultBackend(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.DefaultBackend = "defaultBE"
	prep := testPreparedRequest("trace-1", lipapi.Call{Route: lipapi.RouteIntent{Selector: "gpt-4"}}, b2bua.ALegRecord{}, execctx.Views{}, false)
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if got := primaryBackend(plan.sel); got != "defaultBE" {
		t.Fatalf("model-only default: want defaultBE, got %q", got)
	}
}

func TestBuildRoutePlan_unresolvedModelOnlyFails(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	prep := testPreparedRequest("trace-1", lipapi.Call{Route: lipapi.RouteIntent{Selector: "gpt-4"}}, b2bua.ALegRecord{}, execctx.Views{}, false)
	_, err := ex.buildRoutePlan(context.Background(), prep)
	if err == nil || !errors.Is(err, lipapi.ErrUnresolvedModelOnlySelector) {
		t.Fatalf("want ErrUnresolvedModelOnlySelector, got %v", err)
	}
}

func TestBuildRoutePlan_affinityIdentityError(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.AffinityMissingIdentity = affinity.MissingIdentityFailClosed
	prep := testPreparedRequest("trace-1", lipapi.Call{Route: lipapi.RouteIntent{Selector: "{affinity=session}backendA:model-x"}}, b2bua.ALegRecord{}, execctx.Views{}, true)
	_, err := ex.buildRoutePlan(context.Background(), prep)
	if err == nil || !strings.Contains(err.Error(), "affinity identity") {
		t.Fatalf("want affinity identity error, got %v", err)
	}
}

func TestBuildRoutePlan_initializesBudgetAndSession(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.MaxAttempts = 5
	ex.DefaultBackend = "backendA"
	prep := testPreparedRequest("trace-1", lipapi.Call{Route: lipapi.RouteIntent{Selector: "backendA:model-x"}}, b2bua.ALegRecord{WeightedFirstConsumed: true}, execctx.Views{}, false)
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if plan.progress.budget == nil || plan.progress.budget.max != 5 || plan.progress.budget.usedNow() != 0 {
		t.Fatalf("budget: got %+v", plan.progress.budget)
	}
	if plan.progress.session == nil || !plan.progress.session.FirstRequestConsumed {
		t.Fatalf("session state: got %+v", plan.progress.session)
	}
	if plan.progress.excluded == nil {
		t.Fatal("expected non-nil excluded set")
	}
}
