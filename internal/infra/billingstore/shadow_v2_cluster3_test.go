package billingstore

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/normalize"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestPhase172Cluster3WrongMagnitudeSameReasonIsDrift(t *testing.T) {
	t.Parallel()
	result, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label:          "reasoning-once-fix",
		Currency:       "USD",
		V1Nano:         250000000,
		V2Nano:         900000000,
		ExpectedV2Nano: 200000000,
		ReasonCode:     "reasoning-in-output-once",
	})
	require.NoError(t, err)
	require.Equal(t, ShadowV2UnexpectedDrift, result.Verdict, "severe regression under a fix label must be drift, detail=%s", result.Detail)
}

func TestPhase172Cluster3WrongDirectionIsDrift(t *testing.T) {
	t.Parallel()
	result, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label:          "wrong-direction-drift",
		Currency:       "USD",
		V1Nano:         250000000,
		V2Nano:         300000000,
		ExpectedV2Nano: 200000000,
		ReasonCode:     "reasoning-in-output-once",
	})
	require.NoError(t, err)
	require.Equal(t, ShadowV2UnexpectedDrift, result.Verdict)
}

func TestPhase172Cluster3FixWithoutChangeIsDrift(t *testing.T) {
	t.Parallel()
	result, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label:          "claimed-fix-no-change",
		Currency:       "USD",
		V1Nano:         100000000,
		V2Nano:         100000000,
		ExpectedV2Nano: 100000000,
		ReasonCode:     "reasoning-in-output-once",
	})
	require.NoError(t, err)
	require.Equal(t, ShadowV2UnexpectedDrift, result.Verdict, "claimed fix with no amount change is inconsistent")
}

func TestPhase172Cluster3ChangeWithoutReasonIsDrift(t *testing.T) {
	t.Parallel()
	result, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label:          "unexplained-change",
		Currency:       "USD",
		V1Nano:         100000000,
		V2Nano:         110000000,
		ExpectedV2Nano: 110000000,
		ReasonCode:     "",
	})
	require.NoError(t, err)
	require.Equal(t, ShadowV2UnexpectedDrift, result.Verdict)
}

func TestPhase172Cluster3OverflowRejected(t *testing.T) {
	t.Parallel()
	_, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label:          "overflow-delta",
		Currency:       "USD",
		V1Nano:         math.MinInt64,
		V2Nano:         math.MaxInt64,
		ExpectedV2Nano: math.MaxInt64,
		ReasonCode:     "",
	})
	require.ErrorIs(t, err, ErrShadowV2ComparisonInvalid)
	_, err = CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label:          "negative-amount",
		Currency:       "USD",
		V1Nano:         -1,
		V2Nano:         100,
		ExpectedV2Nano: 100,
		ReasonCode:     "",
	})
	require.ErrorIs(t, err, ErrShadowV2ComparisonInvalid)
}

func TestPhase172Cluster3UnsafeLabelsRejected(t *testing.T) {
	t.Parallel()
	unsafe := []string{
		"Bearer abc123",
		"../../etc/passwd",
		"has space",
		"tab\there",
		"unicode-caf\u00e9",
		"trailing ",
		"Uppercase",
		"with.dot",
		"with/slash",
		"",
		strings.Repeat("a", 65),
	}
	for _, label := range unsafe {
		_, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
			Label:          label,
			Currency:       "USD",
			V1Nano:         100,
			V2Nano:         100,
			ExpectedV2Nano: 100,
			ReasonCode:     "",
		})
		require.ErrorIs(t, err, ErrShadowV2ComparisonInvalid, "unsafe label %q must be rejected", label)
	}
}

func TestPhase172Cluster3ReasonAndCurrencyClosed(t *testing.T) {
	t.Parallel()
	_, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label: "valid-label", Currency: "USD",
		V1Nano: 100, V2Nano: 100, ExpectedV2Nano: 100,
		ReasonCode: "not-a-real-fix",
	})
	require.ErrorIs(t, err, ErrShadowV2ComparisonInvalid)
	_, err = CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label: "valid-label", Currency: "XXX",
		V1Nano: 100, V2Nano: 100, ExpectedV2Nano: 100,
	})
	require.ErrorIs(t, err, ErrShadowV2ComparisonInvalid)
	_, err = CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label: "valid-label", Currency: "usd",
		V1Nano: 100, V2Nano: 100, ExpectedV2Nano: 100,
	})
	require.ErrorIs(t, err, ErrShadowV2ComparisonInvalid)
}

