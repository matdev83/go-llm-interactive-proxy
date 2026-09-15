package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestPhase11AllocationTargetQuery_IsolatesCompleteSubjectScope(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	first := phase11ScopedAllocationRecord(t, "allocation-target-scope-a", "tenant-a", "account-a", "period-a", "shared-b-leg", time.Unix(300, 0).UTC())
	second := phase11ScopedAllocationRecord(t, "allocation-target-scope-b", "tenant-b", "account-b", "period-b", "shared-b-leg", time.Unix(301, 0).UTC())
	third := phase11ScopedAllocationRecord(t, "allocation-target-scope-c", "tenant-a", "account-a", "period-c", "shared-b-leg", time.Unix(302, 0).UTC())
	for _, record := range []economics.AllocationRecord{first, second, third} {
		require.NoError(t, store.AppendAllocation(ctx, record))
	}

	query := first.Targets[0].Target
	page, err := store.ListAllocations(ctx, economics.AllocationQuery{
		StoreID:       "test",
		TargetSubject: &query,
		Limit:         10,
	})
	require.NoError(t, err)
	require.Len(t, page.Allocations, 1)
	require.Equal(t, first.ID, page.Allocations[0].ID)
}

func TestPhase11AllocationTargetQuery_IsolatesPeriodWindowAndResetScope(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	first := phase11WindowScopedAllocationRecord(t, "allocation-target-window-a", "tenant-window", "account-window", "period-window", "pool-a", "window-a", time.Unix(400, 0), time.Unix(300, 0).UTC())
	second := phase11WindowScopedAllocationRecord(t, "allocation-target-window-b", "tenant-window", "account-window", "period-window", "pool-b", "window-b", time.Unix(500, 0), time.Unix(301, 0).UTC())
	require.NoError(t, store.AppendAllocation(ctx, first))
	require.NoError(t, store.AppendAllocation(ctx, second))

	query := first.Targets[0].Target
	page, err := store.ListAllocations(ctx, economics.AllocationQuery{StoreID: "test", TargetSubject: &query, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Allocations, 1)
	require.Equal(t, first.ID, page.Allocations[0].ID)
}

func TestPhase11AllocationTargetQuery_CursorBindsCompleteSubjectScope(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)

	first := phase11ScopedAllocationRecord(t, "allocation-target-cursor-a", "tenant-a", "account-a", "period-a", "shared-b-leg", time.Unix(300, 0).UTC())
	second := phase11ScopedAllocationRecord(t, "allocation-target-cursor-b", "tenant-a", "account-a", "period-a", "shared-b-leg", time.Unix(301, 0).UTC())
	other := phase11ScopedAllocationRecord(t, "allocation-target-cursor-other", "tenant-b", "account-b", "period-b", "shared-b-leg", time.Unix(302, 0).UTC())
	for _, record := range []economics.AllocationRecord{first, second, other} {
		require.NoError(t, store.AppendAllocation(ctx, record))
	}

	filter := first.Targets[0].Target
	firstPage, err := store.ListAllocations(ctx, economics.AllocationQuery{
		StoreID:       "test",
		TargetSubject: &filter,
		Limit:         1,
	})
	require.NoError(t, err)
	require.Len(t, firstPage.Allocations, 1)
	require.Equal(t, first.ID, firstPage.Allocations[0].ID)
	require.NotEmpty(t, firstPage.NextCursor)

	secondPage, err := store.ListAllocations(ctx, economics.AllocationQuery{
		StoreID:       "test",
		TargetSubject: &filter,
		Limit:         1,
		Cursor:        firstPage.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, secondPage.Allocations, 1)
	require.Equal(t, second.ID, secondPage.Allocations[0].ID)

	for _, changed := range []metering.SubjectRef{
		{Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant-b", AccountID: "account-a", PeriodID: "period-a", BLegID: "shared-b-leg"},
		{Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant-a", AccountID: "account-b", PeriodID: "period-a", BLegID: "shared-b-leg"},
		{Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant-a", AccountID: "account-a", PeriodID: "period-b", BLegID: "shared-b-leg"},
	} {
		_, err := store.ListAllocations(ctx, economics.AllocationQuery{
			StoreID:       "test",
			TargetSubject: &changed,
			Limit:         1,
			Cursor:        firstPage.NextCursor,
		})
		require.ErrorIs(t, err, ErrInvalidEconomicsCursor)
	}
}

func TestPhase11AllocationTargetQuery_DenormalizedScopeDriftFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	record := phase11ScopedAllocationRecord(t, "allocation-target-drift", "tenant-drift", "account-drift", "period-drift", "shared-b-leg", time.Unix(300, 0).UTC())
	require.NoError(t, store.AppendAllocation(ctx, record))

	dropAllocationTargetImmutabilityTriggers(t, store, ctx)
	_, err := store.db.NewRaw(`UPDATE billing_allocation_targets SET target_tenant_id = ? WHERE allocation_id = ? AND allocation_version = ?`, "tenant-corrupt", record.ID, record.Version).Exec(ctx)
	require.NoError(t, err)

	query := record.Targets[0].Target
	_, err = store.ListAllocations(ctx, economics.AllocationQuery{
		StoreID:       "test",
		TargetSubject: &query,
		Limit:         10,
	})
	require.ErrorIs(t, err, ErrIdentityConflict)
}

func TestPhase11AllocationTargetScopeMigration_BackfillsLegacyRowsAndPreservesAuthority(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	record := phase11ScopedAllocationRecord(t, "allocation-target-upgrade", "tenant-upgrade", "account-upgrade", "period-upgrade", "shared-b-leg", time.Unix(300, 0).UTC())
	require.NoError(t, store.AppendAllocation(ctx, record))

	var before struct {
		CanonicalJSON string `bun:"canonical_json"`
		Fingerprint   string `bun:"fingerprint"`
	}
	require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint FROM billing_allocations WHERE allocation_id = ? AND allocation_version = ?`, record.ID, record.Version).Scan(ctx, &before))

	dropAllocationTargetImmutabilityTriggers(t, store, ctx)
	clearAllocationTargetScopeColumns(t, store, ctx, record.ID, record.Version)
	removeAllocationTargetScopeMigrationRecord(t, store, ctx)

	require.NoError(t, Migrate(ctx, store.db))

	query := record.Targets[0].Target
	page, err := store.ListAllocations(ctx, economics.AllocationQuery{StoreID: "test", TargetSubject: &query, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Allocations, 1)
	require.Equal(t, record.ID, page.Allocations[0].ID)

	var projection struct {
		TenantID  string `bun:"target_tenant_id"`
		AccountID string `bun:"account_id"`
		PeriodID  string `bun:"period_id"`
		PoolID    string `bun:"target_pool_id"`
		WindowID  string `bun:"target_window_id"`
	}
	require.NoError(t, store.db.NewRaw(`SELECT target_tenant_id, account_id, period_id, target_pool_id, target_window_id FROM billing_allocation_targets WHERE allocation_id = ? AND allocation_version = ? AND target_id = ?`, record.ID, record.Version, query.BLegID).Scan(ctx, &projection))
	require.Equal(t, query.TenantID, projection.TenantID)
	require.Equal(t, query.AccountID, projection.AccountID)
	require.Equal(t, query.PeriodID, projection.PeriodID)
	require.Equal(t, query.PoolID, projection.PoolID)
	require.Equal(t, query.WindowID, projection.WindowID)

	var after struct {
		CanonicalJSON string `bun:"canonical_json"`
		Fingerprint   string `bun:"fingerprint"`
	}
	require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint FROM billing_allocations WHERE allocation_id = ? AND allocation_version = ?`, record.ID, record.Version).Scan(ctx, &after))
	require.Equal(t, before, after)
}

func TestPhase11AllocationTargetScopeMigration_FileReopenBackfillsPendingRows(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "allocation-target-scope.db")))

	firstSQL, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	firstSQL.SetMaxOpenConns(4)
	firstDB, err := db.NewBunDB(firstSQL, db.DialectSQLite)
	require.NoError(t, err)
	first, err := NewDurableStore(ctx, firstDB, Config{StoreID: "restart-target-scope"})
	require.NoError(t, err)
	record := phase11ScopedAllocationRecord(t, "allocation-target-restart", "tenant-restart", "account-restart", "period-restart", "shared-b-leg", time.Unix(300, 0).UTC())
	record.SourceSubject.StoreID = "restart-target-scope"
	for i := range record.Targets {
		if !record.Targets[i].Unallocated {
			record.Targets[i].Target.StoreID = "restart-target-scope"
		}
	}
	require.NoError(t, first.AppendAllocation(ctx, record))
	dropAllocationTargetImmutabilityTriggers(t, first, ctx)
	clearAllocationTargetScopeColumns(t, first, ctx, record.ID, record.Version)
	removeAllocationTargetScopeMigrationRecord(t, first, ctx)
	require.NoError(t, first.Close())
	require.NoError(t, firstSQL.Close())

	secondSQL, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	secondSQL.SetMaxOpenConns(4)
	secondDB, err := db.NewBunDB(secondSQL, db.DialectSQLite)
	require.NoError(t, err)
	second, err := NewDurableStore(ctx, secondDB, Config{StoreID: "restart-target-scope"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	query := record.Targets[0].Target
	page, err := second.ListAllocations(ctx, economics.AllocationQuery{StoreID: "restart-target-scope", TargetSubject: &query, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Allocations, 1)
	require.Equal(t, record.ID, page.Allocations[0].ID)
}

func phase11ScopedAllocationRecord(t *testing.T, id, tenant, account, period, targetID string, createdAt time.Time) economics.AllocationRecord {
	t.Helper()
	record := phase11AllocationRecord(t, id, 1)
	record.SourceSubject.TenantID = tenant
	record.SourceSubject.AccountID = account
	record.SourceSubject.PeriodID = period
	record.CreatedAt = createdAt
	record.Targets[0].TargetID = targetID
	record.Targets[0].Target.TenantID = tenant
	record.Targets[0].Target.AccountID = account
	record.Targets[0].Target.PeriodID = period
	record.Targets[0].Target.BLegID = targetID
	return record
}

func phase11WindowScopedAllocationRecord(t *testing.T, id, tenant, account, period, pool, window string, resetAt, createdAt time.Time) economics.AllocationRecord {
	t.Helper()
	record := phase11AllocationRecord(t, id, 1)
	record.SourceSubject.TenantID = tenant
	record.SourceSubject.AccountID = account
	record.SourceSubject.PeriodID = period
	record.CreatedAt = createdAt
	record.Targets[0].TargetID = "shared-request"
	record.Targets[0].Target = metering.SubjectRef{
		Kind: metering.SubjectRequest, StoreID: "test", TenantID: tenant, AccountID: account,
		RequestID: "shared-request", PeriodID: period, PoolID: pool, WindowID: window,
		ResetAt: resetAt.UTC(), StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC(),
	}
	return record
}

func dropAllocationTargetImmutabilityTriggers(t *testing.T, store *DurableStore, ctx context.Context) {
	t.Helper()
	_, err := store.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS billing_allocation_targets_immutable_update`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS billing_allocation_targets_immutable_delete`)
	require.NoError(t, err)
}

func clearAllocationTargetScopeColumns(t *testing.T, store *DurableStore, ctx context.Context, allocationID string, version uint64) {
	t.Helper()
	_, err := store.db.NewRaw(`
		UPDATE billing_allocation_targets SET
			account_id = '', period_id = '', target_tenant_id = '', target_pool_id = '',
			target_window_id = '', target_reset_at_unix = 0, target_start_at_unix = 0,
			target_end_at_unix = 0
		WHERE allocation_id = ? AND allocation_version = ?`, allocationID, version).Exec(ctx)
	require.NoError(t, err)
}

func removeAllocationTargetScopeMigrationRecord(t *testing.T, store *DurableStore, ctx context.Context) {
	t.Helper()
	_, err := store.db.NewRaw(`DELETE FROM bun_billing_migrations WHERE name = ?`, BillingAllocationTargetScopeMigrationName).Exec(ctx)
	require.NoError(t, err)
}

func TestPhase11AllocationTargetScopeMigration_InvalidCanonicalFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	record := phase11ScopedAllocationRecord(t, "allocation-target-invalid", "tenant-invalid", "account-invalid", "period-invalid", "shared-b-leg", time.Unix(300, 0).UTC())
	require.NoError(t, store.AppendAllocation(ctx, record))

	dropAllocationTargetImmutabilityTriggers(t, store, ctx)
	_, err := store.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS billing_allocations_immutable_update`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS billing_allocations_immutable_delete`)
	require.NoError(t, err)
	clearAllocationTargetScopeColumns(t, store, ctx, record.ID, record.Version)
	_, err = store.db.ExecContext(ctx, `UPDATE billing_allocations SET canonical_json = ? WHERE allocation_id = ?`, `{"version":2,"id":"broken"}`, record.ID)
	require.NoError(t, err)
	removeAllocationTargetScopeMigrationRecord(t, store, ctx)

	err = Migrate(ctx, store.db)
	require.Error(t, err)

	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, BillingAllocationTargetScopeMigrationName).Scan(ctx, &count))
	require.Zero(t, count)
	var tenant string
	require.NoError(t, store.db.NewRaw(`SELECT target_tenant_id FROM billing_allocation_targets WHERE allocation_id = ?`, record.ID).Scan(ctx, &tenant))
	require.Empty(t, tenant)

}
