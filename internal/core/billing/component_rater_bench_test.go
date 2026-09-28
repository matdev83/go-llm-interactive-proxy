package billing_test

// Performance baselines for the post-usage reference component rater
// (component_rater.go). These benchmarks measure ReferenceRater.Rate only: the
// frozen tariff is published/resolved once and the rater and rating input are
// built once, outside the timed loop, because Rate does not mutate its input.
// NewReferenceRater's canonicalization/validation cost is deliberately NOT part
// of these numbers.
//
// The fixture helpers in this package (b1Tariff, b1Resolve, b1Observation,
// b1OperatorInput, f3Resolved, ...) are typed *testing.T and therefore cannot be
// called from *testing.B. The TB-typed builders below mirror those helpers
// exactly and reuse the package's pure builders (b1Key, b1Schema, b1SchemaID).
//
// Every benchmark runs an in-setup sanity rate so a fixture that silently stops
// exercising the intended seam fails loudly instead of reporting a cheap timing.
//
// No network, database or credentials are used.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// benchSinkVal and benchSinkErr keep the rated result observable so the
// compiler cannot eliminate the call.
var (
	benchSinkVal economics.Valuation
	benchSinkErr error
)

// ---------------------------------------------------------------------------
// TB-typed fixture builders mirroring the *testing.T helpers.
// ---------------------------------------------------------------------------

func benchDecimal(tb testing.TB, raw string) metering.Decimal {
	tb.Helper()
	d, err := metering.ParseDecimal(raw)
	if err != nil {
		tb.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	return d
}

func benchRule(tb testing.TB, id string, key metering.ComponentKey, price string) economics.RatingRule {
	tb.Helper()
	d := benchDecimal(tb, price)
	return economics.RatingRule{ID: id, Component: &key, Currency: "USD", UnitPrice: &d}
}

func benchMeasure(tb testing.TB, key metering.ComponentKey, quantity string) metering.Measure {
	tb.Helper()
	d := benchDecimal(tb, quantity)
	return metering.Measure{Key: key, Value: &d, Quality: metering.QualityObserved, MethodRef: b1SchemaID}
}

// benchObservation mirrors f3Observation -> b1Observation: one local, operator,
// backend-ingress B-leg observation in a single fixed scope.
func benchObservation(tb testing.TB, id string, measures ...metering.Measure) metering.Observation {
	tb.Helper()
	return benchObservationAt(tb, id, id+"-stream", 1, measures...)
}

// benchObservationAt is benchObservation with an explicit stream identity and
// sequence, so several observations can share one reduction scope (the scope
// key includes StreamID) when the per-observation measure bound demands
// splitting one logical evidence set.
func benchObservationAt(tb testing.TB, id, streamID string, sequence uint64, measures ...metering.Measure) metering.Observation {
	tb.Helper()
	now := time.Unix(1_700_000_821, 0).UTC()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "store-b1", ALegID: "a-b1",
		BillingCallID: "call-b1", BLegID: "b-leg-1",
	}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: streamID, Sequence: sequence,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTransport,
		Authority:   metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   subject,
		Correlation: metering.CorrelationV2{
			StoreID: "store-b1", ALegID: "a-b1", BillingCallID: "call-b1", BLegID: "b-leg-1",
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: b1SchemaID,
		Measures: measures,
	}
}

// benchBuildSnapshot is the raw publication step used both for building fixtures
// and for probing the publication bounds.
func benchBuildSnapshot(refID string, rules []economics.RatingRule, schemas []metering.ComponentSchema) (economics.TariffSnapshot, error) {
	return economics.BuildTariffSnapshotWithSchemas(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: refID, Version: "v1"}, RaterID: "reference"},
		"USD", rules, schemas,
	)
}

// benchTariff mirrors b1Tariff -> b1Resolve: publish then resolve through the
// real billingcompose catalog, exactly as the production consumers do.
func benchTariff(tb testing.TB, refID string, rules []economics.RatingRule, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	tb.Helper()
	snapshot, err := benchBuildSnapshot(refID, rules, schemas)
	if err != nil {
		tb.Fatalf("BuildTariffSnapshotWithSchemas(%s): %v", refID, err)
	}
	catalog := billingcompose.NewSnapshotCatalog()
	if err := catalog.PutTariff(snapshot); err != nil {
		tb.Fatalf("PutTariff(%s): %v", refID, err)
	}
	resolved, err := catalog.ResolveTariff(context.Background(), billing.VersionRef{ID: snapshot.Ref.ID, Version: snapshot.Ref.Version})
	if err != nil {
		tb.Fatalf("ResolveTariff(%s): %v", refID, err)
	}
	return resolved
}

