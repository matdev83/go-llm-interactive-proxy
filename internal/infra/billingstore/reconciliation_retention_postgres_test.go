//go:build integration

package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// TestReconciliationRetentionPostgresDirect proves the durable retention
// contract on direct PostgreSQL with the same append/replay/conflict/revision/
// scope/bounds behavior as SQLite.
func TestReconciliationRetentionPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "retention-pg"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	var columnCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM information_schema.columns WHERE table_name = 'billing_reconciliations' AND column_name = 'result_schema_version'`).Scan(ctx, &columnCount))
	require.Equal(t, 1, columnCount, "result_schema_version column must exist on PostgreSQL")
	var indexName string
	require.NoError(t, store.db.NewRaw(`SELECT indexname FROM pg_indexes WHERE tablename = 'billing_reconciliations' AND indexname = ?`, billingReconciliationRetentionIndex).Scan(ctx, &indexName))
	require.Equal(t, billingReconciliationRetentionIndex, indexName)

	result := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-1", 1, time.Unix(1_700_002_800, 0).UTC())
	canonical, err := result.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	require.NoError(t, store.AppendReconciliationRetention(ctx, result), "exact replay must be idempotent")

	got, err := store.GetReconciliationRetention(ctx, result.ID, result.ResultRevision)
	require.NoError(t, err)
	gotCanonical, err := got.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(canonical), string(gotCanonical))

	conflict := result
	conflict.CreatedAt = result.CreatedAt.Add(time.Second)
	require.ErrorIs(t, store.AppendReconciliationRetention(ctx, conflict), ErrIdentityConflict)

	revision := retentionTestResult(t, "retention-pg", "b-pg", result.ID, 2, result.CreatedAt.Add(time.Minute))
	secondCanonical, err := revision.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, store.AppendReconciliationRetention(ctx, revision))
	first, err := store.GetReconciliationRetention(ctx, result.ID, 1)
	require.NoError(t, err)
	firstCanonical, err := first.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(canonical), string(firstCanonical), "revision append must not rewrite revision 1")

	latest, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-pg"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(2), latest.ResultRevision)

	// Recreate the store over the same database handle to prove the durable
	// rows survive instance restart without relying on in-memory state.
	reopened, err := openStore(ctx, store.db, Config{StoreID: "retention-pg"})
	require.NoError(t, err)
	reopenedResult, err := reopened.GetReconciliationRetention(ctx, result.ID, 2)
	require.NoError(t, err)
	reopenedCanonical, err := reopenedResult.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(secondCanonical), string(reopenedCanonical))

	_, _, err = store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{TenantID: "tenant-retention-pg"})
	require.ErrorIs(t, err, ErrQueryTooBroad)

	foreign := retentionTestResult(t, "other-store", "b-pg", "retention-pg-foreign", 1, result.CreatedAt)
	require.ErrorIs(t, store.AppendReconciliationRetention(ctx, foreign), ErrEconomicsOutOfScope)

	// Cross-store reads must not discover the row, and foreign-store nested
	// evidence must be rejected before it can be attached durably.
	other, err := openStore(ctx, store.db, Config{StoreID: "retention-pg-other"})
	require.NoError(t, err)
	_, err = other.GetReconciliationRetention(ctx, result.ID, result.ResultRevision)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, found, err = other.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-pg"})
	require.NoError(t, err)
	require.False(t, found)

	foreignNested := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-foreign-nested", 1, result.CreatedAt)
	foreignNested.Quantity.Items[0].Local[0].Observation.StoreID = "store-other"
	require.ErrorIs(t, store.AppendReconciliationRetention(ctx, foreignNested), billing.ErrInvalidReconciliationRetention)

	t.Run("generic schema-2 poison rejected", func(t *testing.T) {
		poison := ReconciliationRecord{
			ID: "retention-pg-poison", Version: 1,
			Subject:             retentionTestSubject("retention-pg", "b-pg"),
			ResultSchemaVersion: ReconciliationRecordSchemaRetention,
			ResultJSON:          json.RawMessage(`{"not":"retention"}`),
			CreatedAt:           result.CreatedAt,
		}
		err := store.AppendReconciliation(ctx, poison)
		require.ErrorIs(t, err, ErrInvalidReconciliation)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, poison.ID).Scan(ctx, &count))
		require.Zero(t, count, "poison schema-2 payload must not be persisted on PostgreSQL")
	})

	t.Run("generic schema-2 forged delta rejected", func(t *testing.T) {
		valid := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-forged", 1, result.CreatedAt)
		raw := forgeRetentionQuantityCanonical(t, valid, func(item map[string]any) {
			item["signed_delta"].(map[string]any)["coefficient"] = "11"
		})
		poison := ReconciliationRecord{
			ID: valid.ID, Version: valid.ResultRevision,
			Subject:             valid.Subject,
			ResultSchemaVersion: ReconciliationRecordSchemaRetention,
			ResultJSON:          raw,
			CreatedAt:           valid.CreatedAt,
		}
		err := store.AppendReconciliation(ctx, poison)
		require.ErrorIs(t, err, ErrInvalidReconciliation)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, poison.ID).Scan(ctx, &count))
		require.Zero(t, count, "forged schema-2 delta must not be persisted on PostgreSQL")
	})

	t.Run("specialized append rejects forged absolute delta", func(t *testing.T) {
		forged := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-forged-specialized", 1, result.CreatedAt)
		forged.Quantity.Items[0].AbsoluteDelta = &metering.Decimal{Coefficient: "11"}
		require.ErrorIs(t, store.AppendReconciliationRetention(ctx, forged), billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, forged.ID).Scan(ctx, &count))
		require.Zero(t, count, "forged retention result must not be persisted on PostgreSQL")
	})

	t.Run("generic schema-2 forged monetary term rejected", func(t *testing.T) {
		valid := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-forged-monetary", 1, result.CreatedAt)
		forged := forgeRetentionCanonical(t, valid, func(document map[string]any) {
			monetary, ok := document["monetary"].(map[string]any)
			require.True(t, ok)
			rows, ok := monetary["rows"].([]any)
			require.True(t, ok)
			row, ok := rows[0].(map[string]any)
			require.True(t, ok)
			term, ok := row["metering_cost_effect"].(map[string]any)
			require.True(t, ok)
			amount, ok := term["amount"].(map[string]any)
			require.True(t, ok)
			decimal, ok := amount["decimal"].(map[string]any)
			require.True(t, ok)
			decimal["coefficient"] = "2"
		})
		poison := ReconciliationRecord{
			ID: valid.ID, Version: valid.ResultRevision,
			Subject:             valid.Subject,
			ResultSchemaVersion: ReconciliationRecordSchemaRetention,
			ResultJSON:          forged,
			CreatedAt:           valid.CreatedAt,
		}
		err := store.AppendReconciliation(ctx, poison)
		require.ErrorIs(t, err, ErrInvalidReconciliation)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, poison.ID).Scan(ctx, &count))
		require.Zero(t, count, "forged monetary schema-2 term must not be persisted on PostgreSQL")
	})

	t.Run("specialized append rejects forged monetary term", func(t *testing.T) {
		forged := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-forged-monetary-specialized", 1, result.CreatedAt)
		forged.Monetary.Rows[0].MeteringCostEffect.Amount = &billing.MonetaryExactAmount{
			Currency: "USD", Decimal: &metering.Decimal{Coefficient: "2", Scale: 1},
		}
		require.ErrorIs(t, store.AppendReconciliationRetention(ctx, forged), billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, forged.ID).Scan(ctx, &count))
		require.Zero(t, count, "forged monetary retention result must not be persisted on PostgreSQL")
	})

	t.Run("generic schema-2 forged evaluation delta rejected", func(t *testing.T) {
		valid := retentionAggregateResultForStore(t, "retention-pg", "b-pg", "retention-pg-forged-aggregate", 1, result.CreatedAt)
		forged := forgeRetentionCanonical(t, valid, func(document map[string]any) {
			aggregate, ok := document["aggregate"].(map[string]any)
			require.True(t, ok)
			findings, ok := aggregate["findings"].([]any)
			require.True(t, ok)
			require.NotEmpty(t, findings)
			finding, ok := findings[0].(map[string]any)
			require.True(t, ok)
			evaluation, ok := finding["evaluation"].(map[string]any)
			require.True(t, ok)
			amount, ok := evaluation["signed_delta"].(map[string]any)
			require.True(t, ok)
			decimal, ok := amount["decimal"].(map[string]any)
			require.True(t, ok)
			decimal["coefficient"] = "31"
		})
		poison := ReconciliationRecord{
			ID: valid.ID, Version: valid.ResultRevision,
			Subject:             valid.Subject,
			ResultSchemaVersion: ReconciliationRecordSchemaRetention,
			ResultJSON:          forged,
			CreatedAt:           valid.CreatedAt,
		}
		err := store.AppendReconciliation(ctx, poison)
		require.ErrorIs(t, err, ErrInvalidReconciliation)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, poison.ID).Scan(ctx, &count))
		require.Zero(t, count, "forged aggregate evaluation must not be persisted on PostgreSQL")
	})

	t.Run("specialized append rejects forged aggregate row total", func(t *testing.T) {
		forged := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-forged-aggregate-specialized", 1, result.CreatedAt)
		forged.Aggregate.Rows[0].GrossAbsoluteDiscrepancy = &billing.MonetaryExactAmount{
			Currency: "USD", Decimal: &metering.Decimal{Coefficient: "1"},
		}
		require.ErrorIs(t, store.AppendReconciliationRetention(ctx, forged), billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, forged.ID).Scan(ctx, &count))
		require.Zero(t, count, "forged aggregate retention result must not be persisted on PostgreSQL")
	})

	t.Run("generic schema-2 envelope mismatch rejected", func(t *testing.T) {
		valid := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-envelope-mismatch", 1, result.CreatedAt)
		record, err := ReconciliationRecordFromRetention(valid)
		require.NoError(t, err)
		record.Scope = "call:other"
		err = store.AppendReconciliation(ctx, record)
		require.ErrorIs(t, err, ErrInvalidReconciliation)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, record.ID).Scan(ctx, &count))
		require.Zero(t, count, "mismatched envelope must not be persisted on PostgreSQL")
	})

	t.Run("raw wire mismatch fails closed", func(t *testing.T) {
		valid := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-envelope-raw", 1, result.CreatedAt)
		record, err := ReconciliationRecordFromRetention(valid)
		require.NoError(t, err)
		authoritative, err := record.CanonicalJSON()
		require.NoError(t, err)
		forged := forgeEnvelopeDocument(t, authoritative, func(document map[string]any) {
			document["scope"] = "call:forged"
		})
		insertRawRetentionRecord(t, store, "retention-pg", record, forged, record.Subject.TenantID)
		_, err = store.GetReconciliation(ctx, record.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, err = store.GetReconciliationRetention(ctx, record.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
	})

	t.Run("projection column mismatch fails closed", func(t *testing.T) {
		valid := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-envelope-projection", 1, result.CreatedAt)
		record, err := ReconciliationRecordFromRetention(valid)
		require.NoError(t, err)
		authoritative, err := record.CanonicalJSON()
		require.NoError(t, err)
		insertRawRetentionRecord(t, store, "retention-pg", record, authoritative, "tenant-forged")
		_, err = store.GetReconciliationRetention(ctx, record.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: record.Subject.BLegID, TenantID: "tenant-forged"})
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		require.False(t, found)
	})

	t.Run("wire column generation mutation fails closed", func(t *testing.T) {
		// Direction 1: a field-less schema-1 wire marked generation 2 must
		// never be returned as a retention result.
		directionA := retentionTestResult(t, "retention-pg", "b-pg-gen-a", "retention-pg-generation-a", 1, result.CreatedAt.Add(2*time.Minute))
		recordA, err := ReconciliationRecordFromRetention(directionA)
		require.NoError(t, err)
		authoritativeA, err := recordA.CanonicalJSON()
		require.NoError(t, err)
		wireA := schema1WireFromSchema2(t, authoritativeA, string(economics.BasisProviderReported))
		insertRawGenerationRow(t, store, "retention-pg", recordA, string(wireA), string(recordA.ResultJSON), "", int64(ReconciliationRecordSchemaRetention), string(economics.BasisProviderReported), recordA.Subject.TenantID)

		_, err = store.GetReconciliation(ctx, directionA.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, err = store.GetReconciliationRetention(ctx, directionA.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, err = store.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: directionA.Subject.BLegID})
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: directionA.Subject.BLegID})
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		require.False(t, found)

		// Direction 2: an explicit schema-2 wire marked generation 1 must not
		// be returned as a legacy record or listed.
		directionB := retentionTestResult(t, "retention-pg", "b-pg-gen-b", "retention-pg-generation-b", 1, result.CreatedAt.Add(3*time.Minute))
		recordB, err := ReconciliationRecordFromRetention(directionB)
		require.NoError(t, err)
		authoritativeB, err := recordB.CanonicalJSON()
		require.NoError(t, err)
		insertRawGenerationRow(t, store, "retention-pg", recordB, string(authoritativeB), string(recordB.ResultJSON), recordB.Fingerprint(), int64(ReconciliationRecordSchemaLegacy), "", recordB.Subject.TenantID)
		_, err = store.GetReconciliation(ctx, directionB.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, err = store.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: directionB.Subject.BLegID})
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, found, err = store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: directionB.Subject.BLegID})
		require.NoError(t, err)
		require.False(t, found, "a generation-1 discriminator must not satisfy a retention latest read")
	})

	t.Run("existing-row replay poison fails closed", func(t *testing.T) {
		// A schema-2 row with an altered outer envelope, the same result_json
		// and an empty fingerprint must not satisfy replay.
		incoming := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-replay-poison", 1, result.CreatedAt.Add(4*time.Minute))
		incomingRecord, err := ReconciliationRecordFromRetention(incoming)
		require.NoError(t, err)
		forgedResult := incoming
		forgedResult.Scope = "call:pg-forged"
		forgedRecord, err := ReconciliationRecordFromRetention(forgedResult)
		require.NoError(t, err)
		forgedCanonical, err := forgedRecord.CanonicalJSON()
		require.NoError(t, err)
		insertRawGenerationRow(t, store, "retention-pg", forgedRecord, string(forgedCanonical), string(incomingRecord.ResultJSON), "", int64(ReconciliationRecordSchemaRetention), "", forgedRecord.Subject.TenantID)
		require.ErrorIs(t, store.AppendReconciliation(ctx, incomingRecord), ErrIdentityConflict, "matching result_json plus an empty fingerprint must not satisfy replay")

		// A schema-2 row whose projection disagrees with its envelope must
		// not satisfy an otherwise exact replay.
		projectionPoison := retentionTestResult(t, "retention-pg", "b-pg", "retention-pg-replay-projection", 1, result.CreatedAt.Add(5*time.Minute))
		projectionRecord, err := ReconciliationRecordFromRetention(projectionPoison)
		require.NoError(t, err)
		projectionCanonical, err := projectionRecord.CanonicalJSON()
		require.NoError(t, err)
		insertRawGenerationRow(t, store, "retention-pg", projectionRecord, string(projectionCanonical), string(projectionRecord.ResultJSON), projectionRecord.Fingerprint(), int64(ReconciliationRecordSchemaRetention), "", "tenant-forged")
		err = store.AppendReconciliation(ctx, projectionRecord)
		require.ErrorIs(t, err, ErrIdentityConflict)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)

		// A canonical-empty row marked generation 2 must not replay as legacy.
		legacy := legacySchema1Fixture(t)
		legacy.ID = "legacy-schema1-pg-generation-2"
		legacy.Subject.StoreID = "retention-pg"
		canonicalResult, err := canonicalJSON(legacy.ResultJSON)
		require.NoError(t, err)
		insertRawGenerationRow(t, store, "retention-pg", legacy, "", string(canonicalResult), "", int64(ReconciliationRecordSchemaRetention), string(economics.BasisProviderReported), legacy.Subject.TenantID)
		require.ErrorIs(t, store.AppendReconciliation(ctx, legacy), ErrIdentityConflict)
	})

	t.Run("unbound aggregate source classification fails closed", func(t *testing.T) {
		valid := retentionAggregateResultForStore(t, "retention-pg", "b-pg-source", "retention-pg-source-binding", 1, result.CreatedAt.Add(6*time.Minute))
		forged := forgeRetentionCanonical(t, valid, func(document map[string]any) {
			aggregate, ok := document["aggregate"].(map[string]any)
			require.True(t, ok)
			findings, ok := aggregate["findings"].([]any)
			require.True(t, ok)
			require.NotEmpty(t, findings)
			findings[0].(map[string]any)["status"] = "matched"
		})
		poison, err := ReconciliationRecordFromRetention(valid)
		require.NoError(t, err)
		poison.ResultJSON = json.RawMessage(forged)
		err = store.AppendReconciliation(ctx, poison)
		require.ErrorIs(t, err, ErrInvalidReconciliation)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'retention-pg' AND reconciliation_id = ?`, poison.ID).Scan(ctx, &count))
		require.Zero(t, count, "an unbound source classification must not be persisted on PostgreSQL")

		wire, err := poison.CanonicalJSON()
		require.NoError(t, err)
		insertRawRetentionRecord(t, store, "retention-pg", poison, wire, poison.Subject.TenantID)
		_, err = store.GetReconciliation(ctx, poison.ID, 1)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		_, err = store.GetReconciliationRetention(ctx, poison.ID, 1)
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		_, err = store.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: poison.Subject.BLegID})
		require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
	})
}

