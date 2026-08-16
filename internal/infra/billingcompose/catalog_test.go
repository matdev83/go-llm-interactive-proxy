package billingcompose_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSnapshotCatalog_PutRejectsMutationAllowsReplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		put           func(*billingcompose.SnapshotCatalog, billing.PricingSnapshot, billing.ChargePolicy, billing.OperatorRateSnapshot) error
		mutatePricing bool
		mutatePolicy  bool
		mutateRates   bool
		flipPresent   bool
	}{
		{name: "pricing different rate", put: func(c *billingcompose.SnapshotCatalog, p billing.PricingSnapshot, _ billing.ChargePolicy, _ billing.OperatorRateSnapshot) error {
			return c.PutPricing(p)
		}, mutatePricing: true},
		{name: "pricing present bit", put: func(c *billingcompose.SnapshotCatalog, p billing.PricingSnapshot, _ billing.ChargePolicy, _ billing.OperatorRateSnapshot) error {
			return c.PutPricing(p)
		}, mutatePricing: true, flipPresent: true},
		{name: "policy different scope", put: func(c *billingcompose.SnapshotCatalog, _ billing.PricingSnapshot, p billing.ChargePolicy, _ billing.OperatorRateSnapshot) error {
			return c.PutPolicy(p)
		}, mutatePolicy: true},
		{name: "operator rate different input", put: func(c *billingcompose.SnapshotCatalog, _ billing.PricingSnapshot, _ billing.ChargePolicy, r billing.OperatorRateSnapshot) error {
			return c.PutOperatorRate(r)
		}, mutateRates: true},
		{name: "operator rate present bit", put: func(c *billingcompose.SnapshotCatalog, _ billing.PricingSnapshot, _ billing.ChargePolicy, r billing.OperatorRateSnapshot) error {
			return c.PutOperatorRate(r)
		}, mutateRates: true, flipPresent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := billingcompose.NewSnapshotCatalog()
			pricing := catalogPricing()
			policy := catalogPolicy()
			rates := catalogOperatorRate()
			if err := c.PutPricing(pricing); err != nil {
				t.Fatalf("PutPricing: %v", err)
			}
			if err := c.PutPolicy(policy); err != nil {
				t.Fatalf("PutPolicy: %v", err)
			}
			if err := c.PutOperatorRate(rates); err != nil {
				t.Fatalf("PutOperatorRate: %v", err)
			}
			if err := tt.put(c, pricing, policy, rates); err != nil {
				t.Fatalf("identical replay: %v", err)
			}

			mutPricing := pricing
			mutPolicy := policy
			mutRates := rates
			if tt.mutatePricing {
				if tt.flipPresent {
					mutPricing.InputRatePresent = false
				} else {
					mutPricing.InputPerMillionNano++
				}
			}
			if tt.mutatePolicy {
				mutPolicy.Scope = billing.ChargeAllPotentialLegs
			}
			if tt.mutateRates {
				if tt.flipPresent {
					mutRates.InputRatePresent = false
				} else {
					mutRates.InputPerMillionNano++
				}
			}
			err := tt.put(c, mutPricing, mutPolicy, mutRates)
			if !errors.Is(err, billingcompose.ErrSnapshotImmutable) {
				t.Fatalf("mutated put error = %v, want %v", err, billingcompose.ErrSnapshotImmutable)
			}

			if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
				t.Fatalf("SetDefaults: %v", err)
			}
			gotPricing, gotPolicy, gotRates, _, err := c.SnapshotsFor(catalogRecord(pricing.Ref, policy.Ref, catalogLeg("backend", "model", rates.Ref)))
			if err != nil {
				t.Fatalf("SnapshotsFor after rejected mutation: %v", err)
			}
			assertPricingEqual(t, gotPricing, pricing)
			assertPolicyEqual(t, gotPolicy, policy)
			if len(gotRates) != 1 || !operatorRateEqual(gotRates[0], rates) {
				t.Fatalf("operator rates = %+v, want original %+v", gotRates, rates)
			}
		})
	}
}

