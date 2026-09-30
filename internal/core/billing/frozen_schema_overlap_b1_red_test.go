package billing_test

// B1 (PR #659 adversarial repair R7 prerequisite): generic frozen-schema
// overlap rejection. This file exercises the production seam end to end: a
// real billingcompose SnapshotCatalog publication, the frozen
// economics.TariffSnapshot it resolves, and the production billing consumers
// (ReferenceRater for the operator plane, RateCustomerPolicyObservation for
// the policy-selected retail plane). It deliberately uses neutral component
// names so acceptance depends on the explicit frozen schema relationship, not
// on component-name heuristics or numerical coincidence.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const b1SchemaID = "b1:frozen-overlap:v1"

func b1Key(direction metering.FlowDirection, component, unit string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: unit, SchemaID: b1SchemaID}
}

func b1Decimal(t *testing.T, raw string) metering.Decimal {
	t.Helper()
	d, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	return d
}

func b1Rule(t *testing.T, id string, key metering.ComponentKey, price string) economics.RatingRule {
	t.Helper()
	d := b1Decimal(t, price)
	return economics.RatingRule{ID: id, Component: &key, Currency: "USD", UnitPrice: &d}
}

func b1FixedRule(t *testing.T, id string, scope economics.FixedFeeScope, amount string) economics.RatingRule {
	t.Helper()
	d := b1Decimal(t, amount)
	return economics.RatingRule{ID: id, Currency: "USD", FixedAmount: &d, FixedScope: scope}
}

func b1Measure(t *testing.T, key metering.ComponentKey, quantity string) metering.Measure {
	t.Helper()
	d := b1Decimal(t, quantity)
	return metering.Measure{Key: key, Value: &d, Quality: metering.QualityObserved, MethodRef: b1SchemaID}
}

func b1UnavailableMeasure(key metering.ComponentKey) metering.Measure {
	return metering.Measure{Key: key, Quality: metering.QualityUnavailable, MethodRef: b1SchemaID}
}

func b1Observation(t *testing.T, id, bLegID, origin string, boundary metering.Boundary, perspective metering.EconomicPerspective, measures ...metering.Measure) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionLocalTransport
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	now := time.Unix(1_700_000_821, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: perspective, Boundary: boundary, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-b1", ALegID: "a-b1", BillingCallID: "call-b1", BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: "store-b1", ALegID: "a-b1", BillingCallID: "call-b1", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: b1SchemaID,
		Measures: measures,
	}
}

func b1Schema(kind metering.RelationshipKind, parent, child metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: b1SchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{{Kind: kind, Parent: parent, Child: child}},
	}}
}

func b1Tariff(t *testing.T, refID string, rules []economics.RatingRule, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	snapshot, err := economics.BuildTariffSnapshotWithSchemas(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: refID, Version: "v1"}, RaterID: "reference"},
		"USD", rules, schemas,
	)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas(%s): %v", refID, err)
	}
	return snapshot
}

func b1Resolve(t *testing.T, snapshot economics.TariffSnapshot) economics.TariffSnapshot {
	t.Helper()
	catalog := billingcompose.NewSnapshotCatalog()
	if err := catalog.PutTariff(snapshot); err != nil {
		t.Fatalf("PutTariff: %v", err)
	}
	resolved, err := catalog.ResolveTariff(context.Background(), billing.VersionRef{ID: snapshot.Ref.ID, Version: snapshot.Ref.Version})
	if err != nil {
		t.Fatalf("ResolveTariff: %v", err)
	}
	return resolved
}

