package billingstore

import (
	"context"
	"testing"
)

func TestPhase9Repair5_SQLiteValuationContextHashIsImmutable(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if _, err := store.db.NewRaw(`INSERT INTO billing_valuations(
		store_id, valuation_id, valuation_version, perspective, basis, subject_kind, subject_id,
		tenant_id, scope, input_set_hash, rater_id, rater_version, tariff_id, tariff_version,
		policy_id, policy_version, qualifier_snapshot, canonical_json, fingerprint, projection_version,
		created_at_unix, valuation_context_hash
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"test", "repair5-context", 2, "customer", "provider_reported", "b_leg", "b-repair5",
		"", "call:repair5", "", "", "", "", "", "", "", "", "{}", "", 1, 1, "context-a").Exec(ctx); err != nil {
		t.Fatalf("insert valuation: %v", err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_valuations SET valuation_context_hash = ? WHERE valuation_id = ?`, "context-b", "repair5-context").Exec(ctx); err == nil {
		t.Fatal("context-hash mutation succeeded; immutable valuation guard must reject it")
	}
}
