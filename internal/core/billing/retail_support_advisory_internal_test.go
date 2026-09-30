package billing

import (
	"context"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func retailInternalAdvisoryTariff(t *testing.T, tariff economics.TariffSnapshot) economics.TariffSnapshot {
	t.Helper()
	tariff.SupportAdvisoryVersion = economics.SupportAdvisoryVersionV1
	tariff.Content = economics.SnapshotContentRef{}
	tariff, err := tariff.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func TestRetailSupportAdvisoryPreNarrowingGroupBound(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "b-0")
	call.SubmissionID = "submission-bound"
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	base := retailInternalAdvisoryTariff(t, phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailFixedRule("call", economics.FixedFeeScopeCall, "2"), phase10RetailFixedRule("submission", economics.FixedFeeScopeSubmission, "3"),
	}))
	var observations []metering.Observation
	var refs []metering.ObservationRef
	var legs []CallLegUsageRecord
	var cards []ModelCustomerTariff
	for i := 0; i < economics.MaxValuationRefs; i++ {
		id := fmt.Sprintf("b-%04d", i)
		obs := phase10RetailObservation(t, call.CallID, id, "obs-"+id, metering.OriginLocal, metering.BoundaryBackendIngress, phase10RetailMeasure{key: key, quantity: "1"})
		ref, err := obs.Ref("store-1")
		if err != nil {
			t.Fatal(err)
		}
		observations = append(observations, obs)
		refs = append(refs, ref)
		legs = append(legs, CallLegUsageRecord{BLegID: id, BackendID: "backend", ModelID: id})
		tariff := retailInternalAdvisoryTariff(t, phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-" + id, Version: "v1"}, RaterID: "reference"}, nil))
		cards = append(cards, ModelCustomerTariff{BackendID: "backend", ModelID: id, Tariff: tariff})
	}
	groups, err := retailQuantityGroups(observations, refs, legs, cards, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != economics.MaxValuationRefs {
		t.Fatalf("pre-narrowing groups %d", len(groups))
	}
	payer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID}
	var result RetailRatingResult
	for _, group := range groups {
		if len(group.observations) != 1 || len(group.refs) != 1 {
			t.Fatal("groups must partition original nonempty observations")
		}
		input := retailEconomicsInput(call, policy, group.tariff, group.observations, group.refs, nil, nil, payer, call.FinishedAt, "call:"+call.CallID.String(), metering.SubjectBillingCall)
		if err := input.Validate(); err != nil {
			t.Fatal(err)
		}
		valuation := newValuation(input, group.tariff, group.refs, nil)
		narrowed, err := (retailInferenceSelection{}).narrowValuationInputs(valuation, group.observations)
		if err != nil {
			t.Fatal(err)
		}
		if len(narrowed.InputObservations) != 0 || len(narrowed.SupportAdvisoryContexts) != 1 {
			t.Fatal("empty narrowed group lost enabled source context")
		}
		combineRetailValuation(&result.InferenceValuation, narrowed)
	}
	// The two fixed groups use the same frozen base tariff, so context dedup
	// reduces 1027 independently rated source groups to 1026 distinct contexts.
	for _, scope := range []economics.FixedFeeScope{economics.FixedFeeScopeCall, economics.FixedFeeScopeSubmission} {
		subject := metering.SubjectBillingCall
		scopeKey := "call:" + call.CallID.String()
		if scope == economics.FixedFeeScopeSubmission {
			subject = metering.SubjectSubmission
			scopeKey = "submission:" + call.SubmissionID
		}
		input := retailEconomicsInput(call, policy, base, nil, refs[:1], nil, nil, payer, call.FinishedAt, scopeKey, subject)
		valuation, err := rateRetailValuation(context.Background(), base, input, nil, true, string(scope), nil)
		if err != nil {
			t.Fatal(err)
		}
		combineRetailValuation(&result.CommercialValuation, valuation)
	}
	proxyTariff := retailInternalAdvisoryTariff(t, phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "proxy", Version: "v1"}, RaterID: "reference"}, []economics.RatingRule{phase10RetailLinearRule("proxy", key, "7")}))
	proxy, err := rateRetailProxyService(context.Background(), call, policy, "USD", "store-1", payer, call.FinishedAt, &RetailProxyServiceInput{Tariff: proxyTariff, Observations: []metering.Observation{phase10RetailProxyObservation(t, call.CallID, "proxy", "1", key)}})
	if err != nil {
		t.Fatal(err)
	}
	result.ProxyServiceValuation = &proxy
	composeRetailValuation(&result, call, refs, base, policy, "store-1")
	if economics.MaxSupportAdvisoryContexts != len(groups)+2+1 {
		t.Fatal("published source-group bound differs from construction")
	}
	if got := len(result.Valuation.SupportAdvisoryContexts); got != economics.MaxValuationRefs+2 {
		t.Fatalf("merged contexts=%d, want 1026", got)
	}
	if result.Valuation.SupportAdvisory != nil {
		t.Fatal("clean enabled groups fabricated advice")
	}
	if err := result.Valuation.Validate(); err != nil {
		t.Fatal(err)
	}
	money, err := retailValuationMoney(result.Valuation)
	if err != nil || money.Nano != 12_000_000_000 {
		t.Fatalf("fixed/proxy money=%+v err=%v", money, err)
	}
}

