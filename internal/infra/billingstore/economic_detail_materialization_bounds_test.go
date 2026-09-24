package billingstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Finding 5B2 RED contract: valuation-history selection and selected-head
// loading are bounded database-side before Go decode/materialization.
//
// Before this slice detailValuations selected every historical row for the
// scope and collapsed it in memory, and detailHeads loaded every row per chunk
// then only hit the core bound at assembly. Both must instead select the latest
// durable revision per true logical stream at the database boundary and enforce
// one global finite budget with a database-side LIMIT, failing closed with
// billing.ErrEconomicDetailBoundExceeded instead of decoding or truncating an
// unbounded set.

// edTestBoundValuation persists one distinct logical valuation stream so a
// bound probe can create many independent same-basis streams without routing
// through the call/A-leg subject derivation. Each stream gets a distinct BLeg
// subject identity and a distinct input observation ref, so
// (subject identity + perspective + basis) is unique per index.
func edTestBoundValuation(t *testing.T, store *DurableStore, accountID string, index int, basis economics.ValuationBasis, createdAt time.Time) (economics.Valuation, economicDetailSubject) {
	t.Helper()
	bLegID := fmt.Sprintf("b-ed-val-bound-%05d", index)
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", accountID, "a-ed-val-bound", fmt.Sprintf("call-ed-val-bound-%05d", index), bLegID)
	ref := metering.ObservationRef{
		StoreID: store.StoreID(), ObservationID: fmt.Sprintf("obs-ed-val-bound-%05d", index),
		Revision: 1, PayloadHash: strings.Repeat("a", 64),
	}
	valuation := edTestValuation(t, fmt.Sprintf("val-ed-val-bound-%05d", index), basis, subject, []metering.ObservationRef{ref}, edTestCurrencyTotal(t, "USD", "1.00"))
	valuation.CreatedAt = createdAt
	require.NoError(t, valuation.Validate())
	return valuation, economicDetailSubject{kind: metering.SubjectBLeg, id: bLegID}
}

// edTestAppendExactStreamValuation persists one revision of an explicitly
// identified logical valuation stream. Distinct revisions vary only the input
// observation ref and creation time; the exact stream identity (subject
// lineage + perspective + basis) is fixed by the caller.
func edTestAppendExactStreamValuation(t *testing.T, store *DurableStore, id string, subject metering.SubjectRef, revision int, createdAt time.Time) {
	t.Helper()
	ref := metering.ObservationRef{
		StoreID: store.StoreID(), ObservationID: fmt.Sprintf("obs-%s-%02d", id, revision),
		Revision: 1, PayloadHash: strings.Repeat("a", 64),
	}
	valuation := edTestValuation(t, id, economics.BasisLocalExpected, subject, []metering.ObservationRef{ref}, edTestCurrencyTotal(t, "USD", "1.00"))
	valuation.CreatedAt = createdAt
	require.NoError(t, valuation.Validate())
	require.NoError(t, store.AppendValuation(context.Background(), valuation))
}