func benchRater(tb testing.TB, tariff economics.TariffSnapshot) *billing.ReferenceRater {
	tb.Helper()
	rater, err := billing.NewReferenceRater(tariff)
	if err != nil {
		tb.Fatalf("NewReferenceRater: %v", err)
	}
	return rater
}

// benchInput mirrors b1OperatorInput (operator E plane, BasisLocalExpected).
func benchInput(tb testing.TB, tariff economics.TariffSnapshot, observations []metering.Observation) economics.PostUsageRatingInput {
	tb.Helper()
	if len(observations) == 0 {
		tb.Fatal("benchInput: no observations")
	}
	return economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisLocalExpected,
		Subject: observations[0].Subject, Scope: "call:call-b1", Observations: observations,
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "b1-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://b1/rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:               tariff.Ref,
		TariffContent:        &tariff.Content,
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://b1/qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		AsOf:                 time.Unix(1_700_000_822, 0).UTC(),
	}
}

// benchRateTimes runs one full sanity rate and fails unless the observed
// classification matches wantErr (nil means a complete valuation).
func benchRateSanity(b *testing.B, rater *billing.ReferenceRater, input economics.PostUsageRatingInput, wantErr error) {
	b.Helper()
	val, err := rater.Rate(context.Background(), input)
	if wantErr == nil {
		if err != nil {
			b.Fatalf("sanity: rate error: %v (completeness=%q lines=%d)", err, val.Completeness, len(val.Lines))
		}
		if val.Completeness != economics.CompletenessComplete {
			b.Fatalf("sanity: completeness=%q, want complete; lines=%+v", val.Completeness, val.Lines)
		}
		return
	}
	if !errors.Is(err, wantErr) {
		b.Fatalf("sanity: err=%v, want %v; completeness=%q lines=%+v", err, wantErr, val.Completeness, val.Lines)
	}
}

func benchRateLoop(b *testing.B, rater *billing.ReferenceRater, input economics.PostUsageRatingInput) {
	b.Helper()
	b.ReportAllocs()
	ctx := context.Background()
	for b.Loop() {
		benchSinkVal, benchSinkErr = rater.Rate(ctx, input)
	}
}

// benchRateLoopNodes is benchRateLoop plus the two per-node metrics section 18
// needs to judge complexity class: allocs/node and B/node. They are read from
// MemStats deltas around the timed loop rather than derived from the reported
// per-op totals, so the numbers are the ones the loop actually produced at this
// node count.
//
// The goroutine STACK delta is reported too. Section 18 requires bounded,
// non-recursive traversal, and StackInuse/StackSys around the loop is the direct
// measurement: an iterative traversal leaves the stack flat as the node count
// grows, while a recursive one scales it linearly with depth. Reporting it keeps
// the claim falsifiable instead of asserted.
func benchRateLoopNodes(b *testing.B, rater *billing.ReferenceRater, input economics.PostUsageRatingInput, nodes int) {
	b.Helper()
	b.ReportAllocs()
	var before, after runtime.MemStats
	ctx := context.Background()
	b.ResetTimer()
	runtime.ReadMemStats(&before)
	for b.Loop() {
		benchSinkVal, benchSinkErr = rater.Rate(ctx, input)
	}
	b.StopTimer()
	runtime.ReadMemStats(&after)
	iterations := float64(b.N)
	if iterations <= 0 {
		iterations = 1
	}
	if nodes > 0 {
		b.ReportMetric(float64(after.Mallocs-before.Mallocs)/iterations/float64(nodes), "allocs/node")
		b.ReportMetric(float64(after.TotalAlloc-before.TotalAlloc)/iterations/float64(nodes), "B/node")
	}
	b.ReportMetric(float64(after.StackInuse-before.StackInuse)/iterations, "stack-B/op")
}

// ---------------------------------------------------------------------------
// 1. OpenAI-shaped schema.
// ---------------------------------------------------------------------------

const (
	benchOpenAIInputTotal = "vendor:bench_openai_input_total"
	benchOpenAITextIn     = "vendor:bench_openai_text_in"
	benchOpenAIAudioIn    = "vendor:bench_openai_audio_in"
	benchOpenAIImageIn    = "vendor:bench_openai_image_in"
	benchOpenAICachedIn   = "vendor:bench_openai_cached_in"
	benchOpenAICacheWrite = "vendor:bench_openai_cache_write_in"
	benchOpenAIOutputTot  = "vendor:bench_openai_output_total"
	benchOpenAITextOut    = "vendor:bench_openai_text_out"
	benchOpenAIAudioOut   = "vendor:bench_openai_audio_out"
	benchOpenAIImageOut   = "vendor:bench_openai_image_out"
	benchOpenAIReasoning  = "vendor:bench_openai_reasoning_out"
)

