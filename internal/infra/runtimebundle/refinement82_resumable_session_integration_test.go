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

// waitRefinement82SettledCalls waits until exactly want call closures exist
// for the account with every exposure closed, then claims any unclaimed
// complete call exactly like the host-loop worker stand-in
// (waitBillingHostLoopCall) does. It returns all closures.
func waitRefinement82SettledCalls(t *testing.T, parent context.Context, store *billingstore.DurableStore, accountID string, want int) []billing.CallUsageRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := store.ListCallUsage(ctx, accountID)
		if err == nil && len(records) == want {
			allClosed := true
			for _, record := range records {
				exposure, exposureErr := store.GetCallExposure(ctx, record.CallID)
				if exposureErr != nil || exposure.IsOpen() {
					allClosed = false
					break
				}
			}
			if allClosed {
				for _, record := range records {
					if _, err := store.ClaimCompleteCall(ctx, record.CallID); err != nil {
						t.Fatalf("ClaimCompleteCall %s: %v", record.CallID, err)
					}
				}
				return records
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d settled calls", want)
		case <-ticker.C:
		}
	}
}

func refinement82RuntimeBLegObservationCount(t *testing.T, ctx context.Context, journal *journalstore.DurableStore, storeID, bLegID string) int {
	t.Helper()
	page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: bLegID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list B-leg observations: %v", err)
	}
	return len(page.Observations)
}

func refinement82RuntimeSawUsage(events []lipapi.Event) bool {
	for _, event := range events {
		if event.Kind == lipapi.EventUsageDelta {
			return true
		}
	}
	return false
}

