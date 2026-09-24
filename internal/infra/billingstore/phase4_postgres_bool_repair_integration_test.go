//go:build integration

package billingstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/stretchr/testify/require"
)

func TestBillingV2PostgresLineBooleanRepairAndAppend(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "phase4-pg-bool-repair"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	for _, column := range billingV2LineBooleanColumns {
		_, err := bunDB.NewRaw("ALTER TABLE billing_valuation_lines ALTER COLUMN " + column + " DROP DEFAULT").Exec(ctx)
		require.NoError(t, err)
		_, err = bunDB.NewRaw("ALTER TABLE billing_valuation_lines ALTER COLUMN " + column + " TYPE INTEGER USING (CASE WHEN " + column + " THEN 1 ELSE 0 END)").Exec(ctx)
		require.NoError(t, err)
		_, err = bunDB.NewRaw("ALTER TABLE billing_valuation_lines ALTER COLUMN " + column + " SET DEFAULT 0").Exec(ctx)
		require.NoError(t, err)
	}
	_, err = bunDB.NewRaw("DELETE FROM bun_billing_migrations WHERE name = ?", BillingV2LineBooleanRepairMigrationName).Exec(ctx)
	require.NoError(t, err)
	require.NoError(t, Migrate(ctx, bunDB))
	// A direct rerun proves the SQL remains idempotent after the columns are
	// already BOOLEAN; migration history normally prevents this path.
	require.NoError(t, billingV2LineBooleanRepairSchemaUp(ctx, bunDB))

	for _, column := range billingV2LineBooleanColumns {
		var dataType, nullable, defaultValue string
		require.NoError(t, bunDB.NewRaw(`
			SELECT data_type, is_nullable, COALESCE(column_default, '')
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'billing_valuation_lines' AND column_name = ?`, column).
			Scan(ctx, &dataType, &nullable, &defaultValue))
		require.Equal(t, "boolean", strings.ToLower(dataType), column)
		require.Equal(t, "NO", nullable, column)
		require.Contains(t, strings.ToLower(defaultValue), "false", column)
	}

	observation := phase4EconomicsObservation(store.StoreID(), "pg-bool-repair-observation", 1)
	valuation := phase4EconomicsValuation(t, observation, "pg-bool-repair-valuation", time.Unix(1_700_010_000, 0).UTC(), strings.Repeat("e", 64))
	require.NoError(t, store.AppendValuation(ctx, valuation))

	var quantityPresent, unitPricePresent, amountPresent, includedUnit bool
	require.NoError(t, bunDB.NewRaw(`
		SELECT quantity_present, unit_price_present, amount_present, included_unit
		FROM billing_valuation_lines WHERE store_id = ? AND valuation_id = ?`, store.StoreID(), valuation.ID).
		Scan(ctx, &quantityPresent, &unitPricePresent, &amountPresent, &includedUnit))
	require.True(t, quantityPresent)
	require.True(t, unitPricePresent)
	require.True(t, amountPresent)
	require.False(t, includedUnit)
}
