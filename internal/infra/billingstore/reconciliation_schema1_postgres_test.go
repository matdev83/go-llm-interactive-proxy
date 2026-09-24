//go:build integration

package billingstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/stretchr/testify/require"
)

// TestReconciliationSchema1PostgresFixtureReplay proves pre-migration schema-1
// bytes and fingerprint replay on live PostgreSQL without reinterpretation.
func TestReconciliationSchema1PostgresFixtureReplay(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	_, err = store.db.NewRaw(`INSERT INTO billing_reconciliations(store_id, reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, tenant_id, scope, basis, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, canonical_json, fingerprint, projection_version, created_at_unix) VALUES ('test','legacy-schema1-fixture',1,'b_leg','b-legacy','{}','tenant-test','call','provider_reported',?,?,?,?,?,?,?,?,1,?)`,
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64),
		"policy-legacy", "v1", `{"a":1,"z":2}`, legacySchema1CanonicalJSON, legacySchema1Fingerprint, int64(1_700_000_500)*1_000_000_000).Exec(ctx)
	require.NoError(t, err)

	record := legacySchema1Fixture(t)
	canonical, err := record.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, legacySchema1CanonicalJSON, string(canonical))
	require.Equal(t, legacySchema1Fingerprint, record.Fingerprint())

	got, err := store.GetReconciliation(ctx, record.ID, record.Version)
	require.NoError(t, err)
	gotCanonical, err := got.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, legacySchema1CanonicalJSON, string(gotCanonical))
	require.Equal(t, legacySchema1Fingerprint, got.Fingerprint())

	require.NoError(t, store.AppendReconciliation(ctx, record), "exact replay must be idempotent on PostgreSQL")

	conflict := record
	conflict.ResultJSON = json.RawMessage(`{"a":3}`)
	require.ErrorIs(t, store.AppendReconciliation(ctx, conflict), ErrIdentityConflict)

	reopened, err := openStore(ctx, store.db, Config{StoreID: "test"})
	require.NoError(t, err)
	reopenedRecord, err := reopened.GetReconciliation(ctx, record.ID, record.Version)
	require.NoError(t, err)
	reopenedCanonical, err := reopenedRecord.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, legacySchema1CanonicalJSON, string(reopenedCanonical))
	require.Equal(t, legacySchema1Fingerprint, reopenedRecord.Fingerprint())
	require.NoError(t, reopened.AppendReconciliation(ctx, record))
}

// TestReconciliationSchema1PostgresForgedGenerationFailsClosed proves a frozen
// schema-1 row whose durable discriminator was forged to the retention
// generation fails closed on read and cannot satisfy schema-2 replay.
func TestReconciliationSchema1PostgresForgedGenerationFailsClosed(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	legacy := legacySchema1Fixture(t)
	insertRawGenerationRow(t, store, "test", legacy, legacySchema1CanonicalJSON, `{"a":1,"z":2}`, legacySchema1Fingerprint, int64(ReconciliationRecordSchemaRetention), "provider_reported", legacy.Subject.TenantID)

	_, err = store.GetReconciliation(ctx, legacy.ID, legacy.Version)
	require.ErrorIs(t, err, ErrReconciliationRetentionMismatch, "a schema-1 wire must not be returned as generation 2")
	_, err = store.GetReconciliationRetention(ctx, legacy.ID, legacy.Version)
	require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
	require.ErrorIs(t, store.AppendReconciliation(ctx, legacy), ErrIdentityConflict, "the frozen schema-1 fixture must not replay through a forged generation 2 discriminator")
}