func b1OperatorInput(t *testing.T, tariff economics.TariffSnapshot, observations []metering.Observation) economics.PostUsageRatingInput {
	t.Helper()
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

func b1RetailInput(t *testing.T, tariff economics.TariffSnapshot, observations []metering.Observation) economics.PostUsageRatingInput {
	t.Helper()
	in := b1OperatorInput(t, tariff, observations)
	in.Perspective = metering.PerspectiveCustomer
	in.Basis = economics.BasisCustomerPolicy
	in.Tariff = economics.RatingSnapshotRef{}
	in.TariffContent = nil
	in.Policy = economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "b1-policy", Version: "v1"}, PolicyID: "customer-independent"}
	in.PolicyContent = &economics.SnapshotContentRef{ContentRef: "catalog://b1/policy/v1", ContentHash: strings.Repeat("5", 64)}
	return in
}

func b1AssertNoPayableLines(t *testing.T, valuation economics.Valuation) {
	t.Helper()
	for _, line := range valuation.Lines {
		if line.Amount != nil || line.Status == economics.RatingLineRated || line.Status == economics.RatingLineExplicitFree {
			t.Fatalf("overlap rejection must not emit payable lines, got %+v", line)
		}
	}
}

func b1PayableTotal(t *testing.T, valuation economics.Valuation) string {
	t.Helper()
	if len(valuation.Totals) == 0 || valuation.Totals[0].Amount == nil {
		t.Fatalf("valuation has no payable total: %+v", valuation.Totals)
	}
	return valuation.Totals[0].Amount.CanonicalString()
}

func b1RunOverlapRejection(t *testing.T, kind metering.RelationshipKind) {
	t.Helper()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-overlap", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
	}, b1Schema(kind, parent, child))
	resolved := b1Resolve(t, tariff)
	if resolved.Content.ContentHash != tariff.Content.ContentHash {
		t.Fatalf("catalog changed frozen schema hash: %s != %s", resolved.Content.ContentHash, tariff.Content.ContentHash)
	}

	operatorObs := b1Observation(t, "b1-overlap", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	operatorVal, operatorErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{operatorObs}))
	if !errors.Is(operatorErr, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("operator overlap error=%v, want ErrSchemaOverlapConflict; lines=%+v totals=%+v", operatorErr, operatorVal.Lines, operatorVal.Totals)
	}
	if operatorVal.Completeness != economics.CompletenessConflict {
		t.Fatalf("operator completeness=%q, want conflict", operatorVal.Completeness)
	}
	b1AssertNoPayableLines(t, operatorVal)

	retailObs := b1Observation(t, "b1-overlap-retail", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveCustomer,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	retailVal, retailErr := billing.RateCustomerPolicyObservation(context.Background(),
		b1RetailInput(t, resolved, []metering.Observation{retailObs}), resolved)
	if !errors.Is(retailErr, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("selected retail overlap error=%v, want ErrSchemaOverlapConflict; lines=%+v totals=%+v", retailErr, retailVal.Lines, retailVal.Totals)
	}
	b1AssertNoPayableLines(t, retailVal)
}

// TestB1FrozenSchemaOverlapRejectedThroughCatalogRaterAndRetail is the primary
// RED vector. Before B1, a complete two-rate tariff (parent aggregate price 1
// and declared included child price 2) rates both measures successfully
// (10*1 + 4*2 = 18), double-counting the same work. After B1 the frozen schema
// relationship is rejected as a typed conflict with no payable lines.
func TestB1FrozenSchemaOverlapRejectedThroughCatalogRaterAndRetail(t *testing.T) {
	t.Parallel()
	for _, kind := range []metering.RelationshipKind{
		metering.RelationshipSubset,
		metering.RelationshipPartition,
		metering.RelationshipAggregate,
	} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			b1RunOverlapRejection(t, kind)
		})
	}
}

// TestB1FrozenSchemaOverlapCrossDirectionStaysAdditive proves direction is
// part of the overlap identity: an explicit inclusion edge between input and
// output components (same unit) remains additive and separately governed.
func TestB1FrozenSchemaOverlapCrossDirectionStaysAdditive(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionOutput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-cross-direction", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-cross-direction", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if err != nil {
		t.Fatalf("cross-direction inclusion must stay additive, got %v", err)
	}
	if len(val.Lines) != 2 || b1PayableTotal(t, val) != "18/0" {
		t.Fatalf("cross-direction lines=%+v total=%v, want two additive lines totalling 18", val.Lines, val.Totals)
	}
}

