package authoritystore_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// storeFromRules builds a ready in-memory store seeded from rules, populating
// the per-rule window spec map so fixed-window rollover is exercised the same
// way the runtimebundle composition wires it.
func storeFromRules(t *testing.T, rules []domain.Rule, at time.Time) *authoritystore.MemoryStore {
	t.Helper()
	limitRows, err := authoritystore.LimitRowsFromRules(rules, at)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	ruleWindows := make(map[string]domain.WindowSpec, len(rules))
	for _, r := range rules {
		ruleWindows[r.ID] = r.Window
	}
	return authoritystore.NewMemory(authoritystore.Config{
		StoreID:     "test-store",
		Backing:     domain.BackingCapabilityAtomic,
		Readiness:   domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		LimitRows:   limitRows,
		RuleWindows: ruleWindows,
	})
}

func budgetRule(id string) domain.Rule {
	return domain.Rule{
		ID:       id,
		Kind:     domain.RuleKindBudget,
		Mode:     domain.RuleModeStrict,
		Unit:     domain.AmountUnitMoneyNano,
		Currency: "usd",
		Limit:    domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 1000},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-budget")},
			Model:   domain.DimensionMatcher{Value: scope.Known("model-budget")},
		},
	}
}

func quotaRule(id string) domain.Rule {
	return domain.Rule{
		ID:    id,
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-quota")},
			Model:   domain.DimensionMatcher{Value: scope.Known("model-quota")},
		},
	}
}

func budgetDimensions() domain.Dimensions {
	return domain.Dimensions{
		Principal: scope.Known("principal-budget"),
		Tenant:    scope.Known("tenant-budget"),
		Backend:   scope.Known("backend-budget"),
		Model:     scope.Known("model-budget"),
	}
}

func quotaDimensions() domain.Dimensions {
	return domain.Dimensions{
		Principal: scope.Known("principal-quota"),
		Tenant:    scope.Known("tenant-quota"),
		Backend:   scope.Known("backend-quota"),
		Model:     scope.Known("model-quota"),
	}
}

func reserveCmd(ruleID, ruleType string, dims domain.Dimensions, amount domain.Amount, at time.Time, source string) app.ReserveCommand {
	return app.ReserveCommand{
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "req-" + ruleID,
			ALegID:           "a-" + ruleID,
			BLegID:           "b-" + ruleID,
			AttemptID:        "attempt-" + ruleID,
			RuleID:           ruleID,
			Sequence:         1,
		},
		RuleID:     ruleID,
		RuleType:   ruleType,
		Dimensions: dims,
		Request:    amount,
		Spend:      amount,
		Authority:  domain.AuthorityLevelAuthoritative,
		At:         at,
		SourceKey:  source,
	}
}

func settleCmd(reservationID, ruleID, source string, kind app.SettlementKind, finalUsage, finalCost, reservedUsage, estimatedUsage, estimatedCost domain.Amount, at time.Time) app.SettleCommand {
	ruleKey := domain.ReservationKey{
		LogicalRequestID: "req-" + ruleID,
		ALegID:           "a-" + ruleID,
		BLegID:           "b-" + ruleID,
		AttemptID:        "attempt-" + ruleID,
		RuleID:           ruleID,
		Sequence:         1,
	}
	return app.SettleCommand{
		SettlementKey:  domain.SettlementKey{ReservationKey: ruleKey, Sequence: 1},
		ReservationKey: ruleKey,
		ReservationID:  reservationID,
		RuleID:         ruleID,
		Kind:           kind,
		FinalUsage:     finalUsage,
		FinalCost:      finalCost,
		ReservedUsage:  reservedUsage,
		EstimatedUsage: estimatedUsage,
		EstimatedCost:  estimatedCost,
		Authority:      domain.AuthorityLevelAuthoritative,
		At:             at,
		SourceKey:      source,
	}
}

func limitRow(t *testing.T, store *authoritystore.MemoryStore, ruleID, unit string) controlplane.AccountingLimitStatusRow {
	t.Helper()
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		RuleID:     ruleID,
		Unit:       unit,
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus(%s): %v", ruleID, err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("LimitStatus(%s) items = 0", ruleID)
	}
	return page.Items[0]
}