// TestRefinement82RuntimeResumeKeepsTerminalOwnership executes the Task 8.2
// lifecycle certification through the real production seams only:
//
//   - call1 runs through the stock host's real Executor/B2BUA attempt path
//     with production BillingCallID/B-leg allocation; emitted usage reaches
//     the real TerminalUsageSink (billing.DurableStore via ComposeBilling)
//     through the production terminal handoff — the test never calls
//     AppendCallLegUsage/AppendCallUsage;
//   - the B-leg terminalizes exactly once and the call1 closure freezes its
//     expected B-leg set;
//   - normal DONE (drain to EOF), stream Close (disconnect), and idle
//     quiescence create no usage/economic/journal/balance writes, proven by
//     snapshots of the actual journal, billing store, outbox, and account;
//   - call2 later resumes the SAME A-leg through the real Executor with the
//     production session continuation (ResumeToken/ALegID), receives a
//     distinct production-allocated BillingCallID/B-leg with no A-leg final
//     marker, while the call1 closure/legs remain unchanged;
//   - post-terminal provider finalizer/correction for the SAME closed B-leg
//     travels the real Task 5.2 LateEconomicAppender + stock observation
//     relay/worker path; lifecycle stays closed once with no replacement
//     B-leg, and the economic head receives the late evidence identity;
//   - host shutdown (Host.Close, the process lifecycle shutdown API; NOT
//     TTL/eviction retirement, which the stock host cannot exercise without
//     clock control) creates no usage/economic/journal/balance writes.
func TestRefinement82RuntimeResumeKeepsTerminalOwnership(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	storeID := "refinement82-resume"
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
	accountID := "refinement82-resume-account"
	provisionBillingHostLoopAccount(t, store, accountID)
	injectRefinement52AuthenticUsageBackend(t, executor, accountID)
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})

	// Call 1 through the real Executor. Only the A-leg continuity identity is
	// supplied; the production allocator creates the BillingCallID and B-leg.
	call1 := &lipapi.Call{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: "refinement82-resume-session",
			ContinuityKey:          "refinement82-resume-session",
		},
		Route:    lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("call one")}}},
	}
	stream1, err := executor.Execute(execCtx, call1)
	if err != nil {
		t.Fatalf("Execute call 1: %v", err)
	}
	events1 := drainBillingHostLoopStream(t, ctx, stream1)
	if !refinement82RuntimeSawUsage(events1) {
		t.Fatal("call 1 stream carried no provider usage before DONE")
	}
	aLegID := strings.TrimSpace(call1.Session.ALegID)
	if aLegID == "" || strings.TrimSpace(call1.Session.ResumeToken) == "" || strings.TrimSpace(call1.Session.AuthoritativeSessionID) == "" {
		t.Fatalf("call 1 did not establish resumable A-leg continuity: %+v", call1.Session)
	}
	closure1, _, _ := waitBillingHostLoopCall(t, store, accountID)
	legs1, err := store.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage call 1: %v", err)
	}
	if len(legs1) != 1 || legs1[0].AttemptSeq <= 0 {
		t.Fatalf("terminal B-leg records = %+v, want one positive-sequence leg", legs1)
	}
	leg1 := legs1[0]
	if len(closure1.ExpectedBLegIDs) != 1 || closure1.ExpectedBLegIDs[0] != leg1.BLegID {
		t.Fatalf("call 1 closure B-leg set = %v, want exactly [%s]", closure1.ExpectedBLegIDs, leg1.BLegID)
	}
	authority := refinement52RuntimeProviderAuthority(t, leg1)
	attempts1, err := executor.Store.LoadAttempts(ctx, aLegID)
	if err != nil {
		t.Fatalf("LoadAttempts call 1: %v", err)
	}
	if len(attempts1) != 1 {
		t.Fatalf("B2BUA attempts after call 1 = %d, want 1", len(attempts1))
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)

	// Deterministic quiescence: every expected call1 side effect must reach
	// its exact durable state BEFORE the zero-write snapshots below. Claiming
	// settles asynchronously in background workers, so snapshotting right
	// after the claim races the settlement journal/balance writes.
	expectedInitial := refinement52ExpectedProviderWork(t, ctx, prod.BillingObservationEconomicWorkBuilder, journal, storeID, leg1.BLegID, nil)
	if expectedInitial.EvidenceRevision != 1 {
		t.Fatalf("initial provider work revision = %d, want 1", expectedInitial.EvidenceRevision)
	}
	waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedInitial.HeadKey, 1, expectedInitial.Input.InputSetHash)
	waitRefinement4StockProviderCurrentAmount(t, ctx, store, accountID, closure1.CallID, expectedInitial.HeadKey, billingHostLoopOperatorNano)
	waitRefinement4StockSettlement(t, ctx, store, accountID, closure1.CallID)
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)

	// DONE (drain to EOF) + disconnect (stream Close) + idle quiescence must
	// create no usage/economic/journal/balance writes in the actual stores.
	obsBefore := refinement82RuntimeBLegObservationCount(t, ctx, journal, storeID, leg1.BLegID)
	legsBeforeIdle, err := store.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	closuresBeforeIdle, err := store.ListCallUsage(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	txsBeforeIdle, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	acctBeforeIdle, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	pendingBeforeIdle, err := journal.ListPendingObservationOutbox(ctx, 64)
	if err != nil {
		t.Fatal(err)
	}
	providerWorkBeforeIdle := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueProvider)
	customerWorkBeforeIdle := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueCustomer)
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	if got := refinement82RuntimeBLegObservationCount(t, ctx, journal, storeID, leg1.BLegID); got != obsBefore {
		t.Fatalf("idle created journal observations: before=%d after=%d", obsBefore, got)
	}
	legsAfterIdle, err := store.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legsAfterIdle) != len(legsBeforeIdle) || legsAfterIdle[0].Fingerprint != legsBeforeIdle[0].Fingerprint {
		t.Fatal("idle altered durable B-leg rows")
	}
	closuresAfterIdle, err := store.ListCallUsage(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(closuresAfterIdle) != len(closuresBeforeIdle) || closuresAfterIdle[0].Fingerprint != closuresBeforeIdle[0].Fingerprint {
		t.Fatal("idle altered call closures")
	}
	txsAfterIdle, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txsAfterIdle) != len(txsBeforeIdle) {
		t.Fatal("idle created journal transactions")
	}
	acctAfterIdle, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acctAfterIdle.BalanceNano != acctBeforeIdle.BalanceNano || acctAfterIdle.Version != acctBeforeIdle.Version {
		t.Fatalf("idle altered balance/version: before=%d/%d after=%d/%d", acctBeforeIdle.BalanceNano, acctBeforeIdle.Version, acctAfterIdle.BalanceNano, acctAfterIdle.Version)
	}
	pendingAfterIdle, err := journal.ListPendingObservationOutbox(ctx, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingAfterIdle) != 0 || len(pendingBeforeIdle) != 0 {
		t.Fatalf("idle left observation outbox work: before=%d after=%d", len(pendingBeforeIdle), len(pendingAfterIdle))
	}
	if got := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueProvider); strings.Join(got, "\x00") != strings.Join(providerWorkBeforeIdle, "\x00") {
		t.Fatal("idle altered provider economic work")
	}
	if got := refinement52EconomicWorkIDs(t, store, billing.EconomicQueueCustomer); strings.Join(got, "\x00") != strings.Join(customerWorkBeforeIdle, "\x00") {
		t.Fatal("idle altered customer economic work")
	}

	// Call 2 resumes the SAME A-leg through the real Executor. Production
	// session continuation supplies continuity; the allocator creates the new
	// BillingCallID/B-leg. No A-leg final marker exists or is required.
	call2 := &lipapi.Call{
		Session: lipapi.SessionRef{
			AuthoritativeSessionID: call1.Session.AuthoritativeSessionID,
			ContinuityKey:          call1.Session.ContinuityKey,
			ResumeToken:            call1.Session.ResumeToken,
			ALegID:                 aLegID,
		},
		Route:    lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("call two")}}},
	}
	stream2, err := executor.Execute(execCtx, call2)
	if err != nil {
		t.Fatalf("Execute resumed call 2: %v", err)
	}
	events2 := drainBillingHostLoopStream(t, ctx, stream2)
	if !refinement82RuntimeSawUsage(events2) {
		t.Fatal("resumed call 2 stream carried no provider usage before DONE")
	}
	if strings.TrimSpace(call2.Session.ALegID) != aLegID {
		t.Fatalf("call 2 A-leg = %q, want resumed A-leg %q", call2.Session.ALegID, aLegID)
	}
	closures := waitRefinement82SettledCalls(t, ctx, store, accountID, 2)
	var closure2 billing.CallUsageRecord
	for _, record := range closures {
		if record.CallID == closure1.CallID {
			if record.Fingerprint != closure1.Fingerprint {
				t.Fatal("resumed call 2 rewrote the call 1 closure")
			}
			continue
		}
		closure2 = record
	}
	if closure2.CallID == "" {
		t.Fatal("resumed call 2 closure missing")
	}
	if closure2.CallID == closure1.CallID {
		t.Fatalf("resumed call reused call 1 BillingCallID %q", closure1.CallID)
	}
	if closure2.ALegID != aLegID {
		t.Fatalf("call 2 closure A-leg = %q, want %q", closure2.ALegID, aLegID)
	}
	legs2, err := store.ListCallLegUsage(ctx, closure2.CallID)
	if err != nil {
		t.Fatalf("ListCallLegUsage call 2: %v", err)
	}
	if len(legs2) != 1 {
		t.Fatalf("call 2 legs = %+v, want one terminal leg", legs2)
	}
	if legs2[0].BLegID == leg1.BLegID {
		t.Fatalf("resumed call reused call 1 B-leg %q", leg1.BLegID)
	}
	if len(closure2.ExpectedBLegIDs) != 1 || closure2.ExpectedBLegIDs[0] != legs2[0].BLegID {
		t.Fatalf("call 2 closure B-leg set = %v, want exactly [%s]", closure2.ExpectedBLegIDs, legs2[0].BLegID)
	}
	legs1AfterCall2, err := store.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs1AfterCall2) != 1 || legs1AfterCall2[0].Fingerprint != leg1.Fingerprint {
		t.Fatal("resumed call 2 mutated the call 1 B-leg record")
	}
	attemptsAfterCall2, err := executor.Store.LoadAttempts(ctx, aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attemptsAfterCall2) != 2 {
		t.Fatalf("B2BUA attempts after resume = %d, want 2", len(attemptsAfterCall2))
	}

	// Post-terminal provider finalizer + correction for the SAME closed B-leg
	// travel the real Task 5.2 sideband (LateEconomicAppender) and the stock
	// observation relay/workers. Lifecycle stays closed exactly once: no
	// replacement B-leg, no reopened attempt.
	identity := coremetering.LateEconomicIdentity{
		StoreID: storeID, BillingCallID: closure1.CallID, ALegID: closure1.ALegID, BLegID: leg1.BLegID,
		AttemptID: leg1.BLegID, AttemptSeq: uint64(leg1.AttemptSeq), ProviderID: leg1.ProviderID,
		ProviderAccountKey: authority.Subject.ProviderAccountKey, ProviderRequestID: authority.Subject.ProviderRequestID, ProviderChargeID: authority.Subject.ProviderChargeID,
	}
	finalizer := refinement52RuntimeObservation(storeID, closure1.CallID, closure1.ALegID, leg1, authority, "82-finalizer", 2, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer}); err != nil {
		t.Fatalf("late provider finalizer: %v", err)
	}
	if _, err := journal.GetObservation(ctx, finalizer.ID, finalizer.Revision); err != nil {
		t.Fatalf("read late finalizer: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	expectedFinalizer := refinement52ExpectedProviderWork(t, ctx, prod.BillingObservationEconomicWorkBuilder, journal, storeID, leg1.BLegID, nil)
	if expectedFinalizer.EvidenceRevision != 2 {
		t.Fatalf("finalizer provider work revision = %d, want 2", expectedFinalizer.EvidenceRevision)
	}
	finalizerInWork := false
	for _, observation := range expectedFinalizer.Input.Observations {
		if observation.ID == finalizer.ID {
			finalizerInWork = true
			break
		}
	}
	if !finalizerInWork {
		t.Fatalf("provider work omitted late finalizer %q: observations=%+v", finalizer.ID, expectedFinalizer.Input.Observations)
	}
	finalizerHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedFinalizer.HeadKey, 2, expectedFinalizer.Input.InputSetHash)
	legsAfterFinalizer, err := store.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legsAfterFinalizer) != 1 || legsAfterFinalizer[0].Fingerprint != leg1.Fingerprint || legsAfterFinalizer[0].BLegID != leg1.BLegID || legsAfterFinalizer[0].AttemptSeq != leg1.AttemptSeq {
		t.Fatalf("late finalizer rewrote lifecycle leg: before=%+v after=%+v", leg1, legsAfterFinalizer)
	}

	correction := refinement52RuntimeObservation(storeID, closure1.CallID, closure1.ALegID, leg1, authority, "82-correction", 3, metering.OriginProvider, metering.AcquisitionProviderFinalizer, metering.AuthorityObservedClaim)
	correction.SourceEventKey = finalizer.SourceEventKey
	correction.Subject = finalizer.Subject
	correction.Correlation = finalizer.Correlation
	correction.Semantics = metering.SemanticsCorrection
	correction.Charges[0].ChargeItemID = finalizer.Charges[0].ChargeItemID
	correctionAmount := metering.DecimalFromNanoUnits(9)
	correction.Charges[0].Amount = &correctionAmount
	priorRef, err := finalizer.Ref(storeID)
	if err != nil {
		t.Fatalf("finalizer ref: %v", err)
	}
	correction.Supersedes = []metering.ObservationRef{priorRef}
	if err := lateAppender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicCorrection, Identity: identity, Observation: correction}); err != nil {
		t.Fatalf("late provider correction: %v", err)
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	if len(correction.Supersedes) != 1 || !correction.Supersedes[0].Equal(priorRef) {
		t.Fatal("correction lost exact finalizer supersession")
	}
	expectedCorrection := refinement52ExpectedProviderWork(t, ctx, prod.BillingObservationEconomicWorkBuilder, journal, storeID, leg1.BLegID, nil)
	if expectedCorrection.EvidenceRevision != 3 {
		t.Fatalf("correction provider work revision = %d, want 3", expectedCorrection.EvidenceRevision)
	}
	correctionHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedCorrection.HeadKey, 3, expectedCorrection.Input.InputSetHash)
	legsAfterCorrection, err := store.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legsAfterCorrection) != 1 || legsAfterCorrection[0].Fingerprint != leg1.Fingerprint {
		t.Fatalf("late correction rewrote lifecycle leg: before=%+v after=%+v", leg1, legsAfterCorrection)
	}
	attemptsAfterLate, err := executor.Store.LoadAttempts(ctx, aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attemptsAfterLate) != 2 {
		t.Fatalf("late evidence allocated/reopened attempt: before=2 after=%d", len(attemptsAfterLate))
	}
	for _, late := range []coremetering.LateEconomicEvidence{
		{Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer},
		{Kind: coremetering.LateEconomicCorrection, Identity: identity, Observation: correction},
	} {
		if err := lateAppender.AppendLateEconomicEvidence(ctx, late); err != nil {
			t.Fatalf("exact late evidence replay (%s): %v", late.Kind, err)
		}
	}
	waitRefinement4StockOutboxDrained(t, ctx, store, journal)
	replayedHead := waitRefinement52StockHeadExact(t, ctx, store, journal, accountID, billing.EconomicQueueProvider, expectedCorrection.HeadKey, 3, expectedCorrection.Input.InputSetHash)
	if replayedHead.Fingerprint != correctionHead.Fingerprint || replayedHead.HeadVersion != correctionHead.HeadVersion || replayedHead.Fence != correctionHead.Fence {
		t.Fatalf("exact replay advanced provider head: before=%+v after=%+v", correctionHead, replayedHead)
	}
	_ = finalizerHead

	// Host shutdown through the process lifecycle API creates no
	// usage/economic/journal/balance writes. This is shutdown/reopen, not
	// TTL/eviction retirement: the stock host holds continuity in memory
	// without clock control, so no TTL-eviction path is exercised here.
	legsShutdownBefore, err := store.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	legs2ShutdownBefore, err := store.ListCallLegUsage(ctx, closure2.CallID)
	if err != nil {
		t.Fatal(err)
	}
	closuresShutdownBefore, err := store.ListCallUsage(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	txsShutdownBefore, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	acctShutdownBefore, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	obsShutdownBefore := refinement82RuntimeBLegObservationCount(t, ctx, journal, storeID, leg1.BLegID)
	headShutdownBefore, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, expectedCorrection.HeadKey)
	if err != nil {
		t.Fatalf("provider head before shutdown: %v", err)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("host shutdown: %v", err)
	}
	reopenedStore := openRefinement52ConcurrentBillingStore(t, billingPath, storeID)
	reopenedJournal := openRefinement52RuntimeJournal(t, journalPath, storeID)
	legsShutdownAfter, err := reopenedStore.ListCallLegUsage(ctx, closure1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legsShutdownAfter) != len(legsShutdownBefore) || legsShutdownAfter[0].Fingerprint != legsShutdownBefore[0].Fingerprint {
		t.Fatal("shutdown altered durable B-leg rows")
	}
	legs2ShutdownAfter, err := reopenedStore.ListCallLegUsage(ctx, closure2.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs2ShutdownAfter) != len(legs2ShutdownBefore) || legs2ShutdownAfter[0].Fingerprint != legs2ShutdownBefore[0].Fingerprint {
		t.Fatal("shutdown altered resumed call B-leg rows")
	}
	closuresShutdownAfter, err := reopenedStore.ListCallUsage(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(closuresShutdownAfter) != len(closuresShutdownBefore) {
		t.Fatal("shutdown altered call closures")
	}
	for i := range closuresShutdownAfter {
		if closuresShutdownAfter[i].Fingerprint != closuresShutdownBefore[i].Fingerprint {
			t.Fatal("shutdown altered a call closure")
		}
	}
	txsShutdownAfter, err := reopenedStore.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txsShutdownAfter) != len(txsShutdownBefore) {
		t.Fatal("shutdown created journal transactions")
	}
	acctShutdownAfter, err := reopenedStore.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acctShutdownAfter.BalanceNano != acctShutdownBefore.BalanceNano || acctShutdownAfter.Version != acctShutdownBefore.Version {
		t.Fatal("shutdown altered account balance/version")
	}
	if got := refinement82RuntimeBLegObservationCount(t, ctx, reopenedJournal, storeID, leg1.BLegID); got != obsShutdownBefore {
		t.Fatalf("shutdown altered journal observations: before=%d after=%d", obsShutdownBefore, got)
	}
	headShutdownAfter, err := reopenedStore.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, expectedCorrection.HeadKey)
	if err != nil {
		t.Fatalf("provider head after shutdown: %v", err)
	}
	if headShutdownAfter.Fingerprint != headShutdownBefore.Fingerprint || headShutdownAfter.HeadVersion != headShutdownBefore.HeadVersion || headShutdownAfter.Fence != headShutdownBefore.Fence {
		t.Fatalf("shutdown altered provider head: before=%+v after=%+v", headShutdownBefore, headShutdownAfter)
	}
}
