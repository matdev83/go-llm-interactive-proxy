package runtimebundle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	_ "modernc.org/sqlite"
)

// refinement52VerificationBudget bounds the verification and restart-replay
// tail after the refinement5.2 sentinel has already proven durable
// convergence. It is deliberately rooted at context.Background rather than at
// the convergence sentinel: under race-heavy package load a healthy-but-slow
// relay drain can consume the whole sentinel before the verification reads
// run, and a read reusing that exhausted parent then fails with
// context.DeadlineExceeded even though the durable state is complete (the
// 60.43s Linux race failure at ListCallLegUsage after convergence). This only
// bounds the post-convergence verification and restart-replay lifecycle; the
// convergence deadline itself is unchanged, and missing convergence is still
// surfaced by the sentinel-scoped head/amount waits before any read here.
const refinement52VerificationBudget = 60 * time.Second

// refinement52VerificationContext returns a fresh bounded context for the
// post-convergence verification reads of the refinement5.2 integration tests.
func refinement52VerificationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), refinement52VerificationBudget)
}

// TestRefinement52RuntimeConcurrentDistinctLateRevisionsSerializeDurably starts
// from one production-closed B-leg, submits a provider finalizer and a
// B-leg-correlated statement concurrently, and verifies that the stock relay
// and valuation workers converge on one exact provider revision. The replay is
// performed after rebuilding both durable stores and the stock host.
func TestRefinement52RuntimeConcurrentDistinctLateRevisionsSerializeDurably(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	storeID := "refinement52-concurrent"
	billingPath := filepath.Join(t.TempDir(), "billing.sqlite")
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	store := openRefinement52ConcurrentBillingStore(t, billingPath, storeID)
	catalog, _, _, operator := seedBillingHostLoopCatalog(t)
	if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding: %v", err)
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
	accountID := "refinement52-concurrent-account"
	provisionBillingHostLoopAccount(t, store, accountID)
	injectRefinement52AuthenticUsageBackend(t, executor, accountID)
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("concurrent late economics")}}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = drainBillingHostLoopStream(t, ctx, stream)
	closure, _, _ := waitBillingHostLoopCall(t, store, accountID)
	legsBefore, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage before late evidence: %v", err)
	}
	if len(legsBefore) != 1 || legsBefore[0].AttemptSeq <= 0 {
		t.Fatalf("terminal B-leg records = %+v, want one positive-sequence leg", legsBefore)
	}
	legBefore := legsBefore[0]
	authority := refinement52RuntimeProviderAuthority(t, legBefore)
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	customerWorkIDsBefore := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueCustomer)
	if len(customerWorkIDsBefore) == 0 {
		t.Fatalf("stock CustomerInput produced no durable customer work")
	}
	waitRefinement52EconomicWorkAttempted(t, ctx, store, billing.EconomicQueueCustomer, len(customerWorkIDsBefore))
	waitRefinement4StockSettlement(t, ctx, store, accountID, closure.CallID)
	customerReportBefore := refinement4StockAccountReport(t, store, accountID)
	customerTransactionsBefore := refinement4StockCustomerTransactions(t, store, accountID)
	if len(customerTransactionsBefore) != 1 {
		t.Fatalf("customer settlement count before late evidence = %d, want 1", len(customerTransactionsBefore))
	}

	initialPage, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: legBefore.BLegID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list initial B-leg evidence: %v", err)
	}
	initialWorks, err := prod.BillingObservationEconomicWorkBuilder.BuildEconomicRevisionWork(ctx, initialPage.Observations)
	if err != nil {
		t.Fatalf("derive initial provider work: %v", err)
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
	initialHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, initialProvider.HeadKey, 1, initialProvider.Input.InputSetHash)
	refinement52RequireCompleteValuation(t, store, initialHead)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, initialProvider.HeadKey, billingHostLoopOperatorNano)
	legsLifecycleBefore := append([]billing.CallLegUsageRecord(nil), legsBefore...)

	identity := coremetering.LateEconomicIdentity{
		StoreID: storeID, BillingCallID: closure.CallID, ALegID: closure.ALegID, BLegID: legBefore.BLegID,
		AttemptID: legBefore.BLegID, AttemptSeq: uint64(legBefore.AttemptSeq), ProviderID: legBefore.ProviderID,
		ProviderAccountKey: authority.Subject.ProviderAccountKey, ProviderRequestID: authority.Subject.ProviderRequestID, ProviderChargeID: authority.Subject.ProviderChargeID,
	}
	finalizer := refinement52RuntimeObservation(storeID, closure.CallID, closure.ALegID, legBefore, authority, "concurrent-finalizer", 2, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
	statement := refinement52RuntimeObservation(storeID, closure.CallID, closure.ALegID, legBefore, authority, "concurrent-statement", 3, metering.OriginStatement, metering.AcquisitionStatementImporter, metering.AuthorityVerifiedStatement)
	statement.Subject = metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: storeID, TenantID: authority.Subject.TenantID, AccountID: authority.Subject.AccountID,
		ProviderAccountKey: authority.Subject.ProviderAccountKey, StatementID: "concurrent-statement-1", StatementLineID: "concurrent-line-1",
	}
	lateEvidence := []coremetering.LateEconomicEvidence{
		{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer},
		{Kind: coremetering.LateEconomicStatement, Identity: identity, Observation: statement},
	}
	start := make(chan struct{})
	errs := make(chan error, len(lateEvidence))
	var wg sync.WaitGroup
	for _, late := range lateEvidence {
		wg.Add(1)
		go func(late coremetering.LateEconomicEvidence) {
			defer wg.Done()
			<-start
			errs <- lateAppender.AppendLateEconomicEvidence(ctx, late)
		}(late)
	}
	close(start)
	wg.Wait()
	close(errs)
	for appendErr := range errs {
		if appendErr != nil {
			t.Fatalf("concurrent distinct late append: %v", appendErr)
		}
	}

	for _, late := range lateEvidence {
		if _, err := journal.GetObservation(ctx, late.Observation.ID, late.Observation.Revision); err != nil {
			t.Fatalf("read concurrent late observation %s/%d: %v", late.Observation.ID, late.Observation.Revision, err)
		}
	}
	expectedProvider := refinement52ExpectedProviderWork(t, ctx, prod.BillingObservationEconomicWorkBuilder, journal, storeID, legBefore.BLegID, &statement)
	if expectedProvider.EvidenceRevision != 3 {
		t.Fatalf("concurrent provider work = %+v, want final evidence revision 3", expectedProvider)
	}
	concurrentHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedProvider.HeadKey, 3, expectedProvider.Input.InputSetHash)
	refinement52RequireCompleteValuation(t, store, concurrentHead)
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	// The finalizer-only revision and the later statement revision share the
	// same converged current amount, so the valuation head and the
	// selected-cost provider head can converge at different times: the amount
	// is already correct while the provider head still names revision 2 (the
	// Linux race observed revision 2/hash d9aa... at this exact read). Wait for
	// the exact durable revision+hash+amount instead of the amount alone.
	providerCostHead := waitRefinement52StockProviderCostHeadExact(t, ctx, store, accountID, closure.CallID, expectedProvider.HeadKey, expectedProvider.EvidenceRevision, expectedProvider.InputSetHash, billingHostLoopOperatorNano+7)
	if providerCostHead.EvidenceRevision != 3 || providerCostHead.InputSetHash != expectedProvider.InputSetHash || providerCostHead.CurrentAmount.Nano != billingHostLoopOperatorNano+7 {
		t.Fatalf("concurrent provider cost head = %+v, want revision 3/hash %q/current %d", providerCostHead, expectedProvider.InputSetHash, billingHostLoopOperatorNano+7)
	}
	providerJournals := refinement4StockProviderJournals(t, store, accountID)
	if len(providerJournals) != 2 || len(providerJournals[0].Entries) == 0 || providerJournals[0].Entries[0].Amount.Nano != billingHostLoopOperatorNano ||
		len(providerJournals[1].Entries) == 0 || providerJournals[1].Entries[0].Amount.Nano != 7 || providerJournals[1].ReversalOf != providerJournals[0].ID || providerJournals[1].CorrectsTransactionID != providerJournals[0].ID {
		t.Fatalf("concurrent provider COGS journals = %+v, want initial +%d and one +7 delta", providerJournals, billingHostLoopOperatorNano)
	}
	legsAfter, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage after concurrent late evidence: %v", err)
	}
	if len(legsAfter) != len(legsLifecycleBefore) || legsAfter[0].Fingerprint != legsLifecycleBefore[0].Fingerprint || legsAfter[0].BLegID != legsLifecycleBefore[0].BLegID || legsAfter[0].AttemptSeq != legsLifecycleBefore[0].AttemptSeq {
		t.Fatalf("concurrent late evidence rewrote lifecycle leg: before=%+v after=%+v", legsLifecycleBefore, legsAfter)
	}
	customerWorkIDsAfter := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueCustomer)
	if strings.Join(customerWorkIDsAfter, "\x00") != strings.Join(customerWorkIDsBefore, "\x00") {
		t.Fatalf("concurrent provider-only evidence changed customer work: before=%v after=%v", customerWorkIDsBefore, customerWorkIDsAfter)
	}
	if got := refinement4StockCustomerTransactions(t, store, accountID); len(got) != len(customerTransactionsBefore) || got[0].ID != customerTransactionsBefore[0].ID || got[0].SemanticFingerprint != customerTransactionsBefore[0].SemanticFingerprint {
		t.Fatalf("concurrent provider-only evidence changed customer settlement: before=%+v after=%+v", customerTransactionsBefore, got)
	}
	if report := refinement4StockAccountReport(t, store, accountID); report.Account.BalanceNano != customerReportBefore.Account.BalanceNano {
		t.Fatalf("concurrent provider-only evidence changed customer balance: before=%+v after=%+v", customerReportBefore.Account, report.Account)
	}

	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("close first stock host: %v", err)
	}
	store2 := openRefinement52ConcurrentBillingStore(t, billingPath, storeID)
	prod2, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store: store2, TerminalUsageSink: store2, Catalog: catalog, Currency: "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 128000, true, nil },
		Strict:         true, ConservativeCeiling: &ceiling, PostTurnBatchSize: 1,
	})
	if err != nil {
		t.Fatalf("ComposeBilling after restart: %v", err)
	}
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
	for _, late := range lateEvidence {
		if err := lateAppender2.AppendLateEconomicEvidence(ctx, late); err != nil {
			t.Fatalf("exact late replay after restart (%s): %v", late.Kind, err)
		}
	}
	waitRefinement4StockOutboxDrained(t, ctx, store2, journal2)
	replayedHead := waitRefinement52StockHeadExact(t, ctx, store2, journal2, accountID, billing.EconomicQueueProvider, expectedProvider.HeadKey, 3, expectedProvider.Input.InputSetHash)
	if replayedHead.Fingerprint != concurrentHead.Fingerprint || replayedHead.HeadVersion != concurrentHead.HeadVersion || replayedHead.Fence != concurrentHead.Fence {
		t.Fatalf("restart replay changed provider valuation head: before=%+v after=%+v", concurrentHead, replayedHead)
	}
	replayedProviderCost, err := store2.GetProviderCostHead(ctx, accountID, closure.CallID, expectedProvider.HeadKey)
	if err != nil {
		t.Fatalf("read provider cost head after restart replay: %v", err)
	}
	if replayedProviderCost.InputSetHash != providerCostHead.InputSetHash || replayedProviderCost.EvidenceRevision != providerCostHead.EvidenceRevision || replayedProviderCost.CurrentAmount != providerCostHead.CurrentAmount || replayedProviderCost.HeadVersion != providerCostHead.HeadVersion || replayedProviderCost.Fence != providerCostHead.Fence {
		t.Fatalf("restart replay changed provider cost head: before=%+v after=%+v", providerCostHead, replayedProviderCost)
	}
	replayedJournals := refinement4StockProviderJournals(t, store2, accountID)
	if len(replayedJournals) != len(providerJournals) {
		t.Fatalf("restart replay changed provider COGS journal count: before=%d after=%d", len(providerJournals), len(replayedJournals))
	}
	for i := range providerJournals {
		if replayedJournals[i].ID != providerJournals[i].ID || replayedJournals[i].SemanticFingerprint != providerJournals[i].SemanticFingerprint {
			t.Fatalf("restart replay changed provider COGS journal %d: before=%+v after=%+v", i, providerJournals[i], replayedJournals[i])
		}
	}
	if report := refinement4StockAccountReport(t, store2, accountID); report.Account.BalanceNano != customerReportBefore.Account.BalanceNano {
		t.Fatalf("restart replay changed customer balance: before=%+v after=%+v", customerReportBefore.Account, report.Account)
	}
	if got := refinement4StockCustomerTransactions(t, store2, accountID); len(got) != len(customerTransactionsBefore) || got[0].ID != customerTransactionsBefore[0].ID || got[0].SemanticFingerprint != customerTransactionsBefore[0].SemanticFingerprint {
		t.Fatalf("restart replay changed customer settlement: before=%+v after=%+v", customerTransactionsBefore, got)
	}
	customerWorkIDsAfterRestart := refinement52EconomicWorkIDs(t, store2, billing.EconomicQueueCustomer)
	if strings.Join(customerWorkIDsAfterRestart, "\x00") != strings.Join(customerWorkIDsBefore, "\x00") {
		t.Fatalf("restart replay changed customer work: before=%v after=%v", customerWorkIDsBefore, customerWorkIDsAfterRestart)
	}
	legsAfterRestart, err := store2.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage after restart replay: %v", err)
	}
	if len(legsAfterRestart) != len(legsLifecycleBefore) || legsAfterRestart[0].Fingerprint != legsLifecycleBefore[0].Fingerprint || legsAfterRestart[0].BLegID != legsLifecycleBefore[0].BLegID || legsAfterRestart[0].AttemptSeq != legsLifecycleBefore[0].AttemptSeq {
		t.Fatalf("restart replay changed lifecycle legs: before=%+v after=%+v", legsLifecycleBefore, legsAfterRestart)
	}
}

// TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay
// forces the durable relay to process a verified statement before its lower
// revision provider finalizer. Both work items therefore carry revision 3,
// but the second item has a strict superset of the first item's evidence. The
// input hashes are deliberately ordered so the historical lexical tie-breaker
// would retain the incomplete head.
func TestRefinement52RuntimeSameRevisionSupersetConvergesAfterPartialRelay(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	storeID := "refinement52-same-revision-superset"
	billingPath := filepath.Join(t.TempDir(), "billing.sqlite")
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	store := openRefinement52ConcurrentBillingStore(t, billingPath, storeID)
	catalog, _, _, operator := seedBillingHostLoopCatalog(t)
	if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding: %v", err)
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
	accountID := "refinement52-same-revision-account"
	provisionBillingHostLoopAccount(t, store, accountID)
	injectRefinement52AuthenticUsageBackend(t, executor, accountID)
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("same revision superset")}}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = drainBillingHostLoopStream(t, ctx, stream)
	closure, _, _ := waitBillingHostLoopCall(t, store, accountID)
	legsBefore, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage before late evidence: %v", err)
	}
	if len(legsBefore) != 1 || legsBefore[0].AttemptSeq <= 0 {
		t.Fatalf("terminal B-leg records = %+v, want one positive-sequence leg", legsBefore)
	}
	legBefore := legsBefore[0]
	authority := refinement52RuntimeProviderAuthority(t, legBefore)
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	customerWorkIDsBefore := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueCustomer)
	if len(customerWorkIDsBefore) == 0 {
		t.Fatalf("stock CustomerInput produced no durable customer work")
	}
	waitRefinement52EconomicWorkAttempted(t, ctx, store, billing.EconomicQueueCustomer, len(customerWorkIDsBefore))
	waitRefinement4StockSettlement(t, ctx, store, accountID, closure.CallID)
	customerReportBefore := refinement4StockAccountReport(t, store, accountID)
	customerTransactionsBefore := refinement4StockCustomerTransactions(t, store, accountID)
	if len(customerTransactionsBefore) != 1 {
		t.Fatalf("customer settlement count before late evidence = %d, want 1", len(customerTransactionsBefore))
	}

	initialPage, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: legBefore.BLegID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list initial B-leg evidence: %v", err)
	}
	initialWorks, err := prod.BillingObservationEconomicWorkBuilder.BuildEconomicRevisionWork(ctx, initialPage.Observations)
	if err != nil {
		t.Fatalf("derive initial provider work: %v", err)
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
	initialHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, initialProvider.HeadKey, 1, initialProvider.Input.InputSetHash)
	refinement52RequireCompleteValuation(t, store, initialHead)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, initialProvider.HeadKey, billingHostLoopOperatorNano)
	providerJournalsBefore := refinement4StockProviderJournals(t, store, accountID)
	if len(providerJournalsBefore) != 1 {
		t.Fatalf("initial provider COGS journals = %+v, want one", providerJournalsBefore)
	}

	identity := coremetering.LateEconomicIdentity{
		StoreID: storeID, BillingCallID: closure.CallID, ALegID: closure.ALegID, BLegID: legBefore.BLegID,
		AttemptID: legBefore.BLegID, AttemptSeq: uint64(legBefore.AttemptSeq), ProviderID: legBefore.ProviderID,
		ProviderAccountKey: authority.Subject.ProviderAccountKey, ProviderRequestID: authority.Subject.ProviderRequestID, ProviderChargeID: authority.Subject.ProviderChargeID,
	}
	statement := refinement52RuntimeObservation(storeID, closure.CallID, closure.ALegID, legBefore, authority, "superset-statement", 3, metering.OriginStatement, metering.AcquisitionStatementImporter, metering.AuthorityVerifiedStatement)
	statement.Subject = metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: storeID, TenantID: authority.Subject.TenantID, AccountID: authority.Subject.AccountID,
		ProviderAccountKey: authority.Subject.ProviderAccountKey, StatementID: "superset-statement-1", StatementLineID: "superset-line-1",
	}
	partialEvidence := append([]metering.Observation(nil), initialPage.Observations...)
	partialEvidence = append(partialEvidence, statement.Clone())
	partialWorks, err := prod.BillingObservationEconomicWorkBuilder.BuildEconomicRevisionWork(ctx, partialEvidence)
	if err != nil {
		t.Fatalf("derive statement-only provider work: %v", err)
	}
	var partialProvider billing.EconomicRevisionWork
	for _, work := range partialWorks {
		if work.Queue == billing.EconomicQueueProvider {
			partialProvider = work
			break
		}
	}
	if partialProvider.HeadKey == "" || partialProvider.EvidenceRevision != 3 {
		t.Fatalf("statement-only provider work = %+v, want revision 3", partialProvider)
	}

	var finalizer metering.Observation
	var completeProvider billing.EconomicRevisionWork
	for candidateIndex := range 10000 {
		candidate := refinement52RuntimeObservation(storeID, closure.CallID, closure.ALegID, legBefore, authority,
			fmt.Sprintf("superset-finalizer-%05d", candidateIndex), 2, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
		completeEvidence := append([]metering.Observation(nil), initialPage.Observations...)
		completeEvidence = append(completeEvidence, candidate, statement.Clone())
		completeWorks, buildErr := prod.BillingObservationEconomicWorkBuilder.BuildEconomicRevisionWork(ctx, completeEvidence)
		if buildErr != nil {
			t.Fatalf("derive complete provider work candidate %d: %v", candidateIndex, buildErr)
		}
		var candidateProvider billing.EconomicRevisionWork
		for _, work := range completeWorks {
			if work.Queue == billing.EconomicQueueProvider {
				candidateProvider = work
				break
			}
		}
		if candidateProvider.HeadKey == partialProvider.HeadKey && candidateProvider.EvidenceRevision == 3 && candidateProvider.InputSetHash < partialProvider.InputSetHash {
			finalizer, completeProvider = candidate, candidateProvider
			break
		}
	}
	if finalizer.ID == "" {
		t.Fatalf("could not find deterministic strict-superset hash below partial hash %q", partialProvider.InputSetHash)
	}
	if len(completeProvider.Input.Observations) <= len(partialProvider.Input.Observations) {
		t.Fatalf("complete provider work did not add evidence: partial=%d complete=%d", len(partialProvider.Input.Observations), len(completeProvider.Input.Observations))
	}

	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicStatement, Identity: identity, Observation: statement}); err != nil {
		t.Fatalf("late statement before finalizer: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	partialHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, partialProvider.HeadKey, 3, partialProvider.Input.InputSetHash)
	refinement52RequireCompleteValuation(t, store, partialHead)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, partialProvider.HeadKey, billingHostLoopOperatorNano)

	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer}); err != nil {
		t.Fatalf("late finalizer after statement: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	completeHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, completeProvider.HeadKey, 3, completeProvider.Input.InputSetHash)
	refinement52RequireCompleteValuation(t, store, completeHead)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, completeProvider.HeadKey, billingHostLoopOperatorNano+7)
	// Durable convergence is now proven by the sentinel-scoped exact-head and
	// provider-amount waits above. Retire that parent before verification and
	// restart replay: under race-heavy package load a healthy but slow relay
	// drain can exhaust the whole sentinel after convergence, and a read that
	// reuses it then fails with context.DeadlineExceeded on a proven-converged
	// state (the 60.43s Linux race failure at ListCallLegUsage after
	// convergence). Cancelling here is deterministic and pins that the tail no
	// longer depends on the convergence parent; the tail runs under a fresh
	// bounded verification context and still fails on real non-convergence.
	cancel()
	verifyCtx, verifyCancel := refinement52VerificationContext(t)
	defer verifyCancel()
	providerCostHead, err := store.GetProviderCostHead(verifyCtx, accountID, closure.CallID, completeProvider.HeadKey)
	if err != nil {
		t.Fatalf("read converged provider cost head: %v", err)
	}
	if providerCostHead.EvidenceRevision != 3 || providerCostHead.InputSetHash != completeProvider.InputSetHash || providerCostHead.CurrentAmount.Nano != billingHostLoopOperatorNano+7 {
		t.Fatalf("converged provider cost head = %+v, want revision 3/hash %q/current %d", providerCostHead, completeProvider.InputSetHash, billingHostLoopOperatorNano+7)
	}
	providerJournals := refinement4StockProviderJournals(t, store, accountID)
	if len(providerJournals) != 2 || len(providerJournals[1].Entries) == 0 || providerJournals[1].Entries[0].Amount.Nano != 7 {
		t.Fatalf("converged provider COGS journals = %+v, want initial plus one +7 delta", providerJournals)
	}
	if report := refinement4StockAccountReport(t, store, accountID); report.Account.BalanceNano != customerReportBefore.Account.BalanceNano {
		t.Fatalf("provider-only convergence changed customer balance: before=%+v after=%+v", customerReportBefore.Account, report.Account)
	}
	if got := refinement4StockCustomerTransactions(t, store, accountID); len(got) != len(customerTransactionsBefore) || got[0].ID != customerTransactionsBefore[0].ID || got[0].SemanticFingerprint != customerTransactionsBefore[0].SemanticFingerprint {
		t.Fatalf("provider-only convergence changed customer settlement: before=%+v after=%+v", customerTransactionsBefore, got)
	}
	customerWorkIDsAfter := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueCustomer)
	if strings.Join(customerWorkIDsAfter, "\x00") != strings.Join(customerWorkIDsBefore, "\x00") {
		t.Fatalf("provider-only convergence changed customer work: before=%v after=%v", customerWorkIDsBefore, customerWorkIDsAfter)
	}
	legsAfter, err := store.ListCallLegUsage(verifyCtx, closure.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage after convergence: %v", err)
	}
	if len(legsAfter) != len(legsBefore) || legsAfter[0].Fingerprint != legsBefore[0].Fingerprint || legsAfter[0].BLegID != legsBefore[0].BLegID || legsAfter[0].AttemptSeq != legsBefore[0].AttemptSeq {
		t.Fatalf("same-revision convergence rewrote lifecycle leg: before=%+v after=%+v", legsBefore, legsAfter)
	}
	if len(providerJournalsBefore) != 1 {
		t.Fatalf("initial provider journal count changed unexpectedly: %+v", providerJournalsBefore)
	}
	t.Logf("same-revision convergence partial_revision=%d partial_hash=%s complete_revision=%d complete_hash=%s complete_hash_lower=%t provider_cost_nano=%d provider_cogs_journals=%d", partialHead.EvidenceRevision, partialHead.InputSetHash, completeHead.EvidenceRevision, completeHead.InputSetHash, completeProvider.InputSetHash < partialProvider.InputSetHash, providerCostHead.CurrentAmount.Nano, len(providerJournals))

	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("close stock host after convergence: %v", err)
	}
	store2 := openRefinement52ConcurrentBillingStore(t, billingPath, storeID)
	prod2, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store: store2, TerminalUsageSink: store2, Catalog: catalog, Currency: "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 128000, true, nil },
		Strict:         true, ConservativeCeiling: &ceiling, PostTurnBatchSize: 1,
	})
	if err != nil {
		t.Fatalf("ComposeBilling after convergence restart: %v", err)
	}
	host2, err := runtimebundle.BuildHost(verifyCtx, runtimebundle.BuildHostInput{
		ConfigPath: configPath, Mandatory: lipsdk.StandardDistributionRequirements(), LogWriter: io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP, Production: prod2,
	})
	if err != nil {
		t.Fatalf("BuildHost after convergence restart: %v", err)
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
		t.Fatalf("NewLateEconomicAppender after convergence restart: %v", err)
	}
	for _, late := range []coremetering.LateEconomicEvidence{
		{Kind: coremetering.LateEconomicStatement, Identity: identity, Observation: statement},
		{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer},
	} {
		if err := lateAppender2.AppendLateEconomicEvidence(verifyCtx, late); err != nil {
			t.Fatalf("exact late replay after convergence restart (%s): %v", late.Kind, err)
		}
	}
	waitRefinement4StockOutboxDrained(t, verifyCtx, store2, journal2)
	replayedHead := waitRefinement52StockHeadExact(t, verifyCtx, store2, journal2, accountID, billing.EconomicQueueProvider, completeProvider.HeadKey, 3, completeProvider.Input.InputSetHash)
	if replayedHead.Fingerprint != completeHead.Fingerprint || replayedHead.HeadVersion != completeHead.HeadVersion || replayedHead.Fence != completeHead.Fence {
		t.Fatalf("restart replay changed same-revision valuation head: before=%+v after=%+v", completeHead, replayedHead)
	}
	replayedProviderCost, err := store2.GetProviderCostHead(verifyCtx, accountID, closure.CallID, completeProvider.HeadKey)
	if err != nil {
		t.Fatalf("read provider cost head after convergence restart: %v", err)
	}
	if replayedProviderCost.InputSetHash != providerCostHead.InputSetHash || replayedProviderCost.EvidenceRevision != providerCostHead.EvidenceRevision || replayedProviderCost.CurrentAmount != providerCostHead.CurrentAmount || replayedProviderCost.HeadVersion != providerCostHead.HeadVersion || replayedProviderCost.Fence != providerCostHead.Fence {
		t.Fatalf("restart replay changed same-revision provider cost head: before=%+v after=%+v", providerCostHead, replayedProviderCost)
	}
	replayedProviderJournals := refinement4StockProviderJournals(t, store2, accountID)
	if len(replayedProviderJournals) != len(providerJournals) {
		t.Fatalf("restart replay changed same-revision provider COGS journal count: before=%d after=%d", len(providerJournals), len(replayedProviderJournals))
	}
	for i := range providerJournals {
		if replayedProviderJournals[i].ID != providerJournals[i].ID || replayedProviderJournals[i].SemanticFingerprint != providerJournals[i].SemanticFingerprint {
			t.Fatalf("restart replay changed same-revision provider journal %d: before=%+v after=%+v", i, providerJournals[i], replayedProviderJournals[i])
		}
	}
	if report := refinement4StockAccountReport(t, store2, accountID); report.Account.BalanceNano != customerReportBefore.Account.BalanceNano {
		t.Fatalf("restart replay changed same-revision customer balance: before=%+v after=%+v", customerReportBefore.Account, report.Account)
	}
	if got := refinement4StockCustomerTransactions(t, store2, accountID); len(got) != len(customerTransactionsBefore) || got[0].ID != customerTransactionsBefore[0].ID || got[0].SemanticFingerprint != customerTransactionsBefore[0].SemanticFingerprint {
		t.Fatalf("restart replay changed same-revision customer settlement: before=%+v after=%+v", customerTransactionsBefore, got)
	}
	if got := refinement52EconomicWorkIDs(t, store2, billing.EconomicQueueCustomer); strings.Join(got, "\x00") != strings.Join(customerWorkIDsBefore, "\x00") {
		t.Fatalf("restart replay changed same-revision customer work: before=%v after=%v", customerWorkIDsBefore, got)
	}
	legsAfterRestart, err := store2.ListCallLegUsage(verifyCtx, closure.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage after convergence restart: %v", err)
	}
	if len(legsAfterRestart) != len(legsBefore) || legsAfterRestart[0].Fingerprint != legsBefore[0].Fingerprint || legsAfterRestart[0].BLegID != legsBefore[0].BLegID || legsAfterRestart[0].AttemptSeq != legsBefore[0].AttemptSeq {
		t.Fatalf("restart replay changed same-revision lifecycle leg: before=%+v after=%+v", legsBefore, legsAfterRestart)
	}
}