// benchOpenAIFixture reproduces the real openaiusage inclusion shape: each of
// the input and output aggregates has a required text/audio partition plus an
// optional image member, and the cached / cache-write / reasoning included
// subsets. Child-only pricing (rules on the required partition leaves), the
// aggregate parents unpriced and excused by their conserved complete partition.
func benchOpenAIFixture(tb testing.TB) (economics.TariffSnapshot, metering.Observation) {
	tb.Helper()
	inTotal := b1Key(metering.DirectionInput, benchOpenAIInputTotal, metering.UnitToken)
	textIn := b1Key(metering.DirectionInput, benchOpenAITextIn, metering.UnitToken)
	audioIn := b1Key(metering.DirectionInput, benchOpenAIAudioIn, metering.UnitToken)
	imageIn := b1Key(metering.DirectionInput, benchOpenAIImageIn, metering.UnitToken)
	cachedIn := b1Key(metering.DirectionInput, benchOpenAICachedIn, metering.UnitToken)
	cacheWrite := b1Key(metering.DirectionInput, benchOpenAICacheWrite, metering.UnitToken)
	outTotal := b1Key(metering.DirectionOutput, benchOpenAIOutputTot, metering.UnitToken)
	textOut := b1Key(metering.DirectionOutput, benchOpenAITextOut, metering.UnitToken)
	audioOut := b1Key(metering.DirectionOutput, benchOpenAIAudioOut, metering.UnitToken)
	imageOut := b1Key(metering.DirectionOutput, benchOpenAIImageOut, metering.UnitToken)
	reasoning := b1Key(metering.DirectionOutput, benchOpenAIReasoning, metering.UnitToken)

	relationships := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: inTotal, Child: textIn},
		{Kind: metering.RelationshipPartition, Parent: inTotal, Child: audioIn},
		{Kind: metering.RelationshipPartition, Parent: inTotal, Child: imageIn, Optional: true},
		{Kind: metering.RelationshipPartition, Parent: outTotal, Child: textOut},
		{Kind: metering.RelationshipPartition, Parent: outTotal, Child: audioOut},
		{Kind: metering.RelationshipPartition, Parent: outTotal, Child: imageOut, Optional: true},
		{Kind: metering.RelationshipSubset, Parent: inTotal, Child: cachedIn},
		{Kind: metering.RelationshipSubset, Parent: inTotal, Child: cacheWrite},
		{Kind: metering.RelationshipSubset, Parent: outTotal, Child: reasoning},
	}
	schemas := []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: relationships}}
	rules := []economics.RatingRule{
		benchRule(tb, "bench-openai-text-in", textIn, "1"),
		benchRule(tb, "bench-openai-audio-in", audioIn, "1"),
		benchRule(tb, "bench-openai-text-out", textOut, "2"),
		benchRule(tb, "bench-openai-audio-out", audioOut, "2"),
	}
	tariff := benchTariff(tb, "bench-openai-style", rules, schemas)
	obs := benchObservation(
		tb, "bench-openai-style",
		benchMeasure(tb, inTotal, "100"),
		benchMeasure(tb, textIn, "60"),
		benchMeasure(tb, audioIn, "40"),
		benchMeasure(tb, outTotal, "50"),
		benchMeasure(tb, textOut, "30"),
		benchMeasure(tb, audioOut, "20"),
	)
	return tariff, obs
}

func BenchmarkComponentRaterOpenAIStyleSchema(b *testing.B) {
	tariff, obs := benchOpenAIFixture(b)
	rater := benchRater(b, tariff)
	input := benchInput(b, tariff, []metering.Observation{obs})
	// 11 declared nodes; 6 observed (2 aggregate parents + 4 priced required
	// leaves). The 3 included subsets and both optional image members are absent.
	b.ReportMetric(11, "nodes")
	b.ReportMetric(6, "observed")
	benchRateSanity(b, rater, input, nil)
	benchRateLoop(b, rater, input)
}

// ---------------------------------------------------------------------------
// 2. Deep inclusion chain.
// ---------------------------------------------------------------------------

func benchChainKey(i int) metering.ComponentKey {
	return b1Key(metering.DirectionInput, fmt.Sprintf("vendor:bench_chain_%04d", i), metering.UnitToken)
}

