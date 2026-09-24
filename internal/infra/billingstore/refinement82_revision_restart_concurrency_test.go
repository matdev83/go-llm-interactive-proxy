package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

// openRefinement82FileBillingStore opens a file-backed durable billing store at
// an explicit path so restart tests can close and reopen the same identity.
// The caller owns both handles and must call closeFunc exactly once per open.
func openRefinement82FileBillingStore(t *testing.T, path, storeID string) (*DurableStore, func()) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
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
	seedTestSchemaIfEmpty(t, bunDB)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	return store, func() {
		_ = store.Close()
		_ = sqlDB.Close()
	}
}

func refinement82ValuationFor(t *testing.T, work corebilling.EconomicRevisionWork) economics.Valuation {
	t.Helper()
	normalized, err := work.Normalize()
	if err != nil {
		t.Fatalf("normalize economic work: %v", err)
	}
	identity, err := normalized.Identity()
	if err != nil {
		t.Fatalf("identify economic work: %v", err)
	}
	return economics.Valuation{
		ID: identity.ValuationKey(), Version: economics.ValuationVersionV2,
		Perspective: normalized.Input.Perspective, Basis: normalized.Input.Basis,
		Subject: normalized.Subject, Scope: normalized.Input.Scope,
		InputObservations: append([]metering.ObservationRef(nil), normalized.Input.ObservationRefs...),
		Completeness:      economics.CompletenessPartial, CreatedAt: time.Unix(1_700_082_000, 0).UTC(),
	}
}

//nolint:revive // test helper keeps t first per Go testing convention
func refinement82ProviderWork(t *testing.T, ctx context.Context, builder corebilling.ObservationEconomicWorkBuilder, observations ...metering.Observation) corebilling.EconomicRevisionWork {
	t.Helper()
	works, err := builder.BuildEconomicRevisionWork(ctx, observations)
	if err != nil {
		t.Fatalf("build economic work: %v", err)
	}
	for _, work := range works {
		if work.Queue == corebilling.EconomicQueueProvider {
			return work
		}
	}
	t.Fatalf("provider economic work missing: %+v", works)
	return corebilling.EconomicRevisionWork{}
}

