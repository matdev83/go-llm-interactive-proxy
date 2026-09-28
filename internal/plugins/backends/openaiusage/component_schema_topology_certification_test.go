package openaiusage_test

// Certification of the shipped OpenAI Chat/Responses provider-family inclusion
// schema against the strengthened component-schema topology validators.
//
// The review's claim is that the three structurally-knowable impossibilities are
// not OpenAI-specific: any schema whose accounting is driven by declared edges
// rather than by a component-name table must pass them for free. This file is
// the executable form of that claim. It:
//
//  1. builds and canonicalizes the real immutable tariff snapshot that carries
//     the production schema, so publication-time validation runs exactly as it
//     does for a real publication;
//  2. compiles that snapshot into a production rater, proving the strengthened
//     gate did not leave a schema that no rater can consume;
//  3. validates the topology under the WIDEST possible gate, every rule at
//     once including the two that are deliberately report-only, so the claim is
//     not an artefact of which rules happen to be enforced today;
//  4. proves the outcome is component-name blind, by rebuilding an isomorphic
//     copy in which every component name is replaced by an opaque vendor name
//     and showing that publication, compilation and the rated money are all
//     unchanged.
//
// Point 4 is the load-bearing one for "no component-name special casing is
// needed": if any part of publication or rating branched on the SDK's built-in
// component vocabulary, the renamed copy would not reproduce the production
// result.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const certRefID = "openai-usage-topology-certification"

// certQuantities are the hand-derived provider quantities for the certification
// vectors. They match the intact wire fixture the existing inclusion vectors use
// (prompt 11 = text 8 + audio 3, completion 9 = text 5 + audio 4) and are never
// read back out of the implementation under test.
var certQuantities = []struct {
	key      sdkmetering.ComponentKey
	quantity string
}{
	{wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken), "11"},
	{wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken), "9"},
	{wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentTextToken), "8"},
	{wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken), "5"},
	{wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken), "3"},
	{wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken), "4"},
}

// certChildTotal is the child-only money the declared partition must produce:
// 8*2 + 3*5 + 5*3 + 4*7 = 74. It is the same total the existing inclusion
// vectors assert, and it is the figure the renamed isomorphic copy must
// reproduce exactly.
const certChildTotal = "74/0"

func certTariff(t *testing.T, id string, rules []economics.RatingRule, schemas []sdkmetering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.BuildTariffSnapshotWithSchemas(economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: id, Version: "v1"}, RaterID: "reference",
	}, "USD", rules, schemas)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas(%s): %v", id, err)
	}
	return tariff
}

func certMeasure(t *testing.T, key sdkmetering.ComponentKey, quantity string) sdkmetering.Measure {
	t.Helper()
	value, err := sdkmetering.ParseDecimal(quantity)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", quantity, err)
	}
	return sdkmetering.Measure{Key: key, Value: &value, Quality: sdkmetering.QualityObserved, MethodRef: certRefID}
}

func certObservation(t *testing.T, id string, measures ...sdkmetering.Measure) sdkmetering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_821, 0).UTC()
	return sdkmetering.Observation{
		Version: sdkmetering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: sdkmetering.OriginLocal, Acquisition: sdkmetering.AcquisitionLocalTransport,
		Authority:   sdkmetering.AuthorityObservedClaim,
		Perspective: sdkmetering.PerspectiveOperator, Boundary: sdkmetering.BoundaryBackendIngress,
		Lifecycle: sdkmetering.LifecycleBackendAttempt,
		Subject: sdkmetering.SubjectRef{
			Kind: sdkmetering.SubjectBLeg, StoreID: "store-cert", ALegID: "a-cert",
			BillingCallID: "call-cert", BLegID: "b-cert",
		},
		Correlation: sdkmetering.CorrelationV2{
			StoreID: "store-cert", ALegID: "a-cert", BillingCallID: "call-cert", BLegID: "b-cert",
		},
		Semantics: sdkmetering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: certRefID,
		Measures: measures,
	}
}

