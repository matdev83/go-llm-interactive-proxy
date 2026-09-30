package billing_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestUncertaintyLegacyMoneyAndKnownConflictControls(t *testing.T) {
	t.Parallel()

	// Vector 29 is the legacy unversioned 100/20/30 subset-sibling baseline:
	// additive charge remains complete at 50 with no error. The other vectors
	// keep representative pre-existing overlap, quantity, cover and ambiguity
	// failures typed while advisory output is absent.
	wanted := map[int]struct{}{1: {}, 10: {}, 19: {}, 20: {}, 29: {}}
	for _, vector := range accVectors() {
		if _, ok := wanted[vector.number]; !ok {
			continue
		}
		vector := vector
		delete(wanted, vector.number)
		t.Run(accNumber(vector.number)+"_"+vector.name, func(t *testing.T) {
			t.Parallel()
			testCase := vector.build(t)
			resolved := f356Schema(t, "uncertainty-legacy-"+accNumber(vector.number), testCase.rules, testCase.relationships)
			accCheckSeams(t, vector, resolved, testCase.observations)
		})
	}
	if len(wanted) != 0 {
		t.Fatalf("missing baseline acceptance vectors: %v", wanted)
	}
}

func TestUncertaintyLegacyBranchRootAdvisoryTextIsPinned(t *testing.T) {
	t.Parallel()

	root := r7Key("vendor:uncertainty_branch_root")
	branchB := r7Key("vendor:uncertainty_branch_b")
	branchC := r7Key("vendor:uncertainty_branch_c")
	contradictionY := r7Key("vendor:uncertainty_contradiction_y")
	contradictionX := r7Key("vendor:uncertainty_contradiction_x")
	relationships := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: root, Child: branchB},
		{Kind: metering.RelationshipPartition, Parent: root, Child: branchC},
		{Kind: metering.RelationshipSubset, Parent: contradictionY, Child: contradictionX},
	}
	rules := []economics.RatingRule{
		b1Rule(t, "legacy-branch-b-rate", branchB, "1"),
		b1Rule(t, "legacy-branch-c-rate", branchC, "1"),
		b1Rule(t, "legacy-contradiction-y-rate", contradictionY, "1"),
		b1Rule(t, "legacy-contradiction-x-rate", contradictionX, "1"),
	}
	resolved := f356Schema(t, "uncertainty-legacy-branch-root", rules, relationships)
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := b1Observation(t, "uncertainty-legacy-branch-root", "b-leg-uncertainty-legacy",
		metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, root, "100"), b1Measure(t, branchB, "40"), b1Measure(t, branchC, "60"),
		b1Measure(t, contradictionY, "10"), b1Measure(t, contradictionX, "20"))
	valuation, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{observation}))
	if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("legacy result error = %v, want typed overlap conflict", err)
	}
	if valuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("legacy completeness = %q, want conflict", valuation.Completeness)
	}

	// These wire-shaped keys and the scope are hand-authored from this fixture's
	// component identities and fixed b1Observation fields. The formatter below
	// mirrors the documented overlap and legacy unknown-containment templates;
	// neither expected diagnostic is obtained from a production error value.
	// legacyScopeKey is hand-framed in Scope.Key field order: store-b1, empty
	// tenant/account, local origin/acquisition, operator/backend-ingress/backend-
	// attempt, the fixed b-leg lineage JSON, the fixture stream ID, and empty
	// charge scope.
	const (
		branchBJSON        = `{"direction":"input","component":"vendor:uncertainty_branch_b","unit":"token","schema_id":"b1:frozen-overlap:v1"}`
		branchCJSON        = `{"direction":"input","component":"vendor:uncertainty_branch_c","unit":"token","schema_id":"b1:frozen-overlap:v1"}`
		contradictionYJSON = `{"direction":"input","component":"vendor:uncertainty_contradiction_y","unit":"token","schema_id":"b1:frozen-overlap:v1"}`
		contradictionXJSON = `{"direction":"input","component":"vendor:uncertainty_contradiction_x","unit":"token","schema_id":"b1:frozen-overlap:v1"}`
		legacyScopeKey     = `8:store-b10:0:5:local27:local_transport_measurement8:operator15:backend_ingress15:backend_attempt134:{"subject":{"kind":"b_leg","store_id":"store-b1","a_leg_id":"a-b1","billing_call_id":"call-b1","b_leg_id":"b-leg-uncertainty-legacy"}}37:uncertainty-legacy-branch-root-stream0:`
	)
	typedOverlap := fmt.Sprintf(
		"billing: frozen schema component overlap is not payable: payable component %s transitively includes payable component %s in one %q direction %q unit scope",
		contradictionYJSON, contradictionXJSON, "input", "token",
	)
	legacyUnknown := fmt.Sprintf("billing: unknown containment intersection: %s ~ %s in scope %q", branchBJSON, branchCJSON, legacyScopeKey)
	wantDiagnostic := typedOverlap + "\n" + legacyUnknown
	if got := err.Error(); got != wantDiagnostic {
		t.Fatalf("legacy branch-root diagnostic changed:\n got: %s\nwant: %s", got, wantDiagnostic)
	}
}
