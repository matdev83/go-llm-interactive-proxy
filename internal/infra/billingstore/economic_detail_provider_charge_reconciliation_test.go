package billingstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 second-pass Finding 2 public boundary contract: a genuine
// SubjectProviderCharge reconciliation retention result that an authoritative
// in-scope B-leg owns must be discoverable through the public call and A-leg
// economic-detail readers, without assuming a valuation row exists, while a
// foreign account, foreign tenant or non-owned B-leg must never leak.
//
// Before the fix detailReconciliations was fed only the A-leg/call/B-leg
// subjects, so latestReconciliationsForSubjects' exact kind/id match silently
// omitted every retained SubjectProviderCharge result and those corrections
// could not invalidate an outstanding continuation.

// edTestProviderChargeSubject builds one genuine provider-charge economic
// subject with its authoritative owning B-leg plus the distinct provider
// account and charge identity that make it a stream of its own.
func edTestProviderChargeSubject(store *DurableStore, accountID, aLegID, callID, bLegID, chargeID, providerAccountKey string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectProviderCharge, StoreID: store.StoreID(), TenantID: "tenant-ed",
		AccountID: accountID, ALegID: aLegID, BillingCallID: callID, BLegID: bLegID,
		ProviderAccountKey: providerAccountKey, ProviderChargeID: chargeID,
	}
}

// edTestProviderChargeReconciliationIndex indexes assembled reconciliations by
// the charge identity plus provider account that identifies one stream.
func edTestProviderChargeReconciliationIndex(reconciliations []billing.EconomicDetailReconciliation) map[string]billing.EconomicDetailReconciliation {
	out := make(map[string]billing.EconomicDetailReconciliation, len(reconciliations))
	for _, entry := range reconciliations {
		out[entry.Subject.ProviderChargeID+"\x00"+entry.Subject.ProviderAccountKey] = entry
	}
	return out
}

// TestQueryEconomicDetailCallIncludesProviderChargeReconciliations proves a
// call reader returns all and only the genuine provider-charge reconciliation
// streams an owned B-leg carries: two independent charge identities, two
// provider accounts of one charge id (an independent stream each), only the
// latest revision of one full identity, and no same-account other-call or
// foreign-account result even when it reuses the requested B-leg id.
func TestQueryEconomicDetailCallIncludesProviderChargeReconciliations(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-pcrec-call", "USD")
	callID := edTestCallID(t)
	aLegID := "a-ed-pcrec-call"
	edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-edPCCR"))
	callIDStr := callID.String()

	// Latest revision of one full identity: the earlier revision is superseded.
	edAppendRetentionSubject(t, store, "rec-pcrec-1a",
		edTestProviderChargeSubject(store, account.ID, aLegID, callIDStr, "b-edPCCR", "pc-pcrec-1", "pa-1"),
		"100", "110", 1, time.Unix(1_700_400_000, 0).UTC())
	edAppendRetentionSubject(t, store, "rec-pcrec-1a",
		edTestProviderChargeSubject(store, account.ID, aLegID, callIDStr, "b-edPCCR", "pc-pcrec-1", "pa-1"),
		"100", "100", 2, time.Unix(1_700_400_100, 0).UTC())
	// A distinct provider account of one charge id is an independent stream.
	edAppendRetentionSubject(t, store, "rec-pcrec-1b",
		edTestProviderChargeSubject(store, account.ID, aLegID, callIDStr, "b-edPCCR", "pc-pcrec-1", "pa-2"),
		"100", "120", 1, time.Unix(1_700_400_200, 0).UTC())
	// A distinct charge identity on the same B-leg is a further stream.
	edAppendRetentionSubject(t, store, "rec-pcrec-2",
		edTestProviderChargeSubject(store, account.ID, aLegID, callIDStr, "b-edPCCR", "pc-pcrec-2", "pa-1"),
		"100", "130", 1, time.Unix(1_700_400_300, 0).UTC())

	// Same-account second call on the same A-leg: only that call's B-leg owns
	// its provider-charge reconciliation.
	otherCall := edTestCallID(t)
	edSetupCall(t, store, account.ID, otherCall, aLegID, edTestLeg(t, "b-edPCCR-other"))
	edAppendRetentionSubject(t, store, "rec-pcrec-other-call",
		edTestProviderChargeSubject(store, account.ID, aLegID, otherCall.String(), "b-edPCCR-other", "pc-pcrec-1", "pa-1"),
		"100", "140", 1, time.Unix(1_700_400_400, 0).UTC())

	// Foreign account deliberately reuses the requested charge id and B-leg id,
	// so only the trusted-account ownership predicate can exclude it. It is
	// newer, so a coarse time-ordered probe would hide the in-scope stream.
	foreign := edTestAccount(t, store, "ed-pcrec-call-foreign", "USD")
	edAppendRetentionSubject(t, store, "rec-pcrec-foreign",
		edTestProviderChargeSubject(store, foreign.ID, aLegID, callIDStr, "b-edPCCR", "pc-pcrec-1", "pa-1"),
		"100", "999", 1, time.Unix(1_700_400_500, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callIDStr, ALegID: aLegID,
	})
	require.NoError(t, err)

	byCharge := edTestProviderChargeReconciliationIndex(got.Reconciliations)
	require.Len(t, got.Reconciliations, 3, "only the attributable provider-charge reconciliation streams are added")
	require.Contains(t, byCharge, "pc-pcrec-1\x00pa-1", "a genuine provider-charge-kind reconciliation must be discoverable")
	require.Contains(t, byCharge, "pc-pcrec-1\x00pa-2", "a distinct provider-account stream of one charge id must survive")
	require.Contains(t, byCharge, "pc-pcrec-2\x00pa-1", "a distinct charge identity on the same B-leg must survive")

	require.NotNil(t, byCharge["pc-pcrec-1\x00pa-1"].Quantity)
	require.Equal(t, billing.ReconciliationStatusMatched, byCharge["pc-pcrec-1\x00pa-1"].Quantity.Status,
		"the latest revision must win within one exact full identity")
	require.Equal(t, metering.SubjectProviderCharge, byCharge["pc-pcrec-1\x00pa-1"].Subject.Kind)

	for _, entry := range got.Reconciliations {
		require.Equal(t, callIDStr, entry.Subject.BillingCallID)
		require.Equal(t, "b-edPCCR", entry.Subject.BLegID)
		require.Equal(t, account.ID, entry.Subject.AccountID)
	}
}

