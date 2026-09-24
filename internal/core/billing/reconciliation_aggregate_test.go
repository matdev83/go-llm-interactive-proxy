package billing

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func reconciliationTestFinding(t *testing.T, id, scope, currency, unit, expected, reported, quality string, status ReconciliationComparisonStatus) ReconciliationFinding {
	t.Helper()
	finding := ReconciliationFinding{
		ID: id, Scope: scope, Currency: currency, Unit: unit,
		Status: status, LocalQuality: quality, ProviderQuality: quality,
	}
	if expected != "" {
		amount := toleranceAmount(t, currencyOrUnit(currency, unit), expected)
		finding.Expected = &amount
	}
	if reported != "" {
		amount := toleranceAmount(t, currencyOrUnit(currency, unit), reported)
		finding.Reported = &amount
	}
	return finding
}

func currencyOrUnit(currency, unit string) string {
	if currency != "" {
		return currency
	}
	return unit
}

func aggregateRow(t *testing.T, result ReconciliationAggregate, scope, currency, unit string) ReconciliationAggregateRow {
	t.Helper()
	for _, row := range result.Rows {
		if row.Scope == scope && row.Currency == currency && row.Unit == unit {
			return row
		}
	}
	t.Fatalf("aggregate has no row %q/%q/%q: %+v", scope, currency, unit, result.Rows)
	return ReconciliationAggregateRow{}
}

func aggregateStatusCount(row ReconciliationAggregateRow, status ReconciliationComparisonStatus) int {
	for _, count := range row.StatusCounts {
		if count.Status == status {
			return count.Count
		}
	}
	return 0
}

func findByComponent(t *testing.T, findings []ReconciliationFinding, direction metering.FlowDirection, component string) ReconciliationFinding {
	t.Helper()
	for _, finding := range findings {
		if finding.Component == component && finding.Direction == direction {
			return finding
		}
	}
	t.Fatalf("no finding for %s/%s: %+v", direction, component, findings)
	return ReconciliationFinding{}
}

// TestReconciliationAggregateGrossNeverNetsOffsets proves offsetting signed
// deltas still produce a non-zero gross absolute discrepancy.
func TestReconciliationAggregateGrossNeverNetsOffsets(t *testing.T) {
	t.Parallel()

	findings := []ReconciliationFinding{
		reconciliationTestFinding(t, "charge-a", "call-1", "USD", "", "100", "105", metering.QualityObserved, ReconciliationStatusDiscrepant),
		reconciliationTestFinding(t, "charge-b", "call-1", "USD", "", "100", "95", metering.QualityObserved, ReconciliationStatusDiscrepant),
	}

	t.Run("beyond tolerance", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("all", ReconciliationToleranceScope{}, toleranceLimit(t, "0.01"), nil))
		result, err := AggregateReconciliationFindings(policy, findings)
		if err != nil {
			t.Fatalf("AggregateReconciliationFindings: %v", err)
		}
		row := aggregateRow(t, result, "call-1", "USD", "")
		assertToleranceAmount(t, "gross absolute discrepancy", row.GrossAbsoluteDiscrepancy, "USD", "10/0")
		assertToleranceAmount(t, "discrepant absolute discrepancy", row.DiscrepantAbsoluteDiscrepancy, "USD", "10/0")
		assertToleranceAmount(t, "net signed discrepancy", row.NetSignedDiscrepancy, "USD", "0/0")
		if row.AffectedCount != 2 {
			t.Fatalf("affected count = %d, want 2", row.AffectedCount)
		}
		if got := aggregateStatusCount(row, ReconciliationStatusDiscrepant); got != 2 {
			t.Fatalf("discrepant count = %d, want 2", got)
		}
		if result.Policy.ID != "tolerance-policy" || result.Policy.Version != "v1" {
			t.Fatalf("policy provenance lost: %+v", result.Policy)
		}
	})

	t.Run("within tolerance still contributes to gross", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("all", ReconciliationToleranceScope{}, toleranceLimit(t, "10"), nil))
		result, err := AggregateReconciliationFindings(policy, findings)
		if err != nil {
			t.Fatalf("AggregateReconciliationFindings: %v", err)
		}
		row := aggregateRow(t, result, "call-1", "USD", "")
		assertToleranceAmount(t, "gross absolute discrepancy", row.GrossAbsoluteDiscrepancy, "USD", "10/0")
		assertToleranceAmount(t, "discrepant absolute discrepancy", row.DiscrepantAbsoluteDiscrepancy, "USD", "0/0")
		if got := aggregateStatusCount(row, ReconciliationStatusWithinTolerance); got != 2 {
			t.Fatalf("within_tolerance count = %d, want 2", got)
		}
	})
}