func seedRetentionLegacyRowsPostgres(t *testing.T, ctx context.Context, store *DurableStore, storeID, subjectID, tenantID string, count int, createdAtBase int64) {
	t.Helper()
	_, err := store.db.NewRaw(`INSERT INTO billing_reconciliations(store_id, reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, tenant_id, scope, basis, result_schema_version, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, canonical_json, fingerprint, projection_version, created_at_unix) SELECT ?, 'legacy-' || i, 1, 'b_leg', ?, '{}', ?, '', '', 1, '', '', '', '', '', '{}', '', '', 1, ? + i FROM generate_series(1, ?) AS i`, storeID, subjectID, tenantID, createdAtBase, count).Exec(ctx)
	require.NoError(t, err)
}

// TestLatestReconciliationRetentionPostgresPlanAndOrder proves the supported
// latest query shapes are index-backed on PostgreSQL and keep the
// deterministic tie-break under a large subject partition.
func TestLatestReconciliationRetentionPostgresPlanAndOrder(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "retention-pg-plan"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	seedRetentionLegacyRowsPostgres(t, ctx, store, "retention-pg-plan", "b-pg-plan", "tenant-retention-pg-plan", 1000, 1_700_000_000)
	createdAt := time.Unix(1_700_004_000, 0).UTC()
	appendResult := func(t *testing.T, id string, revision uint64, at time.Time) {
		t.Helper()
		result := retentionTestResult(t, "retention-pg-plan", "b-pg-plan", id, revision, at)
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	}
	appendResult(t, "retention-pg-tie-a", 1, createdAt)
	appendResult(t, "retention-pg-tie-b", 1, createdAt)
	appendResult(t, "retention-pg-tie-b", 2, createdAt)

	latest, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-pg-plan"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "retention-pg-tie-b", latest.ID)
	require.Equal(t, uint64(2), latest.ResultRevision)

	t.Run("subject plan uses the retention index", func(t *testing.T) {
		assertLatestRetentionPlanUsesIndex(t, ctx, store, latestRetentionSubjectSQL, billingReconciliationRetentionIndex, false,
			"retention-pg-plan", ReconciliationRecordSchemaRetention, string(metering.SubjectBLeg), "b-pg-plan")
	})
	t.Run("subject tenant plan uses the tenant retention index", func(t *testing.T) {
		assertLatestRetentionPlanUsesIndex(t, ctx, store, latestRetentionSubjectTenantSQL, billingReconciliationRetentionTenantIndex, true,
			"retention-pg-plan", ReconciliationRecordSchemaRetention, string(metering.SubjectBLeg), "b-pg-plan", "tenant-retention-pg-plan")
	})

	t.Run("tenant refinement isolates rows under load", func(t *testing.T) {
		tenantA := retentionResultWithTenant(t, "retention-pg-plan", "b-pg-tenant", "retention-pg-tenant-a", "tenant-a", 1, createdAt)
		tenantB := retentionResultWithTenant(t, "retention-pg-plan", "b-pg-tenant", "retention-pg-tenant-b", "tenant-b", 1, createdAt.Add(time.Minute))
		require.NoError(t, store.AppendReconciliationRetention(ctx, tenantA))
		require.NoError(t, store.AppendReconciliationRetention(ctx, tenantB))
		gotA, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-pg-tenant", TenantID: "tenant-a"})
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, tenantA.ID, gotA.ID)
		_, found, err = store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-pg-tenant", TenantID: "tenant-missing"})
		require.NoError(t, err)
		require.False(t, found)
	})
}

