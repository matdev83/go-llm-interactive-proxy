package billing

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// r6RetailConflict is a structurally valid supplier-side evidence conflict. It
// represents two divergent payloads observed under one source identity and is
// never a charge by itself.
func r6RetailConflict() EvidenceConflict {
	return EvidenceConflict{
		Identity:     "source-r6\x00revision:1",
		ExistingHash: "existing-payload",
		IncomingHash: "incoming-payload",
	}
}

func r6RetailMeasureKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentTextToken,
		Unit: metering.UnitToken, SchemaID: "retail.v1",
	}
}

// TestR6UnselectedLoserConflictDoesNotBlockWinnerOnlySelection proves the
// default independent-retail surfaced-winner selector freezes the winner set
// before considering supplier-side conflicts, so an unsurfaced loser that still
// carries an unresolved supplier-charge conflict cannot fence an unambiguous
// winner-only customer settlement.
func TestR6UnselectedLoserConflictDoesNotBlockWinnerOnlySelection(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-loser", "b-winner")
	winner := retailSelectionLeg(t, call.CallID, "b-winner", 3, LegOutcomeWinner, SurfacedYes)
	loser := retailSelectionLeg(t, call.CallID, "b-loser", 2, LegOutcomeLoser, SurfacedNo)
	loser.EvidenceConflicts = []EvidenceConflict{r6RetailConflict()}

	got, err := SelectRetailBLegEvidence(RetailSelectionInput{
		Call: call, Legs: []CallLegUsageRecord{winner, loser}, Policy: policy,
	})
	if err != nil {
		t.Fatalf("winner-only selection blocked by unselected loser conflict: %v", err)
	}
	if len(got.SelectedBLegs) != 1 || got.SelectedBLegs[0].BLegID != "b-winner" {
		t.Fatalf("selection = %+v, want surfaced winner only", got.SelectedBLegs)
	}
	for _, selected := range got.SelectedBLegs {
		if selected.BLegID == "b-loser" {
			t.Fatalf("loser was selected into customer settlement: %+v", got.SelectedBLegs)
		}
	}
}

// TestR6UnselectedLoserConflictDoesNotBlockWinnerOnlyRating drives the public
// rating path and proves the customer charge is computed from the winner's
// complete quantity even when an unselected loser has an unresolved supplier
// conflict.
func TestR6UnselectedLoserConflictDoesNotBlockWinnerOnlyRating(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	key := r6RetailMeasureKey()
	tariff := phase10RetailTariff(t, []economics.RatingRule{phase10RetailLinearRule("text-input", key, "100")})

	build := func(withUnselectedConflict bool) (CallUsageRecord, []CallLegUsageRecord) {
		call := retailSelectionCall(t, policy, "b-winner")
		winner := phase10RetailLeg(t, call.CallID, "b-winner", 3, LegOutcomeWinner, SurfacedYes,
			phase10RetailObservation(t, call.CallID, "b-winner", "winner-usage", metering.OriginProvider, metering.BoundaryBackendIngress,
				phase10RetailMeasure{key: key, quantity: "1"}))
		if !withUnselectedConflict {
			return call, []CallLegUsageRecord{winner}
		}
		call.ExpectedBLegIDs = []string{"b-loser", "b-winner"}
		loser := phase10RetailLeg(t, call.CallID, "b-loser", 2, LegOutcomeLoser, SurfacedNo,
			phase10RetailObservation(t, call.CallID, "b-loser", "loser-usage", metering.OriginProvider, metering.BoundaryBackendIngress,
				phase10RetailMeasure{key: key, quantity: "5"}))
		loser.EvidenceConflicts = []EvidenceConflict{r6RetailConflict()}
		return call, []CallLegUsageRecord{winner, loser}
	}

	controlCall, controlLegs := build(false)
	control, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: controlCall, Legs: controlLegs, Policy: policy, Tariff: tariff,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: controlCall.AccountID},
	})
	if err != nil {
		t.Fatalf("control winner-only rating: %v", err)
	}

	call, legs := build(true)
	got, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: legs, Policy: policy, Tariff: tariff,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("winner-only rating blocked by unselected loser conflict: %v", err)
	}
	if got.CustomerCharge != control.CustomerCharge {
		t.Fatalf("customer charge = %+v, want winner-only control %+v", got.CustomerCharge, control.CustomerCharge)
	}
}

// TestR6SelectedWinnerConflictStillFences proves selected-evidence validation
// is not weakened: a conflict on the leg that actually reaches customer
// settlement remains a hard fence.
func TestR6SelectedWinnerConflictStillFences(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-winner")
	winner := retailSelectionLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes)
	winner.EvidenceConflicts = []EvidenceConflict{r6RetailConflict()}

	_, err := SelectRetailBLegEvidence(RetailSelectionInput{
		Call: call, Legs: []CallLegUsageRecord{winner}, Policy: policy,
	})
	if !errors.Is(err, ErrRetailSelectionUntrusted) {
		t.Fatalf("selected winner conflict error = %v, want %v", err, ErrRetailSelectionUntrusted)
	}
}

