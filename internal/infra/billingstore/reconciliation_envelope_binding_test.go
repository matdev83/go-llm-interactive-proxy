package billingstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// TestReconciliationRetentionEnvelopeBindingRejectsOuterMismatches proves a
// caller-supplied schema-2 envelope that disagrees with its canonical inner
// full result is rejected before replay lookup and insertion, for every outer
// identity field, and that the authoritative mapping stays bound.
func TestReconciliationRetentionEnvelopeBindingRejectsOuterMismatches(t *testing.T) {
	ctx := context.Background()
	createdAt := time.Unix(1_700_006_000, 0).UTC()

	mutations := []struct {
		name         string
		mutate       func(*ReconciliationRecord)
		wantMismatch bool
	}{
		{"reconciliation id", func(record *ReconciliationRecord) { record.ID = "other-reconciliation" }, true},
		{"revision", func(record *ReconciliationRecord) { record.Version = 2 }, true},
		// A foreign subject kind is rejected by subject validation before the
		// envelope comparison, which is also fail-closed.
		{"subject kind", func(record *ReconciliationRecord) { record.Subject.Kind = metering.SubjectALeg }, false},
		{"subject account", func(record *ReconciliationRecord) { record.Subject.AccountID = "account-other" }, true},
		{"subject b-leg id", func(record *ReconciliationRecord) { record.Subject.BLegID = "b-other" }, true},
		{"subject tenant", func(record *ReconciliationRecord) { record.Subject.TenantID = "tenant-other" }, true},
		{"scope", func(record *ReconciliationRecord) { record.Scope = "call:other" }, true},
		{"input set hash", func(record *ReconciliationRecord) { record.InputSetHash = strings.Repeat("d", 64) }, true},
		{"local input hash", func(record *ReconciliationRecord) { record.LocalInputHash = strings.Repeat("e", 64) }, true},
		{"provider input hash", func(record *ReconciliationRecord) { record.ProviderInputHash = strings.Repeat("f", 64) }, true},
		{"policy id", func(record *ReconciliationRecord) { record.PolicyID = "other-policy" }, true},
		{"policy version", func(record *ReconciliationRecord) { record.PolicyVersion = "v2" }, true},
		{"creation time", func(record *ReconciliationRecord) { record.CreatedAt = record.CreatedAt.Add(time.Second) }, true},
	}

	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			store := newSQLiteTestStore(t)
			result := retentionTestResult(t, "test", "b-envelope", "retention-envelope-"+slugged(tc.name), 1, createdAt)
			record, err := ReconciliationRecordFromRetention(result)
			require.NoError(t, err)
			tc.mutate(&record)
			err = store.AppendReconciliation(ctx, record)
			require.ErrorIs(t, err, ErrInvalidReconciliation)
			if tc.wantMismatch {
				require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
			}
			var count int
			require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ?`, "test").Scan(ctx, &count))
			require.Zero(t, count, "mismatched envelope must not be inserted")
		})
	}

	t.Run("subject store mismatch is rejected on the named store", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		other, err := NewDurableStore(ctx, store.db, Config{StoreID: "store-other"})
		require.NoError(t, err)
		result := retentionTestResult(t, "test", "b-envelope", "retention-envelope-store", 1, createdAt)
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		record.Subject.StoreID = "store-other"
		err = other.AppendReconciliation(ctx, record)
		require.ErrorIs(t, err, ErrInvalidReconciliation)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ?`, "store-other").Scan(ctx, &count))
		require.Zero(t, count, "cross-subject envelope must not be inserted")
	})

	t.Run("mismatch is rejected before replay lookup", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		result := retentionTestResult(t, "test", "b-envelope", "retention-envelope-replay", 1, createdAt)
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		record.Scope = "call:other"
		err = store.AppendReconciliation(ctx, record)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		require.NotErrorIs(t, err, ErrIdentityConflict)
		stored, err := store.GetReconciliationRetention(ctx, result.ID, result.ResultRevision)
		require.NoError(t, err)
		require.Equal(t, result.Scope, stored.Scope)
	})

	t.Run("authoritative mapping remains bound and readable", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		result := retentionTestResult(t, "test", "b-envelope", "retention-envelope-valid", 1, createdAt)
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
		got, err := store.GetReconciliationRetention(ctx, result.ID, result.ResultRevision)
		require.NoError(t, err)
		require.Equal(t, result.ID, got.ID)
		require.Equal(t, result.Scope, got.Scope)
	})
}