func TestPhase172Cluster3ErrorsDoNotEchoUnsafeInput(t *testing.T) {
	t.Parallel()
	evil := "Bearer sk-evil\n../../x"
	_, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label: evil, Currency: "USD",
		V1Nano: 100, V2Nano: 100, ExpectedV2Nano: 100,
	})
	require.ErrorIs(t, err, ErrShadowV2ComparisonInvalid)
	require.NotContains(t, err.Error(), "Bearer")
	require.NotContains(t, err.Error(), "sk-evil")
	_, err = CompareShadowV2Semantics(ShadowV2ComparisonInput{
		Label: "ok-label", Currency: "USD",
		V1Nano: 100, V2Nano: 100, ExpectedV2Nano: 100,
		ReasonCode: "evil reason\n",
	})
	require.ErrorIs(t, err, ErrShadowV2ComparisonInvalid)
	require.NotContains(t, err.Error(), "evil")
}

type r2LineSpec struct {
	Tokens  string `json:"tokens"`
	RateKey string `json:"rate_key"`
}

type r2Vector struct {
	Label         string            `json:"label"`
	Currency      string            `json:"currency"`
	ReasonCode    string            `json:"reason_code"`
	ExpectVerdict string            `json:"expect_verdict"`
	Mapping       string            `json:"mapping"`
	Tariff        string            `json:"tariff"`
	Basis         string            `json:"basis"`
	Fields        map[string]string `json:"fields"`
	V1Lines       []r2LineSpec      `json:"v1_lines"`
	ExpectedLines []r2LineSpec      `json:"expected_lines"`
}

type r2Fixture struct {
	Version      int              `json:"version"`
	Provenance   string           `json:"provenance"`
	RateSchedule map[string]int64 `json:"rate_schedule_nano_per_token"`
	Vectors      []r2Vector       `json:"vectors"`
}

func loadR2Fixture(t *testing.T) r2Fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "phase17_2_shadow_semantics.json"))
	require.NoError(t, err)
	var fixture r2Fixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.Equal(t, 2, fixture.Version)
	require.NotEmpty(t, fixture.Provenance)
	require.NotEmpty(t, fixture.Vectors)
	text := string(raw)
	require.NotContains(t, text, "sk-")
	require.NotContains(t, text, "secret")
	require.NotContains(t, text, "Bearer ")
	require.NotContains(t, text, "prompt")
	return fixture
}

// r2NanoSum independently computes an expected/defect amount from fixture line
// inputs with checked integer arithmetic. It never calls production rating.
func r2NanoSum(t *testing.T, rates map[string]int64, lines []r2LineSpec) int64 {
	t.Helper()
	var total int64
	for _, line := range lines {
		rate, ok := rates[line.RateKey]
		require.True(t, ok, "unknown rate key %q", line.RateKey)
		require.GreaterOrEqual(t, rate, int64(0))
		qty, err := strconv.ParseInt(line.Tokens, 10, 64)
		require.NoError(t, err)
		require.GreaterOrEqual(t, qty, int64(0))
		if qty > 0 {
			require.LessOrEqual(t, rate, math.MaxInt64/qty, "rate overflow")
		}
		product := qty * rate
		require.LessOrEqual(t, product, math.MaxInt64-total, "sum overflow")
		total += product
	}
	return total
}

func r2Spec(name, path string, direction metering.FlowDirection, component string) normalize.FieldSpec {
	return normalize.FieldSpec{
		Name: name, EvidencePath: path,
		Key: metering.ComponentKey{Direction: direction, Component: component, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID},
	}
}

