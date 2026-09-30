package economics_test

import (
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// TestRatingCatalogViewCarriesFrozenSchemas pins the public snapshot-source
// payload contract: a view that carries frozen component-relationship material
// must reconstruct the identical TariffSnapshot content hash, and cloning the
// view must not alias caller-owned schema bodies back into the view.
func TestRatingCatalogViewCarriesFrozenSchemas(t *testing.T) {
	t.Parallel()
	rules := []economics.RatingRule{schemaMaterialRule()}
	schemas := schemaMaterialSchemas()
	ref := schemaMaterialRef()

	built, err := economics.BuildTariffSnapshotWithSchemas(ref, "USD", rules, schemas)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	view := economics.RatingCatalogView{
		Currency: "USD",
		Rules:    rules,
		Schemas:  schemas,
	}
	if err := view.Validate(); err != nil {
		t.Fatalf("view.Validate: %v", err)
	}
	reconstructed, err := view.Tariff(ref)
	if err != nil {
		t.Fatalf("view.Tariff: %v", err)
	}
	if reconstructed.Content.ContentHash != built.Content.ContentHash {
		t.Fatalf("view reconstruction hash=%s, want published %s",
			reconstructed.Content.ContentHash, built.Content.ContentHash)
	}
	if !reflect.DeepEqual(reconstructed.Schemas, built.Schemas) {
		t.Fatalf("view reconstruction schemas differ:\n got  %+v\n want %+v",
			reconstructed.Schemas, built.Schemas)
	}

	cloned := view.Clone()
	cloned.Schemas[0].Relationships[0].Child.Component = "mutated-clone-body"
	if view.Schemas[0].Relationships[0].Child.Component != metering.ComponentCacheReadInputToken {
		t.Fatalf("view.Clone aliased schema bodies: %+v", view.Schemas[0])
	}
}

// TestRatingCatalogViewLegacySchemaFreeStaysCompatible pins that a schema-free
// view (every pre-schema source) reconstructs the exact legacy content hash and
// carries no schema material.
func TestRatingCatalogViewLegacySchemaFreeStaysCompatible(t *testing.T) {
	t.Parallel()
	rules := []economics.RatingRule{schemaMaterialRule()}
	ref := schemaMaterialRef()

	legacy, err := economics.BuildTariffSnapshot(ref, "USD", rules)
	if err != nil {
		t.Fatalf("BuildTariffSnapshot: %v", err)
	}
	view := economics.RatingCatalogView{Currency: "USD", Rules: rules}
	reconstructed, err := view.Tariff(ref)
	if err != nil {
		t.Fatalf("view.Tariff: %v", err)
	}
	if reconstructed.Content.ContentHash != legacy.Content.ContentHash {
		t.Fatalf("schema-free view hash=%s, want legacy %s",
			reconstructed.Content.ContentHash, legacy.Content.ContentHash)
	}
	if len(reconstructed.Schemas) != 0 {
		t.Fatalf("schema-free view gained schemas: %+v", reconstructed.Schemas)
	}
}

// TestRatingCatalogViewRejectsInvalidSchemaMaterial keeps view-level validation
// at parity with TariffSnapshot: a cyclic frozen graph is rejected before it can
// be reconstructed under an immutable identity.
func TestRatingCatalogViewRejectsInvalidSchemaMaterial(t *testing.T) {
	t.Parallel()
	first := schemaMaterialKey(metering.ComponentInputToken)
	second := schemaMaterialKey(metering.ComponentCacheReadInputToken)
	cyclic := []metering.ComponentSchema{
		{ID: "schema:material:a", Version: "1", Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: first, Child: second}}},
		{ID: "schema:material:b", Version: "1", Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: second, Child: first}}},
	}
	view := economics.RatingCatalogView{Currency: "USD", Rules: []economics.RatingRule{schemaMaterialRule()}, Schemas: cyclic}
	if err := view.Validate(); err == nil {
		t.Fatal("view with cyclic schemas must be rejected")
	}
	if _, err := view.Tariff(schemaMaterialRef()); err == nil {
		t.Fatal("view.Tariff with cyclic schemas must be rejected")
	}
}