// TestRefinement52VerificationReadSurvivesExhaustedConvergenceParent is the
// deterministic regression for the Linux race failure at
// "ListCallLegUsage after convergence: context deadline exceeded". It
// reproduces the exact condition: the convergence sentinel parent is already
// exhausted when the post-convergence verification read runs, while the
// durable leg is present. The raw parent-scoped read fails with
// context.DeadlineExceeded; the fresh bounded verification context reads the
// same durable row, proving the failure was the expired parent and not missing
// convergence.
func TestRefinement52VerificationReadSurvivesExhaustedConvergenceParent(t *testing.T) {
	t.Parallel()
	store := openRefinement52ConcurrentBillingStore(t, filepath.Join(t.TempDir(), "billing.sqlite"), "refinement52-verification-read")
	callID := billing.BillingCallID("bc_0000000000000000000000000000007a")
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: store.StoreID(), TenantID: "refinement52-verification-tenant",
		AccountID: "refinement52-verification-account", ALegID: "refinement52-verification-a-leg",
		BillingCallID: callID.String(), BLegID: "refinement52-verification-b-leg",
		AttemptID: "refinement52-verification-attempt", AttemptSeq: 1, ProviderAccountKey: "refinement52-verification-provider",
	}
	observation := refinement4StockObservation(subject, "refinement52-verification-observation", 1, 7, metering.SemanticsDelta, nil)
	sealed, err := refinement4StockCallLeg(observation).Seal()
	if err != nil {
		t.Fatalf("seal durable verification leg: %v", err)
	}
	if err := store.AppendLeg(context.Background(), sealed); err != nil {
		t.Fatalf("append durable verification leg: %v", err)
	}

	exhausted, exhaustedCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer exhaustedCancel()
	if _, err := store.ListCallLegUsage(exhausted, callID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exhausted convergence parent must fail the raw verification read, got %v", err)
	}

	verifyCtx, verifyCancel := refinement52VerificationContext(t)
	defer verifyCancel()
	legs, err := store.ListCallLegUsage(verifyCtx, callID)
	if err != nil {
		t.Fatalf("fresh bounded verification context must read the durable leg: %v", err)
	}
	if len(legs) != 1 || legs[0].BLegID != sealed.BLegID || legs[0].Fingerprint != sealed.Fingerprint {
		t.Fatalf("verification read = %+v, want the durable leg %+v", legs, sealed)
	}
}

