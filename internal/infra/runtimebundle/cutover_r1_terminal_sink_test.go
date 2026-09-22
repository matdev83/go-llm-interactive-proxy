package runtimebundle_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingspool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

// Phase 17.3 R1 RED: the real composed terminal sink must serve freshly
// admitted V2 calls. Activate legally, admit via the composed admission
// adapter (auto-V2), append call+leg through prod.BillingTerminalUsageSink
// (never direct store WithOwner), run real workers, prove exactly-once V2
// settlement. Before the fix both appends are fenced as V1.
func TestR1ProdTerminalSinkServesFreshV2Call(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3rbNewStore(t, "r1-sink")
	prod := f3rbCompose(t, store)
	accountID := "acct-r1-sink"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 90000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r1-shadow",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "r1-drain"); err != nil {
		t.Fatal(err)
	}
	activated, err := store.ActivateCutoverV2(ctx, "r1-activate")
	if err != nil {
		t.Fatalf("legal empty activation: %v", err)
	}
	if activated.State != billing.AccountingCutoverV2Active {
		t.Fatalf("activated = %q, want v2_active", activated.State)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	exp, err := prod.BillingExposureAdmission.Admit(ctx, f3rbAdmissionInput(callID.String(), accountID))
	if err != nil {
		t.Fatalf("production adapter Admit in active (auto-V2): %v", err)
	}
	if !exp.IsOpen() {
		t.Fatalf("V2 exposure must be open")
	}
	closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(201, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: exp.PricingRef, ChargePolicyRef: exp.ChargePolicyRef,
		ExpectedBLegIDs: []string{"b-r1"},
	}
	// Real composed terminal transport: never direct WithOwner.
	if err := prod.BillingTerminalUsageSink.AppendCall(ctx, closure); err != nil {
		t.Fatalf("R1 composed terminal AppendCall for admitted V2: %v", err)
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-f3rb", BLegID: "b-r1", AttemptSeq: 1,
		BackendID: "openai-responses", ProviderID: "p", ModelID: "gpt",
		StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(200, 500000000).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens: billing.Quantity{Value: 5, Present: true}, OutputTokens: billing.Quantity{Value: 2, Present: true},
			Cost:      billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:    billing.EvidenceSourceProviderReported,
			Authority: billing.EvidenceAuthorityAuthoritative, DedupeKey: "r1-charge-1",
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v1"},
	}
	if err := prod.BillingTerminalUsageSink.AppendLeg(ctx, leg); err != nil {
		t.Fatalf("R1 composed terminal AppendLeg for admitted V2: %v", err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3rbProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider worker: %v", err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3rbRatingStub{t: t, charge: 90}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer worker: %v", err)
	}
	acct, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.BalanceNano != 90000-90 {
		t.Fatalf("balance = %d, want %d", acct.BalanceNano, 90000-90)
	}
	txs, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 {
		t.Fatalf("journals = %d, want 2 (customer+provider exactly once)", len(txs))
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if afterPin.Owner != billing.PostingOwnerV2 || !afterPin.IsCompleted() {
		t.Fatalf("customer pin must be V2 completed, got %#v", afterPin)
	}
	if expAfter, err := store.GetCallExposure(ctx, callID); err != nil {
		t.Fatal(err)
	} else if expAfter.IsOpen() {
		t.Fatalf("V2 exposure must close")
	}
	// Exactly-once replay through the same composed sink + workers.
	if err := prod.BillingTerminalUsageSink.AppendCall(ctx, closure); err != nil {
		t.Fatalf("composed terminal replay AppendCall: %v", err)
	}
	if err := prod.BillingTerminalUsageSink.AppendLeg(ctx, leg); err != nil {
		t.Fatalf("composed terminal replay AppendLeg: %v", err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider replay: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer replay: %v", err)
	}
	acct2, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acct2.BalanceNano != acct.BalanceNano {
		t.Fatalf("replay moved balance %d -> %d", acct.BalanceNano, acct2.BalanceNano)
	}
	txs2, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs2) != 2 {
		t.Fatalf("replay journals = %d, want 2", len(txs2))
	}
}

// Phase 17.3 R1 spool proof: the deployed process-local spool is the real
// terminal transport. Admit via the composed adapter, append via the spool as
// prod.BillingTerminalUsageSink, restart the spool from the same stable file
// before delivery, then deliver with real spool workers + billing workers and
// prove exactly-once V2 settlement.
func TestR1ProdTerminalSpoolReplayServesFreshV2Call(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3rbNewStore(t, "r1-spool")
	accountID := "acct-r1-spool"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 70000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r1-spool-shadow",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "r1-spool-drain"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "r1-spool-activate"); err != nil {
		t.Fatalf("legal empty activation: %v", err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	spoolPath := filepath.Join(t.TempDir(), "r1-spool", "spool.db")
	spool, err := billingspool.Open(ctx, billingspool.Config{Path: spoolPath}, store)
	if err != nil {
		t.Fatal(err)
	}
	catalog, _, _ := seededComposeCatalog(t)
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store: store, TerminalUsageSink: spool, Catalog: catalog,
		Currency: "USD", ModelMaxOutput: composeModelMax, PostTurnBatchSize: 8, Strict: true,
	})
	if err != nil {
		t.Fatalf("ComposeBilling with spool sink: %v", err)
	}
	exp, err := prod.BillingExposureAdmission.Admit(ctx, f3rbAdmissionInput(callID.String(), accountID))
	if err != nil {
		t.Fatalf("production adapter Admit in active (auto-V2): %v", err)
	}
	closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(300, 0).UTC(), FinishedAt: time.Unix(301, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: exp.PricingRef, ChargePolicyRef: exp.ChargePolicyRef,
		ExpectedBLegIDs: []string{"b-r1s"},
	}
	if err := prod.BillingTerminalUsageSink.AppendCall(ctx, closure); err != nil {
		t.Fatalf("spool AppendCall: %v", err)
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-f3rb", BLegID: "b-r1s", AttemptSeq: 1,
		BackendID: "openai-responses", ProviderID: "p", ModelID: "gpt",
		StartedAt: time.Unix(300, 0).UTC(), FinishedAt: time.Unix(300, 500000000).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens: billing.Quantity{Value: 5, Present: true}, OutputTokens: billing.Quantity{Value: 2, Present: true},
			Cost:      billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:    billing.EvidenceSourceProviderReported,
			Authority: billing.EvidenceAuthorityAuthoritative, DedupeKey: "r1s-charge-1",
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v1"},
	}
	if err := prod.BillingTerminalUsageSink.AppendLeg(ctx, leg); err != nil {
		t.Fatalf("spool AppendLeg: %v", err)
	}
	if got := spool.PendingCount(); got != 2 {
		t.Fatalf("spool pending = %d, want 2 before restart", got)
	}
	// Restart before delivery: close and reopen the same stable spool file.
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	spool2, err := billingspool.Open(ctx, billingspool.Config{Path: spoolPath}, store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool2.Close() }()
	if got := spool2.PendingCount(); got != 2 {
		t.Fatalf("spool pending after restart = %d, want 2", got)
	}
	// Real spool delivery into the central store, then real billing workers.
	for spool2.PendingCount() > 0 {
		if err := spool2.ProcessOnce(ctx); err != nil {
			t.Fatalf("spool delivery after restart: %v", err)
		}
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3rbProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider worker: %v", err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3rbRatingStub{t: t, charge: 70}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer worker: %v", err)
	}
	acct, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.BalanceNano != 70000-70 {
		t.Fatalf("balance = %d, want %d", acct.BalanceNano, 70000-70)
	}
	txs, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 {
		t.Fatalf("journals = %d, want 2 (customer+provider exactly once)", len(txs))
	}
}
