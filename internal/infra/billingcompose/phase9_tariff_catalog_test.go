package billingcompose_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9SnapshotCatalogPublishesAndReplaysTariffMaterial(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	if err := c.PutPricing(pricing); err != nil {
		t.Fatalf("PutPricing: %v", err)
	}
	tariff, err := c.Tariff(pricing.Ref)
	if err != nil {
		t.Fatalf("Tariff: %v", err)
	}
	if tariff.LegacySemantics != billing.LegacyScalarSemantics || tariff.Content.ContentHash != tariff.ContentHash() {
		t.Fatalf("legacy tariff=%+v", tariff)
	}
	if len(tariff.Rules) != 3 {
		t.Fatalf("legacy tariff rules=%d, want input/output/fixed", len(tariff.Rules))
	}
	tariff.Rules[0].ID = "mutated"
	replay, err := c.ResolveTariff(context.Background(), pricing.Ref)
	if err != nil {
		t.Fatalf("ResolveTariff: %v", err)
	}
	if replay.Rules[0].ID == "mutated" {
		t.Fatal("catalog returned a mutable tariff body")
	}
	pricing.Ref.EffectiveAt = time.Unix(99, 0)
	pricing.Ref.FetchedAt = time.Unix(100, 0)
	if err := c.PutPricing(pricing); err != nil {
		t.Fatalf("timestamp-only tariff replay: %v", err)
	}
	if _, err := c.Snapshot(context.Background()); err == nil {
		// A source view needs configured defaults; this assertion documents
		// that publishing a card alone does not invent a default policy.
		t.Fatal("unexpected default snapshot without SetDefaults")
	}
}

func TestPhase9SnapshotCatalogAcceptsGenericTariffWithoutScalarCard(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "phase9.catalog.v1"}
	price, err := metering.ParseDecimal("2")
	if err != nil {
		t.Fatal(err)
	}
	tariff := economics.NewTariffSnapshot(economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "generic", Version: "v1"}, RaterID: "reference",
	}, "USD", []economics.RatingRule{{ID: "image", Component: &key, Currency: "USD", UnitPrice: &price}})
	c := billingcompose.NewSnapshotCatalog()
	if err := c.PutTariff(tariff); err != nil {
		t.Fatalf("PutTariff: %v", err)
	}
	got, err := c.Tariff(billing.VersionRef{ID: "generic", Version: "v1"})
	if err != nil {
		t.Fatalf("Tariff: %v", err)
	}
	if got.ContentHash() != tariff.ContentHash() || got.Rules[0].Component.CanonicalKey() != key.CanonicalKey() {
		t.Fatalf("generic tariff replay=%+v", got)
	}
}

func TestPhase9SnapshotCatalogRejectsAmbiguousTariffRules(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "phase9.catalog.v1"}
	priceOne, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	priceTwo, err := metering.ParseDecimal("2")
	if err != nil {
		t.Fatal(err)
	}
	tariff, err := economics.BuildTariffSnapshot(economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "ambiguous", Version: "v1"}, RaterID: "reference",
	}, "USD", []economics.RatingRule{
		{ID: "first", Component: &key, Currency: "USD", UnitPrice: &priceOne, Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}}},
		{ID: "second", Component: &key, Currency: "USD", UnitPrice: &priceTwo, Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := billingcompose.NewSnapshotCatalog().PutTariff(tariff); err == nil {
		t.Fatal("ambiguous tariff publication unexpectedly succeeded")
	}
}
