//go:build integration

package billingstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// TestQueryEconomicDetailPostgresDirect proves the 16.1B durable detail
// contract on direct PostgreSQL with the same scoped loads, E/Q/P/R planes,
// A-leg gathering, scope rejection and pagination behavior as SQLite. The
// adapter uses one placeholder SQL shape through Bun on both dialects and
// adds no migration, so parity is structural as well as behavioral.
func TestQueryEconomicDetailPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-pg", "USD")
	callID, valuationIDs := edCompleteCall(t, store, account.ID, "a-ed-pg", "edPG")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-pg",
	})
	require.NoError(t, err)
	require.Len(t, got.Observations, 3)
	for _, id := range valuationIDs {
		found := false
		for _, valuation := range got.Valuations {
			if valuation.ID == id {
				found = true
			}
		}
		require.True(t, found, "valuation %q must be loaded on PostgreSQL", id)
	}

	second, _ := edCompleteCall(t, store, account.ID, "a-ed-pg", "edPG2")
	aleg, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: "a-ed-pg",
	})
	require.NoError(t, err)
	require.Len(t, aleg.Observations, 6)
	callIDs := map[string]bool{}
	for _, observation := range aleg.Observations {
		callIDs[observation.Subject.BillingCallID] = true
	}
	require.True(t, callIDs[callID.String()])
	require.True(t, callIDs[second.String()])

	// Finding 3A parity: both calls' E/Q/P/R streams survive on PostgreSQL; a
	// basis-only collapse would keep four, all from the lexically later call.
	require.Len(t, aleg.Valuations, 8)
	valuationsByCall := map[string]int{}
	for _, valuation := range aleg.Valuations {
		valuationsByCall[valuation.Subject.BillingCallID]++
	}
	require.Equal(t, 4, valuationsByCall[callID.String()])
	require.Equal(t, 4, valuationsByCall[second.String()])

	_, err = store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: "other-account", BillingCallID: callID.String(),
	})
	require.ErrorIs(t, err, billing.ErrReportNotFound)

	paged, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-pg", Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, paged.Observations, 2)
	require.True(t, paged.Truncated)
	require.NotNil(t, paged.ValuationFor(economics.BasisLocalExpected))

	// Finding 2 parity: one shared conserved envelope carries both an in-scope
	// B-leg and a same-account foreign-call B-leg. Only the authoritative member
	// may appear in the requested call scope on PostgreSQL.
	amount, err := metering.ParseDecimal("8")
	require.NoError(t, err)
	allocation := economics.AllocationRecord{
		ID: "alloc-ed-pg", Version: 1,
		SourceSubject: metering.SubjectRef{Kind: metering.SubjectResource, StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, ResourceID: "shared-pg", PeriodID: "2026-09"},
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "pg-in-scope", Target: edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-pg", callID.String(), "b-edPG"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "2"}},
			{TargetID: "pg-foreign-call", Target: edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-pg", second.String(), "b-edPG2"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "2"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, allocation))
	isolated, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-pg",
	})
	require.NoError(t, err)
	require.Len(t, isolated.Coverage.Allocations, 1)
	require.Equal(t, "pg-in-scope", isolated.Coverage.Allocations[0].TargetID)
	require.Equal(t, "b-edPG", isolated.Coverage.Allocations[0].Target.BLegID)
	require.Equal(t, callID.String(), isolated.Coverage.Allocations[0].Target.BillingCallID)
}

// TestQueryEconomicDetailReconciliationPostgresDirect proves Finding 3B
// multi-subject reconciliation parity on direct PostgreSQL: independent B-leg
// subjects each keep their own comparison instead of a first-found result.
func TestQueryEconomicDetailReconciliationPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-recon-pg", "USD")
	callID := edTestCallID(t)
	legOne, legTwo := edTestLeg(t, "b-pg-recon-1"), edTestLeg(t, "b-pg-recon-2")
	legTwo.AttemptSeq = 2
	edSetupCall(t, store, account.ID, callID, "a-ed-recon-pg", legOne, legTwo)
	edAppendRetention(t, store, "recon-pg-1", account.ID, "a-ed-recon-pg", callID.String(), "b-pg-recon-1", "100", "110", 1, time.Unix(1_700_020_200, 0).UTC())
	edAppendRetention(t, store, "recon-pg-2", account.ID, "a-ed-recon-pg", callID.String(), "b-pg-recon-2", "100", "120", 1, time.Unix(1_700_020_300, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-recon-pg",
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 2)
	require.Equal(t, "b-pg-recon-1", got.Reconciliations[0].Subject.BLegID)
	require.Equal(t, "b-pg-recon-2", got.Reconciliations[1].Subject.BLegID)
	require.NotNil(t, got.Quantity)
}