//nolint:revive // test helper keeps t first per Go testing convention
func refinement82ValuationCount(t *testing.T, ctx context.Context, store *DurableStore) int {
	t.Helper()
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.storeID).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates
// certifies matrix cases 2, 3, 4 and the provider side of case 9:
//
//   - a pre-terminal authoritative usage checkpoint (revision 1) becomes
//     durable through the real journal append path and can advance provider
//     accrual under revision/fence rules (Req 4.1, 4.2, 4.3);
//   - the terminal checkpoint (revision 2 finalizer via the production late
//     appender after the B-leg row is sealed) advances the same head without
//     duplicate quantities or postings (Req 4.4);
//   - a post-terminal correction (revision 3, same closed B-leg, exact
//     supersession) updates the head by fenced delta without reopening
//     execution or allocating a replacement B-leg (Req 3.4, 4.5);
//   - exact replay at every stage is idempotent: heads, fingerprints, fences,
//     and valuation row counts are unchanged.
func TestRefinement82PreterminalTerminalCorrectionAdvancesWithoutDuplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement82-preterm-%d", time.Now().UnixNano())
	billingStore := openRefinement52BillingStore(t, storeID)
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	journal := openRefinement52Journal(t, journalPath, storeID)

	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Pre-terminal checkpoint: provider response revision 1 is durably
	// appended before any terminal closure exists.
	preterminal := refinement52ProviderObservation(storeID, callID, 1, "preterminal", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	preterminal.ID = "refinement82-preterminal"
	preterminal.SourceEventKey = "refinement82-source-preterminal"
	if err := journal.AppendObservations(ctx, []metering.Observation{preterminal}); err != nil {
		t.Fatalf("durable pre-terminal checkpoint: %v", err)
	}
	work1 := refinement82ProviderWork(t, ctx, builder, preterminal)
	if work1.EvidenceRevision != 1 {
		t.Fatalf("pre-terminal work revision = %d, want 1", work1.EvidenceRevision)
	}
	if err := billingStore.AppendEconomicRevisionResult(ctx, work1, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work1)}); err != nil {
		t.Fatalf("pre-terminal valuation result: %v", err)
	}
	head1, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work1.HeadKey)
	if err != nil {
		t.Fatalf("pre-terminal head: %v", err)
	}
	if head1.HeadVersion != 1 || head1.InputSetHash != work1.InputSetHash {
		t.Fatalf("pre-terminal head = %+v, want version 1 with work hash", head1)
	}
	valuationsAfterPreterminal := refinement82ValuationCount(t, ctx, billingStore)

	// Exact replay of the pre-terminal stage changes nothing.
	if err := billingStore.AppendEconomicRevisionResult(ctx, work1, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work1)}); err != nil {
		t.Fatalf("pre-terminal replay: %v", err)
	}
	head1Replay, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work1.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head1Replay.Fingerprint != head1.Fingerprint || head1Replay.HeadVersion != head1.HeadVersion || head1Replay.Fence != head1.Fence {
		t.Fatalf("pre-terminal replay mutated head: before=%+v after=%+v", head1, head1Replay)
	}
	if got := refinement82ValuationCount(t, ctx, billingStore); got != valuationsAfterPreterminal {
		t.Fatalf("pre-terminal replay added valuations: before=%d after=%d", valuationsAfterPreterminal, got)
	}

	// Terminal checkpoint: seal the B-leg row (execution closes exactly once),
	// then append the provider finalizer revision 2 through the production
	// late appender against the same closed identity.
	leg := refinement52ClosedLeg(storeID, callID)
	if err := billingStore.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("terminal leg append: %v", err)
	}
	appender, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: billingStore,
		Sink: journalstore.NewObservationSinkWithOutbox(journal), Resolver: journal,
	})
	if err != nil {
		t.Fatalf("late appender: %v", err)
	}
	finalizer := refinement52ProviderObservation(storeID, callID, 2, "terminal", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	if err := appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
		Kind:     coremetering.LateEconomicProviderFinalizer,
		Identity: refinement52LateIdentity(storeID, callID), Observation: finalizer,
	}); err != nil {
		t.Fatalf("terminal finalizer append: %v", err)
	}
	work2 := refinement82ProviderWork(t, ctx, builder, preterminal, finalizer)
	if work2.EvidenceRevision != 2 {
		t.Fatalf("terminal work revision = %d, want 2", work2.EvidenceRevision)
	}
	if work2.HeadKey != work1.HeadKey {
		t.Fatalf("terminal work head %q diverged from pre-terminal head %q", work2.HeadKey, work1.HeadKey)
	}
	if err := billingStore.AppendEconomicRevisionResult(ctx, work2, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work2)}); err != nil {
		t.Fatalf("terminal valuation result: %v", err)
	}
	head2, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work2.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head2.HeadVersion != 2 || head2.InputSetHash != work2.InputSetHash {
		t.Fatalf("terminal head = %+v, want version 2 with revision-2 hash", head2)
	}
	valuationsAfterTerminal := refinement82ValuationCount(t, ctx, billingStore)
	if valuationsAfterTerminal != valuationsAfterPreterminal+1 {
		t.Fatalf("terminal stage valuations = %d, want exactly one more than %d", valuationsAfterTerminal, valuationsAfterPreterminal)
	}

	// Post-terminal correction: same closed B-leg, exact finalizer
	// supersession, fenced delta only. Execution is not reopened.
	correction := refinement52ProviderObservation(storeID, callID, 3, "correction", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
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
	if err := appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
		Kind:     coremetering.LateEconomicCorrection,
		Identity: refinement52LateIdentity(storeID, callID), Observation: correction,
	}); err != nil {
		t.Fatalf("post-terminal correction append: %v", err)
	}
	work3 := refinement82ProviderWork(t, ctx, builder, preterminal, finalizer, correction)
	if work3.EvidenceRevision != 3 {
		t.Fatalf("correction work revision = %d, want 3", work3.EvidenceRevision)
	}
	if err := billingStore.AppendEconomicRevisionResult(ctx, work3, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work3)}); err != nil {
		t.Fatalf("correction valuation result: %v", err)
	}
	head3, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work3.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head3.HeadVersion != 3 || head3.InputSetHash != work3.InputSetHash {
		t.Fatalf("correction head = %+v, want version 3 with revision-3 hash", head3)
	}
	if got := refinement82ValuationCount(t, ctx, billingStore); got != valuationsAfterTerminal+1 {
		t.Fatalf("correction stage valuations = %d, want exactly one more than %d", got, valuationsAfterTerminal)
	}
	if err := billingStore.AppendEconomicRevisionResult(ctx, work3, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work3)}); err != nil {
		t.Fatalf("correction replay: %v", err)
	}
	head3Replay, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work3.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head3Replay.Fingerprint != head3.Fingerprint || head3Replay.HeadVersion != head3.HeadVersion || head3Replay.Fence != head3.Fence {
		t.Fatalf("correction replay mutated head: before=%+v after=%+v", head3, head3Replay)
	}

	// The closed B-leg row is byte-stable across all three economic stages:
	// terminal execution closed exactly once, economics only appended.
	legs, err := billingStore.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 1 || legs[0].BLegID != "b-leg" || legs[0].AttemptSeq != 1 {
		t.Fatalf("closed B-leg rows = %+v, want the single sealed leg", legs)
	}
	page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{StoreID: storeID, StreamID: "refinement52-stream", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Observations) != 3 {
		t.Fatalf("durable observation revisions = %d, want pre-terminal + finalizer + correction", len(page.Observations))
	}
}

