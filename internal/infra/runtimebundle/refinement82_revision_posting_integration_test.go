package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	_ "modernc.org/sqlite"
)

// Literal fixture tariff expectations for sub-pass B. All values come from
// the shared billing-host-loop fixtures, never from production output:
// opening funding 10000, customer 310 = 100 input + 200 output + 10 fixed,
// operator 125 = 50 input + 75 output, finalizer charge 7, correction
// charge 9 (delta +2), currency USD throughout.
const (
	refinement82OpeningNano   = int64(10000)
	refinement82CustomerNano  = int64(310)
	refinement82OperatorNano  = int64(125)
	refinement82FinalizerNano = int64(7)
	refinement82CorrectedNano = int64(9)
)

func refinement82ProviderJournalIDs(transactions []billing.JournalTransaction) []string {
	ids := make([]string, 0, len(transactions))
	for _, transaction := range transactions {
		ids = append(ids, transaction.ID)
	}
	return ids
}

func refinement82RequireProviderJournal(t *testing.T, transaction billing.JournalTransaction, bLegID string, wantNano int64) {
	t.Helper()
	if transaction.OperationKind != "provider_call_cogs" {
		t.Fatalf("journal operation = %q, want provider_call_cogs", transaction.OperationKind)
	}
	if transaction.Currency != "USD" {
		t.Fatalf("journal currency = %q, want USD", transaction.Currency)
	}
	if transaction.BLegID != bLegID {
		t.Fatalf("journal B-leg = %q, want %q", transaction.BLegID, bLegID)
	}
	if len(transaction.Entries) == 0 {
		t.Fatalf("journal %q has no entries", transaction.ID)
	}
	var debit, credit int64
	var sawDebitLedger, sawCreditLedger bool
	for _, entry := range transaction.Entries {
		if entry.Amount.Currency != "USD" || entry.Amount.Nano <= 0 {
			t.Fatalf("journal %q entry = %+v, want positive USD", transaction.ID, entry)
		}
		switch entry.Side {
		case billing.JournalDebit:
			debit += entry.Amount.Nano
			if entry.LedgerAccount == "inference_provider_cogs" {
				sawDebitLedger = true
			}
		case billing.JournalCredit:
			credit += entry.Amount.Nano
			if entry.LedgerAccount == "provider_payable_clearing" {
				sawCreditLedger = true
			}
		default:
			t.Fatalf("journal %q entry side = %q", transaction.ID, entry.Side)
		}
	}
	if debit != wantNano || credit != wantNano {
		t.Fatalf("journal %q debit/credit = %d/%d, want %d/%d", transaction.ID, debit, credit, wantNano, wantNano)
	}
	if !sawDebitLedger || !sawCreditLedger {
		t.Fatalf("journal %q entries = %+v, want inference_provider_cogs debit and provider_payable_clearing credit", transaction.ID, transaction.Entries)
	}
}

func refinement82RequireCustomerSettlement(t *testing.T, transactions []billing.JournalTransaction, wantNano int64) billing.JournalTransaction {
	t.Helper()
	if len(transactions) != 1 {
		t.Fatalf("customer settlements = %d, want exactly one", len(transactions))
	}
	settlement := transactions[0]
	if settlement.OperationKind != "customer_call_settlement" {
		t.Fatalf("settlement operation = %q, want customer_call_settlement", settlement.OperationKind)
	}
	if settlement.Currency != "USD" {
		t.Fatalf("settlement currency = %q, want USD", settlement.Currency)
	}
	if len(settlement.Entries) == 0 || settlement.Entries[0].Amount.Nano != wantNano {
		t.Fatalf("settlement entries = %+v, want %d", settlement.Entries, wantNano)
	}
	return settlement
}