// TestReconciliationAggregateEstimatedAndIncomparableNeverMatched proves
// estimated and non-comparable evidence never becomes an exact match.
func TestReconciliationAggregateEstimatedAndIncomparableNeverMatched(t *testing.T) {
	t.Parallel()

	findings := []ReconciliationFinding{
		reconciliationTestFinding(t, "estimated-zero", "call-2", "USD", "", "100", "100", metering.QualityEstimated, ReconciliationStatusDiscrepant),
		{
			ID: "incomparable-coverage", Scope: "call-2", Currency: "USD",
			Status: ReconciliationStatusIncomparable, Reason: ReconciliationReasonCoverageMismatch,
		},
		{
			ID: "conflicting-role", Scope: "call-2", Currency: "USD",
			Status: ReconciliationStatusConflict, Reason: ReconciliationReasonConflictingLocal,
		},
	}
	policy := toleranceTestPolicy(toleranceTestRule("all", ReconciliationToleranceScope{}, toleranceLimit(t, "0.01"), nil))
	result, err := AggregateReconciliationFindings(policy, findings)
	if err != nil {
		t.Fatalf("AggregateReconciliationFindings: %v", err)
	}
	row := aggregateRow(t, result, "call-2", "USD", "")
	for _, finding := range result.Findings {
		if finding.ID == "estimated-zero" {
			if finding.EvaluatedStatus != ReconciliationStatusWithinTolerance {
				t.Fatalf("estimated zero delta status = %q, want within_tolerance", finding.EvaluatedStatus)
			}
			if finding.EvaluationReason != ReconciliationReasonEstimatedNotExact {
				t.Fatalf("estimated reason = %q, want estimated_not_exact", finding.EvaluationReason)
			}
		}
	}
	if got := aggregateStatusCount(row, ReconciliationStatusMatched); got != 0 {
		t.Fatalf("matched count = %d, want 0 for estimated/incomparable evidence", got)
	}
	if got := aggregateStatusCount(row, ReconciliationStatusWithinTolerance); got != 1 {
		t.Fatalf("within_tolerance count = %d, want 1", got)
	}
	if got := aggregateStatusCount(row, ReconciliationStatusIncomparable); got != 1 {
		t.Fatalf("incomparable count = %d, want 1", got)
	}
	if got := aggregateStatusCount(row, ReconciliationStatusConflict); got != 1 {
		t.Fatalf("conflict count = %d, want 1", got)
	}
	if len(row.IncomparableIDs) != 1 || row.IncomparableIDs[0] != "incomparable-coverage" {
		t.Fatalf("incomparable ids = %v", row.IncomparableIDs)
	}
	if len(row.ConflictIDs) != 1 || row.ConflictIDs[0] != "conflicting-role" {
		t.Fatalf("conflict ids = %v", row.ConflictIDs)
	}
}