// TestRefinement52RuntimeLateEconomicAppendAfterTerminalClosure executes a
// real production host/B-leg to terminal closure, then uses the stock atomic
// observation sink after the attempt is gone. The appender's only billing read
// is the original immutable leg key; no executor/attempt allocation path is
// entered by late evidence.
func TestRefinement52RuntimeLateEconomicAppendAfterTerminalClosure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := openBillingHostLoopStore(t)
	catalog, _, _, operator := seedBillingHostLoopCatalog(t)
	if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding: %v", err)
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

	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	configPath := writeRefinement52MeteredConfig(t, journalPath)
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath: configPath, Mandatory: lipsdk.StandardDistributionRequirements(), LogWriter: io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP, Production: prod,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)
	executor := hostActiveExecutor(t, host)
	if _, ok := executor.MeteringObservationSink.(metering.AtomicObservationSink); !ok {
		t.Fatalf("stock late-evidence sink = %T, want atomic sink", executor.MeteringObservationSink)
	}

	journal := openRefinement52RuntimeJournal(t, journalPath, store.StoreID())
	lateAppender, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: store.StoreID(), Legs: store, Sink: executor.MeteringObservationSink, Resolver: journal,
	})
	if err != nil {
		t.Fatalf("NewLateEconomicAppender: %v", err)
	}

	accountID := fmt.Sprintf("refinement52-runtime-%d", time.Now().UnixNano())
	provisionBillingHostLoopAccount(t, store, accountID)
	injectRefinement52AuthenticUsageBackend(t, executor, accountID)
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("late economics")}}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = drainBillingHostLoopStream(t, ctx, stream)
	closure, _, _ := waitBillingHostLoopCall(t, store, accountID)
	legsBefore, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage before late evidence: %v", err)
	}
	if len(legsBefore) != 1 || legsBefore[0].AttemptSeq <= 0 {
		t.Fatalf("terminal B-leg records = %+v, want one positive-sequence leg", legsBefore)
	}
	legBefore := legsBefore[0]
	authority := refinement52RuntimeProviderAuthority(t, legBefore)
	attemptsBefore, err := executor.Store.LoadAttempts(ctx, closure.ALegID)
	if err != nil {
		t.Fatalf("LoadAttempts before late evidence: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	customerWorkIDsBeforeLate := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueCustomer)
	if len(customerWorkIDsBeforeLate) == 0 {
		t.Fatalf("stock CustomerInput produced no durable customer work before late provider evidence")
	}
	waitRefinement52EconomicWorkAttempted(t, ctx, store, billing.EconomicQueueCustomer, len(customerWorkIDsBeforeLate))
	// Close the timing window between the terminal provider observation and
	// late evidence. The initial provider claim is a real production result;
	// waiting for its revision and journal makes the subsequent finalizer and
	// correction deltas deterministic instead of depending on worker scheduling.
	initialEvidencePage, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: store.StoreID(), SubjectKind: metering.SubjectBLeg, SubjectID: legBefore.BLegID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list initial B-leg economic evidence: %v", err)
	}
	initialWorks, err := prod.BillingObservationEconomicWorkBuilder.BuildEconomicRevisionWork(ctx, initialEvidencePage.Observations)
	if err != nil {
		t.Fatalf("derive initial economic identities: %v", err)
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
	initialHead := waitRefinement4StockHeadRevision(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, initialProvider.HeadKey, 1)
	refinement52RequireCompleteValuation(t, store, initialHead)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, initialProvider.HeadKey, billingHostLoopOperatorNano)
	initialProviderJournals := refinement4StockProviderJournals(t, store, accountID)
	if len(initialProviderJournals) != 1 || len(initialProviderJournals[0].Entries) == 0 || initialProviderJournals[0].Entries[0].Amount.Nano != billingHostLoopOperatorNano {
		t.Fatalf("initial provider COGS journals = %+v, want one +%d journal", initialProviderJournals, billingHostLoopOperatorNano)
	}

	identity := coremetering.LateEconomicIdentity{
		StoreID: store.StoreID(), BillingCallID: closure.CallID, ALegID: closure.ALegID, BLegID: legBefore.BLegID,
		AttemptID: legBefore.BLegID, AttemptSeq: uint64(legBefore.AttemptSeq), ProviderID: legBefore.ProviderID,
		ProviderAccountKey: authority.Subject.ProviderAccountKey, ProviderRequestID: authority.Subject.ProviderRequestID, ProviderChargeID: authority.Subject.ProviderChargeID,
	}
	finalizer := refinement52RuntimeObservation(store.StoreID(), closure.CallID, closure.ALegID, legBefore, authority, "finalizer", 2, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer}); err != nil {
		t.Fatalf("late provider finalizer: %v", err)
	}
	if _, err := journal.GetObservation(ctx, finalizer.ID, finalizer.Revision); err != nil {
		t.Fatalf("read late finalizer: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	expectedFinalizer := refinement52ExpectedProviderWork(t, ctx, prod.BillingObservationEconomicWorkBuilder, journal, store.StoreID(), legBefore.BLegID, nil)
	finalizerHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedFinalizer.HeadKey, expectedFinalizer.EvidenceRevision, expectedFinalizer.Input.InputSetHash)
	refinement52RequireCompleteValuation(t, store, finalizerHead)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, expectedFinalizer.HeadKey, billingHostLoopOperatorNano+7)

	statement := refinement52RuntimeObservation(store.StoreID(), closure.CallID, closure.ALegID, legBefore, authority, "statement", 3, metering.OriginStatement, metering.AcquisitionStatementImporter, metering.AuthorityVerifiedStatement)
	statement.Subject = metering.SubjectRef{Kind: metering.SubjectStatementLine, StoreID: store.StoreID(), TenantID: authority.Subject.TenantID, AccountID: authority.Subject.AccountID, ProviderAccountKey: authority.Subject.ProviderAccountKey, StatementID: "statement-1", StatementLineID: "line-1"}
	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicStatement, Identity: identity, Observation: statement}); err != nil {
		t.Fatalf("late provider statement: %v", err)
	}
	if _, err := journal.GetObservation(ctx, statement.ID, statement.Revision); err != nil {
		t.Fatalf("read late statement: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	expectedStatement := refinement52ExpectedProviderWork(t, ctx, prod.BillingObservationEconomicWorkBuilder, journal, store.StoreID(), legBefore.BLegID, &statement)
	if expectedStatement.EvidenceRevision != 3 {
		t.Fatalf("statement provider work = %+v, want revision 3", expectedStatement)
	}
	statementInProviderWork := false
	for _, observation := range expectedStatement.Input.Observations {
		if observation.ID == statement.ID {
			statementInProviderWork = true
			break
		}
	}
	if !statementInProviderWork {
		t.Fatalf("provider statement work omitted verified B-leg-correlated statement %q: observations=%+v", statement.ID, expectedStatement.Input.Observations)
	}
	statementHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedStatement.HeadKey, expectedStatement.EvidenceRevision, expectedStatement.Input.InputSetHash)
	refinement52RequireCompleteValuation(t, store, statementHead)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, expectedStatement.HeadKey, billingHostLoopOperatorNano+7)

	correction := refinement52RuntimeObservation(store.StoreID(), closure.CallID, closure.ALegID, legBefore, authority, "correction", 4, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
	correction.SourceEventKey = finalizer.SourceEventKey
	correction.Subject = finalizer.Subject
	correction.Correlation = finalizer.Correlation
	correction.Semantics = metering.SemanticsCorrection
	correction.Charges[0].ChargeItemID = finalizer.Charges[0].ChargeItemID
	correctionAmount := metering.DecimalFromNanoUnits(9)
	correction.Charges[0].Amount = &correctionAmount
	priorRef, err := finalizer.Ref(store.StoreID())
	if err != nil {
		t.Fatalf("finalizer ref: %v", err)
	}
	correction.Supersedes = []metering.ObservationRef{priorRef}
	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicCorrection, Identity: identity, Observation: correction}); err != nil {
		t.Fatalf("late provider correction: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)

	page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{StoreID: store.StoreID(), StreamID: "refinement52-runtime-stream", Limit: 10})
	if err != nil {
		t.Fatalf("list late observations: %v", err)
	}
	if len(page.Observations) != 3 {
		t.Fatalf("late observation count = %d, want 3", len(page.Observations))
	}
	if len(page.Observations[2].Supersedes) != 1 || !page.Observations[2].Supersedes[0].Equal(priorRef) {
		t.Fatalf("late correction supersedes = %+v, want %+v", page.Observations[2].Supersedes, priorRef)
	}
	legsAfter, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage after late evidence: %v", err)
	}
	if len(legsAfter) != 1 || legsAfter[0].Fingerprint != legBefore.Fingerprint || legsAfter[0].AttemptSeq != legBefore.AttemptSeq {
		t.Fatalf("late evidence rewrote lifecycle leg: before=%+v after=%+v", legBefore, legsAfter)
	}
	attemptsAfter, err := executor.Store.LoadAttempts(ctx, closure.ALegID)
	if err != nil {
		t.Fatalf("LoadAttempts after late evidence: %v", err)
	}
	if len(attemptsAfter) != len(attemptsBefore) {
		t.Fatalf("late evidence allocated/reopened attempt: before=%d after=%d", len(attemptsBefore), len(attemptsAfter))
	}

	// Derive expected identities from stock composition for read-side
	// assertions only. The durable relay claimed the observation outbox and
	// appended the work consumed below; this test never appends a work marker.
	expectedProvider := refinement52ExpectedProviderWork(t, ctx, prod.BillingObservationEconomicWorkBuilder, journal, store.StoreID(), legBefore.BLegID, &statement)
	if expectedProvider.EvidenceRevision != 4 || expectedProvider.HeadKey == "" {
		t.Fatalf("expected provider late work = %+v, want revision 4", expectedProvider)
	}
	statementInProviderWork = false
	for _, observation := range expectedProvider.Input.Observations {
		if observation.ID == statement.ID {
			statementInProviderWork = true
			break
		}
	}
	if !statementInProviderWork {
		t.Fatalf("provider late work omitted verified B-leg-correlated statement %q: observations=%+v", statement.ID, expectedProvider.Input.Observations)
	}
	providerHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedProvider.HeadKey, 4, expectedProvider.Input.InputSetHash)
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure.CallID, expectedProvider.HeadKey, 134)
	refinement52RequireCompleteValuation(t, store, providerHead)
	if providerHead.InputSetHash != expectedProvider.InputSetHash {
		t.Fatalf("provider valuation input hash = %q, want %q", providerHead.InputSetHash, expectedProvider.InputSetHash)
	}
	providerCostHead, err := store.GetProviderCostHead(ctx, accountID, closure.CallID, expectedProvider.HeadKey)
	if err != nil {
		t.Fatalf("read provider cost head after late evidence: %v", err)
	}
	if providerCostHead.EvidenceRevision != 4 || providerCostHead.InputSetHash != expectedProvider.InputSetHash || providerCostHead.CurrentAmount.Nano != 134 {
		t.Fatalf("provider cost head = %+v, want revision 4/hash %q/current 134", providerCostHead, expectedProvider.InputSetHash)
	}
	providerJournals := refinement4StockProviderJournals(t, store, accountID)
	if len(providerJournals) != 3 || providerJournals[0].ID == "" || providerJournals[0].ReversalOf != "" || providerJournals[0].CorrectsTransactionID != "" ||
		providerJournals[1].ReversalOf != providerJournals[0].ID || providerJournals[1].CorrectsTransactionID != providerJournals[0].ID ||
		providerJournals[2].ReversalOf != providerJournals[1].ID || providerJournals[2].CorrectsTransactionID != providerJournals[1].ID {
		t.Fatalf("provider COGS journals = %+v, want initial plus chained finalizer/correction journals", providerJournals)
	}
	if len(providerJournals[0].Entries) == 0 || providerJournals[0].Entries[0].Amount.Nano != billingHostLoopOperatorNano ||
		len(providerJournals[1].Entries) == 0 || providerJournals[1].Entries[0].Amount.Nano != 7 ||
		len(providerJournals[2].Entries) == 0 || providerJournals[2].Entries[0].Amount.Nano != 2 {
		t.Fatalf("provider COGS journal deltas = %+v, want +%d, +7, +2", providerJournals, billingHostLoopOperatorNano)
	}
	conflictingFinalizer := finalizer.Clone()
	conflictingAmount := metering.DecimalFromNanoUnits(8)
	conflictingFinalizer.Charges[0].Amount = &conflictingAmount
	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
		Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: conflictingFinalizer,
	}); err == nil {
		t.Fatal("same late observation identity with changed payload must fail closed")
	}
	pendingAfterConflict, err := journal.ListPendingObservationOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("list observation outbox after conflicting replay: %v", err)
	}
	if len(pendingAfterConflict) != 0 {
		t.Fatalf("conflicting late replay left observation outbox work: %+v", pendingAfterConflict)
	}
	// Replay the exact durable identities through the production appender after
	// settlement. Observation identity fences must make this a no-op all the
	// way through relay, valuation, provider posting, and customer settlement.
	for _, late := range []coremetering.LateEconomicEvidence{
		{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer},
		{Kind: coremetering.LateEconomicStatement, Identity: identity, Observation: statement},
		{Kind: coremetering.LateEconomicCorrection, Identity: identity, Observation: correction},
	} {
		if err := lateAppender.AppendLateEconomicEvidence(ctx, late); err != nil {
			t.Fatalf("exact late evidence replay (%s): %v", late.Kind, err)
		}
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	replayedHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedProvider.HeadKey, 4, expectedProvider.Input.InputSetHash)
	if replayedHead.Fingerprint != providerHead.Fingerprint || replayedHead.HeadVersion != providerHead.HeadVersion || replayedHead.Fence != providerHead.Fence {
		t.Fatalf("exact replay advanced provider valuation head: before=%+v after=%+v", providerHead, replayedHead)
	}
	replayedProviderJournals := refinement4StockProviderJournals(t, store, accountID)
	if len(replayedProviderJournals) != len(providerJournals) {
		t.Fatalf("exact replay changed provider COGS journal count: before=%d after=%d", len(providerJournals), len(replayedProviderJournals))
	}
	for i := range providerJournals {
		if replayedProviderJournals[i].ID != providerJournals[i].ID || replayedProviderJournals[i].SemanticFingerprint != providerJournals[i].SemanticFingerprint {
			t.Fatalf("exact replay changed provider COGS journal %d: before=%+v after=%+v", i, providerJournals[i], replayedProviderJournals[i])
		}
	}
	finalReport := refinement4StockAccountReport(t, store, accountID)
	if finalReport.Account.BalanceNano != billingHostLoopOpeningNano-billingHostLoopCustomerNano || len(refinement4StockCustomerTransactions(t, store, accountID)) != 1 {
		t.Fatalf("late provider-only evidence changed customer settlement: report=%+v customer=%+v", finalReport.Account, refinement4StockCustomerTransactions(t, store, accountID))
	}
	customerWorkIDsAfterLate := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueCustomer)
	if strings.Join(customerWorkIDsAfterLate, "\x00") != strings.Join(customerWorkIDsBeforeLate, "\x00") {
		t.Fatalf("late provider-only evidence created or replaced customer work: before=%v after=%v", customerWorkIDsBeforeLate, customerWorkIDsAfterLate)
	}
}

