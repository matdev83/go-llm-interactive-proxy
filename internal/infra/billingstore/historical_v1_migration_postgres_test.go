//go:build integration

package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestPhase171HistoricalV1MigrationPostgresDirect proves the 17.1 historical
// V1 compatibility reader on direct PostgreSQL with the same byte/hash/
// identity preservation, legacy-opaque projection, read-only balance
// semantics, and explicit V1 writer ownership as SQLite. No migration is
// added, so parity is structural as well as behavioral.
func TestPhase171HistoricalV1MigrationPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	sealedCall, sealedLeg := phase171SeedBaselineV1(t, store)

	bundle, err := store.ReadHistoricalV1CallBundle(ctx, sealedCall.CallID)
	require.NoError(t, err)
	require.Equal(t, sealedCall.Key, bundle.Call.Key)
	require.Equal(t, sealedCall.Fingerprint, bundle.Call.Fingerprint)
	require.Len(t, bundle.Legs, 1)
	require.Equal(t, sealedLeg.Key, bundle.Legs[0].Key)
	require.Equal(t, sealedLeg.Fingerprint, bundle.Legs[0].Fingerprint)
	require.False(t, bundle.Legs[0].BreakdownAvailable)
	require.False(t, bundle.Legs[0].SourceSeparationAvailable)
	require.Equal(t, billing.LegacyScalarSemantics, bundle.Legs[0].LegacySemantics)
	require.Equal(t, billing.HistoricalV1WriterVersion, bundle.WriterVersion)
	require.NoError(t, bundle.Validate())

	single, err := store.ReadHistoricalV1Leg(ctx, sealedCall.CallID, sealedLeg.BLegID)
	require.NoError(t, err)
	require.Equal(t, bundle.Legs[0], single)

	before := phase171JournalCount(t, store, sealedCall.AccountID)
	_, err = store.ReadHistoricalV1CallBundle(ctx, sealedCall.CallID)
	require.NoError(t, err)
	require.Equal(t, before, phase171JournalCount(t, store, sealedCall.AccountID))

	require.NoError(t, store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.HistoricalV1WriterVersion))
	err = store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.V2WriterVersion)
	require.ErrorIs(t, err, billing.ErrHistoricalV1WriterConflict)

	_, err = store.ReadHistoricalV1Leg(ctx, sealedCall.CallID, sealedLeg.BLegID+"-missing")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrUsageRecordNotFound) || errors.Is(err, billing.ErrHistoricalV1Ambiguous) || err != nil)
}
