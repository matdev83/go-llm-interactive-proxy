package authoritystore_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func atomicSetRules() []domain.Rule {
	return []domain.Rule{
		{
			ID:    "atomic.requests",
			Kind:  domain.RuleKindQuota,
			Mode:  domain.RuleModeStrict,
			Unit:  domain.AmountUnitRequests,
			Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
			Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-atomic")}},
		},
		{
			ID:       "atomic.budget",
			Kind:     domain.RuleKindBudget,
			Mode:     domain.RuleModeStrict,
			Unit:     domain.AmountUnitMoneyNano,
			Currency: "usd",
			Limit:    domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 1000, Currency: "usd"},
			Match:    domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-atomic")}},
		},
	}
}

func atomicSetDimensions() domain.Dimensions {
	return domain.Dimensions{
		Principal: scope.Known("principal-atomic"),
		Backend:   scope.Known("backend-atomic"),
		Model:     scope.Known("model-atomic"),
	}
}

func atomicReservationKey(ruleID string) domain.ReservationKey {
	return domain.ReservationKey{
		LogicalRequestID: "request-atomic",
		ALegID:           "a-atomic",
		BLegID:           "b-atomic",
		AttemptID:        "attempt-atomic",
		RuleID:           ruleID,
		Sequence:         1,
	}
}

func atomicReservationSet() app.ReservationSet {
	dims := atomicSetDimensions()
	return app.ReservationSet{
		{
			RuleID:         "atomic.requests",
			Kind:           domain.RuleKindQuota,
			Unit:           domain.AmountUnitRequests,
			Dimensions:     dims,
			ReservationKey: atomicReservationKey("atomic.requests"),
			Amount:         domain.Amount{Unit: domain.AmountUnitRequests, Value: 3},
			SourceKey:      "atomic-reserve-requests",
		},
		{
			RuleID:         "atomic.budget",
			Kind:           domain.RuleKindBudget,
			Unit:           domain.AmountUnitMoneyNano,
			Currency:       "usd",
			Dimensions:     dims,
			ReservationKey: atomicReservationKey("atomic.budget"),
			Amount:         domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 250, Currency: "usd"},
			SourceKey:      "atomic-reserve-budget",
		},
	}
}

func atomicStore(t *testing.T) *authoritystore.MemoryStore {
	t.Helper()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rules := atomicSetRules()
	rows, err := authoritystore.LimitRowsFromRules(rules, at)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	return authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "atomic-set",
		Backing:   domain.BackingCapabilityAtomic,
		Readiness: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		LimitRows: rows,
	})
}

func limitRowFromStore(t *testing.T, store app.StateStore, ruleID, unit string) controlplane.AccountingLimitStatusRow {
	t.Helper()
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		RuleID: ruleID, Unit: unit, Limit: 10, Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus(%s): %v", ruleID, err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("LimitStatus(%s) returned no rows", ruleID)
	}
	return page.Items[0]
}

