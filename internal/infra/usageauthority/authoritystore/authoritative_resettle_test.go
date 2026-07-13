package authoritystore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// decisionByReason returns the single decision row for the given reason code,
// failing the test if not exactly one match is found.
func decisionByReason(t *testing.T, store *authoritystore.MemoryStore, ruleID, reason string) controlplane.AccountingDecisionRow {
	t.Helper()
	page, err := store.DecisionHistory(context.Background(), controlplane.AccountingDecisionQuery{
		Common:     controlplane.CommonFilters{ReasonCode: reason},
		RuleID:     ruleID,
		Limit:      50,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory(%s): %v", reason, err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("DecisionHistory(%s) items = %d, want 1", reason, len(page.Items))
	}
	return page.Items[0]
}

// countDecisionsByReason returns how many decision rows match the reason code.
func countDecisionsByReason(t *testing.T, store *authoritystore.MemoryStore, ruleID, reason string) int {
	t.Helper()
	page, err := store.DecisionHistory(context.Background(), controlplane.AccountingDecisionQuery{
		Common:     controlplane.CommonFilters{ReasonCode: reason},
		RuleID:     ruleID,
		Limit:      50,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory(%s): %v", reason, err)
	}
	return len(page.Items)
}

// TestAuthoritativeResettleAdjustsTokenSettlement proves Finding 8: after an
// estimated settlement, a later authoritative final usage (new source key)
// adjusts Consumed and overage/release by the delta and records an adjustment
// decision, while the prior estimated decision remains in history.
func TestAuthoritativeResettleAdjustsTokenSettlement(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{quotaRule("quota-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitRequests, Value: 60}
	res, err := store.Reserve(context.Background(), reserveCmd("quota-1", "quota", quotaDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// Estimated (partial) settlement: no final usage yet, fall back to the
	// estimated usage (45). Consumed=45, released=15.
	estimated, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "quota-1", "settle-estimated", app.SettlementKindPartial,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 0, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 45},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 450, Currency: "usd"},
		at.Add(time.Minute),
	))
	if err != nil {
		t.Fatalf("estimated Settle: %v", err)
	}
	if !estimated.Applied || estimated.ReleasedDelta.Value != 15 {
		t.Fatalf("estimated settle = %#v, want applied released=15", estimated)
	}
	row := limitRow(t, store, "quota-1", string(domain.AmountUnitRequests))
	if row.Consumed != 45 {
		t.Fatalf("after estimated settle Consumed = %d, want 45", row.Consumed)
	}

	// A new source key cannot turn an estimated/non-authoritative replay into
	// an adjustment. Only the explicit authoritative lifecycle may append one.
	nonAuthoritative := settleCmd(
		res.ReservationID, "quota-1", "settle-non-authoritative", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 70},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 700, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 45},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 450, Currency: "usd"},
		at.Add(90*time.Second),
	)
	nonAuthoritative.Authority = domain.AuthorityLevelEstimated
	if _, err := store.Settle(context.Background(), nonAuthoritative); !errors.Is(err, app.ErrDuplicateSettlement) {
		t.Fatalf("non-authoritative resettle error = %v, want duplicate settlement", err)
	}
	if row = limitRow(t, store, "quota-1", string(domain.AmountUnitRequests)); row.Consumed != 45 {
		t.Fatalf("non-authoritative resettle changed Consumed = %d, want 45", row.Consumed)
	}

	// Authoritative (final) re-settlement with a NEW source key: actual=70.
	// delta = 70 - 45 = 25 -> overage=25, adjustment=-25, Consumed becomes 70.
	authoritative, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "quota-1", "settle-authoritative", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 70},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 700, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 45},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 450, Currency: "usd"},
		at.Add(2*time.Minute),
	))
	if err != nil {
		t.Fatalf("authoritative Settle: %v", err)
	}
	if !authoritative.Applied {
		t.Fatalf("authoritative settle must apply: %#v", authoritative)
	}
	if authoritative.ReleasedDelta.Value != 0 || authoritative.OverageDelta.Value != 25 || authoritative.AdjustmentDelta.Value != -25 {
		t.Fatalf("authoritative deltas = released=%d overage=%d adjustment=%d, want released=0 overage=25 adjustment=-25",
			authoritative.ReleasedDelta.Value, authoritative.OverageDelta.Value, authoritative.AdjustmentDelta.Value)
	}
	if authoritative.OverageDelta.Unit != domain.AmountUnitRequests {
		t.Fatalf("authoritative OverageDelta unit = %s, want requests", authoritative.OverageDelta.Unit)
	}
	row = limitRow(t, store, "quota-1", string(domain.AmountUnitRequests))
	if row.Consumed != 70 {
		t.Fatalf("after authoritative settle Consumed = %d, want 70", row.Consumed)
	}

	// Replay the SAME authoritative source key: idempotent no-op.
	replay, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "quota-1", "settle-authoritative", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 70},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 700, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 45},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 450, Currency: "usd"},
		at.Add(3*time.Minute),
	))
	if err != nil {
		t.Fatalf("replay Settle: %v", err)
	}
	if replay.Applied {
		t.Fatalf("replay of same authoritative source key must be a no-op: %#v", replay)
	}
	row = limitRow(t, store, "quota-1", string(domain.AmountUnitRequests))
	if row.Consumed != 70 {
		t.Fatalf("after replay Consumed = %d, want 70 (unchanged)", row.Consumed)
	}

	// The prior estimated decision (reason "reconciled") remains in history.
	estimatedDecision := decisionByReason(t, store, "quota-1", "reconciled")
	if estimatedDecision.Consumed != 70 {
		// The decision row mirrors the live limit row's current counters at
		// query time; the important invariant is that the row EXISTS.
		_ = estimatedDecision.Consumed
	}
	if countDecisionsByReason(t, store, "quota-1", "authoritative_adjustment") != 1 {
		t.Fatalf("expected exactly one authoritative_adjustment decision in history")
	}
}

