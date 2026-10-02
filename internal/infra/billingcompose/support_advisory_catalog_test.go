package billingcompose

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestSnapshotCatalogPutPricingWithSupportAdvisoryPublishesDefaultAndRouteMaterial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		advisoryDefault bool
	}{
		{name: "advisory default with legacy route", advisoryDefault: true},
		{name: "legacy default with advisory route", advisoryDefault: false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			legacyPricing := supportAdvisoryCatalogPricing("legacy-pricing", "v1", 110)
			advisoryPricing := supportAdvisoryCatalogPricing("advisory-pricing", "v1", 220)
			schemas := supportAdvisoryCatalogSchemas(metering.ComponentCacheReadInputToken)
			wantLegacy := supportAdvisoryExpectedTariff(t, legacyPricing, nil, "")
			wantAdvisory := supportAdvisoryExpectedTariff(t, advisoryPricing, schemas, economics.SupportAdvisoryVersionV1)

			catalog := NewSnapshotCatalog()
			if err := catalog.PutPricing(legacyPricing); err != nil {
				t.Fatalf("PutPricing legacy: %v", err)
			}
			if err := catalog.PutPricingWithSupportAdvisory(advisoryPricing, schemas, economics.SupportAdvisoryVersionV1); err != nil {
				t.Fatalf("PutPricingWithSupportAdvisory: %v", err)
			}
			// Published tariffs own their nested schema material.
			schemas[0].Relationships[0].Child.Component = "caller-mutated-after-publication"

			defaultPricing := legacyPricing
			defaultWant := wantLegacy
			routePricing := advisoryPricing
			routeWant := wantAdvisory
			if tc.advisoryDefault {
				defaultPricing = advisoryPricing
				defaultWant = wantAdvisory
				routePricing = legacyPricing
				routeWant = wantLegacy
			}
			policy := billing.ChargePolicy{
				Ref:                 billing.VersionRef{ID: "policy-" + defaultPricing.Ref.ID, Version: "v1"},
				PricingRef:          defaultPricing.Ref,
				Scope:               billing.ChargeSurfacedTurn,
				IncludeInputTokens:  true,
				IncludeOutputTokens: true,
				IncludeFixedCharges: true,
			}
			if err := catalog.PutPolicy(policy); err != nil {
				t.Fatalf("PutPolicy: %v", err)
			}
			if err := catalog.SetDefaults(defaultPricing.Ref, policy.Ref); err != nil {
				t.Fatalf("SetDefaults: %v", err)
			}
			if err := catalog.SetRoutePricing("backend", "model", routePricing.Ref); err != nil {
				t.Fatalf("SetRoutePricing: %v", err)
			}

			gotDefault, err := catalog.DefaultTariff(ctx)
			if err != nil {
				t.Fatalf("DefaultTariff: %v", err)
			}
			assertSupportAdvisoryCatalogTariff(t, gotDefault, defaultWant)

			gotRoute, err := catalog.RouteTariff(ctx, "backend", "model")
			if err != nil {
				t.Fatalf("RouteTariff: %v", err)
			}
			assertSupportAdvisoryCatalogTariff(t, gotRoute, routeWant)

			snapshot, err := catalog.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if snapshot.Value.SupportAdvisoryVersion != defaultWant.SupportAdvisoryVersion {
				t.Fatalf("RatingCatalogView.SupportAdvisoryVersion = %q, want %q", snapshot.Value.SupportAdvisoryVersion, defaultWant.SupportAdvisoryVersion)
			}
			if !reflect.DeepEqual(snapshot.Value.Schemas, defaultWant.Schemas) {
				t.Fatalf("RatingCatalogView schemas = %+v, want %+v", snapshot.Value.Schemas, defaultWant.Schemas)
			}
			reconstructed, err := snapshot.Value.Tariff(snapshot.RatingRef(defaultWant.Ref.RaterID))
			if err != nil {
				t.Fatalf("RatingCatalogView.Tariff: %v", err)
			}
			assertSupportAdvisoryCatalogTariff(t, reconstructed, defaultWant)

			customer, err := catalog.CustomerRatingSnapshots(
				billing.CallUsageRecord{CustomerPricingRef: defaultPricing.Ref, ChargePolicyRef: policy.Ref},
				[]billing.CallLegUsageRecord{{BackendID: "backend", ModelID: "model"}},
			)
			if err != nil {
				t.Fatalf("CustomerRatingSnapshots: %v", err)
			}
			assertSupportAdvisoryCatalogTariff(t, customer.DefaultTariff, defaultWant)
			if len(customer.ModelTariffs) != 1 {
				t.Fatalf("ModelTariffs len = %d, want 1", len(customer.ModelTariffs))
			}
			assertSupportAdvisoryCatalogTariff(t, customer.ModelTariffs[0].Tariff, routeWant)

			// The explicit view and both customer-rating sources remain isolated
			// from caller mutations to returned nested schema slices.
			if defaultWant.SupportAdvisoryVersion != "" {
				customer.DefaultTariff.Schemas[0].Relationships[0].Child.Component = "mutated-default-result"
				defaultAgain, err := catalog.DefaultTariff(ctx)
				if err != nil {
					t.Fatalf("DefaultTariff after returned-body mutation: %v", err)
				}
				assertSupportAdvisoryCatalogTariff(t, defaultAgain, defaultWant)
				snapshot.Value.Schemas[0].Relationships[0].Child.Component = "mutated-view-result"
				snapshotAgain, err := catalog.Snapshot(ctx)
				if err != nil {
					t.Fatalf("Snapshot after returned-view mutation: %v", err)
				}
				if !reflect.DeepEqual(snapshotAgain.Value.Schemas, defaultWant.Schemas) {
					t.Fatalf("catalog schema changed after mutating returned view: %+v", snapshotAgain.Value.Schemas)
				}
			}
			if routeWant.SupportAdvisoryVersion != "" {
				customer.ModelTariffs[0].Tariff.Schemas[0].Relationships[0].Child.Component = "mutated-model-result"
				routeAgain, err := catalog.RouteTariff(ctx, "backend", "model")
				if err != nil {
					t.Fatalf("RouteTariff after returned-body mutation: %v", err)
				}
				assertSupportAdvisoryCatalogTariff(t, routeAgain, routeWant)
			}
		})
	}
}