// TestQueryEconomicDetailSelectedPostingPostgresDirect proves Finding 4A
// selection/posting parity on direct PostgreSQL: one frozen final posted head
// yields an exact selected subtotal and a computable margin, while two
// independent heads stay explicitly ambiguous and are never summed.
func TestQueryEconomicDetailSelectedPostingPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-head-pg", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-head-pg", "edHPG")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-head-pg", callID.String(), "b-edHPG")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-pg",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-head-pg",
	})
	require.NoError(t, err)
	require.Len(t, got.Heads, 1)
	require.NotNil(t, got.Totals.SelectedAmount)
	require.Equal(t, billing.OperatorCostSelectionStatusFinal, got.Totals.SelectedStatus)
	require.Equal(t, 1, got.Totals.SelectedHeadCount)
	require.False(t, got.Totals.SelectedAmbiguous)
	require.Equal(t, billing.SelectedCostPostingApplied, got.Heads[0].PostingState)
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)

	// The exact durable posting_state column, not an inferred state, is
	// exposed on PostgreSQL too.
	edTestSetHeadPostingState(t, store, account.ID, callID, "head-ed-pg", string(billing.SelectedCostPostingReplayed))
	replayed, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-head-pg",
	})
	require.NoError(t, err)
	require.Len(t, replayed.Heads, 1)
	require.Equal(t, billing.SelectedCostPostingReplayed, replayed.Heads[0].PostingState)

	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-pg-2",
		billing.OperatorCostBasisQ, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.10")
	ambiguous, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-head-pg",
	})
	require.NoError(t, err)
	require.Len(t, ambiguous.Heads, 2)
	require.True(t, ambiguous.Totals.SelectedAmbiguous)
	require.Nil(t, ambiguous.Totals.SelectedAmount)
	require.False(t, ambiguous.Margin.Complete)
	require.Equal(t, "ambiguous_selected", ambiguous.Margin.Reason)
}

// TestQueryEconomicDetailMaterializationBoundPostgresDirect proves Finding 5B2
// parity on direct PostgreSQL: valuation history collapses to the latest
// revision per exact logical stream (including two B-leg attempt streams that
// share the persisted subject_id) at the database boundary while independent
// same-basis streams survive, and a head scope one row over the finite budget
// fails closed instead of being materialized.
func TestQueryEconomicDetailMaterializationBoundPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-bound-pg", "USD")

	base := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-bound-pg", "call-ed-bound-pg", "b-ed-bound-pg")
	attemptOne := base
	attemptOne.AttemptID = "att-ed-bound-pg-1"
	attemptOne.AttemptSeq = 1
	attemptTwo := base
	attemptTwo.AttemptID = "att-ed-bound-pg-2"
	attemptTwo.AttemptSeq = 2
	latestByAttempt := map[string]string{}
	for i, subject := range []metering.SubjectRef{attemptOne, attemptTwo} {
		for revision := 0; revision < 3; revision++ {
			ref := metering.ObservationRef{
				StoreID: store.StoreID(), ObservationID: fmt.Sprintf("obs-ed-bound-pg-%d-%03d", i, revision),
				Revision: 1, PayloadHash: strings.Repeat("a", 64),
			}
			valuation := edTestValuation(t, fmt.Sprintf("val-ed-bound-pg-%d-%03d", i, revision), economics.BasisLocalExpected, subject, []metering.ObservationRef{ref}, edTestCurrencyTotal(t, "USD", "1.00"))
			valuation.CreatedAt = time.Unix(1_700_060_000+int64(i*10+revision), 0).UTC()
			require.NoError(t, valuation.Validate())
			require.NoError(t, store.AppendValuation(ctx, valuation))
			latestByAttempt[subject.AttemptID] = valuation.ID
		}
	}
	subjects := []economicDetailSubject{{kind: metering.SubjectBLeg, id: "b-ed-bound-pg"}}
	for i := 0; i < 2; i++ {
		valuation, subject := edTestBoundValuation(t, store, account.ID, i, economics.BasisLocalExpected, time.Unix(1_700_060_100+int64(i), 0).UTC())
		require.NoError(t, store.AppendValuation(ctx, valuation))
		subjects = append(subjects, subject)
	}
	valuations, err := store.detailValuations(ctx, "tenant-ed", subjects)
	require.NoError(t, err)
	require.Len(t, valuations, 4, "two attempt-lineage streams plus two independent streams on PostgreSQL")
	for _, valuation := range valuations {
		if valuation.Subject.BLegID == "b-ed-bound-pg" {
			require.Equal(t, latestByAttempt[valuation.Subject.AttemptID], valuation.ID, "latest revision within each exact attempt stream wins")
		}
	}

	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-bound-pg", "edBPG")
	headSubject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-bound-pg", callID.String(), "b-edBPG")
	for i := 0; i <= billing.MaxEconomicDetailHeads; i++ {
		edTestPostSelectedHead(t, store, account.ID, callID, headSubject, fmt.Sprintf("head-ed-bpg-%04d", i),
			billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	}
	_, err = store.detailHeads(ctx, account.ID, []string{callID.String()})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}

