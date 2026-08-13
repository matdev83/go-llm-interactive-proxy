package billingcompose_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

func TestNewRatingResolver(t *testing.T) {
	t.Parallel()

	t.Run("nil catalog", func(t *testing.T) {
		t.Parallel()
		got, err := billingcompose.NewRatingResolver(nil, &fakeAuthorizationLookup{})
		if err == nil {
			t.Fatal("expected constructor error for nil catalog")
		}
		if got != nil {
			t.Fatalf("resolver = %T, want nil", got)
		}
		if !strings.Contains(err.Error(), "catalog") {
			t.Fatalf("error = %v, want catalog mention", err)
		}
	})

	t.Run("nil holds", func(t *testing.T) {
		t.Parallel()
		got, err := billingcompose.NewRatingResolver(billingcompose.NewSnapshotCatalog(), nil)
		if err == nil {
			t.Fatal("expected constructor error for nil holds")
		}
		if got != nil {
			t.Fatalf("resolver = %T, want nil", got)
		}
		if !strings.Contains(err.Error(), "authorization") && !strings.Contains(err.Error(), "hold") {
			t.Fatalf("error = %v, want authorization/hold mention", err)
		}
	})

	t.Run("success join", func(t *testing.T) {
		t.Parallel()
		c, pricing, policy, rates := seedCatalog(t)
		record := mustSealCatalogRecord(t, pricing.Ref, policy.Ref, catalogLeg("backend", "model", rates.Ref))
		hold := catalogHold(record, pricing.Ref, policy.Ref)
		holds := &fakeAuthorizationLookup{auth: hold}

		resolver, err := billingcompose.NewRatingResolver(c, holds)
		if err != nil {
			t.Fatalf("NewRatingResolver: %v", err)
		}

		got, err := resolver.ResolveRating(context.Background(), record)
		if err != nil {
			t.Fatalf("ResolveRating: %v", err)
		}
		if holds.calls != 1 || holds.accountID != record.AccountID || holds.turKey != record.Key {
			t.Fatalf("hold lookup account=%q key=%q calls=%d, want account=%q key=%q calls=1",
				holds.accountID, holds.turKey, holds.calls, record.AccountID, record.Key)
		}
		if got.Authorization.Amount != hold.Amount {
			t.Fatalf("authorization amount = %+v, want hold amount %+v", got.Authorization.Amount, hold.Amount)
		}
		if got.Authorization.ID != hold.ID || got.Authorization.TURKey != hold.TURKey {
			t.Fatalf("authorization = %+v, want hold %+v", got.Authorization, hold)
		}
		if got.Record.Key != record.Key || got.Record.AccountID != record.AccountID {
			t.Fatalf("record identity = %s/%s, want %s/%s", got.Record.AccountID, got.Record.Key, record.AccountID, record.Key)
		}
		if !versionIdentityEqual(got.CustomerPricing.Ref, record.CustomerPricingRef) {
			t.Fatalf("customer pricing ref = %+v, want TUR ref %+v", got.CustomerPricing.Ref, record.CustomerPricingRef)
		}
		if !versionIdentityEqual(got.CustomerPolicy.Ref, record.ChargePolicyRef) {
			t.Fatalf("customer policy ref = %+v, want TUR ref %+v", got.CustomerPolicy.Ref, record.ChargePolicyRef)
		}
		assertPricingEqual(t, got.CustomerPricing, pricing)
		assertPolicyEqual(t, got.CustomerPolicy, policy)
		if len(got.OperatorRates) != 1 || !operatorRateEqual(got.OperatorRates[0], rates) {
			t.Fatalf("operator rates = %+v, want %+v", got.OperatorRates, rates)
		}
		if len(got.ModelPricing) != 0 {
			t.Fatalf("model pricing = %+v, want empty when SnapshotsFor has no override cards", got.ModelPricing)
		}
	})

	t.Run("success join passes model pricing through", func(t *testing.T) {
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
		special := catalogLeg("backend", "special", secondRate.Ref)
		special.BLegID = "b-2"
		special.Seq = 2
		record := mustSealCatalogRecord(t, pricing.Ref, policy.Ref,
			catalogLeg("backend", "model", rates.Ref),
			special,
		)
		wantPricing, wantPolicy, wantRates, wantModel, err := c.SnapshotsFor(record)
		if err != nil {
			t.Fatalf("SnapshotsFor: %v", err)
		}
		if len(wantModel) == 0 {
			t.Fatal("test setup: expected SnapshotsFor model pricing cards")
		}

		hold := catalogHold(record, pricing.Ref, policy.Ref)
		resolver, err := billingcompose.NewRatingResolver(c, &fakeAuthorizationLookup{auth: hold})
		if err != nil {
			t.Fatalf("NewRatingResolver: %v", err)
		}
		got, err := resolver.ResolveRating(context.Background(), record)
		if err != nil {
			t.Fatalf("ResolveRating: %v", err)
		}
		assertPricingEqual(t, got.CustomerPricing, wantPricing)
		assertPolicyEqual(t, got.CustomerPolicy, wantPolicy)
		if !reflect.DeepEqual(got.OperatorRates, billing.OperatorRateSet(wantRates)) {
			t.Fatalf("operator rates = %+v, want SnapshotsFor %+v", got.OperatorRates, wantRates)
		}
		if !reflect.DeepEqual(got.ModelPricing, wantModel) {
			t.Fatalf("model pricing = %+v, want SnapshotsFor %+v", got.ModelPricing, wantModel)
		}
		for i, card := range got.ModelPricing {
			if !versionIdentityEqual(card.Pricing.Ref, record.CustomerPricingRef) {
				t.Fatalf("modelPricing[%d] Ref = %+v, want TUR CustomerPricingRef %+v", i, card.Pricing.Ref, record.CustomerPricingRef)
			}
			if versionIdentityEqual(card.Pricing.Ref, override.Ref) {
				t.Fatalf("modelPricing[%d] used override document identity %+v", i, card.Pricing.Ref)
			}
		}
	})

	t.Run("missing hold", func(t *testing.T) {
		t.Parallel()
		c, pricing, policy, rates := seedCatalog(t)
		record := mustSealCatalogRecord(t, pricing.Ref, policy.Ref, catalogLeg("backend", "model", rates.Ref))
		holds := &fakeAuthorizationLookup{err: billing.ErrAuthorizationNotFound}

		resolver, err := billingcompose.NewRatingResolver(c, holds)
		if err != nil {
			t.Fatalf("NewRatingResolver: %v", err)
		}
		got, err := resolver.ResolveRating(context.Background(), record)
		if err == nil {
			t.Fatal("expected missing-hold error")
		}
		if !errors.Is(err, billing.ErrAuthorizationNotFound) {
			t.Fatalf("error = %v, want %v", err, billing.ErrAuthorizationNotFound)
		}
		if !reflect.DeepEqual(got, billing.RatingInput{}) {
			t.Fatalf("partial rating input on missing hold: %+v", got)
		}
		if got.Authorization.Amount != (billing.Money{}) {
			t.Fatalf("invented hold amount %+v", got.Authorization.Amount)
		}
	})

	t.Run("missing catalog snapshot", func(t *testing.T) {
		t.Parallel()
		c, pricing, policy, rates := seedCatalog(t)
		record := mustSealCatalogRecord(t, billing.VersionRef{ID: pricing.Ref.ID, Version: "missing"}, policy.Ref, catalogLeg("backend", "model", rates.Ref))
		hold := catalogHold(record, record.CustomerPricingRef, policy.Ref)
		holds := &fakeAuthorizationLookup{auth: hold}

		resolver, err := billingcompose.NewRatingResolver(c, holds)
		if err != nil {
			t.Fatalf("NewRatingResolver: %v", err)
		}
		got, err := resolver.ResolveRating(context.Background(), record)
		if err == nil {
			t.Fatal("expected missing-snapshot error")
		}
		if !errors.Is(err, billing.ErrRatingSnapshotMismatch) && !errors.Is(err, billingcompose.ErrSnapshotNotFound) {
			t.Fatalf("error = %v, want snapshot mismatch or not found", err)
		}
		if !reflect.DeepEqual(got, billing.RatingInput{}) {
			t.Fatalf("invented rates on missing snapshot: %+v", got)
		}
		if holds.calls != 1 {
			t.Fatalf("hold lookup calls = %d, want 1 before catalog miss", holds.calls)
		}
	})
}

type fakeAuthorizationLookup struct {
	auth      billing.Authorization
	err       error
	accountID string
	turKey    string
	calls     int
}

func (f *fakeAuthorizationLookup) GetAuthorization(_ context.Context, accountID, turKey string) (billing.Authorization, error) {
	f.calls++
	f.accountID = accountID
	f.turKey = turKey
	if f.err != nil {
		return billing.Authorization{}, f.err
	}
	return f.auth, nil
}

func mustSealCatalogRecord(t *testing.T, pricing, policy billing.VersionRef, legs ...billing.LegUsageRecord) billing.TurnUsageRecord {
	t.Helper()
	sealed, err := catalogRecord(pricing, policy, legs...).Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return sealed
}

func catalogHold(record billing.TurnUsageRecord, pricing, policy billing.VersionRef) billing.Authorization {
	return billing.Authorization{
		ID:              record.AuthorizationID,
		AccountID:       record.AccountID,
		TURKey:          record.Key,
		Amount:          billing.Money{Nano: 12345, Currency: "USD"},
		PricingRef:      pricing,
		ChargePolicyRef: policy,
		Status:          billing.HoldStatusOpen,
	}
}
