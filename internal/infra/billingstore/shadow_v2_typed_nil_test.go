package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

type cluster2NilEvidencePtr struct{ marker int }

func (*cluster2NilEvidencePtr) AppendObservations(context.Context, []metering.Observation) error {
	return nil
}

type cluster2EvidenceFunc func(observations []metering.Observation) error

func (f cluster2EvidenceFunc) AppendObservations(_ context.Context, observations []metering.Observation) error {
	return f(observations)
}

type cluster2NilWorkPtr struct{ marker int }

func (*cluster2NilWorkPtr) AppendEconomicRevisionWork(context.Context, billing.EconomicRevisionWork) error {
	return nil
}

type cluster2WorkFunc func(work billing.EconomicRevisionWork) error

func (f cluster2WorkFunc) AppendEconomicRevisionWork(_ context.Context, work billing.EconomicRevisionWork) error {
	return f(work)
}

type cluster2NilResultPtr struct{ marker int }

func (*cluster2NilResultPtr) AppendEconomicRevisionResult(context.Context, billing.EconomicRevisionWork, billing.EconomicRevisionResult) error {
	return nil
}

type cluster2ResultFunc func(work billing.EconomicRevisionWork) error

func (f cluster2ResultFunc) AppendEconomicRevisionResult(_ context.Context, work billing.EconomicRevisionWork, _ billing.EconomicRevisionResult) error {
	return f(work)
}

type cluster2NilReconPtr struct{ marker int }

func (*cluster2NilReconPtr) AppendEconomicRevisionReconciliation(context.Context, billing.EconomicRevisionWork, billing.EconomicReconciliation) error {
	return nil
}

func (*cluster2NilReconPtr) HasEconomicRevisionReconciliation(context.Context, billing.EconomicRevisionIdentity) (bool, error) {
	return false, nil
}

type cluster2ReconFunc func(op string) error

func (f cluster2ReconFunc) AppendEconomicRevisionReconciliation(context.Context, billing.EconomicRevisionWork, billing.EconomicReconciliation) error {
	return f("append")
}

func (f cluster2ReconFunc) HasEconomicRevisionReconciliation(context.Context, billing.EconomicRevisionIdentity) (bool, error) {
	if err := f("has"); err != nil {
		return false, err
	}
	return false, nil
}

type cluster2NilRaterPtr struct{ marker int }

func (r *cluster2NilRaterPtr) Rate(_ context.Context, _ economics.PostUsageRatingInput) (economics.Valuation, error) {
	return economics.Valuation{Completeness: r.completeness()}, nil
}

func (r *cluster2NilRaterPtr) completeness() economics.Completeness {
	return economics.CompletenessPartial
}

type cluster2RaterFunc func(input economics.PostUsageRatingInput) (economics.Valuation, error)

func (f cluster2RaterFunc) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	return f(input)
}

func cluster2ValidShadowPorts(t *testing.T) (billing.ShadowEvidenceSink, billing.EconomicRevisionWorkAppender, billing.EconomicRevisionResultStore, billing.EconomicRevisionReconciliationStore, billing.PostUsageRater) {
	t.Helper()
	journal := openF1Journal(t)
	store := newSQLiteTestStore(t)
	return journal, store, store, store, stubShadowRater{}
}

func TestPhase172Cluster2ShadowCaptureRejectsTypedNilPorts(t *testing.T) {
	t.Parallel()
	evidence, work, results, recons, rater := cluster2ValidShadowPorts(t)
	cfg := ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16}

	var nilEvidence *cluster2NilEvidencePtr
	if _, err := NewShadowV2Capture(cfg, nilEvidence, work, results, recons, rater); err == nil {
		t.Fatal("typed-nil ShadowEvidenceSink pointer accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	var nilEvidenceFunc cluster2EvidenceFunc
	if _, err := NewShadowV2Capture(cfg, nilEvidenceFunc, work, results, recons, rater); err == nil {
		t.Fatal("typed-nil ShadowEvidenceSink func accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	// The ordinary terminal handoff must not satisfy the capture-only port:
	// a TerminalUsageSink-only value can never be passed where shadow evidence
	// is required, so shadow evidence cannot route into V1 posting/claim work.
	type ordinaryTerminalOnly struct{ billing.TerminalUsageSink }
	var terminalOnly any = ordinaryTerminalOnly{newSQLiteTestStore(t)}
	if _, ok := terminalOnly.(billing.ShadowEvidenceSink); ok {
		t.Fatal("ordinary TerminalUsageSink satisfies ShadowEvidenceSink")
	}

	var nilWork *cluster2NilWorkPtr
	if _, err := NewShadowV2Capture(cfg, evidence, nilWork, results, recons, rater); err == nil {
		t.Fatal("typed-nil EconomicRevisionWorkAppender pointer accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	var nilWorkFunc cluster2WorkFunc
	if _, err := NewShadowV2Capture(cfg, evidence, nilWorkFunc, results, recons, rater); err == nil {
		t.Fatal("typed-nil EconomicRevisionWorkAppender func accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	var nilResults *cluster2NilResultPtr
	if _, err := NewShadowV2Capture(cfg, evidence, work, nilResults, recons, rater); err == nil {
		t.Fatal("typed-nil EconomicRevisionResultStore pointer accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	var nilResultsFunc cluster2ResultFunc
	if _, err := NewShadowV2Capture(cfg, evidence, work, nilResultsFunc, recons, rater); err == nil {
		t.Fatal("typed-nil EconomicRevisionResultStore func accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	var nilRecons *cluster2NilReconPtr
	if _, err := NewShadowV2Capture(cfg, evidence, work, results, nilRecons, rater); err == nil {
		t.Fatal("typed-nil EconomicRevisionReconciliationStore pointer accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	var nilReconsFunc cluster2ReconFunc
	if _, err := NewShadowV2Capture(cfg, evidence, work, results, nilReconsFunc, rater); err == nil {
		t.Fatal("typed-nil EconomicRevisionReconciliationStore func accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	var nilRater *cluster2NilRaterPtr
	if _, err := NewShadowV2Capture(cfg, evidence, work, results, recons, nilRater); err == nil {
		t.Fatal("typed-nil PostUsageRater pointer accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}

	var nilRaterFunc cluster2RaterFunc
	if _, err := NewShadowV2Capture(cfg, evidence, work, results, recons, nilRaterFunc); err == nil {
		t.Fatal("typed-nil PostUsageRater func accepted")
	} else {
		require.ErrorIs(t, err, ErrShadowV2Incomplete)
	}
}

func TestPhase172Cluster2ShadowCaptureNilHandleCannotPanic(t *testing.T) {
	t.Parallel()
	var nilCapture *ShadowV2Capture
	require.ErrorIs(t, nilCapture.CaptureObservations(context.Background(), nil), ErrShadowV2Incomplete)
	work := billing.EconomicRevisionWork{}
	require.ErrorIs(t, nilCapture.AppendWork(context.Background(), work), ErrShadowV2Incomplete)
	require.ErrorIs(t, nilCapture.RateAndPersist(context.Background(), work), ErrShadowV2Incomplete)
	require.ErrorIs(t, nilCapture.AppendReconciliation(context.Background(), work, billing.EconomicReconciliation{}), ErrShadowV2Incomplete)
}