// TestQueryEconomicDetailLegBoundPostgresDirect proves Finding 5B1 leg-record
// SQL bounds on direct PostgreSQL: the database-side LIMIT accepts exactly the
// finite maximum and fails closed one row over, so the same global budget and
// placeholder binding hold on both dialects.
func TestQueryEconomicDetailLegBoundPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-leg-pg", "USD")
	atBound := edTestCallID(t)
	overBound := edTestCallID(t)
	for callIndex, spec := range []struct {
		callID billing.BillingCallID
		count  int
	}{{atBound, economicDetailMaxLegRecords}, {overBound, economicDetailMaxLegRecords + 1}} {
		legs := make([]billing.CallLegUsageRecord, 0, spec.count)
		for i := 0; i < spec.count; i++ {
			leg := edTestLeg(t, fmt.Sprintf("b-ed-leg-pg-%d-%04d", callIndex, i))
			leg.AttemptSeq = i + 1
			legs = append(legs, leg)
		}
		edSetupCall(t, store, account.ID, spec.callID, fmt.Sprintf("a-ed-leg-pg-%d", callIndex), legs...)
	}

	atLimit, err := store.detailLegsByCalls(ctx, []string{atBound.String()})
	require.NoError(t, err)
	require.Len(t, atLimit, economicDetailMaxLegRecords)

	_, err = store.detailLegsByCalls(ctx, []string{overBound.String()})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}

// TestQueryEconomicDetailCursorPostgresDirect proves Finding 5A continuation
// parity on direct PostgreSQL: the authenticated per-store cursor traverses a
// multi-page observation scope exactly once, exposes a continuation token that
// agrees with Truncated, and rejects a re-encoded modified position.
func TestQueryEconomicDetailCursorPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-cursor-pg", "USD")
	callID := edTestCallID(t)
	var observations []metering.Observation
	var expected []string
	for seq := 1; seq <= 5; seq++ {
		id := "obs-cursor-pg-" + string(rune('0'+seq))
		subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-cursor-pg", callID.String(), "b-ed-cursor-pg")
		observations = append(observations, edTestObservation(t, id, metering.OriginLocal, "stream-cursor-pg", uint64(seq), subject,
			[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil))
		expected = append(expected, id)
	}
	edSetupCall(t, store, account.ID, callID, "a-ed-cursor-pg", edTestLeg(t, "b-ed-cursor-pg", observations...))

	query := billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-cursor-pg", Limit: 2,
	}
	var collected []string
	var cursor string
	pages := 0
	for {
		page := query
		page.Cursor = cursor
		got, err := store.QueryEconomicDetail(ctx, page)
		require.NoError(t, err)
		require.Equal(t, got.NextCursor != "", got.Truncated, "Truncated must agree with NextCursor on PostgreSQL")
		for _, observation := range got.Observations {
			collected = append(collected, observation.ID)
		}
		if got.NextCursor == "" {
			break
		}
		cursor = got.NextCursor
		pages++
		require.Less(t, pages, 20, "pagination must terminate instead of repeating the first page")
	}
	require.Equal(t, expected, collected, "every in-scope observation must be traversed exactly once in stable order")

	firstPage, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.NotEmpty(t, firstPage.NextCursor)
	forged := forgeOperatorCursorReencode(t, firstPage.NextCursor, func(cursor *operatorCursor) {
		cursor.Detail.ObservationID = "obs-forged-pg"
	})
	tampered := query
	tampered.Cursor = forged
	_, err = store.QueryEconomicDetail(ctx, tampered)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)
}