// injectRefinement52AuthenticUsageBackend emits provider V2 evidence through
// the same buffer/binding seam used by provider adapters. The runtime supplies
// B-leg identity immediately after Open; this helper only supplies the
// provider-owned account/request/charge authority and the customer account
// projection needed by the economic worker.
func injectRefinement52AuthenticUsageBackend(t *testing.T, executor *coreruntime.Executor, accountID string) {
	t.Helper()
	be := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return newRefinement52AuthenticUsageStream(accountID), nil
		},
	}
	if executor.Backends == nil {
		executor.Backends = map[string]execbackend.Backend{}
	}
	executor.Backends[billingHostLoopBackendID] = be
	capFn := func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
		return execbackend.EffectiveCaps(ctx, be, call, cand)
	}
	switch capMap := executor.CapsResolver.(type) {
	case capabilities.MapResolver:
		capMap[billingHostLoopBackendID] = capFn
	case nil:
		executor.CapsResolver = capabilities.MapResolver{billingHostLoopBackendID: capFn}
	default:
		t.Fatalf("CapsResolver type %T cannot accept refinement 5.2 backend caps", executor.CapsResolver)
	}
}

type refinement52AuthenticUsageStream struct {
	*lipapi.FixedEventStream
	*coremetering.ProviderEvidenceBuffer
	accountID string
}