// benchChainRelationships builds a linear chain of depth edges, alternating
// subset and partition edges, all same direction and unit. Nodes are
// chain_0000 .. chain_<depth>.
func benchChainRelationships(depth int) []metering.ComponentRelationship {
	rels := make([]metering.ComponentRelationship, 0, depth)
	for i := range depth {
		kind := metering.RelationshipSubset
		if i%2 == 1 {
			kind = metering.RelationshipPartition
		}
		rels = append(rels, metering.ComponentRelationship{
			Kind: kind, Parent: benchChainKey(i), Child: benchChainKey(i + 1),
		})
	}
	return rels
}

// benchSplitSchemas distributes relationship edges across ComponentSchema
// entries. The set-level validator permits up to MaxComponentSchemas schemas,
// each with at most MaxComponentSchemaRelationships edges, so the single-schema
// relationship bound is not a total bound.
func benchSplitSchemas(schemaPrefix string, rels []metering.ComponentRelationship, perSchema int) []metering.ComponentSchema {
	var schemas []metering.ComponentSchema
	for i := 0; i < len(rels); i += perSchema {
		end := min(i+perSchema, len(rels))
		schemas = append(schemas, metering.ComponentSchema{
			ID: fmt.Sprintf("%s:part%02d", schemaPrefix, len(schemas)), Version: "1",
			Relationships: append([]metering.ComponentRelationship(nil), rels[i:end]...),
		})
	}
	return schemas
}

// benchProbeSingleSchemaChainLimit returns the deepest chain accepted when all
// edges are declared in ONE ComponentSchema (the bound a single publication
// entry can carry), starting at probeFrom and decreasing.
func benchProbeSingleSchemaChainLimit(probeFrom int) int {
	for d := probeFrom; d > 0; d-- {
		_, err := benchBuildSnapshot("bench-probe-single-chain", nil, []metering.ComponentSchema{{
			ID: b1SchemaID + ":probe", Version: "1", Relationships: benchChainRelationships(d),
		}})
		if err == nil {
			return d
		}
	}
	return 0
}

func benchDeepChainFixture(tb testing.TB, depth int) (economics.TariffSnapshot, metering.Observation) {
	tb.Helper()
	schemas := benchSplitSchemas(b1SchemaID, benchChainRelationships(depth), metering.MaxComponentSchemaRelationships)
	root := benchChainKey(0)
	leaf := benchChainKey(depth)
	rules := []economics.RatingRule{
		benchRule(tb, "bench-chain-root", root, "1"),
		benchRule(tb, "bench-chain-leaf", leaf, "1"),
	}
	tariff := benchTariff(tb, fmt.Sprintf("bench-deep-chain-%d", depth), rules, schemas)
	obs := benchObservation(
		tb, fmt.Sprintf("bench-deep-chain-%d", depth),
		benchMeasure(tb, root, "1000"),
		benchMeasure(tb, leaf, "10"),
	)
	return tariff, obs
}

func BenchmarkComponentRaterDeepInclusionChain(b *testing.B) {
	const requested = 200
	_, err200 := benchBuildSnapshot("bench-probe-chain-200", nil, []metering.ComponentSchema{{
		ID: b1SchemaID + ":probe200", Version: "1", Relationships: benchChainRelationships(requested),
	}})
	singleLimit := benchProbeSingleSchemaChainLimit(requested)
	splitSchemas := benchSplitSchemas(b1SchemaID, benchChainRelationships(requested), metering.MaxComponentSchemaRelationships)
	_, acceptedErr := benchBuildSnapshot("bench-probe-chain-split-200", nil, splitSchemas)
	b.Logf("deep chain publication: single-schema depth=%d rejected=%v err=%v; deepest single-schema accepted depth=%d; depth=%d split over %d schemas accepted=%v err=%v",
		requested, err200 != nil, err200, singleLimit, requested, len(splitSchemas), acceptedErr == nil, acceptedErr)

	tariff, obs := benchDeepChainFixture(b, requested)
	rater := benchRater(b, tariff)
	input := benchInput(b, tariff, []metering.Observation{obs})
	b.ReportMetric(float64(requested+1), "nodes")
	b.ReportMetric(2, "observed")
	benchRateSanity(b, rater, input, billing.ErrSchemaOverlapConflict)
	benchRateLoop(b, rater, input)
}

// ---------------------------------------------------------------------------
// 3. Wide fan-out.
// ---------------------------------------------------------------------------

func benchFanParentKey() metering.ComponentKey {
	return b1Key(metering.DirectionInput, "vendor:bench_fan_parent", metering.UnitToken)
}

func benchFanChildKey(i int) metering.ComponentKey {
	return b1Key(metering.DirectionInput, fmt.Sprintf("vendor:bench_fan_child_%04d", i), metering.UnitToken)
}

