package runtime

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func clampTestCatalog(t *testing.T) accounting.PriceCatalog {
	t.Helper()
	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend: "backend-1", Model: "model-1", InputPer1M: "2", OutputPer1M: "4",
		}},
	})
	if err != nil {
		t.Fatalf("NewPriceCatalog: %v", err)
	}
	return catalog
}

func clampTestCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}}
}

func TestAuthorityClampChargesInputBeforeOutput(t *testing.T) {
	t.Parallel()
	// Input costs 2e9 nano units and the remaining 4e9 buys exactly 1M
	// output tokens. Output-only conversion would incorrectly return 1.5M.
	got, outcome := authorityClampMaxOutputTokens(clampTestCatalog(t), clampTestCandidate(), 6_000_000_000, 1_000_000)
	if outcome != authorityClampApplied {
		t.Fatal("authorityClampMaxOutputTokens returned unavailable")
	}
	if got != 1_000_000 {
		t.Fatalf("max output = %d, want 1000000 after input cost", got)
	}
}

func TestAuthorityClampNeverWidensClientCap(t *testing.T) {
	t.Parallel()
	max := 10
	call := lipapi.Call{Options: lipapi.GenerationOptions{MaxOutputTokens: &max}}
	executor := &Executor{AccountingRuntime: AccountingRuntime{AccountingPriceCatalog: clampTestCatalog(t)}}
	err := executor.applyAuthorityClamp(&call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 6_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailClosed,
	}, 1_000_000)
	if err != nil {
		t.Fatalf("applyAuthorityClamp: %v", err)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != max {
		t.Fatalf("max output = %v, want preserved client cap %d", call.Options.MaxOutputTokens, max)
	}
}

func TestAuthorityClampRejectsInputCostOverCap(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{}
	executor := &Executor{AccountingRuntime: AccountingRuntime{AccountingPriceCatalog: clampTestCatalog(t)}}
	err := executor.applyAuthorityClamp(&call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 1_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailClosed,
	}, 1_000_000)
	if err == nil || !lipapi.IsPolicyDenied(err) {
		t.Fatalf("applyAuthorityClamp error = %v, want policy denial when input already exceeds cap", err)
	}
}

func TestAuthorityClampRejectsInputCostOverCapWhenFailOpen(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{}
	executor := &Executor{AccountingRuntime: AccountingRuntime{AccountingPriceCatalog: clampTestCatalog(t)}}
	err := executor.applyAuthorityClamp(&call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 1_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailOpen,
	}, 1_000_000)
	if err == nil || !lipapi.IsPolicyDenied(err) {
		t.Fatalf("applyAuthorityClamp error = %v, want deterministic policy denial", err)
	}
}

func TestAuthorityClampRejectsExactInputCostExhaustion(t *testing.T) {
	t.Parallel()
	// Input at $2/1M for 1M tokens consumes exactly 2e9 nano; remaining output
	// budget is zero and must deny rather than clamp to MaxOutputTokens=0.
	got, outcome := authorityClampMaxOutputTokens(clampTestCatalog(t), clampTestCandidate(), 2_000_000_000, 1_000_000)
	if outcome != authorityClampCapacityExhausted {
		t.Fatalf("outcome = %v max=%d, want capacity exhausted", outcome, got)
	}
	call := lipapi.Call{}
	executor := &Executor{AccountingRuntime: AccountingRuntime{AccountingPriceCatalog: clampTestCatalog(t)}}
	err := executor.applyAuthorityClamp(&call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 2_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailClosed,
	}, 1_000_000)
	if err == nil || !lipapi.IsPolicyDenied(err) {
		t.Fatalf("applyAuthorityClamp error = %v, want policy denial for zero remaining output budget", err)
	}
}

func TestBackendCanEnforceAuthorityClamp(t *testing.T) {
	t.Parallel()
	max := 100
	call := lipapi.Call{Options: lipapi.GenerationOptions{MaxOutputTokens: &max}}
	enforcing := execbackend.Backend{EnforcesMaxOutputTokens: true}
	nonEnforcing := execbackend.Backend{}
	if !backendCanEnforceAuthorityClamp(enforcing, &call) {
		t.Fatal("backend with EnforcesMaxOutputTokens must enforce the clamp")
	}
	if backendCanEnforceAuthorityClamp(nonEnforcing, &call) {
		t.Fatal("backend without EnforcesMaxOutputTokens must fail closed")
	}
	call.Extensions = map[string]json.RawMessage{
		authorityClampIgnoreUnsupportedGenParamsExt: json.RawMessage(`true`),
	}
	if backendCanEnforceAuthorityClamp(enforcing, &call) {
		t.Fatal("ignore_unsupported_gen_params must make the clamp unenforceable")
	}
	if !backendCanEnforceAuthorityClamp(enforcing, &lipapi.Call{}) {
		t.Fatal("calls without MaxOutputTokens are vacuously enforceable")
	}
}
