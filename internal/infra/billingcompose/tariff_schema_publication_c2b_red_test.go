package billingcompose_test

// C2B (PR #659 adversarial repair R7): public frozen-tariff publication.
//
// SnapshotCatalog.PutPricing always derives and publishes a schema-free legacy
// tariff at the pricing ref's id/version. A later PutTariff(schema-bearing) at
// that same id/version therefore fails ErrSnapshotImmutable, so the public
// production catalog could not install an opt-in schema-bearing default or
// route tariff. These tests pin the repaired first-publication boundary:
// PutPricingWithSchemas publishes the scalar card and its matching
// schema-bearing tariff atomically, caller-supplied schemas only, with the
// frozen content hash binding the schema material.

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func c2bInclusionSchemas(edge string) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID:      "c2b:inclusion:v1",
		Version: "1",
		Relationships: []metering.ComponentRelationship{{
			Kind:   metering.RelationshipSubset,
			Parent: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID},
			Child:  metering.ComponentKey{Direction: metering.DirectionInput, Component: edge, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID},
		}},
	}}
}

// c2bExpectedTariff builds the canonical schema-bearing legacy tariff the
// public catalog must reproduce from the scalar card plus explicit schemas.
func c2bExpectedTariff(t *testing.T, pricing billing.PricingSnapshot, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	tariff, err := billing.PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatalf("PricingSnapshotToTariff: %v", err)
	}
	tariff.Schemas = schemas
	tariff.Content = economics.SnapshotContentRef{}
	tariff, err = tariff.Canonical()
	if err != nil {
		t.Fatalf("Canonical schema-bearing tariff: %v", err)
	}
	return tariff
}

func c2bAssertResolved(t *testing.T, c *billingcompose.SnapshotCatalog, ref billing.VersionRef, want economics.TariffSnapshot) {
	t.Helper()
	ctx := context.Background()
	got, err := c.ResolveTariff(ctx, ref)
	if err != nil {
		t.Fatalf("ResolveTariff: %v", err)
	}
	if got.Content.ContentHash != want.Content.ContentHash {
		t.Fatalf("resolved hash = %s, want %s", got.Content.ContentHash, want.Content.ContentHash)
	}
	if !reflect.DeepEqual(got.Schemas, want.Schemas) {
		t.Fatalf("resolved schemas differ:\n got  %+v\n want %+v", got.Schemas, want.Schemas)
	}
}

