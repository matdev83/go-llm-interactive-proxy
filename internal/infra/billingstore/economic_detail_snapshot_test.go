package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Finding 7 RED contract: an economic-detail continuation is only valid while
// the full-scope snapshot it was issued against is unchanged. A resumed A-leg
// call whose observation sorts before the cursor must never be silently omitted
// while newer totals appear; a late valuation/head/reconciliation/allocation
// correction must leave repeated full-scope facts identical or invalidate the
// continuation with an explicit stale/restart classification. Page one binds a
// deterministic bounded fingerprint of the full observation membership and
// every repeated full-scope fact; continuing verifies it under the same bounded
// reads.

// edSnapshotPageOne returns the first authenticated page for a query with a
// one-observation page so a continuation is always issued.
func edSnapshotPageOne(t *testing.T, store *DurableStore, query billing.EconomicDetailQuery) billing.EconomicDetail {
	t.Helper()
	page := query
	page.Limit = 1
	first, err := store.QueryEconomicDetail(context.Background(), page)
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor, "a multi-observation scope must issue a continuation")
	return first
}

// edSnapshotContinue replays the page-one cursor against the same scope.
func edSnapshotContinue(t *testing.T, store *DurableStore, query billing.EconomicDetailQuery, first billing.EconomicDetail) error {
	t.Helper()
	next := query
	next.Limit = 1
	next.Cursor = first.NextCursor
	_, err := store.QueryEconomicDetail(context.Background(), next)
	return err
}

// TestQueryEconomicDetailContinuationInvalidatesOnResumedCallInsertedAhead
// proves a resumed call added between pages cannot be silently skipped when its
// observation sorts before the issued position while newer totals are present.
func TestQueryEconomicDetailContinuationInvalidatesOnResumedCallInsertedAhead(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-snap-resume", "USD")
	aLegID := "a-ed-snap-resume"

	callOne := edTestCallID(t)
	subjectOne := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, aLegID, callOne.String(), "b-ed-snap-one")
	obsOne := edTestObservation(t, "obs-snap-b-one", metering.OriginLocal, "stream-b", 1, subjectOne,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil)
	obsTwo := edTestObservation(t, "obs-snap-b-two", metering.OriginLocal, "stream-b", 2, subjectOne,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "11")}, nil)
	edSetupCall(t, store, account.ID, callOne, aLegID, edTestLeg(t, "b-ed-snap-one", obsOne, obsTwo))

	query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID}
	first := edSnapshotPageOne(t, store, query)
	require.Equal(t, "obs-snap-b-one", first.Observations[0].ID)

	// An unchanged A-leg snapshot must continue cleanly: the rolling summary is
	// a deterministic durable projection, so it must not spuriously invalidate.
	require.NoError(t, edSnapshotContinue(t, store, query, first))

	// The A-leg resumes: a new BillingCallID/B-leg contributes an observation
	// that sorts before the issued position. Traversal must not silently omit
	// it while the next page reports changed totals.
	callTwo := edTestCallID(t)
	subjectTwo := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, aLegID, callTwo.String(), "b-ed-snap-two")
	obsAhead := edTestObservation(t, "obs-snap-a-one", metering.OriginLocal, "stream-a", 1, subjectTwo,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "12")}, nil)
	edSetupCall(t, store, account.ID, callTwo, aLegID, edTestLeg(t, "b-ed-snap-two", obsAhead))

	err := edSnapshotContinue(t, store, query, first)
	require.ErrorIs(t, err, economics.ErrOperatorCursorStale,
		"a resumed call inserted ahead of the cursor must invalidate the continuation, not be silently skipped")
}

