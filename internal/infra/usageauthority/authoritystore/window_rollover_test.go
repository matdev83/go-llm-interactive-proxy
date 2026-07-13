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

func windowedQuotaRule(id string, anchor time.Time, size time.Duration) domain.Rule {
	return domain.Rule{
		ID:    id,
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
		Window: domain.WindowSpec{
			Algorithm: domain.WindowAlgorithmFixed,
			Size:      size,
			Anchor:    anchor,
		},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-window")},
			Model:   domain.DimensionMatcher{Value: scope.Known("model-window")},
		},
	}
}

func windowedDimensions() domain.Dimensions {
	return domain.Dimensions{
		Principal: scope.Known("principal-window"),
		Tenant:    scope.Known("tenant-window"),
		Backend:   scope.Known("backend-window"),
		Model:     scope.Known("model-window"),
	}
}

func allLimitRows(t *testing.T, store *authoritystore.MemoryStore, ruleID string) []controlplane.AccountingLimitStatusRow {
	t.Helper()
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		RuleID:     ruleID,
		Limit:      50,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus(%s): %v", ruleID, err)
	}
	return page.Items
}

// TestWindowRolloverReserveMatchesFreshZeroCounterWindow proves Finding 3:
// after the seeded window expires, a reserve matches a fresh zero-counter
// window whose bounds are the next anchored step, instead of becoming
// unavailable.
func TestWindowRolloverReserveMatchesFreshZeroCounterWindow(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	rule := windowedQuotaRule("quota-hourly", anchor, time.Hour)
	store := storeFromRules(t, []domain.Rule{rule}, anchor.Add(30*time.Minute))

	// Reserve inside window 1 (anchor..anchor+1h): reserved=40, remaining=60.
	w1At := anchor.Add(30 * time.Minute)
	if _, err := store.Reserve(context.Background(), reserveCmd("quota-hourly", "quota", windowedDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 40}, w1At, "reserve-w1")); err != nil {
		t.Fatalf("Reserve w1: %v", err)
	}
	w1Row := limitRow(t, store, "quota-hourly", string(domain.AmountUnitRequests))
	if w1Row.Reserved != 40 || w1Row.Remaining != 60 {
		t.Fatalf("window 1 row = reserved=%d remaining=%d, want reserved=40 remaining=60", w1Row.Reserved, w1Row.Remaining)
	}
	if !w1Row.WindowStart.Equal(anchor) || !w1Row.WindowEnd.Equal(anchor.Add(time.Hour)) {
		t.Fatalf("window 1 bounds = %s..%s, want %s..%s", w1Row.WindowStart, w1Row.WindowEnd, anchor, anchor.Add(time.Hour))
	}

	// Advance past window 1 end into window 2 (anchor+1h..anchor+2h).
	w2At := anchor.Add(90 * time.Minute)
	reserveW2 := reserveCmd("quota-hourly", "quota", windowedDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, w2At, "reserve-w2")
	reserveW2.ReservationKey.AttemptID = "attempt-w2"
	if _, err := store.Reserve(context.Background(), reserveW2); err != nil {
		t.Fatalf("Reserve w2: %v", err)
	}

	rows := allLimitRows(t, store, "quota-hourly")
	if len(rows) != 2 {
		t.Fatalf("limit rows = %d, want 2 (expired window 1 + fresh window 2)", len(rows))
	}
	var w2Row *controlplane.AccountingLimitStatusRow
	for i := range rows {
		if rows[i].WindowStart.Equal(anchor.Add(time.Hour)) {
			w2Row = &rows[i]
		}
	}
	if w2Row == nil {
		t.Fatalf("no window 2 row found among %#v", rows)
	}
	if w2Row.Consumed != 0 || w2Row.Reserved != 10 || w2Row.Remaining != 90 {
		t.Fatalf("window 2 row = consumed=%d reserved=%d remaining=%d, want consumed=0 reserved=10 remaining=90",
			w2Row.Consumed, w2Row.Reserved, w2Row.Remaining)
	}
	if !w2Row.WindowStart.Equal(anchor.Add(time.Hour)) || !w2Row.WindowEnd.Equal(anchor.Add(2*time.Hour)) {
		t.Fatalf("window 2 bounds = %s..%s, want %s..%s",
			w2Row.WindowStart, w2Row.WindowEnd, anchor.Add(time.Hour), anchor.Add(2*time.Hour))
	}
}

