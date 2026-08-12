package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteAppendUsageRecordSealsTURAndIsReplaySafe(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	journalAccount(t, store, "handoff-account")
	record := billing.TurnUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		AccountID:     "handoff-account", TurnID: "turn-1", ALegID: "a-1", AuthorizationID: "auth-1",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted,
		Legs: []billing.LegUsageRecord{{
			ALegID: "a-1", BLegID: "b-1", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model",
			StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
			Evidence:        billing.FinalBillingEvidence{InputTokens: billing.Quantity{Value: 1, Present: true}},
			OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v1"},
		}},
	}
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUsageRecord(ctx, "handoff-account:turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Legs) != 1 || got.Legs[0].BLegID != "b-1" || got.Legs[0].Evidence.InputTokens.Value != 1 || got.Legs[0].OperatorRateRef != (billing.VersionRef{ID: "operator-rates", Version: "v1"}) {
		t.Fatalf("record = %+v", got)
	}
	state, err := store.GetProcessing(ctx, got.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "pending" {
		t.Fatalf("processing state = %+v", state)
	}
}

func TestSQLiteAppendUsageRecordSealsSessionID(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	journalAccount(t, store, "handoff-session")
	record := billing.TurnUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		AccountID:     "handoff-session", TurnID: "turn-1", ALegID: "a-1", AuthorizationID: "auth-1",
		SessionID: "proxy-sess", StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted,
		Legs: []billing.LegUsageRecord{{
			ALegID: "a-1", BLegID: "b-1", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model",
			StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
			Evidence: billing.FinalBillingEvidence{InputTokens: billing.Quantity{Value: 1, Present: true}},
		}},
	}
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUsageRecord(ctx, "handoff-session:turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "proxy-sess" {
		t.Fatalf("SessionID = %q, want proxy-sess", got.SessionID)
	}
	var storedSession string
	if err := store.db.NewRaw(`SELECT session_id FROM turn_usage_records WHERE tur_key = ?`, got.Key).Scan(ctx, &storedSession); err != nil {
		t.Fatal(err)
	}
	if storedSession != "proxy-sess" {
		t.Fatalf("session_id column = %q, want proxy-sess", storedSession)
	}
}

func TestAppendUsageRecordRejectsUnsealableRecord(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	journalAccount(t, store, "handoff-zero")
	record := billing.TurnUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		AccountID:     "handoff-zero", TurnID: "turn-1", ALegID: "a-1", AuthorizationID: "auth-1",
		Outcome: billing.TurnOutcomeFailed,
		Legs: []billing.LegUsageRecord{{
			ALegID: "a-1", BLegID: "b-never", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model",
			Outcome: billing.LegOutcomeLoser, Surfaced: billing.SurfacedNo,
		}},
	}
	err := store.AppendUsageRecord(ctx, record)
	if err == nil {
		t.Fatal("expected Seal rejection for unsealable TUR")
	}
	if !errors.Is(err, billing.ErrInvalidRecord) {
		t.Fatalf("error = %v, want %v", err, billing.ErrInvalidRecord)
	}
}
