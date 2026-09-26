package economics_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// invalidSchemaMaterial is a legitimately invalid schema set: one relationship
// references itself, which metering rejects with ErrInvalidComponentSchema.
func invalidSchemaMaterial() []metering.ComponentSchema {
	key := schemaMaterialKey(metering.ComponentInputToken)
	return []metering.ComponentSchema{{
		ID:      "schema:material:self",
		Version: "1",
		Relationships: []metering.ComponentRelationship{{
			Kind:   metering.RelationshipSubset,
			Parent: key,
			Child:  key,
		}},
	}}
}

// requireBothSchemaSentinels pins that a validation error exposes both the
// public tariff classification and the underlying metering classification.
func requireBothSchemaSentinels(t *testing.T, err error, path string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: invalid schema material was accepted", path)
	}
	if !errors.Is(err, economics.ErrInvalidTariffSnapshot) {
		t.Fatalf("%s: error %v does not match ErrInvalidTariffSnapshot", path, err)
	}
	if !errors.Is(err, metering.ErrInvalidComponentSchema) {
		t.Fatalf("%s: error %v does not match metering.ErrInvalidComponentSchema", path, err)
	}
}

// TestTariffSnapshotInvalidSchemaMaterialPreservesBothSentinels drives every
// exported TariffSnapshot validation path that consumes schema material and
// requires the metering sentinel to survive the tariff classification wrap.
func TestTariffSnapshotInvalidSchemaMaterialPreservesBothSentinels(t *testing.T) {
	t.Parallel()
	schemas := invalidSchemaMaterial()
	rules := []economics.RatingRule{schemaMaterialRule()}

	_, err := economics.BuildTariffSnapshotWithSchemas(schemaMaterialRef(), "USD", rules, schemas)
	requireBothSchemaSentinels(t, err, "BuildTariffSnapshotWithSchemas")

	snapshot := economics.TariffSnapshot{Ref: schemaMaterialRef(), Currency: "USD", Rules: rules, Schemas: schemas}
	requireBothSchemaSentinels(t, snapshot.Validate(), "TariffSnapshot.Validate")

	_, err = snapshot.Canonical()
	requireBothSchemaSentinels(t, err, "TariffSnapshot.Canonical")
}

// TestRatingCatalogViewInvalidSchemaMaterialPreservesBothSentinels drives the
// exported RatingCatalogView validation paths that consume schema material.
func TestRatingCatalogViewInvalidSchemaMaterialPreservesBothSentinels(t *testing.T) {
	t.Parallel()
	view := economics.RatingCatalogView{
		Currency: "USD",
		Rules:    []economics.RatingRule{schemaMaterialRule()},
		Schemas:  invalidSchemaMaterial(),
	}

	requireBothSchemaSentinels(t, view.Validate(), "RatingCatalogView.Validate")

	_, err := view.Tariff(schemaMaterialRef())
	requireBothSchemaSentinels(t, err, "RatingCatalogView.Tariff")
}
