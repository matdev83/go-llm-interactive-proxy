package billing

// B1 (PR #659 adversarial repair R7 prerequisite) finding-3 RED vector: the
// production RateSelectedRetailBLegs seam must surface the frozen-schema
// overlap conflict through the joined retail error channel while preserving an
// independent trusted-scope fixed fee and an unrelated selected B-leg quantity.
// It exercises the real selection mask, B-leg grouping and errors.Join path,
// not the operator ReferenceRater helper used by the other B1 vectors.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const b1RetailSchemaID = "b1.retail.overlap.v1"

func b1RetailTariffWithSchemas(t *testing.T, rules []economics.RatingRule, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.BuildTariffSnapshotWithSchemas(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing", Version: "v3"}, RaterID: "reference"},
		"USD", rules, schemas,
	)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	return tariff
}

func TestB1RetailSelectedBLegsConflictKeepsFeeAndUnrelatedLeg(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-conflict", "b-unrelated")
	call.SubmissionID = "submission-b1-retail"

	parent := metering.ComponentKey{Direction: metering.DirectionInput, Component: "vendor:aggregate_total", Unit: metering.UnitToken, SchemaID: b1RetailSchemaID}
	child := metering.ComponentKey{Direction: metering.DirectionInput, Component: "vendor:included_part", Unit: metering.UnitToken, SchemaID: b1RetailSchemaID}
	unrelated := metering.ComponentKey{Direction: metering.DirectionInput, Component: "vendor:unrelated_part", Unit: metering.UnitToken, SchemaID: b1RetailSchemaID}

	conflictObs := phase10RetailObservation(t, call.CallID, "b-conflict", "retail-conflict", metering.OriginLocal, metering.BoundaryBackendIngress,
		phase10RetailMeasure{key: parent, quantity: "10"},
		phase10RetailMeasure{key: child, quantity: "4"},
	)
	unrelatedObs := phase10RetailObservation(t, call.CallID, "b-unrelated", "retail-unrelated", metering.OriginLocal, metering.BoundaryBackendIngress,
		phase10RetailMeasure{key: unrelated, quantity: "5"},
	)
	conflictLeg := phase10RetailLeg(t, call.CallID, "b-conflict", 1, LegOutcomeFailed, SurfacedNo, conflictObs)
	unrelatedLeg := phase10RetailLeg(t, call.CallID, "b-unrelated", 2, LegOutcomeFailed, SurfacedNo, unrelatedObs)
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{conflictLeg, unrelatedLeg}, Policy: policy})
	if err != nil {
		t.Fatalf("SelectRetailBLegEvidence: %v", err)
	}
	if len(selection.SelectedBLegs) != 2 {
		t.Fatalf("selected B-legs = %+v, want both", selection.SelectedBLegs)
	}

	tariff := b1RetailTariffWithSchemas(t, []economics.RatingRule{
		phase10RetailLinearRule("parent-rate", parent, "1"),
		phase10RetailLinearRule("child-rate", child, "2"),
		phase10RetailLinearRule("unrelated-rate", unrelated, "3"),
		phase10RetailFixedRule("call-fee", economics.FixedFeeScopeCall, "4"),
	}, []metering.ComponentSchema{{
		ID: b1RetailSchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: parent, Child: child}},
	}})

	result, rateErr := RateSelectedRetailBLegs(ctx, RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{conflictLeg, unrelatedLeg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if !errors.Is(rateErr, ErrSchemaOverlapConflict) {
		t.Fatalf("retail conflict error=%v, want joined ErrSchemaOverlapConflict; valuation=%+v", rateErr, result.Valuation)
	}
	if !errors.Is(rateErr, ErrRetailRateIncomplete) {
		t.Fatalf("retail conflict error=%v, want joined ErrRetailRateIncomplete", rateErr)
	}
	if result.Valuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("retail completeness=%q, want conflict", result.Valuation.Completeness)
	}

	var unrelatedAmount, feeAmount *metering.Decimal
	var conflictPayable, feePayable bool
	for _, line := range result.Valuation.Lines {
		if line.Component != nil && line.Amount != nil && line.Amount.CanonicalString() != "0/0" {
			switch line.Component.Component {
			case parent.Component, child.Component:
				conflictPayable = true
			case unrelated.Component:
				unrelatedAmount = line.Amount
			}
		}
		if line.FixedFee != nil && line.Amount != nil {
			feePayable = true
			feeAmount = line.Amount
		}
	}
	if conflictPayable {
		t.Fatalf("overlapping retail quantity lines must not be payable, got %+v", result.Valuation.Lines)
	}
	if unrelatedAmount == nil || unrelatedAmount.CanonicalString() != "15/0" {
		t.Fatalf("unrelated selected B-leg quantity=%v, want 15 in %+v", unrelatedAmount, result.Valuation.Lines)
	}
	if !feePayable || feeAmount == nil || feeAmount.CanonicalString() != "4/0" {
		t.Fatalf("independent fixed fee=%v payable=%v, want 4 in %+v", feeAmount, feePayable, result.Valuation.Lines)
	}
	if len(result.Valuation.Totals) != 1 || result.Valuation.Totals[0].Amount == nil || result.Valuation.Totals[0].Amount.CanonicalString() != "19/0" {
		t.Fatalf("retail payable total=%+v, want one 19 total", result.Valuation.Totals)
	}
}
