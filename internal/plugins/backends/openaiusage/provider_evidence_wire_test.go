package openaiusage_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestOpenAIUsageEvidence_ChatAndResponsesWireAudioDirections(t *testing.T) {
	server := wireAudioServer(t)
	defer server.Close()

	chatObservations := openWireObservations(t, server, "chat", wireSyntheticIdentity("chat"))
	responsesObservations := openWireObservations(t, server, "responses", wireSyntheticIdentity("responses"))

	want := map[string]string{
		"input/audio_token":  "3",
		"output/audio_token": "4",
	}
	assertDirectionalAudio(t, chatObservations, want)
	assertDirectionalAudio(t, responsesObservations, want)
	assertWireEvidence(t, chatObservations, map[string]string{
		"$.usage.prompt_tokens_details.audio_tokens":     "3",
		"$.usage.completion_tokens_details.audio_tokens": "4",
	})
	assertWireEvidence(t, responsesObservations, map[string]string{
		"$.usage.input_tokens_details.audio_tokens":  "3",
		"$.usage.output_tokens_details.audio_tokens": "4",
	})
}

// TestOpenAIUsageEvidence_IntactNativeDetailsInclusionProof certifies that the
// intact provider observation (aggregate ordinary tokens AND directional native
// text/audio children) is charged by the real frozen retail selection and
// rating path without double billing, or is rejected fail-closed before money.
//
// OpenAI's prompt_tokens/completion_tokens totals already include the
// prompt_tokens_details/completion_tokens_details (and Responses
// input_tokens_details/output_tokens_details) children. A tariff that prices
// both the aggregate and its native children is an overlapping additive rule
// set. Requirement 3.4 requires the accounting system to select a disjoint
// native partition or reject the overlap explicitly. A test-only partition that
// removes one side before rating proves nothing about production charging; the
// observation therefore reaches the real retail rater intact.
func TestOpenAIUsageEvidence_IntactNativeDetailsInclusionProof(t *testing.T) {
	server := wireAudioServer(t)
	defer server.Close()

	cases := []struct {
		label string
		path  string
	}{
		{label: "chat", path: "chat"},
		{label: "responses", path: "responses"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			billingCallID, err := corebilling.NewBillingCallID()
			if err != nil {
				t.Fatalf("NewBillingCallID: %v", err)
			}
			callID := billingCallID.String()
			identity := coremetering.ObservationIdentity{
				StoreID: "store", RequestID: tc.label + "-request",
				CallID: callID, BillingCallID: callID,
				ALegID: "a-leg", BLegID: "b-leg", AttemptID: "b-leg",
			}
			observations := openWireObservations(t, server, tc.path, identity)
			if len(observations) != 1 {
				t.Fatalf("%s observations = %d, want one intact observation", tc.label, len(observations))
			}
			intact := observations[0]
			assertIntactAggregateAndChildren(t, tc.label, intact)

			call, leg, policy := wireRetailCall(t, billingCallID, intact)

			// A tariff pricing both the aggregate totals and their included
			// native children must not post the sum. It must select exactly one
			// disjoint partition or be rejected before money. The tariff binds
			// the frozen OpenAI provider-family inclusion schema so the real
			// retail selection/rating path can see the explicit parent/child
			// relation instead of inferring it from component names.
			fullTariff := wireProviderTariff(t, wireFullInclusionRules())
			fullResult, fullErr := rateWireRetail(t, call, leg, policy, fullTariff)
			if !wireOverlapRejected(fullErr) && !wireDisjointPartition(fullResult.InferenceValuation) {
				t.Errorf("%s intact observation with aggregate+native child tariff billed %s over %d lines (err=%v); "+
					"want a disjoint native partition (49/0 for the aggregate partition or 74/0 for the text/audio child partition) "+
					"or an explicit fail-closed overlap rejection",
					tc.label, wireValuationTotal(fullResult.InferenceValuation), len(fullResult.InferenceValuation.Lines), fullErr)
			}

			// A legitimate tariff that prices only the disjoint native children
			// must bill exactly that partition, not become partial because the
			// aggregate summary is unpriced.
			childTariff := wireProviderTariff(t, wireChildPartitionRules())
			childResult, childErr := rateWireRetail(t, call, leg, policy, childTariff)
			if !wireDisjointPartition(childResult.InferenceValuation) {
				t.Errorf("%s child-partition tariff billed %s over %d lines (err=%v); "+
					"want the disjoint text/audio partition 74/0 over the four non-zero child lines without the aggregate summaries",
					tc.label, wireValuationTotal(childResult.InferenceValuation), len(childResult.InferenceValuation.Lines), childErr)
			}
		})
	}
}

func wireAudioServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = io.WriteString(w, `{"id":"chat-audio","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"cached_tokens":0},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"reasoning_tokens":0}}}`)
		case "/responses":
			_, _ = io.WriteString(w, `{"id":"resp-audio","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"text_tokens":8,"audio_tokens":3,"images":0},"output_tokens_details":{"text_tokens":5,"audio_tokens":4,"images":0}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func openWireObservations(t *testing.T, server *httptest.Server, path string, identity coremetering.ObservationIdentity) []sdkmetering.Observation {
	t.Helper()
	call := lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{TransportMode: lipapi.TransportModeNonStreaming},
	}
	candidate := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-test"}}
	client := openaicred.NewClient(server.URL, "sk-test", server.Client(), new(int))
	var (
		stream lipapi.ManagedEventStream
		err    error
	)
	switch path {
	case "chat":
		stream, err = openaicompat.OpenChat(context.Background(), client, openaicompat.InvokeRequest{
			ProviderID: "openai-chat-test", Call: call, Candidate: candidate,
		})
	case "responses":
		stream, err = openaicompat.OpenResponses(context.Background(), client, openaicompat.InvokeRequest{
			ProviderID: "openai-responses-test", Call: call, Candidate: candidate,
		})
	default:
		t.Fatalf("unknown wire path %q", path)
	}
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return drainBoundObservationsWithIdentity(t, stream, path, identity)
}

func wireSyntheticIdentity(label string) coremetering.ObservationIdentity {
	return coremetering.ObservationIdentity{
		StoreID: "store", RequestID: label + "-request", CallID: label + "-call",
		BillingCallID: label + "-billing", ALegID: label + "-a-leg", BLegID: label + "-b-leg", AttemptID: label + "-attempt",
	}
}

// assertIntactAggregateAndChildren proves the observation reaching the rater is
// the intact wire mapping: both the ordinary aggregate token measures and the
// directional native text/audio children are present. This is the property the
// rejected test masked by rebuilding a filtered measure slice.
func assertIntactAggregateAndChildren(t *testing.T, label string, observation sdkmetering.Observation) {
	t.Helper()
	seen := make(map[string]string)
	for _, measure := range observation.Measures {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component
		if measure.Value == nil {
			t.Fatalf("%s measure %s has no value", label, key)
		}
		seen[key] = measure.Value.Coefficient
		if measure.Key.Component == sdkmetering.ComponentTextToken ||
			measure.Key.Component == sdkmetering.ComponentAudioToken {
			if measure.Key.SchemaID != "openai.usage.v2" {
				t.Fatalf("%s native child %s schema = %q, want openai.usage.v2", label, key, measure.Key.SchemaID)
			}
		}
	}
	want := map[string]string{
		"input/input_token":   "11",
		"output/output_token": "9",
		"input/text_token":    "8",
		"output/text_token":   "5",
		"input/audio_token":   "3",
		"output/audio_token":  "4",
	}
	for key, expected := range want {
		if seen[key] != expected {
			t.Fatalf("%s intact measure %s = %q, want %q (all=%v)", label, key, seen[key], expected, seen)
		}
	}
}

func wireRetailCall(t *testing.T, callID corebilling.BillingCallID, observation sdkmetering.Observation) (corebilling.CallUsageRecord, corebilling.CallLegUsageRecord, corebilling.ChargePolicy) {
	t.Helper()
	policy := corebilling.ChargePolicy{
		Ref:                corebilling.VersionRef{ID: "openai-wire-policy", Version: "v1"},
		PricingRef:         corebilling.VersionRef{ID: "openai-wire-tariff", Version: "v1"},
		Scope:              corebilling.ChargeSurfacedTurn,
		IncludeInputTokens: true, IncludeOutputTokens: true,
		Retail: &corebilling.RetailSelectionPolicy{
			Mode: corebilling.RetailSelectionSurfacedWinner, Basis: corebilling.RetailBasisIndependent,
		},
	}
	now := time.Unix(1715620000, 0).UTC()
	call := corebilling.CallUsageRecord{
		SchemaVersion: corebilling.CurrentRecordSchemaVersion,
		CallID:        callID, AccountID: "acct-1", ALegID: "a-leg",
		StartedAt: now, FinishedAt: now.Add(time.Second),
		Outcome:            corebilling.TurnOutcomeCompleted,
		CustomerPricingRef: policy.PricingRef, ChargePolicyRef: policy.Ref,
		ExpectedBLegIDs: []string{"b-leg"},
	}
	leg := corebilling.CallLegUsageRecord{
		CallID: callID, ALegID: "a-leg", BLegID: "b-leg", AttemptSeq: 1,
		BackendID: "openai", ProviderID: "openai-test", ModelID: "gpt-test",
		StartedAt: now, FinishedAt: now.Add(time.Second),
		Outcome: corebilling.LegOutcomeWinner, Surfaced: corebilling.SurfacedYes,
		EvidenceVersion: corebilling.EvidenceFormatVersionV2, EvidenceProjection: corebilling.EvidenceProjectionV1,
		Observations: []sdkmetering.Observation{observation},
	}
	return call, leg, policy
}

func rateWireRetail(t *testing.T, call corebilling.CallUsageRecord, leg corebilling.CallLegUsageRecord, policy corebilling.ChargePolicy, tariff economics.TariffSnapshot) (corebilling.RetailRatingResult, error) {
	t.Helper()
	selection, err := corebilling.SelectRetailBLegEvidence(corebilling.RetailSelectionInput{
		Call: call, Legs: []corebilling.CallLegUsageRecord{leg}, Policy: policy,
	})
	if err != nil {
		t.Fatalf("SelectRetailBLegEvidence: %v", err)
	}
	result, err := corebilling.RateSelectedRetailBLegs(context.Background(), corebilling.RetailRatingInput{
		Call: call, Legs: []corebilling.CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: sdkmetering.PaymentParty{Kind: sdkmetering.PaymentPartyCustomer, ID: call.AccountID},
	})
	return result, err
}

// wireTariff builds the legacy schema-free tariff. It intentionally carries no
// frozen component-relationship material so the schema-free control vectors can
// prove provider inclusion is never inferred or auto-bound.
func wireTariff(t *testing.T, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.BuildTariffSnapshot(economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "openai-wire-tariff", Version: "v1"}, RaterID: "test-rater",
	}, "USD", rules)
	if err != nil {
		t.Fatalf("BuildTariffSnapshot: %v", err)
	}
	return tariff
}

// wireProviderTariff builds the same immutable tariff identity but binds the
// frozen OpenAI Chat/Responses provider-family inclusion schema. The frozen
// schema participates in the content hash, so the selected-retail grouping and
// rating see exactly the provider relation the adapter's native text/audio
// measures express.
func wireProviderTariff(t *testing.T, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.BuildTariffSnapshotWithSchemas(economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "openai-wire-tariff", Version: "v1"}, RaterID: "test-rater",
	}, "USD", rules, openaiusage.NativeUsageInclusionSchemas())
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	return tariff
}

func wireFullInclusionRules() []economics.RatingRule {
	return append([]economics.RatingRule{
		wireLinearRule("input-aggregate", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken), "2"),
		wireLinearRule("output-aggregate", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken), "3"),
	}, wireChildPartitionRules()...)
}

// wireChildPartitionRules prices every non-aggregate leaf the adapter can emit
// (native text/audio children plus the zero-valued cache/reasoning/image
// details) but deliberately omits the inclusive input_token/output_token
// summaries. A legitimate provider tariff that bills the reported detail
// partition must therefore rate completely at 74/0; the aggregate summaries
// must not force a rate-missing partial.
func wireChildPartitionRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("input-text", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentTextToken), "2"),
		wireLinearRule("output-text", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken), "3"),
		wireLinearRule("input-audio", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken), "5"),
		wireLinearRule("output-audio", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken), "7"),
		wireLinearRule("input-cache-read", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken), "1"),
		wireLinearRule("input-cache-write", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken), "1"),
		wireLinearRule("output-reasoning", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentReasoningOutputToken), "1"),
		wireLinearRule("input-image", wireImageKey(sdkmetering.DirectionInput), "1"),
		wireLinearRule("output-image", wireImageKey(sdkmetering.DirectionOutput), "1"),
	}
}

func wireImageKey(direction sdkmetering.FlowDirection) sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{Direction: direction, Component: sdkmetering.ComponentImage, Unit: sdkmetering.UnitImage, SchemaID: "openai.usage.v2"}
}

// wireDisjointPartition accepts exactly one complete, fully-rated native
// partition and nothing else: the aggregate partition (input_token + output_token
// = 11*2 + 9*3 = 49/0) or the text/audio child partition
// (8*2 + 3*5 + 5*3 + 4*7 = 74/0). Zero-valued priced detail lines are ignored;
// the non-zero component set must be exactly one side. The expected amounts are
// hand-derived from the synthetic rates, never from the implementation under
// test. Any total that also includes both sides (123/0) is a double bill.
func wireDisjointPartition(valuation economics.Valuation) bool {
	if valuation.Completeness != economics.CompletenessComplete || len(valuation.Totals) != 1 || valuation.Totals[0].Amount == nil {
		return false
	}
	total := valuation.Totals[0].Amount.CanonicalString()
	nonzero := make(map[string]struct{}, len(valuation.Lines))
	nonzeroCount := 0
	for _, line := range valuation.Lines {
		if line.Amount == nil || line.Amount.Coefficient == "0" {
			continue
		}
		if line.Component == nil {
			return false
		}
		nonzero[line.Component.Component] = struct{}{}
		nonzeroCount++
	}
	switch total {
	case "74/0":
		return sameComponentSet(nonzero, sdkmetering.ComponentTextToken, sdkmetering.ComponentAudioToken) && nonzeroCount == 4
	case "49/0":
		return sameComponentSet(nonzero, sdkmetering.ComponentInputToken, sdkmetering.ComponentOutputToken) && nonzeroCount == 2
	default:
		return false
	}
}

func sameComponentSet(got map[string]struct{}, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, component := range want {
		if _, ok := got[component]; !ok {
			return false
		}
	}
	return true
}

func wireOverlapRejected(err error) bool {
	return err != nil && (errors.Is(err, corebilling.ErrSchemaOverlapConflict) || errors.Is(err, economics.ErrRatingRuleOverlap))
}

func wireValuationTotal(valuation economics.Valuation) string {
	if len(valuation.Totals) == 1 && valuation.Totals[0].Amount != nil {
		return valuation.Totals[0].Amount.CanonicalString()
	}
	return "<no total>"
}

func assertWireEvidence(t *testing.T, observations []sdkmetering.Observation, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(want))
	for _, observation := range observations {
		for _, field := range observation.Evidence {
			if field.Path == "$.usage.input_audio_tokens" || field.Path == "$.usage.output_audio_tokens" {
				t.Fatalf("wire nested audio evidence used synthetic canonical path %q", field.Path)
			}
			if _, wanted := want[field.Path]; wanted {
				if _, duplicate := got[field.Path]; duplicate {
					t.Fatalf("duplicate wire evidence path %q", field.Path)
				}
				got[field.Path] = field.Lexeme
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("wire evidence paths = %v, want %v", got, want)
	}
	for path, expected := range want {
		if got[path] != expected {
			t.Errorf("wire evidence %q = %q, want %q", path, got[path], expected)
		}
	}
}

func wireComponentKey(direction sdkmetering.FlowDirection, component string) sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{Direction: direction, Component: component, Unit: sdkmetering.UnitToken, SchemaID: "openai.usage.v2"}
}

func wireDefaultKey(direction sdkmetering.FlowDirection, component string) sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{Direction: direction, Component: component, Unit: sdkmetering.UnitToken, SchemaID: sdkmetering.DefaultInclusionSchemaID}
}

func wireLinearRule(id string, key sdkmetering.ComponentKey, price string) economics.RatingRule {
	amount, err := sdkmetering.ParseDecimal(price)
	if err != nil {
		panic(err)
	}
	return economics.RatingRule{ID: id, Component: &key, Currency: "USD", UnitPrice: &amount}
}

func drainBoundObservationsWithIdentity(t *testing.T, stream lipapi.ManagedEventStream, label string, identity coremetering.ObservationIdentity) []sdkmetering.Observation {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []lipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("%s Recv: %v", label, err)
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatalf("%s returned no canonical events", label)
	}
	var usageEvents int
	for _, event := range events {
		if event.Kind == lipapi.EventUsageDelta {
			usageEvents++
		}
	}
	if usageEvents == 0 {
		t.Fatalf("%s returned no usage event: %+v", label, events)
	}
	binder, ok := stream.(coremetering.ProviderEvidenceBinder)
	if !ok {
		t.Fatalf("%s stream %T does not expose provider evidence binding", label, stream)
	}
	binder.BindEconomicEvidence(identity)
	source, ok := stream.(interface {
		DrainEconomicObservations() []sdkmetering.Observation
	})
	if !ok {
		t.Fatalf("%s stream %T does not expose provider evidence drain", label, stream)
	}
	observations := source.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("%s observations = %d, want one: %+v", label, len(observations), observations)
	}
	return observations
}

func assertDirectionalAudio(t *testing.T, observations []sdkmetering.Observation, want map[string]string) {
	t.Helper()
	got := make(map[string]string)
	for _, observation := range observations {
		for _, measure := range observation.Measures {
			if measure.Key.Component != sdkmetering.ComponentAudioToken {
				continue
			}
			key := string(measure.Key.Direction) + "/" + measure.Key.Component
			if _, exists := got[key]; exists {
				t.Fatalf("duplicate directional audio measure %q: %+v", key, observation.Measures)
			}
			if measure.Value == nil {
				t.Fatalf("directional audio measure %q has no value", key)
			}
			got[key] = measure.Value.Coefficient
		}
	}
	if len(got) != len(want) {
		t.Fatalf("directional audio measures = %v, want %v", got, want)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("directional audio measure %q = %q, want %q", key, got[key], expected)
		}
	}
}
