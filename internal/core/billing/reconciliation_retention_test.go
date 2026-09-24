package billing

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// retentionTestResult builds one full 12.1/12.2/12.3 result for durable
// retention with unsorted reference slices so canonicalization is exercised.
func retentionTestResult(t *testing.T, id string, revision uint64) ReconciliationRetentionResult {
	t.Helper()

	inputKey := reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	quantity, err := CompareComponentQuantities(
		reconciliationSide(reconciliationObservation(t, "retention-qty-local", metering.OriginLocal,
			reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100"))),
		reconciliationSide(reconciliationObservation(t, "retention-qty-provider", metering.OriginProvider,
			reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "110"))),
	)
	if err != nil {
		t.Fatalf("CompareComponentQuantities: %v", err)
	}

	monetary, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
		monetaryTestValuation(t, "retention-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
		monetaryTestValuation(t, "retention-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		monetaryTestValuation(t, "retention-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
	}})
	if err != nil {
		t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
	}

	absolute := toleranceLimit(t, "0.40")
	policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, absolute, nil))
	findings, err := ReconciliationFindingsFromMonetaryComparison("call:retention", monetary)
	if err != nil {
		t.Fatalf("ReconciliationFindingsFromMonetaryComparison: %v", err)
	}
	aggregate, err := AggregateReconciliationFindings(policy, findings)
	if err != nil {
		t.Fatalf("AggregateReconciliationFindings: %v", err)
	}

	localRef, err := reconciliationObservation(t, "retention-qty-local", metering.OriginLocal,
		reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100")).Ref(reconciliationSubject().StoreID)
	if err != nil {
		t.Fatalf("local ref: %v", err)
	}
	providerRef, err := reconciliationObservation(t, "retention-qty-provider", metering.OriginProvider,
		reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "110")).Ref(reconciliationSubject().StoreID)
	if err != nil {
		t.Fatalf("provider ref: %v", err)
	}

	return ReconciliationRetentionResult{
		SchemaVersion: ReconciliationRetentionSchemaVersionV1,
		ID:            id, ResultRevision: revision,
		Subject: reconciliationSubject(), Scope: "call:retention",
		Policy:            VersionRef{ID: policy.Ref.ID, Version: policy.Ref.Version},
		InputSetHash:      strings.Repeat("a", 64),
		LocalInputHash:    strings.Repeat("b", 64),
		ProviderInputHash: strings.Repeat("c", 64),
		ValuationIDs:      []string{"retention-p", "retention-e", "retention-q"},
		ObservationRefs:   []metering.ObservationRef{providerRef, localRef},
		Quantity:          &quantity,
		Monetary:          &monetary,
		Aggregate:         &aggregate,
		Diagnostics:       []ReconciliationRetentionDiagnostic{{Code: "suspected_pricing_difference", Detail: "residual without provider rate detail"}},
		CreatedAt:         time.Unix(1_700_002_000, 0).UTC(),
	}
}

// TestReconciliationRetentionCanonicalRoundTrip proves the full result is
// serialized deterministically, preserves exact decimals and every source
// reference, and parses back canonically.
func TestReconciliationRetentionCanonicalRoundTrip(t *testing.T) {
	t.Parallel()

	result := retentionTestResult(t, "reconciliation-retention-1", 1)
	canonical, err := result.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	payload := string(canonical)
	for _, fragment := range []string{
		`"result_revision":1`,
		`"gross_absolute_discrepancy"`,
		`"coefficient":"132","scale":2`,
		`"signed_delta"`,
		`"valuation_ids"`,
		`"observation_refs"`,
		`"suspected_pricing_difference"`,
	} {
		if !strings.Contains(payload, fragment) {
			t.Fatalf("canonical payload missing %s:\n%s", fragment, payload)
		}
	}

	parsed, err := ParseReconciliationRetentionResult(canonical)
	if err != nil {
		t.Fatalf("ParseReconciliationRetentionResult: %v", err)
	}
	reparsed, err := parsed.CanonicalJSON()
	if err != nil {
		t.Fatalf("reparsed CanonicalJSON: %v", err)
	}
	if !bytes.Equal(canonical, reparsed) {
		t.Fatalf("round trip changed canonical payload:\n%s\n%s", canonical, reparsed)
	}
	if parsed.ValuationIDs[0] != "retention-e" || parsed.ValuationIDs[1] != "retention-p" || parsed.ValuationIDs[2] != "retention-q" {
		t.Fatalf("valuation ids not canonically ordered: %+v", parsed.ValuationIDs)
	}
	if parsed.Monetary == nil || parsed.Quantity == nil || parsed.Aggregate == nil {
		t.Fatalf("full result evidence was lost: %+v", parsed)
	}
	if parsed.Aggregate.Rows[0].GrossAbsoluteDiscrepancy == nil {
		t.Fatalf("aggregate totals were lost: %+v", parsed.Aggregate.Rows)
	}
}