func TestMemoryStoreReservationSetIsAtomicAcrossUnits(t *testing.T) {
	t.Parallel()

	store := atomicStore(t)
	set := atomicReservationSet()
	reserve, err := store.Reserve(context.Background(), app.ReserveCommand{
		Reservations:   set,
		ReservationKey: set[0].ReservationKey,
		RuleID:         set[0].RuleID,
		RuleType:       string(set[0].Kind),
		Dimensions:     set[0].Dimensions,
		Request:        set[0].Amount,
		At:             time.Date(2026, 7, 10, 12, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !reserve.Applied || len(reserve.Reservations) != 2 {
		t.Fatalf("Reserve result = %#v, want two applied reservations", reserve)
	}

	requests := limitRowFromStore(t, store, "atomic.requests", string(domain.AmountUnitRequests))
	budget := limitRowFromStore(t, store, "atomic.budget", string(domain.AmountUnitMoneyNano))
	if requests.Reserved != 3 || budget.Reserved != 250 {
		t.Fatalf("reserved counters = requests=%d budget=%d, want 3/250", requests.Reserved, budget.Reserved)
	}

	settle, err := store.Settle(context.Background(), app.SettleCommand{
		Reservations: []app.SettlementDescriptor{
			{Reservation: set[0], FinalUsage: domain.Amount{Unit: domain.AmountUnitRequests, Value: 2}},
			{Reservation: set[1], FinalCost: domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 200, Currency: "usd"}},
		},
		Kind:              app.SettlementKindFinal,
		FinalUsagePresent: true,
		FinalCostPresent:  true,
		At:                time.Date(2026, 7, 10, 12, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !settle.Applied || len(settle.Mutations) != 2 {
		t.Fatalf("Settle result = %#v, want two mutations", settle)
	}
	requests = limitRow(t, store, "atomic.requests", string(domain.AmountUnitRequests))
	budget = limitRow(t, store, "atomic.budget", string(domain.AmountUnitMoneyNano))
	if requests.Consumed != 2 || budget.Consumed != 200 || requests.Reserved != 0 || budget.Reserved != 0 {
		t.Fatalf("settled counters = requests=%+v budget=%+v, want consumed 2/200 and no reservations", requests, budget)
	}
}

func TestMemoryStoreReservationSetFailureIdentifiesRule(t *testing.T) {
	t.Parallel()
	store := atomicStore(t)
	set := atomicReservationSet()
	set[1].Amount.Value = 2_000
	_, err := store.Reserve(context.Background(), app.ReserveCommand{Reservations: set, At: time.Date(2026, 7, 10, 12, 1, 0, 0, time.UTC)})
	if !errors.Is(err, app.ErrReservationConflict) {
		t.Fatalf("Reserve error = %v, want reservation conflict", err)
	}
	if got := app.ReservationFailureRuleID(err); got != "atomic.budget" {
		t.Fatalf("failure rule = %q, want atomic.budget", got)
	}
	requests := limitRowFromStore(t, store, "atomic.requests", string(domain.AmountUnitRequests))
	if requests.Reserved != 0 {
		t.Fatalf("requests reserved = %d, want atomic rollback", requests.Reserved)
	}
}

func TestMemoryStoreReservationSetFailureDoesNotPartiallySettleOrRelease(t *testing.T) {
	t.Parallel()

	store := atomicStore(t)
	set := atomicReservationSet()
	if _, err := store.Reserve(context.Background(), app.ReserveCommand{
		Reservations: set,
		At:           time.Date(2026, 7, 10, 12, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	badSettle := app.SettleCommand{
		Reservations: []app.SettlementDescriptor{
			{Reservation: set[0], FinalUsage: domain.Amount{Unit: domain.AmountUnitRequests, Value: 2}},
			{Reservation: set[1], FinalCost: domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 200, Currency: "eur"}},
		},
		Kind:              app.SettlementKindFinal,
		FinalUsagePresent: true,
		FinalCostPresent:  true,
		At:                time.Date(2026, 7, 10, 12, 2, 0, 0, time.UTC),
	}
	if _, err := store.Settle(context.Background(), badSettle); !errors.Is(err, app.ErrReservationConflict) {
		t.Fatalf("bad Settle error = %v, want reservation conflict", err)
	}
	requests := limitRowFromStore(t, store, "atomic.requests", string(domain.AmountUnitRequests))
	budget := limitRowFromStore(t, store, "atomic.budget", string(domain.AmountUnitMoneyNano))
	if requests.Consumed != 0 || requests.Reserved != 3 || budget.Consumed != 0 || budget.Reserved != 250 {
		t.Fatalf("failed settle partially mutated rows: requests=%+v budget=%+v", requests, budget)
	}

	badRelease := app.ReleaseCommand{
		Reservations: []app.ReleaseDescriptor{
			{Reservation: set[0]},
			{Reservation: app.ReservationDescriptor{
				RuleID:         set[1].RuleID,
				ReservationKey: set[1].ReservationKey,
				Amount:         domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 250, Currency: "eur"},
			}},
		},
		Kind: app.ReleaseKindLosing,
		At:   time.Date(2026, 7, 10, 12, 3, 0, 0, time.UTC),
	}
	if _, err := store.Release(context.Background(), badRelease); !errors.Is(err, app.ErrReservationConflict) {
		t.Fatalf("bad Release error = %v, want reservation conflict", err)
	}
	requests = limitRow(t, store, "atomic.requests", string(domain.AmountUnitRequests))
	budget = limitRow(t, store, "atomic.budget", string(domain.AmountUnitMoneyNano))
	if requests.Reserved != 3 || budget.Reserved != 250 {
		t.Fatalf("failed release partially mutated rows: requests=%+v budget=%+v", requests, budget)
	}
}

func TestSQLiteStoreReservationSetRollbackKeepsProjectionAndDatabaseAtomic(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rows, err := authoritystore.LimitRowsFromRules(atomicSetRules(), at)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	dsn := sqliteAuthorityDSN(filepath.Join(t.TempDir(), "atomic.db"))
	db := openSQLiteAuthorityBun(t, dsn)
	store, err := authoritystore.NewDurable(context.Background(), db, authoritystore.Config{
		StoreID:   "sqlite-atomic-set",
		Backing:   domain.BackingCapabilityAtomic,
		Readiness: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		LimitRows: rows,
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	defer func() { _ = store.Close() }()
	set := atomicReservationSet()
	if _, err := store.Reserve(context.Background(), app.ReserveCommand{Reservations: set, At: at.Add(time.Minute)}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_, err = store.Settle(context.Background(), app.SettleCommand{
		Reservations: []app.SettlementDescriptor{
			{Reservation: set[0], FinalUsage: domain.Amount{Unit: domain.AmountUnitRequests, Value: 2}},
			{Reservation: set[1], FinalCost: domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 200, Currency: "eur"}},
		},
		Kind:              app.SettlementKindFinal,
		FinalUsagePresent: true,
		FinalCostPresent:  true,
		At:                at.Add(2 * time.Minute),
	})
	if !errors.Is(err, app.ErrReservationConflict) {
		t.Fatalf("failed durable Settle error = %v, want reservation conflict", err)
	}
	requests := limitRowFromStore(t, store, "atomic.requests", string(domain.AmountUnitRequests))
	budget := limitRowFromStore(t, store, "atomic.budget", string(domain.AmountUnitMoneyNano))
	if requests.Consumed != 0 || requests.Reserved != 3 || budget.Consumed != 0 || budget.Reserved != 250 {
		t.Fatalf("failed durable settle projection = requests=%+v budget=%+v", requests, budget)
	}
}