func TestActiveLimitReturnsUnpersistedCurrentWindowWithoutMutation(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	rule := windowedQuotaRule("active-hourly", anchor, time.Hour)
	store := storeFromRules(t, []domain.Rule{rule}, anchor.Add(30*time.Minute))
	at := anchor.Add(90 * time.Minute)
	row, configured, err := store.ActiveLimit(context.Background(), app.ActiveLimitQuery{RuleID: rule.ID, Dimensions: windowedDimensions(), At: at})
	if err != nil || !configured {
		t.Fatalf("ActiveLimit configured=%v err=%v", configured, err)
	}
	if row.Reserved != 0 || row.Consumed != 0 || row.Remaining != 100 {
		t.Fatalf("unpersisted active row = %#v, want zero counters and remaining 100", row)
	}
	rows := allLimitRows(t, store, rule.ID)
	if len(rows) != 1 {
		t.Fatalf("ActiveLimit mutated history: rows=%d, want seeded row only", len(rows))
	}
}

// TestWindowRolloverExpiredRowRemainsQueryVisible proves the expired window row
// stays in LimitStatus and that decision history still shows the prior-window
// reserve decision with its original window bounds.
func TestWindowRolloverExpiredRowRemainsQueryVisible(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	rule := windowedQuotaRule("quota-hourly", anchor, time.Hour)
	store := storeFromRules(t, []domain.Rule{rule}, anchor.Add(30*time.Minute))

	w1At := anchor.Add(30 * time.Minute)
	if _, err := store.Reserve(context.Background(), reserveCmd("quota-hourly", "quota", windowedDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 40}, w1At, "reserve-w1")); err != nil {
		t.Fatalf("Reserve w1: %v", err)
	}

	// Trigger rollover into window 2.
	w2At := anchor.Add(90 * time.Minute)
	reserveW2 := reserveCmd("quota-hourly", "quota", windowedDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, w2At, "reserve-w2")
	reserveW2.ReservationKey.AttemptID = "attempt-w2"
	if _, err := store.Reserve(context.Background(), reserveW2); err != nil {
		t.Fatalf("Reserve w2: %v", err)
	}

	rows := allLimitRows(t, store, "quota-hourly")
	var expiredSeen bool
	for _, r := range rows {
		if r.WindowEnd.Equal(anchor.Add(time.Hour)) {
			expiredSeen = true
			if r.Reserved != 40 {
				t.Fatalf("expired window 1 row reserved = %d, want 40 (preserved)", r.Reserved)
			}
		}
	}
	if !expiredSeen {
		t.Fatalf("expired window 1 row must remain query-visible: %#v", rows)
	}

	decPage, err := store.DecisionHistory(context.Background(), controlplane.AccountingDecisionQuery{
		RuleID:     "quota-hourly",
		Limit:      50,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory: %v", err)
	}
	var w1DecisionSeen bool
	for _, d := range decPage.Items {
		if d.WindowStart.Equal(anchor) && d.WindowEnd.Equal(anchor.Add(time.Hour)) {
			w1DecisionSeen = true
		}
	}
	if !w1DecisionSeen {
		t.Fatalf("prior-window reserve decision must remain in decision history: %#v", decPage.Items)
	}
}

func TestWindowRolloverSettlementUsesOriginalReservationWindow(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	rule := windowedQuotaRule("quota-settle-rollover", anchor, time.Hour)
	store := storeFromRules(t, []domain.Rule{rule}, anchor.Add(30*time.Minute))

	firstAt := anchor.Add(30 * time.Minute)
	reserve := reserveCmd("quota-settle-rollover", "quota", windowedDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 40}, firstAt, "reserve-original-window")
	reserved, err := store.Reserve(context.Background(), reserve)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	reserveCurrent := reserveCmd("quota-settle-rollover", "quota", windowedDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, anchor.Add(90*time.Minute), "reserve-current-window")
	reserveCurrent.ReservationKey.AttemptID = "attempt-current-window"
	if _, err := store.Reserve(context.Background(), reserveCurrent); err != nil {
		t.Fatalf("Reserve current window: %v", err)
	}

	settled, err := store.Settle(context.Background(), app.SettleCommand{
		ReservationKey: reserve.ReservationKey,
		ReservationID:  reserved.ReservationID,
		RuleID:         rule.ID,
		Kind:           app.SettlementKindFinal,
		FinalUsage:     domain.Amount{Unit: domain.AmountUnitRequests, Value: 30},
		ReservedUsage:  domain.Amount{Unit: domain.AmountUnitRequests, Value: 40},
		At:             anchor.Add(90 * time.Minute),
		SourceKey:      "settle-original-window",
	})
	if err != nil {
		t.Fatalf("Settle after rollover: %v", err)
	}
	if !settled.Applied || settled.ReleasedDelta.Value != 10 {
		t.Fatalf("settle result = %#v, want applied with 10 released", settled)
	}

	rows := allLimitRows(t, store, rule.ID)
	if len(rows) != 2 {
		t.Fatalf("limit rows = %d, want original and current windows", len(rows))
	}
	for _, row := range rows {
		switch {
		case row.WindowStart.Equal(anchor):
			if row.Consumed != 30 || row.Reserved != 0 {
				t.Fatalf("original row after settlement = consumed=%d reserved=%d, want 30/0", row.Consumed, row.Reserved)
			}
		case row.WindowStart.Equal(anchor.Add(time.Hour)):
			if row.Consumed != 0 || row.Reserved != 10 {
				t.Fatalf("current row changed by original settlement = consumed=%d reserved=%d, want 0/10", row.Consumed, row.Reserved)
			}
		}
	}
}

