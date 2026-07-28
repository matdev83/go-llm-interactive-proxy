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
)

func primaryBackend(sel *routing.Selector) string {
	if sel == nil || len(sel.Alternatives) == 0 || sel.Alternatives[0].Primary == nil {
		return ""
	}
	return sel.Alternatives[0].Primary.Backend
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
	prep := &preparedRequest{
		baseline: lipapi.Call{Route: lipapi.RouteIntent{Selector: "alias"}},
		aLeg:     b2bua.ALegRecord{},
	}
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
	prep := &preparedRequest{
		baseline: lipapi.Call{Route: lipapi.RouteIntent{Selector: "gpt-4"}},
		aLeg:     b2bua.ALegRecord{},
	}
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
	prep := &preparedRequest{
		baseline: lipapi.Call{Route: lipapi.RouteIntent{Selector: "gpt-4"}},
		aLeg:     b2bua.ALegRecord{},
	}
	_, err := ex.buildRoutePlan(context.Background(), prep)
	if err == nil || !errors.Is(err, lipapi.ErrUnresolvedModelOnlySelector) {
		t.Fatalf("want ErrUnresolvedModelOnlySelector, got %v", err)
	}
}

func TestBuildRoutePlan_affinityIdentityError(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.AffinityMissingIdentity = affinity.MissingIdentityFailClosed
	prep := &preparedRequest{
		baseline:    lipapi.Call{Route: lipapi.RouteIntent{Selector: "{affinity=session}backendA:model-x"}},
		aLeg:        b2bua.ALegRecord{},
		recvViewsOK: true,
		recvViews:   execctx.Views{},
	}
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
	prep := &preparedRequest{
		baseline: lipapi.Call{Route: lipapi.RouteIntent{Selector: "backendA:model-x"}},
		aLeg:     b2bua.ALegRecord{WeightedFirstConsumed: true},
	}
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if plan.budget == nil || plan.budget.max != 5 || plan.budget.usedNow() != 0 {
		t.Fatalf("budget: got %+v", plan.budget)
	}
	if plan.session == nil || !plan.session.FirstRequestConsumed {
		t.Fatalf("session state: got %+v", plan.session)
	}
	if plan.excluded == nil {
		t.Fatal("expected non-nil excluded set")
	}
}