// TestReconciliationFindingsProjectionsPreserveEvidence proves that 12.1 and
// 12.2 results project into findings without dropping values, quality labels,
// source refs or valuation identities.
func TestReconciliationFindingsProjectionsPreserveEvidence(t *testing.T) {
	t.Parallel()

	inputKey := reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	cacheKey := reconciliationKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	outputKey := reconciliationKey(metering.DirectionOutput, metering.ComponentOutputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)

	t.Run("quantity comparison", func(t *testing.T) {
		t.Parallel()
		comparison, err := CompareComponentQuantities(
			reconciliationSide(
				reconciliationObservation(t, "agg-qty-local-input", metering.OriginLocal, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100")),
				reconciliationObservation(t, "agg-qty-local-cache", metering.OriginLocal, reconciliationMeasure(t, cacheKey, metering.QualityObserved, "tok", "7")),
			),
			reconciliationSide(
				reconciliationObservation(t, "agg-qty-provider-input", metering.OriginProvider, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "105")),
				reconciliationObservation(t, "agg-qty-provider-output", metering.OriginProvider, reconciliationMeasure(t, outputKey, metering.QualityObserved, "tok", "50")),
			),
		)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		findings, err := ReconciliationFindingsFromQuantityComparison("b-leg:agg", comparison)
		if err != nil {
			t.Fatalf("ReconciliationFindingsFromQuantityComparison: %v", err)
		}
		if len(findings) != 3 {
			t.Fatalf("findings = %d, want 3: %+v", len(findings), findings)
		}
		discrepant := findByComponent(t, findings, metering.DirectionInput, metering.ComponentInputToken)
		if discrepant.Status != ReconciliationStatusDiscrepant || discrepant.Unit != metering.UnitToken {
			t.Fatalf("input finding = %+v, want discrepant token", discrepant)
		}
		assertToleranceAmount(t, "quantity expected", discrepant.Expected, metering.UnitToken, "100/0")
		assertToleranceAmount(t, "quantity reported", discrepant.Reported, metering.UnitToken, "105/0")
		if discrepant.LocalQuality != metering.QualityObserved {
			t.Fatalf("quality = %q, want observed", discrepant.LocalQuality)
		}
		if len(discrepant.SourceObservationRefs) != 2 {
			t.Fatalf("source refs = %+v, want local+provider", discrepant.SourceObservationRefs)
		}
		missingProvider := findByComponent(t, findings, metering.DirectionInput, metering.ComponentCacheReadInputToken)
		if missingProvider.Status != ReconciliationStatusMissingProvider || missingProvider.Expected == nil || missingProvider.Reported != nil {
			t.Fatalf("cache finding = %+v, want missing_provider with local value only", missingProvider)
		}
		missingLocal := findByComponent(t, findings, metering.DirectionOutput, metering.ComponentOutputToken)
		if missingLocal.Status != ReconciliationStatusMissingLocal || missingLocal.Expected != nil || missingLocal.Reported == nil {
			t.Fatalf("output finding = %+v, want missing_local with provider value only", missingLocal)
		}
	})

	t.Run("monetary comparison", func(t *testing.T) {
		t.Parallel()
		comparison, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
			monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		findings, err := ReconciliationFindingsFromMonetaryComparison("call:agg", comparison)
		if err != nil {
			t.Fatalf("ReconciliationFindingsFromMonetaryComparison: %v", err)
		}
		if len(findings) != 1 {
			t.Fatalf("findings = %d, want 1: %+v", len(findings), findings)
		}
		finding := findings[0]
		if finding.Status != ReconciliationStatusDiscrepant || finding.Currency != "USD" {
			t.Fatalf("finding = %+v, want discrepant USD", finding)
		}
		assertToleranceAmount(t, "monetary expected", finding.Expected, "USD", "1/0")
		assertToleranceAmount(t, "monetary reported", finding.Reported, "USD", "132/2")
		for _, id := range []string{"valuation-e", "valuation-q", "valuation-p"} {
			if !containsString(finding.ValuationIDs, id) {
				t.Fatalf("valuation ids = %v, want %s", finding.ValuationIDs, id)
			}
		}
		if len(finding.SourceObservationRefs) == 0 {
			t.Fatal("monetary finding lost source observation refs")
		}

		missing, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		missingFindings, err := ReconciliationFindingsFromMonetaryComparison("call:agg", missing)
		if err != nil {
			t.Fatalf("ReconciliationFindingsFromMonetaryComparison: %v", err)
		}
		if len(missingFindings) != 1 || missingFindings[0].Status != ReconciliationStatusMissingProvider {
			t.Fatalf("missing findings = %+v, want one missing_provider", missingFindings)
		}
	})

	t.Run("monetary incomparable", func(t *testing.T) {
		t.Parallel()
		provider := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32"))
		provider.CoverageRefs = []metering.ChargeCoverageRef{{
			Ref: metering.ChargeRef{StoreID: reconciliationSubject().StoreID, ObservationID: "charge-observation", Revision: 1, ChargeItemID: "charge-item"}, Relation: metering.CoverageAdditive,
		}}
		comparison, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
			provider,
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		findings, err := ReconciliationFindingsFromMonetaryComparison("call:agg", comparison)
		if err != nil {
			t.Fatalf("ReconciliationFindingsFromMonetaryComparison: %v", err)
		}
		if len(findings) != 1 || findings[0].Status != ReconciliationStatusIncomparable {
			t.Fatalf("findings = %+v, want one incomparable", findings)
		}
	})
}

