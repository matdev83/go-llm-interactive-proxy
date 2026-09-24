package billing

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestObservationEconomicWorkBuilderSeparatesProviderAndCustomerPlanes(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_100, 0).UTC()
	provider := bridgeTestObservation("provider", metering.OriginProvider, metering.BoundaryBackendIngress, 1, true)
	customerBoundary := bridgeTestObservation("customer-boundary", metering.OriginLocal, metering.BoundaryFrontendEgress, 1, false)
	customerQuantity := bridgeTestObservation("customer-quantity", metering.OriginLocal, metering.BoundaryBackendIngress, 1, false)

	builder, err := NewObservationEconomicWorkBuilder(ObservationEconomicWorkBuilderConfig{
		Now: func() time.Time { return now },
		CustomerInput: func(_ context.Context, subject metering.SubjectRef, observations []metering.Observation) (economics.PostUsageRatingInput, error) {
			return economics.PostUsageRatingInput{
				Version: 2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisCustomerPolicy,
				Subject: subject, Scope: "b_leg", Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: subject.AccountID},
				Observations:         observations,
				Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater", Version: "v1"}, RaterID: "reference"},
				RaterContent:         &economics.SnapshotContentRef{ContentRef: "test://rater/v1", ContentHash: bridgeHash('1')},
				Policy:               economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v1"}, PolicyID: "policy"},
				PolicyContent:        &economics.SnapshotContentRef{ContentRef: "test://policy/v1", ContentHash: bridgeHash('2')},
				QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "test://qualifiers/v1", ContentHash: bridgeHash('3')},
			}, nil
		},
	})
	require.NoError(t, err)

	work, err := builder.BuildEconomicRevisionWork(context.Background(), []metering.Observation{customerBoundary, provider, customerQuantity})
	require.NoError(t, err)
	require.Len(t, work, 2)

	var providerWork, customerWork EconomicRevisionWork
	for _, item := range work {
		switch item.Queue {
		case EconomicQueueProvider:
			providerWork = item
		case EconomicQueueCustomer:
			customerWork = item
		default:
			t.Fatalf("unexpected queue %q", item.Queue)
		}
	}
	require.Equal(t, economics.BasisProviderReported, providerWork.Input.Basis)
	require.Len(t, providerWork.Input.Observations, 1, "customer-boundary and local quantity evidence must not enter provider P")
	require.Equal(t, provider.ID, providerWork.Input.Observations[0].ID)
	require.Equal(t, economics.BasisCustomerPolicy, customerWork.Input.Basis)
	require.Len(t, customerWork.Input.Observations, 2, "frontend customer-boundary evidence must not enter customer B-leg quantity")
	ids := []string{customerWork.Input.Observations[0].ID, customerWork.Input.Observations[1].ID}
	require.Contains(t, ids, provider.ID)
	require.Contains(t, ids, customerQuantity.ID)
	require.NotContains(t, ids, customerBoundary.ID)
	require.NotEmpty(t, providerWork.InputSetHash)
	require.NotEmpty(t, customerWork.InputSetHash)
	providerIdentity, err := providerWork.Identity()
	require.NoError(t, err)
	customerIdentity, err := customerWork.Identity()
	require.NoError(t, err)
	require.NotEqual(t, providerIdentity.Key(), customerIdentity.Key())
}