func TestSnapshotCatalogPublishesSchemaBearingDefaultTariff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	policy := catalogPolicy()
	schemas := c2bInclusionSchemas(metering.ComponentCacheReadInputToken)

	if err := c.PutPricingWithSchemas(pricing, schemas); err != nil {
		t.Fatalf("PutPricingWithSchemas: %v", err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	want := c2bExpectedTariff(t, pricing, schemas)
	c2bAssertResolved(t, c, pricing.Ref, want)

	if def, err := c.DefaultTariff(ctx); err != nil {
		t.Fatalf("DefaultTariff: %v", err)
	} else if def.Content.ContentHash != want.Content.ContentHash || !reflect.DeepEqual(def.Schemas, want.Schemas) {
		t.Fatalf("DefaultTariff dropped frozen schemas: hash=%s schemas=%+v", def.Content.ContentHash, def.Schemas)
	}
	if route, err := c.RouteTariff(ctx, "backend", "model"); err != nil {
		t.Fatalf("RouteTariff: %v", err)
	} else if route.Content.ContentHash != want.Content.ContentHash || !reflect.DeepEqual(route.Schemas, want.Schemas) {
		t.Fatalf("RouteTariff dropped frozen schemas: hash=%s schemas=%+v", route.Content.ContentHash, route.Schemas)
	}
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !reflect.DeepEqual(snap.Value.Schemas, want.Schemas) {
		t.Fatalf("RatingCatalogView dropped frozen schemas:\n got  %+v\n want %+v", snap.Value.Schemas, want.Schemas)
	}
	if reconstructed, err := snap.Value.Tariff(snap.RatingRef(want.Ref.RaterID)); err != nil {
		t.Fatalf("view Tariff: %v", err)
	} else if reconstructed.Content.ContentHash != want.Content.ContentHash {
		t.Fatalf("view round-trip hash = %s, want %s", reconstructed.Content.ContentHash, want.Content.ContentHash)
	}

	// Identical replay is idempotent.
	if err := c.PutPricingWithSchemas(pricing, schemas); err != nil {
		t.Fatalf("identical schema replay: %v", err)
	}
	// Changed schema at the same id/version conflicts without altering the body.
	if err := c.PutPricingWithSchemas(pricing, c2bInclusionSchemas(metering.ComponentCacheWriteInputToken)); !errors.Is(err, billingcompose.ErrSnapshotImmutable) {
		t.Fatalf("changed schema error = %v, want ErrSnapshotImmutable", err)
	}
	c2bAssertResolved(t, c, pricing.Ref, want)

	// Returned bodies are independent frozen clones.
	got, err := c.ResolveTariff(ctx, pricing.Ref)
	if err != nil {
		t.Fatalf("ResolveTariff: %v", err)
	}
	got.Schemas[0].Relationships[0].Child.Component = "mutated-returned-body"
	again, err := c.ResolveTariff(ctx, pricing.Ref)
	if err != nil {
		t.Fatalf("ResolveTariff after mutation: %v", err)
	}
	if again.Schemas[0].Relationships[0].Child.Component != metering.ComponentCacheReadInputToken {
		t.Fatalf("catalog returned a mutable schema body: %+v", again.Schemas[0])
	}
	if again.Content.ContentHash != want.Content.ContentHash {
		t.Fatalf("frozen hash drifted after view mutation: %s", again.Content.ContentHash)
	}
	// Caller mutation of the supplied schema slice must not reach the frozen body.
	schemas[0].Relationships[0].Child.Component = "mutated-caller-material"
	final, err := c.ResolveTariff(ctx, pricing.Ref)
	if err != nil {
		t.Fatalf("ResolveTariff after caller mutation: %v", err)
	}
	if final.Schemas[0].Relationships[0].Child.Component != metering.ComponentCacheReadInputToken {
		t.Fatalf("caller mutation of supplied schemas altered the frozen body: %+v", final.Schemas[0])
	}
}

func TestSnapshotCatalogSchemaBearingRouteTariffIsolated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := billingcompose.NewSnapshotCatalog()
	def := catalogPricing()
	policy := catalogPolicy()
	if err := c.PutPricing(def); err != nil {
		t.Fatalf("PutPricing: %v", err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if err := c.SetDefaults(def.Ref, policy.Ref); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	defWant, err := billing.PricingSnapshotToTariff(def)
	if err != nil {
		t.Fatalf("PricingSnapshotToTariff: %v", err)
	}

	override := catalogPricing()
	override.Ref = billing.VersionRef{ID: "pricing-route", Version: "v1"}
	override.InputPerMillionNano = 321
	schemas := c2bInclusionSchemas(metering.ComponentCacheReadInputToken)
	if err := c.PutPricingWithSchemas(override, schemas); err != nil {
		t.Fatalf("PutPricingWithSchemas route override: %v", err)
	}
	if err := c.SetRoutePricing("backend", "special", override.Ref); err != nil {
		t.Fatalf("SetRoutePricing: %v", err)
	}

	wantOverride := c2bExpectedTariff(t, override, schemas)
	route, err := c.RouteTariff(ctx, "backend", "special")
	if err != nil {
		t.Fatalf("RouteTariff(special): %v", err)
	}
	if route.Content.ContentHash != wantOverride.Content.ContentHash || !reflect.DeepEqual(route.Schemas, wantOverride.Schemas) {
		t.Fatalf("route override tariff = hash %s schemas %+v, want hash %s", route.Content.ContentHash, route.Schemas, wantOverride.Content.ContentHash)
	}
	c2bAssertResolved(t, c, override.Ref, wantOverride)

	// The default and every unrelated route stay schema-free legacy material.
	defTariff, err := c.DefaultTariff(ctx)
	if err != nil {
		t.Fatalf("DefaultTariff: %v", err)
	}
	otherTariff, err := c.RouteTariff(ctx, "backend", "other")
	if err != nil {
		t.Fatalf("RouteTariff(other): %v", err)
	}
	for what, got := range map[string]economics.TariffSnapshot{"default": defTariff, "unrelated route": otherTariff} {
		if len(got.Schemas) != 0 {
			t.Fatalf("%s must stay schema-free, got %+v", what, got.Schemas)
		}
		if got.Content.ContentHash != defWant.Content.ContentHash {
			t.Fatalf("%s hash = %s, want legacy %s", what, got.Content.ContentHash, defWant.Content.ContentHash)
		}
	}
}

func TestSnapshotCatalogPutPricingRemainsSchemaFreeLegacy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	if err := c.PutPricing(pricing); err != nil {
		t.Fatalf("PutPricing: %v", err)
	}
	want, err := billing.PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatalf("PricingSnapshotToTariff: %v", err)
	}
	got, err := c.Tariff(pricing.Ref)
	if err != nil {
		t.Fatalf("Tariff: %v", err)
	}
	if len(got.Schemas) != 0 {
		t.Fatalf("legacy PutPricing must stay schema-free, got %+v", got.Schemas)
	}
	if got.Content.ContentHash != want.Content.ContentHash {
		t.Fatalf("legacy PutPricing hash = %s, want %s", got.Content.ContentHash, want.Content.ContentHash)
	}

	// The legacy ordering documents why PutPricingWithSchemas exists: adopting
	// schemas on an already-published schema-free legacy tariff is immutable.
	schemaTariff := c2bExpectedTariff(t, pricing, c2bInclusionSchemas(metering.ComponentCacheReadInputToken))
	if err := c.PutTariff(schemaTariff); !errors.Is(err, billingcompose.ErrSnapshotImmutable) {
		t.Fatalf("schema-bearing PutTariff after PutPricing = %v, want ErrSnapshotImmutable", err)
	}
	again, err := c.ResolveTariff(ctx, pricing.Ref)
	if err != nil {
		t.Fatalf("ResolveTariff: %v", err)
	}
	if again.Content.ContentHash != want.Content.ContentHash || len(again.Schemas) != 0 {
		t.Fatalf("rejected schema publication altered stored body: hash=%s schemas=%+v", again.Content.ContentHash, again.Schemas)
	}
}

