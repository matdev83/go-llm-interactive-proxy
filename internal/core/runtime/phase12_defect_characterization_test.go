package runtime

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Phase 1.2 characterization: omitted max-output and total-token inference defects
// on the attempt authority spend/usage paths (F-03, F-08).

func phase12SpendCatalog(t *testing.T) accounting.PriceCatalog {
	t.Helper()
	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "phase12-spend",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:     "backend-1",
			Model:       "model-1",
			InputPer1M:  "1.00",
			OutputPer1M: "2.00",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// TestAttemptAuthoritySpendAmount_omittedMaxCurrentlyReservesInputOnly locks F-03:
// when Count.OutputTokens is 0 (omitted/unknown max path), spend equals input-only cost.
// Phase 2 flip: unknown output must not reserve zero future exposure.
func TestAttemptAuthoritySpendAmount_omittedMaxCurrentlyReservesInputOnly(t *testing.T) {
	t.Parallel()

	catalog := phase12SpendCatalog(t)
	cand := authorityCandidate()
	decision := accountingpreflight.Decision{
		Allowed: true,
		Count: accountingapp.CountResult{
			InputTokens:  1_000_000,
			OutputTokens: 0, // omitted / unknown future output
		},
	}

	got := attemptAuthoritySpendAmount(catalog, cand, decision)
	inputOnly := accounting.EstimateCost(accounting.CostInput{
		Backend: "backend-1",
		Model:   "model-1",
		Usage:   accounting.TokenUsage{InputTokens: 1_000_000, OutputTokens: 0},
	}, catalog)

	if got.Value != inputOnly.NanoUnits {
		t.Fatalf("spend=%d want input-only %d", got.Value, inputOnly.NanoUnits)
	}
	if got.Value != 1_000_000_000 {
		t.Fatalf("current defect characterization: spend=%d want 1e9 input-only; Phase 2 must apply conservative unknown-output policy", got.Value)
	}

	// Desired RED evidence: a non-zero output exposure bound would exceed input-only.
	withOutput := accounting.EstimateCost(accounting.CostInput{
		Backend: "backend-1",
		Model:   "model-1",
		Usage:   accounting.TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
	}, catalog)
	if got.Value >= withOutput.NanoUnits {
		t.Fatal("expected input-only spend to be strictly less than input+1M-output estimate")
	}
}

// TestAttemptAuthorityUsageAmount_currentlyInfersWithSubcomponents locks F-08 on the
// runtime settlement path when TotalTokens is absent.
func TestAttemptAuthorityUsageAmount_currentlyInfersWithSubcomponents(t *testing.T) {
	t.Parallel()

	ev := lipapi.Event{
		Kind:             lipapi.EventUsageDelta,
		InputTokens:      100,
		OutputTokens:     20,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		ReasoningTokens:  5,
		TotalTokens:      0,
	}
	got := attemptAuthorityUsageAmount(ev, authoritydomain.Amount{Unit: authoritydomain.AmountUnitTotalTokens})
	// Current: 100+20+40+10+5 = 175. Desired: 120 (input includes cache, output includes reasoning).
	if got.Value != 175 {
		t.Fatalf("current defect characterization: total=%d want 175; Phase 2 must infer without double-count (120)", got.Value)
	}
}

func TestPhase12_desiredSpendAndUsageSemanticsCurrentlyAbsent(t *testing.T) {
	t.Parallel()

	catalog := phase12SpendCatalog(t)
	spend := attemptAuthoritySpendAmount(catalog, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-1", Model: "model-1"},
	}, accountingpreflight.Decision{
		Allowed: true,
		Count:   accountingapp.CountResult{InputTokens: 1_000_000, OutputTokens: 0},
	})
	// Desired (Phase 2): omitted max must not reserve input-only (zero future output).
	// While still defective, spend stays at 1e9 input-only.
	if spend.Value != 1_000_000_000 {
		t.Fatalf("desired unknown-output spend semantics appear present (spend=%d, not input-only 1e9); Phase 2 flip of characterization tests is due", spend.Value)
	}

	ev := lipapi.Event{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 40, CacheWriteTokens: 10, ReasoningTokens: 5}
	got := attemptAuthorityUsageAmount(ev, authoritydomain.Amount{Unit: authoritydomain.AmountUnitTotalTokens})
	if got.Value == 120 {
		t.Fatal("desired non-double-count usage inference already present; Phase 2 flip due")
	}
}
