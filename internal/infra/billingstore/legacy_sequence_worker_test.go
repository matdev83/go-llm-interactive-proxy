package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

func TestLegacySequenceWorkerAmbiguity(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	// 1. Create a prepaid account
	accountID := "legacy-seq-acct"
	account := billing.Account{
		ID:          accountID,
		Currency:    "USD",
		Mode:        billing.AccountPrepaid,
		BalanceNano: 1000000,
		State:       billing.AccountReady,
		Version:     1,
	}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}

	// 2. Setup snapshot catalog with defaults and charge-all policy pricing.
	pricing := billing.PricingSnapshot{
		Ref:                  billing.VersionRef{ID: "prices", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  100,
		OutputPerMillionNano: 200,
		InputRatePresent:     true,
		OutputRatePresent:    true,
		FixedCharges:         []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	policySurfaced := billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "policy-surfaced", Version: "v1"},
		PricingRef:          pricing.Ref,
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}
	policyChargeAll := billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "policy-chargeall", Version: "v1"},
		PricingRef:          pricing.Ref,
		Scope:               billing.ChargeAllPotentialLegs,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}

	catalog := billingcompose.NewSnapshotCatalog()
	if err := catalog.PutPricing(pricing); err != nil {
		t.Fatal(err)
	}
	if err := catalog.PutPolicy(policySurfaced); err != nil {
		t.Fatal(err)
	}
	if err := catalog.PutPolicy(policyChargeAll); err != nil {
		t.Fatal(err)
	}

	resolver, err := billingcompose.NewCallRatingResolver(catalog)
	if err != nil {
		t.Fatal(err)
	}

	worker, err := billing.NewCallPostUsageWorker(store, store, resolver, 10)
	if err != nil {
		t.Fatal(err)
	}

	// Helper to create a call with legacy null-sequence legs.
	createLegacyCall := func(callID billing.BillingCallID, outcome billing.TurnOutcome, policyRef billing.VersionRef, legs []billing.CallLegUsageRecord) {
		expectedIDs := make([]string, 0, len(legs))
		for _, l := range legs {
			expectedIDs = append(expectedIDs, l.BLegID)
		}
		call := billing.CallUsageRecord{
			SchemaVersion:      billing.CurrentRecordSchemaVersion,
			CallID:             callID,
			AccountID:          accountID,
			ALegID:             "a-leg",
			SessionID:          "sess",
			StartedAt:          time.Unix(100, 0).UTC(),
			FinishedAt:         time.Unix(101, 0).UTC(),
			Outcome:            outcome,
			CustomerPricingRef: pricing.Ref,
			ChargePolicyRef:    policyRef,
			ExpectedBLegIDs:    expectedIDs,
		}
		if err := store.AppendCallUsage(ctx, call); err != nil {
			t.Fatal(err)
		}
		for _, l := range legs {
			if err := store.AppendCallLegUsage(ctx, l); err != nil {
				t.Fatal(err)
			}
		}
		_, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
			AccountID:       accountID,
			CallID:          callID.String(),
			Max:             billing.Money{Nano: 100000, Currency: "USD"},
			PricingRef:      pricing.Ref,
			ChargePolicyRef: policyRef,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// --- Case A: Completed surfaced sequence-independent case should settle ---
	callIDA, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	legsA := []billing.CallLegUsageRecord{
		{
			CallID: callIDA, ALegID: "a-leg", BLegID: "b-1", AttemptSeq: 0,
			BackendID: "back", ProviderID: "prov", ModelID: "model",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true}, Source: billing.EvidenceSourceProviderReported, Authority: billing.EvidenceAuthorityAuthoritative,
			},
		},
		{
			CallID: callIDA, ALegID: "a-leg", BLegID: "b-2", AttemptSeq: 0,
			BackendID: "back", ProviderID: "prov", ModelID: "model",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 2000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true}, Source: billing.EvidenceSourceProviderReported, Authority: billing.EvidenceAuthorityAuthoritative,
			},
		},
	}
	createLegacyCall(callIDA, billing.TurnOutcomeCompleted, policySurfaced.Ref, legsA)

	// --- Case B: Charge-all policy sequence-independent case should settle ---
	callIDB, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	legsB := []billing.CallLegUsageRecord{
		{
			CallID: callIDB, ALegID: "a-leg", BLegID: "b-1", AttemptSeq: 0,
			BackendID: "back", ProviderID: "prov", ModelID: "model",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true}, Source: billing.EvidenceSourceProviderReported, Authority: billing.EvidenceAuthorityAuthoritative,
			},
		},
		{
			CallID: callIDB, ALegID: "a-leg", BLegID: "b-2", AttemptSeq: 0,
			BackendID: "back", ProviderID: "prov", ModelID: "model",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 2000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true}, Source: billing.EvidenceSourceProviderReported, Authority: billing.EvidenceAuthorityAuthoritative,
			},
		},
	}
	createLegacyCall(callIDB, billing.TurnOutcomeCanceled, policyChargeAll.Ref, legsB)

	// --- Case C: Sequence-dependent ambiguous case should fail and reconcile ---
	callIDC, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	legsC := []billing.CallLegUsageRecord{
		{
			CallID: callIDC, ALegID: "a-leg", BLegID: "b-1", AttemptSeq: 0,
			BackendID: "back", ProviderID: "prov", ModelID: "model",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true}, Source: billing.EvidenceSourceProviderReported, Authority: billing.EvidenceAuthorityAuthoritative,
			},
		},
		{
			CallID: callIDC, ALegID: "a-leg", BLegID: "b-2", AttemptSeq: 0,
			BackendID: "back", ProviderID: "prov", ModelID: "model",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 2000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true}, Source: billing.EvidenceSourceProviderReported, Authority: billing.EvidenceAuthorityAuthoritative,
			},
		},
	}
	createLegacyCall(callIDC, billing.TurnOutcomeCanceled, policySurfaced.Ref, legsC)

	before, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if before.BalanceNano != account.BalanceNano {
		t.Fatalf("balance before worker = %d, want %d", before.BalanceNano, account.BalanceNano)
	}

	err = worker.ProcessOnce(ctx)
	if !errors.Is(err, billing.ErrBillingAttemptSequenceUnknown) {
		t.Fatalf("ProcessOnce error = %v, want ErrBillingAttemptSequenceUnknown", err)
	}

	// Verify Case A status
	var statusA string
	if err := store.db.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callIDA.String()).Scan(ctx, &statusA); err != nil {
		t.Fatal(err)
	}
	if statusA != "processed" {
		t.Errorf("Case A (completed surfaced) claim_status = %q, want 'processed'", statusA)
	}

	// Verify Case B status
	var statusB string
	if err := store.db.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callIDB.String()).Scan(ctx, &statusB); err != nil {
		t.Fatal(err)
	}
	if statusB != "processed" {
		t.Errorf("Case B (charge all policy) claim_status = %q, want 'processed'", statusB)
	}
	after, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BalanceNano != account.BalanceNano-30 {
		t.Fatalf("balance after settled Cases A and B = %d, want %d", after.BalanceNano, account.BalanceNano-30)
	}

	// Verify Case C status
	var statusC string
	if err := store.db.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callIDC.String()).Scan(ctx, &statusC); err != nil {
		t.Fatal(err)
	}
	if statusC != "reconcile_required" {
		t.Errorf("Case C (ambiguous) claim_status = %q, want 'reconcile_required'", statusC)
	}
}