// TestReconciliationRetentionCanonicalOrderIndependent proves slice order is
// not part of the durable identity.
func TestReconciliationRetentionCanonicalOrderIndependent(t *testing.T) {
	t.Parallel()

	result := retentionTestResult(t, "reconciliation-retention-2", 2)
	want, err := result.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	reordered := result
	reordered.ValuationIDs = []string{"retention-q", "retention-p", "retention-e"}
	reordered.ObservationRefs = []metering.ObservationRef{result.ObservationRefs[1], result.ObservationRefs[0]}
	reordered.Diagnostics = nil
	got, err := reordered.CanonicalJSON()
	if err != nil {
		t.Fatalf("reordered CanonicalJSON: %v", err)
	}
	if bytes.Equal(want, got) {
		t.Fatal("test setup did not change the result")
	}
	// Restoring the canonical diagnostics and comparing proves the sorting path
	// is stable for references; diagnostics are semantic evidence and are
	// intentionally part of identity.
	reordered.Diagnostics = result.Diagnostics
	got, err = reordered.CanonicalJSON()
	if err != nil {
		t.Fatalf("reordered CanonicalJSON: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("reference ordering changed canonical identity:\nwant %s\ngot  %s", want, got)
	}
}

// TestReconciliationRetentionValidation rejects malformed identity, context
// and evidence before persistence.
func TestReconciliationRetentionValidation(t *testing.T) {
	t.Parallel()

	base := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return retentionTestResult(t, "reconciliation-retention-3", 3)
	}

	cases := []struct {
		name   string
		mutate func(*ReconciliationRetentionResult)
	}{
		{"schema version", func(r *ReconciliationRetentionResult) { r.SchemaVersion = 99 }},
		{"missing id", func(r *ReconciliationRetentionResult) { r.ID = "" }},
		{"missing revision", func(r *ReconciliationRetentionResult) { r.ResultRevision = 0 }},
		{"missing subject store", func(r *ReconciliationRetentionResult) { r.Subject.StoreID = "" }},
		{"missing evidence", func(r *ReconciliationRetentionResult) { r.Quantity, r.Monetary, r.Aggregate = nil, nil, nil }},
		{"bad hash", func(r *ReconciliationRetentionResult) { r.InputSetHash = "not-a-hash" }},
		{"bad diagnostic code", func(r *ReconciliationRetentionResult) {
			r.Diagnostics = []ReconciliationRetentionDiagnostic{{Code: ""}}
		}},
		{"bad created at", func(r *ReconciliationRetentionResult) { r.CreatedAt = time.Time{} }},
		{"policy mismatch", func(r *ReconciliationRetentionResult) { r.Policy.Version = "v-other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := base(t)
			tc.mutate(&result)
			if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
			}
			if _, err := result.CanonicalJSON(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("CanonicalJSON error = %v, want ErrInvalidReconciliationRetention", err)
			}
		})
	}

	t.Run("monetary subject mismatch", func(t *testing.T) {
		result := base(t)
		result.Monetary.Subject.BLegID = "b-leg-other"
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})
}

// TestReconciliationRetentionParseRejectsNonCanonicalJSON proves the stored
// canonical bytes are verified rather than trusted through the hash alone.
func TestReconciliationRetentionParseRejectsNonCanonicalJSON(t *testing.T) {
	t.Parallel()

	result := retentionTestResult(t, "reconciliation-retention-4", 4)
	canonical, err := result.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if _, err := ParseReconciliationRetentionResult(append(append([]byte(nil), canonical...), []byte(" ")...)); !errors.Is(err, ErrInvalidReconciliationRetention) {
		t.Fatalf("trailing data error = %v, want ErrInvalidReconciliationRetention", err)
	}
	if _, err := ParseReconciliationRetentionResult([]byte(`{"id":"x"}`)); !errors.Is(err, ErrInvalidReconciliationRetention) {
		t.Fatalf("truncated error = %v, want ErrInvalidReconciliationRetention", err)
	}
}