// TestR6RetryInclusivePolicyStillFencesConflictingLoser proves a policy that
// explicitly selects the retry/loser leg keeps its stricter dependency: the
// conflict on that now-selected leg fences the whole selection.
func TestR6RetryInclusivePolicyStillFencesConflictingLoser(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-loser", "b-winner")
	winner := retailSelectionLeg(t, call.CallID, "b-winner", 3, LegOutcomeWinner, SurfacedYes)
	loser := retailSelectionLeg(t, call.CallID, "b-loser", 2, LegOutcomeLoser, SurfacedNo)
	loser.EvidenceConflicts = []EvidenceConflict{r6RetailConflict()}

	_, err := SelectRetailBLegEvidence(RetailSelectionInput{
		Call: call, Legs: []CallLegUsageRecord{winner, loser}, Policy: policy,
	})
	if !errors.Is(err, ErrRetailSelectionUntrusted) {
		t.Fatalf("retry-inclusive conflicting loser error = %v, want %v", err, ErrRetailSelectionUntrusted)
	}
}

// TestR6CostPassThroughPolicyStillFencesConflictingLoser proves the explicit
// cost-pass-through basis, which selects all attributable legs, keeps its
// stricter dependency on the conflicting loser's evidence.
func TestR6CostPassThroughPolicyStillFencesConflictingLoser(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisCostPassThrough)
	retail := policy.Retail.Clone()
	retail.CostPassThrough = &CostPassThroughPolicy{
		MissingCost: CostPassThroughMissingCostPending,
		SafeBound:   &Money{Nano: 100, Currency: "USD"},
	}
	policy.Retail = &retail
	call := retailSelectionCall(t, policy, "b-loser", "b-winner")
	winner := retailSelectionLeg(t, call.CallID, "b-winner", 3, LegOutcomeWinner, SurfacedYes)
	loser := retailSelectionLeg(t, call.CallID, "b-loser", 2, LegOutcomeLoser, SurfacedNo)
	loser.EvidenceConflicts = []EvidenceConflict{r6RetailConflict()}

	_, err := SelectRetailBLegEvidence(RetailSelectionInput{
		Call: call, Legs: []CallLegUsageRecord{winner, loser}, Policy: policy,
	})
	if !errors.Is(err, ErrRetailSelectionUntrusted) {
		t.Fatalf("cost-pass-through conflicting loser error = %v, want %v", err, ErrRetailSelectionUntrusted)
	}
}

// TestR6CrossCallAndTenantContaminationStillRejected proves global lineage and
// isolation ambiguity still fails even though supplier-only conflict on an
// unselected loser is now permitted.
func TestR6CrossCallAndTenantContaminationStillRejected(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)

	t.Run("cross-call leg", func(t *testing.T) {
		t.Parallel()
		call := retailSelectionCall(t, policy, "b-winner", "b-foreign")
		winner := retailSelectionLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes)
		foreign := mustBillingCallID(t)
		foreignLeg := retailSelectionLeg(t, foreign, "b-foreign", 2, LegOutcomeLoser, SurfacedNo)

		_, err := SelectRetailBLegEvidence(RetailSelectionInput{
			Call: call, Legs: []CallLegUsageRecord{winner, foreignLeg}, Policy: policy,
		})
		if !errors.Is(err, ErrRetailSelectionScopeMismatch) {
			t.Fatalf("cross-call leg error = %v, want %v", err, ErrRetailSelectionScopeMismatch)
		}
	})

	t.Run("tenant contamination", func(t *testing.T) {
		t.Parallel()
		call := retailSelectionCall(t, policy, "b-loser", "b-winner")
		winner := retailSelectionLeg(t, call.CallID, "b-winner", 3, LegOutcomeWinner, SurfacedYes)
		loser := retailSelectionLeg(t, call.CallID, "b-loser", 2, LegOutcomeLoser, SurfacedNo)
		for i := range loser.Observations {
			loser.Observations[i].Subject.TenantID = "tenant-b"
			loser.Observations[i].Correlation.TenantID = "tenant-b"
		}

		_, err := SelectRetailBLegEvidence(RetailSelectionInput{
			Call: call, Legs: []CallLegUsageRecord{winner, loser}, Policy: policy, TenantID: "tenant-a",
		})
		if !errors.Is(err, ErrRetailSelectionScopeMismatch) {
			t.Fatalf("tenant contamination error = %v, want %v", err, ErrRetailSelectionScopeMismatch)
		}
	})
}

// TestR6UnselectedLoserEvidenceRetainedForSupplierReconciliation proves the
// selector neither deletes nor quarantines the loser's source record: the
// unresolved conflict stays on the input record for independent supplier
// reconciliation while the customer selection excludes it.
func TestR6UnselectedLoserEvidenceRetainedForSupplierReconciliation(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-loser", "b-winner")
	winner := retailSelectionLeg(t, call.CallID, "b-winner", 3, LegOutcomeWinner, SurfacedYes)
	loser := retailSelectionLeg(t, call.CallID, "b-loser", 2, LegOutcomeLoser, SurfacedNo)
	loser.EvidenceConflicts = []EvidenceConflict{r6RetailConflict()}
	legs := []CallLegUsageRecord{winner, loser}
	before := legs[1].Clone()

	got, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatalf("winner-only selection: %v", err)
	}
	if len(legs[1].EvidenceConflicts) != 1 || !reflect.DeepEqual(before.EvidenceConflicts, legs[1].EvidenceConflicts) {
		t.Fatalf("loser supplier conflict was not retained: %+v", legs[1].EvidenceConflicts)
	}
	if !legs[1].HasV2Evidence() {
		t.Fatal("loser V2 source evidence was quarantined")
	}
	for _, selected := range got.SelectedBLegs {
		if selected.BLegID == "b-loser" {
			t.Fatalf("loser reached customer selection: %+v", got.SelectedBLegs)
		}
	}
}