// TestSettleBudgetConsumesFinalCostInMoney proves Finding 1: a budget (money)
// reservation settles against cmd.FinalCost (money nano), not cmd.FinalUsage
// (tokens). The released/overage/adjustment deltas and the limit row's Consumed
// are computed in money with the reservation's currency.
func TestSettleBudgetConsumesFinalCostInMoney(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{budgetRule("budget-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100, Currency: "usd"}
	res, err := store.Reserve(context.Background(), reserveCmd("budget-1", "budget", budgetDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !res.Applied {
		t.Fatalf("Reserve must apply: %#v", res)
	}

	// FinalUsage carries tokens (40); FinalCost carries money (80). Settlement
	// must consume the money amount, ignoring the token usage.
	settle, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "budget-1", "settle-1", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 40},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 80, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 40},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 70, Currency: "usd"},
		at.Add(time.Minute),
	))
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !settle.Applied {
		t.Fatalf("Settle must apply: %#v", settle)
	}
	// actual = FinalCost = 80 < reserved 100 -> released=20, overage=0, adjustment=20.
	if settle.ReleasedDelta.Value != 20 || settle.OverageDelta.Value != 0 || settle.AdjustmentDelta.Value != 20 {
		t.Fatalf("settle deltas = released=%d overage=%d adjustment=%d, want released=20 overage=0 adjustment=20",
			settle.ReleasedDelta.Value, settle.OverageDelta.Value, settle.AdjustmentDelta.Value)
	}
	if settle.ReleasedDelta.Unit != domain.AmountUnitMoneyNano || settle.ReleasedDelta.Currency != "usd" {
		t.Fatalf("settle ReleasedDelta unit/currency = %s/%s, want money_nano/usd", settle.ReleasedDelta.Unit, settle.ReleasedDelta.Currency)
	}
	if settle.OverageDelta.Unit != domain.AmountUnitMoneyNano {
		t.Fatalf("settle OverageDelta unit = %s, want money_nano", settle.OverageDelta.Unit)
	}

	row := limitRow(t, store, "budget-1", string(domain.AmountUnitMoneyNano))
	if row.Consumed != 80 {
		t.Fatalf("limit Consumed = %d, want 80 (money)", row.Consumed)
	}
	if row.Reserved != 0 {
		t.Fatalf("limit Reserved = %d, want 0", row.Reserved)
	}
	if row.Remaining != 920 {
		t.Fatalf("limit Remaining = %d, want 920", row.Remaining)
	}
}

// TestSettleBudgetWithOverageInMoney proves a budget settlement where FinalCost
// exceeds the reserved spend records overage (not release) in money.
func TestSettleBudgetWithOverageInMoney(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{budgetRule("budget-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100, Currency: "usd"}
	res, err := store.Reserve(context.Background(), reserveCmd("budget-1", "budget", budgetDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	settle, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "budget-1", "settle-1", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 200},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 150, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 200},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 140, Currency: "usd"},
		at.Add(time.Minute),
	))
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// actual = FinalCost = 150 > reserved 100 -> overage=50, released=0, adjustment=-50.
	if settle.ReleasedDelta.Value != 0 || settle.OverageDelta.Value != 50 || settle.AdjustmentDelta.Value != -50 {
		t.Fatalf("settle deltas = released=%d overage=%d adjustment=%d, want released=0 overage=50 adjustment=-50",
			settle.ReleasedDelta.Value, settle.OverageDelta.Value, settle.AdjustmentDelta.Value)
	}
	row := limitRow(t, store, "budget-1", string(domain.AmountUnitMoneyNano))
	if row.Consumed != 150 {
		t.Fatalf("limit Consumed = %d, want 150", row.Consumed)
	}
}

// TestSettleBudgetZeroFinalCostFallsBackToEstimatedCost proves Finding 1's
// fallback: when a money rule's FinalCost is zero/unavailable, settlement
// records spend from EstimatedCost so an estimated settlement still consumes
// the budget.
func TestSettleBudgetZeroFinalCostFallsBackToEstimatedCost(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{budgetRule("budget-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100, Currency: "usd"}
	res, err := store.Reserve(context.Background(), reserveCmd("budget-1", "budget", budgetDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// FinalCost is zero; EstimatedCost carries the spend estimate (70).
	settle, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "budget-1", "settle-1", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 0, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 70, Currency: "usd"},
		at.Add(time.Minute),
	))
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !settle.Applied {
		t.Fatalf("Settle must apply: %#v", settle)
	}
	// actual = EstimatedCost = 70 < reserved 100 -> released=30.
	if settle.ReleasedDelta.Value != 30 || settle.OverageDelta.Value != 0 {
		t.Fatalf("settle deltas = released=%d overage=%d, want released=30 overage=0",
			settle.ReleasedDelta.Value, settle.OverageDelta.Value)
	}
	if settle.ReleasedDelta.Currency != "usd" {
		t.Fatalf("settle ReleasedDelta currency = %s, want usd (from estimated cost)", settle.ReleasedDelta.Currency)
	}
	row := limitRow(t, store, "budget-1", string(domain.AmountUnitMoneyNano))
	if row.Consumed != 70 {
		t.Fatalf("limit Consumed = %d, want 70 (estimated cost)", row.Consumed)
	}
}

func TestSettleBudgetPreservesAuthoritativeZeroCost(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{budgetRule("budget-zero-authoritative")}, at)
	reserved := domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100, Currency: "usd"}
	res, err := store.Reserve(context.Background(), reserveCmd("budget-zero-authoritative", "budget", budgetDimensions(), reserved, at, "reserve-zero-authoritative"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	settle, err := store.Settle(context.Background(), app.SettleCommand{
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "req-budget-zero-authoritative",
			ALegID:           "a-budget-zero-authoritative",
			BLegID:           "b-budget-zero-authoritative",
			AttemptID:        "attempt-budget-zero-authoritative",
			RuleID:           "budget-zero-authoritative",
			Sequence:         1,
		},
		ReservationID:    res.ReservationID,
		RuleID:           "budget-zero-authoritative",
		Kind:             app.SettlementKindFinal,
		FinalCost:        domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 0, Currency: "usd"},
		FinalCostPresent: true,
		ReservedUsage:    reserved,
		EstimatedCost:    domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 70, Currency: "usd"},
		At:               at.Add(time.Minute),
		SourceKey:        "settle-zero-authoritative",
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if settle.ReleasedDelta.Value != 100 || settle.OverageDelta.Value != 0 {
		t.Fatalf("authoritative zero cost deltas = released=%d overage=%d, want 100/0", settle.ReleasedDelta.Value, settle.OverageDelta.Value)
	}
	row := limitRow(t, store, "budget-zero-authoritative", string(domain.AmountUnitMoneyNano))
	if row.Consumed != 0 {
		t.Fatalf("authoritative zero cost consumed=%d, want 0", row.Consumed)
	}
}

