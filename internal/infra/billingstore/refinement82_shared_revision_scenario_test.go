package billingstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

// refinement82SharedSession is one open generation of the durable billing +
// journal stores for a store identity. Close releases both handles.
type refinement82SharedSession struct {
	Billing *DurableStore
	Journal *journalstore.DurableStore
	Close   func()
}

// refinement82SharedHarness opens fresh durable identities and reopens the
// same identity after a full close (restart). Every generation opens FRESH
// database handles through the existing production constructors and
// migrations — never reuses a closed handle — and each session Close
// releases exactly the handles its own generation opened. All scenario
// assertions use public readers and dialect-portable inputs only (no
// hand-rolled SQL, no hand-rolled transactions); the stores own their
// transaction isolation. Opens counts DB-handle opens for the opener
// regression (two per generation: billing + journal).
type refinement82SharedHarness struct {
	Open   func(t *testing.T, storeID string) refinement82SharedSession
	Reopen func(t *testing.T, storeID string, prev refinement82SharedSession) refinement82SharedSession
	Opens  func() int64
}

// refinement82SQLiteSharedHarness stores each identity in two files under a
// per-identity TempDir; every generation opens fresh file-backed handles and
// Reopen closes the previous generation first.
func refinement82SQLiteSharedHarness() refinement82SharedHarness {
	type paths struct{ billing, journal string }
	dirs := make(map[string]paths)
	var opens int64
	openGeneration := func(t *testing.T, storeID string, p paths) refinement82SharedSession {
		t.Helper()
		billingStore, closeBilling := openRefinement82FileBillingStore(t, p.billing, storeID)
		journalStore := openRefinement52Journal(t, p.journal, storeID)
		opens += 2
		return refinement82SharedSession{
			Billing: billingStore,
			Journal: journalStore,
			Close: func() {
				closeBilling()
				_ = journalStore.Close()
			},
		}
	}
	return refinement82SharedHarness{
		Open: func(t *testing.T, storeID string) refinement82SharedSession {
			t.Helper()
			dir := t.TempDir()
			p := paths{billing: filepath.Join(dir, "billing.sqlite"), journal: filepath.Join(dir, "metering.sqlite")}
			dirs[storeID] = p
			return openGeneration(t, storeID, p)
		},
		Reopen: func(t *testing.T, storeID string, prev refinement82SharedSession) refinement82SharedSession {
			t.Helper()
			prev.Close()
			p, ok := dirs[storeID]
			if !ok {
				t.Fatalf("unknown shared store identity %q", storeID)
			}
			return openGeneration(t, storeID, p)
		},
		Opens: func() int64 { return opens },
	}
}

// TestRefinement82SharedHarnessReopenYieldsFreshHandles is the opener
// regression for production restart modeling: after a full close, reopen
// must return fresh usable handles (open count grows, readiness pings pass,
// and real reads/writes succeed on the reopened generation). Against a
// harness that reuses closed handles, the readiness ping and every
// post-reopen operation fail.
func TestRefinement82SharedHarnessReopenYieldsFreshHandles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	harness := refinement82SQLiteSharedHarness()
	storeID := fmt.Sprintf("refinement82-opener-%d", time.Now().UnixNano())
	sess := harness.Open(t, storeID)
	if got := harness.Opens(); got != 2 {
		t.Fatalf("opens after Open = %d, want 2 (billing + journal)", got)
	}
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := refinement52ClosedLeg(storeID, callID)
	if err := sess.Billing.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	o1 := refinement52ProviderObservation(storeID, callID, 1, "opener-o1", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	if err := sess.Journal.AppendObservations(ctx, []metering.Observation{o1}); err != nil {
		t.Fatal(err)
	}
	sess = harness.Reopen(t, storeID, sess)
	defer sess.Close()
	if got := harness.Opens(); got != 4 {
		t.Fatalf("opens after Reopen = %d, want 4 (fresh handles per generation)", got)
	}
	if err := sess.Billing.CheckReadiness(ctx); err != nil {
		t.Fatalf("reopened billing handle not usable: %v", err)
	}
	if err := sess.Journal.CheckReadiness(ctx); err != nil {
		t.Fatalf("reopened journal handle not usable: %v", err)
	}
	legKey, err := corebilling.CallLegUsageKey(callID, leg.BLegID)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sess.Billing.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatalf("post-reopen leg read: %v", err)
	}
	if sealed.Fingerprint == "" {
		t.Fatal("post-reopen leg has no fingerprint")
	}
	if _, err := sess.Journal.GetObservation(ctx, o1.ID, o1.Revision); err != nil {
		t.Fatalf("post-reopen observation read: %v", err)
	}
	o2 := refinement52ProviderObservation(storeID, callID, 2, "opener-o2", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	if err := sess.Journal.AppendObservations(ctx, []metering.Observation{o2}); err != nil {
		t.Fatalf("post-reopen observation write: %v", err)
	}
	if _, err := sess.Journal.GetObservation(ctx, o2.ID, o2.Revision); err != nil {
		t.Fatalf("post-reopen observation readback: %v", err)
	}
}