// TestB1FrozenSchemaOverlapIndependentBLegScopesStayAdditive proves the check
// is scoped to one source/B-leg/work scope: parent in one B-leg and child in
// another B-leg never collapse into an overlap.
func TestB1FrozenSchemaOverlapIndependentBLegScopesStayAdditive(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-independent-scopes", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)
	parentObs := b1Observation(t, "b1-scope-parent", "b-leg-a", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"))
	childObs := b1Observation(t, "b1-scope-child", "b-leg-b", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, child, "4"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{parentObs, childObs}))
	if err != nil {
		t.Fatalf("independent B-leg scopes must stay additive, got %v", err)
	}
	if len(val.Lines) != 2 || b1PayableTotal(t, val) != "18/0" {
		t.Fatalf("independent scopes lines=%+v total=%v, want two additive lines totalling 18", val.Lines, val.Totals)
	}
}

// TestB1FrozenSchemaOverlapTransformStaysAdditive proves transform edges are
// not treated as inclusion: a declared unit transform remains additive.
func TestB1FrozenSchemaOverlapTransformStaysAdditive(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitSecond)
	tariff := b1Tariff(t, "b1-transform", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
	}, b1Schema(metering.RelationshipTransform, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-transform", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if err != nil {
		t.Fatalf("transform edge must stay additive, got %v", err)
	}
	if len(val.Lines) != 2 || b1PayableTotal(t, val) != "18/0" {
		t.Fatalf("transform lines=%+v total=%v, want two additive lines totalling 18", val.Lines, val.Totals)
	}
}

// TestB1FrozenSchemaOverlapLegacyEmptySchemaStaysAdditive pins legacy behavior:
// with no frozen schema, identically named/related components remain additive.
// Overlap is never inferred from component names or coincidental numbers.
func TestB1FrozenSchemaOverlapLegacyEmptySchemaStaysAdditive(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-legacy", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
	}, nil)
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-legacy", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if err != nil {
		t.Fatalf("legacy empty-schema tariff must be unchanged, got %v", err)
	}
	if len(val.Lines) != 2 || b1PayableTotal(t, val) != "18/0" {
		t.Fatalf("legacy lines=%+v total=%v, want two additive lines totalling 18", val.Lines, val.Totals)
	}
}

// TestB1FrozenSchemaOverlapSingleAggregateRuleUnchanged proves a tariff that
// prices only the parent aggregate with no child evidence is untouched.
func TestB1FrozenSchemaOverlapSingleAggregateRuleUnchanged(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-single", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-single", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if err != nil {
		t.Fatalf("single aggregate rule must be unchanged, got %v", err)
	}
	if v := b1PayableTotal(t, val); v != "10/0" {
		t.Fatalf("single aggregate total=%s, want 10", v)
	}
}

// TestB1FrozenSchemaOverlapChildOnlyPartialUnchanged pins the current
// child-only behavior for B2: when the parent aggregate is present but unpriced
// the rater does not invent an overlap, keeps the priced child, and retains its
// existing partial diagnostic until B2/C owns suppression.
func TestB1FrozenSchemaOverlapChildOnlyPartialUnchanged(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-child-only", []economics.RatingRule{
		b1Rule(t, "child-rate", child, "2"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-child-only", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("unpriced parent must not be treated as a priced overlap: %v", err)
	}
	if val.Completeness != economics.CompletenessPartial {
		t.Fatalf("child-only completeness=%q, want partial (B2 unchanged)", val.Completeness)
	}
	var childRated bool
	for _, line := range val.Lines {
		if line.Component != nil && line.Component.Component == child.Component && line.Status == economics.RatingLineRated {
			childRated = true
		}
	}
	if !childRated {
		t.Fatalf("priced child line missing from %+v", val.Lines)
	}
}