// TestRefinement82RestartContinuesIdentitiesWithoutLossOrDuplicates certifies
// matrix case 5: process/store restart between checkpoints reopens the
// supported durable SQLite stores and host seams, then continues the same
// BillingCallID/B-leg identities and evidence revisions without loss or
// duplicate posting (Req 4.1, 4.2, 4.5, 6.4).
func TestRefinement82RestartContinuesIdentitiesWithoutLossOrDuplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement82-restart-%d", time.Now().UnixNano())
	dir := t.TempDir()
	billingPath := filepath.Join(dir, "billing.sqlite")
	journalPath := filepath.Join(dir, "metering.sqlite")

	billingStore, closeBilling := openRefinement82FileBillingStore(t, billingPath, storeID)
	journal := openRefinement52Journal(t, journalPath, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Phase 1: pre-terminal checkpoint + sealed terminal leg, then full restart.
	preterminal := refinement52ProviderObservation(storeID, callID, 1, "restart-pre", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	preterminal.ID = "refinement82-restart-pre"
	preterminal.SourceEventKey = "refinement82-restart-source-pre"
	if err := journal.AppendObservations(ctx, []metering.Observation{preterminal}); err != nil {
		t.Fatalf("pre-restart checkpoint: %v", err)
	}
	leg := refinement52ClosedLeg(storeID, callID)
	if err := billingStore.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("pre-restart terminal leg: %v", err)
	}
	work1 := refinement82ProviderWork(t, ctx, builder, preterminal)
	if err := billingStore.AppendEconomicRevisionResult(ctx, work1, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work1)}); err != nil {
		t.Fatalf("pre-restart valuation: %v", err)
	}
	headBefore, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work1.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	legKey, err := corebilling.CallLegUsageKey(callID, leg.BLegID)
	if err != nil {
		t.Fatal(err)
	}
	sealedBefore, err := billingStore.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatal(err)
	}
	valuationsBefore := refinement82ValuationCount(t, ctx, billingStore)
	if err := journal.Close(); err != nil {
		t.Fatalf("close journal for restart: %v", err)
	}
	closeBilling()

	// Phase 2: reopen both durable stores through production constructors and
	// continue the same identities: late finalizer + correction reference the
	// identical closed B-leg and advance the same head.
	billingStore, closeBilling = openRefinement82FileBillingStore(t, billingPath, storeID)
	defer closeBilling()
	journal = openRefinement52Journal(t, journalPath, storeID)

	sealedAfterReopen, err := billingStore.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatalf("sealed leg lost across restart: %v", err)
	}
	if sealedAfterReopen.Fingerprint != sealedBefore.Fingerprint {
		t.Fatal("sealed leg fingerprint changed across restart")
	}
	headAfterReopen, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work1.HeadKey)
	if err != nil {
		t.Fatalf("valuation head lost across restart: %v", err)
	}
	if headAfterReopen.Fingerprint != headBefore.Fingerprint || headAfterReopen.InputSetHash != headBefore.InputSetHash {
		t.Fatalf("valuation head changed across restart: before=%+v after=%+v", headBefore, headAfterReopen)
	}
	if _, err := journal.GetObservation(ctx, preterminal.ID, preterminal.Revision); err != nil {
		t.Fatalf("pre-terminal observation lost across restart: %v", err)
	}

	appender, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: billingStore,
		Sink: journalstore.NewObservationSinkWithOutbox(journal), Resolver: journal,
	})
	if err != nil {
		t.Fatalf("restarted late appender: %v", err)
	}
	finalizer := refinement52ProviderObservation(storeID, callID, 2, "restart-final", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	if err := appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
		Kind:     coremetering.LateEconomicProviderFinalizer,
		Identity: refinement52LateIdentity(storeID, callID), Observation: finalizer,
	}); err != nil {
		t.Fatalf("post-restart finalizer: %v", err)
	}
	work2 := refinement82ProviderWork(t, ctx, builder, preterminal, finalizer)
	if err := billingStore.AppendEconomicRevisionResult(ctx, work2, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work2)}); err != nil {
		t.Fatalf("post-restart valuation: %v", err)
	}
	head2, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work2.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head2.HeadVersion != 2 {
		t.Fatalf("post-restart head version = %d, want 2", head2.HeadVersion)
	}
	if got := refinement82ValuationCount(t, ctx, billingStore); got != valuationsBefore+1 {
		t.Fatalf("post-restart valuations = %d, want exactly one more than %d", got, valuationsBefore)
	}
	legs, err := billingStore.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 1 {
		t.Fatalf("B-leg rows after restart = %d, want 1 (no replacement allocation)", len(legs))
	}
}