func refinement82SetupPostingHost(t *testing.T, ctx context.Context, storeID, billingPath, journalPath string, extraOperatorBindings ...string) (*billingstore.DurableStore, *runtimebundle.Host, *billingcompose.SnapshotCatalog, billing.ObservationEconomicWorkBuilder) {
	t.Helper()
	store := openRefinement52ConcurrentBillingStore(t, billingPath, storeID)
	catalog, _, _, operator := seedBillingHostLoopCatalog(t)
	if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding: %v", err)
	}
	for _, backendID := range extraOperatorBindings {
		if err := catalog.SetOperatorRateBinding(backendID, billingHostLoopModelID, operator.Ref); err != nil {
			t.Fatalf("SetOperatorRateBinding(%s): %v", backendID, err)
		}
	}
	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store: store, TerminalUsageSink: store, Catalog: catalog, Currency: "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 128000, true, nil },
		Strict:         true, ConservativeCeiling: &ceiling, PostTurnBatchSize: 1,
	})
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	configPath := writeRefinement52MeteredConfig(t, journalPath)
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath: configPath, Mandatory: lipsdk.StandardDistributionRequirements(), LogWriter: io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP, Production: prod,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)
	return store, host, catalog, prod.BillingObservationEconomicWorkBuilder
}