// TestQueryEconomicDetailSnapshotStalePostgresDirect proves Finding 7 parity on
// direct PostgreSQL: a resumed A-leg call whose observation sorts before the
// issued position invalidates the continuation instead of being silently
// omitted while changed totals are returned.
func TestQueryEconomicDetailSnapshotStalePostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-snap-pg", "USD")
	aLegID := "a-ed-snap-pg"
	callOne := edTestCallID(t)
	subjectOne := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, aLegID, callOne.String(), "b-ed-snap-pg-one")
	obsOne := edTestObservation(t, "obs-snap-pg-b1", metering.OriginLocal, "stream-b", 1, subjectOne,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil)
	obsTwo := edTestObservation(t, "obs-snap-pg-b2", metering.OriginLocal, "stream-b", 2, subjectOne,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "11")}, nil)
	edSetupCall(t, store, account.ID, callOne, aLegID, edTestLeg(t, "b-ed-snap-pg-one", obsOne, obsTwo))

	query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID, Limit: 1}
	firstPage, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.NotEmpty(t, firstPage.NextCursor)

	callTwo := edTestCallID(t)
	subjectTwo := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, aLegID, callTwo.String(), "b-ed-snap-pg-two")
	obsAhead := edTestObservation(t, "obs-snap-pg-a1", metering.OriginLocal, "stream-a", 1, subjectTwo,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "12")}, nil)
	edSetupCall(t, store, account.ID, callTwo, aLegID, edTestLeg(t, "b-ed-snap-pg-two", obsAhead))

	continued := query
	continued.Cursor = firstPage.NextCursor
	_, err = store.QueryEconomicDetail(ctx, continued)
	require.ErrorIs(t, err, economics.ErrOperatorCursorStale,
		"a resumed call inserted ahead of the cursor must invalidate the continuation on PostgreSQL")
}

// TestQueryEconomicDetailAllocationBoundPostgresDirect proves Finding 5B3
// parity on direct PostgreSQL: allocation discovery carries a database-side
// LIMIT against one global distinct-envelope budget with stable ordering, the
// exact envelope load is one indexed batch query, and a cumulative potential
// line output one target over the detail allocation bound fails closed before
// RollupAllocatedCostsDetailed can grow a slice.
func TestQueryEconomicDetailAllocationBoundPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-alloc-bound-pg", "USD")
	recorder := &edLegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	for i := 0; i < economicDetailMaxAllocationRecords; i++ {
		record := edTestAllocationQuantityEnvelope(t, store, fmt.Sprintf("alloc-ed-bound-pg-%03d", i), account.ID,
			[]economics.AllocationTarget{edTestAllocationBLegTarget(store, account.ID, "call-ed-bound-pg", "b-ed-bound-pg")})
		require.NoError(t, store.AppendAllocation(ctx, record))
	}
	atBound, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-bound-pg"}, []string{"b-ed-bound-pg"})
	require.NoError(t, err)
	require.Len(t, atBound, economicDetailMaxAllocationRecords)

	bounded := false
	for _, query := range recorder.queries {
		if strings.Contains(query, "billing_allocation_targets") && strings.Contains(query, "LIMIT") {
			bounded = true
		}
	}
	require.True(t, bounded, "allocation discovery SQL must carry a database-side LIMIT on PostgreSQL; recorded %v", recorder.queries)

	overEnvelope := edTestAllocationQuantityEnvelope(t, store, "alloc-ed-bound-pg-over", account.ID,
		[]economics.AllocationTarget{edTestAllocationBLegTarget(store, account.ID, "call-ed-bound-pg", "b-ed-bound-pg")})
	require.NoError(t, store.AppendAllocation(ctx, overEnvelope))
	_, err = store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-bound-pg"}, []string{"b-ed-bound-pg"})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)

	total := billing.MaxEconomicDetailAllocations + 1
	targets := make([]economics.AllocationTarget, 0, total)
	targets = append(targets, economics.AllocationTarget{
		TargetID: "t-pg-in-scope",
		Target:   edTestBLegSubject(store.StoreID(), edAllocTestTenant, account.ID, "a-ed-alloc-bounds", "call-ed-bound-pg", "b-ed-bound-pg-lines"),
		Weight:   economics.AllocationFraction{Numerator: "1", Denominator: fmt.Sprint(total)},
	})
	for i := 0; i < total-1; i++ {
		targets = append(targets, economics.AllocationTarget{
			TargetID: fmt.Sprintf("t-pg-foreign-%04d", i),
			Target:   edTestBLegSubject(store.StoreID(), edAllocTestTenant, account.ID, "a-ed-alloc-bounds", "call-ed-bound-pg-foreign", fmt.Sprintf("b-ed-bound-pg-foreign-%04d", i)),
			Weight:   economics.AllocationFraction{Numerator: "1", Denominator: fmt.Sprint(total)},
		})
	}
	lineEnvelope := edTestAllocationQuantityEnvelope(t, store, "alloc-ed-bound-pg-lines", account.ID, targets)
	require.NoError(t, store.AppendAllocation(ctx, lineEnvelope))
	_, err = store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-bound-pg"}, []string{"b-ed-bound-pg-lines"})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}

