package runtimebundle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

var errCluster2MonetaryUnreachable = errors.New("runtimebundle: cluster2 monetary double unreachable")

type cluster2ComposeNilEvidencePtr struct{ marker int }

func (*cluster2ComposeNilEvidencePtr) AppendObservations(context.Context, []metering.Observation) error {
	return nil
}

type cluster2ComposeEvidenceFunc func(observations []metering.Observation) error

func (f cluster2ComposeEvidenceFunc) AppendObservations(_ context.Context, observations []metering.Observation) error {
	return f(observations)
}

type cluster2ComposeNilWorkPtr struct{ marker int }

func (*cluster2ComposeNilWorkPtr) AppendEconomicRevisionWork(context.Context, billing.EconomicRevisionWork) error {
	return nil
}

type cluster2ComposeWorkFunc func(work billing.EconomicRevisionWork) error

func (f cluster2ComposeWorkFunc) AppendEconomicRevisionWork(_ context.Context, work billing.EconomicRevisionWork) error {
	return f(work)
}

type cluster2ComposeNilResultPtr struct{ marker int }

func (*cluster2ComposeNilResultPtr) AppendEconomicRevisionResult(context.Context, billing.EconomicRevisionWork, billing.EconomicRevisionResult) error {
	return nil
}

type cluster2ComposeResultFunc func(work billing.EconomicRevisionWork) error

func (f cluster2ComposeResultFunc) AppendEconomicRevisionResult(_ context.Context, work billing.EconomicRevisionWork, _ billing.EconomicRevisionResult) error {
	return f(work)
}

type cluster2ComposeNilReconPtr struct{ marker int }

func (*cluster2ComposeNilReconPtr) AppendEconomicRevisionReconciliation(context.Context, billing.EconomicRevisionWork, billing.EconomicReconciliation) error {
	return nil
}

func (*cluster2ComposeNilReconPtr) HasEconomicRevisionReconciliation(context.Context, billing.EconomicRevisionIdentity) (bool, error) {
	return false, nil
}

type cluster2ComposeReconFunc func(op string) error

func (f cluster2ComposeReconFunc) AppendEconomicRevisionReconciliation(context.Context, billing.EconomicRevisionWork, billing.EconomicReconciliation) error {
	return f("append")
}

func (f cluster2ComposeReconFunc) HasEconomicRevisionReconciliation(context.Context, billing.EconomicRevisionIdentity) (bool, error) {
	if err := f("has"); err != nil {
		return false, err
	}
	return false, nil
}

type cluster2ComposeNilRaterPtr struct{ marker int }

func (r *cluster2ComposeNilRaterPtr) Rate(_ context.Context, _ economics.PostUsageRatingInput) (economics.Valuation, error) {
	return economics.Valuation{Completeness: r.completeness()}, nil
}

func (r *cluster2ComposeNilRaterPtr) completeness() economics.Completeness {
	return economics.CompletenessPartial
}

type cluster2ComposeRaterFunc func(input economics.PostUsageRatingInput) (economics.Valuation, error)

func (f cluster2ComposeRaterFunc) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	return f(input)
}

type cluster2ComposeNilSettlementPtr struct{ marker int }

func (*cluster2ComposeNilSettlementPtr) ApplyCallBillingResult(context.Context, billing.ApplyCallBillingInput) (billing.CallSettlement, error) {
	return billing.CallSettlement{}, errCluster2MonetaryUnreachable
}

type cluster2ComposeSettlementFunc func(input billing.ApplyCallBillingInput) (billing.CallSettlement, error)

func (f cluster2ComposeSettlementFunc) ApplyCallBillingResult(_ context.Context, input billing.ApplyCallBillingInput) (billing.CallSettlement, error) {
	return f(input)
}

func cluster2ValidComposeInput(t *testing.T) runtimebundle.ShadowV2CaptureInput {
	t.Helper()
	store := newShadowBillingStore(t)
	journal := newShadowJournal(t)
	return runtimebundle.ShadowV2CaptureInput{
		StoreID:             "test",
		MaxObservations:     16,
		EvidenceSink:        journal,
		WorkAppender:        store,
		ResultStore:         store,
		ReconciliationStore: store,
		Rater:               newShadowRater(t),
		V1Settlement:        store,
	}
}

func TestPhase172Cluster2ComposeRejectsTypedNilPorts(t *testing.T) {
	t.Parallel()
	valid := cluster2ValidComposeInput(t)

	var nilEvidence *cluster2ComposeNilEvidencePtr
	bad := valid
	bad.EvidenceSink = nilEvidence
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil ShadowEvidenceSink pointer accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilEvidenceFunc cluster2ComposeEvidenceFunc
	bad = valid
	bad.EvidenceSink = nilEvidenceFunc
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil ShadowEvidenceSink func accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilWork *cluster2ComposeNilWorkPtr
	bad = valid
	bad.WorkAppender = nilWork
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil EconomicRevisionWorkAppender pointer accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilWorkFunc cluster2ComposeWorkFunc
	bad = valid
	bad.WorkAppender = nilWorkFunc
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil EconomicRevisionWorkAppender func accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilResults *cluster2ComposeNilResultPtr
	bad = valid
	bad.ResultStore = nilResults
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil EconomicRevisionResultStore pointer accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilResultsFunc cluster2ComposeResultFunc
	bad = valid
	bad.ResultStore = nilResultsFunc
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil EconomicRevisionResultStore func accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilRecons *cluster2ComposeNilReconPtr
	bad = valid
	bad.ReconciliationStore = nilRecons
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil EconomicRevisionReconciliationStore pointer accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilReconsFunc cluster2ComposeReconFunc
	bad = valid
	bad.ReconciliationStore = nilReconsFunc
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil EconomicRevisionReconciliationStore func accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilRater *cluster2ComposeNilRaterPtr
	bad = valid
	bad.Rater = nilRater
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil PostUsageRater pointer accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilRaterFunc cluster2ComposeRaterFunc
	bad = valid
	bad.Rater = nilRaterFunc
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil PostUsageRater func accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilSettlement *cluster2ComposeNilSettlementPtr
	bad = valid
	bad.V1Settlement = nilSettlement
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil V1Settlement pointer accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}

	var nilSettlementFunc cluster2ComposeSettlementFunc
	bad = valid
	bad.V1Settlement = nilSettlementFunc
	if _, err := runtimebundle.ComposeShadowV2Capture(bad); err == nil {
		t.Fatal("typed-nil V1Settlement func accepted")
	} else {
		require.ErrorIs(t, err, runtimebundle.ErrShadowV2CaptureIncomplete)
	}
}

func TestPhase172Cluster2ComposeZeroHandleCannotPanic(t *testing.T) {
	t.Parallel()
	var zero runtimebundle.ShadowV2Handle
	require.ErrorIs(t, zero.CaptureObservations(context.Background(), nil), runtimebundle.ErrShadowV2CaptureIncomplete)
	work := billing.EconomicRevisionWork{}
	require.ErrorIs(t, zero.AppendWork(context.Background(), work), runtimebundle.ErrShadowV2CaptureIncomplete)
	require.ErrorIs(t, zero.RateAndPersist(context.Background(), work), runtimebundle.ErrShadowV2CaptureIncomplete)
	require.ErrorIs(t, zero.AppendReconciliation(context.Background(), work, billing.EconomicReconciliation{}), runtimebundle.ErrShadowV2CaptureIncomplete)
}
