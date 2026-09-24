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

// Phase 16 Finding 1 RED contract: public QueryEconomicDetail must discover
// provider-charge valuation subjects that are attributable through authoritative
// B-leg/call ownership. The durable scope-subject derivation previously emitted
// only A-leg, billing-call and B-leg kind/id pairs, so detailValuations' exact
// kind/id match silently omitted every SubjectProviderCharge stream on an
// in-scope B-leg, including its cost and completeness evidence.

// edTestProviderChargeValuation persists one provider-charge valuation revision
// on one authoritative B-leg. The charge id + provider account key + B-leg
// ownership identity is fixed by the caller so revisions and distinct
// provider-account streams can be exercised independently.
func edTestProviderChargeValuation(t *testing.T, store *DurableStore, accountID, id, chargeID, providerAccountKey, aLegID, callID, bLegID, amount string, createdAt time.Time) economics.Valuation {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectProviderCharge, StoreID: store.StoreID(), TenantID: "tenant-ed",
		AccountID: accountID, ALegID: aLegID, BillingCallID: callID, BLegID: bLegID,
		ProviderAccountKey: providerAccountKey, ProviderChargeID: chargeID,
	}
	ref := metering.ObservationRef{
		StoreID: store.StoreID(), ObservationID: "obs-provider-charge-" + id,
		Revision: 1, PayloadHash: strings.Repeat("a", 64),
	}
	valuation := edTestValuation(t, id, economics.BasisProviderReported, subject, []metering.ObservationRef{ref}, edTestCurrencyTotal(t, "USD", amount))
	valuation.CreatedAt = createdAt
	require.NoError(t, valuation.Validate())
	return valuation
}

func edTestValuationIndex(valuations []economics.Valuation) map[string]economics.Valuation {
	out := make(map[string]economics.Valuation, len(valuations))
	for _, valuation := range valuations {
		out[valuation.ID] = valuation
	}
	return out
}

// TestQueryEconomicDetailCallIncludesProviderChargeValuations proves public call
// detail returns all and only the provider-charge streams attributable to the
// requested call: two independent charge identities, two provider accounts of
// one charge id (an independent stream each), and only the latest revision of a
// stream. A same-account other-call charge and a foreign-account charge whose
// B-leg id collides with the requested one must not leak.
func TestQueryEconomicDetailCallIncludesProviderChargeValuations(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-pc-call", "USD")
	callID, baseIDs := edCompleteCall(t, store, account.ID, "a-ed-pc-call", "edPCC")
	bLegID := "b-edPCC"
	callIDStr := callID.String()

	streams := []economics.Valuation{
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pc-1a", "pc-ed-1", "pa-1", "a-ed-pc-call", callIDStr, bLegID, "1.32", time.Unix(1_700_200_000, 0).UTC()),
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pc-1b", "pc-ed-1", "pa-1", "a-ed-pc-call", callIDStr, bLegID, "1.40", time.Unix(1_700_200_100, 0).UTC()),
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pc-2", "pc-ed-1", "pa-2", "a-ed-pc-call", callIDStr, bLegID, "1.50", time.Unix(1_700_200_200, 0).UTC()),
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pc-3", "pc-ed-2", "pa-1", "a-ed-pc-call", callIDStr, bLegID, "2.00", time.Unix(1_700_200_300, 0).UTC()),
	}
	for _, valuation := range streams {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}

	// Same-account second call on the same A-leg: its charge is attributable to
	// that call's B-leg, never to the requested call.
	otherCall, _ := edCompleteCall(t, store, account.ID, "a-ed-pc-call", "edPCC2")
	require.NoError(t, store.AppendValuation(ctx, edTestProviderChargeValuation(t, store, account.ID,
		"val-ed-pc-other-call", "pc-ed-other-call", "pa-1", "a-ed-pc-call", otherCall.String(), "b-edPCC2", "3.00", time.Unix(1_700_200_400, 0).UTC())))

	// Foreign account charge deliberately reuses the requested B-leg id, so a
	// B-leg-only discovery path could leak it; the trusted account must exclude
	// it without failing the legitimate query.
	foreign := edTestAccount(t, store, "ed-pc-call-foreign", "USD")
	require.NoError(t, store.AppendValuation(ctx, edTestProviderChargeValuation(t, store, foreign.ID,
		"val-ed-pc-foreign", "pc-ed-foreign", "pa-1", "a-ed-pc-call", callIDStr, bLegID, "9.99", time.Unix(1_700_200_500, 0).UTC())))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callIDStr, ALegID: "a-ed-pc-call",
	})
	require.NoError(t, err)

	byID := edTestValuationIndex(got.Valuations)
	for _, id := range baseIDs {
		require.Contains(t, byID, id, "base B-leg valuation %q must survive", id)
	}
	require.Contains(t, byID, "val-ed-pc-1b", "latest revision of an attributable provider-charge stream wins")
	require.NotContains(t, byID, "val-ed-pc-1a", "superseded revision of the same stream must not leak")
	require.Contains(t, byID, "val-ed-pc-2", "a distinct provider-account stream of one charge id must survive")
	require.Contains(t, byID, "val-ed-pc-3", "a distinct provider-charge identity on the same B-leg must survive")
	require.NotContains(t, byID, "val-ed-pc-other-call", "another call's provider charge must not leak into the requested call")
	require.NotContains(t, byID, "val-ed-pc-foreign", "a foreign-account provider charge must not leak, even with a colliding B-leg id")
	require.Len(t, got.Valuations, len(baseIDs)+3, "only the attributable provider-charge streams are added")

	require.Equal(t, "pa-1", byID["val-ed-pc-1b"].Subject.ProviderAccountKey)
	require.Equal(t, "pa-2", byID["val-ed-pc-2"].Subject.ProviderAccountKey)
	require.Equal(t, "pc-ed-2", byID["val-ed-pc-3"].Subject.ProviderChargeID)
}

