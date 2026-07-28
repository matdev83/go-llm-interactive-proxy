package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
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
	plan := &routePlanState{
		sel:      sel,
		budget:   &attemptBudget{max: 3},
		excluded: map[string]struct{}{},
		session:  &routing.SessionRoutingState{},
		rng:      routing.NewSeededRng(1),
	}
	prep := &preparedRequest{
		aScope: aScope,
		aLeg:   aLeg,
	}
	_, err = ex.openInitialAttempt(ctx, prep, plan)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
