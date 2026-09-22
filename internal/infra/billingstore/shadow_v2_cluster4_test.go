package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/stretchr/testify/require"
)

func TestPhase172Cluster4FinancialProbesPropagateErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, err := newSQLiteTestStore(t).GetAccount(ctx, "missing-acct")
	require.Error(t, err, "durable account read on a missing account must fail, never read as zero")

	closed := newSQLiteTestStore(t)
	require.NoError(t, closed.Close())
	_, err = closed.GetAccount(ctx, "any-acct")
	require.Error(t, err, "durable account read on a closed store must fail, never read as zero")
	_, err = closed.JournalTransactions(ctx, "any-acct")
	require.Error(t, err, "durable journal read on a closed store must fail, never read as empty")
	_, err = closed.HasEconomicRevisionResult(ctx, billing.EconomicRevisionIdentity{})
	require.Error(t, err, "durable probe on a closed store must fail, never report absent")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newSQLiteTestStore(t).GetAccount(canceled, "any-acct")
	require.Error(t, err, "durable account read on a canceled context must fail")

	var unitOps int
	err = closed.db.NewRaw(`SELECT COUNT(1) FROM billing_unit_operations`).Scan(context.Background(), &unitOps)
	require.Error(t, err, "unit count query on a closed store must fail, never report zero")
}

func TestPhase172Cluster4RealReconciliationPersistsAndQueries(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()

	rating := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 43, "c4-real-recon")
	rater := shadowV2TestRater(t)
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)
	require.NoError(t, capture.AppendWork(ctx, rating))
	require.NoError(t, capture.RateAndPersist(ctx, rating))

	reconWork := cluster4ReconWorkFor(t, rating)
	require.NoError(t, capture.AppendWork(ctx, reconWork))
	reconciliation := cluster4RealReconciliation(t, ctx, store, reconWork)
	require.NoError(t, capture.AppendReconciliation(ctx, reconWork, reconciliation))

	record, err := store.GetReconciliation(ctx, reconciliation.ID, reconciliation.Version)
	require.NoError(t, err)
	require.Equal(t, reconciliation.InputSetHash, record.InputSetHash)
	require.JSONEq(t, string(reconciliation.ResultJSON), string(record.ResultJSON))
	probed, err := store.HasEconomicRevisionReconciliation(ctx, mustCluster4Identity(t, reconWork))
	require.NoError(t, err)
	require.True(t, probed)
}

func TestPhase172Cluster4ReconciliationMismatchFailsClosed(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()

	rating := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 44, "c4-recon-mismatch")
	rater := shadowV2TestRater(t)
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)
	require.NoError(t, capture.AppendWork(ctx, rating))
	require.NoError(t, capture.RateAndPersist(ctx, rating))

	reconWork := cluster4ReconWorkFor(t, rating)
	require.NoError(t, capture.AppendWork(ctx, reconWork))
	valid := cluster4RealReconciliation(t, ctx, store, reconWork)

	mutants := map[string]func(*billing.EconomicReconciliation){
		"wrong id":         func(r *billing.EconomicReconciliation) { r.ID = "reconciliation:v1:deadbeef" },
		"wrong version":    func(r *billing.EconomicReconciliation) { r.Version++ },
		"wrong input hash": func(r *billing.EconomicReconciliation) { r.InputSetHash = valid.InputSetHash[:63] + "0" },
		"foreign subject":  func(r *billing.EconomicReconciliation) { r.Subject.BLegID = "b-foreign" },
		"wrong basis":      func(r *billing.EconomicReconciliation) { r.Basis = "customer_policy" },
		"malformed result": func(r *billing.EconomicReconciliation) { r.ResultJSON = []byte(`{invalid`) },
		"empty result":     func(r *billing.EconomicReconciliation) { r.ResultJSON = nil },
	}
	for name, mutate := range mutants {
		mutant := valid
		mutate(&mutant)
		require.Error(t, capture.AppendReconciliation(ctx, reconWork, mutant), "mutant %s must fail closed", name)
	}

	// A dependency pointing at a never-persisted rating resolves to no output.
	other := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 45, "c4-recon-unpersisted")
	otherNormalized, err := other.Normalize()
	require.NoError(t, err)
	otherIdentity, err := otherNormalized.Identity()
	require.NoError(t, err)
	otherDep, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(otherNormalized.Queue), otherIdentity)
	require.NoError(t, err)
	_, err = store.LoadEconomicRevisionDependencyOutput(ctx, otherDep)
	require.Error(t, err, "unpersisted dependency output must fail closed, never read as zero valuation")
}
