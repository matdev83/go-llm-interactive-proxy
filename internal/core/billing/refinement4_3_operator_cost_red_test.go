package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

const refinement43CallID BillingCallID = "bc_00000000000000000000000000000043"

type refinement43ProviderCostPoster struct {
	mu       sync.Mutex
	inputs   []ProviderCostRevisionInput
	failOnce bool
}

func (p *refinement43ProviderCostPoster) ApplyProviderCostRevision(_ context.Context, input ProviderCostRevisionInput) (ProviderCostRevisionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs = append(p.inputs, input)
	if p.failOnce {
		p.failOnce = false
		return ProviderCostRevisionResult{}, errors.New("temporary provider posting failure")
	}
	return ProviderCostRevisionResult{Applied: true, CurrentAmount: input.Cost.Subtotal("USD"), Delta: input.Cost.Subtotal("USD")}, nil
}

func refinement43OperatorObservation(t *testing.T, amount string, payer metering.PaymentPartyKind) metering.Observation {
	t.Helper()
	value, err := metering.ParseDecimal(amount)
	require.NoError(t, err)
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "store", AccountID: "acct", ALegID: "a-leg",
		BillingCallID: refinement43CallID.String(), BLegID: "b-leg",
	}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "refinement43-charge-" + amount,
		SourceEventKey: "refinement43-charge-" + amount, Revision: 1,
		StreamID: "refinement43-stream", Sequence: 1, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID,
			ALegID:  subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
		},
		Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(43, 0).UTC(),
		ReceivedAt: time.Unix(43, 0).UTC(), MappingRef: "refinement43.v1",
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "provider-charge", Amount: &value, Currency: "USD",
			Kind: metering.ChargeKindAggregate, Payer: metering.PaymentParty{Kind: payer},
		}},
	}
}

func refinement43Work(t *testing.T, revision uint64, amount string, payer metering.PaymentPartyKind) EconomicRevisionWork {
	t.Helper()
	observation := refinement43OperatorObservation(t, amount, payer)
	input := economics.RatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "refinement43-call", Payer: metering.PaymentParty{Kind: payer},
		Observations: []metering.Observation{observation},
		Rater:        economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "refinement43-rater", Version: "v1"}, RaterID: "reference"},
		AsOf:         time.Unix(43, 0).UTC(),
	}
	return EconomicRevisionWork{
		Queue: EconomicQueueProvider, HeadKey: "refinement43-provider-head", Subject: observation.Subject,
		EvidenceRevision: revision, Input: input, CreatedAt: time.Unix(43+int64(revision), 0).UTC(),
	}
}

type refinement43Rater struct{}

func (refinement43Rater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	return economics.Valuation{
		ID: "rater-output", Version: economics.ValuationVersionV2, Perspective: input.Perspective,
		Basis: input.Basis, Subject: input.Subject, Scope: input.Scope, InputObservations: refs,
		Completeness: economics.CompletenessPartial, CreatedAt: time.Unix(43, 0).UTC(),
	}, nil
}

func TestRefinement43EconomicWorkerPostsProviderCostBeforeCallClosure(t *testing.T) {
	queue := &economicRevisionTestQueue{}
	poster := &refinement43ProviderCostPoster{}
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	require.NoError(t, queue.Append(context.Background(), work))
	worker, err := NewEconomicRevisionWorkerWithProviderCost(queue, newEconomicRevisionTestResultStore(), refinement43Rater{}, poster, EconomicQueueProvider, 8)
	require.NoError(t, err)

	require.NoError(t, worker.ProcessOnce(context.Background()))
	poster.mu.Lock()
	defer poster.mu.Unlock()
	require.Len(t, poster.inputs, 1)
	require.Equal(t, refinement43CallID, poster.inputs[0].CallID)
	require.Equal(t, uint64(1), poster.inputs[0].EvidenceRevision)
	require.Equal(t, int64(10_000_000_000), poster.inputs[0].Cost.Subtotal("USD").Nano)
	// The provider post is scoped to the B-leg revision. No call/session
	// closure marker or A-leg finality is part of this input.
	require.Equal(t, metering.SubjectBLeg, poster.inputs[0].Subject.Kind)
	require.Equal(t, "b-leg", poster.inputs[0].Subject.BLegID)
}

func TestRefinement43ProviderCostRevisionUsesExactCorrectionAndExcludesCustomerPayer(t *testing.T) {
	first := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	correction := refinement43Work(t, 2, "8", metering.PaymentPartyOperator)
	firstResult := economics.Valuation{ID: "first", Version: economics.ValuationVersionV2, Subject: first.Subject, Basis: first.Input.Basis, Perspective: first.Input.Perspective, InputObservations: []metering.ObservationRef{{StoreID: "store", ObservationID: first.Input.Observations[0].ID, Revision: 1, PayloadHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, Completeness: economics.CompletenessPartial, CreatedAt: time.Unix(43, 0).UTC()}
	correctionResult := firstResult
	correctionResult.ID = "correction"
	correctionResult.InputObservations[0].ObservationID = correction.Input.Observations[0].ID

	firstInput, err := BuildProviderCostRevisionInput(first, firstResult)
	require.NoError(t, err)
	correctionInput, err := BuildProviderCostRevisionInput(correction, correctionResult)
	require.NoError(t, err)
	require.Equal(t, int64(10_000_000_000), firstInput.Cost.Subtotal("USD").Nano)
	require.Equal(t, int64(8_000_000_000), correctionInput.Cost.Subtotal("USD").Nano)

	customer := refinement43Work(t, 3, "12", metering.PaymentPartyCustomer)
	customerResult := correctionResult
	customerResult.ID = "customer"
	customerResult.InputObservations[0].ObservationID = customer.Input.Observations[0].ID
	customerInput, err := BuildProviderCostRevisionInput(customer, customerResult)
	require.NoError(t, err)
	require.False(t, customerInput.Cost.Payable)
	require.Equal(t, CostCompletenessKnown, customerInput.Cost.Completeness)
}

func TestRefinement43ProviderCostPostingFailureCanRetry(t *testing.T) {
	queue := &economicRevisionTestQueue{}
	results := newEconomicRevisionTestResultStore()
	poster := &refinement43ProviderCostPoster{failOnce: true}
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	require.NoError(t, queue.Append(context.Background(), work))
	worker, err := NewEconomicRevisionWorkerWithProviderCost(queue, results, refinement43Rater{}, poster, EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.Error(t, worker.ProcessOnce(context.Background()))
	require.NoError(t, worker.ProcessOnce(context.Background()))
	poster.mu.Lock()
	defer poster.mu.Unlock()
	require.Len(t, poster.inputs, 2)
}

func TestRefinement43CustomerQueueNeverPostsProviderCost(t *testing.T) {
	queue := &economicRevisionTestQueue{}
	poster := &refinement43ProviderCostPoster{}
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	work.Queue = EconomicQueueCustomer
	require.NoError(t, queue.Append(context.Background(), work))
	worker, err := NewEconomicRevisionWorkerWithProviderCost(queue, newEconomicRevisionTestResultStore(), refinement43Rater{}, poster, EconomicQueueCustomer, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(context.Background()))
	poster.mu.Lock()
	defer poster.mu.Unlock()
	require.Empty(t, poster.inputs)
}
