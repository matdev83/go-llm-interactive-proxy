package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair7_LegacyEmptyHashValuationReplayPreservesPayload(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "repair7-legacy-replay-observation", 1)
	valuation := phase4EconomicsValuation(t, observation, "repair7-legacy-replay", time.Unix(1_700_040_000, 0).UTC(), "")
	valuation.InputSetHash = ""
	payload, err := valuation.CanonicalJSON()
	if err != nil {
		t.Fatalf("legacy canonical payload: %v", err)
	}
	fingerprint := valuation.Fingerprint()
	insertRepair7LegacyValuation(t, store, valuation, string(payload), fingerprint, 1)
	if err := insertValuationLines(ctx, store.db, store.storeID, valuation); err != nil {
		t.Fatalf("legacy line fixture: %v", err)
	}

	if err := store.AppendValuation(ctx, valuation); err != nil {
		t.Fatalf("legacy replay: %v", err)
	}
	var gotPayload, gotFingerprint, gotInputHash string
	if err := store.db.NewRaw(`SELECT canonical_json, fingerprint, input_set_hash FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ?`, store.storeID, valuation.ID, int64(valuation.Version)).Scan(ctx, &gotPayload, &gotFingerprint, &gotInputHash); err != nil {
		t.Fatalf("legacy row lookup: %v", err)
	}
	if gotPayload != string(payload) || gotFingerprint != fingerprint || gotInputHash != "" {
		t.Fatalf("legacy replay drifted row: payload=%q fingerprint=%q input_hash=%q", gotPayload, gotFingerprint, gotInputHash)
	}

	mutated := valuation.Clone()
	mutated.Lines[0].Amount = &metering.Decimal{Coefficient: "126", Scale: 3}
	if err := store.AppendValuation(ctx, mutated); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("legacy semantic mutation error=%v, want ErrIdentityConflict", err)
	}
}

func TestPhase9Repair7_LegacyEmptyHashValuationRebuildPreservesPayload(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "repair7-legacy-rebuild-observation", 1)
	valuation := phase4EconomicsValuation(t, observation, "repair7-legacy-rebuild", time.Unix(1_700_040_001, 0).UTC(), "")
	valuation.InputSetHash = ""
	payload, err := valuation.CanonicalJSON()
	if err != nil {
		t.Fatalf("legacy canonical payload: %v", err)
	}
	fingerprint := valuation.Fingerprint()
	insertRepair7LegacyValuation(t, store, valuation, string(payload), fingerprint, 0)

	if err := store.RebuildValuationProjections(ctx); err != nil {
		t.Fatalf("legacy projection rebuild: %v", err)
	}
	var gotPayload, gotFingerprint, gotInputHash string
	var projectionVersion int
	if err := store.db.NewRaw(`SELECT canonical_json, fingerprint, input_set_hash, projection_version FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ?`, store.storeID, valuation.ID, int64(valuation.Version)).Scan(ctx, &gotPayload, &gotFingerprint, &gotInputHash, &projectionVersion); err != nil {
		t.Fatalf("legacy row lookup: %v", err)
	}
	if gotPayload != string(payload) || gotFingerprint != fingerprint || gotInputHash != "" {
		t.Fatalf("legacy rebuild drifted row: payload=%q fingerprint=%q input_hash=%q", gotPayload, gotFingerprint, gotInputHash)
	}
	if projectionVersion != BillingEconomicsProjectionVersion {
		t.Fatalf("legacy projection version=%d, want %d", projectionVersion, BillingEconomicsProjectionVersion)
	}
	var lineCount int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuation_lines WHERE store_id = ? AND valuation_id = ? AND valuation_version = ?`, store.storeID, valuation.ID, int64(valuation.Version)).Scan(ctx, &lineCount); err != nil {
		t.Fatalf("legacy line count: %v", err)
	}
	if lineCount != len(valuation.Lines) {
		t.Fatalf("legacy line count=%d, want %d", lineCount, len(valuation.Lines))
	}
}

func insertRepair7LegacyValuation(t *testing.T, store *DurableStore, valuation economics.Valuation, payload, fingerprint string, projectionVersion int) {
	t.Helper()
	_, err := store.db.NewRaw(`
INSERT INTO billing_valuations(
	store_id, valuation_id, valuation_version, perspective, basis, subject_kind, subject_id, tenant_id, scope, input_set_hash,
	rater_id, rater_version, tariff_id, tariff_version, policy_id, policy_version, qualifier_snapshot, canonical_json, fingerprint, projection_version, created_at_unix, valuation_context_hash
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		store.storeID, valuation.ID, int64(valuation.Version), string(valuation.Perspective), string(valuation.Basis), string(valuation.Subject.Kind), subjectIDForEconomics(valuation.Subject), valuation.Subject.TenantID, valuation.Scope, "",
		valuation.Rater.RaterID, valuation.Rater.Version, valuation.Tariff.ID, valuation.Tariff.Version, valuation.Policy.PolicyID, valuation.Policy.Version, valuation.QualifierSnapshot, payload, fingerprint, projectionVersion, valuation.CreatedAt.UnixNano(), "").Exec(context.Background())
	if err != nil {
		t.Fatalf("legacy row insert: %v", err)
	}
}
