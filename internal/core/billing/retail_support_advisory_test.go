package billing_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestRetailSupportAdvisoryRouteProvenance(t *testing.T) {
	t.Parallel()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	measures := []metering.Measure{b1Measure(t, b2bTextInKey(), "8"), b1Measure(t, b2bAudioInKey(), "3")}
	firstObs := b2aRetailObservation(t, callID, "route-a", measures)
	call, first, policy := b2aRetailCall(t, callID, firstObs)
	policy.Retail.Mode = billing.RetailSelectionAllAttributable
	call.ExpectedBLegIDs = append(call.ExpectedBLegIDs, "b-leg-2")
	first.BackendID, first.ModelID = "backend-a", "model-a"
	second := first.Clone()
	second.BLegID, second.AttemptSeq = "b-leg-2", 2
	second.BackendID, second.ModelID = "backend-b", "model-b"
	second.Observations = []metering.Observation{b2aRetailObservation(t, callID, "route-b", measures)}
	second.Observations[0].Subject.BLegID = second.BLegID
	second.Observations[0].Correlation.BLegID = second.BLegID
	base := b1Tariff(t, b2aRetailTariffID, nil, nil)
	legacy := b1Tariff(t, "a-route-legacy", b2bChildOnlyRules(t)[:2], b2bPartitionSchemas(metering.RelationshipSubset))
	enabled := retailAdvisoryEnabled(t, b1Tariff(t, "z-route-enabled", legacy.Rules, legacy.Schemas))
	in := billing.RetailRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{first, second}, Policy: policy, Tariff: base,
		ModelTariffs: []billing.ModelCustomerTariff{{BackendID: first.BackendID, ModelID: first.ModelID, Tariff: legacy}, {BackendID: second.BackendID, ModelID: second.ModelID, Tariff: enabled}},
		Payer:        metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	}
	mixed, err := billing.RateSelectedRetailBLegs(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	assertRetailAdvisoryRoutes(t, mixed, base, []economics.TariffSnapshot{enabled}, []string{second.BLegID})
	replay, err := billing.RateSelectedRetailBLegs(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Valuation.Fingerprint() != mixed.Valuation.Fingerprint() {
		t.Fatal("frozen replay changed")
	}
	// New frozen material changes reporting semantics while retaining every rate.
	republished := legacy.Clone()
	republished.Ref.Version = "v2"
	republished = retailAdvisoryEnabled(t, republished)
	in.ModelTariffs[0].Tariff = republished
	both, err := billing.RateSelectedRetailBLegs(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	assertRetailAdvisoryRoutes(t, both, base, []economics.TariffSnapshot{republished, enabled}, []string{first.BLegID, second.BLegID})
	if mixed.Valuation.ID == both.Valuation.ID || mixed.Valuation.ContextHash() == both.Valuation.ContextHash() {
		t.Fatal("changed source semantics did not change interpretation identity")
	}
	if !reflect.DeepEqual(mixed.Valuation.Totals, both.Valuation.Totals) {
		t.Fatal("reporting changed money")
	}
	for i := range mixed.Valuation.Lines {
		a, b := mixed.Valuation.Lines[i].Clone(), both.Valuation.Lines[i].Clone()
		a.ID, b.ID = "", "" // Duplicate suffix binds the source interpretation.
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("reporting changed payable line %d", i)
		}
	}
}

func retailAdvisoryEnabled(t *testing.T, tariff economics.TariffSnapshot) economics.TariffSnapshot {
	t.Helper()
	tariff.SupportAdvisoryVersion = economics.SupportAdvisoryVersionV1
	tariff.Content = economics.SnapshotContentRef{}
	canonical, err := tariff.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func assertRetailAdvisoryRoutes(t *testing.T, result billing.RetailRatingResult, base economics.TariffSnapshot, sources []economics.TariffSnapshot, legs []string) {
	t.Helper()
	// Each leg charges 8*2 + 3*5 = 31 USD. Unknown overlap never waives it.
	if result.CustomerCharge.Nano != 62_000_000_000 || result.Valuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("money/completeness: %+v", result)
	}
	if result.Valuation.Tariff.ID != base.Ref.ID {
		t.Fatal("composite did not restore base tariff")
	}
	if len(result.Valuation.SupportAdvisoryContexts) != len(sources) || result.Valuation.SupportAdvisory == nil || len(result.Valuation.SupportAdvisory.Pairs) != len(sources) {
		t.Fatalf("source advice lost: contexts=%+v report=%+v", result.Valuation.SupportAdvisoryContexts, result.Valuation.SupportAdvisory)
	}
	for i, source := range sources {
		expected := economics.SupportAdvisoryContext{Version: economics.SupportAdvisoryVersionV1, Tariff: source.Ref, TariffContent: source.Content}
		found := false
		for _, pair := range result.Valuation.SupportAdvisory.Pairs {
			if pair.ContextKey != expected.Key() {
				continue
			}
			found = true
			if !strings.Contains(pair.ScopeKey, legs[i]) {
				t.Fatalf("wrong source scope: %+v", pair)
			}
			keys := map[string]bool{pair.Left.CanonicalKey(): true, pair.Right.CanonicalKey(): true}
			if !keys[b2bTextInKey().CanonicalKey()] || !keys[b2bAudioInKey().CanonicalKey()] {
				t.Fatalf("wrong components: %+v", pair)
			}
		}
		if !found {
			t.Fatalf("missing source pair %s", expected.Key())
		}
	}
	if len(result.Valuation.Lines) != 4 {
		t.Fatalf("line count %d", len(result.Valuation.Lines))
	}
	seen := map[string]bool{}
	for _, line := range result.Valuation.Lines {
		if seen[line.ID] {
			t.Fatal("duplicate line ID")
		}
		seen[line.ID] = true
	}
	if err := result.Valuation.Validate(); err != nil {
		t.Fatal(err)
	}
}
