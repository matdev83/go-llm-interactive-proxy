package authoritystore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func reconcileRow(ruleID string, limit int64) controlplane.AccountingLimitStatusRow {
	return controlplane.AccountingLimitStatusRow{
		RuleID:        ruleID,
		RuleType:      string(domain.RuleKindQuota),
		Unit:          string(domain.AmountUnitRequests),
		Limit:         limit,
		Remaining:     limit,
		Authority:     controlplane.AccountingAuthoritySourceAuthoritative,
		EvidenceState: controlplane.EvidenceRecorded,
	}
}

func reconcileReserveCommand(ruleID, source string, value int64) app.ReserveCommand {
	return app.ReserveCommand{
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: source,
			AttemptID:        source,
			RuleID:           ruleID,
			Sequence:         1,
		},
		RuleID:      ruleID,
		RuleType:    string(domain.RuleKindQuota),
		Request:     domain.Amount{Unit: domain.AmountUnitRequests, Value: value},
		Authority:   domain.AuthorityLevelAuthoritative,
		At:          time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
		SourceKey:   source,
		Correlation: controlplane.Correlation{RequestID: source},
	}
}

func openReconcileDB(t *testing.T, dsn string) *bun.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("bun open: %v", err)
	}
	return bunDB
}

func TestDurableStoreReconcilesConfiguredLimitsOnReopen(t *testing.T) {
	t.Parallel()
	dsn := sqliteAuthorityDSN(filepath.Join(t.TempDir(), "authority.db"))
	ctx := context.Background()

	firstDB := openReconcileDB(t, dsn)
	first, err := authoritystore.NewDurable(ctx, firstDB, authoritystore.Config{
		StoreID:   "reconcile-test",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: []controlplane.AccountingLimitStatusRow{reconcileRow("old-rule", 100)},
	})
	if err != nil {
		t.Fatalf("first NewDurable: %v", err)
	}
	if result, err := first.Reserve(ctx, reconcileReserveCommand("old-rule", "old-reservation", 30)); err != nil || !result.Applied {
		t.Fatalf("old reservation = %#v, err=%v", result, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	secondDB := openReconcileDB(t, dsn)
	second, err := authoritystore.NewDurable(ctx, secondDB, authoritystore.Config{
		StoreID: "reconcile-test", Backing: domain.BackingCapabilityAtomic,
		LimitRows: []controlplane.AccountingLimitStatusRow{
			reconcileRow("old-rule", 10),
			reconcileRow("new-rule", 50),
		},
	})
	if err != nil {
		t.Fatalf("reopen NewDurable: %v", err)
	}
	defer func() { _ = second.Close() }()

	page, err := second.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{RuleID: "old-rule", Limit: 10})
	if err != nil {
		t.Fatalf("old limit status: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("old limit rows = %d, want 1", len(page.Items))
	}
	row := page.Items[0]
	if row.Limit != 10 || row.Reserved != 30 || row.Remaining != 0 {
		t.Fatalf("reconciled row = %#v, want limit=10 reserved=30 remaining=0", row)
	}

	added, err := second.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{RuleID: "new-rule", Limit: 10})
	if err != nil {
		t.Fatalf("new limit status: %v", err)
	}
	if len(added.Items) != 1 || added.Items[0].Limit != 50 {
		t.Fatalf("new rule was not seeded: %#v", added.Items)
	}
	if result, err := second.Reserve(ctx, reconcileReserveCommand("new-rule", "new-reservation", 20)); err != nil || !result.Applied {
		t.Fatalf("new rule reservation = %#v, err=%v", result, err)
	}
}

func TestDurableStoreRemovedRuleCannotAuthorizeAfterReopen(t *testing.T) {
	t.Parallel()
	dsn := sqliteAuthorityDSN(filepath.Join(t.TempDir(), "authority.db"))
	ctx := context.Background()
	firstDB := openReconcileDB(t, dsn)
	first, err := authoritystore.NewDurable(ctx, firstDB, authoritystore.Config{
		StoreID: "removed-rule-test", Backing: domain.BackingCapabilityAtomic,
		LimitRows: []controlplane.AccountingLimitStatusRow{reconcileRow("removed-rule", 100)},
	})
	if err != nil {
		t.Fatalf("first NewDurable: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	secondDB := openReconcileDB(t, dsn)
	second, err := authoritystore.NewDurable(ctx, secondDB, authoritystore.Config{
		StoreID: "removed-rule-test", Backing: domain.BackingCapabilityAtomic,
	})
	if err != nil {
		t.Fatalf("reopen NewDurable: %v", err)
	}
	defer func() { _ = second.Close() }()
	if _, err := second.Reserve(ctx, reconcileReserveCommand("removed-rule", "removed-reservation", 1)); err == nil {
		t.Fatal("removed rule must not authorize a new reservation")
	}
	cmd := reconcileReserveCommand("removed-rule", "removed-active-limit", 1)
	if row, configured, err := second.ActiveLimit(ctx, app.ActiveLimitQuery{RuleID: "removed-rule", Dimensions: cmd.Dimensions, At: cmd.At}); err != nil || configured {
		t.Fatalf("removed ActiveLimit row=%#v configured=%v err=%v, want unconfigured", row, configured, err)
	}
}