// TestQueryEconomicDetailALegIncludesProviderChargeReconciliations proves an
// A-leg reader traverses genuine provider-charge reconciliation streams across
// every owned child B-leg while a different account sharing the same A-leg,
// B-leg and charge identity never leaks.
func TestQueryEconomicDetailALegIncludesProviderChargeReconciliations(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-pcrec-aleg", "USD")
	aLegID := "a-ed-pcrec-aleg"
	first := edTestCallID(t)
	second := edTestCallID(t)
	edSetupCall(t, store, account.ID, first, aLegID, edTestLeg(t, "b-edPCCRA"))
	edSetupCall(t, store, account.ID, second, aLegID, edTestLeg(t, "b-edPCCRB"))

	edAppendRetentionSubject(t, store, "rec-pcrec-a",
		edTestProviderChargeSubject(store, account.ID, aLegID, first.String(), "b-edPCCRA", "pc-aleg", "pa-1"),
		"100", "110", 1, time.Unix(1_700_410_000, 0).UTC())
	edAppendRetentionSubject(t, store, "rec-pcrec-b",
		edTestProviderChargeSubject(store, account.ID, aLegID, second.String(), "b-edPCCRB", "pc-aleg", "pa-1"),
		"100", "120", 1, time.Unix(1_700_410_100, 0).UTC())

	foreign := edTestAccount(t, store, "ed-pcrec-aleg-foreign", "USD")
	edAppendRetentionSubject(t, store, "rec-pcrec-a-foreign",
		edTestProviderChargeSubject(store, foreign.ID, aLegID, first.String(), "b-edPCCRA", "pc-aleg", "pa-1"),
		"100", "999", 1, time.Unix(1_700_410_200, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, ALegID: aLegID,
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 2, "A-leg scope must traverse owned child B-leg provider-charge streams")
	seenCalls := map[string]bool{}
	for _, entry := range got.Reconciliations {
		require.Equal(t, metering.SubjectProviderCharge, entry.Subject.Kind)
		require.Equal(t, aLegID, entry.Subject.ALegID)
		require.Equal(t, account.ID, entry.Subject.AccountID)
		require.NotNil(t, entry.Quantity)
		seenCalls[entry.Subject.BillingCallID] = true
	}
	require.True(t, seenCalls[first.String()], "the first child call's provider-charge stream must survive")
	require.True(t, seenCalls[second.String()], "the second child call's provider-charge stream must survive")
}

// TestQueryEconomicDetailProviderChargeReconciliationTenantIsolation proves a
// supplied tenant scope isolates one provider-charge reconciliation tenant
// while an unscoped read retains each distinct tenant identity independently.
func TestQueryEconomicDetailProviderChargeReconciliationTenantIsolation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-pcrec-tenant", "USD")
	callID := edTestCallID(t)
	aLegID := "a-ed-pcrec-tenant"
	edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-edPCCRT"))

	trusted := edTestProviderChargeSubject(store, account.ID, aLegID, callID.String(), "b-edPCCRT", "pc-tenant", "pa-1")
	foreignTenant := trusted
	foreignTenant.TenantID = "tenant-foreign"

	edAppendRetentionSubject(t, store, "rec-pcrec-tenant-a", trusted, "100", "110", 1, time.Unix(1_700_420_000, 0).UTC())
	// The foreign tenant is newer, so a coarse order would hide the trusted one.
	edAppendRetentionSubject(t, store, "rec-pcrec-tenant-b", foreignTenant, "100", "120", 1, time.Unix(1_700_420_100, 0).UTC())

	unscoped, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.Len(t, unscoped.Reconciliations, 2, "distinct provider-charge tenant identities must both survive when no tenant scope is requested")

	scoped, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.Len(t, scoped.Reconciliations, 1, "tenant scope must isolate one provider-charge reconciliation identity")
	require.Equal(t, "tenant-ed", scoped.Reconciliations[0].Subject.TenantID)
}

