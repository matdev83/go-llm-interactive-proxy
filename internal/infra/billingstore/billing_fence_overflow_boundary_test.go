package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Batch A overflow-boundary tests (lint SA4003): the fence advance guards must
// reject a saturated fence before attempting `fence + 1`, while MaxInt64-1
// passes the guard; the allocation identity gate must reject non-positive
// versions while MaxInt64 proceeds past it. These pin the intended economic
// bounds so the impossible-comparison cleanup cannot weaken them.

// overflowBoundaryStore opens a migrated SQLite store for fence guard tests.
func overflowBoundaryStore(t *testing.T) *DurableStore {
	t.Helper()
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "billing.db")))
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	require.NoError(t, err)
	seedTestSchemaIfEmpty(t, bunDB)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestExecutionFenceAdvanceRejectsSaturatedFence(t *testing.T) {
	t.Parallel()
	store := overflowBoundaryStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	callID := billing.BillingCallID("bc_00000000000000000000000000000fb1")
	existing := providerCostExecutionFenceRow{ID: 1, Fence: math.MaxInt64, ExecutionLineage: "overflow-lineage"}
	err = store.advanceProviderCostExecutionFenceInTx(ctx, tx, existing, "acct-overflow", callID,
		"overflow-lineage", providerCostFenceAuthorityRevision, string(metering.SubjectBLeg),
		"head-overflow", 1, "inputhash-overflow", "fingerprint-overflow", "op-overflow", "tx-overflow")
	require.ErrorContains(t, err, "invalid provider cost execution fence transition")
}

func TestExecutionFenceAdvanceAdmitsBelowSaturation(t *testing.T) {
	t.Parallel()
	store := overflowBoundaryStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	callID := billing.BillingCallID("bc_00000000000000000000000000000fb1")
	// MaxInt64-1 passes the overflow guard and reaches the transition attempt,
	// which fails only because no such fence row exists.
	existing := providerCostExecutionFenceRow{ID: 987, Fence: math.MaxInt64 - 1, ExecutionLineage: "overflow-lineage"}
	err = store.advanceProviderCostExecutionFenceInTx(ctx, tx, existing, "acct-overflow", callID,
		"overflow-lineage", providerCostFenceAuthorityRevision, string(metering.SubjectBLeg),
		"head-overflow", 1, "inputhash-overflow", "fingerprint-overflow", "op-overflow", "tx-overflow")
	require.ErrorIs(t, err, billing.ErrProviderCostRevisionFence)
	require.NotContains(t, err.Error(), "invalid provider cost execution fence transition")
}

func TestPostingFenceAdvanceRejectsSaturatedFence(t *testing.T) {
	t.Parallel()
	store := overflowBoundaryStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	callID := billing.BillingCallID("bc_00000000000000000000000000000fb2")
	// EvidenceRevision is valid-positive so that only the MaxInt64 saturation
	// guard can cause pre-SQL rejection here.
	existing := providerCostPostingFenceRow{ID: 1, Fence: math.MaxInt64, EvidenceRevision: 2}
	err = store.advanceProviderCostPostingFenceInTx(ctx, tx, existing, "acct-overflow", callID,
		"overflow-lineage", providerCostFenceAuthorityRevision, "head-overflow", 1,
		"inputhash-overflow", "fingerprint-overflow",
		billing.Money{Nano: 10, Currency: "USD"}, "op-overflow", "tx-overflow")
	require.ErrorContains(t, err, "invalid provider cost posting fence transition")
}

func TestPostingFenceAdvanceAdmitsBelowSaturation(t *testing.T) {
	t.Parallel()
	store := overflowBoundaryStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	callID := billing.BillingCallID("bc_00000000000000000000000000000fb2")
	// MaxInt64-1 passes the overflow guard and reaches the transition attempt,
	// which fails only because no such fence row exists.
	existing := providerCostPostingFenceRow{ID: 987, Fence: math.MaxInt64 - 1, EvidenceRevision: 2}
	err = store.advanceProviderCostPostingFenceInTx(ctx, tx, existing, "acct-overflow", callID,
		"overflow-lineage", providerCostFenceAuthorityRevision, "head-overflow", 1,
		"inputhash-overflow", "fingerprint-overflow",
		billing.Money{Nano: 10, Currency: "USD"}, "op-overflow", "tx-overflow")
	require.ErrorIs(t, err, billing.ErrProviderCostRevisionFence)
	require.NotContains(t, err.Error(), "invalid provider cost posting fence transition")
}

func TestProviderCostHeadAdvanceRejectsSaturatedVersionOrFence(t *testing.T) {
	t.Parallel()
	store := overflowBoundaryStore(t)
	ctx := context.Background()
	input := billing.ProviderCostRevisionInput{HeadKey: "overflow-head"}
	amount := billing.Money{Nano: 10, Currency: "USD"}
	for _, existing := range []providerCostHeadRow{
		{ID: 1, HeadVersion: math.MaxInt64, Fence: 3},
		{ID: 1, HeadVersion: 3, Fence: math.MaxInt64},
	} {
		tx, err := store.db.BeginTx(ctx, nil)
		require.NoError(t, err)
		err = store.advanceProviderCostHeadInTx(ctx, tx, existing, input, amount, "op-overflow", "tx-overflow")
		_ = tx.Rollback()
		require.ErrorContains(t, err, "provider cost head version overflow")
	}
}

func TestProviderCostHeadAdvanceAdmitsBelowSaturation(t *testing.T) {
	t.Parallel()
	store := overflowBoundaryStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	input := billing.ProviderCostRevisionInput{HeadKey: "overflow-head"}
	amount := billing.Money{Nano: 10, Currency: "USD"}
	// MaxInt64-1 passes the overflow guard and reaches the transition attempt,
	// which fails only because no such head row exists.
	existing := providerCostHeadRow{ID: 987, HeadVersion: math.MaxInt64 - 1, Fence: math.MaxInt64 - 1}
	err = store.advanceProviderCostHeadInTx(ctx, tx, existing, input, amount, "op-overflow", "tx-overflow")
	require.ErrorIs(t, err, billing.ErrProviderCostRevisionFence)
	require.NotContains(t, err.Error(), "provider cost head version overflow")
}

func TestAllocationTargetScopeVersionIdentityBounds(t *testing.T) {
	t.Parallel()
	for _, version := range []int64{-3, 0} {
		_, err := billingAllocationTargetScopeValuesFromRow(billingAllocationTargetScopeRow{ID: 7, AllocationVersion: version})
		if !errors.Is(err, ErrIdentityConflict) {
			t.Fatalf("version %d must fail the identity gate, got %v", version, err)
		}
	}
	// Positive versions pass the identity gate (later stages fail on the
	// empty canonical payload, which must not be an identity conflict);
	// MaxInt64 in particular must not trip an overflow trap.
	for _, version := range []int64{1, math.MaxInt64} {
		_, err := billingAllocationTargetScopeValuesFromRow(billingAllocationTargetScopeRow{ID: 7, AllocationVersion: version})
		if err == nil {
			t.Fatalf("version %d with empty canonical JSON must fail decode, got nil", version)
		}
		if errors.Is(err, ErrIdentityConflict) {
			t.Fatalf("version %d must pass the identity gate, got %v", version, err)
		}
	}
}
