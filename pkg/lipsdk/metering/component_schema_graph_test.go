package metering_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func schemaTestKey(direction metering.FlowDirection, component, unit, schemaID string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: unit, SchemaID: schemaID}
}

func schemaTestRelationship(kind metering.RelationshipKind, parent, child metering.ComponentKey) metering.ComponentRelationship {
	return metering.ComponentRelationship{Kind: kind, Parent: parent, Child: child}
}

func TestComponentSchemaCloneIsDeep(t *testing.T) {
	t.Parallel()
	parent := schemaTestKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, "schema:clone:v1")
	child := schemaTestKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, "schema:clone:v1")
	child.Dimensions = []metering.Dimension{{Name: "cache", Value: "read"}}
	original := metering.ComponentSchema{
		ID:      "schema:clone:v1",
		Version: "1",
		Relationships: []metering.ComponentRelationship{
			schemaTestRelationship(metering.RelationshipSubset, parent, child),
		},
	}
	clone := original.Clone()
	clone.Relationships[0].Parent.Component = "mutated"
	clone.Relationships[0].Child.Dimensions[0].Value = "mutated"
	clone.Relationships = append(clone.Relationships, schemaTestRelationship(metering.RelationshipSubset, child, parent))
	if original.Relationships[0].Parent.Component != metering.ComponentInputToken ||
		original.Relationships[0].Child.Dimensions[0].Value != "read" ||
		len(original.Relationships) != 1 {
		t.Fatalf("Clone must deep-copy relationships and key dimensions: %+v", original)
	}
}

func TestValidateComponentSchemasAcceptsNilAndDisjointGraphs(t *testing.T) {
	t.Parallel()
	if err := metering.ValidateComponentSchemas(nil); err != nil {
		t.Fatalf("nil schemas must remain valid for legacy publication: %v", err)
	}
	parent := schemaTestKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, "schema:ok:v1")
	child := schemaTestKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, "schema:ok:v1")
	schemas := []metering.ComponentSchema{{
		ID:      "schema:ok:v1",
		Version: "1",
		Relationships: []metering.ComponentRelationship{
			schemaTestRelationship(metering.RelationshipSubset, parent, child),
		},
	}}
	if err := metering.ValidateComponentSchemas(schemas); err != nil {
		t.Fatalf("disjoint valid graph must be accepted: %v", err)
	}
}

func TestValidateComponentSchemasRejectsDuplicateSchemaID(t *testing.T) {
	t.Parallel()
	schemas := []metering.ComponentSchema{
		{ID: "schema:dup:v1", Version: "1"},
		{ID: "schema:dup:v1", Version: "2"},
	}
	err := metering.ValidateComponentSchemas(schemas)
	if !errors.Is(err, metering.ErrInvalidComponentSchema) {
		t.Fatalf("duplicate schema id error=%v, want ErrInvalidComponentSchema", err)
	}
}

func TestValidateComponentSchemasRejectsSchemaAndEdgeBounds(t *testing.T) {
	t.Parallel()
	tooManySchemas := make([]metering.ComponentSchema, metering.MaxComponentSchemas+1)
	for i := range tooManySchemas {
		tooManySchemas[i] = metering.ComponentSchema{ID: fmt.Sprintf("schema:%d:v1", i), Version: "1"}
	}
	if err := metering.ValidateComponentSchemas(tooManySchemas); !errors.Is(err, metering.ErrInvalidComponentSchema) {
		t.Fatalf("schema bound error=%v, want ErrInvalidComponentSchema", err)
	}

	relationships := make([]metering.ComponentRelationship, metering.MaxComponentSchemaRelationships+1)
	for i := range relationships {
		relationships[i] = schemaTestRelationship(
			metering.RelationshipSubset,
			schemaTestKey(metering.DirectionInput, fmt.Sprintf("parent_%d", i), metering.UnitToken, "schema:bounded:v1"),
			schemaTestKey(metering.DirectionInput, fmt.Sprintf("child_%d", i), metering.UnitToken, "schema:bounded:v1"),
		)
	}
	if err := metering.ValidateComponentSchemas([]metering.ComponentSchema{{ID: "schema:bounded:v1", Version: "1", Relationships: relationships}}); !errors.Is(err, metering.ErrInvalidComponentSchema) {
		t.Fatalf("relationship bound error=%v, want ErrInvalidComponentSchema", err)
	}
}

func TestValidateComponentSchemasRejectsCycles(t *testing.T) {
	t.Parallel()
	first := schemaTestKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, "schema:cycle:v1")
	second := schemaTestKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, "schema:cycle:v1")
	schemas := []metering.ComponentSchema{
		{ID: "schema:cycle:a", Version: "1", Relationships: []metering.ComponentRelationship{schemaTestRelationship(metering.RelationshipSubset, first, second)}},
		{ID: "schema:cycle:b", Version: "1", Relationships: []metering.ComponentRelationship{schemaTestRelationship(metering.RelationshipSubset, second, first)}},
	}
	if err := metering.ValidateComponentSchemas(schemas); !errors.Is(err, metering.ErrInvalidComponentSchema) {
		t.Fatalf("cycle error=%v, want ErrInvalidComponentSchema", err)
	}
}

func TestValidateComponentSchemasRejectsAmbiguousDuplicateEdgeSemantics(t *testing.T) {
	t.Parallel()
	parent := schemaTestKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, "schema:ambiguous:v1")
	child := schemaTestKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, "schema:ambiguous:v1")
	sameSchema := []metering.ComponentSchema{{
		ID:      "schema:ambiguous:v1",
		Version: "1",
		Relationships: []metering.ComponentRelationship{
			schemaTestRelationship(metering.RelationshipSubset, parent, child),
			schemaTestRelationship(metering.RelationshipAggregate, parent, child),
		},
	}}
	if err := metering.ValidateComponentSchemas(sameSchema); !errors.Is(err, metering.ErrInvalidComponentSchema) {
		t.Fatalf("same-schema ambiguous edge error=%v, want ErrInvalidComponentSchema", err)
	}
	crossSchema := []metering.ComponentSchema{
		{ID: "schema:ambiguous:a", Version: "1", Relationships: []metering.ComponentRelationship{schemaTestRelationship(metering.RelationshipSubset, parent, child)}},
		{ID: "schema:ambiguous:b", Version: "1", Relationships: []metering.ComponentRelationship{schemaTestRelationship(metering.RelationshipPartition, parent, child)}},
	}
	if err := metering.ValidateComponentSchemas(crossSchema); !errors.Is(err, metering.ErrInvalidComponentSchema) {
		t.Fatalf("cross-schema ambiguous edge error=%v, want ErrInvalidComponentSchema", err)
	}
}