func TestSnapshotCatalog_PutSameIdentityDifferentFetchedAtIsReplay(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	pricing.Ref.EffectiveAt = time.Unix(10, 0).UTC()
	pricing.Ref.FetchedAt = time.Unix(11, 0).UTC()
	if err := c.PutPricing(pricing); err != nil {
		t.Fatal(err)
	}
	replay := pricing
	replay.Ref.EffectiveAt = time.Unix(99, 0).UTC()
	replay.Ref.FetchedAt = time.Unix(100, 0).UTC()
	if err := c.PutPricing(replay); err != nil {
		t.Fatalf("timestamp-only replay: %v", err)
	}
	policy := catalogPolicy()
	rates := catalogOperatorRate()
	if err := c.PutPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := c.PutOperatorRate(rates); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatal(err)
	}
	got, _, _, _, err := c.SnapshotsFor(catalogRecord(billing.VersionRef{ID: pricing.Ref.ID, Version: pricing.Ref.Version}, policy.Ref, catalogLeg("backend", "model", rates.Ref)))
	if err != nil {
		t.Fatal(err)
	}
	assertPricingEqual(t, got, pricing)
}

func TestSnapshotCatalog_PutRejectsInvalidSnapshots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*billingcompose.SnapshotCatalog) error
	}{
		{name: "pricing missing ref", run: func(c *billingcompose.SnapshotCatalog) error {
			p := catalogPricing()
			p.Ref = billing.VersionRef{}
			return c.PutPricing(p)
		}},
		{name: "policy missing ref", run: func(c *billingcompose.SnapshotCatalog) error {
			p := catalogPolicy()
			p.Ref = billing.VersionRef{}
			return c.PutPolicy(p)
		}},
		{name: "operator rate missing ref", run: func(c *billingcompose.SnapshotCatalog) error {
			r := catalogOperatorRate()
			r.Ref = billing.VersionRef{}
			return c.PutOperatorRate(r)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.run(billingcompose.NewSnapshotCatalog()); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSnapshotCatalog_ReturnedBodiesAreCopies(t *testing.T) {
	t.Parallel()
	c, pricing, policy, rates := seedCatalog(t)
	pricing.FixedCharges[0].Amount.Nano = 999
	if err := c.PutPricing(catalogPricing()); err != nil {
		t.Fatalf("caller mutation after Put must not change catalog: %v", err)
	}
	got, _, _, _, err := c.SnapshotsFor(catalogRecord(pricing.Ref, policy.Ref, catalogLeg("backend", "model", rates.Ref)))
	if err != nil {
		t.Fatal(err)
	}
	if got.FixedCharges[0].Amount.Nano != 3 {
		t.Fatalf("catalog body mutated via caller slice = %d, want 3", got.FixedCharges[0].Amount.Nano)
	}
	got.InputRatePresent = false
	got.FixedCharges[0].Amount.Nano = 1
	again, _, _, _, err := c.SnapshotsFor(catalogRecord(pricing.Ref, policy.Ref, catalogLeg("backend", "model", rates.Ref)))
	if err != nil {
		t.Fatal(err)
	}
	if !again.InputRatePresent || again.FixedCharges[0].Amount.Nano != 3 {
		t.Fatalf("catalog body mutated via returned copy: %+v", again)
	}
}

func TestSnapshotCatalog_SetDefaultsRequiresPublishedMatchingPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(*testing.T, *billingcompose.SnapshotCatalog) (billing.VersionRef, billing.VersionRef)
		wantErr error
	}{
		{
			name: "unpublished pricing",
			setup: func(t *testing.T, c *billingcompose.SnapshotCatalog) (billing.VersionRef, billing.VersionRef) {
				t.Helper()
				policy := catalogPolicy()
				if err := c.PutPolicy(policy); err != nil {
					t.Fatal(err)
				}
				return catalogPricing().Ref, policy.Ref
			},
			wantErr: billingcompose.ErrSnapshotNotFound,
		},
		{
			name: "unpublished policy",
			setup: func(t *testing.T, c *billingcompose.SnapshotCatalog) (billing.VersionRef, billing.VersionRef) {
				t.Helper()
				pricing := catalogPricing()
				if err := c.PutPricing(pricing); err != nil {
					t.Fatal(err)
				}
				return pricing.Ref, catalogPolicy().Ref
			},
			wantErr: billingcompose.ErrSnapshotNotFound,
		},
		{
			name: "policy pricing ref mismatch",
			setup: func(t *testing.T, c *billingcompose.SnapshotCatalog) (billing.VersionRef, billing.VersionRef) {
				t.Helper()
				pricing := catalogPricing()
				other := catalogPricing()
				other.Ref = billing.VersionRef{ID: "pricing", Version: "v8"}
				other.InputPerMillionNano = 10
				policy := catalogPolicy()
				if err := c.PutPricing(pricing); err != nil {
					t.Fatal(err)
				}
				if err := c.PutPricing(other); err != nil {
					t.Fatal(err)
				}
				if err := c.PutPolicy(policy); err != nil {
					t.Fatal(err)
				}
				return other.Ref, policy.Ref
			},
			wantErr: billingcompose.ErrPolicyPricingMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := billingcompose.NewSnapshotCatalog()
			pricingRef, policyRef := tt.setup(t, c)
			err := c.SetDefaults(pricingRef, policyRef)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("SetDefaults error = %v, want %v", err, tt.wantErr)
			}
		})
	}

	t.Run("matching published refs", func(t *testing.T) {
		t.Parallel()
		c := billingcompose.NewSnapshotCatalog()
		pricing := catalogPricing()
		policy := catalogPolicy()
		if err := c.PutPricing(pricing); err != nil {
			t.Fatal(err)
		}
		if err := c.PutPolicy(policy); err != nil {
			t.Fatal(err)
		}
		if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSnapshotCatalog_BindingsRequirePublishedSnapshots(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	if err := c.SetRoutePricing("backend", "model", catalogPricing().Ref); !errors.Is(err, billingcompose.ErrSnapshotNotFound) {
		t.Fatalf("SetRoutePricing unpublished = %v, want %v", err, billingcompose.ErrSnapshotNotFound)
	}
	if err := c.SetOperatorRateBinding("backend", "model", catalogOperatorRate().Ref); !errors.Is(err, billingcompose.ErrSnapshotNotFound) {
		t.Fatalf("SetOperatorRateBinding unpublished = %v, want %v", err, billingcompose.ErrSnapshotNotFound)
	}

	pricing := catalogPricing()
	rates := catalogOperatorRate()
	if err := c.PutPricing(pricing); err != nil {
		t.Fatal(err)
	}
	if err := c.PutOperatorRate(rates); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutePricing("backend", "model", pricing.Ref); err != nil {
		t.Fatalf("SetRoutePricing published: %v", err)
	}
	if err := c.SetOperatorRateBinding("backend", "model", rates.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding published: %v", err)
	}
}

func TestSnapshotCatalog_RoutePricingUsesOverrideElseDefault(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)
	ctx := context.Background()
	got, err := c.RoutePricing(ctx, "backend", "other")
	if err != nil {
		t.Fatal(err)
	}
	assertPricingEqual(t, got, pricing)

	override := catalogPricing()
	override.Ref = billing.VersionRef{ID: "pricing", Version: "v8"}
	override.InputPerMillionNano = 10
	override.OutputPerMillionNano = 20
	if err := c.PutPricing(override); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutePricing("backend", "special", override.Ref); err != nil {
		t.Fatal(err)
	}
	gotOverride, err := c.RoutePricing(ctx, "backend", "special")
	if err != nil {
		t.Fatal(err)
	}
	wantOverride := override
	wantOverride.Ref = pricing.Ref
	assertPricingEqual(t, gotOverride, wantOverride)
	if gotOverride.Ref != policy.PricingRef {
		t.Fatalf("override RoutePricing Ref = %+v, want policy PricingRef %+v", gotOverride.Ref, policy.PricingRef)
	}
	gotDefault, err := c.RoutePricing(ctx, "backend", "other")
	if err != nil {
		t.Fatal(err)
	}
	assertPricingEqual(t, gotDefault, pricing)
}

func TestSnapshotCatalog_AdmissionSnapshotRefs(t *testing.T) {
	t.Parallel()
	c, pricing, policy, rates := seedCatalog(t)
	ctx := context.Background()
	call := lipapi.Call{ID: "call-1"}

	gotPolicy, err := c.Policy(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	assertPolicyEqual(t, gotPolicy, policy)
	if got := c.CustomerPricingRef(ctx, call); !versionIdentityEqual(got, pricing.Ref) {
		t.Fatalf("CustomerPricingRef = %+v, want %+v", got, pricing.Ref)
	}
	if got := c.ChargePolicyRef(ctx, call); !versionIdentityEqual(got, policy.Ref) {
		t.Fatalf("ChargePolicyRef = %+v, want %+v", got, policy.Ref)
	}
	if got := c.OperatorRateRef(ctx, "backend", "unbound"); got != (billing.VersionRef{}) {
		t.Fatalf("unbound OperatorRateRef = %+v, want empty", got)
	}
	if err := c.SetOperatorRateBinding("backend", "model", rates.Ref); err != nil {
		t.Fatal(err)
	}
	if got := c.OperatorRateRef(ctx, "backend", "model"); !versionIdentityEqual(got, rates.Ref) {
		t.Fatalf("OperatorRateRef = %+v, want %+v", got, rates.Ref)
	}
}

func TestSnapshotCatalog_SnapshotsForReturnsExactBodies(t *testing.T) {
	t.Parallel()
	c, pricing, policy, rates := seedCatalog(t)
	override := catalogPricing()
	override.Ref = billing.VersionRef{ID: "pricing", Version: "v8"}
	override.InputPerMillionNano = 10
	override.OutputPerMillionNano = 20
	override.InputRatePresent = true
	override.OutputRatePresent = false
	if err := c.PutPricing(override); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutePricing("backend", "special", override.Ref); err != nil {
		t.Fatal(err)
	}
	secondRate := catalogOperatorRate()
	secondRate.Ref = billing.VersionRef{ID: "operator-rates", Version: "v2"}
	secondRate.InputPerMillionNano = 5
	if err := c.PutOperatorRate(secondRate); err != nil {
		t.Fatal(err)
	}

	unboundRecord := catalogRecord(pricing.Ref, policy.Ref, catalogLeg("backend", "model", rates.Ref))
	_, _, _, unboundCards, err := c.SnapshotsFor(unboundRecord)
	if err != nil {
		t.Fatal(err)
	}
	if len(unboundCards) != 0 {
		t.Fatalf("model pricing = %+v, want empty when no TUR leg matches a route override", unboundCards)
	}

	record := catalogRecord(pricing.Ref, policy.Ref,
		catalogLeg("backend", "model", rates.Ref),
		func() billing.LegUsageRecord {
			leg := catalogLeg("backend", "special", secondRate.Ref)
			leg.BLegID = "b-2"
			leg.Seq = 2
			return leg
		}(),
	)
	gotPricing, gotPolicy, gotRates, modelPricing, err := c.SnapshotsFor(record)
	if err != nil {
		t.Fatal(err)
	}
	assertPricingEqual(t, gotPricing, pricing)
	assertPolicyEqual(t, gotPolicy, policy)
	if len(gotRates) != 2 {
		t.Fatalf("operator rates len = %d, want 2", len(gotRates))
	}
	if !operatorRateEqual(gotRates[0], rates) || !operatorRateEqual(gotRates[1], secondRate) {
		t.Fatalf("operator rates = %+v", gotRates)
	}
	if len(modelPricing) != 2 {
		t.Fatalf("model pricing len = %d, want 2 unique billed backend/model cards, got %+v", len(modelPricing), modelPricing)
	}
	for i, card := range modelPricing {
		if card.Pricing.Ref != gotPricing.Ref {
			t.Fatalf("modelPricing[%d] Ref = %+v, want shared CustomerPricing Ref %+v", i, card.Pricing.Ref, gotPricing.Ref)
		}
		if !versionIdentityEqual(card.Pricing.Ref, record.CustomerPricingRef) {
			t.Fatalf("modelPricing[%d] Ref = %+v, want TUR CustomerPricingRef %+v", i, card.Pricing.Ref, record.CustomerPricingRef)
		}
		if versionIdentityEqual(card.Pricing.Ref, override.Ref) {
			t.Fatalf("modelPricing[%d] emitted override document identity %+v", i, card.Pricing.Ref)
		}
	}
	defaultCard := findModelPricing(t, modelPricing, "backend", "model")
	assertPricingEqual(t, defaultCard.Pricing, pricing)
	overrideCard := findModelPricing(t, modelPricing, "backend", "special")
	wantOverride := override
	wantOverride.Ref = record.CustomerPricingRef
	assertPricingEqual(t, overrideCard.Pricing, wantOverride)
	if !overrideCard.Pricing.InputRatePresent || overrideCard.Pricing.OutputRatePresent {
		t.Fatalf("override Present bits not preserved: %+v", overrideCard.Pricing)
	}

	gotOverride, _, _, _, err := c.SnapshotsFor(catalogRecord(override.Ref, policy.Ref, catalogLeg("backend", "model", rates.Ref)))
	if err != nil {
		t.Fatal(err)
	}
	assertPricingEqual(t, gotOverride, override)
}

func TestSnapshotCatalog_SnapshotsForFailsClosedWithoutSubstitute(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record func(pricing billing.PricingSnapshot, policy billing.ChargePolicy, rates billing.OperatorRateSnapshot) billing.TurnUsageRecord
	}{
		{
			name: "missing customer pricing version",
			record: func(pricing billing.PricingSnapshot, policy billing.ChargePolicy, rates billing.OperatorRateSnapshot) billing.TurnUsageRecord {
				return catalogRecord(billing.VersionRef{ID: pricing.Ref.ID, Version: "missing"}, policy.Ref, catalogLeg("backend", "model", rates.Ref))
			},
		},
		{
			name: "missing charge policy version",
			record: func(pricing billing.PricingSnapshot, policy billing.ChargePolicy, rates billing.OperatorRateSnapshot) billing.TurnUsageRecord {
				return catalogRecord(pricing.Ref, billing.VersionRef{ID: policy.Ref.ID, Version: "missing"}, catalogLeg("backend", "model", rates.Ref))
			},
		},
		{
			name: "missing operator rate version",
			record: func(pricing billing.PricingSnapshot, policy billing.ChargePolicy, rates billing.OperatorRateSnapshot) billing.TurnUsageRecord {
				return catalogRecord(pricing.Ref, policy.Ref, catalogLeg("backend", "model", billing.VersionRef{ID: rates.Ref.ID, Version: "missing"}))
			},
		},
		{
			name: "missing pricing id does not use default",
			record: func(_ billing.PricingSnapshot, policy billing.ChargePolicy, rates billing.OperatorRateSnapshot) billing.TurnUsageRecord {
				return catalogRecord(billing.VersionRef{ID: "other-prices", Version: "v7"}, policy.Ref, catalogLeg("backend", "model", rates.Ref))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, pricing, policy, rates := seedCatalog(t)
			gotPricing, gotPolicy, gotRates, modelPricing, err := c.SnapshotsFor(tt.record(pricing, policy, rates))
			if err == nil {
				t.Fatal("expected fail-closed error")
			}
			if !errors.Is(err, billing.ErrRatingSnapshotMismatch) && !errors.Is(err, billingcompose.ErrSnapshotNotFound) {
				t.Fatalf("error = %v, want snapshot mismatch or not found", err)
			}
			if !pricingEqual(gotPricing, billing.PricingSnapshot{}) || gotPolicy != (billing.ChargePolicy{}) || gotRates != nil || modelPricing != nil {
				t.Fatalf("partial substitute returned: pricing=%+v policy=%+v rates=%+v model=%+v", gotPricing, gotPolicy, gotRates, modelPricing)
			}
		})
	}
}

func TestSnapshotCatalog_RoutePricingFailsClosedWithoutDefault(t *testing.T) {
	t.Parallel()
	c := billingcompose.NewSnapshotCatalog()
	if _, err := c.RoutePricing(context.Background(), "backend", "model"); err == nil {
		t.Fatal("expected error when defaults are unset")
	}
	if _, err := c.Policy(context.Background(), lipapi.Call{}); err == nil {
		t.Fatal("expected policy error when defaults are unset")
	}
}

func TestSnapshotCatalog_ConcurrentReadsDuringPublish(t *testing.T) {
	c, pricing, policy, rates := seedCatalog(t)

	const (
		readers    = 8
		writers    = 4
		iterations = 200
	)

	record := catalogRecord(pricing.Ref, policy.Ref, catalogLeg("backend", "model", rates.Ref))

	var wg sync.WaitGroup
	errs := make(chan error, readers+writers)

	for range readers {
		wg.Go(func() {
			for range iterations {
				if _, err := c.RoutePricing(context.Background(), "backend", "model"); err != nil {
					errs <- err
					return
				}
				if !c.HasDefaults() {
					errs <- errors.New("defaults lost during concurrent read")
					return
				}
				if _, _, _, _, err := c.SnapshotsFor(record); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	for i := range writers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := range iterations {
				p := catalogPricing()
				p.Ref = billing.VersionRef{ID: fmt.Sprintf("pricing-%d", n), Version: fmt.Sprintf("v%d", j)}
				if err := c.PutPricing(p); err != nil {
					errs <- err
					return
				}
				r := catalogOperatorRate()
				r.Ref = billing.VersionRef{ID: fmt.Sprintf("rate-%d", n), Version: fmt.Sprintf("v%d", j)}
				if err := c.PutOperatorRate(r); err != nil {
					errs <- err
					return
				}
				if err := c.SetRoutePricing("backend", fmt.Sprintf("model-%d-%d", n, j), p.Ref); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent catalog access: %v", err)
	}
}

func TestSnapshotCatalog_BindingsAreImmutable(t *testing.T) {
	t.Parallel()
	c, pricing, _, rates := seedCatalog(t)

	if err := c.SetRoutePricing("backend", "model", pricing.Ref); err != nil {
		t.Fatalf("initial route binding: %v", err)
	}
	if err := c.SetOperatorRateBinding("backend", "model", rates.Ref); err != nil {
		t.Fatalf("initial operator binding: %v", err)
	}

	// Identical replay of the same binding is allowed.
	if err := c.SetRoutePricing("backend", "model", pricing.Ref); err != nil {
		t.Fatalf("identical route replay: %v", err)
	}
	if err := c.SetOperatorRateBinding("backend", "model", rates.Ref); err != nil {
		t.Fatalf("identical operator replay: %v", err)
	}

	other := catalogPricing()
	other.Ref = billing.VersionRef{ID: "pricing", Version: "v8"}
	if err := c.PutPricing(other); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutePricing("backend", "model", other.Ref); !errors.Is(err, billingcompose.ErrBindingImmutable) {
		t.Fatalf("route rebind = %v, want ErrBindingImmutable", err)
	}

	otherRate := catalogOperatorRate()
	otherRate.Ref = billing.VersionRef{ID: "operator-rates", Version: "v2"}
	if err := c.PutOperatorRate(otherRate); err != nil {
		t.Fatal(err)
	}
	if err := c.SetOperatorRateBinding("backend", "model", otherRate.Ref); !errors.Is(err, billingcompose.ErrBindingImmutable) {
		t.Fatalf("operator rebind = %v, want ErrBindingImmutable", err)
	}
}

func seedCatalog(t *testing.T) (*billingcompose.SnapshotCatalog, billing.PricingSnapshot, billing.ChargePolicy, billing.OperatorRateSnapshot) {
	t.Helper()
	c := billingcompose.NewSnapshotCatalog()
	pricing := catalogPricing()
	policy := catalogPolicy()
	rates := catalogOperatorRate()
	if err := c.PutPricing(pricing); err != nil {
		t.Fatal(err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := c.PutOperatorRate(rates); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatal(err)
	}
	return c, pricing, policy, rates
}

func catalogPricing() billing.PricingSnapshot {
	return billing.PricingSnapshot{
		Ref:                  billing.VersionRef{ID: "pricing", Version: "v7"},
		Currency:             "USD",
		InputPerMillionNano:  100,
		OutputPerMillionNano: 200,
		InputRatePresent:     true,
		OutputRatePresent:    true,
		FixedCharges:         []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 3, Currency: "USD"}}},
	}
}

func catalogPolicy() billing.ChargePolicy {
	return billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "policy", Version: "v2"},
		PricingRef:          catalogPricing().Ref,
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}
}

func catalogOperatorRate() billing.OperatorRateSnapshot {
	return billing.OperatorRateSnapshot{
		Ref:                  billing.VersionRef{ID: "operator-rates", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  50,
		OutputPerMillionNano: 75,
		InputRatePresent:     true,
		OutputRatePresent:    true,
	}
}

func catalogLeg(backend, model string, rate billing.VersionRef) billing.LegUsageRecord {
	return billing.LegUsageRecord{
		ALegID: "a-1", BLegID: "b-1", Seq: 1,
		BackendID: backend, ProviderID: "provider", ModelID: model,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1, Present: true},
			OutputTokens: billing.Quantity{Value: 1, Present: true},
		},
		OperatorRateRef: rate,
	}
}

