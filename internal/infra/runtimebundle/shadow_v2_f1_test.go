package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestPhase172F1ComposedShadowFirstKeepsV1Claimable(t *testing.T) {
	store := newShadowBillingStore(t)
	journal := newShadowJournal(t)
	ctx := context.Background()
	account := billing.Account{ID: "f1-acct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: account.ID, ALegID: "a-f1", SessionID: "session-f1",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    []string{"b-f1"},
	}
	_, err = store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(),
		Max: billing.Money{Nano: 100_000, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
	require.NoError(t, err)

	handle, err := runtimebundle.ComposeShadowV2Capture(runtimebundle.ShadowV2CaptureInput{
		StoreID: c4StoreID, MaxObservations: 16,
		EvidenceSink: journal, WorkAppender: store, ResultStore: store, ReconciliationStore: store,
		Rater: r1EchoRater{}, V1Settlement: store,
	})
	require.NoError(t, err)
	// Shadow-first: V2 observation under the SAME CallID/BLegID lives only in
	// observation storage; it must never create V1 rows or claim state.
	observation := c4Observation(t, callID, "b-f1", "f1-composed-obs", 1)
	observation.Subject.AccountID = account.ID
	observation.Subject.ALegID = "a-f1"
	observation.Subject.CallID = callID.String()
	observation.Correlation.ALegID = "a-f1"
	observation.Correlation.CallID = callID.String()
	require.NoError(t, handle.CaptureObservations(ctx, []metering.Observation{observation}))
	require.NoError(t, handle.CaptureObservations(ctx, []metering.Observation{observation}))

	// Legitimate ordinary V1 scalar leg/call with existing exposure follows.
	leg := c4V1Leg(t, callID, "b-f1")
	leg.ALegID = "a-f1"
	require.NoError(t, store.AppendCallLegUsage(ctx, leg))
	require.NoError(t, store.AppendCallUsage(ctx, call))

	claimed, err := store.ClaimCompleteCalls(ctx, 10)
	require.NoError(t, err)
	found := false
	for _, complete := range claimed {
		if complete.Closure.CallID == callID {
			found = true
		}
	}
	require.True(t, found, "existing-exposure V1 closure must stay claimable after composed shadow-first capture")

	pending, err := store.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1, "exactly the legitimate V1 leg may be pending after shadow-first capture")

	// V2 observations remain queryable under source-separated storage.
	page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: "test", SubjectKind: metering.SubjectBLeg, SubjectID: "b-f1", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
}