// TestWindowRolloverDeterministicForAnchorAndSize proves rollover is
// deterministic: the same anchor+size always yields the same anchored step
// bounds, even when more than one window has elapsed.
func TestWindowRolloverDeterministicForAnchorAndSize(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	rule := windowedQuotaRule("quota-hourly", anchor, time.Hour)
	store := storeFromRules(t, []domain.Rule{rule}, anchor.Add(30*time.Minute))

	// Skip two windows ahead: window 1 expired, window 2 expired, land in
	// window 3 (anchor+2h..anchor+3h).
	w3At := anchor.Add(150 * time.Minute)
	if _, err := store.Reserve(context.Background(), reserveCmd("quota-hourly", "quota", windowedDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 5}, w3At, "reserve-w3")); err != nil {
		t.Fatalf("Reserve w3: %v", err)
	}

	rows := allLimitRows(t, store, "quota-hourly")
	var w3Row *controlplane.AccountingLimitStatusRow
	for i := range rows {
		if rows[i].WindowStart.Equal(anchor.Add(2 * time.Hour)) {
			w3Row = &rows[i]
		}
	}
	if w3Row == nil {
		t.Fatalf("no window 3 row found among %#v", rows)
	}
	if !w3Row.WindowEnd.Equal(anchor.Add(3*time.Hour)) || !w3Row.WindowResetAt.Equal(anchor.Add(3*time.Hour)) {
		t.Fatalf("window 3 end/reset = %s/%s, want %s/%s",
			w3Row.WindowEnd, w3Row.WindowResetAt, anchor.Add(3*time.Hour), anchor.Add(3*time.Hour))
	}
	if w3Row.Reserved != 5 || w3Row.Remaining != 95 {
		t.Fatalf("window 3 row = reserved=%d remaining=%d, want reserved=5 remaining=95", w3Row.Reserved, w3Row.Remaining)
	}
}

// TestWindowRolloverNoWindowRuleUnaffected proves a rule without a WindowSpec
// keeps existing behavior: its limit row never advances and admission keeps
// matching the same row regardless of time.
func TestWindowRolloverNoWindowRuleUnaffected(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := quotaRule("quota-nowindow") // no Window configured
	store := storeFromRules(t, []domain.Rule{rule}, at)

	if _, err := store.Reserve(context.Background(), reserveCmd("quota-nowindow", "quota", quotaDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 20}, at, "reserve-1")); err != nil {
		t.Fatalf("Reserve 1: %v", err)
	}
	// Far in the future: a no-window rule must still match the same row.
	future := at.Add(72 * time.Hour)
	reserveFuture := reserveCmd("quota-nowindow", "quota", quotaDimensions(),
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 30}, future, "reserve-2")
	reserveFuture.ReservationKey.AttemptID = "attempt-future"
	if _, err := store.Reserve(context.Background(), reserveFuture); err != nil {
		t.Fatalf("Reserve 2 (future, no window): %v", err)
	}
	row := limitRow(t, store, "quota-nowindow", string(domain.AmountUnitRequests))
	if row.Reserved != 50 {
		t.Fatalf("no-window row reserved = %d, want 50 (same row, no rollover)", row.Reserved)
	}
	if !row.WindowStart.IsZero() || !row.WindowEnd.IsZero() {
		t.Fatalf("no-window row bounds must stay zero: %s..%s", row.WindowStart, row.WindowEnd)
	}
	rows := allLimitRows(t, store, "quota-nowindow")
	if len(rows) != 1 {
		t.Fatalf("no-window rule limit rows = %d, want 1 (no advancement)", len(rows))
	}
}