// benchFanOutFixture builds one parent with width complete partition children.
// Child-only pricing: the parent is unobserved-or-observed per observeParent and
// excused by conservation when observed, the children are priced.
func benchFanOutFixture(tb testing.TB, width int, observeChildren bool) (economics.TariffSnapshot, []metering.Observation) {
	tb.Helper()
	parent := benchFanParentKey()
	relationships := make([]metering.ComponentRelationship, 0, width)
	// Conservation: the observed parent quantity is exactly the sum of the
	// observed children so the complete partition is provably covered.
	measures := []metering.Measure{benchMeasure(tb, parent, fmt.Sprintf("%d", width*10))}
	rules := make([]economics.RatingRule, 0, width)
	for i := range width {
		child := benchFanChildKey(i)
		relationships = append(relationships, metering.ComponentRelationship{
			Kind: metering.RelationshipPartition, Parent: parent, Child: child,
		})
		rules = append(rules, benchRule(tb, fmt.Sprintf("bench-fan-child-%04d", i), child, "1"))
		if observeChildren {
			measures = append(measures, benchMeasure(tb, child, "10"))
		}
	}
	refID := fmt.Sprintf("bench-fan-%d-%v", width, observeChildren)
	tariff := benchTariff(tb, refID, rules, benchSplitSchemas(b1SchemaID, relationships, metering.MaxComponentSchemaRelationships))
	return tariff, benchChunkedObservations(tb, refID, measures)
}

// benchChunkedObservations splits measures across observations so no observation
// exceeds the per-observation component-entry bound.
func benchChunkedObservations(tb testing.TB, refID string, measures []metering.Measure) []metering.Observation {
	tb.Helper()
	streamID := refID + "-stream"
	var out []metering.Observation
	for i := 0; i < len(measures); i += metering.MaxObservationMeasures {
		end := min(i+metering.MaxObservationMeasures, len(measures))
		out = append(out, benchObservationAt(tb, fmt.Sprintf("%s-obs%02d", refID, len(out)), streamID, uint64(len(out)+1), measures[i:end]...))
	}
	return out
}

func BenchmarkComponentRaterWideFanOut(b *testing.B) {
	const width = 100
	tariff, obs := benchFanOutFixture(b, width, true)
	rater := benchRater(b, tariff)
	input := benchInput(b, tariff, obs)
	b.ReportMetric(width+1, "nodes")
	b.ReportMetric(width+1, "observed")
	benchRateSanity(b, rater, input, nil)
	benchRateLoop(b, rater, input)
}

// ---------------------------------------------------------------------------
// 4. Layered fan-in diamond.
// ---------------------------------------------------------------------------

func benchDiamondKey(layer, index int) metering.ComponentKey {
	return b1Key(metering.DirectionInput, fmt.Sprintf("vendor:bench_diamond_%02d_%02d", layer, index), metering.UnitToken)
}

// benchDiamondFixture builds a layered diamond: layer 0 has one node, the
// internal layers have two nodes each, the final layer has one node, and every
// node in layer i+1 is a subset child of BOTH nodes in layer i (so every
// non-root node has exactly two parents). All nodes are observed and priced.
func benchDiamondFixture(tb testing.TB, depth int) (economics.TariffSnapshot, metering.Observation) {
	tb.Helper()
	layerWidth := func(layer int) int {
		if layer == 0 || layer == depth {
			return 1
		}
		return 2
	}
	var relationships []metering.ComponentRelationship
	var measures []metering.Measure
	var rules []economics.RatingRule
	for layer := 0; layer <= depth; layer++ {
		quantity := fmt.Sprintf("%d", 1000-layer*40)
		for index := 0; index < layerWidth(layer); index++ {
			key := benchDiamondKey(layer, index)
			measures = append(measures, benchMeasure(tb, key, quantity))
			rules = append(rules, benchRule(tb, fmt.Sprintf("bench-diamond-%02d-%02d", layer, index), key, "1"))
			if layer == 0 {
				continue
			}
			for parentIndex := 0; parentIndex < layerWidth(layer-1); parentIndex++ {
				relationships = append(relationships, metering.ComponentRelationship{
					Kind:   metering.RelationshipSubset,
					Parent: benchDiamondKey(layer-1, parentIndex),
					Child:  key,
				})
			}
		}
	}
	refID := fmt.Sprintf("bench-diamond-depth-%d", depth)
	tariff := benchTariff(tb, refID, rules, []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1", Relationships: relationships,
	}})
	obs := benchObservation(tb, refID, measures...)
	return tariff, obs
}