func certRate(t *testing.T, tariff economics.TariffSnapshot, observation sdkmetering.Observation) (economics.Valuation, error) {
	t.Helper()
	rater, err := corebilling.NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	return rater.Rate(context.Background(), economics.PostUsageRatingInput{
		Version: 2, Perspective: sdkmetering.PerspectiveOperator, Basis: economics.BasisLocalExpected,
		Subject: observation.Subject, Scope: "call:call-cert", Observations: []sdkmetering.Observation{observation},
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "cert-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://cert/rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:               tariff.Ref,
		TariffContent:        &tariff.Content,
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://cert/qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		AsOf:                 time.Unix(1_700_000_822, 0).UTC(),
	})
}

// certNameBlindCopy rebuilds the production schema with every component name
// replaced by an opaque vendor name, preserving direction, unit, schema
// identity, kind and optionality exactly. The mapping is keyed on the full
// canonical key and assigned in sorted order, so it is deterministic and it
// keeps the two distinct cache-write identities (native detail and default
// inclusion) as two distinct names.
//
// Extra keys (the tariff rules price a few components the schema does not
// declare) join the same mapping, so a caller can rename a whole tariff rather
// than only the declared edges.
func certNameBlindCopy(t *testing.T, extra ...sdkmetering.ComponentKey) ([]sdkmetering.ComponentSchema, map[string]sdkmetering.ComponentKey) {
	t.Helper()
	production := openaiusage.NativeUsageInclusionSchemas()
	unique := make(map[string]sdkmetering.ComponentKey)
	for _, schema := range production {
		for _, relationship := range schema.Relationships {
			unique[relationship.Parent.CanonicalKey()] = relationship.Parent
			unique[relationship.Child.CanonicalKey()] = relationship.Child
		}
	}
	for _, key := range extra {
		unique[key.CanonicalKey()] = key
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	renamed := make(map[string]sdkmetering.ComponentKey, len(keys))
	for i, key := range keys {
		original := unique[key]
		blind := original
		blind.Component = fmt.Sprintf("vendor:cert:c%02d", i+1)
		renamed[key] = blind
	}

	out := make([]sdkmetering.ComponentSchema, 0, len(production))
	for _, schema := range production {
		clone := schema.Clone()
		for i := range clone.Relationships {
			parent := renamed[clone.Relationships[i].Parent.CanonicalKey()]
			child := renamed[clone.Relationships[i].Child.CanonicalKey()]
			clone.Relationships[i].Parent = parent
			clone.Relationships[i].Child = child
		}
		out = append(out, clone)
	}
	return out, renamed
}

func certAssertStructurePreserved(t *testing.T, production, blind []sdkmetering.ComponentSchema) {
	t.Helper()
	if len(production) != len(blind) {
		t.Fatalf("schema count = %d, want %d", len(blind), len(production))
	}
	for s := range production {
		if production[s].ID != blind[s].ID || production[s].Version != blind[s].Version {
			t.Fatalf("schema identity changed: %+v vs %+v", blind[s], production[s])
		}
		if len(production[s].Relationships) != len(blind[s].Relationships) {
			t.Fatalf("relationship count = %d, want %d", len(blind[s].Relationships), len(production[s].Relationships))
		}
		for r := range production[s].Relationships {
			got, want := blind[s].Relationships[r], production[s].Relationships[r]
			if got.Kind != want.Kind || got.Optional != want.Optional {
				t.Fatalf("edge %d kind/optional changed: %+v vs %+v", r, got, want)
			}
			if got.Parent.Direction != want.Parent.Direction || got.Child.Direction != want.Child.Direction {
				t.Fatalf("edge %d direction changed: %+v vs %+v", r, got, want)
			}
			if got.Parent.Unit != want.Parent.Unit || got.Child.Unit != want.Child.Unit {
				t.Fatalf("edge %d unit changed: %+v vs %+v", r, got, want)
			}
			if got.Parent.SchemaID != want.Parent.SchemaID || got.Child.SchemaID != want.Child.SchemaID {
				t.Fatalf("edge %d schema identity changed: %+v vs %+v", r, got, want)
			}
		}
	}
}

// TestNativeUsageInclusionSchemaPassesStrengthenedTopologyGate is the
// certification itself: the shipped production schema is published, compiled
// and rated under the strengthened validator, with no finding under ANY rule.
func TestNativeUsageInclusionSchemaPassesStrengthenedTopologyGate(t *testing.T) {
	t.Parallel()
	schemas := openaiusage.NativeUsageInclusionSchemas()

	// (1) Topology, under the widest gate this package can express: all three
	// rules at once, including the two that are report-only today. A schema that
	// only survives the current default is not certified.
	report := sdkmetering.InspectComponentSchemaTopology(schemas)
	if report.Len() != 0 {
		t.Fatalf("shipped production schema reported %d topology finding(s), want 0: %v", report.Len(), report.Findings)
	}
	if got := report.Rules(); len(got) != 0 {
		t.Fatalf("shipped production schema reported rules %v, want none", got)
	}
	if err := sdkmetering.ValidateComponentSchemasWithTopology(schemas, sdkmetering.SchemaTopologyAllRules); err != nil {
		t.Fatalf("shipped production schema must pass the widest topology gate: %v", err)
	}
	if err := sdkmetering.ValidateComponentSchemas(schemas); err != nil {
		t.Fatalf("shipped production schema must pass the default publication gate: %v", err)
	}

	// (2) Build and canonicalize the immutable snapshot. This runs the
	// publication validation inside economics, and it is the gate a real
	// publication crosses.
	rules := wireChildPartitionRules()
	tariff := certTariff(t, certRefID, rules, schemas)
	if err := tariff.Validate(); err != nil {
		t.Fatalf("canonicalized snapshot must validate: %v", err)
	}
	if len(tariff.Schemas) != 1 {
		t.Fatalf("snapshot schema count = %d, want 1", len(tariff.Schemas))
	}

	// (3) Compile. A schema no rater can consume is not a certified schema.
	if _, err := corebilling.NewReferenceRater(tariff); err != nil {
		t.Fatalf("shipped production schema must compile into a rater: %v", err)
	}

	// (4) Rate the intact partition, so the certification covers the money and
	// not only the publication.
	measures := make([]sdkmetering.Measure, 0, len(certQuantities))
	for _, q := range certQuantities {
		measures = append(measures, certMeasure(t, q.key, q.quantity))
	}
	valuation, err := certRate(t, tariff, certObservation(t, "cert-production", measures...))
	if err != nil {
		t.Fatalf("child-only tariff over the shipped partition must rate: %v", err)
	}
	if valuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness = %q, want complete", valuation.Completeness)
	}
	if got := wireValuationTotal(valuation); got != certChildTotal {
		t.Fatalf("total = %s, want %s", got, certChildTotal)
	}
}

