package economics_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Pinned fixtures captured from the pre-schema-material implementation. They
// must never be regenerated from the implementation under test: a change here
// proves a historical frozen hash was rewritten.
const (
	pinnedMinimalHash  = "0558daf5b4c9fc8dae5715a1d19e4e723645728fb5c98b6876618a825a04344a"
	pinnedMinimalJSON  = `{"ref":{"id":"pinned-min","version":"v1","rater_id":"reference"},"currency":"USD","rules":[{"id":"image","component":{"direction":"input","component":"image","unit":"image","schema_id":"phase9.catalog.v1"},"currency":"USD","unit_price":{"coefficient":"2","scale":0}}],"content":{"content_ref":"tariff-snapshot:v1://pinned-min/v1","content_hash":"0558daf5b4c9fc8dae5715a1d19e4e723645728fb5c98b6876618a825a04344a"}}`
	pinnedMetadataHash = "cc443965c9ab90928b519d45c6856ddcb9ec80300af8925f238566e7621fa0a9"
)

func schemaMaterialKey(component string) metering.ComponentKey {
	return metering.ComponentKey{Direction: metering.DirectionInput, Component: component, Unit: metering.UnitToken, SchemaID: "schema:material:v1"}
}

func schemaMaterialOutputKey(component string) metering.ComponentKey {
	return metering.ComponentKey{Direction: metering.DirectionOutput, Component: component, Unit: metering.UnitToken, SchemaID: "schema:material:v1"}
}

func schemaMaterialRule() economics.RatingRule {
	price, err := metering.ParseDecimal("1")
	if err != nil {
		panic(err)
	}
	key := schemaMaterialKey(metering.ComponentInputToken)
	return economics.RatingRule{ID: "input", Component: &key, Currency: "USD", UnitPrice: &price}
}

func schemaMaterialRef() economics.RatingSnapshotRef {
	return economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "schema-material", Version: "v1"}, RaterID: "reference"}
}

func schemaMaterialSchemas() []metering.ComponentSchema {
	return []metering.ComponentSchema{
		{
			ID:      "schema:material:a",
			Version: "1",
			Relationships: []metering.ComponentRelationship{{
				Kind:   metering.RelationshipSubset,
				Parent: schemaMaterialKey(metering.ComponentInputToken),
				Child:  schemaMaterialKey(metering.ComponentCacheReadInputToken),
			}},
		},
		{
			ID:      "schema:material:b",
			Version: "1",
			Relationships: []metering.ComponentRelationship{
				{
					Kind:   metering.RelationshipSubset,
					Parent: schemaMaterialOutputKey(metering.ComponentOutputToken),
					Child:  schemaMaterialOutputKey(metering.ComponentReasoningOutputToken),
				},
				{
					Kind:   metering.RelationshipSubset,
					Parent: schemaMaterialOutputKey(metering.ComponentOutputToken),
					Child:  schemaMaterialOutputKey(metering.ComponentAudioToken),
				},
			},
		},
	}
}

func mustSchemaTariff(t *testing.T, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	snapshot, err := economics.BuildTariffSnapshotWithSchemas(schemaMaterialRef(), "USD", []economics.RatingRule{schemaMaterialRule()}, schemas)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	return snapshot
}

func TestTariffSnapshotNilSchemasPreservePinnedFrozenIdentity(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "phase9.catalog.v1"}
	price, err := metering.ParseDecimal("2")
	if err != nil {
		t.Fatal(err)
	}
	minimal, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "pinned-min", Version: "v1"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{{ID: "image", Component: &key, Currency: "USD", UnitPrice: &price}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(minimal)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != pinnedMinimalJSON {
		t.Fatalf("legacy canonical bytes changed\n got  %s\n want %s", wire, pinnedMinimalJSON)
	}
	if minimal.Content.ContentHash != pinnedMinimalHash {
		t.Fatalf("legacy content hash=%s, want pinned %s", minimal.Content.ContentHash, pinnedMinimalHash)
	}
	emptySchemas := minimal
	emptySchemas.Schemas = []metering.ComponentSchema{}
	canonical, err := emptySchemas.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Content.ContentHash != pinnedMinimalHash {
		t.Fatalf("empty schema slice must reproduce the legacy hash, got %s", canonical.Content.ContentHash)
	}

	metadata := economics.TariffSnapshot{
		Ref:                 economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "pinned", Version: "v1"}, RaterID: "reference"},
		Currency:            "USD",
		CatalogVersion:      "cat-1",
		Rules:               []economics.RatingRule{{ID: "image", Component: &key, Currency: "USD", UnitPrice: &price}},
		EffectiveQualifiers: []metering.Dimension{{Name: "region", Value: "us"}},
		LegacySemantics:     economics.LegacyScalarSemanticsV1,
	}
	metadataCanonical, err := metadata.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if metadataCanonical.Content.ContentHash != pinnedMetadataHash {
		t.Fatalf("metadata legacy content hash=%s, want pinned %s", metadataCanonical.Content.ContentHash, pinnedMetadataHash)
	}
}