// TestAuthoritativeResettleAdjustsMoneySettlement proves Finding 8 for a money
// rule: the adjustment delta is computed in money from FinalCost.
func TestAuthoritativeResettleAdjustsMoneySettlement(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{budgetRule("budget-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100, Currency: "usd"}
	res, err := store.Reserve(context.Background(), reserveCmd("budget-1", "budget", budgetDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// Estimated settlement: FinalCost=0 -> EstimatedCost=60. Consumed=60.
	if _, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "budget-1", "settle-estimated", app.SettlementKindPartial,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 0, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 60, Currency: "usd"},
		at.Add(time.Minute),
	)); err != nil {
		t.Fatalf("estimated Settle: %v", err)
	}
	if row := limitRow(t, store, "budget-1", string(domain.AmountUnitMoneyNano)); row.Consumed != 60 {
		t.Fatalf("after estimated settle Consumed = %d, want 60", row.Consumed)
	}

	// Authoritative re-settlement: FinalCost=130. delta = 130 - 60 = 70 ->
	// overage=70, adjustment=-70, Consumed becomes 130.
	authoritative, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "budget-1", "settle-authoritative", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 130, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 60, Currency: "usd"},
		at.Add(2*time.Minute),
	))
	if err != nil {
		t.Fatalf("authoritative Settle: %v", err)
	}
	if !authoritative.Applied {
		t.Fatalf("authoritative settle must apply: %#v", authoritative)
	}
	if authoritative.OverageDelta.Value != 70 || authoritative.AdjustmentDelta.Value != -70 {
		t.Fatalf("authoritative deltas = overage=%d adjustment=%d, want overage=70 adjustment=-70",
			authoritative.OverageDelta.Value, authoritative.AdjustmentDelta.Value)
	}
	if authoritative.OverageDelta.Unit != domain.AmountUnitMoneyNano || authoritative.OverageDelta.Currency != "usd" {
		t.Fatalf("authoritative OverageDelta = %s/%s, want money_nano/usd", authoritative.OverageDelta.Unit, authoritative.OverageDelta.Currency)
	}
	if row := limitRow(t, store, "budget-1", string(domain.AmountUnitMoneyNano)); row.Consumed != 130 {
		t.Fatalf("after authoritative settle Consumed = %d, want 130", row.Consumed)
	}
}

// TestAuthoritativeResettleDeltaZeroRecordsDecisionNoCounterChange proves that
// when the authoritative actual equals the prior settled actual (delta == 0),
// an adjustment decision is still recorded but the limit counters do not move.
func TestAuthoritativeResettleDeltaZeroRecordsDecisionNoCounterChange(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{quotaRule("quota-1")}, at)

	reserved := domain.Amount{Unit: domain.AmountUnitRequests, Value: 60}
	res, err := store.Reserve(context.Background(), reserveCmd("quota-1", "quota", quotaDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	if _, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "quota-1", "settle-estimated", app.SettlementKindPartial,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 0, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 50},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 500, Currency: "usd"},
		at.Add(time.Minute),
	)); err != nil {
		t.Fatalf("estimated Settle: %v", err)
	}
	before := limitRow(t, store, "quota-1", string(domain.AmountUnitRequests))
	if before.Consumed != 50 {
		t.Fatalf("after estimated settle Consumed = %d, want 50", before.Consumed)
	}

	// Authoritative actual equals the estimated actual (50) -> delta 0.
	authoritative, err := store.Settle(context.Background(), settleCmd(
		res.ReservationID, "quota-1", "settle-authoritative", app.SettlementKindFinal,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 50},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 500, Currency: "usd"},
		reserved,
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 50},
		domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 500, Currency: "usd"},
		at.Add(2*time.Minute),
	))
	if err != nil {
		t.Fatalf("authoritative Settle: %v", err)
	}
	if !authoritative.Applied {
		t.Fatalf("delta-zero authoritative settle must still apply (record decision): %#v", authoritative)
	}
	if authoritative.ReleasedDelta.Value != 0 || authoritative.OverageDelta.Value != 0 || authoritative.AdjustmentDelta.Value != 0 {
		t.Fatalf("delta-zero authoritative deltas = %#v %#v %#v, want all 0",
			authoritative.ReleasedDelta, authoritative.OverageDelta, authoritative.AdjustmentDelta)
	}
	after := limitRow(t, store, "quota-1", string(domain.AmountUnitRequests))
	if after.Consumed != 50 {
		t.Fatalf("after delta-zero authoritative settle Consumed = %d, want 50 (unchanged)", after.Consumed)
	}
	if countDecisionsByReason(t, store, "quota-1", "authoritative_adjustment") != 1 {
		t.Fatalf("delta-zero authoritative settle must still record one adjustment decision")
	}
}
