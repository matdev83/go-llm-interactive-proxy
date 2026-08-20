package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func TestOpenInitialAttempt_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	setupCtx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(setupCtx, "open-loop-cancel")
	if err != nil {
		t.Fatal(err)
	}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLeg.ALegID)

	sel, err := routing.Parse("exec:m")
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	rng := routing.NewSeededRng(1)
	progress := newRecoveryController(recoveryControllerInput{
		budget:   &attemptBudget{max: 3},
		sel:      sel,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		rng:      rng,
	})
	plan := &routePlanState{
		routeFacts: routeFacts{
			sel: sel,
			rng: rng,
		},
		progress: progress,
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ALegID: aLeg.ALegID,
		},
	}
	preSession := session.SessionView{
		ALegID: aLeg.ALegID,
	}
	ibt, err := newIdentityBoundTurn(
		"open-loop-cancel",
		call,
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
		t.Fatal(err)
	}
	prep := &preparedRequest{
		identity: ibt,
		call:     ibt.call,
		aScope:   aScope,
	}
	_, err = ex.openInitialAttempt(ctx, prep, plan)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
