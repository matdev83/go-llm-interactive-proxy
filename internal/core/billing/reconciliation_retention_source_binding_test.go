package billing

import (
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// bindingQuantityKey is the shared quantity component key for the source
// binding fixtures.
func bindingQuantityKey() metering.ComponentKey {
	return reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
}

// bindingQuantity runs the authoritative Task 12.1 producer over two sides.
func bindingQuantity(t *testing.T, local, provider []metering.Observation) ComponentQuantityComparison {
	t.Helper()
	comparison, err := CompareComponentQuantities(reconciliationSide(local...), reconciliationSide(provider...))
	if err != nil {
		t.Fatalf("CompareComponentQuantities: %v", err)
	}
	return comparison
}

// bindingQuantityResult composes a retention result whose aggregate is the
// authoritative projection of the retained quantity comparison. The monetary
// comparison is absent, so quantity-origin findings must bind through
// ReconciliationFindingsFromQuantityComparison alone.
func bindingQuantityResult(t *testing.T, quantity ComponentQuantityComparison) ReconciliationRetentionResult {
	t.Helper()
	result := retentionTestResult(t, "retention-source-binding", 1)
	policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.40"), nil))
	findings, err := ReconciliationFindingsFromQuantityComparison(result.Scope, quantity)
	if err != nil {
		t.Fatalf("ReconciliationFindingsFromQuantityComparison: %v", err)
	}
	aggregate, err := AggregateReconciliationFindings(policy, findings)
	if err != nil {
		t.Fatalf("AggregateReconciliationFindings: %v", err)
	}
	result.Quantity = &quantity
	result.Monetary = nil
	result.Aggregate = &aggregate
	return result
}

// bindingMonetary runs the authoritative Task 12.2 producer over E/Q/P
// valuations.
func bindingMonetary(t *testing.T, valuations ...economics.Valuation) MonetaryDiscrepancyComparison {
	t.Helper()
	comparison, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: valuations})
	if err != nil {
		t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
	}
	return comparison
}

// bindingMonetaryResult replaces the retained monetary comparison and rebuilds
// the aggregate from its authoritative producer projection.
func bindingMonetaryResult(t *testing.T, result ReconciliationRetentionResult, monetary MonetaryDiscrepancyComparison) ReconciliationRetentionResult {
	t.Helper()
	policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.40"), nil))
	findings, err := ReconciliationFindingsFromMonetaryComparison(result.Scope, monetary)
	if err != nil {
		t.Fatalf("ReconciliationFindingsFromMonetaryComparison: %v", err)
	}
	aggregate, err := AggregateReconciliationFindings(policy, findings)
	if err != nil {
		t.Fatalf("AggregateReconciliationFindings: %v", err)
	}
	result.Monetary = &monetary
	result.Policy = policy.Ref
	result.Aggregate = &aggregate
	return result
}

func requireRetentionSourceBindingAccepted(t *testing.T, result ReconciliationRetentionResult) {
	t.Helper()
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	canonical, err := result.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if _, err := ParseReconciliationRetentionResult(canonical); err != nil {
		t.Fatalf("ParseReconciliationRetentionResult: %v", err)
	}
}

func requireRetentionSourceBindingRejected(t *testing.T, result ReconciliationRetentionResult) {
	t.Helper()
	if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
		t.Fatalf("Validate error = %v, want ErrInvalidReconciliationRetention", err)
	}
	if _, err := result.CanonicalJSON(); !errors.Is(err, ErrInvalidReconciliationRetention) {
		t.Fatalf("CanonicalJSON error = %v, want ErrInvalidReconciliationRetention", err)
	}
}

