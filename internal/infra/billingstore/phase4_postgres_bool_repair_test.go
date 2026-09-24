package billingstore

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const billingV2LineBooleanRepairMigrationName = "20260912020000"

func TestBillingV2ValuationLineBooleanDDLAndRepairMigration(t *testing.T) {
	t.Run("postgres uses booleans for Go bool projections", func(t *testing.T) {
		ddl := strings.Join(billingV2PostgresDDL(), "\n")
		for _, column := range []string{
			"quantity_present",
			"unit_price_present",
			"rate_numerator_present",
			"rate_denominator_present",
			"amount_present",
			"rounded_present",
			"included_unit",
		} {
			pattern := `(?i)\b` + regexp.QuoteMeta(column) + `\s+BOOLEAN\s+NOT\s+NULL\s+DEFAULT\s+FALSE\b`
			require.Regexp(t, regexp.MustCompile(pattern), ddl, "PostgreSQL column %s", column)
		}
	})

	t.Run("sqlite keeps integer boolean representation", func(t *testing.T) {
		ddl := strings.Join(billingV2SQLiteDDL(), "\n")
		for _, column := range []string{
			"quantity_present",
			"unit_price_present",
			"rate_numerator_present",
			"rate_denominator_present",
			"amount_present",
			"rounded_present",
			"included_unit",
		} {
			pattern := `(?i)\b` + regexp.QuoteMeta(column) + `\s+INTEGER\s+NOT\s+NULL\s+DEFAULT\s+0\b`
			require.Regexp(t, regexp.MustCompile(pattern), ddl, "SQLite column %s", column)
		}
	})

	t.Run("postgres repair converts every legacy representation", func(t *testing.T) {
		statements := strings.Join(billingV2LineBooleanRepairPostgresDDL(), "\n")
		for _, column := range []string{
			"quantity_present",
			"unit_price_present",
			"rate_numerator_present",
			"rate_denominator_present",
			"amount_present",
			"rounded_present",
			"included_unit",
		} {
			require.Contains(t, statements, "ALTER COLUMN "+column+" DROP DEFAULT")
			require.Contains(t, statements, "ALTER COLUMN "+column+" TYPE BOOLEAN USING ("+column+"::text IN ('1','true','t'))")
			require.Contains(t, statements, "ALTER COLUMN "+column+" SET DEFAULT FALSE")
		}
	})

	t.Run("repair migration is registered and recorded", func(t *testing.T) {
		require.Contains(t, RequiredMigrationNames, billingV2LineBooleanRepairMigrationName)

		store := newSQLiteTestStore(t)
		var count int
		require.NoError(t, store.db.NewRaw(
			"SELECT COUNT(*) FROM bun_billing_migrations WHERE name = ?",
			billingV2LineBooleanRepairMigrationName,
		).Scan(context.Background(), &count))
		require.Equal(t, 1, count)
	})
}