// TestReconciliationRetentionEnvelopeBindingFailsClosedOnRawRows proves a
// pre-existing schema-2 row whose canonical envelope disagrees with its inner
// result, or whose projection columns disagree with its canonical envelope,
// fails closed on get, retention read, latest lookup and restart instead of
// being repaired, overwritten or returned.
func TestReconciliationRetentionEnvelopeBindingFailsClosedOnRawRows(t *testing.T) {
	ctx := context.Background()
	storeID := "envelope-binding-restart"
	createdAt := time.Unix(1_700_006_100, 0).UTC()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "envelope-binding.db")))
	open := func() *DurableStore {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(4)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		seedTestSchemaIfEmpty(t, bunDB)
		store, err := NewDurableStore(ctx, bunDB, Config{StoreID: storeID})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		return store
	}

	insertRaw := func(t *testing.T, store *DurableStore, record ReconciliationRecord, canonicalWire []byte, tenantColumn string) {
		t.Helper()
		insertRawRetentionRecord(t, store, storeID, record, canonicalWire, tenantColumn)
	}

	assertFailsClosed := func(t *testing.T, store *DurableStore, id, bLegID string) {
		t.Helper()
		_, err := store.GetReconciliation(ctx, id, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, err = store.GetReconciliationRetention(ctx, id, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: bLegID})
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		require.False(t, found)
	}

	t.Run("canonical wire outer mismatch", func(t *testing.T) {
		store := open()
		result := retentionTestResult(t, storeID, "b-wire", "retention-wire-mismatch", 1, createdAt)
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		authoritative, err := record.CanonicalJSON()
		require.NoError(t, err)
		forged := forgeEnvelopeDocument(t, authoritative, func(document map[string]any) {
			document["scope"] = "call:forged"
		})
		insertRaw(t, store, record, forged, result.Subject.TenantID)
		assertFailsClosed(t, store, result.ID, result.Subject.BLegID)

		// A second store handle over the same file represents a restart and
		// must fail closed on the same row instead of repairing it.
		reopened := open()
		assertFailsClosed(t, reopened, result.ID, result.Subject.BLegID)
	})

	t.Run("projection column mismatch", func(t *testing.T) {
		store := open()
		result := retentionTestResult(t, storeID, "b-projection", "retention-projection-mismatch", 1, createdAt)
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		authoritative, err := record.CanonicalJSON()
		require.NoError(t, err)
		insertRaw(t, store, record, authoritative, "tenant-forged")

		_, err = store.GetReconciliation(ctx, result.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, err = store.GetReconciliationRetention(ctx, result.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: result.Subject.BLegID, TenantID: "tenant-forged"})
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		require.False(t, found)

		reopened := open()
		_, err = reopened.GetReconciliationRetention(ctx, result.ID, 1)
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
	})
}

func slugged(name string) string {
	return strings.ReplaceAll(name, " ", "-")
}

// insertRawRetentionRecord writes one schema-2 row directly, bypassing the
// store append guards, so reads can be proven to fail closed on rows written
// before this binding existed.
func insertRawRetentionRecord(t *testing.T, store *DurableStore, storeID string, record ReconciliationRecord, canonicalWire []byte, tenantColumn string) {
	t.Helper()
	subjectJSON, err := json.Marshal(record.Subject)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO billing_reconciliations(store_id, reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, tenant_id, scope, basis, result_schema_version, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, canonical_json, fingerprint, projection_version, created_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		storeID, record.ID, int64(record.Version), string(record.Subject.Kind), subjectIDForEconomics(record.Subject), string(subjectJSON), tenantColumn, record.Scope, "", int64(ReconciliationRecordSchemaRetention), record.InputSetHash, record.LocalInputHash, record.ProviderInputHash, record.PolicyID, record.PolicyVersion, string(record.ResultJSON), string(canonicalWire), "", BillingEconomicsProjectionVersion, record.CreatedAt.UnixNano()).Exec(context.Background())
	require.NoError(t, err)
}

// forgeEnvelopeDocument applies a JSON-level mutation to a canonical
// reconciliation envelope and re-encodes it, preserving byte-canonical shape.
func forgeEnvelopeDocument(t *testing.T, canonical []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var document map[string]any
	require.NoError(t, decoder.Decode(&document))
	mutate(document)
	forged, err := json.Marshal(document)
	require.NoError(t, err)
	return forged
}