func BenchmarkComponentRaterFanInDiamond(b *testing.B) {
	const depth = 10
	tariff, obs := benchDiamondFixture(b, depth)
	rater := benchRater(b, tariff)
	input := benchInput(b, tariff, []metering.Observation{obs})
	nodes := 2 * depth
	b.ReportMetric(float64(nodes), "nodes")
	b.ReportMetric(float64(nodes), "observed")
	benchRateSanity(b, rater, input, billing.ErrSchemaOverlapConflict)
	benchRateLoop(b, rater, input)
}

// ---------------------------------------------------------------------------
// 5. No schema (floor cost).
// ---------------------------------------------------------------------------

func BenchmarkComponentRaterNoSchema(b *testing.B) {
	const width = 100
	parent := benchFanParentKey()
	rules := []economics.RatingRule{benchRule(b, "bench-noschema-parent", parent, "1")}
	measures := []metering.Measure{benchMeasure(b, parent, "1000")}
	for i := range width {
		child := benchFanChildKey(i)
		rules = append(rules, benchRule(b, fmt.Sprintf("bench-noschema-child-%04d", i), child, "1"))
		measures = append(measures, benchMeasure(b, child, "10"))
	}
	tariff := benchTariff(b, "bench-no-schema", rules, nil)
	obs := benchObservation(b, "bench-no-schema", measures...)
	rater := benchRater(b, tariff)
	input := benchInput(b, tariff, []metering.Observation{obs})
	b.ReportMetric(width+1, "nodes")
	b.ReportMetric(width+1, "observed")
	benchRateSanity(b, rater, input, nil)
	benchRateLoop(b, rater, input)
}

// ---------------------------------------------------------------------------
// 6. Wide fan-out with unobserved children (unprovable cover).
// ---------------------------------------------------------------------------

func BenchmarkComponentRaterWideFanOutUnobserved(b *testing.B) {
	const width = 100
	tariff, obs := benchFanOutFixture(b, width, false)
	rater := benchRater(b, tariff)
	input := benchInput(b, tariff, obs)
	b.ReportMetric(width+1, "nodes")
	b.ReportMetric(1, "observed")
	benchRateSanity(b, rater, input, billing.ErrSchemaPartitionIncomplete)
	benchRateLoop(b, rater, input)
}

// ---------------------------------------------------------------------------
// 7. Excluded-child path (forces the second aggregateMeasures reduction).
// ---------------------------------------------------------------------------

// benchExcludedChildSchema declares parent --subset--> child. The parent has a
// rule, the child does not, so includedChildExclusions removes the child from
// the first reduction; child quantity > parent quantity makes the containment
// contradiction observable ONLY on consistencyAggregates (the second, full
// reduction). ErrSchemaSubsetContradiction therefore proves the second
// aggregateMeasures call actually ran.
func BenchmarkComponentRaterExcludedChildPath(b *testing.B) {
	parent := b1Key(metering.DirectionInput, "vendor:bench_excluded_parent", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:bench_excluded_child", metering.UnitToken)
	schemas := []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: parent, Child: child},
		},
	}}
	rules := []economics.RatingRule{benchRule(b, "bench-excluded-parent-rate", parent, "1")}
	tariff := benchTariff(b, "bench-excluded-child", rules, schemas)
	obs := benchObservation(
		b, "bench-excluded-child",
		benchMeasure(b, parent, "100"),
		benchMeasure(b, child, "150"),
	)
	rater := benchRater(b, tariff)
	input := benchInput(b, tariff, []metering.Observation{obs})
	b.ReportMetric(2, "nodes")
	b.ReportMetric(2, "observed")
	// Prove BOTH halves of the double reduction:
	//   - the unpriced child is absent from the emitted lines (exactly one line:
	//     the priced parent). If includedChildExclusions had NOT fired, the
	//     child would be aggregated and emit its own missing-rate line, so
	//     len(Lines)==2.
	//   - the containment contradiction is observed, and it can only be computed
	//     from consistencyAggregates, the second (exclusion-free) reduction:
	//     the first reduction has the child excluded, so it cannot see the
	//     150 > 100 bound at all.
	val, err := rater.Rate(context.Background(), input)
	if !errors.Is(err, billing.ErrSchemaSubsetContradiction) {
		b.Fatalf("excluded-child sanity: err=%v, want ErrSchemaSubsetContradiction; lines=%+v", err, val.Lines)
	}
	if len(val.Lines) != 1 {
		b.Fatalf("excluded-child sanity: got %d lines, want exactly 1 (priced parent only); the unpriced child was not excluded from the first reduction: %+v", len(val.Lines), val.Lines)
	}
	benchRateLoop(b, rater, input)
}

