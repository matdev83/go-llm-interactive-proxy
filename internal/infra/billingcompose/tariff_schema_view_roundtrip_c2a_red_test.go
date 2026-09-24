package billingcompose_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

// TestSnapshotCatalogRatingViewPublicDefaultRoundTrip pins the publicly
// reachable rating-source boundary: the configured default card is published,
// read back through the public RatingCatalogView source, and reconstructed into
// a TariffSnapshot whose identity is unchanged. The schema-free default must
// stay byte-compatible with the legacy contract.
//
// Provider-supplied frozen schemas are only reachable on the generic tariff
// material (see TestSnapshotCatalogPublishesAndReplaysFrozenSchemas); publishing
// them as the *default* view body is the C2B opt-in publication gap. This test
// carries the view-level field so the boundary cannot silently drop it once
// C2B lands.
func TestSnapshotCatalogRatingViewPublicDefaultRoundTrip(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	policy := catalogPolicy()
	if err := c.PutPricing(pricing); err != nil {
		t.Fatalf("PutPricing: %v", err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}

	want, err := c.DefaultTariff(context.Background())
	if err != nil {
		t.Fatalf("DefaultTariff: %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	view := snap.Value
	if !reflect.DeepEqual(view.Schemas, want.Schemas) {
		t.Fatalf("snapshot view schemas differ from the default tariff:\n got  %+v\n want %+v",
			view.Schemas, want.Schemas)
	}
	reconstructed, err := view.Tariff(snap.RatingRef(want.Ref.RaterID))
	if err != nil {
		t.Fatalf("reconstruct default via RatingCatalogView.Tariff: %v", err)
	}
	if reconstructed.Content.ContentHash != want.Content.ContentHash {
		t.Fatalf("default view round-trip rewrote the frozen content hash:\n got  %s\n want %s",
			reconstructed.Content.ContentHash, want.Content.ContentHash)
	}
}