// assertLatestRetentionPlanUsesIndex uses ANALYZE plus a seq-scan-disabled
// transaction so the assertion fails when the query has no index path, and
// proves tenant is an index condition rather than a post-index filter.
func assertLatestRetentionPlanUsesIndex(t *testing.T, ctx context.Context, store *DurableStore, sqlText, wantIndex string, wantTenantCond bool, args ...any) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `ANALYZE billing_reconciliations`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)
	var planJSON string
	require.NoError(t, tx.NewRaw(`EXPLAIN (FORMAT JSON) `+sqlText, args...).Scan(ctx, &planJSON))
	require.Contains(t, planJSON, wantIndex)
	require.NotContains(t, planJSON, `"Seq Scan"`)

	var document []struct {
		Plan map[string]any `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(planJSON), &document))
	require.NotEmpty(t, document)
	indexCond := planNodeString(document[0].Plan, "Index Cond")
	require.NotEmpty(t, indexCond, "plan must use an Index Cond: %s", planJSON)
	require.Equal(t, wantTenantCond, strings.Contains(indexCond, "tenant_id"), "tenant must be an index condition when required: %s", indexCond)
	if wantTenantCond {
		filter := planNodeString(document[0].Plan, "Filter")
		require.False(t, strings.Contains(filter, "tenant_id"), "tenant must not be a post-index filter: %s", filter)
	}
}

// planNodeString returns the first value of the named plan key found anywhere in
// the plan tree.
func planNodeString(plan map[string]any, key string) string {
	if value, ok := plan[key].(string); ok && value != "" {
		return value
	}
	children, ok := plan["Plans"].([]any)
	if !ok {
		return ""
	}
	for _, child := range children {
		childPlan, ok := child.(map[string]any)
		if !ok {
			continue
		}
		if value := planNodeString(childPlan, key); value != "" {
			return value
		}
	}
	return ""
}
