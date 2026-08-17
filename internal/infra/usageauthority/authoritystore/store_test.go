package authoritystore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

// sqliteAuthorityDSN builds a SQLite DSN with the pragmas the durable authority
// store expects plus _txlock=immediate so write transactions open as
// BEGIN IMMEDIATE and serialize concurrent writers (requirement 11.1).
func sqliteAuthorityDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_txlock=immediate"
}

func TestSQLiteStore_ConcurrentIdenticalReserveIsIdempotent(t *testing.T) {
	t.Parallel()
	dsn := sqliteAuthorityDSN(filepath.Join(t.TempDir(), "authority.db"))
	seed := authoritystore.Config{StoreID: "identical", Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness()}
	dbA := openSQLiteAuthorityBun(t, dsn)
	storeA, err := authoritystore.NewDurable(context.Background(), dbA, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeA.Close() }()
	dbB := openSQLiteAuthorityBun(t, dsn)
	storeB, err := authoritystore.NewDurable(context.Background(), dbB, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeB.Close() }()

	cmd := strictReserveCommandForPersistence()
	start := make(chan struct{})
	results := make(chan struct {
		out app.ReserveResult
		err error
	}, 2)
	var wg sync.WaitGroup
	for _, store := range []app.StateStore{storeA, storeB} {
		wg.Add(1)
		go func(store app.StateStore) {
			defer wg.Done()
			<-start
			out, err := store.Reserve(context.Background(), cmd)
			results <- struct {
				out app.ReserveResult
				err error
			}{out: out, err: err}
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	applied := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("identical reserve: %v", result.err)
		}
		if result.out.Applied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("applied results = %d, want exactly one", applied)
	}
	page, err := storeA.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{RuleID: "rule-strict", Unit: string(domain.AmountUnitRequests), Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("LimitStatus = %#v, err=%v", page, err)
	}
	if page.Items[0].Reserved != 60 {
		t.Fatalf("reserved = %d, want one reservation (60)", page.Items[0].Reserved)
	}
}

