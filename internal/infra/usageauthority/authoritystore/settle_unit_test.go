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

func storeFromRules(t *testing.T, rules []domain.Rule, at time.Time) *authoritystore.MemoryStore {
	t.Helper()
	rows, err := authoritystore.LimitRowsFromRules(rules, at)
	if err != nil {
		t.Fatal(err)
	}
	windows := make(map[string]domain.WindowSpec, len(rules))
	for _, r := range rules {
		windows[r.ID] = r.Window
	}
	return authoritystore.NewMemory(authoritystore.Config{StoreID: "test-store", Backing: domain.BackingCapabilityAtomic, Readiness: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}, LimitRows: rows, RuleWindows: windows})
}

func quotaRule(id string) domain.Rule {
	return domain.Rule{ID: id, Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitRequests, Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100}, Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-quota")}, Model: domain.DimensionMatcher{Value: scope.Known("model-quota")}}}
}
func quotaDimensions() domain.Dimensions {
	return domain.Dimensions{Principal: scope.Known("principal-quota"), Tenant: scope.Known("tenant-quota"), Backend: scope.Known("backend-quota"), Model: scope.Known("model-quota")}
}

func reserveCmd(ruleID, ruleType string, dims domain.Dimensions, amount domain.Amount, at time.Time, source string) app.ReserveCommand {
	key := domain.ReservationKey{LogicalRequestID: "req-" + ruleID, ALegID: "a-" + ruleID, BLegID: "b-" + ruleID, AttemptID: "attempt-" + ruleID, RuleID: ruleID, Sequence: 1}
	return app.ReserveCommand{ReservationKey: key, RuleID: ruleID, RuleType: ruleType, Dimensions: dims, Request: amount, Authority: domain.AuthorityLevelAuthoritative, At: at, SourceKey: source}
}

// Extra arguments preserve the historical test helper call shape; monetary
// arguments are intentionally ignored because UsageAuthority is quantity-only.
func settleCmd(reservationID, ruleID, source string, kind app.SettlementKind, finalUsage, _retired, reservedUsage, estimatedUsage, _retiredEstimate domain.Amount, at time.Time) app.SettleCommand {
	key := domain.ReservationKey{LogicalRequestID: "req-" + ruleID, ALegID: "a-" + ruleID, BLegID: "b-" + ruleID, AttemptID: "attempt-" + ruleID, RuleID: ruleID, Sequence: 1}
	return app.SettleCommand{SettlementKey: domain.SettlementKey{ReservationKey: key, Sequence: 1}, ReservationKey: key, ReservationID: reservationID, RuleID: ruleID, Kind: kind, FinalUsage: finalUsage, ReservedUsage: reservedUsage, EstimatedUsage: estimatedUsage, Authority: domain.AuthorityLevelAuthoritative, At: at, SourceKey: source}
}

func limitRow(t *testing.T, store *authoritystore.MemoryStore, ruleID, unit string) controlplane.AccountingLimitStatusRow {
	t.Helper()
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{RuleID: ruleID, Unit: unit, Limit: 10, Visibility: controlplane.VisibilityDefault})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("LimitStatus(%s): err=%v rows=%d", ruleID, err, len(page.Items))
	}
	return page.Items[0]
}

func TestSettleQuotaConsumesFinalUsage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{quotaRule("quota-1")}, at)
	reserved := domain.Amount{Unit: domain.AmountUnitRequests, Value: 60}
	res, err := store.Reserve(context.Background(), reserveCmd("quota-1", "quota", quotaDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := store.Settle(context.Background(), settleCmd(res.ReservationID, "quota-1", "settle-1", app.SettlementKindFinal, domain.Amount{Unit: domain.AmountUnitRequests, Value: 45}, domain.Amount{}, reserved, domain.Amount{Unit: domain.AmountUnitRequests, Value: 45}, domain.Amount{}, at.Add(time.Minute)))
	if err != nil || !settled.Applied || settled.ReleasedDelta.Value != 15 {
		t.Fatalf("settle=%#v err=%v", settled, err)
	}
	if got := limitRow(t, store, "quota-1", string(domain.AmountUnitRequests)); got.Consumed != 45 {
		t.Fatalf("consumed=%d", got.Consumed)
	}
}

func TestSettleQuotaNonFinalFallsBackToEstimatedUsage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{quotaRule("quota-1")}, at)
	reserved := domain.Amount{Unit: domain.AmountUnitRequests, Value: 60}
	res, err := store.Reserve(context.Background(), reserveCmd("quota-1", "quota", quotaDimensions(), reserved, at, "reserve-1"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Settle(context.Background(), settleCmd(res.ReservationID, "quota-1", "settle-1", app.SettlementKindPartial, domain.Amount{}, domain.Amount{}, reserved, domain.Amount{Unit: domain.AmountUnitRequests, Value: 50}, domain.Amount{}, at.Add(time.Minute)))
	if err != nil || !got.Applied {
		t.Fatalf("settle=%#v err=%v", got, err)
	}
	if row := limitRow(t, store, "quota-1", string(domain.AmountUnitRequests)); row.Consumed != 50 {
		t.Fatalf("consumed=%d", row.Consumed)
	}
}
