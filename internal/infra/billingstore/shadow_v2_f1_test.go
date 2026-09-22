package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

var f1JournalSequence atomic.Int64

func openF1Journal(t *testing.T) *journalstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:f1-journal-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", f1JournalSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	journal, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

func f1Capture(t *testing.T, journal *journalstore.DurableStore, store *DurableStore) *ShadowV2Capture {
	t.Helper()
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, shadowV2TestRater(t),
	)
	require.NoError(t, err)
	return capture
}

func f1Observation(t *testing.T, callID billing.BillingCallID, bLegID, obsID string, revision uint64) metering.Observation {
	t.Helper()
	observation := shadowV2ObservationForLeg(callID, "a-f1", bLegID, obsID, revision)
	observation.Subject.StoreID = "test"
	observation.Subject.TenantID = "tenant-f1"
	observation.Subject.AccountID = "f1-acct"
	observation.Correlation.StoreID = "test"
	observation.Correlation.TenantID = "tenant-f1"
	return observation
}

func f1ObservationsForBLeg(t *testing.T, store *journalstore.DurableStore, bLegID string) []metering.Observation {
	t.Helper()
	page, err := store.ListObservations(context.Background(), journalstore.ObservationQuery{
		StoreID: "test", SubjectKind: metering.SubjectBLeg, SubjectID: bLegID, Limit: 100,
	})
	require.NoError(t, err)
	require.Empty(t, page.NextCursor)
	return page.Observations
}

func f1ScalarLeg(t *testing.T, callID billing.BillingCallID, bLegID string) billing.CallLegUsageRecord {
	t.Helper()
	leg := testIndependentCallLegFor(callID, bLegID)
	leg.ALegID = "a-f1"
	return leg
}

func f1Call(t *testing.T, callID billing.BillingCallID, bLegIDs ...string) billing.CallUsageRecord {
	t.Helper()
	return billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: "f1-acct", ALegID: "a-f1", SessionID: "session-f1",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    bLegIDs,
	}
}

func f1ProvisionAccount(t *testing.T, ctx context.Context, store *DurableStore) billing.Account {
	t.Helper()
	account := billing.Account{ID: "f1-acct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	return account
}

func f1SettleV1(t *testing.T, ctx context.Context, store *DurableStore, call billing.CallUsageRecord, maxNano, chargeNano int64) {
	t.Helper()
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: call.AccountID, CallID: call.CallID.String(),
		Max: billing.Money{Nano: maxNano, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
	require.NoError(t, err)
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{CallID: call.CallID, CustomerCharge: billing.Money{Nano: chargeNano, Currency: "USD"}, Fingerprint: "f1-v1-result"}})
	require.NoError(t, err)
	require.False(t, settled.Replayed)
}

func TestPhase172F1ShadowFirstKeepsV1Settleable(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	account := f1ProvisionAccount(t, ctx, store)
	callID := mustShadowCallID(t)

	capture := f1Capture(t, journal, store)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{f1Observation(t, callID, "b-f1", "f1-obs-1", 1)}))

	call := f1Call(t, callID, "b-f1")
	_, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(),
		Max: billing.Money{Nano: 100_000, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
	require.NoError(t, err)
	require.NoError(t, store.AppendCallUsage(ctx, call))
	require.NoError(t, store.AppendCallLegUsage(ctx, f1ScalarLeg(t, callID, "b-f1")))

	claimed, err := store.ClaimCompleteCalls(ctx, 10)
	require.NoError(t, err)
	found := false
	for _, complete := range claimed {
		if complete.Closure.CallID == callID {
			found = true
		}
	}
	require.True(t, found, "legitimate V1 closure must stay claimable after shadow-first capture with its exposure present")

	f1SettleV1(t, ctx, store, call, 100_000, 25_000)
	gotAccount, err := store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, int64(975_000), gotAccount.BalanceNano)
	require.Len(t, f1ObservationsForBLeg(t, journal, "b-f1"), 1, "V2 observations must survive the V1 lifecycle")
}