func newRefinement52AuthenticUsageStream(accountID string) *refinement52AuthenticUsageStream {
	stream := &refinement52AuthenticUsageStream{
		FixedEventStream:       lipapi.NewFixedEventStream(billingHostLoopUsageEvents()),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
		accountID:              accountID,
	}
	stream.AddUsageEvent(lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		CostNanoUnits: billingHostLoopOperatorNano,
		Currency:      "USD",
		CostPresent:   true,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:              lipapi.UsagePlaneProviderBillable,
			Source:             lipapi.UsageSourceProviderReported,
			Authority:          lipapi.UsageAuthorityAuthoritative,
			DedupeKey:          "refinement52-runtime:provider-cost",
			ProviderAccountKey: "provider-account",
			ProviderRequestID:  "provider-request",
			ProviderChargeID:   "provider-charge",
		},
	}, "refinement52-runtime.provider.v2")
	return stream
}

// DrainEconomicObservations adds the trusted customer account projection after
// ProviderEvidenceBuffer has validated provider authority and runtime lineage.
// It never changes provider account/request/charge IDs and revalidates the
// resulting observation before handing it to the production runtime.
func (s *refinement52AuthenticUsageStream) DrainEconomicObservations() []metering.Observation {
	if s == nil || s.ProviderEvidenceBuffer == nil {
		return nil
	}
	observations := s.ProviderEvidenceBuffer.DrainEconomicObservations()
	for i := range observations {
		observations[i].Subject.AccountID = s.accountID
		for j := range observations[i].Charges {
			// UsageAccountingMetadata has no payer field; this provider-billable
			// fixture supplies the adapter's explicit operator ownership at the
			// production observation boundary.
			observations[i].Charges[j].Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator}
		}
		if err := observations[i].Validate(); err != nil {
			return nil
		}
	}
	return observations
}

