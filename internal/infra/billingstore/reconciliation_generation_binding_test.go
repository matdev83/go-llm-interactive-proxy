package billingstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// openGenerationBindingStore opens a file-backed SQLite store so a second
// handle over the same file represents a process restart. The same DSN can be
// shared by a "test"-scoped handle used by the frozen schema-1 fixture.
func openGenerationBindingStore(t *testing.T, dsn, storeID string) *DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// schema1WireFromSchema2 produces the field-less legacy wire shape for the same
// inner full result: it removes the wire generation field and substitutes the
// caller's valid legacy basis. Decoding it normalizes to the legacy generation.
func schema1WireFromSchema2(t *testing.T, canonical []byte, basis string) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var document map[string]any
	require.NoError(t, decoder.Decode(&document))
	delete(document, "result_schema_version")
	document["basis"] = basis
	wire, err := json.Marshal(document)
	require.NoError(t, err)
	return wire
}

// insertRawGenerationRow writes one reconciliation row directly with an
// explicit durable generation discriminator, basis and tenant projection, so
// the generation gate can be probed independently of the append guards.
func insertRawGenerationRow(t *testing.T, store *DurableStore, storeID string, record ReconciliationRecord, canonicalWire, resultJSON, fingerprint string, generation int64, basis, tenantColumn string) {
	t.Helper()
	subjectJSON, err := json.Marshal(record.Subject)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO billing_reconciliations(store_id, reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, tenant_id, scope, basis, result_schema_version, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, canonical_json, fingerprint, projection_version, created_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		storeID, record.ID, int64(record.Version), string(record.Subject.Kind), subjectIDForEconomics(record.Subject), string(subjectJSON), tenantColumn, record.Scope, basis, generation, record.InputSetHash, record.LocalInputHash, record.ProviderInputHash, record.PolicyID, record.PolicyVersion, resultJSON, canonicalWire, fingerprint, BillingEconomicsProjectionVersion, record.CreatedAt.UnixNano()).Exec(context.Background())
	require.NoError(t, err)
}