// runRefinement82SharedRevisionScenario certifies, identically on every
// dialect, durable rev1/rev2/rev3 identity/fence/idempotency, restart
// readback, and deterministic barrier concurrency for one B-leg subject:
//
//   - a sealed terminal B-leg plus pre-terminal (rev1), finalizer (rev2),
//     and statement (rev3) observations advance one provider head rev1->2->3
//     with exact replay idempotency at every stage;
//   - close/reopen preserves heads, legs, observations, outbox, and
//     valuations; a post-restart correction (rev4) advances the same head;
//   - a synchronized 8-way exact-replay burst keeps one identity;
//   - a synchronized 8-way worker-claim race elects exactly one winner;
//   - a synchronized superset-vs-incomparable race yields exactly one
//     success plus one fence with no partial persistence.
func runRefinement82SharedRevisionScenario(t *testing.T, ctx context.Context, harness refinement82SharedHarness) {
	t.Helper()
	storeID := fmt.Sprintf("refinement82-shared-%d", time.Now().UnixNano())
	sess := harness.Open(t, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	buildProviderWork := func(observations ...metering.Observation) corebilling.EconomicRevisionWork {
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
	appendResult := func(work corebilling.EconomicRevisionWork) {
		t.Helper()
		if err := sess.Billing.AppendEconomicRevisionResult(ctx, work, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, work)}); err != nil {
			t.Fatalf("append economic result: %v", err)
		}
	}
	requireHead := func(headKey string, revision uint64, hash string) corebilling.EconomicValuationHead {
		t.Helper()
		head, err := sess.Billing.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, headKey)
		if err != nil {
			t.Fatalf("read provider head: %v", err)
		}
		if head.EvidenceRevision != revision || head.InputSetHash != hash {
			t.Fatalf("provider head = %+v, want revision %d/hash %q", head, revision, hash)
		}
		return head
	}
	valuationCount := func() int {
		t.Helper()
		page, err := sess.Billing.ListValuations(ctx, ValuationQuery{StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: "b-leg", Limit: 100})
		if err != nil {
			t.Fatalf("list valuations: %v", err)
		}
		return len(page.Valuations)
	}
	observationCount := func() int {
		t.Helper()
		page, err := sess.Journal.ListObservations(ctx, journalstore.ObservationQuery{StoreID: storeID, StreamID: "refinement82-stream", Limit: 100})
		if err != nil {
			t.Fatalf("list observations: %v", err)
		}
		return len(page.Observations)
	}
	pendingOutbox := func() int {
		t.Helper()
		pending, err := sess.Journal.ListPendingObservationOutbox(ctx, 100)
		if err != nil {
			t.Fatalf("list pending outbox: %v", err)
		}
		return len(pending)
	}

	leg := refinement52ClosedLeg(storeID, callID)
	if err := sess.Billing.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("terminal leg append: %v", err)
	}
	o1 := refinement52ProviderObservation(storeID, callID, 1, "shared-o1", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	o1.ID = "refinement82-shared-o1"
	o1.SourceEventKey = "refinement82-shared-source-o1"
	o1.StreamID = "refinement82-stream"
	if err := sess.Journal.AppendObservations(ctx, []metering.Observation{o1}); err != nil {
		t.Fatalf("pre-terminal checkpoint: %v", err)
	}
	appender, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: sess.Billing,
		Sink: journalstore.NewObservationSinkWithOutbox(sess.Journal), Resolver: sess.Journal,
	})
	if err != nil {
		t.Fatalf("late appender: %v", err)
	}
	finalizer := refinement52ProviderObservation(storeID, callID, 2, "shared-final", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	finalizer.StreamID = "refinement82-stream"
	statement := refinement52StatementObservation(storeID, callID, 3)
	statement.StreamID = "refinement82-stream"
	lateIdentity := refinement52LateIdentity(storeID, callID)
	if err := appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicProviderFinalizer, Identity: lateIdentity, Observation: finalizer}); err != nil {
		t.Fatalf("finalizer append: %v", err)
	}
	if err := appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicStatement, Identity: lateIdentity, Observation: statement}); err != nil {
		t.Fatalf("statement append: %v", err)
	}

	w1 := buildProviderWork(o1)
	appendResult(w1)
	head1 := requireHead(w1.HeadKey, 1, w1.InputSetHash)
	w2 := buildProviderWork(o1, finalizer)
	if w2.HeadKey != w1.HeadKey {
		t.Fatalf("rev-2 head key %q diverged from rev-1 %q", w2.HeadKey, w1.HeadKey)
	}
	appendResult(w2)
	head2 := requireHead(w2.HeadKey, 2, w2.InputSetHash)
	if head2.HeadVersion != head1.HeadVersion+1 {
		t.Fatalf("rev-2 head version = %d, want %d", head2.HeadVersion, head1.HeadVersion+1)
	}
	w3 := buildProviderWork(o1, finalizer, statement)
	appendResult(w3)
	requireHead(w3.HeadKey, 3, w3.InputSetHash)
	for _, replay := range []corebilling.EconomicRevisionWork{w1, w2, w3} {
		if err := sess.Billing.AppendEconomicRevisionResult(ctx, replay, corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, replay)}); err != nil {
			t.Fatalf("exact replay: %v", err)
		}
	}
	requireHead(w3.HeadKey, 3, w3.InputSetHash)
	if got := valuationCount(); got != 3 {
		t.Fatalf("valuations after replay = %d, want 3", got)
	}
	if got := observationCount(); got != 3 {
		t.Fatalf("durable observations = %d, want o1 + finalizer + statement", got)
	}
	if got := pendingOutbox(); got != 2 {
		t.Fatalf("pending outbox = %d, want finalizer + statement", got)
	}
	legKey, err := corebilling.CallLegUsageKey(callID, leg.BLegID)
	if err != nil {
		t.Fatal(err)
	}
	sealedBefore, err := sess.Billing.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatal(err)
	}

	// Restart: close both handles and reopen the same identity through the
	// production constructors; every durable fact must read back stable.
	sess = harness.Reopen(t, storeID, sess)
	defer sess.Close()
	requireHead(w3.HeadKey, 3, w3.InputSetHash)
	sealedAfter, err := sess.Billing.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatalf("sealed leg lost across restart: %v", err)
	}
	if sealedAfter.Fingerprint != sealedBefore.Fingerprint {
		t.Fatal("sealed leg fingerprint changed across restart")
	}
	for _, want := range []struct {
		id  string
		rev uint64
	}{{o1.ID, 1}, {finalizer.ID, 2}, {statement.ID, 3}} {
		if _, err := sess.Journal.GetObservation(ctx, want.id, want.rev); err != nil {
			t.Fatalf("observation %s/%d lost across restart: %v", want.id, want.rev, err)
		}
	}
	if got := observationCount(); got != 3 {
		t.Fatalf("observations after restart = %d, want 3", got)
	}
	if got := pendingOutbox(); got != 2 {
		t.Fatalf("pending outbox after restart = %d, want 2", got)
	}
	if got := valuationCount(); got != 3 {
		t.Fatalf("valuations after restart = %d, want 3", got)
	}

	// Post-restart correction advances the same head to rev 4.
	appender2, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: sess.Billing,
		Sink: journalstore.NewObservationSinkWithOutbox(sess.Journal), Resolver: sess.Journal,
	})
	if err != nil {
		t.Fatalf("restarted late appender: %v", err)
	}
	correction := refinement52ProviderObservation(storeID, callID, 4, "shared-correction", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	correction.StreamID = "refinement82-stream"
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
	if err := appender2.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{Kind: coremetering.LateEconomicCorrection, Identity: lateIdentity, Observation: correction}); err != nil {
		t.Fatalf("post-restart correction: %v", err)
	}
	w4 := buildProviderWork(o1, finalizer, statement, correction)
	if w4.HeadKey != w1.HeadKey || w4.EvidenceRevision != 4 {
		t.Fatalf("correction work = %+v, want same head at revision 4", w4)
	}
	appendResult(w4)
	requireHead(w4.HeadKey, 4, w4.InputSetHash)
	if got := valuationCount(); got != 4 {
		t.Fatalf("valuations after correction = %d, want 4", got)
	}
	if got := pendingOutbox(); got != 3 {
		t.Fatalf("pending outbox after correction = %d, want 3", got)
	}
	legs, err := sess.Billing.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 1 || legs[0].Fingerprint != sealedBefore.Fingerprint {
		t.Fatal("correction changed the closed B-leg row")
	}

	// Barrier 1: synchronized exact-replay burst keeps one identity and one
	// head version; every caller succeeds.
	for i, err := range refinement82ReleaseBarrier(t, 8, func(int) error {
		return appender2.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
			Kind: coremetering.LateEconomicProviderFinalizer, Identity: lateIdentity, Observation: finalizer,
		})
	}) {
		if err != nil {
			t.Fatalf("shared burst replay %d: %v", i, err)
		}
	}
	if got := observationCount(); got != 4 {
		t.Fatalf("observations after burst = %d, want 4", got)
	}
	if got := pendingOutbox(); got != 3 {
		t.Fatalf("pending outbox after burst = %d, want 3", got)
	}

	// Barrier 2: synchronized worker-claim race elects exactly one winner.
	claimWork := w4
	type claimOutcome struct {
		acquired bool
		fence    uint64
	}
	claimOutcomes := make([]claimOutcome, 8)
	for i, err := range refinement82ReleaseBarrier(t, 8, func(i int) error {
		claim, acquired, err := sess.Billing.ClaimEconomicRevisionWork(ctx, claimWork, fmt.Sprintf("shared-worker-%d", i), time.Minute)
		if err != nil {
			return err
		}
		claimOutcomes[i] = claimOutcome{acquired: acquired, fence: claim.Fence}
		return nil
	}) {
		if err != nil {
			t.Fatalf("shared claim race %d: %v", i, err)
		}
	}
	var claimWinners []int
	for i, outcome := range claimOutcomes {
		if outcome.acquired {
			if outcome.fence == 0 {
				t.Fatalf("shared claimant %d acquired with zero fence", i)
			}
			claimWinners = append(claimWinners, i)
		}
	}
	if len(claimWinners) != 1 {
		t.Fatalf("shared claim winners = %v, want exactly one", claimWinners)
	}
	if err := sess.Billing.CompleteEconomicRevisionWork(ctx, claimWork, corebilling.EconomicRevisionWorkClaim{Owner: fmt.Sprintf("shared-worker-%d", claimWinners[0]), Fence: claimOutcomes[claimWinners[0]].fence}); err != nil {
		t.Fatalf("shared winner complete: %v", err)
	}

	// Barrier 3: superset (guaranteed advance) vs incomparable (guaranteed
	// fence) on a fresh subject head — deterministic winner and error class.
	callID2, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	fa := refinement52ProviderObservation(storeID, callID2, 1, "shared-fence-a", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	fb := refinement52ProviderObservation(storeID, callID2, 3, "shared-fence-b", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	fc := refinement52ProviderObservation(storeID, callID2, 2, "shared-fence-c", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	fd := refinement52ProviderObservation(storeID, callID2, 2, "shared-fence-d", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	baseWork := buildProviderWork(fa, fb)
	appendResult(baseWork)
	raceSuperset := buildProviderWork(fa, fb, fc)
	raceFenced := buildProviderWork(fb, fd)
	if raceSuperset.HeadKey != baseWork.HeadKey || raceFenced.HeadKey != baseWork.HeadKey {
		t.Fatal("fence race head keys diverged")
	}
	raceWorks := []corebilling.EconomicRevisionWork{raceSuperset, raceFenced}
	raceErrs := refinement82ReleaseBarrier(t, 2, func(i int) error {
		return sess.Billing.AppendEconomicRevisionResult(ctx, raceWorks[i], corebilling.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, raceWorks[i])})
	})
	if raceErrs[0] != nil {
		t.Fatalf("shared superset branch error = %v, want nil", raceErrs[0])
	}
	if !errors.Is(raceErrs[1], corebilling.ErrEconomicRevisionFence) {
		t.Fatalf("shared incomparable branch error = %v, want ErrEconomicRevisionFence", raceErrs[1])
	}
	finalHead, err := sess.Billing.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, baseWork.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if finalHead.InputSetHash != raceSuperset.InputSetHash {
		t.Fatalf("shared fence-race head = %+v, want superset hash", finalHead)
	}
	if got := valuationCount(); got != 6 {
		t.Fatalf("shared valuations at end = %d, want 4 + base + superset", got)
	}
	finalLegs, err := sess.Billing.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalLegs) != 1 || finalLegs[0].Fingerprint != sealedBefore.Fingerprint {
		t.Fatal("shared scenario changed the closed B-leg row")
	}
}

// TestRefinement82SharedRevisionScenarioSQLite runs the shared
// revision/restart/concurrency scenario on file-backed SQLite stores with
// real migrations through the production constructors.
func TestRefinement82SharedRevisionScenarioSQLite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runRefinement82SharedRevisionScenario(t, ctx, refinement82SQLiteSharedHarness())
}