func containsString(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}

// TestReconciliationAggregateFromProjectedEvidence runs the tolerance policy
// over projected 12.1/12.2 evidence and keeps the aggregate bounded and
// source-complete.
func TestReconciliationAggregateFromProjectedEvidence(t *testing.T) {
	t.Parallel()

	comparison, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
		monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
		monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
	}})
	if err != nil {
		t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
	}
	findings, err := ReconciliationFindingsFromMonetaryComparison("call:integration", comparison)
	if err != nil {
		t.Fatalf("ReconciliationFindingsFromMonetaryComparison: %v", err)
	}

	t.Run("within tolerance", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.40"), nil))
		result, err := AggregateReconciliationFindings(policy, findings)
		if err != nil {
			t.Fatalf("AggregateReconciliationFindings: %v", err)
		}
		row := aggregateRow(t, result, "call:integration", "USD", "")
		if got := aggregateStatusCount(row, ReconciliationStatusWithinTolerance); got != 1 {
			t.Fatalf("within_tolerance count = %d, want 1", got)
		}
		assertToleranceAmount(t, "gross absolute discrepancy", row.GrossAbsoluteDiscrepancy, "USD", "32/2")
		if row.AffectedCount != 1 {
			t.Fatalf("affected count = %d, want 1", row.AffectedCount)
		}
		if len(result.Findings) != 1 || !containsString(result.Findings[0].ValuationIDs, "valuation-p") {
			t.Fatalf("aggregate lost source valuation ids: %+v", result.Findings)
		}
	})

	t.Run("beyond tolerance", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.10"), nil))
		result, err := AggregateReconciliationFindings(policy, findings)
		if err != nil {
			t.Fatalf("AggregateReconciliationFindings: %v", err)
		}
		row := aggregateRow(t, result, "call:integration", "USD", "")
		if got := aggregateStatusCount(row, ReconciliationStatusDiscrepant); got != 1 {
			t.Fatalf("discrepant count = %d, want 1", got)
		}
		assertToleranceAmount(t, "discrepant absolute discrepancy", row.DiscrepantAbsoluteDiscrepancy, "USD", "32/2")
	})
}