// TestReconciliationGenerationBindingRejectsWireColumnCrossGeneration proves
// the decoded canonical wire generation is bound to the durable discriminator
// before any overwrite or projection binding: a field-less schema-1 wire can
// never be returned as generation 2, and an explicit schema-2 wire can never be
// returned as generation 1, on get, list, specialized retention, latest or
// after a restart.
func TestReconciliationGenerationBindingRejectsWireColumnCrossGeneration(t *testing.T) {
	ctx := context.Background()
	storeID := "generation-binding"
	createdAt := time.Unix(1_700_007_000, 0).UTC()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "generation-binding.db")))
	open := func() *DurableStore { return openGenerationBindingStore(t, dsn, storeID) }
	openTestScope := func() *DurableStore { return openGenerationBindingStore(t, dsn, "test") }

	t.Run("fieldless schema-1 wire stored as generation 2 fails closed", func(t *testing.T) {
		store := open()
		result := retentionTestResult(t, storeID, "b-gen-a", "retention-gen-a", 1, createdAt)
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		authoritative, err := record.CanonicalJSON()
		require.NoError(t, err)
		wire := schema1WireFromSchema2(t, authoritative, string(economics.BasisProviderReported))
		insertRawGenerationRow(t, store, storeID, record, string(wire), string(record.ResultJSON), "", int64(ReconciliationRecordSchemaRetention), string(economics.BasisProviderReported), record.Subject.TenantID)

		t.Run("generic get", func(t *testing.T) {
			_, err := store.GetReconciliation(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch, "a schema-1 wire must not be returned as generation 2")
		})
		t.Run("specialized retention", func(t *testing.T) {
			_, err := store.GetReconciliationRetention(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch, "a schema-1 wire must not satisfy a schema-2 retention read")
		})
		t.Run("latest", func(t *testing.T) {
			_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: result.Subject.BLegID})
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
			require.False(t, found)
		})
		t.Run("list", func(t *testing.T) {
			_, err := store.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: result.Subject.BLegID})
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch, "a schema-1 wire must not be listed as generation 2")
		})
		t.Run("restart", func(t *testing.T) {
			reopened := open()
			_, err := reopened.GetReconciliation(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
			_, err = reopened.GetReconciliationRetention(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		})
	})

	t.Run("explicit schema-2 wire stored as generation 1 fails closed", func(t *testing.T) {
		store := open()
		result := retentionTestResult(t, storeID, "b-gen-b", "retention-gen-b", 1, createdAt)
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		authoritative, err := record.CanonicalJSON()
		require.NoError(t, err)
		insertRawGenerationRow(t, store, storeID, record, string(authoritative), string(record.ResultJSON), record.Fingerprint(), int64(ReconciliationRecordSchemaLegacy), "", record.Subject.TenantID)

		t.Run("generic get", func(t *testing.T) {
			_, err := store.GetReconciliation(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch, "a schema-2 wire must not be returned as generation 1")
		})
		t.Run("list", func(t *testing.T) {
			_, err := store.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: result.Subject.BLegID})
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch, "a schema-2 wire must not be listed as generation 1")
		})
		t.Run("specialized retention", func(t *testing.T) {
			_, err := store.GetReconciliationRetention(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		})
		t.Run("latest does not discover the forged generation", func(t *testing.T) {
			_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: result.Subject.BLegID})
			require.NoError(t, err)
			require.False(t, found, "a generation-1 discriminator must not satisfy a retention latest read")
		})
		t.Run("restart", func(t *testing.T) {
			reopened := open()
			_, err := reopened.GetReconciliation(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		})
	})

	t.Run("canonical-empty row may only be legacy generation", func(t *testing.T) {
		store := open()
		result := retentionTestResult(t, storeID, "b-gen-c", "retention-gen-c", 1, createdAt)
		record, err := ReconciliationRecordFromRetention(result)
		require.NoError(t, err)
		insertRawGenerationRow(t, store, storeID, record, "", string(record.ResultJSON), "", int64(ReconciliationRecordSchemaRetention), "", record.Subject.TenantID)

		t.Run("generic get", func(t *testing.T) {
			_, err := store.GetReconciliation(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch, "schema 2 must never be synthesized from projections without canonical bytes")
		})
		t.Run("specialized retention", func(t *testing.T) {
			_, err := store.GetReconciliationRetention(ctx, result.ID, 1)
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
		})
		t.Run("latest", func(t *testing.T) {
			_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: result.Subject.BLegID})
			require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
			require.False(t, found)
		})
	})

	t.Run("canonical-empty legacy row stays readable and replayable", func(t *testing.T) {
		store := openTestScope()
		legacy := legacySchema1Fixture(t)
		canonicalResult, err := canonicalJSON(legacy.ResultJSON)
		require.NoError(t, err)
		insertRawGenerationRow(t, store, "test", legacy, "", string(canonicalResult), "", int64(ReconciliationRecordSchemaLegacy), string(economics.BasisProviderReported), legacy.Subject.TenantID)

		got, err := store.GetReconciliation(ctx, legacy.ID, legacy.Version)
		require.NoError(t, err, "a canonical-empty legacy row must keep its projection-synthesized read")
		require.Equal(t, ReconciliationRecordSchemaLegacy, got.ResultSchemaVersion)
		require.NoError(t, store.AppendReconciliation(ctx, legacy), "canonical-empty legacy replay through result_json must stay idempotent")

		zero := legacy
		zero.ID = "legacy-schema1-zero-generation"
		insertRawGenerationRow(t, store, "test", zero, "", string(canonicalResult), "", 0, string(economics.BasisProviderReported), zero.Subject.TenantID)
		zeroRecord, err := store.GetReconciliation(ctx, zero.ID, zero.Version)
		require.NoError(t, err)
		require.Equal(t, ReconciliationRecordSchemaLegacy, zeroRecord.ResultSchemaVersion, "an explicit zero discriminator normalizes to the legacy generation")
	})

	t.Run("explicit zero and unknown durable discriminators are gated", func(t *testing.T) {
		freshDSN := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "generation-zero.db")))
		store := openGenerationBindingStore(t, freshDSN, "test")

		zero := legacySchema1Fixture(t)
		insertRawGenerationRow(t, store, "test", zero, legacySchema1CanonicalJSON, `{"a":1,"z":2}`, legacySchema1Fingerprint, 0, string(economics.BasisProviderReported), zero.Subject.TenantID)
		got, err := store.GetReconciliation(ctx, zero.ID, zero.Version)
		require.NoError(t, err)
		require.Equal(t, ReconciliationRecordSchemaLegacy, got.ResultSchemaVersion, "an explicit zero discriminator normalizes to the legacy generation on a canonical row too")
		gotCanonical, err := got.CanonicalJSON()
		require.NoError(t, err)
		require.Equal(t, legacySchema1CanonicalJSON, string(gotCanonical), "zero normalization must not rewrite frozen bytes")

		unknown := legacySchema1Fixture(t)
		unknown.ID = "legacy-schema1-unknown-generation"
		insertRawGenerationRow(t, store, "test", unknown, "", `{"a":1,"z":2}`, "", 3, string(economics.BasisProviderReported), unknown.Subject.TenantID)
		_, err = store.GetReconciliation(ctx, unknown.ID, unknown.Version)
		require.ErrorIs(t, err, ErrInvalidReconciliation, "an unknown durable generation must fail closed")
		_, err = store.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: unknown.Subject.BLegID})
		require.ErrorIs(t, err, ErrInvalidReconciliation)
	})
}

