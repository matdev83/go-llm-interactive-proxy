package billingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// TestReconciliationRetentionRejectsUnboundAggregateSourceFindingsDurably
// proves an aggregate source classification that is not the exact producer
// projection of the retained quantity/monetary evidence can neither be
// appended (specialized or generic schema-2) nor returned by any durable read
// path, including after a restart.
func TestReconciliationRetentionRejectsUnboundAggregateSourceFindingsDurably(t *testing.T) {
	ctx := context.Background()
	createdAt := time.Unix(1_700_008_000, 0).UTC()
	forgeAggregateStatus := func(t *testing.T) func(document map[string]any) {
		t.Helper()
		return func(document map[string]any) {
			aggregate, ok := document["aggregate"].(map[string]any)
			require.True(t, ok, "canonical payload must carry the aggregate block")
			findings, ok := aggregate["findings"].([]any)
			require.True(t, ok)
			require.NotEmpty(t, findings)
			finding, ok := findings[0].(map[string]any)
			require.True(t, ok)
			finding["status"] = "matched"
		}
	}

	t.Run("specialized append", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		result := retentionAggregateResultForStore(t, "test", "b-source-binding", "retention-binding-specialized", 1, createdAt)
		result.Aggregate.Findings[0].Status = billing.ReconciliationStatusMatched
		err := store.AppendReconciliationRetention(ctx, result)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ?`, "test", result.ID).Scan(ctx, &count))
		require.Zero(t, count, "an unbound source classification must not be persisted")
	})

	t.Run("generic schema-2 append", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		result := retentionAggregateResultForStore(t, "test", "b-source-binding", "retention-binding-generic", 1, createdAt)
		forged := forgeRetentionCanonical(t, result, forgeAggregateStatus(t))
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		record.ResultJSON = json.RawMessage(forged)
		err = store.AppendReconciliation(ctx, record)
		require.ErrorIs(t, err, ErrInvalidReconciliation)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ?`, "test", record.ID).Scan(ctx, &count))
		require.Zero(t, count, "a forged canonical source classification must not be persisted")
	})

	t.Run("durable forged row fails closed on get, list, latest and restart", func(t *testing.T) {
		dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "source-binding.db")))
		store := openGenerationBindingStore(t, dsn, "test")

		result := retentionAggregateResultForStore(t, "test", "b-source-binding", "retention-binding-row", 1, createdAt)
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		forgedInner := forgeRetentionCanonical(t, result, forgeAggregateStatus(t))
		record.ResultJSON = json.RawMessage(forgedInner)
		wire, err := record.CanonicalJSON()
		require.NoError(t, err)
		insertRawRetentionRecord(t, store, "test", record, wire, record.Subject.TenantID)

		_, err = store.GetReconciliation(ctx, result.ID, 1)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		_, err = store.GetReconciliationRetention(ctx, result.ID, 1)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		_, err = store.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: result.Subject.BLegID})
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: result.Subject.BLegID})
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		require.False(t, found)

		reopened := openGenerationBindingStore(t, dsn, "test")
		_, err = reopened.GetReconciliationRetention(ctx, result.ID, 1)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention, "restart must not change the verdict")
	})
}
