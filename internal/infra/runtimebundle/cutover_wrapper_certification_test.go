package runtimebundle_test

// Phase 17.3C wrapper/decorator certification: production runtime composition
// must not bypass mandatory claim metadata by hiding optional interfaces.
// A decorator that forwards only non-claim ports must fail the narrow claim
// type-asserts; production constructors require explicit non-nil claim ports
// (legacy constructors remain test-only). Deterministic, no sleeps.

import (
	"os"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// hideClaimBillingStore forwards only non-claim billing ports. It embeds the
// non-claim interfaces, never the concrete store, so GetCutoverClaimMetadata
// and the F6+F8 token-carrying claim ports are NOT promoted even when the
// inner store implements them.
type hideClaimBillingStore struct {
	billing.AuthoritativeBilling
	billing.CallUsageStore
	billing.CallSettlementStore
	billing.ProviderCostWorkReader
	billing.ProviderCostStore
}

func TestCutoverWrapperHidingFailsClaimAsserts(t *testing.T) {
	t.Parallel()
	var hiding *hideClaimBillingStore
	if _, ok := any(hiding).(billing.CutoverClaimMetadataProvider); ok {
		t.Fatalf("hiding decorator must not expose CutoverClaimMetadataProvider")
	}
	if _, ok := any(hiding).(billing.ProviderCostWorkClaimStore); ok {
		t.Fatalf("hiding decorator must not expose ProviderCostWorkClaimStore")
	}
	if _, ok := any(hiding).(billing.FinancialAdjustmentClaimStore); ok {
		t.Fatalf("hiding decorator must not expose FinancialAdjustmentClaimStore")
	}
	if _, ok := any(hiding).(billing.ClaimedCompleteCallClaimer); ok {
		t.Fatalf("hiding decorator must not expose ClaimedCompleteCallClaimer")
	}
	if _, ok := any(hiding).(billing.ClaimedProviderCostWorkClaimer); ok {
		t.Fatalf("hiding decorator must not expose ClaimedProviderCostWorkClaimer")
	}
	if _, ok := any(hiding).(billing.EconomicRevisionWorkCutoverClaimer); ok {
		t.Fatalf("hiding decorator must not expose EconomicRevisionWorkCutoverClaimer")
	}
	// Production constructors must reject nil claim ports (fail closed).
	if _, err := billing.NewCallPostUsageWorkerWithClaim(nil, nil, nil, nil, 8); err == nil {
		t.Fatalf("nil customer claim must be rejected")
	}
	if _, err := billing.NewCallProviderCostWorkerWithClaim(nil, nil, nil, nil, 8); err == nil {
		t.Fatalf("nil provider claim must be rejected")
	}
	if _, err := billing.NewCallPostUsageWorkerWithCutover(nil, nil, nil, nil, 8); err == nil {
		t.Fatalf("nil customer cutover claimer must be rejected")
	}
	if _, err := billing.NewCallProviderCostWorkerWithCutover(nil, nil, nil, nil, 8); err == nil {
		t.Fatalf("nil provider cutover claimer must be rejected")
	}
	if _, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover(nil, nil, nil, nil, nil, nil, billing.EconomicQueueProvider, 8); err == nil {
		t.Fatalf("nil economic cutover claimer must be rejected")
	}
}

func TestCutoverWrapperProductionRequiresExplicitPorts(t *testing.T) {
	t.Parallel()
	// Source guard: production composition must use WithCutover token-carrying
	// constructors and fail closed when the claim port is hidden. Legacy
	// fallbacks must not reach production posting.
	candidates := []string{
		"internal/infra/runtimebundle/process_billing.go",
		"../../internal/infra/runtimebundle/process_billing.go",
		"../../../internal/infra/runtimebundle/process_billing.go",
		"process_billing.go",
	}
	var content string
	var found bool
	for _, c := range candidates {
		if data, err := os.ReadFile(c); err == nil {
			content = string(data)
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cannot read process_billing.go for wrapper guard")
	}
	for _, want := range []string{
		"NewCallPostUsageWorkerWithCutover",
		"NewCallProviderCostWorkerWithCutover",
		"NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover",
		"requires cutover claim metadata port",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("process_billing.go must contain %q (explicit required cutover claim ports)", want)
		}
	}
	// Legacy test-only fallbacks must not appear as production construction
	// (allow comments mentioning legacy).
	lines := strings.SplitSeq(content, "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(line, "NewCallPostUsageWorker(") {
			t.Fatalf("process_billing.go must not construct legacy NewCallPostUsageWorker in production (use WithCutover)")
		}
		if strings.Contains(line, "NewCallProviderCostWorker(") && !strings.Contains(line, "NewCallProviderCostWorkerWithCutover") {
			t.Fatalf("process_billing.go must not construct legacy NewCallProviderCostWorker in production (use WithCutover)")
		}
		if strings.Contains(line, "NewEconomicRevisionWorkerWithReconcilerAndProviderCost(") && !strings.Contains(line, "WithCutover") {
			t.Fatalf("process_billing.go must not construct legacy economic worker in production (use WithCutover)")
		}
		// Old WithClaim (lookup, not atomic token) must not be production either;
		// WithCutover is the required token-carrying path.
		if strings.Contains(line, "NewCallPostUsageWorkerWithClaim(") {
			t.Fatalf("process_billing.go must not construct legacy WithClaim in production (use WithCutover)")
		}
		if strings.Contains(line, "NewCallProviderCostWorkerWithClaim(") {
			t.Fatalf("process_billing.go must not construct legacy WithClaim in production (use WithCutover)")
		}
		if strings.Contains(line, "NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithClaim(") {
			t.Fatalf("process_billing.go must not construct legacy WithClaim in production (use WithCutover)")
		}
	}
}
