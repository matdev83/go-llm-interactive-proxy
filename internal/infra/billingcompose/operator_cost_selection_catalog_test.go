package billingcompose

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func catalogOperatorCostSelectionTestPolicy() billing.OperatorCostSelectionPolicy {
	return billing.OperatorCostSelectionPolicy{
		Version: billing.OperatorCostSelectionPolicyV1,
		Ref:     billing.VersionRef{ID: "operator-selection", Version: "v1"},
		Rules: []billing.OperatorCostSelectionRule{{
			ID: "p-final", Basis: billing.OperatorCostBasisP, Status: billing.OperatorCostSelectionStatusFinal,
			RequireOperatorPayer: true,
		}},
		KnownZeroProvenance: []billing.OperatorCostProvenance{billing.OperatorCostProvenanceNeverStarted},
	}
}

// TestSnapshotCatalogOperatorCostSelectionPolicyFrozen proves the catalog can
// hold one explicit immutable selection policy for later consumers without
// changing any runtime wiring.
func TestSnapshotCatalogOperatorCostSelectionPolicyFrozen(t *testing.T) {
	t.Parallel()

	catalog := NewSnapshotCatalog()
	policy := catalogOperatorCostSelectionTestPolicy()
	if err := catalog.PutOperatorCostSelectionPolicy(policy); err != nil {
		t.Fatalf("PutOperatorCostSelectionPolicy: %v", err)
	}
	if err := catalog.PutOperatorCostSelectionPolicy(policy); err != nil {
		t.Fatalf("exact replay must be idempotent: %v", err)
	}

	changed := policy.Clone()
	changed.Rules[0].Basis = billing.OperatorCostBasisQ
	if err := catalog.PutOperatorCostSelectionPolicy(changed); !errors.Is(err, ErrSnapshotImmutable) {
		t.Fatalf("error = %v, want ErrSnapshotImmutable", err)
	}

	got, err := catalog.OperatorCostSelectionPolicy(policy.Ref)
	if err != nil {
		t.Fatalf("OperatorCostSelectionPolicy: %v", err)
	}
	if got.Ref != policy.Ref || len(got.Rules) != 1 || got.Rules[0].Basis != billing.OperatorCostBasisP {
		t.Fatalf("stored policy = %+v, want frozen P rule", got)
	}
	if len(got.KnownZeroProvenance) != 1 || got.KnownZeroProvenance[0] != billing.OperatorCostProvenanceNeverStarted {
		t.Fatalf("known-zero authorization lost: %+v", got.KnownZeroProvenance)
	}

	got.Rules[0].Basis = billing.OperatorCostBasisS
	got.KnownZeroProvenance[0] = billing.OperatorCostProvenanceNotBillable
	again, err := catalog.OperatorCostSelectionPolicy(policy.Ref)
	if err != nil {
		t.Fatalf("OperatorCostSelectionPolicy: %v", err)
	}
	if again.Rules[0].Basis != billing.OperatorCostBasisP || again.KnownZeroProvenance[0] != billing.OperatorCostProvenanceNeverStarted {
		t.Fatalf("catalog aliased caller memory: %+v", again)
	}

	if _, err := catalog.OperatorCostSelectionPolicy(billing.VersionRef{ID: "missing", Version: "v1"}); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("missing policy error = %v, want ErrSnapshotNotFound", err)
	}
	invalid := policy.Clone()
	invalid.Version = 99
	if err := catalog.PutOperatorCostSelectionPolicy(invalid); err == nil {
		t.Fatal("invalid policy must be rejected")
	}
	var nilCatalog *SnapshotCatalog
	if err := nilCatalog.PutOperatorCostSelectionPolicy(policy); err == nil {
		t.Fatal("nil catalog must be rejected")
	}
}
