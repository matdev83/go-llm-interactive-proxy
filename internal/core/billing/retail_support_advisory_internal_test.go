package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
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
		if pair.ContextKey != source.SupportAdvisoryContexts[0].Key() || pair.ScopeKey != "scope-"+source.ID || pair.Left.CanonicalKey() != right.CanonicalKey() || pair.Right.CanonicalKey() != key.CanonicalKey() {
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
		if pair.Left.Component != right.Component || pair.Right.Component != key.Component {
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

func retailAdvisoryMergeRatedSource(t *testing.T, id string) (economics.Valuation, economics.SupportAdvisoryContext) {
	t.Helper()
	policy := retailSelectionPolicy(RetailSelectionAllAttributable, RetailBasisIndependent)
	call := retailSelectionCall(t, policy, "advisory-merge-"+id)
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	tariff := retailInternalAdvisoryTariff(t, phase10RetailTariffWithRef(t,
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "advisory-merge-" + id, Version: "v1"}, RaterID: "reference"},
		[]economics.RatingRule{phase10RetailLinearRule("advisory-merge", key, "1")},
	))
	observation := phase10RetailObservation(t, call.CallID, "leg-"+id, "observation-"+id, metering.OriginLocal, metering.BoundaryBackendIngress, phase10RetailMeasure{key: key, quantity: "1"})
	ref, err := observation.Ref("store-1")
	if err != nil {
		t.Fatal(err)
	}
	payer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID}
	input := retailEconomicsInput(call, policy, tariff, []metering.Observation{observation}, []metering.ObservationRef{ref}, nil, nil, payer, call.FinishedAt, "call:"+call.CallID.String(), metering.SubjectBillingCall)
	valuation, err := rateRetailValuation(context.Background(), tariff, input, nil, false, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := valuation.Validate(); err != nil {
		t.Fatalf("source valuation is invalid: %v", err)
	}
	if len(valuation.SupportAdvisoryContexts) != 1 {
		t.Fatalf("source advisory contexts=%d, want 1", len(valuation.SupportAdvisoryContexts))
	}
	return valuation, valuation.SupportAdvisoryContexts[0]
}

func retailAdvisoryMergePair(contextKey, scope string, reverse bool) economics.SupportAdvisoryPair {
	textKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	audioKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentAudioToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	left, right := textKey, audioKey
	if reverse {
		left, right = right, left
	}
	return economics.SupportAdvisoryPair{ContextKey: contextKey, ScopeKey: scope, Left: left, Right: right}
}

func retailAdvisoryMergeReport(contextKey string, start, end int, incomplete ...economics.SupportAdvisoryReason) *economics.SupportAdvisoryReport {
	report := &economics.SupportAdvisoryReport{}
	for scope := start; scope < end; scope++ {
		report.Pairs = append(report.Pairs, retailAdvisoryMergePair(contextKey, fmt.Sprintf("scope-%03d", scope), scope%2 == 0))
	}
	for _, reason := range incomplete {
		report.IncompleteContexts = append(report.IncompleteContexts, economics.SupportAdvisoryIncomplete{ContextKey: contextKey, Reason: reason})
	}
	return report
}

