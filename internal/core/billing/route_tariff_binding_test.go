package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 14.1 BLOCKER 1 RED: every route tariff used to form a rich quote must
// be frozen as a deterministic binding (route key + tariff VersionRef +
// content hash) that terminal settlement checks before posting.

func blockerBaseTariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	price, err := metering.ParseDecimal("0.000000001")
	if err != nil {
		t.Fatal(err)
	}
	fee, err := metering.ParseDecimal("0.000000005")
	if err != nil {
		t.Fatal(err)
	}
	tariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing", Version: "v3"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{
			{ID: "text-input", Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: &price},
			{ID: "call-fee", Kind: economics.RatingRuleFixed, Currency: "USD", FixedAmount: &fee, FixedScope: economics.FixedFeeScopeCall},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func blockerModelTariff(t *testing.T, id, version, price string) economics.TariffSnapshot {
	t.Helper()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	priceDecimal, err := metering.ParseDecimal(price)
	if err != nil {
		t.Fatal(err)
	}
	tariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: id, Version: version}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{{ID: "text-input", Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: &priceDecimal}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func blockerQuoteInput(t *testing.T, base economics.TariffSnapshot, routes []RichQuoteRoute) RichQuoteInput {
	t.Helper()
	policy := ChargePolicy{
		Ref: VersionRef{ID: "retail-policy", Version: "v10"}, PricingRef: VersionRef{ID: base.Ref.ID, Version: base.Ref.Version},
		Scope: ChargeSurfacedTurn, IncludeInputTokens: true, IncludeFixedCharges: true, IncludeResourceCharges: true,
		Retail: &RetailSelectionPolicy{Mode: RetailSelectionSurfacedWinner, Basis: RetailBasisIndependent},
	}
	textUpper, err := metering.ParseDecimal("100")
	if err != nil {
		t.Fatal(err)
	}
	return RichQuoteInput{
		Currency: "USD", Policy: policy, BaseTariff: base, Routes: routes,
		Bounds: []RichComponentBound{{
			Key:         metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"},
			Upper:       textUpper,
			Enforceable: true,
		}},
	}
}

func TestRichQuoteExposesDeterministicRouteTariffBindings(t *testing.T) {
	t.Parallel()
	base := blockerBaseTariff(t)
	modelV1 := blockerModelTariff(t, "model-pricing", "v1", "0.000000002")
	in := blockerQuoteInput(t, base, []RichQuoteRoute{
		{ID: "b:m2", Backend: "b", Model: "m2"},
		{ID: "a:m1", Backend: "a", Model: "m1", Tariff: modelV1},
	})
	bound, err := EstimateRichCustomerCharge(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.RouteTariffs) != 2 {
		t.Fatalf("route bindings = %+v, want one per quoted route", bound.RouteTariffs)
	}
	if bound.RouteTariffs[0].RouteID != "a:m1" || bound.RouteTariffs[1].RouteID != "b:m2" {
		t.Fatalf("route bindings not deterministically ordered: %+v", bound.RouteTariffs)
	}
	byRoute := map[string]RouteTariffBinding{}
	for _, binding := range bound.RouteTariffs {
		byRoute[binding.RouteID] = binding
	}
	if byRoute["a:m1"].TariffID != "model-pricing" || byRoute["a:m1"].TariffVersion != "v1" || len(byRoute["a:m1"].ContentHash) != 64 {
		t.Fatalf("model route binding = %+v, want model-pricing v1 with content hash", byRoute["a:m1"])
	}
	if byRoute["b:m2"].TariffID != base.Ref.ID || byRoute["b:m2"].TariffVersion != base.Ref.Version {
		t.Fatalf("base route binding = %+v, want base tariff identity", byRoute["b:m2"])
	}
	if byRoute["b:m2"].ContentHash != base.Content.ContentHash {
		t.Fatalf("base content hash = %q, want %q", byRoute["b:m2"].ContentHash, base.Content.ContentHash)
	}
	// Reordered route input must yield identical bindings.
	swapped := in
	swapped.Routes = []RichQuoteRoute{{ID: "a:m1", Backend: "a", Model: "m1", Tariff: modelV1}, {ID: "b:m2", Backend: "b", Model: "m2"}}
	again, err := EstimateRichCustomerCharge(swapped)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.RouteTariffs) != 2 || again.RouteTariffs[0] != bound.RouteTariffs[0] || again.RouteTariffs[1] != bound.RouteTariffs[1] {
		t.Fatalf("reordered bindings = %+v, want %+v", again.RouteTariffs, bound.RouteTariffs)
	}
}