func TestRetailSupportAdvisoryMergeOwnsSourceReports(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	right := key.Clone()
	right.Component = metering.ComponentAudioToken
	var composite economics.Valuation
	var sources []economics.Valuation
	for _, id := range []string{"a", "z"} {
		tariff := retailInternalAdvisoryTariff(t, phase10RetailTariffWithRef(t, economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: id, Version: "v1"}, RaterID: "reference"}, nil))
		sourceContext := economics.SupportAdvisoryContext{Version: economics.SupportAdvisoryVersionV1, Tariff: tariff.Ref, TariffContent: tariff.Content}
		source := economics.Valuation{ID: id, SupportAdvisoryContexts: []economics.SupportAdvisoryContext{sourceContext}, SupportAdvisory: &economics.SupportAdvisoryReport{
			Pairs:              []economics.SupportAdvisoryPair{{ContextKey: sourceContext.Key(), ScopeKey: "scope-" + id, Left: key.Clone(), Right: right.Clone()}},
			IncompleteContexts: []economics.SupportAdvisoryIncomplete{{ContextKey: sourceContext.Key(), Reason: economics.SupportAdvisoryEvidenceUnavailable}},
		}}
		sources = append(sources, source)
		combineRetailValuation(&composite, source)
	}
	if len(composite.SupportAdvisoryContexts) != 2 || composite.SupportAdvisory == nil || len(composite.SupportAdvisory.Pairs) != 2 || len(composite.SupportAdvisory.IncompleteContexts) != 2 {
		t.Fatalf("merged advice=%+v", composite)
	}
	for i, source := range sources {
		pair := composite.SupportAdvisory.Pairs[i]
		if pair.ContextKey != source.SupportAdvisoryContexts[0].Key() || pair.ScopeKey != "scope-"+source.ID || pair.Left.CanonicalKey() != key.CanonicalKey() || pair.Right.CanonicalKey() != right.CanonicalKey() {
			t.Fatalf("source pair rewritten: %+v", pair)
		}
		if composite.SupportAdvisory.IncompleteContexts[i].ContextKey != pair.ContextKey || composite.SupportAdvisory.IncompleteContexts[i].Reason != economics.SupportAdvisoryEvidenceUnavailable {
			t.Fatal("source incompleteness lost")
		}
		source.SupportAdvisory.Pairs[0].Left.Component = "vendor:mutated"
		source.SupportAdvisory.IncompleteContexts[0].Reason = economics.SupportAdvisoryGraphBudget
		source.SupportAdvisoryContexts[0].Tariff.ID = "mutated"
	}
	for _, pair := range composite.SupportAdvisory.Pairs {
		if pair.Left.Component != key.Component {
			t.Fatal("merged report aliases source")
		}
	}
	for _, entry := range composite.SupportAdvisory.IncompleteContexts {
		if entry.Reason != economics.SupportAdvisoryEvidenceUnavailable {
			t.Fatal("merged incomplete entry aliases source")
		}
	}
	for _, sourceContext := range composite.SupportAdvisoryContexts {
		if sourceContext.Tariff.ID == "mutated" {
			t.Fatal("merged context aliases source")
		}
	}
}
