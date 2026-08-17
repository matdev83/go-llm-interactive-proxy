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
			got, err := c.CustomerRatingSnapshots(catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "model")))
			if err != nil {
				t.Fatalf("CustomerRatingSnapshots after rejected mutation: %v", err)
			}
			assertPricingEqual(t, got.DefaultPricing, pricing)
			assertPolicyEqual(t, got.Policy, policy)
			gotRate, err := c.OperatorRate(rates.Ref)
			if err != nil {
				t.Fatalf("OperatorRate: %v", err)
			}
			if !operatorRateEqual(gotRate, rates) {
				t.Fatalf("operator rate = %+v, want original %+v", gotRate, rates)
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
	if err := c.PutPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := c.PutOperatorRate(catalogOperatorRate()); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatal(err)
	}
	got, err := c.CustomerRatingSnapshots(catalogCustomerCall(billing.VersionRef{ID: pricing.Ref.ID, Version: pricing.Ref.Version}, policy.Ref, catalogCustomerLeg("backend", "model")))
	if err != nil {
		t.Fatal(err)
	}
	assertPricingEqual(t, got.DefaultPricing, pricing)
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
	c, pricing, policy, _ := seedCatalog(t)
	pricing.FixedCharges[0].Amount.Nano = 999
	if err := c.PutPricing(catalogPricing()); err != nil {
		t.Fatalf("caller mutation after Put must not change catalog: %v", err)
	}
	got, err := c.CustomerRatingSnapshots(catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "model")))
	if err != nil {
		t.Fatalf("CustomerRatingSnapshots after rejected mutation: %v", err)
	}
	if got.DefaultPricing.FixedCharges[0].Amount.Nano != 3 {
		t.Fatalf("catalog body mutated via caller slice = %d, want 3", got.DefaultPricing.FixedCharges[0].Amount.Nano)
	}
	got.DefaultPricing.InputRatePresent = false
	got.DefaultPricing.FixedCharges[0].Amount.Nano = 1
	again, err := c.CustomerRatingSnapshots(catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "model")))
	if err != nil {
		t.Fatalf("CustomerRatingSnapshots after returned mutation: %v", err)
	}
	if !again.DefaultPricing.InputRatePresent || again.DefaultPricing.FixedCharges[0].Amount.Nano != 3 {
		t.Fatalf("catalog body mutated via returned copy: %+v", again.DefaultPricing)
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

func TestSnapshotCatalog_CustomerRatingSnapshotsReturnsExactBodies(t *testing.T) {
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

	unbound, err := c.CustomerRatingSnapshots(catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "model")))
	if err != nil {
		t.Fatal(err)
	}
	if len(unbound.ModelPricing) != 0 {
		t.Fatalf("model pricing = %+v, want empty when no call leg matches a route override", unbound.ModelPricing)
	}

	special := catalogCustomerLeg("backend", "special")
	special.BLegID = "b-2"
	special.AttemptSeq = 2
	got, err := c.CustomerRatingSnapshots(catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "model"), special))
	if err != nil {
		t.Fatal(err)
	}
	assertPricingEqual(t, got.DefaultPricing, pricing)
	assertPolicyEqual(t, got.Policy, policy)
	if gotRate, err := c.OperatorRate(rates.Ref); err != nil || !operatorRateEqual(gotRate, rates) {
		t.Fatalf("operator rate (default ref) = %+v err=%v", gotRate, err)
	}
	if gotRate, err := c.OperatorRate(secondRate.Ref); err != nil || !operatorRateEqual(gotRate, secondRate) {
		t.Fatalf("operator rate (v2 ref) = %+v err=%v", gotRate, err)
	}
	modelPricing := got.ModelPricing
	if len(modelPricing) != 2 {
		t.Fatalf("model pricing len = %d, want 2 unique billed backend/model cards, got %+v", len(modelPricing), modelPricing)
	}
	for i, card := range modelPricing {
		if card.Pricing.Ref != got.DefaultPricing.Ref {
			t.Fatalf("modelPricing[%d] Ref = %+v, want shared CustomerPricing Ref %+v", i, card.Pricing.Ref, got.DefaultPricing.Ref)
		}
		if versionIdentityEqual(card.Pricing.Ref, override.Ref) {
			t.Fatalf("modelPricing[%d] emitted override document identity %+v", i, card.Pricing.Ref)
		}
	}
	defaultCard := findModelPricing(t, modelPricing, "backend", "model")
	assertPricingEqual(t, defaultCard.Pricing, pricing)
	overrideCard := findModelPricing(t, modelPricing, "backend", "special")
	wantOverride := override
	wantOverride.Ref = got.DefaultPricing.Ref
	assertPricingEqual(t, overrideCard.Pricing, wantOverride)
	if !overrideCard.Pricing.InputRatePresent || overrideCard.Pricing.OutputRatePresent {
		t.Fatalf("override Present bits not preserved: %+v", overrideCard.Pricing)
	}

	gotOverride, err := c.CustomerRatingSnapshots(catalogCustomerCall(override.Ref, policy.Ref, catalogCustomerLeg("backend", "model")))
	if err != nil {
		t.Fatal(err)
	}
	assertPricingEqual(t, gotOverride.DefaultPricing, override)
}