// TestRefinement82ProviderRevisionPostingsAdvancePerStage drives rev1
// (terminal provider evidence), rev2 (terminal authoritative checkpoint via
// provider finalizer), and rev3 (post-terminal correction) for one B-leg
// through the actual stock workers and asserts the durable monetary trail
// after every stage with literal fixture amounts:
//
//   - rev1 posts exactly one +125 provider COGS journal and the provider-cost
//     head carries 125 at revision 1; default retail is still unposted while
//     the provider plane has advanced (zero customer settlements pre-claim);
//   - at call closure the real settlement path posts exactly one 310
//     customer settlement and the balance reaches 9690;
//   - rev2 posts only the +7 delta with ReversalOf/CorrectsTransactionID
//     linkage; customer settlement and balance are unchanged;
//   - rev3 posts only the +2 delta with chained linkage across a host
//     restart; worker cursors, fences, journals, and balances survive the
//     restart and continue idempotently;
//   - exact replay of every stage creates no new journal, balance,
//     exposure, valuation, work, or provider-head duplicate.
func TestRefinement82ProviderRevisionPostingsAdvancePerStage(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	storeID := "refinement82-postings"
	billingPath := filepath.Join(t.TempDir(), "billing.sqlite")
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	store, host, catalog, builder := refinement82SetupPostingHost(t, ctx, storeID, billingPath, journalPath)
	executor := hostActiveExecutor(t, host)
	journal, ok := executor.MeteringRecorder.(*journalstore.DurableStore)
	if !ok || journal == nil {
		t.Fatalf("stock metering recorder = %T, want durable journal", executor.MeteringRecorder)
	}
	if _, ok := executor.MeteringObservationSink.(metering.AtomicObservationSink); !ok {
		t.Fatalf("stock observation sink = %T, want atomic sink", executor.MeteringObservationSink)
	}
	lateAppender, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: store, Sink: executor.MeteringObservationSink, Resolver: journal,
	})
	if err != nil {
		t.Fatalf("NewLateEconomicAppender: %v", err)
	}
	accountID := "refinement82-postings-account"
	provisionBillingHostLoopAccount(t, store, accountID)
	injectRefinement52AuthenticUsageBackend(t, executor, accountID)
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})

	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: "refinement82-postings-session",
			ContinuityKey:          "refinement82-postings-session",
		},
		Route:    lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("postings per stage")}}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// No speculative posting: before the stream reaches DONE, no closure,
	// journal, or settlement can exist.
	if records, err := store.ListCallUsage(ctx, accountID); err != nil || len(records) != 0 {
		t.Fatalf("pre-terminal closures = %+v, err = %v; want none", records, err)
	}
	if got := refinement4StockProviderJournals(t, store, accountID); len(got) != 0 {
		t.Fatalf("pre-terminal provider journals = %+v, want none", got)
	}
	if got := refinement4StockCustomerTransactions(t, store, accountID); len(got) != 0 {
		t.Fatalf("pre-terminal customer settlements = %+v, want none", got)
	}
	if !refinement82RuntimeSawUsage(drainBillingHostLoopStream(t, ctx, stream)) {
		t.Fatal("stream carried no provider usage before DONE")
	}

	// The terminal handoff row exists (closure present, unclaimed). The
	// provider plane must advance through the real worker to a durable rev-1
	// head with no test-side claim involved.
	closuresUnclaimed := waitBillingHostLoopCallRecords(t, store, accountID)
	if len(closuresUnclaimed) != 1 {
		t.Fatalf("unclaimed closures = %d, want 1", len(closuresUnclaimed))
	}
	closure1 := closuresUnclaimed[0]
	legs1, err := store.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs1) != 1 {
		t.Fatalf("call legs = %d, want 1", len(legs1))
	}
	leg1 := legs1[0]
	authority := refinement52RuntimeProviderAuthority(t, leg1)
	initialPage, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: leg1.BLegID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	initialWorks, err := builder.BuildEconomicRevisionWork(ctx, initialPage.Observations)
	if err != nil {
		t.Fatal(err)
	}
	var initialProvider billing.EconomicRevisionWork
	for _, work := range initialWorks {
		if work.Queue == billing.EconomicQueueProvider {
			initialProvider = work
			break
		}
	}
	if initialProvider.HeadKey == "" || initialProvider.EvidenceRevision != 1 {
		t.Fatalf("initial provider work = %+v, want revision 1", initialProvider)
	}
	waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, initialProvider.HeadKey, 1, initialProvider.Input.InputSetHash)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure1.CallID, initialProvider.HeadKey, refinement82OperatorNano)
	// Timing separation, observed without racing the settlement worker: while
	// the provider plane has already advanced to a durable rev-1 head, retail
	// must still be unposted whenever the call exposure is still open. When
	// the worker has already closed the exposure, the structural proof below
	// (exactly one settlement per sealed closure, TurnID-linked) applies.
	if exposure, err := store.GetCallExposure(ctx, closure1.CallID); err != nil {
		t.Fatalf("GetCallExposure pre-claim: %v", err)
	} else if exposure.IsOpen() {
		t.Logf("ordering window observed: provider head rev1 durable while exposure open")
		if got := refinement4StockCustomerTransactions(t, store, accountID); len(got) != 0 {
			t.Fatalf("retail posted before call claim while provider already advanced: %+v", got)
		}
	} else {
		t.Logf("ordering window missed: exposure already closed; structural proof applies")
	}
	closure1, _, _ = waitBillingHostLoopCall(t, store, accountID)
	exposure1 := waitRefinement4StockSettlement(t, ctx, store, accountID, closure1.CallID)
	if exposure1.IsOpen() {
		t.Fatalf("settlement left exposure open: %+v", exposure1)
	}

	// rev1 money: exactly one +125 COGS journal on the winner B-leg, head at
	// 125/rev1, exactly one 310 customer settlement, balance 9690.
	providerJournals1 := refinement4StockProviderJournals(t, store, accountID)
	if len(providerJournals1) != 1 {
		t.Fatalf("provider COGS journals after rev1 = %d, want 1", len(providerJournals1))
	}
	refinement82RequireProviderJournal(t, providerJournals1[0], leg1.BLegID, refinement82OperatorNano)
	costHead1, err := store.GetProviderCostHead(ctx, accountID, closure1.CallID, initialProvider.HeadKey)
	if err != nil {
		t.Fatalf("provider cost head rev1: %v", err)
	}
	if costHead1.EvidenceRevision != 1 || costHead1.CurrentAmount.Nano != refinement82OperatorNano || costHead1.CurrentAmount.Currency != "USD" {
		t.Fatalf("provider cost head rev1 = %+v, want revision 1/current 125 USD", costHead1)
	}
	settlement1 := refinement82RequireCustomerSettlement(t, refinement4StockCustomerTransactions(t, store, accountID), refinement82CustomerNano)
	acct1, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acct1.BalanceNano != refinement82OpeningNano-refinement82CustomerNano {
		t.Fatalf("balance after rev1 settlement = %d, want %d", acct1.BalanceNano, refinement82OpeningNano-refinement82CustomerNano)
	}
	acctVersionAfterSettlement := acct1.Version

	// rev2 terminal checkpoint via the real late sideband: only the +7 delta
	// posts, chained to the initial journal; customer plane is untouched.
	identity := coremetering.LateEconomicIdentity{
		StoreID: storeID, BillingCallID: closure1.CallID, ALegID: closure1.ALegID, BLegID: leg1.BLegID,
		AttemptID: leg1.BLegID, AttemptSeq: uint64(leg1.AttemptSeq), ProviderID: leg1.ProviderID,
		ProviderAccountKey: authority.Subject.ProviderAccountKey, ProviderRequestID: authority.Subject.ProviderRequestID, ProviderChargeID: authority.Subject.ProviderChargeID,
	}
	finalizer := refinement52RuntimeObservation(storeID, closure1.CallID, closure1.ALegID, leg1, authority, "82-postings-finalizer", 2, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer}); err != nil {
		t.Fatalf("late provider finalizer: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	expectedFinalizer := refinement52ExpectedProviderWork(t, ctx, builder, journal, storeID, leg1.BLegID, nil)
	if expectedFinalizer.EvidenceRevision != 2 {
		t.Fatalf("finalizer provider work revision = %d, want 2", expectedFinalizer.EvidenceRevision)
	}
	waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedFinalizer.HeadKey, 2, expectedFinalizer.Input.InputSetHash)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure1.CallID, expectedFinalizer.HeadKey, refinement82OperatorNano+refinement82FinalizerNano)
	providerJournals2 := refinement4StockProviderJournals(t, store, accountID)
	if len(providerJournals2) != 2 {
		t.Fatalf("provider COGS journals after rev2 = %d, want initial + one delta", len(providerJournals2))
	}
	if providerJournals2[0].ID != providerJournals1[0].ID || providerJournals2[0].SemanticFingerprint != providerJournals1[0].SemanticFingerprint {
		t.Fatal("rev2 rewrote the initial COGS journal")
	}
	refinement82RequireProviderJournal(t, providerJournals2[1], leg1.BLegID, refinement82FinalizerNano)
	if providerJournals2[1].ReversalOf != providerJournals2[0].ID || providerJournals2[1].CorrectsTransactionID != providerJournals2[0].ID {
		t.Fatalf("rev2 journal linkage = %+v, want reversal/correction of %q", providerJournals2[1], providerJournals2[0].ID)
	}
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure1.CallID, expectedFinalizer.HeadKey, refinement82OperatorNano+refinement82FinalizerNano)
	customerAfterRev2 := refinement4StockCustomerTransactions(t, store, accountID)
	if len(customerAfterRev2) != 1 || customerAfterRev2[0].ID != settlement1.ID || customerAfterRev2[0].SemanticFingerprint != settlement1.SemanticFingerprint {
		t.Fatalf("rev2 altered customer settlement: before=%+v after=%+v", settlement1, customerAfterRev2)
	}
	acctAfterRev2, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acctAfterRev2.BalanceNano != acct1.BalanceNano || acctAfterRev2.Version != acctVersionAfterSettlement {
		t.Fatal("rev2 altered customer balance/version")
	}

	// Restart between stages: worker cursors, fences, journals, and balances
	// survive the restart and continue idempotently on the rebuilt host.
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("close first stock host: %v", err)
	}
	store2 := openRefinement52ConcurrentBillingStore(t, billingPath, storeID)
	prod2, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store: store2, TerminalUsageSink: store2, Catalog: catalog, Currency: "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 128000, true, nil },
		Strict:         true, ConservativeCeiling: &billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}, PostTurnBatchSize: 1,
	})
	if err != nil {
		t.Fatalf("ComposeBilling after restart: %v", err)
	}
	configPath := writeRefinement52MeteredConfig(t, journalPath)
	host2, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath: configPath, Mandatory: lipsdk.StandardDistributionRequirements(), LogWriter: io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP, Production: prod2,
	})
	if err != nil {
		t.Fatalf("BuildHost after restart: %v", err)
	}
	hostServeCleanup(t, host2)
	executor2 := hostActiveExecutor(t, host2)
	journal2, ok := executor2.MeteringRecorder.(*journalstore.DurableStore)
	if !ok || journal2 == nil {
		t.Fatalf("restarted metering recorder = %T, want durable journal", executor2.MeteringRecorder)
	}
	lateAppender2, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: store2, Sink: executor2.MeteringObservationSink, Resolver: journal2,
	})
	if err != nil {
		t.Fatalf("NewLateEconomicAppender after restart: %v", err)
	}
	restartedJournals := refinement4StockProviderJournals(t, store2, accountID)
	if len(restartedJournals) != len(providerJournals2) {
		t.Fatalf("restart changed provider journal count: before=%d after=%d", len(providerJournals2), len(restartedJournals))
	}
	for i := range providerJournals2 {
		if restartedJournals[i].ID != providerJournals2[i].ID || restartedJournals[i].SemanticFingerprint != providerJournals2[i].SemanticFingerprint {
			t.Fatalf("restart changed provider journal %d", i)
		}
	}

	// rev3 post-terminal correction on the rebuilt host: only the +2 delta
	// posts with chained linkage; execution stays closed; customer untouched.
	correction := refinement52RuntimeObservation(storeID, closure1.CallID, closure1.ALegID, leg1, authority, "82-postings-correction", 3, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
	correction.SourceEventKey = finalizer.SourceEventKey
	correction.Subject = finalizer.Subject
	correction.Correlation = finalizer.Correlation
	correction.Semantics = metering.SemanticsCorrection
	correction.Charges[0].ChargeItemID = finalizer.Charges[0].ChargeItemID
	correctionAmount := metering.DecimalFromNanoUnits(refinement82CorrectedNano)
	correction.Charges[0].Amount = &correctionAmount
	priorRef, err := finalizer.Ref(storeID)
	if err != nil {
		t.Fatalf("finalizer ref: %v", err)
	}
	correction.Supersedes = []metering.ObservationRef{priorRef}
	if err := lateAppender2.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicCorrection, Identity: identity, Observation: correction}); err != nil {
		t.Fatalf("late provider correction after restart: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store2, journal2)
	expectedCorrection := refinement52ExpectedProviderWork(t, ctx, prod2.BillingObservationEconomicWorkBuilder, journal2, storeID, leg1.BLegID, nil)
	if expectedCorrection.EvidenceRevision != 3 {
		t.Fatalf("correction provider work revision = %d, want 3", expectedCorrection.EvidenceRevision)
	}
	correctionHead := waitRefinement52StockHeadExact(t, ctx, store2, journal2, accountID, billing.EconomicQueueProvider, expectedCorrection.HeadKey, 3, expectedCorrection.Input.InputSetHash)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store2, accountID, closure1.CallID, expectedCorrection.HeadKey, refinement82OperatorNano+refinement82CorrectedNano)
	providerJournals3 := refinement4StockProviderJournals(t, store2, accountID)
	if len(providerJournals3) != 3 {
		t.Fatalf("provider COGS journals after rev3 = %d, want initial + two deltas", len(providerJournals3))
	}
	refinement82RequireProviderJournal(t, providerJournals3[2], leg1.BLegID, refinement82CorrectedNano-refinement82FinalizerNano)
	if providerJournals3[2].ReversalOf != providerJournals3[1].ID || providerJournals3[2].CorrectsTransactionID != providerJournals3[1].ID {
		t.Fatalf("rev3 journal linkage = %+v, want reversal/correction of %q", providerJournals3[2], providerJournals3[1].ID)
	}
	waitRefinement4StockProviderCurrentAmount(t, ctx, store2, accountID, closure1.CallID, expectedCorrection.HeadKey, refinement82OperatorNano+refinement82CorrectedNano)
	legsAfterRev3, err := store2.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legsAfterRev3) != 1 || legsAfterRev3[0].Fingerprint != leg1.Fingerprint {
		t.Fatalf("rev3 rewrote lifecycle leg: before=%+v after=%+v", leg1, legsAfterRev3)
	}
	customerAfterRev3 := refinement4StockCustomerTransactions(t, store2, accountID)
	if len(customerAfterRev3) != 1 || customerAfterRev3[0].ID != settlement1.ID || customerAfterRev3[0].SemanticFingerprint != settlement1.SemanticFingerprint {
		t.Fatalf("rev3 altered customer settlement: before=%+v after=%+v", settlement1, customerAfterRev3)
	}
	acctAfterRev3, err := store2.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acctAfterRev3.BalanceNano != acct1.BalanceNano || acctAfterRev3.Version != acctVersionAfterSettlement {
		t.Fatal("rev3 altered customer balance/version")
	}

	// Exact replay of every stage: no new journal, balance, exposure,
	// valuation, work, or provider-head duplicate.
	for _, late := range []coremetering.LateEconomicEvidence{
		{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer},
		{Kind: coremetering.LateEconomicCorrection, Identity: identity, Observation: correction},
	} {
		if err := lateAppender2.AppendLateEconomicEvidence(ctx, late); err != nil {
			t.Fatalf("exact late evidence replay (%s): %v", late.Kind, err)
		}
	}
	waitRefinement4StockOutboxDrained(t, ctx, store2, journal2)
	replayedHead := waitRefinement52StockHeadExact(t, ctx, store2, journal2, accountID, billing.EconomicQueueProvider, expectedCorrection.HeadKey, 3, expectedCorrection.Input.InputSetHash)
	if replayedHead.Fingerprint != correctionHead.Fingerprint || replayedHead.HeadVersion != correctionHead.HeadVersion || replayedHead.Fence != correctionHead.Fence {
		t.Fatalf("replay advanced provider head: before=%+v after=%+v", correctionHead, replayedHead)
	}
	replayedJournals := refinement4StockProviderJournals(t, store2, accountID)
	if strings.Join(refinement82ProviderJournalIDs(replayedJournals), "\x00") != strings.Join(refinement82ProviderJournalIDs(providerJournals3), "\x00") {
		t.Fatal("replay created provider journal duplicates")
	}
	for i := range providerJournals3 {
		if replayedJournals[i].SemanticFingerprint != providerJournals3[i].SemanticFingerprint {
			t.Fatalf("replay altered provider journal %d", i)
		}
	}
	acctReplayed, err := store2.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acctReplayed.BalanceNano != acct1.BalanceNano || acctReplayed.Version != acctVersionAfterSettlement {
		t.Fatal("replay altered customer balance/version")
	}
	if got := refinement4StockCustomerTransactions(t, store2, accountID); len(got) != 1 || got[0].ID != settlement1.ID {
		t.Fatalf("replay altered customer settlement: %+v", got)
	}
}