func retailAdvisoryContentBytes(t *testing.T, valuation economics.Valuation) []byte {
	t.Helper()
	canonical, err := valuation.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	content := struct {
		Contexts []economics.SupportAdvisoryContext `json:"contexts"`
		Report   *economics.SupportAdvisoryReport   `json:"report,omitempty"`
	}{Contexts: canonical.SupportAdvisoryContexts, Report: canonical.SupportAdvisory}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestRetailSupportAdvisoryCompositionCanonicalPrefixAndPermutation(t *testing.T) {
	t.Parallel()
	first, contextA := retailAdvisoryMergeRatedSource(t, "a")
	second, contextB := retailAdvisoryMergeRatedSource(t, "b")
	third, contextC := retailAdvisoryMergeRatedSource(t, "c")
	sources := []economics.Valuation{first, second, third}
	contexts := []economics.SupportAdvisoryContext{contextA, contextB, contextC}
	sources[0].SupportAdvisory = retailAdvisoryMergeReport(contextA.Key(), 0, 80, economics.SupportAdvisoryCandidateBudget, economics.SupportAdvisoryGraphBudget)
	sources[1].SupportAdvisory = retailAdvisoryMergeReport(contextB.Key(), 0, 80, economics.SupportAdvisoryEvidenceUnavailable)
	sources[2].SupportAdvisory = retailAdvisoryMergeReport(contextC.Key(), 0, 80)

	orders := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	var wantContent []byte
	for orderIndex, order := range orders {
		var composite economics.Valuation
		for _, sourceIndex := range order {
			combineRetailValuation(&composite, sources[sourceIndex])
		}
		if err := composite.Validate(); err != nil {
			t.Fatalf("order %v produced invalid final valuation: %v", order, err)
		}
		if composite.Completeness != economics.CompletenessComplete {
			t.Fatalf("order %v changed completeness to %q", order, composite.Completeness)
		}
		if got := len(composite.SupportAdvisoryContexts); got != 3 {
			t.Fatalf("order %v retained %d contexts, want all 3", order, got)
		}
		if composite.SupportAdvisory == nil {
			t.Fatalf("order %v missing support advisory report", order)
		}
		if len(composite.SupportAdvisory.Pairs) != economics.MaxSupportAdvisoryPairs {
			t.Fatalf("order %v pairs=%d, want %d", order, len(composite.SupportAdvisory.Pairs), economics.MaxSupportAdvisoryPairs)
		}

		contextKeys := []string{contexts[0].Key(), contexts[1].Key(), contexts[2].Key()}
		sort.Strings(contextKeys)
		for index, context := range composite.SupportAdvisoryContexts {
			if context.Key() != contextKeys[index] {
				t.Fatalf("order %v context %d=%q, want %q", order, index, context.Key(), contextKeys[index])
			}
		}
		for index, pair := range composite.SupportAdvisory.Pairs {
			contextIndex, scopeIndex := index/80, index%80
			wantContext := contextKeys[contextIndex]
			if contextIndex == 2 || pair.ContextKey != wantContext || pair.ScopeKey != fmt.Sprintf("scope-%03d", scopeIndex) {
				t.Fatalf("order %v pair %d=%+v, want context %q and scope-%03d", order, index, pair, wantContext, scopeIndex)
			}
			if pair.Left.Component != metering.ComponentAudioToken || pair.Right.Component != metering.ComponentTextToken {
				t.Fatalf("order %v pair %d was not canonically oriented: %+v", order, index, pair)
			}
		}

		wantIncomplete := []economics.SupportAdvisoryIncomplete{
			{ContextKey: contextA.Key(), Reason: economics.SupportAdvisoryCandidateBudget},
			{ContextKey: contextA.Key(), Reason: economics.SupportAdvisoryGraphBudget},
			{ContextKey: contextB.Key(), Reason: economics.SupportAdvisoryEvidenceUnavailable},
			{ContextKey: contextB.Key(), Reason: economics.SupportAdvisoryPairLimit},
			{ContextKey: contextC.Key(), Reason: economics.SupportAdvisoryPairLimit},
		}
		sort.Slice(wantIncomplete, func(i, j int) bool {
			if wantIncomplete[i].ContextKey != wantIncomplete[j].ContextKey {
				return wantIncomplete[i].ContextKey < wantIncomplete[j].ContextKey
			}
			return wantIncomplete[i].Reason < wantIncomplete[j].Reason
		})
		if !reflect.DeepEqual(composite.SupportAdvisory.IncompleteContexts, wantIncomplete) {
			t.Fatalf("order %v incomplete entries=%v, want %v", order, composite.SupportAdvisory.IncompleteContexts, wantIncomplete)
		}

		content := retailAdvisoryContentBytes(t, composite)
		if orderIndex == 0 {
			wantContent = content
		} else if !reflect.DeepEqual(content, wantContent) {
			t.Fatalf("order %v changed canonical report content\n got: %s\nwant: %s", order, content, wantContent)
		}
		if orderIndex != 0 {
			continue
		}

		withoutAdvice := composite.Clone()
		withoutAdvice.SupportAdvisoryContexts = nil
		withoutAdvice.SupportAdvisory = nil
		var financialControl economics.Valuation
		for _, sourceIndex := range order {
			source := sources[sourceIndex].Clone()
			source.SupportAdvisoryContexts = nil
			source.SupportAdvisory = nil
			combineRetailValuation(&financialControl, source)
		}
		if !reflect.DeepEqual(withoutAdvice, financialControl) {
			t.Fatal("report composition changed non-advisory valuation fields")
		}
		advisedMoney, err := retailValuationMoney(composite)
		if err != nil {
			t.Fatal(err)
		}
		controlMoney, err := retailValuationMoney(financialControl)
		if err != nil {
			t.Fatal(err)
		}
		if advisedMoney != controlMoney {
			t.Fatalf("report composition changed settlement money: advised=%+v control=%+v", advisedMoney, controlMoney)
		}
		if advisedMoney != (Money{Nano: 3_000_000_000, Currency: "USD"}) {
			t.Fatalf("three one-dollar source groups settled as %+v, want USD 3.00", advisedMoney)
		}
	}
}

func TestRetailSupportAdvisoryComposition129KeepsCanonicalPrefix(t *testing.T) {
	t.Parallel()
	base, sourceContext := retailAdvisoryMergeRatedSource(t, "limit")
	contextKey := sourceContext.Key()
	first := base.Clone()
	first.SupportAdvisory = retailAdvisoryMergeReport(contextKey, 0, economics.MaxSupportAdvisoryPairs)
	second := base.Clone()
	second.ID += ":second"
	second.SupportAdvisory = retailAdvisoryMergeReport(contextKey, economics.MaxSupportAdvisoryPairs, economics.MaxSupportAdvisoryPairs+1)

	var composite economics.Valuation
	combineRetailValuation(&composite, first)
	combineRetailValuation(&composite, second)
	if got := len(composite.SupportAdvisory.Pairs); got != economics.MaxSupportAdvisoryPairs {
		t.Fatalf("129 distinct pairs retained %d entries, want 128", got)
	}
	if err := composite.Validate(); err != nil {
		t.Fatalf("129-pair result is invalid: %v", err)
	}
	if composite.Completeness != economics.CompletenessComplete {
		t.Fatalf("report limit changed valuation completeness to %q", composite.Completeness)
	}
	money, err := retailValuationMoney(composite)
	if err != nil {
		t.Fatal(err)
	}
	if money != (Money{Nano: 2_000_000_000, Currency: "USD"}) {
		t.Fatalf("129-pair report limit changed settlement money to %+v, want USD 2.00", money)
	}
	if len(composite.SupportAdvisory.IncompleteContexts) != 1 || composite.SupportAdvisory.IncompleteContexts[0] != (economics.SupportAdvisoryIncomplete{ContextKey: contextKey, Reason: economics.SupportAdvisoryPairLimit}) {
		t.Fatalf("129 distinct pairs must report pair_limit, got %+v", composite.SupportAdvisory.IncompleteContexts)
	}
	for index, pair := range composite.SupportAdvisory.Pairs {
		if pair.ScopeKey != fmt.Sprintf("scope-%03d", index) {
			t.Fatalf("129-pair prefix changed at %d: %+v", index, pair)
		}
	}
}

func TestRetailSupportAdvisoryCompositionExact128DeduplicatesPair(t *testing.T) {
	t.Parallel()
	base, sourceContext := retailAdvisoryMergeRatedSource(t, "exact")
	contextKey := sourceContext.Key()
	first := base.Clone()
	first.SupportAdvisory = retailAdvisoryMergeReport(contextKey, 0, 64)
	second := base.Clone()
	second.ID += ":second"
	second.SupportAdvisory = &economics.SupportAdvisoryReport{Pairs: []economics.SupportAdvisoryPair{
		retailAdvisoryMergePair(contextKey, "scope-000", false),
	}}
	second.SupportAdvisory.Pairs = append(second.SupportAdvisory.Pairs, retailAdvisoryMergeReport(contextKey, 64, 128).Pairs...)

	var composite economics.Valuation
	combineRetailValuation(&composite, first)
	combineRetailValuation(&composite, second)
	if err := composite.Validate(); err != nil {
		t.Fatalf("exact-limit result is invalid: %v", err)
	}
	if got := len(composite.SupportAdvisory.Pairs); got != economics.MaxSupportAdvisoryPairs {
		t.Fatalf("128 distinct pairs plus a duplicate retained %d entries, want 128", got)
	}
	if len(composite.SupportAdvisory.IncompleteContexts) != 0 {
		t.Fatalf("exact 128-pair result is incomplete: %+v", composite.SupportAdvisory.IncompleteContexts)
	}
	for index, pair := range composite.SupportAdvisory.Pairs {
		if pair.ScopeKey != fmt.Sprintf("scope-%03d", index) {
			t.Fatalf("exact-limit pair %d=%+v, want canonical scope-%03d", index, pair, index)
		}
	}
}

func TestRetailSupportAdvisoryCompositionPreservesOversizedSourceForFinalValidation(t *testing.T) {
	t.Parallel()
	source, sourceContext := retailAdvisoryMergeRatedSource(t, "oversized")
	source.SupportAdvisory = retailAdvisoryMergeReport(sourceContext.Key(), 0, economics.MaxSupportAdvisoryPairs+1)

	var composite economics.Valuation
	combineRetailValuation(&composite, source)
	if got := len(composite.SupportAdvisory.Pairs); got != economics.MaxSupportAdvisoryPairs+1 {
		t.Fatalf("oversized source was rewritten to %d pairs, want raw 129 for final validation", got)
	}
	if err := composite.Validate(); err == nil {
		t.Fatal("final SDK validation accepted an oversized source report")
	}
}

//nolint:paralleltest // AllocsPerRun temporarily changes process-wide GOMAXPROCS.
func TestRetailSupportAdvisoryContextMergeAllocationBound(t *testing.T) {
	base, _ := retailAdvisoryMergeRatedSource(t, "allocation-bound")
	contexts := make([]economics.SupportAdvisoryContext, economics.MaxValuationRefs)
	for i := range contexts {
		contexts[i] = base.SupportAdvisoryContexts[0]
		contexts[i].Tariff.ID = fmt.Sprintf("tariff-%04d", i)
	}
	sort.Slice(contexts, func(i, j int) bool { return contexts[i].Key() < contexts[j].Key() })
	allocations := testing.AllocsPerRun(3, func() {
		dst := economics.Valuation{SupportAdvisoryContexts: append([]economics.SupportAdvisoryContext(nil), contexts...)}
		mergeRetailSupportAdvisory(&dst, base)
		if len(dst.SupportAdvisoryContexts) != len(contexts)+1 {
			t.Fatal("merge lost a distinct source context")
		}
	})
	// Each context may encode its identity once, plus generous slice/map overhead.
	// Sorting must not repeatedly allocate identity encodings per comparison.
	limit := float64(8 * (len(contexts) + 1))
	t.Logf("context merge allocations %.0f, limit %.0f", allocations, limit)
	if allocations > limit {
		t.Fatalf("context merge allocations %.0f exceed %.0f for %d contexts", allocations, limit, len(contexts)+1)
	}
}