func TestSnapshotCatalog_CustomerRatingSnapshotsFailsClosedWithoutSubstitute(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(pricing billing.PricingSnapshot, policy billing.ChargePolicy) (billing.CallUsageRecord, []billing.CallLegUsageRecord)
	}{
		{
			name: "missing customer pricing version",
			call: func(pricing billing.PricingSnapshot, policy billing.ChargePolicy) (billing.CallUsageRecord, []billing.CallLegUsageRecord) {
				return catalogCustomerCall(billing.VersionRef{ID: pricing.Ref.ID, Version: "missing"}, policy.Ref, catalogCustomerLeg("backend", "model"))
			},
		},
		{
			name: "missing charge policy version",
			call: func(pricing billing.PricingSnapshot, policy billing.ChargePolicy) (billing.CallUsageRecord, []billing.CallLegUsageRecord) {
				return catalogCustomerCall(pricing.Ref, billing.VersionRef{ID: policy.Ref.ID, Version: "missing"}, catalogCustomerLeg("backend", "model"))
			},
		},
		{
			name: "missing pricing id does not use default",
			call: func(_ billing.PricingSnapshot, policy billing.ChargePolicy) (billing.CallUsageRecord, []billing.CallLegUsageRecord) {
				return catalogCustomerCall(billing.VersionRef{ID: "other-prices", Version: "v7"}, policy.Ref, catalogCustomerLeg("backend", "model"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, pricing, policy, _ := seedCatalog(t)
			got, err := c.CustomerRatingSnapshots(tt.call(pricing, policy))
			if err == nil {
				t.Fatal("expected fail-closed error")
			}
			if !errors.Is(err, billing.ErrRatingSnapshotMismatch) && !errors.Is(err, billingcompose.ErrSnapshotNotFound) {
				t.Fatalf("error = %v, want snapshot mismatch or not found", err)
			}
			if !pricingEqual(got.DefaultPricing, billing.PricingSnapshot{}) || got.Policy != (billing.ChargePolicy{}) || got.ModelPricing != nil {
				t.Fatalf("partial substitute returned: %+v", got)
			}
		})
	}
}

func TestSnapshotCatalog_RoutePricingBindingRequiresPublishedBody(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)
	unpublished := catalogPricing()
	unpublished.Ref = billing.VersionRef{ID: "pricing-unpublished", Version: "v1"}
	// The binding API cannot reference a pricing body that is not published, so
	// an override binding always resolves to an immutable published card;
	// resolution can never silently substitute another model's price.
	if err := c.SetRoutePricing("backend", "special", unpublished.Ref); !errors.Is(err, billingcompose.ErrSnapshotNotFound) {
		t.Fatalf("SetRoutePricing(unpublished) = %v, want ErrSnapshotNotFound", err)
	}
	if err := c.PutPricing(unpublished); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutePricing("backend", "special", unpublished.Ref); err != nil {
		t.Fatal(err)
	}
	got, err := c.CustomerRatingSnapshots(catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "special")))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ModelPricing) != 1 || got.ModelPricing[0].Pricing.InputPerMillionNano != unpublished.InputPerMillionNano {
		t.Fatalf("model pricing = %+v, want override body", got.ModelPricing)
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
	c, pricing, policy, _ := seedCatalog(t)

	const (
		readers    = 8
		writers    = 4
		iterations = 200
	)

	record, recordLegs := catalogCustomerCall(pricing.Ref, policy.Ref, catalogCustomerLeg("backend", "model"))

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
				if _, err := c.CustomerRatingSnapshots(record, recordLegs); err != nil {
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

// catalogCustomerLeg builds a call-leg usage record carrying backend/model
// identity only: the facts customer snapshot resolution consumes. Operator
// rate refs are intentionally absent so these helpers cannot accidentally
// couple customer resolution to provider rates.
func catalogCustomerLeg(backend, model string) billing.CallLegUsageRecord {
	return billing.CallLegUsageRecord{
		CallID: catalogCustomerCallID, ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1,
		BackendID: backend, ProviderID: "provider", ModelID: model,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1, Present: true},
			OutputTokens: billing.Quantity{Value: 1, Present: true},
		},
	}
}

var catalogCustomerCallID = billing.BillingCallID("bc_00000000000000000000000000000000")

func catalogCustomerCall(pricing, policy billing.VersionRef, legs ...billing.CallLegUsageRecord) (billing.CallUsageRecord, []billing.CallLegUsageRecord) {
	ids := make([]string, 0, len(legs))
	for _, leg := range legs {
		ids = append(ids, leg.BLegID)
	}
	return billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             catalogCustomerCallID,
		AccountID:          "acct-1",
		ALegID:             "a-1",
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricing,
		ChargePolicyRef:    policy,
		ExpectedBLegIDs:    ids,
	}, legs
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