// TestEconomicDetailValuationAttemptLineageStreamsSurvive proves the database
// boundary keys on the complete canonical SubjectRef identity rather than the
// persisted subject_id proxy. Two B-leg streams that share subject_kind,
// subject_id, tenant, perspective and basis but differ by attempt lineage have
// different billing.EconomicValuationStreamKey values, so both latest
// revisions must survive while multiple revisions of each collapse only within
// its own exact stream.
func TestEconomicDetailValuationAttemptLineageStreamsSurvive(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-identity-attempt", "USD")

	base := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-identity", "call-ed-identity", "b-ed-identity")
	attemptOne := base
	attemptOne.AttemptID = "att-ed-identity-1"
	attemptOne.AttemptSeq = 1
	attemptTwo := base
	attemptTwo.AttemptID = "att-ed-identity-2"
	attemptTwo.AttemptSeq = 2

	require.NotEqual(t,
		billing.EconomicValuationStreamKey(economics.Valuation{Subject: attemptOne, Perspective: metering.PerspectiveOperator, Basis: economics.BasisLocalExpected}),
		billing.EconomicValuationStreamKey(economics.Valuation{Subject: attemptTwo, Perspective: metering.PerspectiveOperator, Basis: economics.BasisLocalExpected}),
		"shared kind/subject_id but distinct attempt lineage must be distinct true streams")

	edTestAppendExactStreamValuation(t, store, "val-ed-identity-a1", attemptOne, 1, time.Unix(1_700_080_000, 0).UTC())
	edTestAppendExactStreamValuation(t, store, "val-ed-identity-a2", attemptOne, 2, time.Unix(1_700_080_100, 0).UTC())
	edTestAppendExactStreamValuation(t, store, "val-ed-identity-b1", attemptTwo, 1, time.Unix(1_700_080_200, 0).UTC())
	edTestAppendExactStreamValuation(t, store, "val-ed-identity-b2", attemptTwo, 2, time.Unix(1_700_080_300, 0).UTC())

	got, err := store.detailValuations(ctx, "tenant-ed", []economicDetailSubject{{kind: metering.SubjectBLeg, id: "b-ed-identity"}})
	require.NoError(t, err)
	require.Len(t, got, 2, "both attempt-lineage streams must survive")

	byAttempt := map[string]string{}
	for _, valuation := range got {
		byAttempt[valuation.Subject.AttemptID] = valuation.ID
	}
	require.Equal(t, "val-ed-identity-a2", byAttempt["att-ed-identity-1"], "latest revision within the exact stream wins")
	require.Equal(t, "val-ed-identity-b2", byAttempt["att-ed-identity-2"], "latest revision within the exact stream wins")
}

// TestEconomicDetailValuationProviderChargeLineageStreamsSurvive proves the
// same exact identity boundary for provider-charge subjects, whose persisted
// subject_id is the provider charge identity while the provider account key is
// also part of the true stream identity.
func TestEconomicDetailValuationProviderChargeLineageStreamsSurvive(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-identity-charge", "USD")

	chargeOne := metering.SubjectRef{
		Kind: metering.SubjectProviderCharge, StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID,
		BillingCallID: "call-ed-identity", BLegID: "b-ed-identity-charge",
		ProviderChargeID: "pc-ed-identity", ProviderAccountKey: "acct-ed-identity-1",
	}
	chargeTwo := chargeOne
	chargeTwo.ProviderAccountKey = "acct-ed-identity-2"

	require.NotEqual(t,
		billing.EconomicValuationStreamKey(economics.Valuation{Subject: chargeOne, Perspective: metering.PerspectiveOperator, Basis: economics.BasisLocalExpected}),
		billing.EconomicValuationStreamKey(economics.Valuation{Subject: chargeTwo, Perspective: metering.PerspectiveOperator, Basis: economics.BasisLocalExpected}),
		"shared provider charge id but distinct account key must be distinct true streams")

	edTestAppendExactStreamValuation(t, store, "val-ed-identity-c1", chargeOne, 1, time.Unix(1_700_090_000, 0).UTC())
	edTestAppendExactStreamValuation(t, store, "val-ed-identity-c2", chargeOne, 2, time.Unix(1_700_090_100, 0).UTC())
	edTestAppendExactStreamValuation(t, store, "val-ed-identity-d1", chargeTwo, 1, time.Unix(1_700_090_200, 0).UTC())

	got, err := store.detailValuations(ctx, "tenant-ed", []economicDetailSubject{{kind: metering.SubjectProviderCharge, id: "pc-ed-identity"}})
	require.NoError(t, err)
	require.Len(t, got, 2, "both provider-charge lineage streams must survive")

	byAccount := map[string]string{}
	for _, valuation := range got {
		byAccount[valuation.Subject.ProviderAccountKey] = valuation.ID
	}
	require.Equal(t, "val-ed-identity-c2", byAccount["acct-ed-identity-1"], "latest revision within the exact stream wins")
	require.Equal(t, "val-ed-identity-d1", byAccount["acct-ed-identity-2"])
}