// r2Mapping returns the frozen production mappings under test. They are
// versioned config, not per-vector inventions.
func r2Mapping(t *testing.T, id string) normalize.Mapping {
	t.Helper()
	base := normalize.Mapping{
		ID: "r2.reasoning", Family: "r2", Version: "v1",
		InputMode:     normalize.InputSeparate,
		InputUncached: r2Spec("input", "$.usage.input_tokens", metering.DirectionInput, metering.ComponentInputToken),
		CacheRead:     []normalize.FieldSpec{r2Spec("read", "$.usage.cache_read_input_tokens", metering.DirectionInput, metering.ComponentCacheReadInputToken)},
		CacheWrite:    []normalize.FieldSpec{r2Spec("write", "$.usage.cache_write_input_tokens", metering.DirectionInput, metering.ComponentCacheWriteInputToken)},
		Output:        r2Spec("output", "$.usage.output_tokens", metering.DirectionOutput, metering.ComponentOutputToken),
		Reasoning:     r2Spec("reasoning", "$.usage.reasoning_output_tokens", metering.DirectionOutput, metering.ComponentReasoningOutputToken),
		ReasoningMode: normalize.ReasoningIncluded,
	}
	switch id {
	case "reasoning-included":
		return base
	case "reasoning-disjoint":
		base.ReasoningMode = normalize.ReasoningDisjoint
		return base
	case "inclusive":
		base.ID = "r2.inclusive"
		base.InputMode = normalize.InputInclusive
		base.InputTotal = r2Spec("total", "$.usage.total_tokens", metering.DirectionInput, metering.ComponentInputTokenTotal)
		return base
	default:
		t.Fatalf("unknown frozen mapping %q", id)
		return normalize.Mapping{}
	}
}

func r2Rule(t *testing.T, id, component string, direction metering.FlowDirection, nanoPerToken int64) economics.RatingRule {
	t.Helper()
	require.GreaterOrEqual(t, nanoPerToken, int64(0))
	scale := uint8(9)
	coeff := strconv.FormatInt(nanoPerToken, 10)
	if nanoPerToken == 0 {
		scale = 0
	}
	price := metering.Decimal{Coefficient: coeff, Scale: scale}
	key := metering.ComponentKey{Direction: direction, Component: component, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	return economics.RatingRule{ID: id, Component: &key, Currency: "USD", UnitPrice: &price}
}

// r2Tariff returns the frozen production tariffs under test. T1 leaves
// included reasoning explicitly free; T2 is the regressed config that prices
// it. Both are built through the production tariff constructor.
func r2Tariff(t *testing.T, id string, rates map[string]int64) economics.TariffSnapshot {
	t.Helper()
	rules := []economics.RatingRule{
		r2Rule(t, "output", metering.ComponentOutputToken, metering.DirectionOutput, rates["output"]),
		r2Rule(t, "input", metering.ComponentInputToken, metering.DirectionInput, rates["input_uncached"]),
		r2Rule(t, "read", metering.ComponentCacheReadInputToken, metering.DirectionInput, rates["cache_read"]),
		r2Rule(t, "write", metering.ComponentCacheWriteInputToken, metering.DirectionInput, rates["cache_write"]),
	}
	switch id {
	case "t1":
		rules = append(rules, r2Rule(t, "reasoning-free", metering.ComponentReasoningOutputToken, metering.DirectionOutput, rates["reasoning_t1"]))
	case "t2":
		rules = append(rules, r2Rule(t, "reasoning-priced", metering.ComponentReasoningOutputToken, metering.DirectionOutput, rates["reasoning_t2"]))
	default:
		t.Fatalf("unknown frozen tariff %q", id)
	}
	snapshot, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r2-tariff", Version: "v1"}, RaterID: "reference"},
		"USD", rules)
	require.NoError(t, err)
	return snapshot
}

func r2Observation(t *testing.T, obsID string, revision uint64, origin, acquisition string, result normalize.Result) metering.Observation {
	t.Helper()
	base := metering.Observation{
		Version: metering.ObservationVersionV2, ID: obsID, SourceEventKey: obsID, Revision: revision,
		StreamID: "r2-stream", Sequence: revision,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant-r2",
			AccountID: "r2-acct", ALegID: "a-r2", BillingCallID: "call-r2",
			BLegID: "b-r2", AttemptID: "attempt-r2", AttemptSeq: revision,
		},
		Correlation: metering.CorrelationV2{
			StoreID: "test", TenantID: "tenant-r2", CallID: "call-r2",
			BillingCallID: "call-r2", ALegID: "a-r2", BLegID: "b-r2",
			AttemptID: "attempt-r2", AttemptSeq: revision,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: time.Unix(1_700_195_000, 0).UTC(), ReceivedAt: time.Unix(1_700_195_001, 0).UTC(),
	}
	attached, err := result.Attach(base)
	require.NoError(t, err)
	return attached
}

