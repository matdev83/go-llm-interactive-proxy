package billing

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// reconciliationSubject returns one B-leg subject shared by the comparison
// fixtures. Full economic identity includes the subject, so fixtures that change
// only one field remain visibly distinguishable.
func reconciliationSubject() metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "store-reconciliation",
		ALegID: "a-reconciliation", BillingCallID: "call-reconciliation", BLegID: "b-reconciliation",
	}
}

func reconciliationDecimal(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	return &value
}

func reconciliationKey(direction metering.FlowDirection, component, unit, schema string, dimensions ...metering.Dimension) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: unit, SchemaID: schema, Dimensions: dimensions}
}

func reconciliationMeasure(t *testing.T, key metering.ComponentKey, quality, method, raw string) metering.Measure {
	t.Helper()
	measure := metering.Measure{Key: key, Quality: quality, MethodRef: method}
	if raw != "" {
		measure.Value = reconciliationDecimal(t, raw)
	}
	return measure
}

func reconciliationObservationWithSemantics(t *testing.T, id string, origin, semantics string, measures ...metering.Measure) metering.Observation {
	t.Helper()
	subject := reconciliationSubject()
	acquisition := metering.AcquisitionLocalTokenizer
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	now := time.Unix(1_700_000_700, 0).UTC()
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, ALegID: subject.ALegID,
			BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
		},
		Semantics:  semantics,
		ObservedAt: now, ReceivedAt: now, MappingRef: "reconciliation.compare.test.v1",
		Measures: measures,
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation %q invalid: %v", id, err)
	}
	return observation
}

func reconciliationObservation(t *testing.T, id string, origin string, measures ...metering.Measure) metering.Observation {
	t.Helper()
	return reconciliationObservationWithSemantics(t, id, origin, metering.SemanticsDelta, measures...)
}

const reconciliationDefaultTokenizer = "reconciliation-tokenizer-v1"

func reconciliationSide(observations ...metering.Observation) ReconciliationEvidenceSet {
	return ReconciliationEvidenceSet{Subject: reconciliationSubject(), Tokenizer: reconciliationDefaultTokenizer, Observations: observations}
}

func reconciliationItem(t *testing.T, result ComponentQuantityComparison, direction metering.FlowDirection, component string) ComponentQuantityComparisonItem {
	t.Helper()
	for _, item := range result.Items {
		if item.Key.Direction == direction && item.Key.Component == component {
			return item
		}
	}
	t.Fatalf("comparison has no %s/%s item: %+v", direction, component, result.Items)
	return ComponentQuantityComparisonItem{}
}

func assertReconciliationDelta(t *testing.T, item ComponentQuantityComparisonItem, signed, absolute string) {
	t.Helper()
	if item.SignedDelta == nil || item.AbsoluteDelta == nil {
		t.Fatalf("item %s/%s lacks delta: %+v", item.Key.Direction, item.Key.Component, item)
	}
	if got := item.SignedDelta.CanonicalString(); got != signed {
		t.Fatalf("signed delta = %s, want %s", got, signed)
	}
	if got := item.AbsoluteDelta.CanonicalString(); got != absolute {
		t.Fatalf("absolute delta = %s, want %s", got, absolute)
	}
}

func assertReconciliationNoDelta(t *testing.T, item ComponentQuantityComparisonItem) {
	t.Helper()
	if item.SignedDelta != nil || item.AbsoluteDelta != nil {
		t.Fatalf("item %s/%s carries delta %v/%v without comparable evidence", item.Key.Direction, item.Key.Component, item.SignedDelta, item.AbsoluteDelta)
	}
	if item.Status == ReconciliationStatusMatched || item.Status == ReconciliationStatusDiscrepant {
		t.Fatalf("item %s/%s status %q implies a comparison", item.Key.Direction, item.Key.Component, item.Status)
	}
}