func TestPhase172F1V1FirstKeepsShadowObservable(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	f1ProvisionAccount(t, ctx, store)
	callID := mustShadowCallID(t)

	call := f1Call(t, callID, "b-f1")
	require.NoError(t, store.AppendCallUsage(ctx, call))
	require.NoError(t, store.AppendCallLegUsage(ctx, f1ScalarLeg(t, callID, "b-f1")))

	capture := f1Capture(t, journal, store)
	observation := f1Observation(t, callID, "b-f1", "f1-obs-1", 1)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))

	require.Len(t, f1ObservationsForBLeg(t, journal, "b-f1"), 1, "exact replay must stay idempotent")

	pending, err := store.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1, "exactly the legitimate V1 leg may be pending")
	worker, err := billing.NewCallProviderCostWorker(store, store, f1ProviderCostResolver{}, 4)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	journals, err := store.JournalTransactions(ctx, "f1-acct")
	require.NoError(t, err)
	require.Len(t, journals, 1)
	require.Equal(t, "provider_call_cogs", journals[0].OperationKind)
	require.Len(t, f1ObservationsForBLeg(t, journal, "b-f1"), 1)
}

type f1ProviderCostResolver struct{}

func (f1ProviderCostResolver) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	return billing.RateProviderCost(leg, billing.OperatorRateSet{}, "USD")
}

func f1RatingInput(t *testing.T, observation metering.Observation) economics.PostUsageRatingInput {
	t.Helper()
	return economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
}

func TestPhase172F1SameBLegProjectionAndEvidence(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	f1ProvisionAccount(t, ctx, store)
	callID := mustShadowCallID(t)

	require.NoError(t, store.AppendCallLegUsage(ctx, f1ScalarLeg(t, callID, "b-f1")))
	capture := f1Capture(t, journal, store)
	observation := f1Observation(t, callID, "b-f1", "f1-obs-1", 1)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))

	row, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-f1"))
	require.NoError(t, err)
	require.Empty(t, row.Observations, "V1 row must keep its scalar projection without V2 observations")
	require.True(t, row.Evidence.Cost.Present)

	observations := f1ObservationsForBLeg(t, journal, "b-f1")
	require.Len(t, observations, 1)
	require.Len(t, observations[0].Measures, 1, "V2 source-separated evidence lives in observation storage")

	ref, err := observations[0].Ref("test")
	require.NoError(t, err)
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: "f1-head",
		Subject: observation.Subject, EvidenceRevision: 1,
		Input:     f1RatingInput(t, observation),
		CreatedAt: time.Unix(1_700_210_000, 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	require.NotEmpty(t, normalized.InputSetHash)
	require.Equal(t, []metering.ObservationRef{ref}, normalized.Input.ObservationRefs)
}

func openF1FileBillingStore(t *testing.T, path string) (*DurableStore, func()) {
	t.Helper()
	return openRefinement82FileBillingStore(t, path, "test")
}

func openF1FileJournal(t *testing.T, path string) (*journalstore.DurableStore, func()) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	journal, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "test"})
	require.NoError(t, err)
	return journal, func() {
		_ = journal.Close()
		_ = sqlDB.Close()
	}
}

func TestPhase172F1ReplayAndReopen(t *testing.T) {
	billingPath := t.TempDir() + "/f1-billing.sqlite"
	journalPath := t.TempDir() + "/f1-journal.sqlite"
	billingStore, closeBilling := openF1FileBillingStore(t, billingPath)
	journalStore, closeJournal := openF1FileJournal(t, journalPath)
	ctx := context.Background()
	callID := mustShadowCallID(t)

	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journalStore, billingStore, billingStore, billingStore, shadowV2TestRater(t),
	)
	require.NoError(t, err)
	observation := f1Observation(t, callID, "b-f1", "f1-obs-1", 1)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))
	require.Len(t, f1ObservationsForBLeg(t, journalStore, "b-f1"), 1)

	require.NoError(t, billingStore.AppendCallLegUsage(ctx, f1ScalarLeg(t, callID, "b-f1")))
	closeBilling()
	closeJournal()

	reopenedBilling, closeReopenedBilling := openF1FileBillingStore(t, billingPath)
	defer closeReopenedBilling()
	reopenedJournal, closeReopenedJournal := openF1FileJournal(t, journalPath)
	defer closeReopenedJournal()
	require.Len(t, f1ObservationsForBLeg(t, reopenedJournal, "b-f1"), 1)
	recapture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		reopenedJournal, reopenedBilling, reopenedBilling, reopenedBilling, shadowV2TestRater(t),
	)
	require.NoError(t, err)
	require.NoError(t, recapture.CaptureObservations(ctx, []metering.Observation{observation}))
	require.Len(t, f1ObservationsForBLeg(t, reopenedJournal, "b-f1"), 1, "replay after reopen must stay idempotent")
	pending, err := reopenedBilling.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the legitimate V1 leg keeps exactly one pending item across reopen")
}