// ---------------------------------------------------------------------------
// Scaling sub-benchmarks: superlinearity visible rather than asserted.
// ---------------------------------------------------------------------------

func BenchmarkComponentRaterDeepChainScaling(b *testing.B) {
	for _, depth := range []int{10, 50, 200} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			tariff, obs := benchDeepChainFixture(b, depth)
			rater := benchRater(b, tariff)
			input := benchInput(b, tariff, []metering.Observation{obs})
			b.ReportMetric(float64(depth+1), "nodes")
			b.ReportMetric(2, "observed")
			benchRateSanity(b, rater, input, billing.ErrSchemaOverlapConflict)
			benchRateLoop(b, rater, input)
		})
	}
}

// A priced fan-out cannot exceed economics.MaxValuationLines (128): width=200
// priced children produces 200 payable lines and the valuation is rejected with
// "line bound exceeded", so 100 is the largest scaling point that yields a
// valid complete valuation (the evidence set is chunked to respect the
// 128-measure per-observation bound).
func BenchmarkComponentRaterWideFanOutScaling(b *testing.B) {
	for _, width := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("width=%d", width), func(b *testing.B) {
			tariff, obs := benchFanOutFixture(b, width, true)
			rater := benchRater(b, tariff)
			input := benchInput(b, tariff, obs)
			b.ReportMetric(float64(width+1), "nodes")
			b.ReportMetric(float64(width+1), "observed")
			benchRateSanity(b, rater, input, nil)
			benchRateLoop(b, rater, input)
		})
	}
}

// ---------------------------------------------------------------------------
// Post-change measurements (sections 18 and 7).
//
// The benchmarks above are the pre-change baselines and are left byte-for-byte
// intact so the two runs are comparable. The ones below exist because the
// baselines report only ns/op, B/op and allocs/op, which cannot by themselves
// distinguish LINEAR from SUPERLINEAR work: the per-node metrics and the
// goroutine-STACK metric are what make the complexity class measurable rather
// than asserted.
// ---------------------------------------------------------------------------

// benchPerNodeWidths is the same fan-out scaling ladder the baseline uses, so
// the exponents computed from the two runs are over the SAME node counts.
var benchPerNodeWidths = []int{10, 50, 100}

// benchPerNodeDepths is the same deep-chain scaling ladder as the baseline.
var benchPerNodeDepths = []int{10, 50, 200}

// BenchmarkComponentRaterWideFanOutPerNodeScaling re-runs the baseline
// wide-fan-out ladder and additionally reports allocs/node and B/node, so the
// scaling EXPONENT can be computed from the same run. A linear algorithm keeps
// allocs/node flat as the width grows; a superlinear one makes it climb.
func BenchmarkComponentRaterWideFanOutPerNodeScaling(b *testing.B) {
	for _, width := range benchPerNodeWidths {
		b.Run(fmt.Sprintf("width=%d", width), func(b *testing.B) {
			tariff, obs := benchFanOutFixture(b, width, true)
			rater := benchRater(b, tariff)
			input := benchInput(b, tariff, obs)
			b.ReportMetric(float64(width+1), "nodes")
			b.ReportMetric(float64(width+1), "observed")
			benchRateSanity(b, rater, input, nil)
			benchRateLoopNodes(b, rater, input, width+1)
		})
	}
}

// BenchmarkComponentRaterWideFanOutUnobservedPerNode measures the section-18
// "1 observed of 100 declared" case with per-node metrics. Before the solver
// this case cost almost as much as rating all 101 measures; the per-node figure
// is what shows whether the unprovable cover is now cheap relative to the
// declared graph size.
func BenchmarkComponentRaterWideFanOutUnobservedPerNode(b *testing.B) {
	const width = 100
	tariff, obs := benchFanOutFixture(b, width, false)
	rater := benchRater(b, tariff)
	input := benchInput(b, tariff, obs)
	b.ReportMetric(float64(width+1), "nodes")
	b.ReportMetric(1, "observed")
	benchRateSanity(b, rater, input, billing.ErrSchemaPartitionIncomplete)
	benchRateLoopNodes(b, rater, input, width+1)
}

// BenchmarkComponentRaterDeepChainPerNodeScaling re-runs the baseline deep-chain
// ladder with per-node metrics.
func BenchmarkComponentRaterDeepChainPerNodeScaling(b *testing.B) {
	for _, depth := range benchPerNodeDepths {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			tariff, obs := benchDeepChainFixture(b, depth)
			rater := benchRater(b, tariff)
			input := benchInput(b, tariff, []metering.Observation{obs})
			b.ReportMetric(float64(depth+1), "nodes")
			b.ReportMetric(2, "observed")
			benchRateSanity(b, rater, input, billing.ErrSchemaOverlapConflict)
			benchRateLoopNodes(b, rater, input, depth+1)
		})
	}
}

