package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/stretchr/testify/require"
)

func r3UnitGrant(t *testing.T, ctx context.Context, store *DurableStore, key billing.CustomerUnitKey) billing.CustomerUnitOperationResult {
	t.Helper()
	grant := customerUnitOperation("r3-unit-grant-1", key, billing.CustomerUnitOperationGrant, "", "5", 0, 1)
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	require.NoError(t, err)
	require.False(t, grantResult.Replayed)
	return grantResult
}

func TestPhase172R3SnapshotDetectsUnitDebit(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := customerUnitTestKey("r3-acct", "r3-pool", "r3-period")
	grantResult := r3UnitGrant(t, ctx, store, key)

	before, err := store.CustomerUnitBalance(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "5/0", before.Available.CanonicalString())
	require.Equal(t, grantResult.After, before, "authoritative read must agree with the grant result before any debit")

	reserve := customerUnitOperation("r3-unit-reserve-1", key, billing.CustomerUnitOperationReserve, "r3-reservation", "2", grantResult.After.Version, 1)
	reserveResult, err := store.ApplyCustomerUnitOperation(ctx, reserve)
	require.NoError(t, err)
	commit := customerUnitOperation("r3-unit-commit-1", key, billing.CustomerUnitOperationCommit, reserve.ReservationID, "2", reserveResult.After.Version, 1)
	_, err = store.ApplyCustomerUnitOperation(ctx, commit)
	require.NoError(t, err)

	after, err := store.CustomerUnitBalance(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "3/0", after.Available.CanonicalString())
	require.NotEqual(t,
		before.Available.CanonicalString(), after.Available.CanonicalString(),
		"financial snapshot must reflect the debit, not the historical grant result")
}

func TestPhase172R3BalanceReaderPropagatesErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := customerUnitTestKey("r3-acct", "r3-pool", "r3-period")

	_, err := newSQLiteTestStore(t).CustomerUnitBalance(ctx, billing.CustomerUnitKey{})
	require.Error(t, err, "unvalidated key scope must fail, never read as zero")

	closed := newSQLiteTestStore(t)
	require.NoError(t, closed.Close())
	_, err = closed.CustomerUnitBalance(ctx, key)
	require.Error(t, err, "closed store must fail, never read as zero")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newSQLiteTestStore(t).CustomerUnitBalance(canceled, key)
	require.Error(t, err, "canceled context must fail, never read as zero")
}