func r2RatingInput(t *testing.T, basis economics.ValuationBasis, observation metering.Observation, snapshot economics.TariffSnapshot) economics.PostUsageRatingInput {
	t.Helper()
	return economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: basis,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r2-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://r2-rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:               snapshot.Ref,
		TariffContent:        &snapshot.Content,
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://r2-qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		AsOf:                 time.Unix(100, 0).UTC(),
	}
}

func r2Work(t *testing.T, observation metering.Observation, basis economics.ValuationBasis, headKey string, revision uint64, snapshot economics.TariffSnapshot) billing.EconomicRevisionWork {
	t.Helper()
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey,
		Subject: observation.Subject, EvidenceRevision: revision,
		Input:     r2RatingInput(t, basis, observation, snapshot),
		CreatedAt: time.Unix(1_700_196_000+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

//nolint:revive // test helper keeps t first per Go testing convention
func r2PersistedTotalNano(t *testing.T, ctx context.Context, store *DurableStore, work billing.EconomicRevisionWork) int64 {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.NotEmpty(t, valuation.Totals)
	require.True(t, valuation.Totals[0].RoundedAmount.Present)
	return valuation.Totals[0].RoundedAmount.NanoUnits
}

func r2Basis(t *testing.T, name string) economics.ValuationBasis {
	t.Helper()
	switch name {
	case "Q":
		return economics.BasisProviderQuantityLocal
	case "E":
		return economics.BasisLocalExpected
	default:
		t.Fatalf("unknown basis %q", name)
		return ""
	}
}

func r2NormalizeFields(t *testing.T, mappingID string, fields map[string]string) normalize.Result {
	t.Helper()
	mapping := r2Mapping(t, mappingID)
	inputs := make([]normalize.Field, 0, len(fields))
	for name, lexeme := range fields {
		inputs = append(inputs, normalize.Field{Name: name, Lexeme: lexeme, Present: true})
	}
	result, err := normalize.Normalize(normalize.Input{Mapping: mapping, Fields: inputs})
	require.NoError(t, err)
	require.Equal(t, normalize.StatusComplete, result.Status, "vector evidence must be complete: %v", result.Diagnostics)
	return result
}

func TestPhase172Cluster3EvidenceDrivenSemantics(t *testing.T) {
	fixture := loadR2Fixture(t)
	require.Len(t, fixture.Vectors, 7)
	seen := map[string]bool{}
	for i, vector := range fixture.Vectors {
		store := newSQLiteTestStore(t)
		journal := openF1Journal(t)
		ctx := context.Background()
		revision := uint64(51 + i)

		rater, err := billing.NewReferenceRater(r2Tariff(t, vector.Tariff, fixture.RateSchedule))
		require.NoError(t, err)
		snapshot := rater.Snapshot()
		capture, err := NewShadowV2Capture(
			ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
			journal, store, store, store, rater,
		)
		require.NoError(t, err)

		var actualV2 int64
		if vector.Mapping == "direct" {
			actualV2 = r2RateDirectPlanes(t, ctx, store, capture, vector, revision, snapshot, fixture.RateSchedule)
		} else {
			actualV2 = r2RateNormalized(t, ctx, store, capture, vector, revision, snapshot)
		}
		expectedV2 := r2NanoSum(t, fixture.RateSchedule, vector.ExpectedLines)
		v1 := r2NanoSum(t, fixture.RateSchedule, vector.V1Lines)
		result, err := CompareShadowV2Semantics(ShadowV2ComparisonInput{
			Label:          vector.Label,
			Currency:       vector.Currency,
			V1Nano:         v1,
			V2Nano:         actualV2,
			ExpectedV2Nano: expectedV2,
			ReasonCode:     vector.ReasonCode,
		})
		require.NoError(t, err, "vector %s", vector.Label)
		require.Equal(t, vector.ExpectVerdict, string(result.Verdict), "vector %s detail=%s", vector.Label, result.Detail)
		seen[string(result.Verdict)] = true
	}
	require.True(t, seen["expected_fix"] && seen["match"] && seen["unexpected_drift"])
}

// r2RateNormalized runs raw provider fields through the production inclusion
// normalizer, rates the attached evidence with the frozen production rater,
// persists through the shared shadow normalization, and reads back the actual
// total. Every amount after this point is production output.
//
//nolint:revive // test helper keeps t first per Go testing convention
func r2RateNormalized(t *testing.T, ctx context.Context, store *DurableStore, capture *ShadowV2Capture, vector r2Vector, revision uint64, snapshot economics.TariffSnapshot) int64 {
	t.Helper()
	result := r2NormalizeFields(t, vector.Mapping, vector.Fields)
	observation := r2Observation(t, "r2-"+vector.Label, revision, metering.OriginProvider, metering.AcquisitionProviderResponse, result)
	work := r2Work(t, observation, r2Basis(t, vector.Basis), "r2-head-"+vector.Label, revision, snapshot)
	require.NoError(t, capture.AppendWork(ctx, work))
	require.NoError(t, capture.RateAndPersist(ctx, work))
	got := r2PersistedTotalNano(t, ctx, store, work)
	if vector.Label == "reasoning-once-fix" {
		r2RequireExplicitFreeReasoning(t, ctx, store, work)
	}
	return got
}

// r2RateDirectPlanes rates local-expected and provider-quantity planes
// independently through the production rater, persists both, and returns the
// expected-plane total. The provider plane proves E/Q independence instead of
// overwriting E.
//
//nolint:revive // test helper keeps t first per Go testing convention
func r2RateDirectPlanes(t *testing.T, ctx context.Context, store *DurableStore, capture *ShadowV2Capture, vector r2Vector, revision uint64, snapshot economics.TariffSnapshot, rates map[string]int64) int64 {
	t.Helper()
	localTokens, ok := vector.Fields["local_tokens"]
	require.True(t, ok, "eqp vector must carry local_tokens evidence")
	providerTokens, ok := vector.Fields["provider_tokens"]
	require.True(t, ok, "eqp vector must carry provider_tokens evidence")
	localObs := r2DirectObservation(t, "r2-"+vector.Label+"-e", revision, metering.OriginLocal, metering.AcquisitionLocalTransport, localTokens)
	providerObs := r2DirectObservation(t, "r2-"+vector.Label+"-q", revision, metering.OriginProvider, metering.AcquisitionProviderResponse, providerTokens)
	expectedWork := r2Work(t, localObs, economics.BasisLocalExpected, "r2-head-"+vector.Label+"-e", revision, snapshot)
	quantityWork := r2Work(t, providerObs, economics.BasisProviderQuantityLocal, "r2-head-"+vector.Label+"-q", revision, snapshot)
	require.NoError(t, capture.AppendWork(ctx, expectedWork))
	require.NoError(t, capture.RateAndPersist(ctx, expectedWork))
	require.NoError(t, capture.AppendWork(ctx, quantityWork))
	require.NoError(t, capture.RateAndPersist(ctx, quantityWork))
	quantityTotal := r2PersistedTotalNano(t, ctx, store, quantityWork)
	require.Equal(t, r2NanoSum(t, rates, []r2LineSpec{{Tokens: providerTokens, RateKey: "input_token"}}), quantityTotal)
	quantityIdentity, err := quantityWork.Identity()
	require.NoError(t, err)
	quantityValuation, err := store.GetValuation(ctx, quantityIdentity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Len(t, quantityValuation.InputObservations, 1)
	expectedIdentity, err := expectedWork.Identity()
	require.NoError(t, err)
	expectedValuation, err := store.GetValuation(ctx, expectedIdentity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Len(t, expectedValuation.InputObservations, 1)
	require.NotEqual(t,
		expectedValuation.InputObservations[0].ObservationID,
		quantityValuation.InputObservations[0].ObservationID,
		"E and Q must retain disjoint source evidence")
	return r2PersistedTotalNano(t, ctx, store, expectedWork)
}

func r2DirectObservation(t *testing.T, obsID string, revision uint64, origin, acquisition, tokens string) metering.Observation {
	t.Helper()
	qty, err := strconv.ParseInt(tokens, 10, 64)
	require.NoError(t, err)
	value := metering.Decimal{Coefficient: strconv.FormatInt(qty, 10), Scale: 0}
	now := time.Unix(1_700_195_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: obsID, SourceEventKey: obsID, Revision: revision,
		StreamID: "r2-stream", Sequence: revision,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant-r2",
			AccountID: "r2-acct", ALegID: "a-r2", BillingCallID: "call-r2",
			BLegID: "b-r2", AttemptID: "attempt-r2", AttemptSeq: revision,
		},
		Correlation: metering.CorrelationV2{
			StoreID: "test", TenantID: "tenant-r2", CallID: "call-r2",
			BillingCallID: "call-r2", ALegID: "a-r2", BLegID: "b-r2",
			AttemptID: "attempt-r2", AttemptSeq: revision,
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now,
		MappingRef: "r2.direct@v1",
		Measures: []metering.Measure{{
			Key:     metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID},
			Value:   &value,
			Quality: metering.QualityObserved,
		}},
	}
}

// r2RequireExplicitFreeReasoning proves the included-reasoning measure is
// retained as an explicit-free line rather than billed or silently dropped.
//
//nolint:revive // test helper keeps t first per Go testing convention
func r2RequireExplicitFreeReasoning(t *testing.T, ctx context.Context, store *DurableStore, work billing.EconomicRevisionWork) {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	found := false
	for _, line := range valuation.Lines {
		if line.Component != nil && line.Component.Component == metering.ComponentReasoningOutputToken {
			found = true
			require.Equal(t, economics.RatingLineExplicitFree, line.Status)
		}
	}
	require.True(t, found, "included reasoning must persist as an explicit-free line")
}

// TestPhase172R2ProductionSensitivity proves the certified verdict tracks
// production behavior: controlled lower-seam changes (mapping mode, tariff)
// move the actual rated total while the frozen expectation stands still.
func TestPhase172R2ProductionSensitivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fields := map[string]string{"input": "300", "output": "200", "reasoning": "50"}
	rates := map[string]int64{"output": 2_000_000, "reasoning_t1": 0, "reasoning_t2": 4_000_000, "input_uncached": 1_000_000, "cache_read": 500_000, "cache_write": 500_000, "input_token": 1_000_000}

	included := r2NormalizeFields(t, "reasoning-included", fields)
	disjointResult, err := normalize.Normalize(normalize.Input{Mapping: r2DisjointMapping(t), Fields: r2Fields(t, fields)})
	require.NoError(t, err)
	require.Equal(t, normalize.StatusComplete, disjointResult.Status)

	includedTotal := r2RateResultForSensitivity(t, ctx, included, r2Tariff(t, "t1", rates), economics.BasisProviderQuantityLocal)
	disjointTotal := r2RateResultForSensitivity(t, ctx, disjointResult, r2Tariff(t, "t1", rates), economics.BasisProviderQuantityLocal)
	require.Equal(t, int64(700_000_000), includedTotal)
	require.Equal(t, int64(600_000_000), disjointTotal)

	regressedTotal := r2RateResultForSensitivity(t, ctx, included, r2Tariff(t, "t2", rates), economics.BasisProviderQuantityLocal)
	require.Equal(t, int64(900_000_000), regressedTotal)
}

func r2DisjointMapping(t *testing.T) normalize.Mapping {
	t.Helper()
	return r2Mapping(t, "reasoning-disjoint")
}

func r2Fields(t *testing.T, fields map[string]string) []normalize.Field {
	t.Helper()
	inputs := make([]normalize.Field, 0, len(fields))
	for name, lexeme := range fields {
		inputs = append(inputs, normalize.Field{Name: name, Lexeme: lexeme, Present: true})
	}
	return inputs
}

//nolint:revive // test helper keeps t first per Go testing convention
func r2RateResultForSensitivity(t *testing.T, ctx context.Context, result normalize.Result, snapshot economics.TariffSnapshot, basis economics.ValuationBasis) int64 {
	t.Helper()
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	observation := r2Observation(t, "r2-sensitivity", 61, metering.OriginProvider, metering.AcquisitionProviderResponse, result)
	work := r2Work(t, observation, basis, "r2-sensitivity-head", 61, snapshot)
	rater, err := billing.NewReferenceRater(snapshot)
	require.NoError(t, err)
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)
	require.NoError(t, capture.AppendWork(ctx, work))
	require.NoError(t, capture.RateAndPersist(ctx, work))
	return r2PersistedTotalNano(t, ctx, store, work)
}