func writeRefinement52MeteredConfig(t *testing.T, journalPath string) string {
	t.Helper()
	basePath := writeBillingHostLoopConfig(t)
	base, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	configText := strings.Replace(string(base), "continuity:\n  in_memory: true\n  store: memory\n", "continuity:\n  in_memory: true\n  store: memory\nmetering:\n  enabled: true\n  journal:\n    store: sqlite\n    sqlite_path: \""+filepath.ToSlash(journalPath)+"\"\n", 1)
	path := filepath.Join(t.TempDir(), "refinement52-runtime.yaml")
	if err := os.WriteFile(path, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openRefinement52RuntimeJournal(t *testing.T, path, storeID string) *journalstore.DurableStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	journal, err := journalstore.OpenStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = journal.Close()
		_ = sqlDB.Close()
	})
	return journal
}

func refinement52RuntimeObservation(storeID string, callID billing.BillingCallID, aLegID string, leg billing.CallLegUsageRecord, providerAuthority metering.Observation, id string, revision uint64, origin, acquisition, authority string) metering.Observation {
	now := time.Unix(1_700_003_000+int64(revision), 0).UTC()
	component := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:tokens", Unit: metering.UnitToken, SchemaID: "refinement52-runtime:v1"}
	value := metering.Decimal{Coefficient: "4", Scale: 0}
	amount := metering.DecimalFromNanoUnits(7)
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: providerAuthority.Subject.TenantID, AccountID: providerAuthority.Subject.AccountID, ALegID: aLegID, RequestID: providerAuthority.Subject.RequestID, BillingCallID: callID.String(), CallID: callID.String(), BLegID: leg.BLegID, AttemptID: leg.BLegID, AttemptSeq: uint64(leg.AttemptSeq), ProviderAccountKey: providerAuthority.Subject.ProviderAccountKey, ProviderRequestID: providerAuthority.Subject.ProviderRequestID, ProviderChargeID: providerAuthority.Subject.ProviderChargeID}
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "refinement52-runtime-" + id, SourceEventKey: "refinement52-runtime-source-" + id, Revision: revision,
		StreamID: "refinement52-runtime-stream", Sequence: revision, Origin: origin, Acquisition: acquisition, Authority: authority,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject, Correlation: metering.CorrelationV2{StoreID: storeID, TenantID: providerAuthority.Subject.TenantID, RequestID: providerAuthority.Subject.RequestID, CallID: callID.String(), BillingCallID: callID.String(), ALegID: aLegID, BLegID: leg.BLegID, AttemptID: leg.BLegID, AttemptSeq: uint64(leg.AttemptSeq), ProviderAccountKey: providerAuthority.Subject.ProviderAccountKey, ProviderRequestID: providerAuthority.Subject.ProviderRequestID, ProviderChargeID: providerAuthority.Subject.ProviderChargeID},
		Semantics: metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now, MappingRef: "refinement52-runtime:v1",
		Charges: []metering.ReportedCharge{{ChargeItemID: "refinement52-charge-" + id, Amount: &amount, Currency: "USD", Kind: metering.ChargeKindAggregate, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator}}},
	}
	if acquisition != metering.AcquisitionProviderFinalizer {
		observation.Measures = []metering.Measure{{Key: component, Value: &value, Quality: metering.QualityObserved}}
	}
	return observation
}

