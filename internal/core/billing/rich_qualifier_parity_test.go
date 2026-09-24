package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 14 Finding 3: a rich quote may derive effective qualifiers ONLY from
// the immutable tariff snapshot. An external/ad-hoc qualifier overlay that
// would choose a different conditional price must fail closed before
// admission. Snapshot-embedded qualifiers select the identical rule, amount,
// and content binding at quote and terminal settlement.

func qualifierTariff(t *testing.T, id, version string, embedded []metering.Dimension, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.RatingCatalogView{
		Currency: "USD", Rules: rules, EffectiveQualifiers: embedded,
	}.Tariff(economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: id, Version: version}, RaterID: "reference"})
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func qualifierTextRule(t *testing.T, id, price string, conditions ...economics.QualifierCondition) economics.RatingRule {
	t.Helper()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	unitPrice, err := metering.ParseDecimal(price)
	if err != nil {
		t.Fatal(err)
	}
	return economics.RatingRule{ID: id, Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: &unitPrice, Conditions: conditions}
}

func qualifierQuoteInput(t *testing.T, tariff economics.TariffSnapshot) RichQuoteInput {
	t.Helper()
	upper, err := metering.ParseDecimal("100")
	if err != nil {
		t.Fatal(err)
	}
	return RichQuoteInput{
		Currency: "USD",
		Policy: ChargePolicy{
			Ref: VersionRef{ID: "retail-policy", Version: "v10"}, PricingRef: VersionRef{ID: tariff.Ref.ID, Version: tariff.Ref.Version},
			Scope: ChargeSurfacedTurn, IncludeInputTokens: true,
			Retail: &RetailSelectionPolicy{Mode: RetailSelectionSurfacedWinner, Basis: RetailBasisIndependent},
		},
		BaseTariff: tariff,
		Routes:     []RichQuoteRoute{{ID: "a:m1", Backend: "a", Model: "m1"}},
		Bounds: []RichComponentBound{{
			Key:         metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"},
			Upper:       upper,
			Enforceable: true,
		}},
	}
}

func TestRichQuoteRejectsExternalQualifierOverlay(t *testing.T) {
	t.Parallel()
	// The tariff carries NO embedded qualifiers, so the region condition can
	// never be satisfied from snapshot material. There is no external overlay
	// seam: the quote must fail closed rather than guess a price.
	tariff := qualifierTariff(t, "cond-pricing", "v1", nil, []economics.RatingRule{
		qualifierTextRule(t, "text-us", "0.000000002", economics.QualifierCondition{Name: "region", Value: "us"}),
	})
	in := qualifierQuoteInput(t, tariff)
	if _, err := EstimateRichCustomerCharge(in); !errors.Is(err, ErrEstimateUnbounded) {
		t.Fatalf("unbound conditional rule = %v, want ErrEstimateUnbounded", err)
	}
}

func TestRichQuoteEmbeddedQualifierParityWithSettlement(t *testing.T) {
	t.Parallel()
	embedded := []metering.Dimension{{Name: "region", Value: "us"}}
	tariff := qualifierTariff(t, "cond-pricing", "v1", embedded, []economics.RatingRule{
		qualifierTextRule(t, "text-us", "0.000000002", economics.QualifierCondition{Name: "region", Value: "us"}),
		qualifierTextRule(t, "text-eu", "0.000000007", economics.QualifierCondition{Name: "region", Value: "eu"}),
	})
	quote, err := EstimateRichCustomerCharge(qualifierQuoteInput(t, tariff))
	if err != nil {
		t.Fatal(err)
	}
	// 100 text tokens at the us conditional price: 200 nano, no fixed fees.
	if quote.Amount.Nano != 200 || quote.Amount.Currency != "USD" {
		t.Fatalf("quote = %+v, want 200 USD nano from the us rule", quote.Amount)
	}
	if len(quote.RouteTariffs) != 1 || quote.RouteTariffs[0].RouteID != "a:m1" {
		t.Fatalf("quote bindings = %+v, want one entry for a:m1", quote.RouteTariffs)
	}
	if quote.RouteTariffs[0].ContentHash != tariff.Content.ContentHash {
		t.Fatalf("quote hash = %q, want tariff content %q", quote.RouteTariffs[0].ContentHash, tariff.Content.ContentHash)
	}
	policy := qualifierQuoteInput(t, tariff).Policy
	call := retailSelectionCall(t, policy, "b-winner")
	observation := phase10RetailObservation(t, call.CallID, "b-winner", "winner-usage",
		metering.OriginProvider, metering.BoundaryBackendIngress,
		phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "100"},
	)
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes, observation)
	leg.BackendID = "a"
	leg.ModelID = "m1"
	settled, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg},
		MaxCustomerCharge: Money{Nano: 1_000_000_000, Currency: "USD"},
		CustomerPricing:   PricingSnapshot{Ref: VersionRef{ID: tariff.Ref.ID, Version: tariff.Ref.Version}, Currency: "USD"},
		CustomerPolicy:    policy, CustomerTariff: tariff,
	})
	if err != nil {
		t.Fatalf("settlement: %v", err)
	}
	if settled.CustomerCharge != quote.Amount {
		t.Fatalf("settlement %+v != quote %+v; embedded qualifier must select the same rule", settled.CustomerCharge, quote.Amount)
	}
	foundRegion := false
	for _, qualifier := range settled.CustomerValuation.EffectiveQualifiers {
		if qualifier.Name == "region" && qualifier.Value == "us" {
			foundRegion = true
		}
	}
	if !foundRegion {
		t.Fatalf("valuation qualifiers = %+v, want embedded region=us", settled.CustomerValuation.EffectiveQualifiers)
	}
	if len(settled.RouteTariffs) != 1 || settled.RouteTariffs[0] != quote.RouteTariffs[0] {
		t.Fatalf("settlement bindings = %+v, want quote bindings %+v", settled.RouteTariffs, quote.RouteTariffs)
	}
	if err := CheckSettledRouteTariffs(quote.RouteTariffs, settled.RouteTariffs, settled.CustomerCharge); err != nil {
		t.Fatalf("parity check = %v, want nil", err)
	}
	// Same version with a changed embedded qualifier is a different content
	// hash and fails the terminal fence atomically.
	euTariff := qualifierTariff(t, "cond-pricing", "v1", []metering.Dimension{{Name: "region", Value: "eu"}}, []economics.RatingRule{
		qualifierTextRule(t, "text-us", "0.000000002", economics.QualifierCondition{Name: "region", Value: "us"}),
		qualifierTextRule(t, "text-eu", "0.000000007", economics.QualifierCondition{Name: "region", Value: "eu"}),
	})
	if euTariff.Content.ContentHash == tariff.Content.ContentHash {
		t.Fatal("changed embedded qualifier must change the canonical content hash")
	}
	euQuote, err := EstimateRichCustomerCharge(qualifierQuoteInput(t, euTariff))
	if err != nil {
		t.Fatal(err)
	}
	if euQuote.Amount.Nano != 700 {
		t.Fatalf("eu quote = %+v, want 700 USD nano from the eu rule", euQuote.Amount)
	}
	if err := CheckSettledRouteTariffs(quote.RouteTariffs, euQuote.RouteTariffs, euQuote.Amount); !errors.Is(err, ErrRatingSnapshotMismatch) {
		t.Fatalf("cross-qualifier settlement = %v, want ErrRatingSnapshotMismatch", err)
	}
}