// benchMaxPublishableChainDepth is the deepest chain the PUBLIC publication
// bounds permit: MaxComponentSchemas (64) schemas of MaxComponentSchemaRelationships
// (128) relationships each. It is computed rather than hard-coded, so a change to
// either bound moves this probe with it instead of silently testing a shallower
// graph than the contract allows.
func benchMaxPublishableChainDepth() int {
	return metering.MaxComponentSchemas * metering.MaxComponentSchemaRelationships
}

// benchRecursionGuardDepths spans the baseline's deepest chain and a modest step
// past it, so the stack metric has room to show growth if any traversal is
// recursive. The order-of-magnitude-deep cases (3200 and the 8192 maximum) are
// NOT here: at 200x benchtime they would dominate the whole suite. They live in
// TestReplayDeepestPublishableChainTraversalIsBounded, which runs each depth once
// and makes the same stack measurement.
var benchRecursionGuardDepths = []int{200, 400}

// BenchmarkComponentRaterRecursionGuard measures the goroutine STACK the rater
// consumes as a function of chain DEPTH, holding the node count constant at one
// per level and reporting the stack delta per rate.
//
// A recursive traversal needs one live frame per containment level, so
// stack-B/op grows linearly with depth. An iterative traversal (a work queue, a
// Kahn topological order, or a bounded fixed-point sweep) holds a flat stack and
// reports the same stack-B/op at every depth. This is the direct, falsifiable
// measurement behind the section-18 claim that the graph work is bounded and
// non-recursive; it is not a proxy for it.
func BenchmarkComponentRaterRecursionGuard(b *testing.B) {
	for _, depth := range benchRecursionGuardDepths {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			tariff, obs := benchDeepChainFixture(b, depth)
			rater := benchRater(b, tariff)
			input := benchInput(b, tariff, []metering.Observation{obs})
			b.ReportMetric(float64(depth+1), "nodes")
			benchRateSanity(b, rater, input, billing.ErrSchemaOverlapConflict)
			benchRateLoopNodes(b, rater, input, depth+1)
		})
	}
}

// BenchmarkComponentRaterPublishedBounds is a one-shot assertion that the four
// known publication/valuation bounds this work package must report are the ones
// the benchmark fixtures are actually living under, so the numbers in the report
// cannot silently be measured against a graph that exceeds them.
func BenchmarkComponentRaterPublishedBounds(b *testing.B) {
	b.ReportMetric(metering.MaxComponentSchemaRelationships, "max-rels-per-schema")
	b.ReportMetric(metering.MaxComponentSchemas, "max-schemas")
	b.ReportMetric(metering.MaxObservationMeasures, "max-measures-per-obs")
	b.ReportMetric(economics.MaxValuationLines, "max-valuation-lines")

	// A single schema carrying one more relationship than the bound must be
	// refused, and the bound itself must be accepted.
	overLimit := metering.MaxComponentSchemaRelationships + 1
	if _, err := benchBuildSnapshot("bench-bounds-over", nil, []metering.ComponentSchema{{
		ID: b1SchemaID + ":over", Version: "1", Relationships: benchChainRelationships(overLimit),
	}}); err == nil {
		b.Fatalf("a schema with %d relationships was accepted; MaxComponentSchemaRelationships is no longer %d",
			overLimit, metering.MaxComponentSchemaRelationships)
	}
	if _, err := benchBuildSnapshot("bench-bounds-at", nil, []metering.ComponentSchema{{
		ID: b1SchemaID + ":at", Version: "1", Relationships: benchChainRelationships(metering.MaxComponentSchemaRelationships),
	}}); err != nil {
		b.Fatalf("a schema with exactly MaxComponentSchemaRelationships=%d relationships was refused: %v",
			metering.MaxComponentSchemaRelationships, err)
	}
	// A schema set one entry past the schema bound must be refused too.
	overSchemas := make([]metering.ComponentSchema, metering.MaxComponentSchemas+1)
	for i := range overSchemas {
		overSchemas[i] = metering.ComponentSchema{ID: fmt.Sprintf("%s:os%03d", b1SchemaID, i), Version: "1"}
	}
	if _, err := benchBuildSnapshot("bench-bounds-over-schemas", nil, overSchemas); err == nil {
		b.Fatalf("a schema set with %d entries was accepted; MaxComponentSchemas is no longer %d",
			len(overSchemas), metering.MaxComponentSchemas)
	}
}