// TestReconciliationGenerationBindingRejectsReplayPoison proves an existing row
// is decoded, generation-checked and schema-2 projection-checked before any
// identity comparison, so a row with matching result_json plus an empty
// fingerprint, a forged projection or a forged generation can never satisfy
// replay as schema 2.
func TestReconciliationGenerationBindingRejectsReplayPoison(t *testing.T) {
	ctx := context.Background()
	storeID := "replay-generation"
	createdAt := time.Unix(1_700_007_500, 0).UTC()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "replay-generation.db")))
	open := func() *DurableStore { return openGenerationBindingStore(t, dsn, storeID) }
	openTestScope := func() *DurableStore { return openGenerationBindingStore(t, dsn, "test") }

	t.Run("altered canonical envelope with same result JSON and empty fingerprint", func(t *testing.T) {
		store := open()
		incoming := retentionTestResult(t, storeID, "b-replay-a", "retention-replay-a", 1, createdAt)
		incomingRecord, err := ReconciliationRecordFromRetention(incoming)
		require.NoError(t, err)

		forgedResult := incoming
		forgedResult.Scope = "call:b-replay-a-forged"
		forgedRecord, err := ReconciliationRecordFromRetention(forgedResult)
		require.NoError(t, err)
		forgedCanonical, err := forgedRecord.CanonicalJSON()
		require.NoError(t, err)
		insertRawGenerationRow(t, store, storeID, forgedRecord, string(forgedCanonical), string(incomingRecord.ResultJSON), "", int64(ReconciliationRecordSchemaRetention), "", forgedRecord.Subject.TenantID)

		err = store.AppendReconciliation(ctx, incomingRecord)
		require.ErrorIs(t, err, ErrIdentityConflict, "matching result_json plus an empty fingerprint must not satisfy a schema-2 replay")

		var storedCanonical, storedFingerprint string
		var storedGeneration int64
		require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint, result_schema_version FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ?`, storeID, incoming.ID).Scan(ctx, &storedCanonical, &storedFingerprint, &storedGeneration))
		require.Equal(t, string(forgedCanonical), storedCanonical, "replay must never rewrite stored canonical bytes")
		require.Empty(t, storedFingerprint)
		require.Equal(t, int64(ReconciliationRecordSchemaRetention), storedGeneration)
	})

	t.Run("schema-2 row with forged projection fails replay", func(t *testing.T) {
		store := open()
		incoming := retentionTestResult(t, storeID, "b-replay-b", "retention-replay-b", 1, createdAt)
		incomingRecord, err := ReconciliationRecordFromRetention(incoming)
		require.NoError(t, err)
		authoritative, err := incomingRecord.CanonicalJSON()
		require.NoError(t, err)
		insertRawGenerationRow(t, store, storeID, incomingRecord, string(authoritative), string(incomingRecord.ResultJSON), incomingRecord.Fingerprint(), int64(ReconciliationRecordSchemaRetention), "", "tenant-forged")

		err = store.AppendReconciliation(ctx, incomingRecord)
		require.ErrorIs(t, err, ErrIdentityConflict, "a schema-2 row whose projection disagrees with its envelope must not replay")
		require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)
	})

	t.Run("fieldless schema-1 row marked generation 2 fails schema-2 replay", func(t *testing.T) {
		store := open()
		incoming := retentionTestResult(t, storeID, "b-replay-c", "retention-replay-c", 1, createdAt)
		incomingRecord, err := ReconciliationRecordFromRetention(incoming)
		require.NoError(t, err)
		authoritative, err := incomingRecord.CanonicalJSON()
		require.NoError(t, err)
		wire := schema1WireFromSchema2(t, authoritative, string(economics.BasisProviderReported))
		insertRawGenerationRow(t, store, storeID, incomingRecord, string(wire), string(incomingRecord.ResultJSON), "", int64(ReconciliationRecordSchemaRetention), string(economics.BasisProviderReported), incomingRecord.Subject.TenantID)

		err = store.AppendReconciliation(ctx, incomingRecord)
		require.ErrorIs(t, err, ErrIdentityConflict, "a schema-1 wire marked generation 2 must not satisfy schema-2 replay")
	})

	t.Run("canonical-empty row marked generation 2 fails legacy replay", func(t *testing.T) {
		store := openTestScope()
		legacy := legacySchema1Fixture(t)
		legacy.ID = "legacy-schema1-empty-generation-2"
		canonicalResult, err := canonicalJSON(legacy.ResultJSON)
		require.NoError(t, err)
		insertRawGenerationRow(t, store, "test", legacy, "", string(canonicalResult), "", int64(ReconciliationRecordSchemaRetention), string(economics.BasisProviderReported), legacy.Subject.TenantID)

		err = store.AppendReconciliation(ctx, legacy)
		require.ErrorIs(t, err, ErrIdentityConflict, "a canonical-empty row marked generation 2 must not replay as legacy")
	})

	t.Run("frozen schema-1 row with forged generation discriminator fails replay", func(t *testing.T) {
		store := openTestScope()
		legacy := legacySchema1Fixture(t)
		insertRawGenerationRow(t, store, "test", legacy, legacySchema1CanonicalJSON, `{"a":1,"z":2}`, legacySchema1Fingerprint, int64(ReconciliationRecordSchemaRetention), string(economics.BasisProviderReported), legacy.Subject.TenantID)

		err := store.AppendReconciliation(ctx, legacy)
		require.ErrorIs(t, err, ErrIdentityConflict, "the frozen schema-1 fixture must not replay through a forged generation 2 discriminator")
	})

	t.Run("exact schema-2 replay stays idempotent", func(t *testing.T) {
		store := open()
		incoming := retentionTestResult(t, storeID, "b-replay-valid", "retention-replay-valid", 1, createdAt)
		require.NoError(t, store.AppendReconciliationRetention(ctx, incoming))
		require.NoError(t, store.AppendReconciliationRetention(ctx, incoming), "the authoritative schema-2 mapping must keep replaying idempotently")
	})

	t.Run("canonical-empty legacy replay controls still hold", func(t *testing.T) {
		store := openTestScope()
		legacy := legacySchema1Fixture(t)
		legacy.ID = "legacy-schema1-replay-mismatch"
		canonicalResult, err := canonicalJSON(legacy.ResultJSON)
		require.NoError(t, err)
		insertRawGenerationRow(t, store, "test", legacy, "", string(canonicalResult), "", int64(ReconciliationRecordSchemaLegacy), string(economics.BasisProviderReported), legacy.Subject.TenantID)

		conflict := legacy
		conflict.ResultJSON = json.RawMessage(`{"a":1,"z":3}`)
		require.ErrorIs(t, store.AppendReconciliation(ctx, conflict), ErrIdentityConflict)

		wrongFingerprint := legacy
		wrongFingerprint.ID = "legacy-schema1-replay-fingerprint"
		insertRawGenerationRow(t, store, "test", wrongFingerprint, "", string(canonicalResult), legacySchema1Fingerprint, int64(ReconciliationRecordSchemaLegacy), string(economics.BasisProviderReported), wrongFingerprint.Subject.TenantID)
		require.ErrorIs(t, store.AppendReconciliation(ctx, wrongFingerprint), ErrIdentityConflict, "a nonempty legacy fingerprint must still be enforced")
	})
}
