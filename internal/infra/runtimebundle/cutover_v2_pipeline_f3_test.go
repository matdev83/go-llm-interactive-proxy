package runtimebundle_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreRuntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	_ "modernc.org/sqlite"
)

// Phase 17.3 F3 runtimebundle certification: legal activation followed by a
// fresh V2 customer call through the actual production composition
// (ComposeBilling adapter chooses owner from the durable marker per StoreID,
// retains it via exposure+pin, terminal uses the admitted record, workers
// claim/post with V2 pins/tokens exactly once). No preloaded V1 work,
// direct marker skipping, pin deletion, or test-only insertion. Public
// pkg/lipruntime.Options stays non-money (untouched; composition injects
// billing internally via ComposeBilling).

func f3rbNewStore(t *testing.T, storeID string) *billingstore.DurableStore {
	t.Helper()
	dsn := "file:f3rb-" + storeID + "?mode=memory&cache=shared&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func f3rbCompose(t *testing.T, store *billingstore.DurableStore) runtimebundle.ProductionOptions {
	t.Helper()
	catalog, _, _ := seededComposeCatalog(t)
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:             store,
		TerminalUsageSink: store,
		Catalog:           catalog,
		Currency:          "USD",
		ModelMaxOutput:    composeModelMax,
		PostTurnBatchSize: 8,
		Strict:            true,
	})
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	if prod.BillingExposureAdmission == nil || prod.BillingTerminalUsageSink == nil || prod.BillingStore == nil {
		t.Fatal("incomplete production composition")
	}
	return prod
}

func f3rbAdmissionInput(callID string, accountID string) coreRuntime.BillingExposureAdmissionInput {
	primary := routing.Primary{Backend: "openai-responses", Model: "gpt"}
	return coreRuntime.BillingExposureAdmissionInput{
		BillingAdmissionInput: coreRuntime.BillingAdmissionInput{
			Call:   lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-f3rb"}},
			ALegID: "a-f3rb", BillingCallID: callID,
			Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
			RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
			Scope:       scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)},
			SessionID:   "sess-f3rb",
			AccountID:   accountID,
		},
		CallID: callID,
	}
}

type f3rbProviderStub struct{}

func (f3rbProviderStub) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return billing.OperatorCostResult{}, err
	}
	return billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}, nil
}

type f3rbRatingStub struct {
	charge int64
	fp     string
	callID billing.BillingCallID
}

func (s f3rbRatingStub) ResolveCallRating(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure) (billing.CallRatingResult, error) {
	return billing.CallRatingResult{CallID: complete.Closure.CallID, CustomerCharge: billing.Money{Nano: s.charge, Currency: "USD"}, Fingerprint: s.fp}, nil
}

func TestF3RuntimeBundleV2PipelineAfterLegalActivation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3rbNewStore(t, "f3rb-pipe")
	prod := f3rbCompose(t, store)
	accountID := "acct-f3rb-pipe"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 90000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	// Legal empty/drained sequence via coordinator only.
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f3rb-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = sh
	if _, _, err := store.BeginCutoverDraining(ctx, "f3rb-drain"); err != nil {
		t.Fatal(err)
	}
	activated, err := store.ActivateCutoverV2(ctx, "f3rb-activate")
	if err != nil {
		t.Fatalf("legal empty activation: %v", err)
	}
	if activated.State != billing.AccountingCutoverV2Active {
		t.Fatalf("activated = %q, want v2_active", activated.State)
	}
	// Legacy V1 store admission remains fenced in active.
	v1Call, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	v1Closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: v1Call, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v7"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    []string{"b-v1"},
	}
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: v1Call.String(),
		Max:        billing.Money{Nano: 50, Currency: "USD"},
		PricingRef: v1Closure.CustomerPricingRef, ChargePolicyRef: v1Closure.ChargePolicyRef,
	}); err == nil {
		t.Fatalf("active legacy V1 AdmitExposure must be fenced")
	}
	// Fresh V2 via the actual production admission path. The composed
	// adapter chooses V2 from the durable marker (no invented owner).
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
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("V2 admission pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || pin.MarkerState != billing.AccountingCutoverV2Active {
		t.Fatalf("admission pin must be V2 active, got %#v", pin)
	}
	// Terminal evidence under the admitted V2 owner (provider leg first
	// appears at terminal). Uses the admitted record, not a fresh global.
	closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(201, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: exp.PricingRef, ChargePolicyRef: exp.ChargePolicyRef,
		ExpectedBLegIDs: []string{"b-f3rb"},
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 terminal closure: %v", err)
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-f3rb", BLegID: "b-f3rb", AttemptSeq: 1,
		BackendID: "openai-responses", ProviderID: "p", ModelID: "gpt",
		StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(200, 500000000).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens: billing.Quantity{Value: 5, Present: true}, OutputTokens: billing.Quantity{Value: 2, Present: true},
			Cost:      billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:    billing.EvidenceSourceProviderReported,
			Authority: billing.EvidenceAuthorityAuthoritative, DedupeKey: "f3rb-charge-1",
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v1"},
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, leg, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 terminal leg: %v", err)
	}
	// Claim/post exactly once with V2 pins/tokens through production workers.
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3rbProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider worker: %v", err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3rbRatingStub{charge: 90, fp: "f3rb-fp"}, store, 8)
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
}

func TestF3RuntimeBundleExplicitV2RejectedPreactive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3rbNewStore(t, "f3rb-pre")
	prod := f3rbCompose(t, store)
	accountID := "acct-f3rb-pre"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	in := f3rbAdmissionInput(callID.String(), accountID)
	// Explicit V2 via adapter is rejected pre-active (v1_active, no marker).
	type v2Admitter interface {
		AdmitV2(context.Context, coreRuntime.BillingExposureAdmissionInput) (billing.CallExposure, error)
	}
	adapter, ok := prod.BillingExposureAdmission.(v2Admitter)
	if !ok {
		t.Fatalf("production adapter must expose explicit AdmitV2")
	}
	if _, err := adapter.AdmitV2(ctx, in); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("preactive AdmitV2 err = %v, want V2NotAuthorized", err)
	}
	// Explicit store V2 likewise rejected.
	closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v7"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    []string{"b-x"},
	}
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: 50, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("preactive store V2 err = %v, want V2NotAuthorized", err)
	}
}