// TestNativeUsageInclusionSchemaTopologyIsComponentNameBlind proves the
// certification is not an artefact of the SDK's built-in component vocabulary.
//
// The same topology is republished with every component name replaced by an
// opaque vendor name. If publication, compilation or rating branched on a
// component NAME, the renamed copy could not reproduce the production verdict
// or the production total. It must reproduce both exactly.
func TestNativeUsageInclusionSchemaTopologyIsComponentNameBlind(t *testing.T) {
	t.Parallel()
	production := openaiusage.NativeUsageInclusionSchemas()
	extra := make([]sdkmetering.ComponentKey, 0, 4)
	for _, rule := range wireChildPartitionRules() {
		if rule.Component != nil {
			extra = append(extra, *rule.Component)
		}
	}
	blind, renamed := certNameBlindCopy(t, extra...)
	certAssertStructurePreserved(t, production, blind)

	// The renamed copy must contain no component name the SDK recognises, so
	// this really is a name-blind variant rather than a partial rename.
	for _, schema := range blind {
		for _, relationship := range schema.Relationships {
			for _, key := range []sdkmetering.ComponentKey{relationship.Parent, relationship.Child} {
				if !strings.HasPrefix(key.Component, "vendor:cert:") {
					t.Fatalf("component %q was not replaced by an opaque vendor name", key.Component)
				}
			}
		}
	}

	// Publication under the widest gate is identical.
	blindReport := sdkmetering.InspectComponentSchemaTopology(blind)
	productionReport := sdkmetering.InspectComponentSchemaTopology(production)
	if blindReport.Len() != productionReport.Len() {
		t.Fatalf("renamed schema reported %d finding(s), production reported %d; the analysis is not name-blind",
			blindReport.Len(), productionReport.Len())
	}
	if err := sdkmetering.ValidateComponentSchemasWithTopology(blind, sdkmetering.SchemaTopologyAllRules); err != nil {
		t.Fatalf("renamed isomorphic schema must pass the widest topology gate: %v", err)
	}

	// Publication and compilation are identical, with no per-name special case.
	blindRules := make([]economics.RatingRule, 0, len(wireChildPartitionRules()))
	for _, rule := range wireChildPartitionRules() {
		clone := rule
		if rule.Component != nil {
			blindKey, ok := renamed[rule.Component.CanonicalKey()]
			if !ok {
				t.Fatalf("rule %s key %s is not covered by the rename map", rule.ID, rule.Component.CanonicalKey())
			}
			clone.Component = &blindKey
		}
		blindRules = append(blindRules, clone)
	}
	blindTariff := certTariff(t, certRefID, blindRules, blind)
	if err := blindTariff.Validate(); err != nil {
		t.Fatalf("renamed snapshot must validate: %v", err)
	}
	if _, err := corebilling.NewReferenceRater(blindTariff); err != nil {
		t.Fatalf("renamed schema must compile into a rater: %v", err)
	}

	// The money is identical: same structure, same quantities, same rates.
	measures := make([]sdkmetering.Measure, 0, len(certQuantities))
	for _, q := range certQuantities {
		blindKey, ok := renamed[q.key.CanonicalKey()]
		if !ok {
			t.Fatalf("key %s is not covered by the rename map", q.key.CanonicalKey())
		}
		measures = append(measures, certMeasure(t, blindKey, q.quantity))
	}
	valuation, err := certRate(t, blindTariff, certObservation(t, "cert-name-blind", measures...))
	if err != nil {
		t.Fatalf("child-only tariff over the renamed partition must rate: %v", err)
	}
	if valuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("renamed completeness = %q, want complete (the accounting is structural, not name-driven)", valuation.Completeness)
	}
	if got := wireValuationTotal(valuation); got != certChildTotal {
		t.Fatalf("renamed total = %s, want the production total %s; the accounting is not name-blind", got, certChildTotal)
	}
}

