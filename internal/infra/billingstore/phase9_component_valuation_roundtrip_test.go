package billingstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9IndependentValuationsRoundTripExactCostsAndRefs(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := metering.ComponentKey{
		Direction: metering.DirectionInput, Component: "phase9:meter", Unit: metering.UnitCount, SchemaID: "phase9.v1",
	}
	one, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	three, err := metering.ParseDecimal("3")
	if err != nil {
		t.Fatal(err)
	}
	tariff, err := economics.BuildTariffSnapshot(economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "phase9-roundtrip-tariff", Version: "v1"}, RaterID: "reference",
	}, "USD", []economics.RatingRule{{
		ID: "phase9-meter-rate", Component: &key, Currency: "USD", RateNumerator: &one, RateDenominator: &three,
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider := phase9RoundTripObservation(t, "provider", metering.OriginProvider, key, "5")
	provider.Charges = []metering.ReportedCharge{{
		ChargeItemID: "provider-charge", Amount: phase9RoundTripDecimal(t, "2.50"), Currency: "USD", Kind: metering.ChargeKindComponent, Component: &key,
	}}
	local := phase9RoundTripObservation(t, "local", metering.OriginLocal, key, "4")
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisLocalExpected,
		Subject: local.Subject, Observations: []metering.Observation{local, provider},
		Rater:        economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent: &economics.SnapshotContentRef{ContentRef: "phase9://rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:       tariff.Ref, TariffContent: &tariff.Content,
		// The trusted rater derives the canonical input-set identity from the
		// observations.  An empty caller value exercises that boundary while
		// retaining the round-trip assertions below.
		InputSetHash: "", QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "phase9://qualifiers/v1", ContentHash: strings.Repeat("3", 64)},
	}
	rater, err := billing.NewReferenceRater(tariff)
	if err != nil {
		t.Fatal(err)
	}
	valuations, err := billing.RateIndependentValuations(ctx, rater, input)
	if err != nil {
		t.Fatalf("RateIndependentValuations: %v", err)
	}
	if valuations.Expected == nil || valuations.ProviderQuantity == nil || valuations.ProviderReported == nil {
		t.Fatalf("independent valuations=%+v", valuations)
	}
	for _, valuation := range []*economics.Valuation{valuations.Expected, valuations.ProviderQuantity, valuations.ProviderReported} {
		want, err := valuation.CanonicalJSON()
		if err != nil {
			t.Fatalf("canonical %s: %v", valuation.Basis, err)
		}
		if err := store.AppendValuation(ctx, *valuation); err != nil {
			t.Fatalf("append %s: %v", valuation.Basis, err)
		}
		got, err := store.GetValuation(ctx, valuation.ID, valuation.Version)
		if err != nil {
			t.Fatalf("get %s: %v", valuation.Basis, err)
		}
		gotJSON, err := got.CanonicalJSON()
		if err != nil {
			t.Fatalf("canonical round trip %s: %v", valuation.Basis, err)
		}
		if string(gotJSON) != string(want) {
			t.Fatalf("round trip %s changed canonical valuation", valuation.Basis)
		}
	}
	if valuations.Expected.Lines[0].AmountNumerator == "" || valuations.Expected.Lines[0].AmountDenominator == "" {
		t.Fatalf("expected line lost exact rational amount: %+v", valuations.Expected.Lines[0])
	}
	if valuations.ProviderQuantity.Lines[0].AmountNumerator == "" || valuations.ProviderQuantity.Lines[0].AmountDenominator == "" {
		t.Fatalf("provider quantity line lost exact rational amount: %+v", valuations.ProviderQuantity.Lines[0])
	}
	if valuations.ProviderReported.Lines[0].Status != economics.RatingLineProviderReported {
		t.Fatalf("provider reported status=%q", valuations.ProviderReported.Lines[0].Status)
	}
}

func phase9RoundTripDecimal(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	d, err := metering.ParseDecimal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func phase9RoundTripObservation(t *testing.T, id, origin string, key metering.ComponentKey, quantity string) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionLocalTransport
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	now := phase9RoundTripTime()
	return metering.Observation{
		Version: 2, ID: id, SourceEventKey: id, Revision: 1, StreamID: "phase9-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant", AccountID: "account", ALegID: "a", BillingCallID: "call", BLegID: "b"},
		Correlation: metering.CorrelationV2{StoreID: "test", TenantID: "tenant", CallID: "call", BillingCallID: "call", ALegID: "a", BLegID: "b"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "phase9",
		Measures: []metering.Measure{{Key: key, Value: phase9RoundTripDecimal(t, quantity), Quality: metering.QualityObserved}},
	}
}

func phase9RoundTripTime() time.Time {
	return time.Unix(1_700_010_000, 0).UTC()
}