// refinement82ReleaseBarrier runs fns concurrently with a synchronized
// start: every goroutine signals ready first, the starter waits for all N
// readies, then closes the gate. No sleeps; every goroutine result is
// collected into the indexed slice. Overlapping execution of the critical
// section is intended but never assumed — assertions must accept every
// scheduling the production contract allows.
func refinement82ReleaseBarrier(t *testing.T, n int, fn func(i int) error) []error {
	t.Helper()
	ready := make(chan int, n)
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ready <- i
			<-start
			errs[i] = fn(i)
		}(i)
	}
	for range n {
		<-ready
	}
	close(start)
	wg.Wait()
	return errs
}

// TestRefinement82ConcurrentExactReplaysShareOneIdentity fires 16 late
// appends of one finalizer revision through a synchronized start gate and
// then races 8 identical revision-result appends. The allowed outcome set is
// exact: every appender call succeeds (atomic sink) with a single durable
// journal/outbox identity, and exactly one result append creates the
// valuation/head while the rest are classified as conflicts — no duplicate
// rows, no partially advanced head (Req 4.2, 4.5, 6.4).
func TestRefinement82ConcurrentExactReplaysShareOneIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement82-barrier-replay-%d", time.Now().UnixNano())
	billingStore := openRefinement52BillingStore(t, storeID)
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	journal := openRefinement52Journal(t, journalPath, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := billingStore.AppendCallLegUsage(ctx, refinement52ClosedLeg(storeID, callID)); err != nil {
		t.Fatal(err)
	}
	appender, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: billingStore,
		Sink: journalstore.NewObservationSinkWithOutbox(journal), Resolver: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalizer := refinement52ProviderObservation(storeID, callID, 2, "barrier-final", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	identity := refinement52LateIdentity(storeID, callID)

	const replayWriters = 16
	for i, err := range refinement82ReleaseBarrier(t, replayWriters, func(int) error {
		return appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
			Kind: coremetering.LateEconomicProviderFinalizer, Identity: identity, Observation: finalizer,
		})
	}) {
		if err != nil {
			t.Fatalf("barrier exact replay %d: %v", i, err)
		}
	}
	page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{StoreID: storeID, StreamID: "refinement52-stream", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Observations) != 1 {
		t.Fatalf("durable observations after %d-way replay = %d, want 1", replayWriters, len(page.Observations))
	}
	pending, err := journal.ListPendingObservationOutbox(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending outbox after %d-way replay = %d, want 1", replayWriters, len(pending))
	}

	initial := refinement52ProviderObservation(storeID, callID, 1, "barrier-initial", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	work := refinement82ProviderWork(t, ctx, builder, initial, finalizer)
	valuation := refinement82ValuationFor(t, work)
	// The writer lock serializes the race: the first commit wins and every
	// other arrival hits the idempotent-replay probe, so all results are nil
	// while exactly one valuation row and one head version are created.
	const resultWriters = 8
	results := refinement82ReleaseBarrier(t, resultWriters, func(int) error {
		return billingStore.AppendEconomicRevisionResult(ctx, work, corebilling.EconomicRevisionResult{Valuation: valuation})
	})
	for i, err := range results {
		if err != nil {
			t.Fatalf("barrier result replay %d: %v", i, err)
		}
	}
	head, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, work.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head.InputSetHash != work.InputSetHash || head.HeadVersion != 1 {
		t.Fatalf("head after replay race = %+v, want single version with work hash", head)
	}
	valuations, err := billingStore.ListValuations(ctx, ValuationQuery{StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: "b-leg", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(valuations.Valuations) != 1 {
		t.Fatalf("durable valuations after replay race = %d, want 1", len(valuations.Valuations))
	}
}

// TestRefinement82ConcurrentWorkerClaimsElectOneWinner races 8 production
// worker claims over one revision work item through a synchronized start
// gate. Exactly one claimant acquires the lease regardless of scheduling.
// A foreign-owner completion fails closed, the winner completes exactly
// once, a second completion is claim-lost, and after completion no claimant
// can acquire again. All errors are collected; none may occur (Req 4.2).
func TestRefinement82ConcurrentWorkerClaimsElectOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement82-barrier-claim-%d", time.Now().UnixNano())
	billingStore := openRefinement52BillingStore(t, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	initial := refinement52ProviderObservation(storeID, callID, 1, "claim-initial", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	work := refinement82ProviderWork(t, ctx, builder, initial)
	if err := billingStore.AppendEconomicRevisionWork(ctx, work); err != nil {
		t.Fatalf("append revision work: %v", err)
	}

	const claimants = 8
	type claimOutcome struct {
		acquired bool
		fence    uint64
	}
	outcomes := make([]claimOutcome, claimants)
	errs := refinement82ReleaseBarrier(t, claimants, func(i int) error {
		claim, acquired, err := billingStore.ClaimEconomicRevisionWork(ctx, work, fmt.Sprintf("worker-%d", i), time.Minute)
		if err != nil {
			return err
		}
		outcomes[i] = claimOutcome{acquired: acquired, fence: claim.Fence}
		return nil
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("barrier claim %d: %v", i, err)
		}
	}
	var winners []int
	for i, outcome := range outcomes {
		if outcome.acquired {
			if outcome.fence == 0 {
				t.Fatalf("claimant %d acquired with zero fence", i)
			}
			winners = append(winners, i)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("claim winners = %v, want exactly one", winners)
	}
	winner := winners[0]
	// A foreign-owner completion fails closed even though the lease is held.
	loser := (winner + 1) % claimants
	if err := billingStore.CompleteEconomicRevisionWork(ctx, work, corebilling.EconomicRevisionWorkClaim{Owner: fmt.Sprintf("worker-%d", loser), Fence: outcomes[winner].fence}); !errors.Is(err, corebilling.ErrEconomicRevisionClaimLost) {
		t.Fatalf("foreign-owner complete error = %v, want ErrEconomicRevisionClaimLost", err)
	}
	if err := billingStore.CompleteEconomicRevisionWork(ctx, work, corebilling.EconomicRevisionWorkClaim{Owner: fmt.Sprintf("worker-%d", winner), Fence: outcomes[winner].fence}); err != nil {
		t.Fatalf("winner complete: %v", err)
	}
	if err := billingStore.CompleteEconomicRevisionWork(ctx, work, corebilling.EconomicRevisionWorkClaim{Owner: fmt.Sprintf("worker-%d", winner), Fence: outcomes[winner].fence}); !errors.Is(err, corebilling.ErrEconomicRevisionClaimLost) {
		t.Fatalf("second complete error = %v, want ErrEconomicRevisionClaimLost", err)
	}
	reclaims := refinement82ReleaseBarrier(t, claimants, func(i int) error {
		_, acquired, err := billingStore.ClaimEconomicRevisionWork(ctx, work, fmt.Sprintf("worker-%d", i), time.Minute)
		if err != nil {
			return err
		}
		if acquired {
			return fmt.Errorf("claimant %d acquired completed work", i)
		}
		return nil
	})
	for i, err := range reclaims {
		if err != nil {
			t.Fatalf("post-completion claim %d: %v", i, err)
		}
	}
}

// TestRefinement82ConcurrentRevisionRaceFencesExactlyOnce races a strict
// superset (guaranteed advance) against an incomparable same-revision branch
// (guaranteed fence) on an established head through a synchronized start
// gate. Both commit orders yield the same outcome: exactly one success plus
// one ErrEconomicRevisionFence, the final head equals the superset at
// version 2, exactly two valuations persist, and a later strict subset is a
// stable no-op (Req 4.2, 4.5, 6.4).
func TestRefinement82ConcurrentRevisionRaceFencesExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement82-barrier-fence-%d", time.Now().UnixNano())
	billingStore := openRefinement52BillingStore(t, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a := refinement52ProviderObservation(storeID, callID, 1, "fence-a", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	b := refinement52ProviderObservation(storeID, callID, 3, "fence-b", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	c := refinement52ProviderObservation(storeID, callID, 2, "fence-c", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	d := refinement52ProviderObservation(storeID, callID, 2, "fence-d", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	base := refinement82ProviderWork(t, ctx, builder, a, b)
	if base.EvidenceRevision != 3 {
		t.Fatalf("base revision = %d, want 3", base.EvidenceRevision)
	}
	if err := billingStore.AppendEconomicRevisionResult(ctx, base, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, base)}); err != nil {
		t.Fatalf("establish base head: %v", err)
	}
	// X strictly contains the base set (guaranteed advance); Y is
	// incomparable with the base set and with X (guaranteed fence). Both
	// commit orders therefore yield the same outcome: X nil, Y fenced.
	superset := refinement82ProviderWork(t, ctx, builder, a, b, c)
	incomparable := refinement82ProviderWork(t, ctx, builder, b, d)
	if superset.HeadKey != base.HeadKey || incomparable.HeadKey != base.HeadKey {
		t.Fatalf("race head keys differ: base=%q superset=%q incomparable=%q", base.HeadKey, superset.HeadKey, incomparable.HeadKey)
	}
	if superset.EvidenceRevision != 3 || incomparable.EvidenceRevision != 3 {
		t.Fatalf("race revisions = %d/%d, want 3/3", superset.EvidenceRevision, incomparable.EvidenceRevision)
	}
	works := []corebilling.EconomicRevisionWork{superset, incomparable}
	results := refinement82ReleaseBarrier(t, 2, func(i int) error {
		return billingStore.AppendEconomicRevisionResult(ctx, works[i], corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, works[i])})
	})
	if results[0] != nil {
		t.Fatalf("superset branch error = %v, want nil (strict superset always advances)", results[0])
	}
	if !errors.Is(results[1], corebilling.ErrEconomicRevisionFence) {
		t.Fatalf("incomparable branch error = %v, want ErrEconomicRevisionFence", results[1])
	}
	head, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, base.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head.InputSetHash != superset.InputSetHash || head.HeadVersion != 2 {
		t.Fatalf("head after fence race = %+v, want superset hash at version 2", head)
	}
	valuations, err := billingStore.ListValuations(ctx, ValuationQuery{StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: "b-leg", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(valuations.Valuations) != 2 {
		t.Fatalf("durable valuations after fence race = %d, want base + superset", len(valuations.Valuations))
	}
	subset := refinement82ProviderWork(t, ctx, builder, b)
	if err := billingStore.AppendEconomicRevisionResult(ctx, subset, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, subset)}); err != nil {
		t.Fatalf("stale subset result: %v", err)
	}
	headAfterSubset, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, base.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if headAfterSubset.InputSetHash != head.InputSetHash || headAfterSubset.HeadVersion != head.HeadVersion {
		t.Fatalf("stale subset moved head: before=%+v after=%+v", head, headAfterSubset)
	}
}

// TestRefinement82ConcurrentClaimWhileCorrectionLands interleaves a worker
// claim plus rev-2 result against a concurrent rev-3 correction result
// through a synchronized start gate: the claim lease and the head fence are
// independent gates, so both result appends succeed while exactly one
// claimant holds the rev-2 lease. Both commit orders converge on the same
// final head (rev 3); the rev-2 result is either the prior head or a stale
// no-op, never a rollback (Req 4.2, 4.5, 6.4).
func TestRefinement82ConcurrentClaimWhileCorrectionLands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement82-barrier-interleave-%d", time.Now().UnixNano())
	billingStore := openRefinement52BillingStore(t, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	o1 := refinement52ProviderObservation(storeID, callID, 1, "interleave-o1", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	o2 := refinement52ProviderObservation(storeID, callID, 2, "interleave-o2", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	o3 := refinement52ProviderObservation(storeID, callID, 3, "interleave-o3", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	rev2 := refinement82ProviderWork(t, ctx, builder, o1, o2)
	rev3 := refinement82ProviderWork(t, ctx, builder, o1, o2, o3)
	if rev2.HeadKey != rev3.HeadKey {
		t.Fatalf("interleave head keys differ: %q vs %q", rev2.HeadKey, rev3.HeadKey)
	}
	acquired := make([]bool, 2)
	errs := refinement82ReleaseBarrier(t, 2, func(i int) error {
		owner := "worker-rev2"
		appendWork := rev2
		if i == 1 {
			owner = "worker-rev3"
			appendWork = rev3
		}
		claim, got, err := billingStore.ClaimEconomicRevisionWork(ctx, rev2, owner, time.Minute)
		if err != nil {
			return err
		}
		acquired[i] = got
		if err := billingStore.AppendEconomicRevisionResult(ctx, appendWork, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, appendWork)}); err != nil {
			return err
		}
		if got {
			return billingStore.CompleteEconomicRevisionWork(ctx, rev2, corebilling.EconomicRevisionWorkClaim{Owner: owner, Fence: claim.Fence})
		}
		return nil
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("interleave side %d: %v", i, err)
		}
	}
	acquiredCount := 0
	for _, got := range acquired {
		if got {
			acquiredCount++
		}
	}
	if acquiredCount != 1 {
		t.Fatalf("rev-2 lease holders = %d, want exactly one", acquiredCount)
	}
	head, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, rev3.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head.EvidenceRevision != 3 || head.InputSetHash != rev3.InputSetHash {
		t.Fatalf("head after interleave = %+v, want rev 3 with correction hash", head)
	}
	if _, got, err := billingStore.ClaimEconomicRevisionWork(ctx, rev2, "worker-verify", time.Minute); err != nil || got {
		t.Fatalf("post-interleave rev-2 claim = (%v, %v), want held/completed", got, err)
	}
}

// TestRefinement82ConcurrentOutOfOrderRevisionsConverge races rev-3 and
// rev-2 results for one head through a synchronized start gate, proving
// late/early arrival order cannot corrupt the head: both appends succeed,
// and both commit orders converge on rev 3 with the correction hash. A rev-2
// commit after rev 3 is a stale no-op, never a rollback (Req 4.2, 4.5, 6.4).
func TestRefinement82ConcurrentOutOfOrderRevisionsConverge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement82-barrier-order-%d", time.Now().UnixNano())
	billingStore := openRefinement52BillingStore(t, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	o1 := refinement52ProviderObservation(storeID, callID, 1, "order-o1", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	o2 := refinement52ProviderObservation(storeID, callID, 2, "order-o2", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	o3 := refinement52ProviderObservation(storeID, callID, 3, "order-o3", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	rev2 := refinement82ProviderWork(t, ctx, builder, o1, o2)
	rev3 := refinement82ProviderWork(t, ctx, builder, o1, o2, o3)
	if rev2.HeadKey != rev3.HeadKey {
		t.Fatalf("order head keys differ: %q vs %q", rev2.HeadKey, rev3.HeadKey)
	}
	// Index 0 appends the higher revision first; index 1 appends the lower
	// revision first. The barrier makes the commit order overlap; both orders
	// must converge on rev 3.
	orders := [][2]corebilling.EconomicRevisionWork{{rev3, rev2}, {rev2, rev3}}
	for _, order := range orders {
		errs := refinement82ReleaseBarrier(t, 2, func(i int) error {
			work := order[i]
			return billingStore.AppendEconomicRevisionResult(ctx, work, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work)})
		})
		for i, err := range errs {
			if err != nil {
				t.Fatalf("out-of-order append %d: %v", i, err)
			}
		}
		head, err := billingStore.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, rev3.HeadKey)
		if err != nil {
			t.Fatal(err)
		}
		if head.EvidenceRevision != 3 || head.InputSetHash != rev3.InputSetHash {
			t.Fatalf("head after out-of-order race = %+v, want rev 3 with correction hash", head)
		}
	}
}