// TestQueryEconomicDetailProviderChargePostgresDirect proves Finding 1 parity on
// direct PostgreSQL: public call detail discovers the provider-charge streams
// attributable to the requested B-leg through the dialect-correct JSON
// extraction, preserves distinct provider accounts of one charge id, and never
// surfaces a foreign account's charge even when it reuses the in-scope B-leg id.
func TestQueryEconomicDetailProviderChargePostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-pc-pg", "USD")
	callID, baseIDs := edCompleteCall(t, store, account.ID, "a-ed-pc-pg", "edPCPG")
	bLegID := "b-edPCPG"
	callIDStr := callID.String()

	streams := []economics.Valuation{
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pc-pg-1", "pc-ed-pg-1", "pa-1", "a-ed-pc-pg", callIDStr, bLegID, "1.32", time.Unix(1_700_400_000, 0).UTC()),
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pc-pg-2", "pc-ed-pg-1", "pa-2", "a-ed-pc-pg", callIDStr, bLegID, "1.50", time.Unix(1_700_400_100, 0).UTC()),
	}
	for _, valuation := range streams {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	foreign := edTestAccount(t, store, "ed-pc-pg-foreign", "USD")
	require.NoError(t, store.AppendValuation(ctx, edTestProviderChargeValuation(t, store, foreign.ID,
		"val-ed-pc-pg-foreign", "pc-ed-pg-1", "pa-1", "a-ed-pc-pg", callIDStr, bLegID, "9.99", time.Unix(1_700_400_200, 0).UTC())))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callIDStr, ALegID: "a-ed-pc-pg",
	})
	require.NoError(t, err)
	byID := edTestValuationIndex(got.Valuations)
	for _, id := range baseIDs {
		require.Contains(t, byID, id)
	}
	require.Contains(t, byID, "val-ed-pc-pg-1")
	require.Contains(t, byID, "val-ed-pc-pg-2")
	require.NotContains(t, byID, "val-ed-pc-pg-foreign")
	require.Len(t, got.Valuations, len(baseIDs)+2)
}

