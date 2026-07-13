package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestExecutorAuthorityRequestSizeEstimateHasNoAdmissionSideEffects(t *testing.T) {
	t.Parallel()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	leg, err := store.CreateALeg(context.Background(), "request-size")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}
	auth := &recordingAuthorityService{status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady}}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.UsageAuthority = auth
	ex.Preflight = accountingpreflight.NewChecker(authorityAdmissionCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
		return accountingapp.CountResult{InputTokens: 3, TotalTokens: 3}, nil
	}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory})
	sel, err := routing.Parse("[max_context=10]backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	est := ex.requestSizeEstimateForRouting(context.Background(), sel, lipapi.Call{ID: "request-1"})
	if !est.Available || est.Tokens != 4 {
		t.Fatalf("request size estimate = %#v, want available estimate with 4 tokens", est)
	}
	if auth.admitCalls.Load() != 0 {
		t.Fatalf("admit calls = %d, want 0", auth.admitCalls.Load())
	}
	if _, err := store.FetchALeg(context.Background(), leg.ALegID); err != nil {
		t.Fatalf("fetch a-leg: %v", err)
	}
}