// TestSettleBudgetCurrencyInheritsFromReservation proves Finding 1's currency
// consistency: when FinalCost carries no currency, the settled actual uses the
// reservation's ReservedAmount currency.
func TestSettleBudgetCurrencyInheritsFromReservation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{budgetRule("budget-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100, Currency: "usd"}
	res, err := store.Reserve(context.Background(), reserveCmd("budget-1", "budget", budgetDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// FinalCost has an empty currency; the store must adopt the reservation's
	// currency so the deltas are not currency-less.
	settle, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "budget-1", "settle-1", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 40},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 80, Currency: ""},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 40},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 70, Currency: "eur"},
		at.Add(time.Minute),
	))
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if settle.ReleasedDelta.Currency != "usd" {
		t.Fatalf("settle ReleasedDelta currency = %q, want usd (from reservation)", settle.ReleasedDelta.Currency)
	}
	if settle.OverageDelta.Currency != "usd" {
		t.Fatalf("settle OverageDelta currency = %q, want usd", settle.OverageDelta.Currency)
	}
}

// TestSettleQuotaConsumesFinalUsageInTokens proves Finding 1's non-money path
// stays on cmd.FinalUsage (tokens/requests), preserving existing behavior.
func TestSettleQuotaConsumesFinalUsageInTokens(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{quotaRule("quota-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitRequests, Value: 60}
	res, err := store.Reserve(context.Background(), reserveCmd("quota-1", "quota", quotaDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// FinalUsage carries tokens (40); FinalCost carries money (400). A quota
	// rule must consume the token usage, ignoring the cost.
	settle, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "quota-1", "settle-1", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 40},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 400, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 50},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 500, Currency: "usd"},
		at.Add(time.Minute),
	))
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// actual = FinalUsage = 40 < reserved 60 -> released=20.
	if settle.ReleasedDelta.Value != 20 || settle.OverageDelta.Value != 0 || settle.AdjustmentDelta.Value != 20 {
		t.Fatalf("settle deltas = released=%d overage=%d adjustment=%d, want released=20 overage=0 adjustment=20",
			settle.ReleasedDelta.Value, settle.OverageDelta.Value, settle.AdjustmentDelta.Value)
	}
	if settle.ReleasedDelta.Unit != domain.AmountUnitRequests {
		t.Fatalf("settle ReleasedDelta unit = %s, want requests", settle.ReleasedDelta.Unit)
	}
	row := limitRow(t, store, "quota-1", string(domain.AmountUnitRequests))
	if row.Consumed != 40 {
		t.Fatalf("limit Consumed = %d, want 40 (tokens)", row.Consumed)
	}
}

// TestSettleQuotaNonFinalZeroFinalUsageFallsBackToEstimatedUsage proves the
// token path keeps its existing fallback for partial/cancellation settlements.
func TestSettleQuotaNonFinalZeroFinalUsageFallsBackToEstimatedUsage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{quotaRule("quota-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitRequests, Value: 60}
	res, err := store.Reserve(context.Background(), reserveCmd("quota-1", "quota", quotaDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	settle, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "quota-1", "settle-1", app.SettlementKindPartial,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 0, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 45},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 450, Currency: "usd"},
		at.Add(time.Minute),
	))
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// actual = EstimatedUsage = 45 < reserved 60 -> released=15.
	if settle.ReleasedDelta.Value != 15 {
		t.Fatalf("settle ReleasedDelta = %d, want 15", settle.ReleasedDelta.Value)
	}
	row := limitRow(t, store, "quota-1", string(domain.AmountUnitRequests))
	if row.Consumed != 45 {
		t.Fatalf("limit Consumed = %d, want 45 (estimated usage)", row.Consumed)
	}
}