// TestQueryEconomicDetailProviderChargeReconciliationPostgresDirect proves
// Finding 2 parity on direct PostgreSQL: a genuine provider-charge-kind
// reconciliation retention is discovered through the dialect-correct
// subject_json extraction and ownership pin, distinct provider accounts of one
// charge id survive as independent streams, and a foreign account's result
// never leaks even when it reuses the in-scope charge id and B-leg id.
func TestQueryEconomicDetailProviderChargeReconciliationPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-pcrec-pg", "USD")
	callID := edTestCallID(t)
	aLegID := "a-ed-pcrec-pg"
	edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-edPCCRPG"))

	edAppendRetentionSubject(t, store, "rec-pcrec-pg-1",
		edTestProviderChargeSubject(store, account.ID, aLegID, callID.String(), "b-edPCCRPG", "pc-pg", "pa-1"),
		"100", "110", 1, time.Unix(1_700_460_000, 0).UTC())
	edAppendRetentionSubject(t, store, "rec-pcrec-pg-2",
		edTestProviderChargeSubject(store, account.ID, aLegID, callID.String(), "b-edPCCRPG", "pc-pg", "pa-2"),
		"100", "120", 1, time.Unix(1_700_460_100, 0).UTC())

	foreign := edTestAccount(t, store, "ed-pcrec-pg-foreign", "USD")
	edAppendRetentionSubject(t, store, "rec-pcrec-pg-foreign",
		edTestProviderChargeSubject(store, foreign.ID, aLegID, callID.String(), "b-edPCCRPG", "pc-pg", "pa-1"),
		"100", "999", 1, time.Unix(1_700_460_200, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	byCharge := edTestProviderChargeReconciliationIndex(got.Reconciliations)
	require.Len(t, got.Reconciliations, 2, "only the attributable provider-charge reconciliation streams are added on PostgreSQL")
	require.Contains(t, byCharge, "pc-pg\x00pa-1")
	require.Contains(t, byCharge, "pc-pg\x00pa-2")
	for _, entry := range got.Reconciliations {
		require.Equal(t, account.ID, entry.Subject.AccountID)
		require.Equal(t, metering.SubjectProviderCharge, entry.Subject.Kind)
	}
}

// TestQueryEconomicDetailAllocationSupersessionPostgresDirect proves Finding 3
// parity on direct PostgreSQL: the same-source bounded supersession closure
// retires a target-moving replacement's predecessor without leaving it live,
// and an account-less shared envelope keeps its membership identity while its
// full source aggregate is redacted for an account scope.
func TestQueryEconomicDetailAllocationSupersessionPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-sup-pg", "USD")
	callA := edTestCallID(t)
	callB := edTestCallID(t)
	edSetupCall(t, store, account.ID, callA, "a-ed-sup-pg", edTestLeg(t, "b-ed-sup-pg-a"))
	edSetupCall(t, store, account.ID, callB, "a-ed-sup-pg-b", edTestLeg(t, "b-ed-sup-pg-b"))

	amount := edTestDecimal(t, "10")
	v1 := economics.AllocationRecord{
		ID: "alloc-sup-pg-v1", Version: 1,
		SourceSubject: edSupAllocationSource(store, account.ID, "shared-pg-move"),
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    edSupAllocationPolicy(),
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-pg-a", Target: edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, "a-ed-sup-pg", callA.String(), "b-ed-sup-pg-a"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, v1))

	v2 := v1.Clone()
	v2.ID = "alloc-sup-pg-v2"
	v2.Operation = economics.AllocationOperationReplacement
	v2.Supersedes = []economics.AllocationRef{edBalanceAllocationRef(store, v1)}
	v2.Targets = []economics.AllocationTarget{
		{TargetID: "t-pg-b", Target: edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, "a-ed-sup-pg-b", callB.String(), "b-ed-sup-pg-b"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
	}
	require.NoError(t, store.AppendAllocation(ctx, v2))

	moved, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callA.String(), ALegID: "a-ed-sup-pg",
	})
	require.NoError(t, err)
	require.Empty(t, moved.Coverage.Allocations)
	require.NotNil(t, moved.Coverage.AllocationState)
	require.Equal(t, economics.AllocationSupersessionResolved, moved.Coverage.AllocationState.Status)
	require.True(t, moved.Coverage.AllocationState.Complete)
	require.Contains(t, edAllocationRefIDs(moved.Coverage.AllocationState.Superseded), v1.ID)

	callC := edTestCallID(t)
	edSetupCall(t, store, account.ID, callC, "a-ed-sup-pg-c", edTestLeg(t, "b-ed-sup-pg-c"))
	shared := economics.AllocationRecord{
		ID: "alloc-sup-pg-shared", Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: store.StoreID(),
			ResourceID: "shared-pg-accountless", PeriodID: "2026-09",
		},
		SourceBasis: economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    edSupAllocationPolicy(),
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-pg-accountless", Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: store.StoreID(), BLegID: "b-ed-sup-pg-c"}, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "4"}},
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: "3", Denominator: "4"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, shared.Validate())
	require.NoError(t, store.AppendAllocation(ctx, shared))

	redacted, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callC.String(), ALegID: "a-ed-sup-pg-c",
	})
	require.NoError(t, err)
	require.NotEmpty(t, redacted.Coverage.Allocations)
	require.NotNil(t, redacted.Coverage.AllocationState)
	require.True(t, redacted.Coverage.AllocationState.Redacted)
	for _, line := range redacted.Coverage.Allocations {
		require.True(t, line.Redacted)
		require.Nil(t, line.SourceAmount)
		require.Nil(t, line.RoundedAmount)
	}
}
