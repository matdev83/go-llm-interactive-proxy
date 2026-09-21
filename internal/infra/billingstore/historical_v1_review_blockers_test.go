package billingstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func phase171InsertRawLegRow(t *testing.T, store *DurableStore, key, fingerprint, callIDColumn string, record billing.CallLegUsageRecord) {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encode raw leg: %v", err)
	}
	if _, err := store.db.NewRaw(`INSERT INTO usage_leg_records(usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, attempt_seq, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		key, fingerprint, callIDColumn, record.ALegID, record.BLegID, nil,
		record.BackendID, record.ProviderID, record.ModelID, record.StartedAt, record.FinishedAt,
		string(record.Outcome), string(record.Surfaced), string(raw), time.Now().UTC()).Exec(context.Background()); err != nil {
		t.Fatalf("insert raw leg row: %v", err)
	}
}

// Review blocker 1 RED at the storage seam: row metadata must be validated,
// not discarded by a payload-only loader.
func TestPhase171ReviewStoreWriterClaimRejectsRowDrift(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	sealedCall, sealedLeg := phase171SeedBaselineV1(t, store)

	// Insert a second leg row for the same call whose fingerprint column does
	// not match its sealed payload. A payload-only loader still resolves V1
	// ownership and returns nil.
	second := sealedLeg
	second.BLegID = "b-secondary"
	second.Key = ""
	second.Fingerprint = ""
	resealed, err := second.Seal()
	if err != nil {
		t.Fatalf("seal second leg: %v", err)
	}
	phase171InsertRawLegRow(t, store, resealed.Key, "deadbeef", sealedCall.CallID.String(), resealed)
	if err := store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.HistoricalV1WriterVersion); err == nil {
		t.Fatal("fingerprint-drifted row must fail closed")
	}
}

func TestPhase171ReviewStoreWriterClaimRejectsPayloadCallMismatch(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	sealedCall, sealedLeg := phase171SeedBaselineV1(t, store)
	_ = sealedLeg

	// Insert a row whose call_id column matches the request but whose sealed
	// payload belongs to a foreign call. The loader must not accept it as
	// same-call ownership proof.
	foreign := sealedLeg
	foreign.CallID = billing.BillingCallID("bc_ffffffffffffffffffffffffffffffff")
	foreign.BLegID = "b-foreign"
	foreign.Key = ""
	foreign.Fingerprint = ""
	resealed, err := foreign.Seal()
	if err != nil {
		t.Fatalf("seal foreign leg: %v", err)
	}
	phase171InsertRawLegRow(t, store, resealed.Key, resealed.Fingerprint, sealedCall.CallID.String(), resealed)
	if err := store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.HistoricalV1WriterVersion); err == nil {
		t.Fatal("payload call mismatch must fail closed")
	}
}