// TestReconciliationRetentionDeepValidationAndScopeBinding proves nested
// malformed values are rejected and every retained reference is bound to the
// parent store and subject before canonicalization.
func TestReconciliationRetentionDeepValidationAndScopeBinding(t *testing.T) {
	t.Parallel()

	base := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		return retentionTestResult(t, "reconciliation-retention-deep", 1)
	}
	foreignRef := metering.ObservationRef{StoreID: "store-other", ObservationID: "foreign-observation", Revision: 1, PayloadHash: strings.Repeat("a", 64)}

	cases := map[string]func(*ReconciliationRetentionResult){
		"foreign top-level observation ref": func(r *ReconciliationRetentionResult) {
			r.ObservationRefs[0] = foreignRef
		},
		"foreign quantity evidence ref": func(r *ReconciliationRetentionResult) {
			r.Quantity.Items[0].Local[0].Observation = foreignRef
		},
		"foreign monetary valuation subject": func(r *ReconciliationRetentionResult) {
			r.Monetary.Valuations[0].Valuation.Subject.BLegID = "b-leg-other"
		},
		"foreign monetary valuation store": func(r *ReconciliationRetentionResult) {
			r.Monetary.Valuations[0].Valuation.Subject.StoreID = "store-other"
		},
		"foreign coverage ref": func(r *ReconciliationRetentionResult) {
			r.CoverageRefs = []metering.ChargeCoverageRef{{
				Ref: metering.ChargeRef{StoreID: "store-other", ObservationID: "charge-observation", Revision: 1, ChargeItemID: "charge-item"}, Relation: metering.CoverageAdditive,
			}}
		},
		"foreign aggregate finding ref": func(r *ReconciliationRetentionResult) {
			r.Aggregate.Findings[0].SourceObservationRefs = []metering.ObservationRef{foreignRef}
		},
		"unknown quantity item status": func(r *ReconciliationRetentionResult) {
			r.Quantity.Items[0].Status = "bogus"
		},
		"unknown quantity evidence quality": func(r *ReconciliationRetentionResult) {
			r.Quantity.Items[0].Local[0].Quality = "bogus"
		},
		"unpaired quantity delta": func(r *ReconciliationRetentionResult) {
			r.Quantity.Items[0].SignedDelta = nil
		},
		"delta on missing status": func(r *ReconciliationRetentionResult) {
			r.Quantity.Items[0].Status = ReconciliationStatusMissingLocal
		},
		"unknown monetary term status": func(r *ReconciliationRetentionResult) {
			r.Monetary.Rows[0].EndToEndCostDelta.Status = "bogus"
		},
		"amount on missing term": func(r *ReconciliationRetentionResult) {
			r.Monetary.Rows[0].ReportedPriceResidual = MonetaryDiscrepancyTerm{Status: MonetaryTermMissing, Reason: MonetaryReasonMissingP, Amount: operatorCostTestAmount(t, "USD", "1")}
		},
		"noncanonical monetary amount": func(r *ReconciliationRetentionResult) {
			noncanonical := MonetaryExactAmount{Currency: "USD", Numerator: "2", Denominator: "2"}
			r.Monetary.Rows[0].EndToEndCostDelta.Amount = &noncanonical
		},
		"negative aggregate count": func(r *ReconciliationRetentionResult) {
			r.Aggregate.Rows[0].AffectedCount = -1
		},
		"aggregate amount unit mismatch": func(r *ReconciliationRetentionResult) {
			other := operatorCostTestAmount(t, "EUR", "1")
			r.Aggregate.Rows[0].GrossAbsoluteDiscrepancy = other
		},
		"duplicate quantity item": func(r *ReconciliationRetentionResult) {
			duplicate := r.Quantity.Items[0]
			duplicate.Status = ReconciliationStatusMissingProvider
			duplicate.Provider = nil
			duplicate.SignedDelta = nil
			duplicate.AbsoluteDelta = nil
			r.Quantity.Items = append(r.Quantity.Items, duplicate)
		},
		"duplicate monetary row": func(r *ReconciliationRetentionResult) {
			r.Monetary.Rows = append(r.Monetary.Rows, r.Monetary.Rows[0])
		},
		"duplicate monetary role": func(r *ReconciliationRetentionResult) {
			r.Monetary.Valuations = append(r.Monetary.Valuations, r.Monetary.Valuations[0])
		},
		"duplicate aggregate finding": func(r *ReconciliationRetentionResult) {
			r.Aggregate.Findings = append(r.Aggregate.Findings, r.Aggregate.Findings[0])
		},
		"duplicate aggregate row": func(r *ReconciliationRetentionResult) {
			r.Aggregate.Rows = append(r.Aggregate.Rows, r.Aggregate.Rows[0])
		},
		"duplicate quantity evidence": func(r *ReconciliationRetentionResult) {
			r.Quantity.Items[0].Local = append(r.Quantity.Items[0].Local, r.Quantity.Items[0].Local[0])
		},
		"unknown aggregate finding status": func(r *ReconciliationRetentionResult) {
			r.Aggregate.Findings[0].Status = "bogus"
		},
		"unknown aggregate status count": func(r *ReconciliationRetentionResult) {
			r.Aggregate.Rows[0].StatusCounts = []ReconciliationStatusCount{{Status: "bogus", Count: 1}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := base(t)
			mutate(&result)
			if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("Validate error = %v, want ErrInvalidReconciliationRetention", err)
			}
			if _, err := result.CanonicalJSON(); !errors.Is(err, ErrInvalidReconciliationRetention) {
				t.Fatalf("CanonicalJSON error = %v, want ErrInvalidReconciliationRetention", err)
			}
		})
	}

	t.Run("noncanonical nested amount is typed", func(t *testing.T) {
		t.Parallel()
		result := base(t)
		noncanonical := MonetaryExactAmount{Currency: "USD", Numerator: "2", Denominator: "2"}
		result.Monetary.Rows[0].EndToEndCostDelta.Amount = &noncanonical
		if err := result.Validate(); !errors.Is(err, ErrInvalidMonetaryExactAmount) {
			t.Fatalf("Validate error = %v, want ErrInvalidMonetaryExactAmount", err)
		}
	})

	t.Run("valid result passes", func(t *testing.T) {
		t.Parallel()
		if err := base(t).Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
}

// TestReconciliationRetentionCanonicalPermutations proves canonical identity is
// independent of input order for distinct nested entries with tied prefixes and
// that duplicate/conflicting entries fail closed.
func TestReconciliationRetentionCanonicalPermutations(t *testing.T) {
	t.Parallel()

	withExtraEntries := func(t *testing.T) ReconciliationRetentionResult {
		t.Helper()
		result := retentionTestResult(t, "reconciliation-retention-permutation", 2)

		secondKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
		value := metering.Decimal{Coefficient: "50"}
		result.Quantity.Items = append(result.Quantity.Items, ComponentQuantityComparisonItem{
			Key: secondKey, Status: ReconciliationStatusMissingProvider,
			Local: []ReconciliationQuantityEvidence{{
				Key: secondKey, Quality: metering.QualityObserved, Value: &value,
				Observation: metering.ObservationRef{StoreID: reconciliationSubject().StoreID, ObservationID: "retention-second-local", Revision: 1, PayloadHash: strings.Repeat("b", 64)},
			}},
		})
		// Add a genuinely derived second currency row: give Q an extra EUR
		// total and re-run the authoritative producer over the retained
		// valuations. EUR is present only on Q, so its terms are
		// missing/currency_missing rather than an invented underived row.
		valuations := make([]economics.Valuation, 0, len(result.Monetary.Valuations))
		for i := range result.Monetary.Valuations {
			valuation := result.Monetary.Valuations[i].Valuation
			if result.Monetary.Valuations[i].Role == MonetaryRoleQ {
				totals := append([]economics.CurrencyTotal(nil), valuation.Totals...)
				valuation.Totals = append(totals, monetaryDecimalTotal(t, "EUR", "1.10"))
			}
			valuations = append(valuations, valuation)
		}
		recomputed, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: valuations})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		result.Monetary = &recomputed
		// Rebuild the aggregate from the authoritative producers over the
		// updated quantity and monetary evidence, so every retained finding
		// stays a genuine producer projection of its source comparison.
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.40"), nil))
		result.Quantity.Status, result.Quantity.Complete = summarizeComponentQuantityComparison(result.Quantity.Items)
		quantityFindings, err := ReconciliationFindingsFromQuantityComparison(result.Scope, *result.Quantity)
		if err != nil {
			t.Fatalf("ReconciliationFindingsFromQuantityComparison: %v", err)
		}
		monetaryFindings, err := ReconciliationFindingsFromMonetaryComparison(result.Scope, recomputed)
		if err != nil {
			t.Fatalf("ReconciliationFindingsFromMonetaryComparison: %v", err)
		}
		aggregate, err := AggregateReconciliationFindings(policy, append(quantityFindings, monetaryFindings...))
		if err != nil {
			t.Fatalf("AggregateReconciliationFindings: %v", err)
		}
		result.Aggregate = &aggregate
		extraRef := metering.ObservationRef{StoreID: reconciliationSubject().StoreID, ObservationID: "retention-extra-ref", Revision: 1, PayloadHash: strings.Repeat("c", 64)}
		result.ObservationRefs = append(result.ObservationRefs, extraRef)
		result.ValuationIDs = append(result.ValuationIDs, "aaa-extra-valuation")
		result.Monetary.Status, result.Monetary.Reason = monetaryDiscrepancyRollup(result.Monetary)
		return result
	}

	t.Run("true permutation has identical identity", func(t *testing.T) {
		t.Parallel()
		first := withExtraEntries(t)
		canonicalFirst, err := first.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON(first): %v", err)
		}
		permuted := withExtraEntries(t)
		permuted.Quantity.Items[0], permuted.Quantity.Items[1] = permuted.Quantity.Items[1], permuted.Quantity.Items[0]
		permuted.Monetary.Rows[0], permuted.Monetary.Rows[1] = permuted.Monetary.Rows[1], permuted.Monetary.Rows[0]
		permuted.Aggregate.Findings[0], permuted.Aggregate.Findings[1] = permuted.Aggregate.Findings[1], permuted.Aggregate.Findings[0]
		permuted.ObservationRefs[0], permuted.ObservationRefs[2] = permuted.ObservationRefs[2], permuted.ObservationRefs[0]
		permuted.ValuationIDs[0], permuted.ValuationIDs[3] = permuted.ValuationIDs[3], permuted.ValuationIDs[0]
		canonicalPermuted, err := permuted.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON(permuted): %v", err)
		}
		if !bytes.Equal(canonicalFirst, canonicalPermuted) {
			t.Fatalf("permutation changed canonical bytes:\n%s\n%s", canonicalFirst, canonicalPermuted)
		}
		if first.Fingerprint() != permuted.Fingerprint() {
			t.Fatalf("permutation changed fingerprint: %s vs %s", first.Fingerprint(), permuted.Fingerprint())
		}
	})

	t.Run("tied prefix with different payload fails closed", func(t *testing.T) {
		t.Parallel()
		result := withExtraEntries(t)
		conflicting := result.Quantity.Items[0]
		conflicting.Status = ReconciliationStatusMissingProvider
		conflicting.Provider = nil
		conflicting.SignedDelta = nil
		conflicting.AbsoluteDelta = nil
		result.Quantity.Items = append(result.Quantity.Items, conflicting)
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})

	t.Run("conflicting evidence for one observation fails closed", func(t *testing.T) {
		t.Parallel()
		result := withExtraEntries(t)
		conflicting := result.Quantity.Items[0].Local[0]
		conflicting.Quality = metering.QualityEstimated
		result.Quantity.Items[0].Local = append(result.Quantity.Items[0].Local, conflicting)
		if err := result.Validate(); !errors.Is(err, ErrInvalidReconciliationRetention) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationRetention", err)
		}
	})
}