// TestQueryEconomicDetailContinuationInvalidatesOnLateCorrections proves each
// late correction of a repeated full-scope fact invalidates the continuation
// instead of changing supposedly frozen facts on the next page.
func TestQueryEconomicDetailContinuationInvalidatesOnLateCorrections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("valuation", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		account := edTestAccount(t, store, "ed-snap-val", "USD")
		callID, _ := edCompleteCall(t, store, account.ID, "a-ed-snap-val", "edSV")
		query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-snap-val"}
		first := edSnapshotPageOne(t, store, query)

		subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-snap-val", callID.String(), "b-edSV")
		extra := edTestObservation(t, "obs-snap-val-rev", metering.OriginLocal, "stream-edSV-rev", 1, subject,
			[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "20")}, nil)
		ref := edObservationRef(t, store.StoreID(), extra)
		revised := edTestValuation(t, "val-edSV-e-rev", economics.BasisLocalExpected, subject, []metering.ObservationRef{ref}, edTestCurrencyTotal(t, "USD", "9.99"))
		revised.CreatedAt = time.Unix(1_700_099_000, 0).UTC()
		require.NoError(t, store.AppendValuation(ctx, revised))

		require.ErrorIs(t, edSnapshotContinue(t, store, query, first), economics.ErrOperatorCursorStale)
	})

	t.Run("head", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		account := edTestAccount(t, store, "ed-snap-head", "USD")
		callID, _ := edCompleteCall(t, store, account.ID, "a-ed-snap-head", "edSH")
		query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-snap-head"}
		first := edSnapshotPageOne(t, store, query)

		subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-snap-head", callID.String(), "b-edSH")
		edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-snap-p",
			billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

		require.ErrorIs(t, edSnapshotContinue(t, store, query, first), economics.ErrOperatorCursorStale)
	})

	t.Run("reconciliation", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		account := edTestAccount(t, store, "ed-snap-rec", "USD")
		callID, _ := edCompleteCall(t, store, account.ID, "a-ed-snap-rec", "edSR")
		query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-snap-rec"}
		subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-snap-rec", callID.String(), "b-edSR")
		base := orTestRetentionForSubject(t, store.StoreID(), "rec-snap-base", 1, subject)
		require.NoError(t, store.AppendReconciliationRetention(ctx, base))
		first := edSnapshotPageOne(t, store, query)

		corrected := orTestRetentionForSubject(t, store.StoreID(), "rec-snap-corrected", 1, subject)
		corrected.CreatedAt = time.Unix(1_700_099_500, 0).UTC()
		require.NoError(t, store.AppendReconciliationRetention(ctx, corrected))

		require.ErrorIs(t, edSnapshotContinue(t, store, query, first), economics.ErrOperatorCursorStale)
	})

	t.Run("allocation", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		account := edTestAccount(t, store, "ed-snap-alloc", "USD")
		callID := edTestCallID(t)
		aLegID := "a-ed-snap-alloc"
		subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, aLegID, callID.String(), "b-ed-snap-alloc")
		obs := edTestObservation(t, "obs-snap-alloc", metering.OriginLocal, "stream-alloc", 1, subject,
			[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil)
		obsTwo := edTestObservation(t, "obs-snap-alloc-two", metering.OriginLocal, "stream-alloc", 2, subject,
			[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "11")}, nil)
		edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-ed-snap-alloc", obs, obsTwo))

		query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID}
		target := economics.AllocationTarget{
			TargetID: "t-b-ed-snap-alloc",
			Target:   edTestBLegSubject(store.StoreID(), edAllocTestTenant, account.ID, aLegID, callID.String(), "b-ed-snap-alloc"),
			Weight:   economics.AllocationFraction{Numerator: "1", Denominator: "1"},
		}
		record := edTestAllocationQuantityEnvelope(t, store, "alloc-snap-base", account.ID,
			[]economics.AllocationTarget{target})
		require.NoError(t, store.AppendAllocation(ctx, record))
		first := edSnapshotPageOne(t, store, query)
		require.Len(t, first.Coverage.Allocations, 1)

		correction := edTestAllocationQuantityEnvelope(t, store, "alloc-snap-corrected", account.ID,
			[]economics.AllocationTarget{target})
		correction.CreatedAt = time.Unix(1_700_099_700, 0).UTC()
		require.NoError(t, store.AppendAllocation(ctx, correction))

		require.ErrorIs(t, edSnapshotContinue(t, store, query, first), economics.ErrOperatorCursorStale)
	})
}

// TestQueryEconomicDetailContinuationUnchangedSurvivesReopen keeps the positive
// contract: an unchanged snapshot continues exactly once and survives a durable
// reopen, so the snapshot boundary does not spuriously reject valid traversal.
func TestQueryEconomicDetailContinuationUnchangedSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dsn := operatorCursorDSN(t, "economic-detail-snapshot-reopen.db")

	firstBun, firstSQL := openOperatorCursorBun(t, dsn)
	first, err := NewDurableStore(ctx, firstBun, Config{StoreID: "ed-snapshot-reopen"})
	require.NoError(t, err)
	account := edTestAccount(t, first, "ed-snapshot-reopen", "USD")
	callID := edTestCallID(t)
	subject := edTestBLegSubject(first.StoreID(), "tenant-ed", account.ID, "a-ed-snapshot-reopen", callID.String(), "b-ed-snapshot-reopen")
	var observations []metering.Observation
	for i := range 3 {
		observations = append(observations, edTestObservation(t, "obs-snapshot-reopen-"+string(rune('a'+i)), metering.OriginLocal, "stream-reopen", uint64(i+1), subject,
			[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil))
	}
	edSetupCall(t, first, account.ID, callID, "a-ed-snapshot-reopen", edTestLeg(t, "b-ed-snapshot-reopen", observations...))

	query := billing.EconomicDetailQuery{StoreID: first.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-snapshot-reopen", Limit: 1}
	firstPage, err := first.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.NotEmpty(t, firstPage.NextCursor)
	require.NoError(t, first.Close())
	_ = firstSQL.Close()

	reopenedBun, reopenedSQL := openOperatorCursorBun(t, dsn)
	reopened, err := NewDurableStore(ctx, reopenedBun, Config{StoreID: "ed-snapshot-reopen"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close(); _ = reopenedSQL.Close() })

	secondQuery := query
	secondQuery.Cursor = firstPage.NextCursor
	secondPage, err := reopened.QueryEconomicDetail(ctx, secondQuery)
	require.NoError(t, err, "an unchanged snapshot must continue across a durable reopen")
	require.Len(t, secondPage.Observations, 1)
	require.NotEqual(t, firstPage.Observations[0].ID, secondPage.Observations[0].ID)
}