// TestQueryEconomicDetailALegIncludesProviderChargeValuations proves public
// A-leg detail returns every provider-charge stream attributable across all
// in-scope calls/B-legs, including distinct provider accounts, while a different
// account sharing the same A-leg identity does not leak.
func TestQueryEconomicDetailALegIncludesProviderChargeValuations(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-pc-aleg", "USD")
	first, firstBase := edCompleteCall(t, store, account.ID, "a-ed-pc-aleg", "edPCA")
	second, secondBase := edCompleteCall(t, store, account.ID, "a-ed-pc-aleg", "edPCB")

	streams := []economics.Valuation{
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pca-1", "pc-ed-a", "pa-1", "a-ed-pc-aleg", first.String(), "b-edPCA", "1.32", time.Unix(1_700_210_000, 0).UTC()),
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pca-2", "pc-ed-a", "pa-2", "a-ed-pc-aleg", first.String(), "b-edPCA", "1.50", time.Unix(1_700_210_100, 0).UTC()),
		edTestProviderChargeValuation(t, store, account.ID, "val-ed-pcb-1", "pc-ed-b", "pa-1", "a-ed-pc-aleg", second.String(), "b-edPCB", "2.00", time.Unix(1_700_210_200, 0).UTC()),
	}
	for _, valuation := range streams {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}

	// A different account sharing the same A-leg/B-leg identity is out of the
	// trusted account scope and must never be gathered by the A-leg reader.
	foreign := edTestAccount(t, store, "ed-pc-aleg-foreign", "USD")
	require.NoError(t, store.AppendValuation(ctx, edTestProviderChargeValuation(t, store, foreign.ID,
		"val-ed-pca-foreign", "pc-ed-a", "pa-1", "a-ed-pc-aleg", first.String(), "b-edPCA", "9.99", time.Unix(1_700_210_300, 0).UTC())))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, ALegID: "a-ed-pc-aleg",
	})
	require.NoError(t, err)

	byID := edTestValuationIndex(got.Valuations)
	for _, id := range append(append([]string{}, firstBase...), secondBase...) {
		require.Contains(t, byID, id, "base valuation %q must survive across the A-leg", id)
	}
	require.Contains(t, byID, "val-ed-pca-1", "first call charge account pa-1 must survive")
	require.Contains(t, byID, "val-ed-pca-2", "first call charge account pa-2 must survive")
	require.Contains(t, byID, "val-ed-pcb-1", "second call charge must survive")
	require.NotContains(t, byID, "val-ed-pca-foreign", "a foreign-account provider charge must not leak into the A-leg scope")
	require.Len(t, got.Valuations, len(firstBase)+len(secondBase)+3)
}

// TestQueryEconomicDetailProviderChargeSubjectsBoundFailsClosed proves the
// attributable provider-charge discovery is finite and fails explicitly one row
// over its budget before materializing the excess.
func TestQueryEconomicDetailProviderChargeSubjectsBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-pc-bound", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-pc-bound", "edPCB")

	for i := 0; i <= billing.MaxEconomicDetailValuations; i++ {
		valuation := edTestProviderChargeValuation(t, store, account.ID,
			fmt.Sprintf("val-ed-pc-bound-%05d", i), fmt.Sprintf("pc-ed-pc-bound-%05d", i),
			"pa-1", "a-ed-pc-bound", callID.String(), "b-edPCB", "1.00",
			time.Unix(1_700_300_000+int64(i), 0).UTC())
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}

	_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-pc-bound",
	})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}