// openSQLiteAuthorityBun opens a Bun-backed SQLite database for the authority
// store tests. The returned *bun.DB owns the underlying *sql.DB (Close closes
// both). MaxOpenConns is set to 1 so BEGIN IMMEDIATE writers serialize through a
// single connection and never self-deadlock on the same file.
func openSQLiteAuthorityBun(t *testing.T, dsn string) *bun.DB {
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

// openSQLiteAuthorityRaw opens a Bun-backed SQLite database and also returns the
// underlying *sql.DB so tests can introspect sqlite_master. The caller closes
// either handle (closing the *bun.DB closes the *sql.DB).
func openSQLiteAuthorityRaw(t *testing.T, dsn string) (*bun.DB, *sql.DB) {
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
	return bunDB, sqlDB
}

type sqliteFactory struct{}

func (sqliteFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authority.db")
	bunDB := openSQLiteAuthorityBun(t, sqliteAuthorityDSN(path))
	store, err := authoritystore.NewDurable(context.Background(), bunDB, authoritystore.Config{
		StoreID:   "sqlite-test",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		_ = bunDB.Close()
		t.Fatalf("NewDurable: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func TestSQLiteStore_Contract(t *testing.T) {
	t.Parallel()
	contract.RunSuite(t, sqliteFactory{})
}

func TestSQLiteStore_Migration_CreatesTables(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "authority.db")
	bunDB, sqlDB := openSQLiteAuthorityRaw(t, sqliteAuthorityDSN(path))
	defer func() { _ = sqlDB.Close() }()

	store, err := authoritystore.NewDurable(context.Background(), bunDB, authoritystore.Config{
		StoreID:   "sqlite-migration",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	defer func() { _ = store.Close() }()

	for _, table := range []string{
		"usage_authority_state",
		"usage_authority_limit_rows",
		"usage_authority_decisions",
		"usage_authority_reservations",
	} {
		var name string
		if err := sqlDB.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("lookup table %s: %v", table, err)
		}
		if name != table {
			t.Fatalf("table name = %q, want %q", name, table)
		}
	}
	if err := authoritystore.Migrate(context.Background(), bunDB); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestSQLiteStore_PersistenceRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := sqliteAuthorityDSN(filepath.Join(dir, "authority.db"))

	bunDB := openSQLiteAuthorityBun(t, dsn)

	store, err := authoritystore.NewDurable(context.Background(), bunDB, authoritystore.Config{
		StoreID:   "sqlite-roundtrip",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}

	if _, err := store.Reserve(context.Background(), strictReserveCommandForPersistence()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopenedDB := openSQLiteAuthorityBun(t, dsn)
	reopened, err := authoritystore.NewDurable(context.Background(), reopenedDB, authoritystore.Config{
		StoreID:   "sqlite-roundtrip",
		Backing:   domain.BackingCapabilityAtomic,
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("reopen NewDurable: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	page, err := reopened.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known("principal-1"),
				TenantID:    scope.Known("tenant-1"),
			},
			BackendID: "backend-1",
			Model:     "model-1",
		},
		RuleID:     "rule-strict",
		Unit:       string(domain.AmountUnitRequests),
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus after reopen: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("LimitStatus after reopen items = %d, want 1", len(page.Items))
	}
}

func TestSQLiteStore_ReadinessUnavailableAfterClose(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "authority.db")
	bunDB := openSQLiteAuthorityBun(t, sqliteAuthorityDSN(path))
	store, err := authoritystore.NewDurable(context.Background(), bunDB, authoritystore.Config{
		StoreID:   "sqlite-readiness",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	status, readErr := store.CheckReadiness(context.Background())
	if readErr == nil {
		t.Fatalf("CheckReadiness must fail after close")
	}
	if status.State != domain.AuthorityStateUnavailable {
		t.Fatalf("CheckReadiness state = %v, want unavailable", status.State)
	}
	if !errors.Is(readErr, app.ErrUnavailable) {
		t.Fatalf("CheckReadiness error = %v, want app.ErrUnavailable", readErr)
	}
	if strings.Contains(readErr.Error(), "database is closed") || strings.Contains(readErr.Error(), "sql:") {
		t.Fatalf("CheckReadiness error leaked driver text: %q", readErr.Error())
	}
}

func TestSQLiteStore_ClosedStoreReturnsUnavailable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "authority.db")
	bunDB := openSQLiteAuthorityBun(t, sqliteAuthorityDSN(path))
	store, err := authoritystore.NewDurable(context.Background(), bunDB, authoritystore.Config{
		StoreID:   "sqlite-closed",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "reserve",
			run: func(t *testing.T) {
				t.Helper()
				_, err := store.Reserve(context.Background(), strictReserveCommandForPersistence())
				assertUnavailableError(t, "Reserve", err)
			},
		},
		{
			name: "settle",
			run: func(t *testing.T) {
				t.Helper()
				_, err := store.Settle(context.Background(), strictSettleCommandForPersistence())
				assertUnavailableError(t, "Settle", err)
			},
		},
		{
			name: "release",
			run: func(t *testing.T) {
				t.Helper()
				_, err := store.Release(context.Background(), strictReleaseCommandForPersistence())
				assertUnavailableError(t, "Release", err)
			},
		},
		{
			name: "limit_status",
			run: func(t *testing.T) {
				t.Helper()
				_, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{})
				assertUnavailableError(t, "LimitStatus", err)
			},
		},
		{
			name: "decision_history",
			run: func(t *testing.T) {
				t.Helper()
				_, err := store.DecisionHistory(context.Background(), controlplane.AccountingDecisionQuery{})
				assertUnavailableError(t, "DecisionHistory", err)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

func strictReserveCommandForPersistence() app.ReserveCommand {
	return app.ReserveCommand{
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "req-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           "rule-strict",
			Sequence:         1,
		},
		RuleID:   "rule-strict",
		RuleType: "quota",
		Dimensions: domain.Dimensions{
			Principal:    scope.Known("principal-1"),
			Tenant:       scope.Known("tenant-1"),
			Organization: scope.Unknown(),
			Workspace:    scope.Known("workspace-1"),
			Project:      scope.Known(""),
			Department:   scope.Unknown(),
			CostCenter:   scope.Unknown(),
			Backend:      scope.Known("backend-1"),
			Model:        scope.Known("model-1"),
			Route:        scope.Known("route-1"),
			PolicyLabels: map[string]scope.Value{"tier": scope.Known("standard")},
		},
		Request:      domain.Amount{Unit: domain.AmountUnitRequests, Value: 60},
		Authority:    domain.AuthorityLevelAuthoritative,
		EstimateOnly: false,
		At:           time.Date(2026, 7, 4, 12, 1, 0, 0, time.UTC),
		SourceKey:    "reserve-1",
	}
}

func strictSettleCommandForPersistence() app.SettleCommand {
	return app.SettleCommand{
		ReservationKey: strictReserveCommandForPersistence().ReservationKey,
		ReservedUsage:  domain.Amount{Unit: domain.AmountUnitRequests, Value: 60},
		FinalUsage:     domain.Amount{Unit: domain.AmountUnitRequests, Value: 40},
		EstimatedUsage: domain.Amount{Unit: domain.AmountUnitRequests, Value: 50},
		Kind:           app.SettlementKindFinal,
		At:             time.Date(2026, 7, 4, 12, 2, 0, 0, time.UTC),
		SourceKey:      "settle-1",
	}
}

func strictReleaseCommandForPersistence() app.ReleaseCommand {
	return app.ReleaseCommand{
		ReservationKey: strictReserveCommandForPersistence().ReservationKey,
		Amount:         domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Kind:           app.ReleaseKindSwallowed,
		At:             time.Date(2026, 7, 4, 12, 3, 0, 0, time.UTC),
		SourceKey:      "release-1",
	}
}

func assertUnavailableError(t *testing.T, op string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s error = nil, want app.ErrUnavailable", op)
	}
	if !errors.Is(err, app.ErrUnavailable) {
		t.Fatalf("%s error = %v, want app.ErrUnavailable", op, err)
	}
	if strings.Contains(err.Error(), "database is closed") || strings.Contains(err.Error(), "sql:") || strings.Contains(err.Error(), "driver") {
		t.Fatalf("%s error leaked driver text: %q", op, err.Error())
	}
}

// TestSQLiteStore_ApplyUsagePersistsAndReplayIsNoOp proves advisory usage
// persists across a reopen and the idempotency map is hydrated from the
// decision ledger so a replayed source key stays a no-op after restart
// (requirement 7.7, 7.8).
func TestSQLiteStore_ApplyUsagePersistsAndReplayIsNoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := sqliteAuthorityDSN(filepath.Join(dir, "authority.db"))

	bunDB := openSQLiteAuthorityBun(t, dsn)
	store, err := authoritystore.NewDurable(context.Background(), bunDB, authoritystore.Config{
		StoreID:   "sqlite-advisory-roundtrip",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}

	advisoryQuery := controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope:     controlplane.ScopeFilters{PrincipalID: scope.Known("principal-2"), WorkspaceID: scope.Known("workspace-2")},
			BackendID: "backend-2",
			Model:     "model-2",
			ALegID:    "a-2",
			BLegID:    "b-2",
		},
		RuleID:     "rule-advisory",
		Unit:       string(domain.AmountUnitRequests),
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	}

	if _, err := store.ApplyUsage(context.Background(), app.ApplyUsageCommand{
		Correlation: controlplane.Correlation{
			TraceID: "trace-2", RequestID: "req-2", ALegID: "a-2", BLegID: "b-2",
			BackendID: "backend-2", Model: "model-2",
		},
		Dimensions: domain.Dimensions{
			Principal:    scope.Known("principal-2"),
			Tenant:       scope.Unknown(),
			Workspace:    scope.Known("workspace-2"),
			Project:      scope.Known(""),
			Backend:      scope.Known("backend-2"),
			Model:        scope.Known("model-2"),
			PolicyLabels: map[string]scope.Value{"tier": scope.Known("advisory")},
		},
		RuleIDs:      []string{"rule-advisory"},
		RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 8},
		At:           time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC),
		SourceKey:    "advisory-persist-1",
	}); err != nil {
		t.Fatalf("ApplyUsage: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopenedDB := openSQLiteAuthorityBun(t, dsn)
	reopened, err := authoritystore.NewDurable(context.Background(), reopenedDB, authoritystore.Config{
		StoreID:   "sqlite-advisory-roundtrip",
		Backing:   domain.BackingCapabilityAtomic,
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("reopen NewDurable: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	page, err := reopened.LimitStatus(context.Background(), advisoryQuery)
	if err != nil {
		t.Fatalf("LimitStatus after reopen: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("LimitStatus after reopen items = %d, want 1", len(page.Items))
	}
	if page.Items[0].Consumed != 18 {
		t.Fatalf("advisory Consumed after reopen = %d, want 18 (10 seeded + 8 applied, persisted)", page.Items[0].Consumed)
	}

	// Replay the same source key after reopen: must be a no-op (idempotency
	// map hydrated from the decision ledger).
	replay, err := reopened.ApplyUsage(context.Background(), app.ApplyUsageCommand{
		Correlation: controlplane.Correlation{
			TraceID: "trace-2", RequestID: "req-2", ALegID: "a-2", BLegID: "b-2",
			BackendID: "backend-2", Model: "model-2",
		},
		Dimensions: domain.Dimensions{
			Principal:    scope.Known("principal-2"),
			Tenant:       scope.Unknown(),
			Workspace:    scope.Known("workspace-2"),
			Project:      scope.Known(""),
			Backend:      scope.Known("backend-2"),
			Model:        scope.Known("model-2"),
			PolicyLabels: map[string]scope.Value{"tier": scope.Known("advisory")},
		},
		RuleIDs:      []string{"rule-advisory"},
		RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 8},
		At:           time.Date(2026, 7, 4, 12, 45, 0, 0, time.UTC),
		SourceKey:    "advisory-persist-1",
	})
	if err != nil {
		t.Fatalf("replay ApplyUsage after reopen: %v", err)
	}
	if replay.Applied {
		t.Fatalf("replay after reopen must not apply: %#v", replay)
	}
	page2, err := reopened.LimitStatus(context.Background(), advisoryQuery)
	if err != nil {
		t.Fatalf("LimitStatus after replay: %v", err)
	}
	if page2.Items[0].Consumed != 18 {
		t.Fatalf("advisory Consumed after replay = %d, want 18 (no double-count after reopen)", page2.Items[0].Consumed)
	}
	correction, err := reopened.ApplyUsage(context.Background(), app.ApplyUsageCommand{
		Correlation: controlplane.Correlation{TraceID: "trace-2", RequestID: "req-2", ALegID: "a-2", BLegID: "b-2", BackendID: "backend-2", Model: "model-2"},
		Dimensions: domain.Dimensions{
			Principal: scope.Known("principal-2"), Tenant: scope.Unknown(), Workspace: scope.Known("workspace-2"), Project: scope.Known(""),
			Backend: scope.Known("backend-2"), Model: scope.Known("model-2"),
			PolicyLabels: map[string]scope.Value{"tier": scope.Known("advisory")},
		},
		RuleIDs: []string{"rule-advisory"}, RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 50},
		At: time.Date(2026, 7, 4, 12, 45, 0, 0, time.UTC), SourceKey: "advisory-persist-1",
	})
	if err != nil || !correction.Applied {
		t.Fatalf("same-authority correction = %#v, err=%v", correction, err)
	}
	page3, err := reopened.LimitStatus(context.Background(), advisoryQuery)
	if err != nil || len(page3.Items) != 1 || page3.Items[0].Consumed != 60 {
		t.Fatalf("corrected advisory row = %#v, err=%v, want consumed 60", page3.Items, err)
	}
}

func TestSQLiteStore_UnreservedFactSurvivesRestartForAuthorityUpgrade(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := sqliteAuthorityDSN(filepath.Join(dir, "authority.db"))
	newStore := func(t *testing.T) *authoritystore.DurableStore {
		t.Helper()
		db := openSQLiteAuthorityBun(t, dsn)
		store, err := authoritystore.NewDurable(context.Background(), db, authoritystore.Config{
			StoreID:   "sqlite-advisory-authority-upgrade",
			Backing:   domain.BackingCapabilityAtomic,
			LimitRows: contract.SeededLimitRows(),
			Readiness: contract.SeededReadiness(),
		})
		if err != nil {
			_ = db.Close()
			t.Fatalf("NewDurable: %v", err)
		}
		return store
	}
	command := func(authority domain.AuthorityLevel, count int64) app.ApplyUsageCommand {
		return app.ApplyUsageCommand{
			Correlation: controlplane.Correlation{
				TraceID: "trace-authority-upgrade", RequestID: "req-authority-upgrade",
				ALegID: "a-authority-upgrade", BLegID: "b-authority-upgrade",
				BackendID: "backend-2", Model: "model-2",
			},
			Dimensions: domain.Dimensions{
				Principal:    scope.Known("principal-2"),
				Tenant:       scope.Unknown(),
				Workspace:    scope.Known("workspace-2"),
				Project:      scope.Known(""),
				Backend:      scope.Known("backend-2"),
				Model:        scope.Known("model-2"),
				PolicyLabels: map[string]scope.Value{"tier": scope.Known("advisory")},
			},
			RuleIDs:      []string{"rule-advisory"},
			RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: count},
			Authority:    authority,
			At:           time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC),
			SourceKey:    "unreserved-authority-upgrade",
		}
	}

	store := newStore(t)
	first, err := store.ApplyUsage(context.Background(), command(domain.AuthorityLevelEstimated, 8))
	if err != nil || !first.Applied {
		t.Fatalf("estimated ApplyUsage = %#v, err=%v", first, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	store = newStore(t)
	upgraded, err := store.ApplyUsage(context.Background(), command(domain.AuthorityLevelAuthoritative, 12))
	if err != nil || !upgraded.Applied {
		t.Fatalf("authoritative ApplyUsage after restart = %#v, err=%v", upgraded, err)
	}
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{RuleID: "rule-advisory", Unit: string(domain.AmountUnitRequests), Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Consumed != 22 {
		t.Fatalf("upgraded advisory row = %#v, err=%v, want consumed 22", page.Items, err)
	}
	corrected, err := store.ApplyUsage(context.Background(), command(domain.AuthorityLevelAuthoritative, 99))
	if err != nil || !corrected.Applied {
		t.Fatalf("same-authority correction = %#v, err=%v, want applied", corrected, err)
	}
	page, err = store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{RuleID: "rule-advisory", Unit: string(domain.AmountUnitRequests), Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Consumed != 109 {
		t.Fatalf("corrected advisory row = %#v, err=%v, want consumed 109", page.Items, err)
	}
	replay, err := store.ApplyUsage(context.Background(), command(domain.AuthorityLevelAuthoritative, 99))
	if err != nil || replay.Applied {
		t.Fatalf("exact authoritative replay = %#v, err=%v, want no-op", replay, err)
	}
	_ = store.Close()
}

var sqliteCrossInstanceSeq atomic.Int64

// TestSQLiteStore_CrossInstanceCannotOverCommit proves two DurableStore
// instances sharing one SQLite authority database (separate in-memory
// projections, like two proxy processes) cannot over-commit a strict window
// (requirement 11.1). The durable flush locks and re-reads the live limit rows
// inside a BEGIN IMMEDIATE transaction before the capacity check runs, so the
// second instance sees the first instance's committed reservation and denies
// rather than double-reserving. The conditional UPDATE on the pre-image
// row_json backstops the lock: a lost update rolls back and returns
// app.ErrReservationConflict.
func TestSQLiteStore_CrossInstanceCannotOverCommit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := sqliteAuthorityDSN(filepath.Join(dir, "authority.db"))
	storeID := fmt.Sprintf("sqlite-cross-%d", sqliteCrossInstanceSeq.Add(1))

	seed := authoritystore.Config{
		StoreID:   storeID,
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	}

	// Instance A opens first and seeds the live rows.
	storeA := openSQLiteAuthorityBun(t, dsn)
	durableA, err := authoritystore.NewDurable(context.Background(), storeA, seed)
	if err != nil {
		_ = storeA.Close()
		t.Fatalf("NewDurable A: %v", err)
	}
	defer func() { _ = durableA.Close() }()

	// Instance B opens against the same file and hydrates from the seeded DB.
	storeB := openSQLiteAuthorityBun(t, dsn)
	durableB, err := authoritystore.NewDurable(context.Background(), storeB, seed)
	if err != nil {
		_ = storeB.Close()
		t.Fatalf("NewDurable B: %v", err)
	}
	defer func() { _ = durableB.Close() }()

	ctx := context.Background()
	reserveA := strictReserveCommandForPersistence()
	reserveA.SourceKey = "cross-reserve-a"
	reserveA.ReservationKey.AttemptID = "attempt-cross-a"
	reserveA.ReservationKey.Sequence = 1
	reserveB := strictReserveCommandForPersistence()
	reserveB.SourceKey = "cross-reserve-b"
	reserveB.ReservationKey.AttemptID = "attempt-cross-b"
	reserveB.ReservationKey.Sequence = 2

	outA, errA := durableA.Reserve(ctx, reserveA)
	if errA != nil {
		t.Fatalf("instance A reserve: %v", errA)
	}
	if !outA.Applied {
		t.Fatalf("instance A reserve must apply: %#v", outA)
	}

	outB, errB := durableB.Reserve(ctx, reserveB)
	if errB == nil {
		t.Fatalf("instance B reserve must be rejected (would over-commit): %#v", outB)
	}
	if !errors.Is(errB, app.ErrReservationConflict) {
		t.Fatalf("instance B reserve error = %v, want app.ErrReservationConflict", errB)
	}

	// The committed DB state must reflect a single reservation (reserved=60,
	// remaining=40), not 120. Query through instance A's projection, which is
	// kept consistent with the committed state.
	page, err := durableA.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known("principal-1"),
				TenantID:    scope.Known("tenant-1"),
			},
			BackendID: "backend-1",
			Model:     "model-1",
		},
		RuleID:     "rule-strict",
		Unit:       string(domain.AmountUnitRequests),
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("LimitStatus items = %d, want 1", len(page.Items))
	}
	if page.Items[0].Reserved != 60 || page.Items[0].Remaining != 40 {
		t.Fatalf("cross-instance totals = reserved=%d remaining=%d, want reserved=60 remaining=40 (no over-commit)",
			page.Items[0].Reserved, page.Items[0].Remaining)
	}

	// Instance B's projection must also stay consistent with the committed DB
	// after the rejected reserve: it must not carry a stale or doubled
	// reservation. Re-reserving a smaller amount that fits must succeed, and
	// instance B's own projection (refreshed inside that reserve) must reflect
	// the combined committed total.
	reserveB2 := strictReserveCommandForPersistence()
	reserveB2.Request = domain.Amount{Unit: domain.AmountUnitRequests, Value: 30}
	reserveB2.SourceKey = "cross-reserve-b2"
	reserveB2.ReservationKey.AttemptID = "attempt-cross-b2"
	reserveB2.ReservationKey.Sequence = 3
	outB2, errB2 := durableB.Reserve(ctx, reserveB2)
	if errB2 != nil {
		t.Fatalf("instance B follow-up reserve within remaining capacity: %v", errB2)
	}
	if !outB2.Applied {
		t.Fatalf("instance B follow-up reserve must apply: %#v", outB2)
	}
	pageAfter, err := durableB.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known("principal-1"),
				TenantID:    scope.Known("tenant-1"),
			},
			BackendID: "backend-1",
			Model:     "model-1",
		},
		RuleID:     "rule-strict",
		Unit:       string(domain.AmountUnitRequests),
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus after follow-up: %v", err)
	}
	if pageAfter.Items[0].Reserved != 90 || pageAfter.Items[0].Remaining != 10 {
		t.Fatalf("cross-instance totals after follow-up = reserved=%d remaining=%d, want reserved=90 remaining=10",
			pageAfter.Items[0].Reserved, pageAfter.Items[0].Remaining)
	}
}

// TestSQLiteStore_RepeatedCapacityDenialIsIdempotent proves a logical
// reservation that was denied by a full window stays a typed capacity denial
// on replay. The second attempt must not collide with the decision ledger and
// become an infrastructure/unavailable error.
func TestSQLiteStore_RepeatedCapacityDenialIsIdempotent(t *testing.T) {
	t.Parallel()
	dsn := sqliteAuthorityDSN(filepath.Join(t.TempDir(), "authority.db"))
	seed := authoritystore.Config{StoreID: "capacity-denial-replay", Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness()}
	db := openSQLiteAuthorityBun(t, dsn)
	store, err := authoritystore.NewDurable(context.Background(), db, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	fill := strictReserveCommandForPersistence()
	if _, err := store.Reserve(context.Background(), fill); err != nil {
		t.Fatalf("fill reserve: %v", err)
	}
	denied := strictReserveCommandForPersistence()
	denied.SourceKey = "capacity-denial-replay"
	denied.ReservationKey.AttemptID = "attempt-capacity-denial-replay"
	denied.ReservationKey.Sequence = 9
	denied.Request = domain.Amount{Unit: domain.AmountUnitRequests, Value: 50}
	first, err := store.Reserve(context.Background(), denied)
	if err == nil || !errors.Is(err, app.ErrCapacityExceeded) || errors.Is(err, app.ErrUnavailable) {
		t.Fatalf("first denial = %#v, err=%v; want typed capacity denial", first, err)
	}
	second, err := store.Reserve(context.Background(), denied)
	if err == nil || !errors.Is(err, app.ErrCapacityExceeded) || errors.Is(err, app.ErrUnavailable) {
		t.Fatalf("replayed denial = %#v, err=%v; want same typed capacity denial", second, err)
	}
	page, err := store.DecisionHistory(context.Background(), controlplane.AccountingDecisionQuery{RuleID: "rule-strict", Common: controlplane.CommonFilters{ReasonCode: "quota_exceeded"}, Limit: 20, Visibility: controlplane.VisibilityDefault})
	if err != nil {
		t.Fatalf("DecisionHistory: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("capacity denial decisions = %d, want one durable replay marker", len(page.Items))
	}
}

func TestSQLiteStore_AuthoritativeResettleSurvivesRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := sqliteAuthorityDSN(filepath.Join(dir, "authority.db"))
	storeID := "sqlite-authoritative-restart"

	db1 := openSQLiteAuthorityBun(t, dsn)
	store, err := authoritystore.NewDurable(context.Background(), db1, authoritystore.Config{
		StoreID:   storeID,
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	reserved, err := store.Reserve(context.Background(), strictReserveCommandForPersistence())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	first := strictSettleCommandForPersistence()
	first.ReservationID = reserved.ReservationID
	first.SourceKey = "settle-estimated-restart"
	if _, err := store.Settle(context.Background(), first); err != nil {
		t.Fatalf("estimated Settle: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2 := openSQLiteAuthorityBun(t, dsn)
	reopened, err := authoritystore.NewDurable(context.Background(), db2, authoritystore.Config{
		StoreID:   storeID,
		Backing:   domain.BackingCapabilityAtomic,
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("reopen NewDurable: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	authoritative := first
	authoritative.SourceKey = "settle-authoritative-restart"
	authoritative.FinalUsage = domain.Amount{Unit: domain.AmountUnitRequests, Value: 30}
	authoritative.FinalUsagePresent = true
	authoritative.Authority = domain.AuthorityLevelAuthoritative
	adjustment, err := reopened.Settle(context.Background(), authoritative)
	if err != nil {
		t.Fatalf("authoritative Settle after restart: %v", err)
	}
	if !adjustment.Applied || adjustment.ReleasedDelta.Value != 10 || adjustment.AdjustmentDelta.Value != 10 {
		t.Fatalf("authoritative adjustment = %#v, want released/adjustment 10/10", adjustment)
	}
	replay, err := reopened.Settle(context.Background(), authoritative)
	if err != nil {
		t.Fatalf("authoritative replay: %v", err)
	}
	if replay.Applied {
		t.Fatalf("authoritative replay must be idempotent: %#v", replay)
	}

	page, err := reopened.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		RuleID: "rule-strict", Unit: string(domain.AmountUnitRequests), Limit: 10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Consumed != 30 || page.Items[0].Reserved != 0 {
		t.Fatalf("post-restart counters = %#v, want consumed 30 reserved 0", page.Items)
	}
}

func TestSQLiteStoreReservationDenialPersistsDecisionEvidence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := sqliteAuthorityDSN(filepath.Join(dir, "authority.db"))
	storeID := "sqlite-denial-evidence"
	db1 := openSQLiteAuthorityBun(t, dsn)
	store, err := authoritystore.NewDurable(context.Background(), db1, authoritystore.Config{
		StoreID:   storeID,
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	denied := strictReserveCommandForPersistence()
	denied.SourceKey = "denied-evidence"
	denied.Request = domain.Amount{Unit: domain.AmountUnitRequests, Value: 101}
	if _, err := store.Reserve(context.Background(), denied); !errors.Is(err, app.ErrReservationConflict) {
		t.Fatalf("denied Reserve error = %v, want reservation conflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2 := openSQLiteAuthorityBun(t, dsn)
	reopened, err := authoritystore.NewDurable(context.Background(), db2, authoritystore.Config{
		StoreID:   storeID,
		Backing:   domain.BackingCapabilityAtomic,
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		t.Fatalf("reopen NewDurable: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	decisions, err := reopened.DecisionHistory(context.Background(), controlplane.AccountingDecisionQuery{
		RuleID: "rule-strict", Limit: 10, Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory: %v", err)
	}
	found := false
	for _, row := range decisions.Items {
		if row.Outcome == controlplane.AccountingOutcomeDeny && row.ReasonCode == "quota_exceeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("persisted denial decision not found: %#v", decisions.Items)
	}
}