// TestReconciliationCompareJoinsExactEconomicIdentity proves that quantities
// only pair when the full economic subject, payer, currency, effective
// measurement context and component key agree. A local measure must never be
// silently compared against an unrelated provider identity.
func TestReconciliationCompareJoinsExactEconomicIdentity(t *testing.T) {
	t.Parallel()

	inputKey := reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	local := reconciliationSide(reconciliationObservation(t, "obs-local-input", metering.OriginLocal,
		reconciliationMeasure(t, inputKey, metering.QualityObserved, "tokenizer-a", "100")))
	provider := reconciliationSide(reconciliationObservation(t, "obs-provider-input", metering.OriginProvider,
		reconciliationMeasure(t, inputKey, metering.QualityObserved, "tokenizer-a", "110")))

	result, err := CompareComponentQuantities(local, provider)
	if err != nil {
		t.Fatalf("CompareComponentQuantities: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %+v, want one joined component", result.Items)
	}
	item := result.Items[0]
	if item.Status != ReconciliationStatusDiscrepant {
		t.Fatalf("status = %q, want discrepant", item.Status)
	}
	assertReconciliationDelta(t, item, "10/0", "10/0")
	if !item.Key.Equal(inputKey) {
		t.Fatalf("item key = %+v, want full component key", item.Key)
	}

	t.Run("payer mismatch is incomparable", func(t *testing.T) {
		paid := provider
		paid.Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator}
		billed := local
		billed.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "cust-1"}
		mismatched, err := CompareComponentQuantities(billed, paid)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if mismatched.Status != ReconciliationStatusIncomparable {
			t.Fatalf("status = %q, want incomparable", mismatched.Status)
		}
		if len(mismatched.Items) != 1 || mismatched.Items[0].Reason != ReconciliationReasonPayerMismatch {
			t.Fatalf("items = %+v, want payer_mismatch", mismatched.Items)
		}
		assertReconciliationNoDelta(t, mismatched.Items[0])
	})

	t.Run("effective measurement context mismatch is incomparable", func(t *testing.T) {
		eu := local
		eu.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "eu"}}
		us := provider
		us.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "us"}}
		mismatched, err := CompareComponentQuantities(eu, us)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(mismatched.Items) != 1 || mismatched.Items[0].Reason != ReconciliationReasonContextMismatch {
			t.Fatalf("items = %+v, want measurement_context_mismatch", mismatched.Items)
		}
		assertReconciliationNoDelta(t, mismatched.Items[0])
	})

	t.Run("component qualifier mismatch is incomparable", func(t *testing.T) {
		png := reconciliationKey(metering.DirectionInput, "media_upload", metering.UnitImage, "reconciliation.media.v1", metering.Dimension{Name: "format", Value: "png"})
		jpeg := reconciliationKey(metering.DirectionInput, "media_upload", metering.UnitImage, "reconciliation.media.v1", metering.Dimension{Name: "format", Value: "jpeg"})
		mismatched, err := CompareComponentQuantities(
			reconciliationSide(reconciliationObservation(t, "obs-local-png", metering.OriginLocal, reconciliationMeasure(t, png, metering.QualityObserved, "", "2"))),
			reconciliationSide(reconciliationObservation(t, "obs-provider-jpeg", metering.OriginProvider, reconciliationMeasure(t, jpeg, metering.QualityObserved, "", "3"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(mismatched.Items) != 1 || mismatched.Items[0].Status != ReconciliationStatusIncomparable {
			t.Fatalf("items = %+v, want one incomparable item", mismatched.Items)
		}
		if mismatched.Items[0].Reason != ReconciliationReasonQualifierMismatch {
			t.Fatalf("reason = %q, want qualifier_mismatch", mismatched.Items[0].Reason)
		}
		assertReconciliationNoDelta(t, mismatched.Items[0])
	})
}

// TestReconciliationCompareRequiresTokenizerIdentity proves a missing or
// unmapped tokenizer declaration is never compatible for token-measured
// components, while native non-token units remain comparable.
func TestReconciliationCompareRequiresTokenizerIdentity(t *testing.T) {
	t.Parallel()

	inputKey := reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	tokenMeasure := func(t *testing.T, id, origin, value string) metering.Observation {
		t.Helper()
		return reconciliationObservation(t, id, origin, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", value))
	}

	t.Run("local undeclared provider declared", func(t *testing.T) {
		local := reconciliationSide(tokenMeasure(t, "tok-local", metering.OriginLocal, "100"))
		local.Tokenizer = ""
		provider := reconciliationSide(tokenMeasure(t, "tok-provider", metering.OriginProvider, "110"))
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusIncomparable || result.Reason != ReconciliationReasonTokenizerMissing {
			t.Fatalf("result = %+v, want incomparable tokenizer_missing", result)
		}
		for _, item := range result.Items {
			assertReconciliationNoDelta(t, item)
		}
	})

	t.Run("provider undeclared local declared", func(t *testing.T) {
		local := reconciliationSide(tokenMeasure(t, "tok-local", metering.OriginLocal, "100"))
		provider := reconciliationSide(tokenMeasure(t, "tok-provider", metering.OriginProvider, "110"))
		provider.Tokenizer = ""
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusIncomparable || result.Reason != ReconciliationReasonTokenizerMissing {
			t.Fatalf("result = %+v, want incomparable tokenizer_missing", result)
		}
	})

	t.Run("both undeclared token measured is incomparable", func(t *testing.T) {
		local := reconciliationSide(tokenMeasure(t, "tok-local", metering.OriginLocal, "100"))
		local.Tokenizer = ""
		provider := reconciliationSide(tokenMeasure(t, "tok-provider", metering.OriginProvider, "110"))
		provider.Tokenizer = ""
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(result.Items) != 1 {
			t.Fatalf("items = %+v, want one item", result.Items)
		}
		item := result.Items[0]
		if item.Status != ReconciliationStatusIncomparable || item.Reason != ReconciliationReasonTokenizerRequired {
			t.Fatalf("item = %+v, want incomparable tokenizer_required", item)
		}
		if result.Status != ReconciliationStatusIncomparable {
			t.Fatalf("result status = %q, want incomparable", result.Status)
		}
		assertReconciliationNoDelta(t, item)
	})

	t.Run("different declarations have no mapping", func(t *testing.T) {
		local := reconciliationSide(tokenMeasure(t, "tok-local", metering.OriginLocal, "100"))
		local.Tokenizer = "tokenizer-v1"
		provider := reconciliationSide(tokenMeasure(t, "tok-provider", metering.OriginProvider, "100"))
		provider.Tokenizer = "tokenizer-v2"
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusIncomparable || result.Reason != ReconciliationReasonTokenizerMismatch {
			t.Fatalf("result = %+v, want incomparable tokenizer_mismatch", result)
		}
		for _, item := range result.Items {
			assertReconciliationNoDelta(t, item)
		}
	})

	t.Run("matching declarations compare", func(t *testing.T) {
		local := reconciliationSide(tokenMeasure(t, "tok-local", metering.OriginLocal, "100"))
		provider := reconciliationSide(tokenMeasure(t, "tok-provider", metering.OriginProvider, "110"))
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusDiscrepant {
			t.Fatalf("result = %+v, want discrepant with matched declarations", result)
		}
	})

	t.Run("native non-token unit compares without declaration", func(t *testing.T) {
		seconds := reconciliationKey(metering.DirectionInput, "audio", metering.UnitSecond, "reconciliation.media.v1")
		local := reconciliationSide(reconciliationObservation(t, "sec-local", metering.OriginLocal, reconciliationMeasure(t, seconds, metering.QualityObserved, "opaque", "1.5")))
		local.Tokenizer = ""
		provider := reconciliationSide(reconciliationObservation(t, "sec-provider", metering.OriginProvider, reconciliationMeasure(t, seconds, metering.QualityObserved, "opaque", "2.5")))
		provider.Tokenizer = ""
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusDiscrepant {
			t.Fatalf("result = %+v, want discrepant native unit comparison", result)
		}
		if len(result.Items) != 1 {
			t.Fatalf("items = %+v, want one item", result.Items)
		}
		assertReconciliationDelta(t, result.Items[0], "1/0", "1/0")
	})

	t.Run("one sided declaration is incompatible even for native units", func(t *testing.T) {
		seconds := reconciliationKey(metering.DirectionInput, "audio", metering.UnitSecond, "reconciliation.media.v1")
		local := reconciliationSide(reconciliationObservation(t, "sec-local", metering.OriginLocal, reconciliationMeasure(t, seconds, metering.QualityObserved, "opaque", "1.5")))
		local.Tokenizer = ""
		provider := reconciliationSide(reconciliationObservation(t, "sec-provider", metering.OriginProvider, reconciliationMeasure(t, seconds, metering.QualityObserved, "opaque", "2.5")))
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusIncomparable || result.Reason != ReconciliationReasonTokenizerMissing {
			t.Fatalf("result = %+v, want incomparable tokenizer_missing", result)
		}
	})
}

// TestReconciliationCompareSignedAndAbsoluteDeltaIsExact checks the C4 quantity
// delta formula provider - local with the existing bounded decimal type. No
// float conversion may lose precision for fractional native units.
func TestReconciliationCompareSignedAndAbsoluteDeltaIsExact(t *testing.T) {
	t.Parallel()

	tokenKey := reconciliationKey(metering.DirectionOutput, metering.ComponentOutputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)

	t.Run("provider above local is positive", func(t *testing.T) {
		result, err := CompareComponentQuantities(
			reconciliationSide(reconciliationObservation(t, "obs-local-pos", metering.OriginLocal, reconciliationMeasure(t, tokenKey, metering.QualityObserved, "tok", "110"))),
			reconciliationSide(reconciliationObservation(t, "obs-provider-pos", metering.OriginProvider, reconciliationMeasure(t, tokenKey, metering.QualityObserved, "tok", "120"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		assertReconciliationDelta(t, result.Items[0], "10/0", "10/0")
	})

	t.Run("provider below local is negative signed positive absolute", func(t *testing.T) {
		result, err := CompareComponentQuantities(
			reconciliationSide(reconciliationObservation(t, "obs-local-neg", metering.OriginLocal, reconciliationMeasure(t, tokenKey, metering.QualityObserved, "tok", "120"))),
			reconciliationSide(reconciliationObservation(t, "obs-provider-neg", metering.OriginProvider, reconciliationMeasure(t, tokenKey, metering.QualityObserved, "tok", "110"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		assertReconciliationDelta(t, result.Items[0], "-10/0", "10/0")
	})

	t.Run("equal evidence is matched with exact zero", func(t *testing.T) {
		result, err := CompareComponentQuantities(
			reconciliationSide(reconciliationObservation(t, "obs-local-eq", metering.OriginLocal, reconciliationMeasure(t, tokenKey, metering.QualityObserved, "tok", "100"))),
			reconciliationSide(reconciliationObservation(t, "obs-provider-eq", metering.OriginProvider, reconciliationMeasure(t, tokenKey, metering.QualityObserved, "tok", "100"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		item := result.Items[0]
		if item.Status != ReconciliationStatusMatched {
			t.Fatalf("status = %q, want matched", item.Status)
		}
		assertReconciliationDelta(t, item, "0/0", "0/0")
	})

	t.Run("fractional native unit keeps exact scale", func(t *testing.T) {
		seconds := reconciliationKey(metering.DirectionInput, "audio", metering.UnitSecond, "reconciliation.media.v1")
		result, err := CompareComponentQuantities(
			reconciliationSide(reconciliationObservation(t, "obs-local-sec", metering.OriginLocal, reconciliationMeasure(t, seconds, metering.QualityEstimated, "", "1.5"))),
			reconciliationSide(reconciliationObservation(t, "obs-provider-sec", metering.OriginProvider, reconciliationMeasure(t, seconds, metering.QualityObserved, "", "2.25"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		signed := result.Items[0].SignedDelta
		if signed == nil {
			t.Fatalf("item lacks signed delta: %+v", result.Items[0])
		}
		const want = "75/2" // 2.25 - 1.5 = 0.75 exactly
		if got := signed.CanonicalString(); got != want {
			t.Fatalf("signed delta = %s, want %s", got, want)
		}
	})
}

// TestReconciliationCompareIncompatibleEvidenceIsTyped proves that schema,
// tokenizer, partition period, coverage and currency incompatibilities are
// reported as typed partial/incomparable outcomes instead of matched or zero.
func TestReconciliationCompareIncompatibleEvidenceIsTyped(t *testing.T) {
	t.Parallel()

	baseKey := reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, "reconciliation.schema.a")
	localObservation := func() metering.Observation {
		return reconciliationObservation(t, "obs-local-incompat", metering.OriginLocal,
			reconciliationMeasure(t, baseKey, metering.QualityObserved, "tokenizer-a", "100"))
	}
	providerObservation := func() metering.Observation {
		return reconciliationObservation(t, "obs-provider-incompat", metering.OriginProvider,
			reconciliationMeasure(t, baseKey, metering.QualityObserved, "tokenizer-a", "100"))
	}

	t.Run("inclusion partition schema mismatch", func(t *testing.T) {
		other := reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, "reconciliation.schema.b")
		result, err := CompareComponentQuantities(
			reconciliationSide(localObservation()),
			reconciliationSide(reconciliationObservation(t, "obs-provider-schema", metering.OriginProvider,
				reconciliationMeasure(t, other, metering.QualityObserved, "tokenizer-a", "100"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		item := result.Items[0]
		if item.Status != ReconciliationStatusIncomparable || item.Reason != ReconciliationReasonSchemaMismatch {
			t.Fatalf("item = %+v, want incomparable schema_mismatch", item)
		}
		assertReconciliationNoDelta(t, item)
	})

	t.Run("tokenizer mismatch", func(t *testing.T) {
		local := reconciliationSide(localObservation())
		local.Tokenizer = "tokenizer-semantics-a"
		provider := reconciliationSide(reconciliationObservation(t, "obs-provider-tokenizer", metering.OriginProvider,
			reconciliationMeasure(t, baseKey, metering.QualityObserved, "tokenizer-b", "100")))
		provider.Tokenizer = "tokenizer-semantics-b"
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		item := result.Items[0]
		if item.Status != ReconciliationStatusIncomparable || item.Reason != ReconciliationReasonTokenizerMismatch {
			t.Fatalf("item = %+v, want incomparable tokenizer_mismatch", item)
		}
		assertReconciliationNoDelta(t, item)
	})

	t.Run("aggregation semantics mismatch", func(t *testing.T) {
		result, err := CompareComponentQuantities(
			reconciliationSide(localObservation()),
			reconciliationSide(reconciliationObservationWithSemantics(t, "obs-provider-cumulative", metering.OriginProvider, metering.SemanticsCumulative,
				reconciliationMeasure(t, baseKey, metering.QualityObserved, "tokenizer-a", "100"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		item := result.Items[0]
		if item.Status != ReconciliationStatusIncomparable || item.Reason != ReconciliationReasonSemanticsMismatch {
			t.Fatalf("item = %+v, want incomparable semantics_mismatch", item)
		}
		assertReconciliationNoDelta(t, item)
	})

	t.Run("period mismatch", func(t *testing.T) {
		local := reconciliationSide(providerObservation())
		local.PeriodID = "period-2026-01"
		provider := reconciliationSide(providerObservation())
		provider.PeriodID = "period-2026-02"
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusIncomparable || result.Reason != ReconciliationReasonPeriodMismatch {
			t.Fatalf("result = %+v, want incomparable period_mismatch", result)
		}
		assertReconciliationNoDelta(t, result.Items[0])
	})

	t.Run("coverage mismatch", func(t *testing.T) {
		local := reconciliationSide(localObservation())
		local.Coverage = []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: reconciliationSubject().StoreID, ObservationID: "obs-charge", Revision: 1, ChargeItemID: "item-a"}, Relation: metering.CoverageAdditive}}
		provider := reconciliationSide(providerObservation())
		provider.Coverage = []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: reconciliationSubject().StoreID, ObservationID: "obs-charge", Revision: 1, ChargeItemID: "item-b"}, Relation: metering.CoverageAdditive}}
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusIncomparable || result.Reason != ReconciliationReasonCoverageMismatch {
			t.Fatalf("result = %+v, want incomparable coverage_mismatch", result)
		}
		assertReconciliationNoDelta(t, result.Items[0])
	})

	t.Run("currency mismatch", func(t *testing.T) {
		local := reconciliationSide(localObservation())
		local.Currency = "USD"
		provider := reconciliationSide(providerObservation())
		provider.Currency = "EUR"
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Status != ReconciliationStatusIncomparable || result.Reason != ReconciliationReasonCurrencyMismatch {
			t.Fatalf("result = %+v, want incomparable currency_mismatch", result)
		}
	})
}

// TestReconciliationCompareDistinguishesMissingAndAttemptedUnknown keeps
// missing evidence statuses distinct. An attempted but unavailable partition is
// partial, never a zero, and a truly absent partition is a missing side.
func TestReconciliationCompareDistinguishesMissingAndAttemptedUnknown(t *testing.T) {
	t.Parallel()

	inputKey := reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	cacheReadKey := reconciliationKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	cacheWriteKey := reconciliationKey(metering.DirectionInput, metering.ComponentCacheWriteInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)

	local := reconciliationSide(reconciliationObservation(t, "obs-local-partial", metering.OriginLocal,
		reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100"),
		reconciliationMeasure(t, cacheReadKey, metering.QualityUnavailable, "", ""),
	))
	provider := reconciliationSide(reconciliationObservation(t, "obs-provider-partial", metering.OriginProvider,
		reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "110"),
		reconciliationMeasure(t, cacheReadKey, metering.QualityObserved, "tok", "40"),
		reconciliationMeasure(t, cacheWriteKey, metering.QualityObserved, "tok", "5"),
	))

	result, err := CompareComponentQuantities(local, provider)
	if err != nil {
		t.Fatalf("CompareComponentQuantities: %v", err)
	}
	if result.Complete {
		t.Fatalf("comparison with missing/attempted evidence reported complete: %+v", result)
	}

	inputItem := reconciliationItem(t, result, metering.DirectionInput, metering.ComponentInputToken)
	if inputItem.Status != ReconciliationStatusDiscrepant {
		t.Fatalf("input item = %+v, want discrepant", inputItem)
	}
	assertReconciliationDelta(t, inputItem, "10/0", "10/0")

	cacheReadItem := reconciliationItem(t, result, metering.DirectionInput, metering.ComponentCacheReadInputToken)
	if cacheReadItem.Status != ReconciliationStatusPartial || cacheReadItem.Reason != ReconciliationReasonValueUnavailableLocal {
		t.Fatalf("cache read item = %+v, want partial value_unavailable_local", cacheReadItem)
	}
	assertReconciliationNoDelta(t, cacheReadItem)

	cacheWriteItem := reconciliationItem(t, result, metering.DirectionInput, metering.ComponentCacheWriteInputToken)
	if cacheWriteItem.Status != ReconciliationStatusMissingLocal {
		t.Fatalf("cache write item = %+v, want missing_local", cacheWriteItem)
	}
	assertReconciliationNoDelta(t, cacheWriteItem)

	t.Run("provider-only evidence is missing_local", func(t *testing.T) {
		only := reconciliationSide(reconciliationObservation(t, "obs-provider-only", metering.OriginProvider,
			reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100")))
		absent := reconciliationSide(reconciliationObservation(t, "obs-local-other", metering.OriginLocal,
			reconciliationMeasure(t, cacheReadKey, metering.QualityObserved, "tok", "7")))
		mixed, err := CompareComponentQuantities(absent, only)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		missingProvider := reconciliationItem(t, mixed, metering.DirectionInput, metering.ComponentCacheReadInputToken)
		if missingProvider.Status != ReconciliationStatusMissingProvider {
			t.Fatalf("local-only item = %+v, want missing_provider", missingProvider)
		}
		missingLocal := reconciliationItem(t, mixed, metering.DirectionInput, metering.ComponentInputToken)
		if missingLocal.Status != ReconciliationStatusMissingLocal {
			t.Fatalf("provider-only item = %+v, want missing_local", missingLocal)
		}
	})

	t.Run("explicit observed zero is not missing", func(t *testing.T) {
		zeroLocal := reconciliationSide(reconciliationObservation(t, "obs-local-zero", metering.OriginLocal,
			reconciliationMeasure(t, cacheReadKey, metering.QualityObserved, "tok", "0")))
		zeroProvider := reconciliationSide(reconciliationObservation(t, "obs-provider-zero", metering.OriginProvider,
			reconciliationMeasure(t, cacheReadKey, metering.QualityObserved, "tok", "0")))
		zeroResult, err := CompareComponentQuantities(zeroLocal, zeroProvider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(zeroResult.Items) != 1 {
			t.Fatalf("items = %+v, want exactly one observed zero", zeroResult.Items)
		}
		if zeroResult.Items[0].Status != ReconciliationStatusMatched {
			t.Fatalf("zero item = %+v, want matched zero", zeroResult.Items[0])
		}
		assertReconciliationDelta(t, zeroResult.Items[0], "0/0", "0/0")
	})
}

// TestReconciliationCompareRetainsQualityAndSourceReferences proves that
// independent estimates keep their quality label, measurement method and
// immutable observation references on both sides of a discrepancy.
func TestReconciliationCompareRetainsQualityAndSourceReferences(t *testing.T) {
	t.Parallel()

	outputKey := reconciliationKey(metering.DirectionOutput, metering.ComponentOutputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	localObservation := reconciliationObservation(t, "obs-local-estimate", metering.OriginLocal,
		reconciliationMeasure(t, outputKey, metering.QualityEstimated, "local-estimator-v1", "95"))
	providerObservation := reconciliationObservation(t, "obs-provider-report", metering.OriginProvider,
		reconciliationMeasure(t, outputKey, metering.QualityObserved, "provider-tokenizer-v2", "100"))

	result, err := CompareComponentQuantities(reconciliationSide(localObservation), reconciliationSide(providerObservation))
	if err != nil {
		t.Fatalf("CompareComponentQuantities: %v", err)
	}
	item := result.Items[0]
	if item.Status != ReconciliationStatusDiscrepant {
		t.Fatalf("item = %+v, want discrepant", item)
	}
	if len(item.Local) != 1 || len(item.Provider) != 1 {
		t.Fatalf("item evidence = %d local / %d provider, want one each", len(item.Local), len(item.Provider))
	}
	localEvidence, providerEvidence := item.Local[0], item.Provider[0]
	if localEvidence.Quality != metering.QualityEstimated || localEvidence.MethodRef != "local-estimator-v1" {
		t.Fatalf("local evidence lost quality/method: %+v", localEvidence)
	}
	if providerEvidence.Quality != metering.QualityObserved || providerEvidence.MethodRef != "provider-tokenizer-v2" {
		t.Fatalf("provider evidence lost quality/method: %+v", providerEvidence)
	}
	localRef, err := localObservation.Ref(reconciliationSubject().StoreID)
	if err != nil {
		t.Fatalf("local observation ref: %v", err)
	}
	if localEvidence.Observation != localRef {
		t.Fatalf("local source ref = %+v, want %+v", localEvidence.Observation, localRef)
	}
	providerRef, err := providerObservation.Ref(reconciliationSubject().StoreID)
	if err != nil {
		t.Fatalf("provider observation ref: %v", err)
	}
	if providerEvidence.Observation != providerRef {
		t.Fatalf("provider source ref = %+v, want %+v", providerEvidence.Observation, providerRef)
	}
}

// TestReconciliationCompareIsDeterministicAndFailsClosed covers deterministic
// ordering, duplicate/conflicting input rejection, input/result cardinality
// bounds and empty/cross-store rejection.
func TestReconciliationCompareIsDeterministicAndFailsClosed(t *testing.T) {
	t.Parallel()

	keyFor := func(name string) metering.ComponentKey {
		return reconciliationKey(metering.DirectionNone, name, metering.UnitCount, "reconciliation.bound.v1")
	}
	observationFor := func(id, origin, component string, value string) metering.Observation {
		return reconciliationObservation(t, id, origin, reconciliationMeasure(t, keyFor(component), metering.QualityObserved, "method", value))
	}

	t.Run("observation order does not change results", func(t *testing.T) {
		first := reconciliationSide(
			observationFor("obs-z", metering.OriginLocal, "z_metric", "1"),
			observationFor("obs-a", metering.OriginLocal, "a_metric", "2"),
		)
		second := reconciliationSide(
			observationFor("obs-a", metering.OriginLocal, "a_metric", "2"),
			observationFor("obs-z", metering.OriginLocal, "z_metric", "1"),
		)
		provider := reconciliationSide(
			observationFor("obs-p1", metering.OriginProvider, "z_metric", "4"),
			observationFor("obs-p2", metering.OriginProvider, "a_metric", "3"),
		)
		left, err := CompareComponentQuantities(first, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities(first): %v", err)
		}
		right, err := CompareComponentQuantities(second, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities(second): %v", err)
		}
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("comparison depends on observation order:\nleft=%+v\nright=%+v", left, right)
		}
		if left.Items[0].Key.Component != "a_metric" || left.Items[1].Key.Component != "z_metric" {
			t.Fatalf("items are not deterministically ordered: %+v", left.Items)
		}
	})

	t.Run("identical duplicate evidence conflicts", func(t *testing.T) {
		local := reconciliationSide(
			observationFor("obs-dup-1", metering.OriginLocal, "dup_metric", "5"),
			observationFor("obs-dup-2", metering.OriginLocal, "dup_metric", "5"),
		)
		provider := reconciliationSide(observationFor("obs-dup-p", metering.OriginProvider, "dup_metric", "6"))
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].Status != ReconciliationStatusConflict {
			t.Fatalf("items = %+v, want one conflict item", result.Items)
		}
		if result.Items[0].Reason != ReconciliationReasonDuplicateLocal {
			t.Fatalf("reason = %q, want duplicate_local", result.Items[0].Reason)
		}
		if len(result.Items[0].Local) != 2 {
			t.Fatalf("conflict item must retain both local sources: %+v", result.Items[0])
		}
		assertReconciliationNoDelta(t, result.Items[0])
	})

	t.Run("contradictory duplicate evidence conflicts", func(t *testing.T) {
		local := reconciliationSide(
			observationFor("obs-conf-1", metering.OriginLocal, "conf_metric", "5"),
			observationFor("obs-conf-2", metering.OriginLocal, "conf_metric", "6"),
		)
		provider := reconciliationSide(observationFor("obs-conf-p", metering.OriginProvider, "conf_metric", "5"))
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].Status != ReconciliationStatusConflict {
			t.Fatalf("items = %+v, want one conflict item", result.Items)
		}
		if result.Items[0].Reason != ReconciliationReasonConflictingLocal {
			t.Fatalf("reason = %q, want conflicting_local", result.Items[0].Reason)
		}
		assertReconciliationNoDelta(t, result.Items[0])
	})

	t.Run("provider conflict is distinct", func(t *testing.T) {
		local := reconciliationSide(observationFor("obs-pconf-l", metering.OriginLocal, "pconf_metric", "5"))
		provider := reconciliationSide(
			observationFor("obs-pconf-1", metering.OriginProvider, "pconf_metric", "6"),
			observationFor("obs-pconf-2", metering.OriginProvider, "pconf_metric", "7"),
		)
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Items[0].Reason != ReconciliationReasonConflictingProvider {
			t.Fatalf("reason = %q, want conflicting_provider", result.Items[0].Reason)
		}
	})

	t.Run("observation bound fails closed", func(t *testing.T) {
		observations := make([]metering.Observation, 0, MaxReconciliationObservations+1)
		for i := 0; i <= MaxReconciliationObservations; i++ {
			observations = append(observations, observationFor(fmt.Sprintf("obs-bound-%d", i), metering.OriginLocal, fmt.Sprintf("bound_metric_%d", i), "1"))
		}
		_, err := CompareComponentQuantities(reconciliationSide(observations...), reconciliationSide())
		if !errors.Is(err, ErrReconciliationBoundExceeded) {
			t.Fatalf("error = %v, want ErrReconciliationBoundExceeded", err)
		}
	})

	t.Run("result cardinality bound fails closed", func(t *testing.T) {
		providerObservations := make([]metering.Observation, 0, 33)
		for i := 0; i < 32; i++ {
			measures := make([]metering.Measure, 0, metering.MaxObservationMeasures)
			for j := 0; j < metering.MaxObservationMeasures; j++ {
				name := fmt.Sprintf("cardinality_metric_%d", i*metering.MaxObservationMeasures+j)
				measures = append(measures, reconciliationMeasure(t, keyFor(name), metering.QualityObserved, "method", "1"))
			}
			providerObservations = append(providerObservations, reconciliationObservation(t, fmt.Sprintf("obs-card-%d", i), metering.OriginProvider, measures...))
		}
		providerObservations = append(providerObservations, reconciliationObservation(t, "obs-card-tail", metering.OriginProvider,
			reconciliationMeasure(t, keyFor("cardinality_metric_tail"), metering.QualityObserved, "method", "1")))
		_, err := CompareComponentQuantities(
			reconciliationSide(observationFor("obs-card-local", metering.OriginLocal, "cardinality_local", "1")),
			reconciliationSide(providerObservations...),
		)
		if !errors.Is(err, ErrReconciliationBoundExceeded) {
			t.Fatalf("error = %v, want ErrReconciliationBoundExceeded", err)
		}
	})

	t.Run("empty evidence is rejected", func(t *testing.T) {
		if _, err := CompareComponentQuantities(reconciliationSide(), reconciliationSide()); !errors.Is(err, ErrReconciliationInput) {
			t.Fatalf("error = %v, want ErrReconciliationInput", err)
		}
	})

	t.Run("cross-store comparison is rejected", func(t *testing.T) {
		otherSubject := reconciliationSubject()
		otherSubject.StoreID = "store-other"
		otherObservation := observationFor("obs-other", metering.OriginProvider, "other_metric", "1")
		otherObservation.Subject = otherSubject
		otherObservation.Correlation = metering.CorrelationV2{StoreID: otherSubject.StoreID, ALegID: otherSubject.ALegID, BillingCallID: otherSubject.BillingCallID, BLegID: otherSubject.BLegID}
		provider := ReconciliationEvidenceSet{Subject: otherSubject, Observations: []metering.Observation{otherObservation}}
		_, err := CompareComponentQuantities(reconciliationSide(observationFor("obs-local-store", metering.OriginLocal, "store_metric", "1")), provider)
		if !errors.Is(err, ErrReconciliationInput) {
			t.Fatalf("error = %v, want ErrReconciliationInput", err)
		}
	})
}

// TestReconciliationCompareSameSideSemanticsIdentity proves that same-side
// duplicate/conflict classification includes the observation semantics and the
// other declared payload fields: two sources identical in value, quality and
// method but differing only in valid semantics are conflicting, not
// duplicates, and true duplicates are preserved.
func TestReconciliationCompareSameSideSemanticsIdentity(t *testing.T) {
	t.Parallel()

	key := reconciliationKey(metering.DirectionNone, "semantics_metric", metering.UnitCount, "reconciliation.semantics.v1")
	observed := func(id, origin, semantics string) metering.Observation {
		return reconciliationObservationWithSemantics(t, id, origin, semantics,
			reconciliationMeasure(t, key, metering.QualityObserved, "method", "5"))
	}

	t.Run("local semantics-only difference is conflicting", func(t *testing.T) {
		local := reconciliationSide(
			observed("obs-sem-local-delta", metering.OriginLocal, metering.SemanticsDelta),
			observed("obs-sem-local-cumulative", metering.OriginLocal, metering.SemanticsCumulative),
		)
		provider := reconciliationSide(observed("obs-sem-provider", metering.OriginProvider, metering.SemanticsDelta))
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].Status != ReconciliationStatusConflict {
			t.Fatalf("items = %+v, want one conflict item", result.Items)
		}
		if result.Items[0].Reason != ReconciliationReasonConflictingLocal {
			t.Fatalf("reason = %q, want conflicting_local", result.Items[0].Reason)
		}
		if len(result.Items[0].Local) != 2 {
			t.Fatalf("conflict item must retain both local sources: %+v", result.Items[0])
		}
		assertReconciliationNoDelta(t, result.Items[0])
	})

	t.Run("provider semantics-only difference is conflicting", func(t *testing.T) {
		local := reconciliationSide(observed("obs-sem2-local", metering.OriginLocal, metering.SemanticsDelta))
		provider := reconciliationSide(
			observed("obs-sem2-provider-delta", metering.OriginProvider, metering.SemanticsDelta),
			observed("obs-sem2-provider-cumulative", metering.OriginProvider, metering.SemanticsCumulative),
		)
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(result.Items) != 1 || result.Items[0].Status != ReconciliationStatusConflict {
			t.Fatalf("items = %+v, want one conflict item", result.Items)
		}
		if result.Items[0].Reason != ReconciliationReasonConflictingProvider {
			t.Fatalf("reason = %q, want conflicting_provider", result.Items[0].Reason)
		}
		if len(result.Items[0].Provider) != 2 {
			t.Fatalf("conflict item must retain both provider sources: %+v", result.Items[0])
		}
		assertReconciliationNoDelta(t, result.Items[0])
	})

	t.Run("identical semantics remain exact duplicates", func(t *testing.T) {
		local := reconciliationSide(
			observed("obs-sem-dup-1", metering.OriginLocal, metering.SemanticsDelta),
			observed("obs-sem-dup-2", metering.OriginLocal, metering.SemanticsDelta),
		)
		provider := reconciliationSide(observed("obs-sem-dup-p", metering.OriginProvider, metering.SemanticsDelta))
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Items[0].Reason != ReconciliationReasonDuplicateLocal {
			t.Fatalf("reason = %q, want duplicate_local", result.Items[0].Reason)
		}
	})

	t.Run("identical semantics provider duplicates are preserved", func(t *testing.T) {
		local := reconciliationSide(observed("obs-sem-pdup-l", metering.OriginLocal, metering.SemanticsCumulative))
		provider := reconciliationSide(
			observed("obs-sem-pdup-1", metering.OriginProvider, metering.SemanticsCumulative),
			observed("obs-sem-pdup-2", metering.OriginProvider, metering.SemanticsCumulative),
		)
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Items[0].Reason != ReconciliationReasonDuplicateProvider {
			t.Fatalf("reason = %q, want duplicate_provider", result.Items[0].Reason)
		}
	})

	t.Run("quality-only difference is conflicting", func(t *testing.T) {
		local := reconciliationSide(
			reconciliationObservationWithSemantics(t, "obs-q-local-observed", metering.OriginLocal, metering.SemanticsDelta,
				reconciliationMeasure(t, key, metering.QualityObserved, "method", "5")),
			reconciliationObservationWithSemantics(t, "obs-q-local-estimated", metering.OriginLocal, metering.SemanticsDelta,
				reconciliationMeasure(t, key, metering.QualityEstimated, "method", "5")),
		)
		provider := reconciliationSide(observed("obs-q-provider", metering.OriginProvider, metering.SemanticsDelta))
		result, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if result.Items[0].Reason != ReconciliationReasonConflictingLocal {
			t.Fatalf("reason = %q, want conflicting_local for a quality-only difference", result.Items[0].Reason)
		}
	})
}

// TestReconciliationCompareKeepsUnitsAndDirectionsDistinct proves that native
// multimodal units and input/output direction are part of component identity.
// Different direction or unit cannot produce a joined delta.
func TestReconciliationCompareKeepsUnitsAndDirectionsDistinct(t *testing.T) {
	t.Parallel()

	t.Run("direction remains distinct", func(t *testing.T) {
		inputImage := reconciliationKey(metering.DirectionInput, "image", metering.UnitImage, "reconciliation.media.v1")
		outputImage := reconciliationKey(metering.DirectionOutput, "image", metering.UnitImage, "reconciliation.media.v1")
		result, err := CompareComponentQuantities(
			reconciliationSide(reconciliationObservation(t, "obs-local-image-in", metering.OriginLocal, reconciliationMeasure(t, inputImage, metering.QualityObserved, "", "2"))),
			reconciliationSide(reconciliationObservation(t, "obs-provider-image-out", metering.OriginProvider, reconciliationMeasure(t, outputImage, metering.QualityObserved, "", "2"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(result.Items) != 2 {
			t.Fatalf("items = %+v, want two unjoined direction records", result.Items)
		}
		inputItem := reconciliationItem(t, result, metering.DirectionInput, "image")
		outputItem := reconciliationItem(t, result, metering.DirectionOutput, "image")
		if inputItem.Status != ReconciliationStatusMissingProvider || outputItem.Status != ReconciliationStatusMissingLocal {
			t.Fatalf("direction items = %+v / %+v, want missing_provider / missing_local", inputItem, outputItem)
		}
		assertReconciliationNoDelta(t, inputItem)
		assertReconciliationNoDelta(t, outputItem)
	})

	t.Run("native units remain distinct", func(t *testing.T) {
		tokenWidget := reconciliationKey(metering.DirectionNone, "widget", metering.UnitToken, "reconciliation.widget.v1")
		countWidget := reconciliationKey(metering.DirectionNone, "widget", metering.UnitCount, "reconciliation.widget.v1")
		result, err := CompareComponentQuantities(
			reconciliationSide(reconciliationObservation(t, "obs-local-widget", metering.OriginLocal, reconciliationMeasure(t, tokenWidget, metering.QualityObserved, "", "3"))),
			reconciliationSide(reconciliationObservation(t, "obs-provider-widget", metering.OriginProvider, reconciliationMeasure(t, countWidget, metering.QualityObserved, "", "3"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		if len(result.Items) != 2 || result.Items[0].Status == result.Items[1].Status {
			t.Fatalf("items = %+v, want distinct missing sides", result.Items)
		}
		for _, item := range result.Items {
			assertReconciliationNoDelta(t, item)
		}
	})

	t.Run("multimodal duration joins on its own unit", func(t *testing.T) {
		videoSeconds := reconciliationKey(metering.DirectionInput, "video", metering.UnitSecond, "reconciliation.media.v1")
		result, err := CompareComponentQuantities(
			reconciliationSide(reconciliationObservation(t, "obs-local-video", metering.OriginLocal, reconciliationMeasure(t, videoSeconds, metering.QualityObserved, "", "1.5"))),
			reconciliationSide(reconciliationObservation(t, "obs-provider-video", metering.OriginProvider, reconciliationMeasure(t, videoSeconds, metering.QualityObserved, "", "2.5"))),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		item := result.Items[0]
		if item.Status != ReconciliationStatusDiscrepant || item.Key.Unit != metering.UnitSecond {
			t.Fatalf("item = %+v, want discrepant second-unit comparison", item)
		}
		assertReconciliationDelta(t, item, "1/0", "1/0")
		if strings.TrimSpace(item.Key.Unit) == "" {
			t.Fatal("native unit was dropped from the result key")
		}
	})
}

// TestReconciliationUnionCapacityRejectsOverflow pins the bounded size
// computation used for the full-key union map. len(local)+len(provider) is a
// potentially large value; adding the operands directly overflows to a
// negative capacity hint for adversarial lengths. The helper must never return
// a negative or over-large capacity, while preserving the exact sum for the
// in-contract inputs.
func TestReconciliationUnionCapacityRejectsOverflow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		localLen    int
		providerLen int
	}{
		{name: "both max int", localLen: math.MaxInt, providerLen: math.MaxInt},
		{name: "local max int", localLen: math.MaxInt, providerLen: 1},
		{name: "provider max int", localLen: 1, providerLen: math.MaxInt},
		{name: "both at contract bound", localLen: MaxReconciliationMeasures, providerLen: MaxReconciliationMeasures},
		{name: "empty", localLen: 0, providerLen: 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reconciliationUnionCapacity(tc.localLen, tc.providerLen)
			if got < 0 {
				t.Fatalf("reconciliationUnionCapacity(%d, %d) = %d, want non-negative capacity", tc.localLen, tc.providerLen, got)
			}
			if got > 2*MaxReconciliationMeasures {
				t.Fatalf("reconciliationUnionCapacity(%d, %d) = %d, want capacity <= %d", tc.localLen, tc.providerLen, got, 2*MaxReconciliationMeasures)
			}
		})
	}
}

// TestReconciliationUnionCapacityKeepsExactSmallSum proves the overflow guard
// does not weaken the capacity hint for inputs the comparator can actually
// produce.
func TestReconciliationUnionCapacityKeepsExactSmallSum(t *testing.T) {
	t.Parallel()

	if got := reconciliationUnionCapacity(3, 4); got != 7 {
		t.Fatalf("reconciliationUnionCapacity(3, 4) = %d, want 7", got)
	}
	if got := reconciliationUnionCapacity(1, 1); got != 2 {
		t.Fatalf("reconciliationUnionCapacity(1, 1) = %d, want 2", got)
	}
}
