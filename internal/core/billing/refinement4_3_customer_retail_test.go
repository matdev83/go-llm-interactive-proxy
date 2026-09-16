package billing

import (
	"errors"
	"testing"
)

func TestRefinement43RetailRejectsUnapprovedProvisionalSelectionMode(t *testing.T) {
	t.Parallel()
	policy := ChargePolicy{
		Ref:                VersionRef{ID: "refinement43-policy", Version: "v1"},
		PricingRef:         VersionRef{ID: "refinement43-pricing", Version: "v1"},
		Scope:              ChargeSurfacedTurn,
		IncludeInputTokens: true,
		Retail: &RetailSelectionPolicy{
			Mode:  RetailSelectionMode("provisional"),
			Basis: RetailBasisIndependent,
		},
	}
	if _, err := ResolveRetailSelectionPolicy(policy); !errors.Is(err, ErrRetailSelectionInvalid) {
		t.Fatalf("unapproved provisional retail policy = %v, want ErrRetailSelectionInvalid", err)
	}
}
