package authoritystore_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	_ "modernc.org/sqlite"
)

type sqliteFactory struct{}

func (sqliteFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authority.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	store, err := authoritystore.NewDurable(context.Background(), db, authoritystore.Config{
		StoreID:   "sqlite-test",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		_ = db.Close()
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
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	defer func() { _ = db.Close() }()

	store, err := authoritystore.NewDurable(context.Background(), db, authoritystore.Config{
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
		if err := db.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("lookup table %s: %v", table, err)
		}
		if name != table {
			t.Fatalf("table name = %q, want %q", name, table)
		}
	}
	if err := authoritystore.Migrate(context.Background(), db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestSQLiteStore_PersistenceRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "authority.db")) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}

	store, err := authoritystore.NewDurable(context.Background(), db, authoritystore.Config{
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

	db2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql reopen: %v", err)
	}
	defer func() { _ = db2.Close() }()
	reopened, err := authoritystore.NewDurable(context.Background(), db2, authoritystore.Config{
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
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	store, err := authoritystore.NewDurable(context.Background(), db, authoritystore.Config{
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
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	store, err := authoritystore.NewDurable(context.Background(), db, authoritystore.Config{
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
		tc := tc
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
		},
		Request:      domain.Amount{Unit: domain.AmountUnitRequests, Value: 60},
		Spend:        domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 600, Currency: "usd"},
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