// TestReconciliationRetentionRejectsOversizePayload proves the bounded
// payload contract fails closed before persistence.
func TestReconciliationRetentionRejectsOversizePayload(t *testing.T) {
	t.Parallel()

	result := retentionTestResult(t, "reconciliation-retention-5", 5)
	items := make([]ComponentQuantityComparisonItem, 0, 4096)
	for i := range MaxReconciliationRetentionObservationRefs {
		key := metering.ComponentKey{Direction: metering.DirectionNone, Component: fmt.Sprintf("synthetic_metric_%d", i), Unit: metering.UnitCount, SchemaID: "retention.oversize.v1"}
		items = append(items, ComponentQuantityComparisonItem{
			Key: key, Status: ReconciliationStatusMissingProvider,
			Local: []ReconciliationQuantityEvidence{{
				Key: key, Quality: metering.QualityObserved,
				Value:       &metering.Decimal{Coefficient: "1", Scale: 0},
				Observation: metering.ObservationRef{StoreID: reconciliationSubject().StoreID, ObservationID: fmt.Sprintf("retention-oversize-%d", i), Revision: 1, PayloadHash: strings.Repeat("d", 64)},
			}},
		})
	}
	result.Quantity = &ComponentQuantityComparison{Status: ReconciliationStatusPartial, Items: items}
	if _, err := result.CanonicalJSON(); !errors.Is(err, ErrInvalidReconciliationRetention) {
		t.Fatalf("error = %v, want ErrInvalidReconciliationRetention for oversize payload", err)
	}
}