// TestB1FrozenSchemaOverlapZeroAbsentNullNotPriced proves the rejection never
// manufactures a priced value: an absent child, a zero-quantity child, and an
// unavailable/null child are not turned into a payable overlap.
func TestB1FrozenSchemaOverlapZeroAbsentNullNotPriced(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-zero", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)

	for _, tc := range []struct {
		name     string
		measures []metering.Measure
	}{
		{name: "absent", measures: []metering.Measure{b1Measure(t, parent, "10")}},
		{name: "zero", measures: []metering.Measure{b1Measure(t, parent, "10"), b1Measure(t, child, "0")}},
		{name: "null", measures: []metering.Measure{b1Measure(t, parent, "10"), b1UnavailableMeasure(child)}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := b1Observation(t, "b1-zero-"+tc.name, "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, tc.measures...)
			rater, err := billing.NewReferenceRater(resolved)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
			if errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
				t.Fatalf("%s child must not be priced into an overlap: %v", tc.name, rateErr)
			}
			var parentAmount *metering.Decimal
			var childAmount *metering.Decimal
			for _, line := range val.Lines {
				if line.Component == nil {
					continue
				}
				switch line.Component.Component {
				case parent.Component:
					parentAmount = line.Amount
				case child.Component:
					childAmount = line.Amount
				}
			}
			if parentAmount == nil || parentAmount.CanonicalString() != b1Decimal(t, "10").CanonicalString() {
				t.Fatalf("%s parent amount=%v, want non-fabricated 10", tc.name, parentAmount)
			}
			if childAmount != nil && childAmount.CanonicalString() != b1Decimal(t, "0").CanonicalString() {
				t.Fatalf("%s child fabricated amount=%v", tc.name, childAmount)
			}
		})
	}
}

// TestB1FrozenSchemaOverlapSchemaHashAndRaterFreeze proves the frozen schema
// participates in the snapshot content hash and that the constructed rater
// keeps a private frozen copy: mutating the caller's snapshot after
// construction cannot disable the overlap guarantee.
func TestB1FrozenSchemaOverlapSchemaHashAndRaterFreeze(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	rules := []economics.RatingRule{b1Rule(t, "parent-rate", parent, "1"), b1Rule(t, "child-rate", child, "2")}
	legacy := b1Tariff(t, "b1-hash", rules, nil)
	withSchema := b1Tariff(t, "b1-hash", rules, b1Schema(metering.RelationshipSubset, parent, child))
	if legacy.Content.ContentHash == withSchema.Content.ContentHash {
		t.Fatal("frozen schema material must participate in the snapshot content hash")
	}

	rater, err := billing.NewReferenceRater(withSchema)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	// Caller-side mutation after construction must not change replay behavior.
	withSchema.Schemas = nil
	withSchema.Rules = nil
	obs := b1Observation(t, "b1-freeze", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, rater.Snapshot(), []metering.Observation{obs}))
	if !errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("rater did not freeze schema material: err=%v lines=%+v", rateErr, val.Lines)
	}
}

// TestB1FrozenSchemaOverlapFixedFeeUnaffected proves fixed-fee behavior is not
// perturbed when no overlapping quantity double count exists.
func TestB1FrozenSchemaOverlapFixedFeeUnaffected(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-fixed", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1FixedRule(t, "call-fee", economics.FixedFeeScopeCall, "4"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-fixed", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if err != nil {
		t.Fatalf("fixed fee with no overlap must rate normally, got %v", err)
	}
	if v := b1PayableTotal(t, val); v != "14/0" {
		t.Fatalf("fixed fee total=%s, want 14", v)
	}
}

