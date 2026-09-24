package billingcompose_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func catalogSchemaKey(component string) metering.ComponentKey {
	return metering.ComponentKey{Direction: metering.DirectionInput, Component: component, Unit: metering.UnitToken, SchemaID: "catalog:schema:v1"}
}

func catalogSchemaTariff(t *testing.T, child string) economics.TariffSnapshot {
	t.Helper()
	price, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	key := catalogSchemaKey(metering.ComponentInputToken)
	schemas := []metering.ComponentSchema{{
		ID:      "catalog:schema:v1",
		Version: "1",
		Relationships: []metering.ComponentRelationship{{
			Kind:   metering.RelationshipSubset,
			Parent: key,
			Child:  catalogSchemaKey(child),
		}},
	}}
	snapshot, err := economics.BuildTariffSnapshotWithSchemas(economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "catalog-schema", Version: "v1"}, RaterID: "reference",
	}, "USD", []economics.RatingRule{{ID: "input", Component: &key, Currency: "USD", UnitPrice: &price}}, schemas)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	return snapshot
}

func TestSnapshotCatalogPublishesAndReplaysFrozenSchemas(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	tariff := catalogSchemaTariff(t, metering.ComponentCacheReadInputToken)
	if err := c.PutTariff(tariff); err != nil {
		t.Fatalf("PutTariff: %v", err)
	}
	want, err := tariff.Canonical()
	if err != nil {
		t.Fatal(err)
	}

	got, err := c.Tariff(billing.VersionRef{ID: "catalog-schema", Version: "v1"})
	if err != nil {
		t.Fatalf("Tariff: %v", err)
	}
	if got.Content.ContentHash != tariff.Content.ContentHash {
		t.Fatalf("published hash=%s, want %s", got.Content.ContentHash, tariff.Content.ContentHash)
	}
	if !reflect.DeepEqual(got.Schemas, want.Schemas) {
		t.Fatalf("published schemas differ:\n%+v\n%+v", got.Schemas, want.Schemas)
	}

	resolved, err := c.ResolveTariff(context.Background(), billing.VersionRef{ID: "catalog-schema", Version: "v1"})
	if err != nil {
		t.Fatalf("ResolveTariff: %v", err)
	}
	if !reflect.DeepEqual(resolved.Schemas, want.Schemas) {
		t.Fatalf("ResolveTariff dropped the frozen schemas: %+v", resolved.Schemas)
	}

	got.Schemas[0].Relationships[0].Child.Component = "mutated-returned-body"
	reread, err := c.Tariff(billing.VersionRef{ID: "catalog-schema", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if reread.Schemas[0].Relationships[0].Child.Component != metering.ComponentCacheReadInputToken {
		t.Fatalf("catalog returned a mutable schema body: %+v", reread.Schemas[0])
	}

	if err := c.PutTariff(tariff); err != nil {
		t.Fatalf("identical schema replay must be idempotent: %v", err)
	}
}

func TestSnapshotCatalogRejectsSchemaContentMutationAtSameVersion(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	if err := c.PutTariff(catalogSchemaTariff(t, metering.ComponentCacheReadInputToken)); err != nil {
		t.Fatalf("PutTariff: %v", err)
	}
	err := c.PutTariff(catalogSchemaTariff(t, metering.ComponentCacheWriteInputToken))
	if !errors.Is(err, billingcompose.ErrSnapshotImmutable) {
		t.Fatalf("changed schema edge error=%v, want ErrSnapshotImmutable", err)
	}
}
