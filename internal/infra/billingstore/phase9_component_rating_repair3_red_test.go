package billingstore

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair3_ValuationIdentityIncludesSnapshotContext(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: "phase9:identity-meter", Unit: metering.UnitCount, SchemaID: "phase9.v1"}
	local := phase9RoundTripObservation(t, "identity-local", metering.OriginLocal, key, "5")
	tariffV1 := phase9Repair3Tariff(t, key, "v1", "1")
	tariffV2 := phase9Repair3Tariff(t, key, "v2", "2")

	input := func(tariff economics.TariffSnapshot) economics.PostUsageRatingInput {
		return economics.PostUsageRatingInput{
			Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisLocalExpected,
			Subject: local.Subject, Scope: "call", Observations: []metering.Observation{local},
			Rater:        economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-rater", Version: "v1"}, RaterID: "reference"},
			RaterContent: &economics.SnapshotContentRef{ContentRef: "phase9://rater/v1", ContentHash: strings.Repeat("1", 64)},
			Tariff:       tariff.Ref, TariffContent: &tariff.Content,
			InputSetHash:         "",
			QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "phase9://qualifiers/v1", ContentHash: strings.Repeat("3", 64)},
		}
	}
	raterV1, err := billing.NewReferenceRater(tariffV1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := raterV1.Rate(ctx, input(tariffV1))
	if err != nil {
		t.Fatalf("v1 rating: %v", err)
	}
	raterV2, err := billing.NewReferenceRater(tariffV2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := raterV2.Rate(ctx, input(tariffV2))
	if err != nil {
		t.Fatalf("v2 rating: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("snapshot context was omitted from valuation identity: %q", first.ID)
	}
	if err := store.AppendValuation(ctx, first); err != nil {
		t.Fatalf("append v1 valuation: %v", err)
	}
	if err := store.AppendValuation(ctx, second); err != nil {
		t.Fatalf("append v2 valuation: %v", err)
	}
	if err := store.AppendValuation(ctx, second); err != nil {
		t.Fatalf("same-context replay: %v", err)
	}
	if _, err := store.GetValuation(ctx, first.ID, first.Version); err != nil {
		t.Fatalf("get v1 valuation: %v", err)
	}
	if _, err := store.GetValuation(ctx, second.ID, second.Version); err != nil {
		t.Fatalf("get v2 valuation: %v", err)
	}
}

func phase9Repair3Tariff(t *testing.T, key metering.ComponentKey, version, price string) economics.TariffSnapshot {
	t.Helper()
	rate := phase9RoundTripDecimal(t, price)
	snapshot, err := economics.BuildTariffSnapshot(economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "phase9-identity-tariff", Version: version}, RaterID: "reference",
	}, "USD", []economics.RatingRule{{
		ID: "phase9-identity-rate", Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: rate,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