func TestSnapshotCatalogPutPricingWithSupportAdvisoryIsImmutableAndReplayable(t *testing.T) {
	t.Parallel()

	catalog := NewSnapshotCatalog()
	pricing := supportAdvisoryCatalogPricing("advisory-pricing", "v1", 220)
	schemas := supportAdvisoryCatalogSchemas(metering.ComponentCacheReadInputToken)
	want := supportAdvisoryExpectedTariff(t, pricing, schemas, economics.SupportAdvisoryVersionV1)

	if err := catalog.PutPricingWithSupportAdvisory(pricing, schemas, economics.SupportAdvisoryVersionV1); err != nil {
		t.Fatalf("initial advisory publication: %v", err)
	}
	if err := catalog.PutPricingWithSupportAdvisory(pricing, schemas, economics.SupportAdvisoryVersionV1); err != nil {
		t.Fatalf("identical advisory replay: %v", err)
	}

	changedSchemas := supportAdvisoryCatalogSchemas(metering.ComponentCacheWriteInputToken)
	if err := catalog.PutPricingWithSupportAdvisory(pricing, changedSchemas, economics.SupportAdvisoryVersionV1); !errors.Is(err, ErrSnapshotImmutable) {
		t.Fatalf("same-ref changed schema error = %v, want ErrSnapshotImmutable", err)
	}
	if err := catalog.PutPricingWithSchemas(pricing, schemas); !errors.Is(err, ErrSnapshotImmutable) {
		t.Fatalf("same-ref advisory downgrade error = %v, want ErrSnapshotImmutable", err)
	}
	got, err := catalog.Tariff(pricing.Ref)
	if err != nil {
		t.Fatalf("Tariff after immutable conflicts: %v", err)
	}
	assertSupportAdvisoryCatalogTariff(t, got, want)

	newIdentity := pricing
	newIdentity.Ref.Version = "v2"
	newWant := supportAdvisoryExpectedTariff(t, newIdentity, schemas, economics.SupportAdvisoryVersionV1)
	if err := catalog.PutPricingWithSupportAdvisory(newIdentity, schemas, economics.SupportAdvisoryVersionV1); err != nil {
		t.Fatalf("new pricing version publication: %v", err)
	}
	newGot, err := catalog.Tariff(newIdentity.Ref)
	if err != nil {
		t.Fatalf("Tariff for new pricing version: %v", err)
	}
	assertSupportAdvisoryCatalogTariff(t, newGot, newWant)
}

func TestSnapshotCatalogPutPricingWithSupportAdvisoryRejectsInvalidMaterialAtomically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		schemas []metering.ComponentSchema
		wantErr error
	}{
		{name: "unsupported version", version: "component-support-advisory-v2", schemas: supportAdvisoryCatalogSchemas(metering.ComponentCacheReadInputToken), wantErr: economics.ErrInvalidTariffSnapshot},
		{name: "whitespace version", version: " ", schemas: supportAdvisoryCatalogSchemas(metering.ComponentCacheReadInputToken), wantErr: economics.ErrInvalidTariffSnapshot},
		{name: "malformed schemas", version: economics.SupportAdvisoryVersionV1, schemas: []metering.ComponentSchema{{ID: " ", Version: "1"}}, wantErr: economics.ErrInvalidTariffSnapshot},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			catalog := NewSnapshotCatalog()
			pricing := supportAdvisoryCatalogPricing("invalid-advisory-pricing", "v1", 220)
			err := catalog.PutPricingWithSupportAdvisory(pricing, tc.schemas, tc.version)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("publication error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
			assertSupportAdvisoryCatalogPublicationAbsent(t, catalog, pricing.Ref)
		})
	}
}