func TestRichQuoteBindingsDetectVersionAndContentChanges(t *testing.T) {
	t.Parallel()
	base := blockerBaseTariff(t)
	quoteWith := func(t *testing.T, model economics.TariffSnapshot) []RouteTariffBinding {
		t.Helper()
		bound, err := EstimateRichCustomerCharge(blockerQuoteInput(t, base, []RichQuoteRoute{{ID: "a:m1", Backend: "a", Model: "m1", Tariff: model}}))
		if err != nil {
			t.Fatal(err)
		}
		return bound.RouteTariffs
	}
	v1 := quoteWith(t, blockerModelTariff(t, "model-pricing", "v1", "0.000002"))
	v2 := quoteWith(t, blockerModelTariff(t, "model-pricing", "v2", "0.000002"))
	if v1[0] == v2[0] {
		t.Fatalf("v1 vs v2 bindings identical: %+v", v1[0])
	}
	mutated := quoteWith(t, blockerModelTariff(t, "model-pricing", "v1", "0.000009"))
	if mutated[0] == v1[0] {
		t.Fatalf("same-version content mutation undetected: %+v vs %+v", mutated[0], v1[0])
	}
	if mutated[0].TariffVersion != "v1" || mutated[0].ContentHash == v1[0].ContentHash {
		t.Fatalf("mutated binding = %+v, want v1 with distinct hash", mutated[0])
	}
}