// TestNativeUsageInclusionSchemaRejectedByStrengthenedGateIsTyped pins the
// negative control: the certification above is not vacuous. A copy of the
// production schema with one edge turned into a shared containment descendant
// between two complete siblings is refused by the enforced rule with the typed
// topology sentinel, at publication time, before any rater runs.
func TestNativeUsageInclusionSchemaRejectedByStrengthenedGateIsTyped(t *testing.T) {
	t.Parallel()
	schemas := openaiusage.NativeUsageInclusionSchemas()
	// The two input partition children (text and audio) both gain a containment
	// path to one new descendant. Everything else, including every name, is the
	// production declaration.
	descendant := wireComponentKey(sdkmetering.DirectionInput, "vendor:cert:shared_descendant")
	broken := schemas[0].Clone()
	broken.Relationships = append(
		broken.Relationships,
		sdkmetering.ComponentRelationship{
			Kind: sdkmetering.RelationshipSubset, Parent: broken.Relationships[0].Child, Child: descendant,
		},
		sdkmetering.ComponentRelationship{
			Kind: sdkmetering.RelationshipSubset, Parent: broken.Relationships[1].Child, Child: descendant,
		},
	)
	brokenSet := []sdkmetering.ComponentSchema{broken}

	report := sdkmetering.InspectComponentSchemaTopology(brokenSet)
	if !report.Has(sdkmetering.SchemaTopologyCompleteSiblingSharedDescendant) {
		t.Fatalf("the negative control must trip the shared-descendant rule: %v", report.Findings)
	}
	err := sdkmetering.ValidateComponentSchemas(brokenSet)
	if !errors.Is(err, sdkmetering.ErrComponentSchemaTopology) {
		t.Fatalf("negative control error = %v, want ErrComponentSchemaTopology", err)
	}
	if !errors.Is(err, sdkmetering.ErrInvalidComponentSchema) {
		t.Fatalf("negative control error = %v, want it to also wrap ErrInvalidComponentSchema", err)
	}
	// The production schema it was derived from must remain clean, so the
	// rejection above is caused by the added edges and nothing else.
	if got := sdkmetering.InspectComponentSchemaTopology(schemas).Len(); got != 0 {
		t.Fatalf("production schema reported %d finding(s), want 0", got)
	}
}