// TestReconciliationRetentionBindsAggregateFindingsToProducerSources proves
// every retained aggregate finding is exactly one authoritative producer
// projection of the retained quantity/monetary evidence: the source
// Status/Reason, exact amounts, qualities, source refs and valuation ids cannot
// disagree with the evidence the producer consumes, and findings without
// either retained comparison fail closed instead of inferring provenance.
func TestReconciliationRetentionBindsAggregateFindingsToProducerSources(t *testing.T) {
	t.Parallel()
	key := bindingQuantityKey()

	quantityObservation := func(t *testing.T, id, origin, value string) metering.Observation {
		t.Helper()
		return reconciliationObservation(t, id, origin, reconciliationMeasure(t, key, metering.QualityObserved, "binding-tokenizer-v1", value))
	}
	quantityBase := func(t *testing.T, local, provider []metering.Observation) ReconciliationRetentionResult {
		t.Helper()
		return bindingQuantityResult(t, bindingQuantity(t, local, provider))
	}
	monetaryBase := func(t *testing.T, valuations ...economics.Valuation) ReconciliationRetentionResult {
		t.Helper()
		return bindingMonetaryResult(t, retentionTestResult(t, "retention-source-binding", 1), bindingMonetary(t, valuations...))
	}
	completeMonetary := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return monetaryBase(t,
			monetaryTestValuation(t, "binding-monetary-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "binding-monetary-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")))
	}
	missingPMonetary := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return monetaryBase(t,
			monetaryTestValuation(t, "binding-missing-p-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "binding-missing-p-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")))
	}
	partialMonetary := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return monetaryBase(t,
			monetaryPartialTestValuation(t, "binding-partial-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "binding-partial-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
			monetaryTestValuation(t, "binding-partial-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")))
	}
	incomparableMonetary := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		provider := monetaryTestValuation(t, "binding-incomparable-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32"))
		provider.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "catalog://binding/other-qualifiers/v1", ContentHash: strings.Repeat("9", 64)}
		if err := provider.Validate(); err != nil {
			t.Fatalf("provider valuation: %v", err)
		}
		return monetaryBase(t,
			monetaryTestValuation(t, "binding-incomparable-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			provider)
	}

	t.Run("quantity producer controls bind", func(t *testing.T) {
		t.Parallel()
		matched := quantityBase(t,
			[]metering.Observation{quantityObservation(t, "binding-qty-matched-local", metering.OriginLocal, "100")},
			[]metering.Observation{quantityObservation(t, "binding-qty-matched-provider", metering.OriginProvider, "100")})
		if matched.Aggregate.Findings[0].Status != ReconciliationStatusMatched {
			t.Fatalf("matched control source status = %s", matched.Aggregate.Findings[0].Status)
		}
		requireRetentionSourceBindingAccepted(t, matched)

		discrepant := quantityBase(t,
			[]metering.Observation{quantityObservation(t, "binding-qty-discrepant-local", metering.OriginLocal, "100")},
			[]metering.Observation{quantityObservation(t, "binding-qty-discrepant-provider", metering.OriginProvider, "110")})
		requireRetentionSourceBindingAccepted(t, discrepant)

		cacheReadKey := reconciliationKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
		missing := quantityBase(t,
			[]metering.Observation{reconciliationObservation(t, "binding-qty-missing-local", metering.OriginLocal, reconciliationMeasure(t, cacheReadKey, metering.QualityObserved, "binding-tokenizer-v1", "7"))},
			[]metering.Observation{quantityObservation(t, "binding-qty-missing-provider", metering.OriginProvider, "100")})
		requireRetentionSourceBindingAccepted(t, missing)

		incomparable := quantityBase(t,
			[]metering.Observation{reconciliationObservation(t, "binding-qty-incomparable-local", metering.OriginLocal, reconciliationMeasure(t, key, metering.QualityObserved, "binding-tokenizer-v1", "100"))},
			[]metering.Observation{reconciliationObservationWithSemantics(t, "binding-qty-incomparable-provider", metering.OriginProvider, metering.SemanticsCumulative,
				reconciliationMeasure(t, key, metering.QualityObserved, "binding-tokenizer-v1", "100"))})
		if incomparable.Quantity.Items[0].Status != ReconciliationStatusIncomparable {
			t.Fatalf("incomparable control status = %s", incomparable.Quantity.Items[0].Status)
		}
		requireRetentionSourceBindingAccepted(t, incomparable)

		conflictKey := reconciliationKey(metering.DirectionNone, "retention_binding_metric", metering.UnitCount, "reconciliation.semantics.v1")
		conflictObservation := func(t *testing.T, id, origin, semantics string) metering.Observation {
			t.Helper()
			return reconciliationObservationWithSemantics(t, id, origin, semantics,
				reconciliationMeasure(t, conflictKey, metering.QualityObserved, "binding-tokenizer-v1", "5"))
		}
		conflict := quantityBase(t,
			[]metering.Observation{
				conflictObservation(t, "binding-conflict-delta", metering.OriginLocal, metering.SemanticsDelta),
				conflictObservation(t, "binding-conflict-cumulative", metering.OriginLocal, metering.SemanticsCumulative),
			},
			[]metering.Observation{conflictObservation(t, "binding-conflict-provider", metering.OriginProvider, metering.SemanticsDelta)})
		if conflict.Quantity.Items[0].Status != ReconciliationStatusConflict {
			t.Fatalf("conflict control status = %s", conflict.Quantity.Items[0].Status)
		}
		requireRetentionSourceBindingAccepted(t, conflict)
	})

	t.Run("monetary producer controls bind", func(t *testing.T) {
		t.Parallel()
		requireRetentionSourceBindingAccepted(t, completeMonetary(t))
		requireRetentionSourceBindingAccepted(t, missingPMonetary(t))
		requireRetentionSourceBindingAccepted(t, partialMonetary(t))
		requireRetentionSourceBindingAccepted(t, incomparableMonetary(t))
		matched := monetaryBase(t,
			monetaryTestValuation(t, "binding-monetary-matched-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "binding-monetary-matched-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.00")))
		requireRetentionSourceBindingAccepted(t, matched)
	})

	t.Run("forged aggregate source classifications fail closed", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name   string
			build  func(t *testing.T) ReconciliationRetentionResult
			mutate func(t *testing.T, result *ReconciliationRetentionResult)
		}{
			{
				name:  "comparable monetary source status changed to matched",
				build: completeMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					result.Aggregate.Findings[0].Status = ReconciliationStatusMatched
				},
			},
			{
				name:  "comparable monetary source carries an invented reason",
				build: completeMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					result.Aggregate.Findings[0].Reason = ReconciliationReasonCoverageMismatch
				},
			},
			{
				name:  "absent-value monetary finding marked matched",
				build: missingPMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					finding := &result.Aggregate.Findings[0]
					finding.Status = ReconciliationStatusMatched
					finding.Reason = ReconciliationReasonNone
					finding.EvaluatedStatus = ReconciliationStatusPartial
					finding.EvaluationReason = ReconciliationReasonTolerancePolicyMissing
				},
			},
			{
				name:  "monetary missing side swapped",
				build: missingPMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					finding := &result.Aggregate.Findings[0]
					finding.Status = ReconciliationStatusMissingLocal
					finding.EvaluatedStatus = ReconciliationStatusMissingLocal
				},
			},
			{
				name:  "monetary partial reason swapped",
				build: partialMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					finding := &result.Aggregate.Findings[0]
					finding.Reason = ReconciliationComparisonReason(MonetaryReasonAmountUnavailable)
					finding.EvaluationReason = finding.Reason
				},
			},
			{
				name:  "monetary incomparable reason swapped",
				build: incomparableMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					finding := &result.Aggregate.Findings[0]
					finding.Reason = ReconciliationComparisonReason(MonetaryReasonCurrencyMismatch)
					finding.EvaluationReason = finding.Reason
				},
			},
			{
				name: "quantity comparable finding marked matched",
				build: func(t *testing.T) ReconciliationRetentionResult {
					t.Helper()
					return quantityBase(t,
						[]metering.Observation{quantityObservation(t, "binding-poison-qty-matched-local", metering.OriginLocal, "100")},
						[]metering.Observation{quantityObservation(t, "binding-poison-qty-matched-provider", metering.OriginProvider, "110")})
				},
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					result.Aggregate.Findings[0].Status = ReconciliationStatusMatched
				},
			},
			{
				name: "quantity missing side swapped",
				build: func(t *testing.T) ReconciliationRetentionResult {
					t.Helper()
					cacheReadKey := reconciliationKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
					return quantityBase(t,
						[]metering.Observation{reconciliationObservation(t, "binding-poison-qty-missing-local", metering.OriginLocal, reconciliationMeasure(t, cacheReadKey, metering.QualityObserved, "binding-tokenizer-v1", "7"))},
						[]metering.Observation{quantityObservation(t, "binding-poison-qty-missing-provider", metering.OriginProvider, "100")})
				},
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					for i := range result.Aggregate.Findings {
						finding := &result.Aggregate.Findings[i]
						if finding.Component == metering.ComponentInputToken {
							finding.Status = ReconciliationStatusMissingProvider
							finding.EvaluatedStatus = ReconciliationStatusMissingProvider
						}
					}
				},
			},
			{
				name: "quantity comparable finding forged amounts",
				build: func(t *testing.T) ReconciliationRetentionResult {
					t.Helper()
					return quantityBase(t,
						[]metering.Observation{quantityObservation(t, "binding-poison-qty-amount-local", metering.OriginLocal, "100")},
						[]metering.Observation{quantityObservation(t, "binding-poison-qty-amount-provider", metering.OriginProvider, "110")})
				},
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					finding := &result.Aggregate.Findings[0]
					finding.Expected = operatorCostTestAmount(t, metering.UnitToken, "102")
					finding.Evaluation.Expected = operatorCostTestAmount(t, metering.UnitToken, "102")
					finding.Evaluation.SignedDelta = operatorCostTestAmount(t, metering.UnitToken, "8")
					finding.Evaluation.AbsoluteDelta = operatorCostTestAmount(t, metering.UnitToken, "8")
				},
			},
			{
				name: "comparable quantity item carries a source reason",
				build: func(t *testing.T) ReconciliationRetentionResult {
					t.Helper()
					return quantityBase(t,
						[]metering.Observation{quantityObservation(t, "binding-poison-item-reason-local", metering.OriginLocal, "100")},
						[]metering.Observation{quantityObservation(t, "binding-poison-item-reason-provider", metering.OriginProvider, "100")})
				},
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					result.Aggregate = nil
					result.Quantity.Items[0].Reason = ReconciliationReasonCoverageMismatch
				},
			},
			{
				name:  "monetary finding forged valuation ids",
				build: completeMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					result.Aggregate.Findings[0].ValuationIDs = append(result.Aggregate.Findings[0].ValuationIDs, "forged-valuation")
				},
			},
			{
				name:  "monetary finding forged source refs",
				build: completeMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					result.Aggregate.Findings[0].SourceObservationRefs = append(result.Aggregate.Findings[0].SourceObservationRefs, operatorCostTestRef("forged-observation"))
				},
			},
			{
				name:  "aggregate findings without retained comparisons",
				build: completeMonetary,
				mutate: func(t *testing.T, result *ReconciliationRetentionResult) {
					t.Helper()
					result.Quantity = nil
					result.Monetary = nil
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				result := tc.build(t)
				tc.mutate(t, &result)
				requireRetentionSourceBindingRejected(t, result)
			})
		}
	})
}
