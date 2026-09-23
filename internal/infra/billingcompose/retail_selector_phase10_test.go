package billingcompose_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPhase10SnapshotCatalogFreezesRetailSelectionPolicy(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	policy := catalogPolicy()
	policy.Retail = &billing.RetailSelectionPolicy{
		Mode:          billing.RetailSelectionNamedOutcomes,
		OutcomeSubset: []billing.LegOutcome{billing.LegOutcomeWinner},
		Basis:         billing.RetailBasisIndependent,
	}
	if err := c.PutPricing(pricing); err != nil {
		t.Fatal(err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatal(err)
	}

	policy.Retail.OutcomeSubset[0] = billing.LegOutcomeFailed
	got, err := c.Policy(context.Background(), lipapi.Call{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Retail.OutcomeSubset) != 1 || got.Retail.OutcomeSubset[0] != billing.LegOutcomeWinner {
		t.Fatalf("catalog policy changed after caller mutation: %+v", got.Retail)
	}

	got.Retail.OutcomeSubset[0] = billing.LegOutcomeFailed
	again, err := c.Policy(context.Background(), lipapi.Call{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Retail.OutcomeSubset[0] != billing.LegOutcomeWinner {
		t.Fatalf("catalog policy returned an aliased subset: %+v", again.Retail)
	}

	snapshots, err := c.CustomerRatingSnapshots(catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "model")))
	if err != nil {
		t.Fatal(err)
	}
	snapshots.Policy.Retail.OutcomeSubset[0] = billing.LegOutcomeFailed
	reloaded, err := c.CustomerRatingSnapshots(catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "model")))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Policy.Retail.OutcomeSubset[0] != billing.LegOutcomeWinner {
		t.Fatalf("customer rating snapshots returned an aliased policy: %+v", reloaded.Policy.Retail)
	}
}