func TestSnapshotCatalogPutPricingWithSupportAdvisoryHandlesNilCatalog(t *testing.T) {
	t.Parallel()

	var catalog *SnapshotCatalog
	err := catalog.PutPricingWithSupportAdvisory(
		supportAdvisoryCatalogPricing("nil-advisory-pricing", "v1", 220),
		supportAdvisoryCatalogSchemas(metering.ComponentCacheReadInputToken),
		economics.SupportAdvisoryVersionV1,
	)
	if !errors.Is(err, errNilSnapshotCatalog) {
		t.Fatalf("nil catalog publication error = %v, want errNilSnapshotCatalog", err)
	}
}

func TestSnapshotCatalogPutTariffOnlyIdentityDoesNotBindAsCustomerPricing(t *testing.T) {
	t.Parallel()

	catalog := NewSnapshotCatalog()
	pricing := supportAdvisoryCatalogPricing("tariff-only-pricing", "v1", 220)
	tariff, err := billing.PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatalf("PricingSnapshotToTariff: %v", err)
	}
	if err := catalog.PutTariff(tariff); err != nil {
		t.Fatalf("PutTariff: %v", err)
	}
	if err := catalog.SetRoutePricing("backend", "model", pricing.Ref); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("SetRoutePricing for tariff-only key = %v, want ErrSnapshotNotFound", err)
	}
}

func supportAdvisoryCatalogPricing(id, version string, inputRate int64) billing.PricingSnapshot {
	return billing.PricingSnapshot{
		Ref:                 billing.VersionRef{ID: id, Version: version},
		Currency:            "USD",
		InputPerMillionNano: inputRate,
		InputRatePresent:    true,
	}
}

func supportAdvisoryCatalogSchemas(child string) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID:      "catalog-support-advisory-schema",
		Version: "1",
		Relationships: []metering.ComponentRelationship{{
			Kind: metering.RelationshipSubset,
			Parent: metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
				Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
			},
			Child: metering.ComponentKey{
				Direction: metering.DirectionInput, Component: child,
				Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
			},
		}},
	}}
}

func supportAdvisoryExpectedTariff(t *testing.T, pricing billing.PricingSnapshot, schemas []metering.ComponentSchema, version string) economics.TariffSnapshot {
	t.Helper()

	tariff, err := billing.PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatalf("independent PricingSnapshotToTariff fixture: %v", err)
	}
	if len(schemas) > 0 || version != "" {
		tariff.Schemas = schemas
		tariff.SupportAdvisoryVersion = version
		tariff.Content = economics.SnapshotContentRef{}
		tariff, err = tariff.Canonical()
		if err != nil {
			t.Fatalf("canonical expected tariff fixture: %v", err)
		}
	}
	return tariff
}

func assertSupportAdvisoryCatalogTariff(t *testing.T, got, want economics.TariffSnapshot) {
	t.Helper()

	if got.Ref.ID != want.Ref.ID || got.Ref.Version != want.Ref.Version || got.Ref.RaterID != want.Ref.RaterID {
		t.Fatalf("tariff ref = %+v, want %+v", got.Ref, want.Ref)
	}
	if got.SupportAdvisoryVersion != want.SupportAdvisoryVersion {
		t.Fatalf("tariff SupportAdvisoryVersion = %q, want %q", got.SupportAdvisoryVersion, want.SupportAdvisoryVersion)
	}
	if !reflect.DeepEqual(got.Schemas, want.Schemas) {
		t.Fatalf("tariff schemas = %+v, want %+v", got.Schemas, want.Schemas)
	}
	if got.Content.ContentRef != want.Content.ContentRef || got.Content.ContentHash != want.Content.ContentHash || got.ContentHash() != want.ContentHash() {
		t.Fatalf("tariff content = %+v / %s, want %+v / %s", got.Content, got.ContentHash(), want.Content, want.ContentHash())
	}
}

func assertSupportAdvisoryCatalogPublicationAbsent(t *testing.T, catalog *SnapshotCatalog, ref billing.VersionRef) {
	t.Helper()

	if _, err := catalog.Tariff(ref); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("Tariff after rejected publication = %v, want ErrSnapshotNotFound", err)
	}
	if err := catalog.SetRoutePricing("backend", "model", ref); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("SetRoutePricing after rejected publication = %v, want ErrSnapshotNotFound", err)
	}
}
