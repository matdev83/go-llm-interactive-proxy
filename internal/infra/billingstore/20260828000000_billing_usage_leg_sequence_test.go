package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestUsageLegSequenceSchemaCreatesCallAttemptSeqUniqueIndex(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	var name string
	if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, usageLegCallAttemptSeqIndex).Scan(ctx, &name); err != nil {
		t.Fatalf("call/attempt_seq unique index lookup: %v", err)
	}
	if name != usageLegCallAttemptSeqIndex {
		t.Fatalf("index = %q, want %q", name, usageLegCallAttemptSeqIndex)
	}
	if err := usageLegSequenceSchemaUp(ctx, store.db); err != nil {
		t.Fatalf("usage-leg sequence schema idempotent: %v", err)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
	var attemptSeqType string
	if err := store.db.NewRaw(`SELECT type FROM pragma_table_info('usage_leg_records') WHERE name = 'attempt_seq'`).Scan(ctx, &attemptSeqType); err != nil {
		t.Fatal(err)
	}
	if attemptSeqType != "INTEGER" {
		t.Fatalf("attempt_seq column type = %q, want INTEGER (nullable)", attemptSeqType)
	}
}

// TestSQLiteMigrateFromPreSequenceSchemaPreservesLegacyRows proves the
// brownfield upgrade path: a deployment whose usage_leg_records predates the
// sequence migration keeps its v1 rows readable (attempt_seq NULL, no guessed
// values) after the migration adds the nullable column and the
// (call_id, attempt_seq) unique index on top.
func TestSQLiteMigrateFromPreSequenceSchemaPreservesLegacyRows(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	// Revert the sequence migration in place to reconstruct the pre-fix
	// schema: index first, then the nullable column, then the migration marker.
	if _, err := store.db.NewRaw(`DROP INDEX ` + usageLegCallAttemptSeqIndex).Exec(ctx); err != nil {
		t.Fatalf("drop attempt_seq index: %v", err)
	}
	if _, err := store.db.NewRaw(`ALTER TABLE usage_leg_records DROP COLUMN attempt_seq`).Exec(ctx); err != nil {
		t.Fatalf("drop attempt_seq column: %v", err)
	}
	if _, err := store.db.NewRaw(`DELETE FROM bun_billing_migrations WHERE name = ?`, UsageLegSequenceMigrationName).Exec(ctx); err != nil {
		t.Fatalf("reset sequence migration marker: %v", err)
	}
	var legacyCol string
	if err := store.db.NewRaw(`SELECT name FROM pragma_table_info('usage_leg_records') WHERE name = 'attempt_seq'`).Scan(ctx, &legacyCol); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("pre-fix schema must lack attempt_seq (err=%v)", err)
	}

	// Pre-fix rows were sealed with the v1 contract (no sequence) and written
	// without an attempt_seq column.
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	for _, bLegID := range []string{"b-pre-fix-1", "b-pre-fix-2"} {
		legacySrc := testIndependentCallLegFor(callID, bLegID)
		legacySrc.AttemptSeq = 0
		legacy, sealErr := legacySrc.Seal()
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		payload, marshalErr := json.Marshal(legacy)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		sealedAt := time.Now().UTC()
		if _, err := store.db.NewRaw(`INSERT INTO usage_leg_records( usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, legacy.Key, legacy.Fingerprint, legacy.CallID.String(), legacy.ALegID, legacy.BLegID, legacy.BackendID, legacy.ProviderID, legacy.ModelID, legacy.StartedAt, legacy.FinishedAt, string(legacy.Outcome), string(legacy.Surfaced), string(payload), sealedAt).Exec(ctx); err != nil {
			t.Fatalf("pre-fix insert: %v", err)
		}
	}

	// Upgrade applies only the sequence migration on top of the pre-fix schema.
	if err := Migrate(ctx, store.db); err != nil {
		t.Fatalf("upgrade migrate: %v", err)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema after upgrade: %v", err)
	}

	// Legacy rows remain readable with NULL attempt_seq (order unknown, never
	// guessed) and the new positive-sequence contract coexists in the same call.
	for _, bLegID := range []string{"b-pre-fix-1", "b-pre-fix-2"} {
		key := mustCallLegKey(t, callID, bLegID)
		var attemptSeq sql.NullString
		if err := store.db.NewRaw(`SELECT attempt_seq FROM usage_leg_records WHERE usage_leg_key = ?`, key).Scan(ctx, &attemptSeq); err != nil {
			t.Fatal(err)
		}
		if attemptSeq.Valid {
			t.Fatalf("upgraded legacy attempt_seq = %q, want NULL", attemptSeq.String)
		}
		got, err := store.GetCallLegUsage(ctx, key)
		if err != nil {
			t.Fatalf("legacy row must remain readable after upgrade: %v", err)
		}
		if got.AttemptSeq != 0 {
			t.Fatalf("legacy restored AttemptSeq = %d, want 0 (unknown)", got.AttemptSeq)
		}
	}
	newer := testIndependentCallLegFor(callID, "b-new")
	newer.AttemptSeq = 1
	if err := store.AppendCallLegUsage(ctx, newer); err != nil {
		t.Fatalf("new positive-sequence leg after upgrade: %v", err)
	}
	dup := testIndependentCallLegFor(callID, "b-dup")
	dup.AttemptSeq = 1 // same positive sequence in the same call -> conflict
	if err := store.AppendCallLegUsage(ctx, dup); !errors.Is(err, ErrLegAttemptSequenceConflict) {
		t.Fatalf("duplicate attempt_seq after upgrade = %v, want ErrLegAttemptSequenceConflict", err)
	}
}