// TestB1FrozenSchemaOverlapFixedFeeSurvivesConflict proves an independent
// trusted-scope fixed fee stays payable while the overlapping quantity lines
// are suppressed and the typed conflict is returned.
func TestB1FrozenSchemaOverlapFixedFeeSurvivesConflict(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-fixed-conflict", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
		b1FixedRule(t, "call-fee", economics.FixedFeeScopeCall, "4"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-fixed-conflict", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if !errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("overlap error=%v, want ErrSchemaOverlapConflict", rateErr)
	}
	var fixedPayable, quantityPayable bool
	for _, line := range val.Lines {
		if line.Amount == nil {
			continue
		}
		if line.FixedFee != nil {
			fixedPayable = true
		}
		if line.Component != nil {
			quantityPayable = true
		}
	}
	if !fixedPayable {
		t.Fatalf("independent fixed fee must stay payable, got %+v", val.Lines)
	}
	if quantityPayable {
		t.Fatalf("overlapping quantity lines must be suppressed, got %+v", val.Lines)
	}
}

// TestB1FrozenSchemaOverlapConflictPreservesUnrelatedBLeg is the finding-1 RED
// vector. A same-scope parent/child conflict in one B-leg must not erase an
// unrelated payable quantity in another B-leg. Before the repair the whole
// aggregate loop was gated on overlapErr==nil, so the unrelated line disappeared
// and the valuation reported no payable total even though only one scope was
// conflicted.
func TestB1FrozenSchemaOverlapConflictPreservesUnrelatedBLeg(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	other := b1Key(metering.DirectionInput, "vendor:unrelated_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-unrelated-scope", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		b1Rule(t, "child-rate", child, "2"),
		b1Rule(t, "other-rate", other, "3"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)

	conflictObs := b1Observation(t, "b1-conflict-scope", "b-leg-conflict", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	otherObs := b1Observation(t, "b1-unrelated-scope", "b-leg-unrelated", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, other, "5"))

	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{conflictObs, otherObs}))
	if !errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("conflicted B-leg error=%v, want ErrSchemaOverlapConflict; lines=%+v totals=%+v", rateErr, val.Lines, val.Totals)
	}
	if val.Completeness != economics.CompletenessConflict {
		t.Fatalf("conflicted completeness=%q, want conflict", val.Completeness)
	}
	var unrelatedAmount *metering.Decimal
	var conflictedPayable bool
	for _, line := range val.Lines {
		if line.Amount == nil || line.Component == nil {
			continue
		}
		switch line.Component.Component {
		case parent.Component, child.Component:
			if line.Amount.CanonicalString() != b1Decimal(t, "0").CanonicalString() {
				conflictedPayable = true
			}
		case other.Component:
			unrelatedAmount = line.Amount
		}
	}
	if conflictedPayable {
		t.Fatalf("scope-conflicted quantity lines must not be payable, got %+v", val.Lines)
	}
	if unrelatedAmount == nil || unrelatedAmount.CanonicalString() != b1Decimal(t, "15").CanonicalString() {
		t.Fatalf("unrelated B-leg quantity must stay payable at 15, got %v in %+v", unrelatedAmount, val.Lines)
	}
	if total := b1PayableTotal(t, val); total != "15/0" {
		t.Fatalf("unrelated B-leg payable total=%s, want 15", total)
	}
}

// TestB1FrozenSchemaOverlapZeroQuantityMinimumAmountIsPriced is the finding-2a
// RED vector. A zero raw quantity whose rule resolves to a positive minimum is
// an effective payable contribution and must participate in the overlap check.
// Before the repair the priced test only inspected the raw aggregate quantity, so
// zero-quantity minimum lines were skipped and two related payable lines could
// both be emitted without any conflict.
func TestB1FrozenSchemaOverlapZeroQuantityMinimumAmountIsPriced(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	minRate := b1Decimal(t, "2")
	minAmount := b1Decimal(t, "5")
	minimum := economics.RatingRule{
		ID: "child-min", Kind: economics.RatingRuleMinimum, Component: &child, Currency: "USD",
		UnitPrice: &minRate, MinimumAmount: &minAmount,
	}
	tariff := b1Tariff(t, "b1-zero-minimum", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		minimum,
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-zero-minimum", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "0"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if !errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("zero-quantity minimum error=%v, want ErrSchemaOverlapConflict; lines=%+v totals=%+v", rateErr, val.Lines, val.Totals)
	}
	if val.Completeness != economics.CompletenessConflict {
		t.Fatalf("zero-quantity minimum completeness=%q, want conflict", val.Completeness)
	}
	b1AssertNoPayableLines(t, val)
}

