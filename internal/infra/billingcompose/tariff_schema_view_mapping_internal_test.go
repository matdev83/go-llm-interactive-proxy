package billingcompose

import (
	"context"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func schemaViewMappingTariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	price, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	parent := metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
		Unit: metering.UnitToken, SchemaID: "view:mapping:v1",
	}
	child := metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentCacheReadInputToken,
		Unit: metering.UnitToken, SchemaID: "view:mapping:v1",
	}
	snapshot, err := economics.BuildTariffSnapshotWithSchemas(
		economics.RatingSnapshotRef{
			VersionRef: economics.VersionRef{ID: "view-mapping", Version: "v1"}, RaterID: "reference",
		},
		"USD",
		[]economics.RatingRule{{ID: "input", Component: &parent, Currency: "USD", UnitPrice: &price}},
		[]metering.ComponentSchema{{
			ID: "view:mapping:v1", Version: "1",
			Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: parent, Child: child}},
		}},
	)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	return snapshot
}

// TestSnapshotCatalogSnapshotMapsFrozenSchemasToRatingView covers the exact C2A
// boundary: once the catalog's default tariff body carries frozen
// component-relationship schemas (the C2B provider opt-in publication), the
// default RatingCatalogView must carry them too, and reconstructing the view
// must reproduce the identical immutable identity. Provider schema publication
// is not yet reachable through the public catalog API, so this white-box test
// seeds the default body directly; the view mapping itself is production code.
func TestSnapshotCatalogSnapshotMapsFrozenSchemasToRatingView(t *testing.T) {
	t.Parallel()
	c := NewSnapshotCatalog()
	pricing := billing.PricingSnapshot{
		Ref:                  billing.VersionRef{ID: "view-mapping-pricing", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  100,
		OutputPerMillionNano: 200,
		InputRatePresent:     true,
		OutputRatePresent:    true,
	}
	policy := billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "view-mapping-policy", Version: "v1"},
		PricingRef:          pricing.Ref,
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
	}
	if err := c.PutPricing(pricing); err != nil {
		t.Fatalf("PutPricing: %v", err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	schemaTariff := schemaViewMappingTariff(t)
	if err := c.PutTariff(schemaTariff); err != nil {
		t.Fatalf("PutTariff: %v", err)
	}
	c.tariffs[c.defaultPricing] = schemaTariff.Clone()

	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.State != economics.SnapshotReady {
		t.Fatalf("snapshot state=%q, want ready", snap.State)
	}
	if !reflect.DeepEqual(snap.Value.Schemas, schemaTariff.Schemas) {
		t.Fatalf("RatingCatalogView dropped the frozen schemas:\n got  %+v\n want %+v",
			snap.Value.Schemas, schemaTariff.Schemas)
	}
	reconstructed, err := snap.Value.Tariff(snap.RatingRef(schemaTariff.Ref.RaterID))
	if err != nil {
		t.Fatalf("reconstruct via RatingCatalogView.Tariff: %v", err)
	}
	if reconstructed.Content.ContentHash != schemaTariff.Content.ContentHash {
		t.Fatalf("view round-trip rewrote the frozen content hash:\n got  %s\n want %s",
			reconstructed.Content.ContentHash, schemaTariff.Content.ContentHash)
	}
	if !reflect.DeepEqual(reconstructed.Schemas, schemaTariff.Schemas) {
		t.Fatalf("view round-trip changed the frozen schemas:\n got  %+v\n want %+v",
			reconstructed.Schemas, schemaTariff.Schemas)
	}

	reconstructed.Schemas[0].Relationships[0].Child.Component = "mutated-view-body"
	stored, err := c.DefaultTariff(context.Background())
	if err != nil {
		t.Fatalf("DefaultTariff: %v", err)
	}
	if stored.Schemas[0].Relationships[0].Child.Component != metering.ComponentCacheReadInputToken {
		t.Fatalf("catalog returned a mutable schema body: %+v", stored.Schemas[0])
	}
	if stored.Content.ContentHash != schemaTariff.Content.ContentHash {
		t.Fatalf("catalog frozen hash drifted after view mutation: %s", stored.Content.ContentHash)
	}
}