func edTestValuationQueryBounded(t *testing.T, queries []string) {
	t.Helper()
	for _, query := range queries {
		if strings.Contains(query, "billing_valuations") && strings.Contains(query, "LIMIT") {
			return
		}
	}
	require.Failf(t, "valuation SQL must carry a database-side LIMIT", "recorded queries: %v", queries)
}

func edTestHeadQueryBounded(t *testing.T, queries []string) {
	t.Helper()
	for _, query := range queries {
		if strings.Contains(query, "billing_provider_cost_heads") && strings.Contains(query, "LIMIT") {
			return
		}
	}
	require.Failf(t, "head SQL must carry a database-side LIMIT", "recorded queries: %v", queries)
}

// TestEconomicDetailValuationLatestPerStreamAndDatabaseBound proves the
// database boundary selects only the latest revision of one logical stream
// (many revisions, one returned), keeps independent same-basis streams, and
// carries a database-side LIMIT so history is not fully materialized in Go.
func TestEconomicDetailValuationLatestPerStreamAndDatabaseBound(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-val-latest", "USD")

	historySubject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-val-history", "call-ed-val-history", "b-ed-val-history")
	var latestID string
	for i := 0; i < 40; i++ {
		ref := metering.ObservationRef{
			StoreID: store.StoreID(), ObservationID: fmt.Sprintf("obs-ed-val-history-%03d", i),
			Revision: 1, PayloadHash: strings.Repeat("a", 64),
		}
		valuation := edTestValuation(t, fmt.Sprintf("val-ed-val-history-%03d", i), economics.BasisLocalExpected, historySubject, []metering.ObservationRef{ref}, edTestCurrencyTotal(t, "USD", "1.00"))
		valuation.CreatedAt = time.Unix(1_700_030_000+int64(i), 0).UTC()
		require.NoError(t, valuation.Validate())
		require.NoError(t, store.AppendValuation(ctx, valuation))
		latestID = valuation.ID
	}

	subjects := []economicDetailSubject{{kind: metering.SubjectBLeg, id: "b-ed-val-history"}}
	for i := 0; i < 3; i++ {
		valuation, subject := edTestBoundValuation(t, store, account.ID, i, economics.BasisLocalExpected, time.Unix(1_700_030_100+int64(i), 0).UTC())
		require.NoError(t, store.AppendValuation(ctx, valuation))
		subjects = append(subjects, subject)
	}

	// Attach the recorder only around the read, so write-path LIMITs cannot
	// mask a missing read bound.
	recorder := &edLegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	got, err := store.detailValuations(ctx, "tenant-ed", subjects)
	require.NoError(t, err)
	require.Len(t, got, 4, "one latest revision per logical stream, independent same-basis streams preserved")

	var history *economics.Valuation
	for i := range got {
		if got[i].Subject.BLegID == "b-ed-val-history" {
			history = &got[i]
		}
	}
	require.NotNil(t, history)
	require.Equal(t, latestID, history.ID, "the latest revision of a long stream is the current one")

	edTestValuationQueryBounded(t, recorder.queries)
}

// TestEconomicDetailValuationStreamsBoundFailsClosed proves a scope with more
// distinct logical streams than the finite valuation budget fails closed
// instead of materializing the over-bound set.
func TestEconomicDetailValuationStreamsBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-val-over", "USD")

	subjects := make([]economicDetailSubject, 0, billing.MaxEconomicDetailValuations+1)
	for i := 0; i <= billing.MaxEconomicDetailValuations; i++ {
		valuation, subject := edTestBoundValuation(t, store, account.ID, i, economics.BasisLocalExpected, time.Unix(1_700_040_000+int64(i), 0).UTC())
		require.NoError(t, store.AppendValuation(ctx, valuation))
		subjects = append(subjects, subject)
	}

	got, err := store.detailValuations(ctx, "tenant-ed", subjects)
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, got, "an over-bound valuation set must never be retained")
}