func TestTariffSnapshotSchemaMaterialHashingIsOrderIndependent(t *testing.T) {
	t.Parallel()
	ordered := mustSchemaTariff(t, schemaMaterialSchemas())

	shuffledSchemas := schemaMaterialSchemas()
	shuffledSchemas[0], shuffledSchemas[1] = shuffledSchemas[1], shuffledSchemas[0]
	shuffledSchemas[0].Relationships = []metering.ComponentRelationship{
		schemaMaterialSchemas()[1].Relationships[1],
		schemaMaterialSchemas()[1].Relationships[0],
	}
	shuffled := mustSchemaTariff(t, shuffledSchemas)

	if ordered.Content.ContentHash != shuffled.Content.ContentHash {
		t.Fatalf("schema/relationship order changed the frozen hash: %s != %s", ordered.Content.ContentHash, shuffled.Content.ContentHash)
	}
	firstCanonical, err := ordered.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	secondCanonical, err := shuffled.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstCanonical.Schemas, secondCanonical.Schemas) {
		t.Fatalf("canonical schema order differs:\n%+v\n%+v", firstCanonical.Schemas, secondCanonical.Schemas)
	}
}

func TestTariffSnapshotSchemaMaterialHashingIsChangeSensitive(t *testing.T) {
	t.Parallel()
	base := mustSchemaTariff(t, schemaMaterialSchemas())

	kindChanged := schemaMaterialSchemas()
	kindChanged[0].Relationships[0].Kind = metering.RelationshipPartition
	if changed := mustSchemaTariff(t, kindChanged); changed.Content.ContentHash == base.Content.ContentHash {
		t.Fatal("changing a relationship kind must change the frozen hash")
	}

	edgeChanged := schemaMaterialSchemas()
	edgeChanged[0].Relationships[0].Child = schemaMaterialKey(metering.ComponentCacheWriteInputToken)
	if changed := mustSchemaTariff(t, edgeChanged); changed.Content.ContentHash == base.Content.ContentHash {
		t.Fatal("changing a relationship edge must change the frozen hash")
	}

	idChanged := schemaMaterialSchemas()
	idChanged[0].ID = "schema:material:renamed"
	if changed := mustSchemaTariff(t, idChanged); changed.Content.ContentHash == base.Content.ContentHash {
		t.Fatal("changing a schema id must change the frozen hash")
	}
}

func TestTariffSnapshotSchemaMaterialIsImmutableAcrossCallerMutation(t *testing.T) {
	t.Parallel()
	schemas := schemaMaterialSchemas()
	snapshot, err := economics.BuildTariffSnapshotWithSchemas(schemaMaterialRef(), "USD", []economics.RatingRule{schemaMaterialRule()}, schemas)
	if err != nil {
		t.Fatal(err)
	}
	schemas[0].Relationships[0].Child.Component = "mutated-after-build"
	schemas[0].Relationships = nil
	canonical, err := snapshot.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Schemas[0].Relationships[0].Child.Component != metering.ComponentCacheReadInputToken {
		t.Fatalf("published snapshot mutated through caller-owned input: %+v", canonical.Schemas[0])
	}

	returned := snapshot.Clone()
	returned.Schemas[0].Relationships[0].Child.Component = "mutated-returned-copy"
	returned.Schemas[0].ID = "mutated"
	reread := snapshot.Clone()
	if reread.Schemas[0].Relationships[0].Child.Component != metering.ComponentCacheReadInputToken ||
		reread.Schemas[0].ID != "schema:material:a" {
		t.Fatalf("snapshot body is not isolated from a returned clone: %+v", reread.Schemas[0])
	}
}

func TestTariffSnapshotRejectsInvalidSchemaMaterial(t *testing.T) {
	t.Parallel()
	first := schemaMaterialKey(metering.ComponentInputToken)
	second := schemaMaterialKey(metering.ComponentCacheReadInputToken)
	cyclic := []metering.ComponentSchema{
		{ID: "schema:material:a", Version: "1", Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: first, Child: second}}},
		{ID: "schema:material:b", Version: "1", Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: second, Child: first}}},
	}
	if _, err := economics.BuildTariffSnapshotWithSchemas(schemaMaterialRef(), "USD", []economics.RatingRule{schemaMaterialRule()}, cyclic); !errors.Is(err, economics.ErrInvalidTariffSnapshot) {
		t.Fatalf("cyclic schemas error=%v, want ErrInvalidTariffSnapshot", err)
	}

	duplicateID := []metering.ComponentSchema{
		{ID: "schema:material:a", Version: "1"},
		{ID: "schema:material:a", Version: "2"},
	}
	if _, err := economics.BuildTariffSnapshotWithSchemas(schemaMaterialRef(), "USD", []economics.RatingRule{schemaMaterialRule()}, duplicateID); !errors.Is(err, economics.ErrInvalidTariffSnapshot) {
		t.Fatalf("duplicate schema id error=%v, want ErrInvalidTariffSnapshot", err)
	}
}

func TestTariffSnapshotSchemaMaterialJSONRoundTrip(t *testing.T) {
	t.Parallel()
	canonical, err := mustSchemaTariff(t, schemaMaterialSchemas()).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	var decoded economics.TariffSnapshot
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Content.ContentHash != canonical.Content.ContentHash {
		t.Fatalf("round-trip hash=%s, want %s", decoded.Content.ContentHash, canonical.Content.ContentHash)
	}
	if !reflect.DeepEqual(decoded.Schemas, canonical.Schemas) {
		t.Fatalf("round-trip schemas differ:\n%+v\n%+v", decoded.Schemas, canonical.Schemas)
	}
	rewritten, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(rewritten) != string(wire) {
		t.Fatalf("round-trip serialization is not stable\n%s\n%s", rewritten, wire)
	}
}