func TestAdmitExposurePersistsRouteTariffBindingsDeterministically(t *testing.T) {
	t.Parallel()
	admit := exposureAdmit("call-1", 40)
	admit.RouteTariffs = []RouteTariffBinding{
		{RouteID: "b:m2", TariffID: "retail-pricing", TariffVersion: "v3", ContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{RouteID: "a:m1", TariffID: "model-pricing", TariffVersion: "v1", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	admitted, err := EvaluateAdmit(exposurePrepaid(100), nil, admit)
	if err != nil {
		t.Fatal(err)
	}
	if len(admitted.RouteTariffs) != 2 || admitted.RouteTariffs[0].RouteID != "a:m1" || admitted.RouteTariffs[1].RouteID != "b:m2" {
		t.Fatalf("admitted bindings not sorted: %+v", admitted.RouteTariffs)
	}
	reordered := exposureAdmit("call-1", 40)
	reordered.RouteTariffs = []RouteTariffBinding{admit.RouteTariffs[0], admit.RouteTariffs[1]}
	if err := CheckExposureReplay(admitted, reordered); err != nil {
		t.Fatalf("reordered binding replay = %v, want idempotent", err)
	}
	tampered := exposureAdmit("call-1", 40)
	tampered.RouteTariffs = []RouteTariffBinding{{
		RouteID: "a:m1", TariffID: "model-pricing", TariffVersion: "v2",
		ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	if err := CheckExposureReplay(admitted, tampered); !errors.Is(err, ErrExposureConflict) {
		t.Fatalf("tampered binding replay = %v, want ErrExposureConflict", err)
	}
	for _, tc := range []struct {
		name     string
		bindings []RouteTariffBinding
	}{
		{"empty route", []RouteTariffBinding{{RouteID: "", TariffID: "m", TariffVersion: "v1", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{"duplicate route", []RouteTariffBinding{
			{RouteID: "a:m1", TariffID: "m", TariffVersion: "v1", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{RouteID: "a:m1", TariffID: "m", TariffVersion: "v1", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		}},
		{"bad hash", []RouteTariffBinding{{RouteID: "a:m1", TariffID: "m", TariffVersion: "v1", ContentHash: "not-a-hash"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bad := exposureAdmit("call-1", 40)
			bad.RouteTariffs = tc.bindings
			if _, err := EvaluateAdmit(exposurePrepaid(100), nil, bad); !errors.Is(err, ErrExposureInvalid) {
				t.Fatalf("invalid bindings = %v, want ErrExposureInvalid", err)
			}
		})
	}
}

func TestCheckSettledRouteTariffsFailsClosedOnMismatch(t *testing.T) {
	t.Parallel()
	admitted := []RouteTariffBinding{
		{RouteID: "a:m1", TariffID: "model-pricing", TariffVersion: "v1", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{RouteID: "b:m2", TariffID: "retail-pricing", TariffVersion: "v3", ContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	if err := CheckSettledRouteTariffs(admitted, admitted, exposureUSD(25)); err != nil {
		t.Fatalf("exact match = %v, want nil", err)
	}
	// Settlement may use a subset (failover winner only).
	if err := CheckSettledRouteTariffs(admitted, admitted[:1], exposureUSD(25)); err != nil {
		t.Fatalf("rated subset = %v, want nil", err)
	}
	for _, tc := range []struct {
		name string
		used []RouteTariffBinding
	}{
		{"version change", []RouteTariffBinding{{RouteID: "a:m1", TariffID: "model-pricing", TariffVersion: "v2", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{"content change", []RouteTariffBinding{{RouteID: "a:m1", TariffID: "model-pricing", TariffVersion: "v1", ContentHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}},
		{"route swap", []RouteTariffBinding{{RouteID: "c:m3", TariffID: "model-pricing", TariffVersion: "v1", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{"missing binding", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := CheckSettledRouteTariffs(admitted, tc.used, exposureUSD(25)); !errors.Is(err, ErrRatingSnapshotMismatch) {
				t.Fatalf("mismatch = %v, want ErrRatingSnapshotMismatch", err)
			}
		})
	}
	// The ONLY legacy compatibility success without tariff attestation is
	// admitted-empty + used-empty. A used binding against a legacy-empty
	// exposure, and an omitted binding list against a rich exposure (even at
	// zero charge), both fail closed.
	if err := CheckSettledRouteTariffs(nil, nil, exposureUSD(25)); err != nil {
		t.Fatalf("legacy empty = %v, want nil", err)
	}
	if err := CheckSettledRouteTariffs(nil, admitted[:1], exposureUSD(25)); !errors.Is(err, ErrRatingSnapshotMismatch) {
		t.Fatalf("used against legacy-empty = %v, want ErrRatingSnapshotMismatch", err)
	}
	if err := CheckSettledRouteTariffs(admitted, nil, exposureUSD(0)); !errors.Is(err, ErrRatingSnapshotMismatch) {
		t.Fatalf("omitted binding at zero charge = %v, want ErrRatingSnapshotMismatch", err)
	}
}

func TestAttestNoUsageRoutesEchoesAdmittedBindingsForExecutedRoutes(t *testing.T) {
	t.Parallel()
	admitted := []RouteTariffBinding{
		{RouteID: "a:m1", TariffID: "model-pricing", TariffVersion: "v1", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{RouteID: "b:m2", TariffID: "retail-pricing", TariffVersion: "v3", ContentHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	legs := []CallLegUsageRecord{
		{BLegID: "b-2", BackendID: "b", ModelID: "m2"},
		{BLegID: "b-1", BackendID: "a", ModelID: "m1"},
		{BLegID: "b-1-retry", BackendID: "a", ModelID: "m1"},
	}
	attested, err := AttestNoUsageRoutes(admitted, legs)
	if err != nil {
		t.Fatal(err)
	}
	if len(attested) != 2 || attested[0] != admitted[0] || attested[1] != admitted[1] {
		t.Fatalf("attested = %+v, want sorted admitted entries", attested)
	}
	if _, err := AttestNoUsageRoutes(admitted, []CallLegUsageRecord{{BLegID: "b-x", BackendID: "c", ModelID: "m3"}}); !errors.Is(err, ErrRatingSnapshotMismatch) {
		t.Fatalf("unadmitted leg route = %v, want ErrRatingSnapshotMismatch", err)
	}
	attested, err = AttestNoUsageRoutes(nil, legs)
	if err != nil || len(attested) != 0 {
		t.Fatalf("legacy attestation = %+v, err=%v, want empty", attested, err)
	}
}

func TestRateCallEmitsBindingsForZeroUseSelectedLeg(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	price, err := metering.ParseDecimal("0.000000002")
	if err != nil {
		t.Fatal(err)
	}
	base, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing", Version: "v3"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{{ID: "text-input", Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: &price}},
	)
	if err != nil {
		t.Fatal(err)
	}
	policy := ChargePolicy{
		Ref: VersionRef{ID: "retail-policy", Version: "v10"}, PricingRef: VersionRef{ID: base.Ref.ID, Version: base.Ref.Version},
		Scope: ChargeSurfacedTurn, IncludeInputTokens: true,
		Retail: &RetailSelectionPolicy{Mode: RetailSelectionSurfacedWinner, Basis: RetailBasisIndependent},
	}
	call := retailSelectionCall(t, policy, "b-winner")
	observation := phase10RetailObservation(
		t, call.CallID, "b-winner", "winner-zero",
		metering.OriginProvider, metering.BoundaryBackendIngress,
		phase10RetailMeasure{key: key, quantity: "0"},
	)
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes, observation)
	leg.BackendID = "a"
	leg.ModelID = "m1"
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg},
		MaxCustomerCharge: Money{Nano: 1_000_000_000, Currency: "USD"},
		CustomerPricing:   PricingSnapshot{Ref: VersionRef{ID: base.Ref.ID, Version: base.Ref.Version}, Currency: "USD"},
		CustomerPolicy:    policy, CustomerTariff: base,
	})
	if err != nil {
		t.Fatalf("zero-use RateCall: %v", err)
	}
	if result.CustomerCharge.Nano != 0 {
		t.Fatalf("zero-use charge = %+v, want zero", result.CustomerCharge)
	}
	if len(result.RouteTariffs) != 1 || result.RouteTariffs[0].RouteID != "a:m1" || result.RouteTariffs[0].TariffID != base.Ref.ID {
		t.Fatalf("zero-use bindings = %+v, want one base entry for a:m1", result.RouteTariffs)
	}
	if result.RouteTariffs[0].ContentHash != base.Content.ContentHash {
		t.Fatalf("zero-use hash = %q, want %q", result.RouteTariffs[0].ContentHash, base.Content.ContentHash)
	}
}

func TestRateCallPopulatesUsedRouteTariffBindings(t *testing.T) {
	t.Parallel()
	base := blockerBaseTariff(t)
	modelV1 := blockerModelTariff(t, "model-pricing", "v1", "0.000002")
	policy := ChargePolicy{
		Ref: VersionRef{ID: "retail-policy", Version: "v10"}, PricingRef: VersionRef{ID: base.Ref.ID, Version: base.Ref.Version},
		Scope: ChargeSurfacedTurn, IncludeInputTokens: true, IncludeFixedCharges: true, IncludeResourceCharges: true,
		Retail: &RetailSelectionPolicy{Mode: RetailSelectionSurfacedWinner, Basis: RetailBasisIndependent},
	}
	call := retailSelectionCall(t, policy, "b-winner")
	observation := phase10RetailObservation(
		t, call.CallID, "b-winner", "winner-usage",
		metering.OriginProvider, metering.BoundaryBackendIngress,
		phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "100"},
	)
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes, observation)
	leg.BackendID = "a"
	leg.ModelID = "m1"
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg},
		MaxCustomerCharge: Money{Nano: 1_000_000_000, Currency: "USD"},
		CustomerPricing:   PricingSnapshot{Ref: VersionRef{ID: base.Ref.ID, Version: base.Ref.Version}, Currency: "USD"},
		CustomerPolicy:    policy, CustomerTariff: base,
		ModelTariffs: []ModelCustomerTariff{{BackendID: "a", ModelID: "m1", Tariff: modelV1}},
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}
	if len(result.RouteTariffs) != 1 {
		t.Fatalf("used bindings = %+v, want one model entry for a:m1", result.RouteTariffs)
	}
	got := result.RouteTariffs[0]
	if got.RouteID != "a:m1" || got.TariffID != "model-pricing" || got.TariffVersion != "v1" {
		t.Fatalf("used binding = %+v, want a:m1 model-pricing v1", got)
	}
	canonical, err := modelV1.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentHash != canonical.Content.ContentHash {
		t.Fatalf("used hash = %q, want %q", got.ContentHash, canonical.Content.ContentHash)
	}
	// Scalar legacy rating carries no tariff material.
	scalarCallID := mustBillingCallID(t)
	scalarCall := testCallUsageRecord(scalarCallID)
	scalarPricing := PricingSnapshot{
		Ref: VersionRef{ID: "prices", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
	}
	scalarPolicy := ChargePolicy{Ref: VersionRef{ID: "policy", Version: "v2"}, PricingRef: scalarPricing.Ref, Scope: ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true}
	scalar, err := RateCall(CallRatingInput{
		Call: scalarCall, Legs: []CallLegUsageRecord{testCallLegUsageRecord(scalarCallID, "b-win")},
		MaxCustomerCharge: Money{Nano: 1_000_000_000, Currency: "USD"},
		CustomerPricing:   scalarPricing, CustomerPolicy: scalarPolicy,
	})
	if err != nil {
		t.Fatalf("scalar RateCall: %v", err)
	}
	if len(scalar.RouteTariffs) != 0 {
		t.Fatalf("scalar bindings = %+v, want none", scalar.RouteTariffs)
	}
}
