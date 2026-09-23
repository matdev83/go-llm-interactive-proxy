package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// legacySchema1CanonicalJSON and legacySchema1Fingerprint are frozen fixtures
// derived by running the checkpoint 2301e083 serializer for the record built by
// legacySchema1Fixture. They must never change: pre-migration rows were written
// with exactly these bytes and this fingerprint.
const legacySchema1CanonicalJSON = `{"id":"legacy-schema1-fixture","version":1,"subject":{"kind":"b_leg","store_id":"test","tenant_id":"tenant-test","a_leg_id":"a-legacy","billing_call_id":"call-legacy","b_leg_id":"b-legacy"},"scope":"call","basis":"provider_reported","input_set_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","local_input_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","provider_input_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","policy_id":"policy-legacy","policy_version":"v1","result":{"a":1,"z":2},"created_at":"2023-11-14T22:21:40Z"}`

const legacySchema1Fingerprint = "241bd07977e9b3aa35ffd0b9db3dc058d208b99ecd191cd28fbd06247f0ad970"

func legacySchema1Fixture(t *testing.T) ReconciliationRecord {
	t.Helper()
	return ReconciliationRecord{
		ID:      "legacy-schema1-fixture",
		Version: 1,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant-test",
			ALegID: "a-legacy", BillingCallID: "call-legacy", BLegID: "b-legacy",
		},
		Scope:             "call",
		Basis:             economics.BasisProviderReported,
		InputSetHash:      strings.Repeat("a", 64),
		LocalInputHash:    strings.Repeat("b", 64),
		ProviderInputHash: strings.Repeat("c", 64),
		PolicyID:          "policy-legacy",
		PolicyVersion:     "v1",
		ResultJSON:        json.RawMessage(`{"z":2,"a":1}`),
		CreatedAt:         time.Unix(1_700_000_500, 0).UTC(),
	}
}

// insertLegacySchema1Row stores the frozen pre-migration bytes exactly as the
// checkpoint serializer wrote them, including the old fingerprint.
//
//nolint:revive // test helper keeps t first per Go testing convention
func insertLegacySchema1Row(t *testing.T, ctx context.Context, store *DurableStore) {
	t.Helper()
	_, err := store.db.ExecContext(ctx, `INSERT INTO billing_reconciliations(store_id, reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, tenant_id, scope, basis, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, canonical_json, fingerprint, projection_version, created_at_unix) VALUES ('test','legacy-schema1-fixture',1,'b_leg','b-legacy','{}','tenant-test','call','provider_reported',?,?,?,?,?,?,?,?,1,?)`,
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64),
		"policy-legacy", "v1", `{"a":1,"z":2}`, legacySchema1CanonicalJSON, legacySchema1Fingerprint, int64(1_700_000_500)*1_000_000_000)
	require.NoError(t, err)
}

// TestReconciliationSchema1CanonicalBytesAndReplayIdentity proves the serializer
// still emits the exact pre-migration bytes/fingerprint and that unchanged
// legacy rows replay instead of conflicting.
func TestReconciliationSchema1CanonicalBytesAndReplayIdentity(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	record := legacySchema1Fixture(t)
	canonical, err := record.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, legacySchema1CanonicalJSON, string(canonical), "schema-1 canonical bytes must match the checkpoint fixture")
	require.Equal(t, legacySchema1Fingerprint, record.Fingerprint(), "schema-1 fingerprint must match the checkpoint fixture")
	require.NotContains(t, string(canonical), "result_schema_version", "schema-1 wire must not carry the new field")

	insertLegacySchema1Row(t, ctx, store)

	got, err := store.GetReconciliation(ctx, record.ID, record.Version)
	require.NoError(t, err)
	gotCanonical, err := got.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, legacySchema1CanonicalJSON, string(gotCanonical), "reading a legacy row must not rewrite its canonical bytes")
	require.Equal(t, legacySchema1Fingerprint, got.Fingerprint(), "reading a legacy row must preserve its stored fingerprint")
	require.Equal(t, ReconciliationRecordSchemaLegacy, got.ResultSchemaVersion)

	require.NoError(t, store.AppendReconciliation(ctx, record), "exact replay of an unchanged legacy row must be idempotent")

	conflict := record
	conflict.ResultJSON = json.RawMessage(`{"a":3}`)
	require.ErrorIs(t, store.AppendReconciliation(ctx, conflict), ErrIdentityConflict, "changed payload under the same identity must still conflict")

	var storedCanonical, storedFingerprint string
	require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint FROM billing_reconciliations WHERE store_id = 'test' AND reconciliation_id = ?`, record.ID).Scan(ctx, &storedCanonical, &storedFingerprint))
	require.Equal(t, legacySchema1CanonicalJSON, storedCanonical)
	require.Equal(t, legacySchema1Fingerprint, storedFingerprint)

	t.Run("fresh append stores frozen bytes", func(t *testing.T) {
		fresh := newSQLiteTestStore(t)
		require.NoError(t, fresh.AppendReconciliation(ctx, legacySchema1Fixture(t)))
		var canonicalJSON, fingerprint string
		require.NoError(t, fresh.db.NewRaw(`SELECT canonical_json, fingerprint FROM billing_reconciliations WHERE store_id = 'test' AND reconciliation_id = ?`, record.ID).Scan(ctx, &canonicalJSON, &fingerprint))
		require.Equal(t, legacySchema1CanonicalJSON, canonicalJSON)
		require.Equal(t, legacySchema1Fingerprint, fingerprint)
	})
}

// TestReconciliationSchema1RestartAndSchema2Separation proves legacy bytes
// survive a restart and that schema-2 records keep their own canonical shape.
func TestReconciliationSchema1RestartAndSchema2Separation(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "schema1-compat.db")))
	open := func() *DurableStore {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(4)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
		require.NoError(t, err)
		return store
	}

	first := open()
	insertLegacySchema1Row(t, ctx, first)
	retention := retentionTestResult(t, "test", "b-schema2", "retention-schema2", 1, time.Unix(1_700_000_600, 0).UTC())
	require.NoError(t, first.AppendReconciliationRetention(ctx, retention))
	require.NoError(t, first.Close())

	second := open()
	defer func() { _ = second.Close() }()
	record := legacySchema1Fixture(t)
	got, err := second.GetReconciliation(ctx, record.ID, record.Version)
	require.NoError(t, err)
	gotCanonical, err := got.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, legacySchema1CanonicalJSON, string(gotCanonical))
	require.Equal(t, legacySchema1Fingerprint, got.Fingerprint())
	require.NoError(t, second.AppendReconciliation(ctx, record), "restart replay of an unchanged legacy row must be idempotent")

	schema2, err := second.GetReconciliation(ctx, retention.ID, retention.ResultRevision)
	require.NoError(t, err)
	require.Equal(t, ReconciliationRecordSchemaRetention, schema2.ResultSchemaVersion)
	schema2Canonical, err := schema2.CanonicalJSON()
	require.NoError(t, err)
	require.Contains(t, string(schema2Canonical), `"result_schema_version":2`)
}
