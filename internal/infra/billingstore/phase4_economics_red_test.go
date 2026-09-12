package billingstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// This is intentionally a pre-implementation behavioral check for Phase 4.
// The V2 valuation/reconciliation projections must be part of the existing
// billing migration path.
func TestPhase4EconomicsSchemaExists(t *testing.T) {
	store := newSQLiteTestStore(t)
	defer store.Close()

	ctx := context.Background()
	var valuations int
	require.NoError(t, store.db.NewRaw("SELECT COUNT(*) FROM billing_valuations").Scan(ctx, &valuations))
	var lines int
	require.NoError(t, store.db.NewRaw("SELECT COUNT(*) FROM billing_valuation_lines").Scan(ctx, &lines))
	var reconciliations int
	require.NoError(t, store.db.NewRaw("SELECT COUNT(*) FROM billing_reconciliations").Scan(ctx, &reconciliations))
}