// TestSnapshotCatalogPublishesOpenAINativeInclusionSchema publishes the real
// openaiusage-provided frozen schema as explicitly supplied caller material and
// proves the resolved production tariff is accepted by the stock post-usage
// rater with its provider-family relation intact.
func TestSnapshotCatalogPublishesOpenAINativeInclusionSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	policy := catalogPolicy()
	if err := c.PutPricingWithSchemas(pricing, openaiusage.NativeUsageInclusionSchemas()); err != nil {
		t.Fatalf("PutPricingWithSchemas: %v", err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	want := c2bExpectedTariff(t, pricing, openaiusage.NativeUsageInclusionSchemas())
	resolved, err := c.RouteTariff(ctx, "openai", "gpt-test")
	if err != nil {
		t.Fatalf("RouteTariff: %v", err)
	}
	if resolved.Content.ContentHash != want.Content.ContentHash || !reflect.DeepEqual(resolved.Schemas, want.Schemas) {
		t.Fatalf("catalog did not publish the OpenAI inclusion schema: hash=%s", resolved.Content.ContentHash)
	}
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("stock post-usage rater rejected published schema tariff: %v", err)
	}
	frozen := rater.Snapshot()
	if frozen.Content.ContentHash != resolved.Content.ContentHash || !reflect.DeepEqual(frozen.Schemas, want.Schemas) {
		t.Fatalf("stock rater did not freeze the published schema material")
	}
}