// TestRefinement82RetryLoserExcludedFromRetailWinnerPostsCOGS executes a
// stock failover call (pre-output "bad" failure plus "good" winner) and
// proves exactly the mechanically executed split through production money:
//
//   - both B-legs are recorded in lifecycle (never-started failure plus
//     winner); the loser carries no provider-payable charge (local
//     diagnostics only) and posts no COGS journal, while the winner's
//     operator-payable 125 posts exactly once with the winner B-leg identity;
//   - default retail posts exactly one 310 settlement for the winner-selected
//     quantities; the loser contributes zero tokens and zero charge;
//   - the balance reaches exactly 9690 with no duplicate or phantom posting.
//
// Boundary (honest): this never-started loser proves nothing about a
// provider-payable failed/loser B-leg; that all-payable inclusion case
// belongs to retail-selector scope (8.3), not to Task 8.2, and is not
// claimed here.
func TestRefinement82RetryLoserExcludedFromRetailWinnerPostsCOGS(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	storeID := "refinement82-retry"
	billingPath := filepath.Join(t.TempDir(), "billing.sqlite")
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	store, host, _, _ := refinement82SetupPostingHost(t, ctx, storeID, billingPath, journalPath, "bad", "good")
	executor := hostActiveExecutor(t, host)
	if _, ok := executor.MeteringRecorder.(*journalstore.DurableStore); !ok {
		t.Fatalf("stock metering recorder = %T, want durable journal", executor.MeteringRecorder)
	}
	accountID := "refinement82-retry-account"
	provisionBillingHostLoopAccount(t, store, accountID)
	opens := injectBillingHostLoopFailoverBackends(t, executor)
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: "refinement82-retry-session",
			ContinuityKey:          "refinement82-retry-session",
		},
		Route:    lipapi.RouteIntent{Selector: "bad:" + billingHostLoopModelID + "|good:" + billingHostLoopModelID},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("retry economics")}}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute failover call: %v", err)
	}
	if !refinement82RuntimeSawUsage(drainBillingHostLoopStream(t, ctx, stream)) {
		t.Fatal("failover stream carried no provider usage before DONE")
	}
	if got := opens.Load(); got != 2 {
		t.Fatalf("backend opens = %d, want failed bad open plus successful good open", got)
	}
	closure, _, complete := waitBillingHostLoopCall(t, store, accountID)
	legs, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 2 {
		t.Fatalf("failover legs = %+v, want never-started failure plus winner", legs)
	}
	if len(complete.Legs) != 2 {
		t.Fatalf("complete call legs = %d, want 2", len(complete.Legs))
	}
	var winner, loser billing.CallLegUsageRecord
	for _, leg := range legs {
		switch leg.Outcome {
		case billing.LegOutcomeWinner:
			winner = leg
		case billing.LegOutcomeNeverStarted, billing.LegOutcomeFailed, billing.LegOutcomeSwallowed:
			if loser.BLegID != "" {
				t.Fatalf("second failure leg %q, want exactly one loser", leg.BLegID)
			}
			loser = leg
		default:
			t.Fatalf("unexpected leg outcome %q", leg.Outcome)
		}
	}
	if winner.BLegID == "" || loser.BLegID == "" {
		t.Fatalf("legs = %+v, want one winner and one loser", legs)
	}
	winnerPayable := false
	for _, observation := range winner.Observations {
		if len(observation.Charges) != 0 {
			winnerPayable = true
			break
		}
	}
	if !winnerPayable {
		t.Fatal("winner leg carries no provider-payable charge")
	}
	// The pre-output loser carries only local diagnostic boundary
	// observations (no Charges, no provider account/request/charge): nothing
	// operator-payable, so COGS must contain no loser posting while the leg
	// itself remains recorded in lifecycle.
	for _, observation := range loser.Observations {
		if len(observation.Charges) != 0 {
			t.Fatalf("loser observation %q carries charges = %+v, want none (non-payable)", observation.ID, observation.Charges)
		}
		if observation.Origin == metering.OriginProvider && observation.Subject.ProviderChargeID != "" {
			t.Fatalf("loser observation %q carries payable provider charge %q", observation.ID, observation.Subject.ProviderChargeID)
		}
	}
	if len(closure.ExpectedBLegIDs) != 2 {
		t.Fatalf("closure B-leg set = %v, want both attempts recorded", closure.ExpectedBLegIDs)
	}
	waitBillingHostLoopProviderCost(t, store, accountID)

	providerJournals := refinement4StockProviderJournals(t, store, accountID)
	if len(providerJournals) != 1 {
		t.Fatalf("provider COGS journals = %d, want exactly the winner posting", len(providerJournals))
	}
	refinement82RequireProviderJournal(t, providerJournals[0], winner.BLegID, refinement82OperatorNano)

	settlement := refinement82RequireCustomerSettlement(t, refinement4StockCustomerTransactions(t, store, accountID), refinement82CustomerNano)
	if settlement.AccountID != accountID {
		t.Fatalf("settlement account = %q, want %q", settlement.AccountID, accountID)
	}
	acct, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.BalanceNano != refinement82OpeningNano-refinement82CustomerNano {
		t.Fatalf("balance = %d, want %d", acct.BalanceNano, refinement82OpeningNano-refinement82CustomerNano)
	}
}