func TestObservationEconomicWorkBuilderCorrectionChangesIdentityAndReorderingDoesNot(t *testing.T) {
	t.Parallel()
	first := bridgeTestObservation("first", metering.OriginProvider, metering.BoundaryBackendIngress, 1, true)
	correction := bridgeTestObservation("correction", metering.OriginProvider, metering.BoundaryBackendIngress, 2, true)
	builder, err := NewObservationEconomicWorkBuilder(ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)

	ordered, err := builder.BuildEconomicRevisionWork(context.Background(), []metering.Observation{first, correction})
	require.NoError(t, err)
	require.Len(t, ordered, 1)
	require.Equal(t, uint64(2), ordered[0].EvidenceRevision)

	reordered, err := builder.BuildEconomicRevisionWork(context.Background(), []metering.Observation{correction, first})
	require.NoError(t, err)
	require.Len(t, reordered, 1)
	orderedIdentity, err := ordered[0].Identity()
	require.NoError(t, err)
	reorderedIdentity, err := reordered[0].Identity()
	require.NoError(t, err)
	require.Equal(t, orderedIdentity, reorderedIdentity)

	changed := correction.Clone()
	changed.Measures[0].Value = &metering.Decimal{Coefficient: "99", Scale: 0}
	changedWork, err := builder.BuildEconomicRevisionWork(context.Background(), []metering.Observation{first, changed})
	require.NoError(t, err)
	changedIdentity, err := changedWork[0].Identity()
	require.NoError(t, err)
	require.NotEqual(t, orderedIdentity, changedIdentity)
}

func TestObservationEconomicWorkBuilderKeepsProviderAccountScopesSeparate(t *testing.T) {
	t.Parallel()
	first := bridgeTestObservation("provider-a", metering.OriginProvider, metering.BoundaryBackendIngress, 1, true)
	second := bridgeTestObservation("provider-b", metering.OriginProvider, metering.BoundaryBackendIngress, 1, true)
	first.Correlation.ProviderAccountKey = "provider-a"
	second.Subject.ProviderAccountKey = "provider-b"
	second.Correlation.ProviderAccountKey = "provider-b"

	builder, err := NewObservationEconomicWorkBuilder(ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)
	work, err := builder.BuildEconomicRevisionWork(context.Background(), []metering.Observation{first, second})
	require.NoError(t, err)
	require.Len(t, work, 2, "provider-account identity is part of the economic scope")
	require.NotEqual(t, work[0].HeadKey, work[1].HeadKey)
	require.NotEqual(t, work[0].Subject.ProviderAccountKey, work[1].Subject.ProviderAccountKey)
	conflict := first.Clone()
	conflict.Measures[0].Value = &metering.Decimal{Coefficient: "99", Scale: 0}
	_, err = builder.BuildEconomicRevisionWork(context.Background(), []metering.Observation{first, conflict})
	require.Error(t, err, "same durable observation identity with changed semantics must not be silently deduplicated")
}

func bridgeHash(ch byte) string {
	const hex = "0123456789abcdef"
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = hex[int(ch)%len(hex)]
	}
	return string(buf)
}

func bridgeTestObservation(id string, origin string, boundary metering.Boundary, revision uint64, charge bool) metering.Observation {
	now := time.Unix(1_700_000_000+int64(revision), 0).UTC()
	acquisition := metering.AcquisitionProviderResponse
	if origin == metering.OriginLocal {
		acquisition = metering.AcquisitionLocalTransport
	}
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:tokens", Unit: metering.UnitToken, SchemaID: "test:v1"}
	value := metering.Decimal{Coefficient: "5", Scale: 0}
	observation := metering.Observation{
		Version: 2, ID: id, SourceEventKey: "source-" + id, Revision: revision, StreamID: "bridge-stream", Sequence: revision,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: boundary, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "bridge-store", TenantID: "tenant", AccountID: "account", ALegID: "a", BillingCallID: "call", BLegID: "b", AttemptID: "attempt"},
		Correlation: metering.CorrelationV2{StoreID: "bridge-store", TenantID: "tenant", CallID: "call", BillingCallID: "call", ALegID: "a", BLegID: "b", AttemptID: "attempt"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "test:bridge:v1",
		Measures: []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
	}
	if charge {
		amount := metering.Decimal{Coefficient: "1", Scale: 0}
		observation.Charges = []metering.ReportedCharge{{ChargeItemID: "charge-" + id, Component: &key, Amount: &amount, Currency: "USD", Kind: metering.ChargeKindComponent}}
	}
	return observation
}