func catalogRecord(pricing, policy billing.VersionRef, legs ...billing.LegUsageRecord) billing.TurnUsageRecord {
	return billing.TurnUsageRecord{
		SchemaVersion:         billing.CurrentRecordSchemaVersion,
		AccountID:             "acct-1",
		TurnID:                "turn-1",
		ALegID:                "a-1",
		LegacyAuthorizationID: "auth-1",
		StartedAt:             time.Unix(100, 0).UTC(),
		FinishedAt:            time.Unix(101, 0).UTC(),
		Outcome:               billing.TurnOutcomeCompleted,
		CustomerPricingRef:    pricing,
		ChargePolicyRef:       policy,
		Legs:                  legs,
	}
}

func assertPricingEqual(t *testing.T, got, want billing.PricingSnapshot) {
	t.Helper()
	if !pricingEqual(got, want) {
		t.Fatalf("pricing mismatch\ngot  %+v\nwant %+v", got, want)
	}
}

func assertPolicyEqual(t *testing.T, got, want billing.ChargePolicy) {
	t.Helper()
	if !policyEqual(got, want) {
		t.Fatalf("policy mismatch\ngot  %+v\nwant %+v", got, want)
	}
}

func pricingEqual(got, want billing.PricingSnapshot) bool {
	got.Ref.EffectiveAt, got.Ref.FetchedAt = want.Ref.EffectiveAt, want.Ref.FetchedAt
	return reflect.DeepEqual(got, want)
}

func policyEqual(got, want billing.ChargePolicy) bool {
	got.Ref.EffectiveAt, got.Ref.FetchedAt = want.Ref.EffectiveAt, want.Ref.FetchedAt
	got.PricingRef.EffectiveAt, got.PricingRef.FetchedAt = want.PricingRef.EffectiveAt, want.PricingRef.FetchedAt
	return got == want
}

func operatorRateEqual(got, want billing.OperatorRateSnapshot) bool {
	got.Ref.EffectiveAt, got.Ref.FetchedAt = want.Ref.EffectiveAt, want.Ref.FetchedAt
	return got == want
}

func versionIdentityEqual(got, want billing.VersionRef) bool {
	return got.ID == want.ID && got.Version == want.Version
}

func findModelPricing(t *testing.T, cards []billing.ModelCustomerPricing, backend, model string) billing.ModelCustomerPricing {
	t.Helper()
	var found []billing.ModelCustomerPricing
	for _, card := range cards {
		if card.BackendID == backend && card.ModelID == model {
			found = append(found, card)
		}
	}
	if len(found) != 1 {
		t.Fatalf("model pricing cards for %s/%s: got %d, want 1 in %+v", backend, model, len(found), cards)
	}
	return found[0]
}