// TestQueryEconomicDetailProviderChargeReconciliationBoundFailsClosed proves
// the provider-charge reconciliation discovery is finite and independent of
// valuation existence: many retained genuine provider-charge results with no
// persisted valuation at all must fail explicitly instead of being silently
// truncated.
func TestQueryEconomicDetailProviderChargeReconciliationBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-pcrec-bound", "USD")
	callID := edTestCallID(t)
	aLegID := "a-ed-pcrec-bound"
	edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-edPCCRBD"))

	for i := range billing.MaxEconomicDetailReconciliations {
		edAppendRetentionSubject(t, store, fmt.Sprintf("rec-pcrec-bound-%05d", i),
			edTestProviderChargeSubject(store, account.ID, aLegID, callID.String(), "b-edPCCRBD",
				fmt.Sprintf("pc-pcrec-bound-%05d", i), "pa-1"),
			"100", "110", 1, time.Unix(1_700_430_000+int64(i), 0).UTC())
	}

	var persistedValuations int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.StoreID()).Scan(ctx, &persistedValuations))
	require.Zero(t, persistedValuations, "the bound proof must not depend on any persisted valuation row")

	_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}

// TestQueryEconomicDetailContinuationInvalidatesOnLateProviderChargeReconciliation
// proves a provider-charge reconciliation inserted or corrected between pages
// makes the outstanding continuation stale instead of silently changing the
// full-scope set the cursor promised.
func TestQueryEconomicDetailContinuationInvalidatesOnLateProviderChargeReconciliation(t *testing.T) {
	t.Parallel()

	t.Run("insert", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		account := edTestAccount(t, store, "ed-pcrec-snap-insert", "USD")
		callID, _ := edCompleteCall(t, store, account.ID, "a-ed-pcrec-snap-insert", "edPCCRS")
		query := billing.EconomicDetailQuery{StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-pcrec-snap-insert"}
		first := edSnapshotPageOne(t, store, query)

		edAppendRetentionSubject(t, store, "rec-pcrec-snap-late",
			edTestProviderChargeSubject(store, account.ID, "a-ed-pcrec-snap-insert", callID.String(), "b-edPCCRS", "pc-snap", "pa-1"),
			"100", "110", 1, time.Unix(1_700_440_000, 0).UTC())

		require.ErrorIs(t, edSnapshotContinue(t, store, query, first), economics.ErrOperatorCursorStale,
			"a late provider-charge reconciliation must invalidate the continuation")
	})

	t.Run("correction", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		account := edTestAccount(t, store, "ed-pcrec-snap-correct", "USD")
		callID, _ := edCompleteCall(t, store, account.ID, "a-ed-pcrec-snap-correct", "edPCCRC")
		subject := edTestProviderChargeSubject(store, account.ID, "a-ed-pcrec-snap-correct", callID.String(), "b-edPCCRC", "pc-snap-correct", "pa-1")
		edAppendRetentionSubject(t, store, "rec-pcrec-snap-correct", subject, "100", "110", 1, time.Unix(1_700_450_000, 0).UTC())

		query := billing.EconomicDetailQuery{StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-pcrec-snap-correct"}
		first := edSnapshotPageOne(t, store, query)
		require.Len(t, first.Reconciliations, 1)

		edAppendRetentionSubject(t, store, "rec-pcrec-snap-correct", subject, "100", "100", 2, time.Unix(1_700_450_100, 0).UTC())

		require.ErrorIs(t, edSnapshotContinue(t, store, query, first), economics.ErrOperatorCursorStale,
			"a corrected provider-charge reconciliation must invalidate the continuation")
	})
}