func refinement52RuntimeProviderAuthority(t *testing.T, leg billing.CallLegUsageRecord) metering.Observation {
	t.Helper()
	for _, observation := range leg.Observations {
		if observation.Origin == metering.OriginProvider && observation.Authority == metering.AuthorityObservedClaim && observation.Subject.ProviderAccountKey != "" && observation.Subject.ProviderRequestID != "" && observation.Subject.ProviderChargeID != "" {
			return observation
		}
	}
	t.Fatalf("closed leg has no provider-origin authority observation: %+v", leg.Observations)
	return metering.Observation{}
}

func refinement52EconomicWorkIDs(t *testing.T, store *billingstore.DurableStore, queue billing.EconomicQueue) []string {
	t.Helper()
	var rows []struct {
		WorkID string `bun:"work_id"`
	}
	if err := store.DB().NewRaw(`SELECT work_id FROM billing_economic_work WHERE store_id = ? AND kind = ? ORDER BY work_id`, store.StoreID(), "economic_revision:"+string(queue)).Scan(context.Background(), &rows); err != nil {
		t.Fatalf("list %s economic work: %v", queue, err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.WorkID)
	}
	return ids
}

//nolint:revive // test helper keeps t first per Go testing convention
func waitRefinement52EconomicWorkAttempted(t *testing.T, parent context.Context, store *billingstore.DurableStore, queue billing.EconomicQueue, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var attempted int64
	var err error
	for {
		err = store.DB().NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND queue = ? AND attempt_count > 0`, store.StoreID(), queue.String()).Scan(ctx, &attempted)
		if err == nil && attempted >= int64(want) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s economic worker attempts >= %d: got=%d err=%v", queue, want, attempted, err)
		case <-ticker.C:
		}
	}
}

//nolint:revive // test helper keeps t first per Go testing convention
func refinement52ExpectedProviderWork(t *testing.T, ctx context.Context, builder billing.ObservationEconomicWorkBuilder, journal *journalstore.DurableStore, storeID, blegID string, linkedStatement *metering.Observation) billing.EconomicRevisionWork {
	t.Helper()
	evidencePage, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: blegID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list B-leg economic evidence: %v", err)
	}
	economicEvidence := append([]metering.Observation(nil), evidencePage.Observations...)
	if linkedStatement != nil {
		economicEvidence = append(economicEvidence, linkedStatement.Clone())
	}
	works, err := builder.BuildEconomicRevisionWork(ctx, economicEvidence)
	if err != nil {
		t.Fatalf("derive provider economic identity: %v", err)
	}
	for _, work := range works {
		if work.Queue == billing.EconomicQueueProvider {
			return work
		}
	}
	t.Fatalf("provider economic work missing for B-leg %q: observations=%+v", blegID, economicEvidence)
	return billing.EconomicRevisionWork{}
}

//nolint:revive // test helper keeps t first per Go testing convention
func waitRefinement52StockHeadExact(t *testing.T, parent context.Context, store *billingstore.DurableStore, journal *journalstore.DurableStore, accountID string, queue billing.EconomicQueue, headKey string, revision uint64, inputSetHash string) billing.EconomicValuationHead {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var head billing.EconomicValuationHead
	var err error
	for {
		head, err = store.GetEconomicValuationHead(ctx, queue, headKey)
		if err == nil && head.EvidenceRevision == revision && head.InputSetHash == inputSetHash {
			return head
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for exact %s economic head %q revision %d/hash %q: head=%+v: %s", queue, headKey, revision, inputSetHash, head, refinement4StockDiagnostics(t, store, journal, accountID, err))
		case <-ticker.C:
		}
	}
}

// waitRefinement52StockProviderCostHeadExact polls the durable selected-cost
// provider head until it names the exact expected evidence revision AND
// input-set hash at the expected current amount. The valuation head proven by
// waitRefinement52StockHeadExact and this selected-cost head advance in
// separate transactions: the finalizer-only revision and the later statement
// revision can share one converged current amount, so waiting on the amount
// alone can return while the head still names the earlier revision (the Linux
// race at TestRefinement52RuntimeConcurrentDistinctLateRevisionsSerializeDurably
// observed revision 2 while expecting revision 3, both at the same amount). An
// exact wait on revision+hash+amount reads the converged head without weakening
// the revision assertion and fails diagnostically if the head never reaches
// that exact durable state.
//
//nolint:revive // test helper keeps t first per Go testing convention
func waitRefinement52StockProviderCostHeadExact(t *testing.T, parent context.Context, store *billingstore.DurableStore, accountID string, callID billing.BillingCallID, headKey string, revision uint64, inputSetHash string, want int64) billing.ProviderCostHead {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, refinement4StockPhaseWait)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var head billing.ProviderCostHead
	var err error
	for {
		head, err = store.GetProviderCostHead(ctx, accountID, callID, headKey)
		if err == nil && head.EvidenceRevision == revision && head.InputSetHash == inputSetHash && head.CurrentAmount.Nano == want {
			return head
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for exact provider cost head %q revision %d/hash %q/current %d: head=%+v err=%v", headKey, revision, inputSetHash, want, head, err)
		case <-ticker.C:
		}
	}
}

func refinement52RequireCompleteValuation(t *testing.T, store *billingstore.DurableStore, head billing.EconomicValuationHead) {
	t.Helper()
	valuation, err := store.GetValuation(context.Background(), head.ValuationID, head.ValuationVersion)
	if err != nil {
		t.Fatalf("read valuation %q/%d: %v", head.ValuationID, head.ValuationVersion, err)
	}
	if valuation.InputSetHash != head.InputSetHash || valuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("valuation = %+v, want complete hash %q", valuation, head.InputSetHash)
	}
}

func openRefinement52ConcurrentBillingStore(t *testing.T, path, storeID string) *billingstore.DurableStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = sqlDB.Close()
	})
	return store
}