// TestEconomicDetailValuationStreamsBoundaryPasses proves exactly the finite
// stream budget is still accepted; the bound fails over, not at, the maximum.
func TestEconomicDetailValuationStreamsBoundaryPasses(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-val-max", "USD")

	subjects := make([]economicDetailSubject, 0, billing.MaxEconomicDetailValuations)
	for i := 0; i < billing.MaxEconomicDetailValuations; i++ {
		valuation, subject := edTestBoundValuation(t, store, account.ID, i, economics.BasisLocalExpected, time.Unix(1_700_050_000+int64(i), 0).UTC())
		require.NoError(t, store.AppendValuation(ctx, valuation))
		subjects = append(subjects, subject)
	}

	got, err := store.detailValuations(ctx, "tenant-ed", subjects)
	require.NoError(t, err)
	require.Len(t, got, billing.MaxEconomicDetailValuations)
}

// TestEconomicDetailHeadQueryCarriesDatabaseSideLimit proves head loading is
// bounded in SQL itself rather than only at assembly.
func TestEconomicDetailHeadQueryCarriesDatabaseSideLimit(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-head-limit", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-head-limit", "edHL")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-head-limit", callID.String(), "b-edHL")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-hl",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	recorder := &edLegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	got, err := store.detailHeads(ctx, account.ID, []string{callID.String()})
	require.NoError(t, err)
	require.Len(t, got, 1)

	edTestHeadQueryBounded(t, recorder.queries)
}

// TestEconomicDetailHeadsBoundFailsClosed proves a call with more heads than
// the finite head budget fails closed at load time instead of being
// materialized and only rejected later.
func TestEconomicDetailHeadsBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-head-over", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-head-over", "edHO")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-head-over", callID.String(), "b-edHO")
	for i := 0; i <= billing.MaxEconomicDetailHeads; i++ {
		edTestPostSelectedHead(t, store, account.ID, callID, subject, fmt.Sprintf("head-ed-ho-%04d", i),
			billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	}

	got, err := store.detailHeads(ctx, account.ID, []string{callID.String()})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, got, "an over-bound head set must never be retained")
}

// TestEconomicDetailHeadsBoundaryAndGlobalBudget proves exactly the finite head
// budget is accepted and that the budget is global across chunks rather than
// reset per chunk.
func TestEconomicDetailHeadsBoundaryAndGlobalBudget(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-head-global", "USD")
	firstCall, _ := edCompleteCall(t, store, account.ID, "a-ed-head-global", "edHG1")
	secondCall, _ := edCompleteCall(t, store, account.ID, "a-ed-head-global", "edHG2")
	firstSubject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-head-global", firstCall.String(), "b-edHG1")
	secondSubject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-head-global", secondCall.String(), "b-edHG2")
	for i := 0; i < billing.MaxEconomicDetailHeads; i++ {
		edTestPostSelectedHead(t, store, account.ID, firstCall, firstSubject, fmt.Sprintf("head-ed-hg1-%04d", i),
			billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	}
	edTestPostSelectedHead(t, store, account.ID, secondCall, secondSubject, "head-ed-hg2",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	atBound, err := store.detailHeads(ctx, account.ID, []string{firstCall.String()})
	require.NoError(t, err)
	require.Len(t, atBound, billing.MaxEconomicDetailHeads)

	// Split the two calls into different chunks: each chunk is individually
	// under the bound, but their cumulative total is one row over.
	padded := make([]string, 0, economicDetailChunkSize+1)
	padded = append(padded, firstCall.String())
	for i := 1; i < economicDetailChunkSize; i++ {
		padded = append(padded, fmt.Sprintf("call-pad-head-%04d", i))
	}
	padded = append(padded, secondCall.String())

	over, err := store.detailHeads(ctx, account.ID, padded)
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, over)
}
