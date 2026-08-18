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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func atomicSetRules() []domain.Rule {
	return []domain.Rule{
		{ID: "atomic.requests", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitRequests, Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-atomic")}}},
		{ID: "atomic.tokens", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitInputTokens, Limit: domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 1000}, Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-atomic")}}},
	}
}

func atomicSetDimensions() domain.Dimensions {
	return domain.Dimensions{Principal: scope.Known("principal-atomic"), Backend: scope.Known("backend-atomic"), Model: scope.Known("model-atomic")}
}
func atomicReservationKey(ruleID string) domain.ReservationKey {
	return domain.ReservationKey{LogicalRequestID: "request-atomic", ALegID: "a-atomic", BLegID: "b-atomic", AttemptID: "attempt-atomic", RuleID: ruleID, Sequence: 1}
}
func atomicReservationSet() app.ReservationSet {
	dims := atomicSetDimensions()
	return app.ReservationSet{
		{RuleID: "atomic.requests", Kind: domain.RuleKindQuota, Unit: domain.AmountUnitRequests, Dimensions: dims, ReservationKey: atomicReservationKey("atomic.requests"), Amount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 3}, SourceKey: "atomic-reserve-requests"},
		{RuleID: "atomic.tokens", Kind: domain.RuleKindQuota, Unit: domain.AmountUnitInputTokens, Dimensions: dims, ReservationKey: atomicReservationKey("atomic.tokens"), Amount: domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 250}, SourceKey: "atomic-reserve-tokens"},
	}
}

func atomicStore(t *testing.T) *authoritystore.MemoryStore {
	t.Helper()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rows, err := authoritystore.LimitRowsFromRules(atomicSetRules(), at)
	if err != nil {
		t.Fatal(err)
	}
	return authoritystore.NewMemory(authoritystore.Config{StoreID: "atomic-set", Backing: domain.BackingCapabilityAtomic, Readiness: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}, LimitRows: rows})
}

func limitRowFromStore(t *testing.T, store app.StateStore, ruleID, unit string) controlplane.AccountingLimitStatusRow {
	t.Helper()
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{RuleID: ruleID, Unit: unit, Limit: 10, Visibility: controlplane.VisibilityDefault})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("LimitStatus(%s): err=%v rows=%d", ruleID, err, len(page.Items))
	}
	return page.Items[0]
}

func TestMemoryStoreReservationSetIsAtomicAcrossQuantities(t *testing.T) {
	t.Parallel()
	store := atomicStore(t)
	set := atomicReservationSet()
	reserve, err := store.Reserve(context.Background(), app.ReserveCommand{Reservations: set, ReservationKey: set[0].ReservationKey, RuleID: set[0].RuleID, RuleType: string(set[0].Kind), Dimensions: set[0].Dimensions, Request: set[0].Amount, At: time.Now().UTC()})
	if err != nil || !reserve.Applied || len(reserve.Reservations) != 2 {
		t.Fatalf("reserve=%#v err=%v", reserve, err)
	}
	if got := limitRowFromStore(t, store, "atomic.requests", string(domain.AmountUnitRequests)); got.Reserved != 3 {
		t.Fatalf("request reserved=%d", got.Reserved)
	}
	if got := limitRowFromStore(t, store, "atomic.tokens", string(domain.AmountUnitInputTokens)); got.Reserved != 250 {
		t.Fatalf("token reserved=%d", got.Reserved)
	}
	settle, err := store.Settle(context.Background(), app.SettleCommand{Reservations: []app.SettlementDescriptor{{Reservation: set[0], FinalUsage: domain.Amount{Unit: domain.AmountUnitRequests, Value: 2}}, {Reservation: set[1], FinalUsage: domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 200}}}, Kind: app.SettlementKindFinal, FinalUsagePresent: true, At: time.Now().UTC()})
	if err != nil || !settle.Applied || len(settle.Mutations) != 2 {
		t.Fatalf("settle=%#v err=%v", settle, err)
	}
}

func TestMemoryStoreReservationSetFailureRollsBackAllQuantities(t *testing.T) {
	t.Parallel()
	store := atomicStore(t)
	set := atomicReservationSet()
	set[1].Amount.Value = 2000
	_, err := store.Reserve(context.Background(), app.ReserveCommand{Reservations: set, At: time.Now().UTC()})
	if !errors.Is(err, app.ErrReservationConflict) || app.ReservationFailureRuleID(err) != "atomic.tokens" {
		t.Fatalf("err=%v rule=%q", err, app.ReservationFailureRuleID(err))
	}
	if got := limitRowFromStore(t, store, "atomic.requests", string(domain.AmountUnitRequests)); got.Reserved != 0 {
		t.Fatalf("request reservation leaked: %d", got.Reserved)
	}
}