// TestB1FrozenSchemaOverlapBlockMinimumZeroQuantityIsPriced proves the priced
// signal is the effective post-rule amount for a combined block+minimum rule,
// not the raw zero quantity and not the mere presence of a block modifier.
func TestB1FrozenSchemaOverlapBlockMinimumZeroQuantityIsPriced(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	rate := b1Decimal(t, "2")
	block := b1Decimal(t, "10")
	minAmount := b1Decimal(t, "5")
	blockMinimum := economics.RatingRule{
		ID: "child-block-min", Kind: economics.RatingRuleLinear, Component: &child, Currency: "USD",
		UnitPrice: &rate, BlockSize: &block, MinimumAmount: &minAmount,
	}
	tariff := b1Tariff(t, "b1-block-minimum", []economics.RatingRule{
		b1Rule(t, "parent-rate", parent, "1"),
		blockMinimum,
	}, b1Schema(metering.RelationshipAggregate, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-block-minimum", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "0"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if !errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("block+minimum zero-quantity error=%v, want ErrSchemaOverlapConflict; lines=%+v", rateErr, val.Lines)
	}
	if val.Completeness != economics.CompletenessConflict {
		t.Fatalf("block+minimum zero-quantity completeness=%q, want conflict", val.Completeness)
	}
	b1AssertNoPayableLines(t, val)
}

// TestB1FrozenSchemaOverlapExplicitZeroRateIsNotPriced is the finding-2b RED
// vector. A positive raw quantity priced at an explicit zero unit rate has no
// economic contribution, so it must not be treated as a priced overlap. Before
// the repair the positive raw quantity alone triggered a false conflict and
// suppressed the genuinely payable child.
func TestB1FrozenSchemaOverlapExplicitZeroRateIsNotPriced(t *testing.T) {
	t.Parallel()
	parent := b1Key(metering.DirectionInput, "vendor:aggregate_total", metering.UnitToken)
	child := b1Key(metering.DirectionInput, "vendor:included_part", metering.UnitToken)
	tariff := b1Tariff(t, "b1-explicit-free", []economics.RatingRule{
		b1Rule(t, "parent-free", parent, "0"),
		b1Rule(t, "child-rate", child, "2"),
	}, b1Schema(metering.RelationshipSubset, parent, child))
	resolved := b1Resolve(t, tariff)
	obs := b1Observation(t, "b1-explicit-free", "b-leg-1", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, parent, "10"), b1Measure(t, child, "4"))
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	val, rateErr := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{obs}))
	if errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("explicit zero rate must not conflict: %v", rateErr)
	}
	if rateErr != nil {
		t.Fatalf("explicit zero rate must rate normally, got %v", rateErr)
	}
	var childAmount *metering.Decimal
	var parentStatus economics.RatingLineStatus
	for _, line := range val.Lines {
		if line.Component == nil {
			continue
		}
		switch line.Component.Component {
		case child.Component:
			childAmount = line.Amount
		case parent.Component:
			parentStatus = line.Status
		}
	}
	if childAmount == nil || childAmount.CanonicalString() != b1Decimal(t, "8").CanonicalString() {
		t.Fatalf("priced child amount=%v, want 8 in %+v", childAmount, val.Lines)
	}
	if parentStatus != economics.RatingLineExplicitFree {
		t.Fatalf("zero-rate parent status=%q, want explicit free", parentStatus)
	}
	if total := b1PayableTotal(t, val); total != "8/0" {
		t.Fatalf("explicit-free total=%s, want 8", total)
	}
}