// TestReconciliationAggregateValidatesInputsAndBounds covers malformed
// findings and bounded cardinality.
func TestReconciliationAggregateValidatesInputsAndBounds(t *testing.T) {
	t.Parallel()

	policy := toleranceTestPolicy(toleranceTestRule("all", ReconciliationToleranceScope{}, toleranceLimit(t, "0.01"), nil))

	t.Run("unknown status", func(t *testing.T) {
		t.Parallel()
		bad := reconciliationTestFinding(t, "bad-status", "call-3", "USD", "", "1", "1", metering.QualityObserved, "bogus")
		if _, err := AggregateReconciliationFindings(policy, []ReconciliationFinding{bad}); !errors.Is(err, ErrReconciliationAggregateInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationAggregateInvalid", err)
		}
	})

	t.Run("missing unit key", func(t *testing.T) {
		t.Parallel()
		bad := ReconciliationFinding{ID: "no-unit", Scope: "call-3", Status: ReconciliationStatusDiscrepant}
		if _, err := AggregateReconciliationFindings(policy, []ReconciliationFinding{bad}); !errors.Is(err, ErrReconciliationAggregateInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationAggregateInvalid", err)
		}
	})

	t.Run("amount unit mismatch", func(t *testing.T) {
		t.Parallel()
		bad := reconciliationTestFinding(t, "unit-mismatch", "call-3", "USD", "", "1", "1", metering.QualityObserved, ReconciliationStatusDiscrepant)
		other := toleranceAmount(t, "EUR", "1")
		bad.Reported = &other
		if _, err := AggregateReconciliationFindings(policy, []ReconciliationFinding{bad}); !errors.Is(err, ErrReconciliationAggregateInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationAggregateInvalid", err)
		}
	})

	t.Run("finding bound", func(t *testing.T) {
		t.Parallel()
		findings := make([]ReconciliationFinding, 0, MaxReconciliationAggregateFindings+1)
		for i := 0; i <= MaxReconciliationAggregateFindings; i++ {
			findings = append(findings, reconciliationTestFinding(t, fmt.Sprintf("bound-%d", i), "call-3", "USD", "", "1", "1", metering.QualityObserved, ReconciliationStatusMatched))
		}
		if _, err := AggregateReconciliationFindings(policy, findings); !errors.Is(err, ErrReconciliationAggregateBoundExceeded) {
			t.Fatalf("error = %v, want ErrReconciliationAggregateBoundExceeded", err)
		}
	})
}

// TestReconciliationAggregateDeterministicRows proves deterministic ordering
// across scopes, currencies and units independent of input order.
func TestReconciliationAggregateDeterministicRows(t *testing.T) {
	t.Parallel()

	findings := []ReconciliationFinding{
		reconciliationTestFinding(t, "b-usd", "call-b", "USD", "", "10", "11", metering.QualityObserved, ReconciliationStatusDiscrepant),
		reconciliationTestFinding(t, "a-eur", "call-a", "EUR", "", "10", "11", metering.QualityObserved, ReconciliationStatusDiscrepant),
		reconciliationTestFinding(t, "a-token", "call-a", "", "token", "100", "101", metering.QualityObserved, ReconciliationStatusDiscrepant),
		reconciliationTestFinding(t, "a-usd", "call-a", "USD", "", "10", "11", metering.QualityObserved, ReconciliationStatusDiscrepant),
	}
	policy := toleranceTestPolicy(toleranceTestRule("all", ReconciliationToleranceScope{}, toleranceLimit(t, "0"), nil))

	first, err := AggregateReconciliationFindings(policy, findings)
	if err != nil {
		t.Fatalf("AggregateReconciliationFindings: %v", err)
	}
	reversed := make([]ReconciliationFinding, len(findings))
	for i := range findings {
		reversed[i] = findings[len(findings)-1-i]
	}
	second, err := AggregateReconciliationFindings(policy, reversed)
	if err != nil {
		t.Fatalf("AggregateReconciliationFindings: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("aggregate depends on input order:\nfirst=%+v\nsecond=%+v", first, second)
	}
	type rowKey struct{ Scope, Currency, Unit string }
	gotRows := make([]rowKey, 0, len(first.Rows))
	for _, row := range first.Rows {
		gotRows = append(gotRows, rowKey{row.Scope, row.Currency, row.Unit})
	}
	wantRows := []rowKey{
		{"call-a", "", "token"},
		{"call-a", "EUR", ""},
		{"call-a", "USD", ""},
		{"call-b", "USD", ""},
	}
	if !reflect.DeepEqual(gotRows, wantRows) {
		t.Fatalf("row order = %+v, want %+v", gotRows, wantRows)
	}
	for _, row := range first.Rows {
		if row.GrossAbsoluteDiscrepancy == nil || row.NetSignedDiscrepancy == nil {
			t.Fatalf("row %+v lost discrepancy totals", row)
		}
		if strings.TrimSpace(row.Scope) == "" {
			t.Fatalf("row scope must not be empty: %+v", row)
		}
	}
}
