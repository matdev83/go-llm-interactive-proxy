package openaiusage_test

// R7-C1C (PR #659 adversarial repair): bounded output-image overlap slice.
//
// The OpenAI native Chat/Responses output detail has no image token member, so
// the only supported way the adapter emits a native directional OUTPUT image
// token is the recognized compatible-provider flat alias family
// (output_image_tokens / image_output_tokens / completion_image_tokens). That
// flat measure is a token quantity included in the ordinary completion/output
// total, exactly like the nested Chat input image tokens are included in the
// prompt total.
//
// The frozen provider-family schema already declares the INPUT image token
// subset edge. These vectors pin the symmetric OUTPUT subset edge: an
// aggregate output_token plus its included native output image token must be a
// typed overlap rather than an additive double bill, while an aggregate-only or
// child-only tariff keeps its prior behavior and no output image token is
// fabricated from a nested completion detail.
//
// Every vector drives the real adapter (OpenChat) -> provider evidence buffer
// -> frozen schema -> retail selection/rating path, not a hand-built measure
// slice.

import (
	"testing"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// wireFlatOutputImageChatUsage is the recognized compatible-provider flat Chat
// shape: the ordinary completion_tokens aggregate plus one flat output image
// token member. It is the output-side analogue of the established flat
// multimodal fixture in
// internal/infra/billingstore/phase19_1_lifecycle_certification_test.go.
func wireFlatOutputImageChatUsage(key, value string) string {
	return `{"id":"chat-out-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":9,"total_tokens":19,"` + key + `":` + value + `}}`
}

// wireFlatOutputImageChatNoEvidence is the same shape with no output image token
// member at all (the absent shape).
func wireFlatOutputImageChatNoEvidence() string {
	return `{"id":"chat-out-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":9,"total_tokens":19}}`
}

func wireOutputAggregateOnlyRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("input-aggregate", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken), "2"),
		wireLinearRule("output-aggregate", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken), "3"),
	}
}

func wireOutputAggregatePlusImageRules() []economics.RatingRule {
	return append(wireOutputAggregateOnlyRules(),
		wireLinearRule("output-image-token", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentImageToken), "5"))
}

func wireOutputImageOnlyRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("output-image-token", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentImageToken), "5"),
	}
}

// wireFlatChatEvidence drives one real Chat completion response through the
// production adapter and provider evidence buffer, returning the bound call ID
// and the single intact observation.
func wireFlatChatEvidence(t *testing.T, chatJSON, label string) (corebilling.BillingCallID, sdkmetering.Observation) {
	t.Helper()
	server := wireUsageServer(t, chatJSON, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	return callID, openWireObservations(t, server, "chat", wireIdentityForCall(callID, label))[0]
}

func assertNativeOutputImageTokens(t *testing.T, observation sdkmetering.Observation, want string) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentImageToken || measure.Key.Direction != sdkmetering.DirectionOutput {
			continue
		}
		if measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("native output image_token unit = %q, want %q", measure.Key.Unit, sdkmetering.UnitToken)
		}
		if measure.Value == nil || measure.Value.Coefficient != want {
			t.Fatalf("native output image_token = %+v, want %s", measure.Value, want)
		}
		return
	}
	t.Fatalf("native output image_token measure missing: %+v", observation.Measures)
}

func assertNoOutputImageToken(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionOutput {
			t.Fatalf("unexpected output image_token measure: %+v", measure)
		}
	}
}

// TestOpenAIUsageEvidence_FlatOutputImageTokenDoubleBilledFailsClosed is the
// primary R7-C1C RED vector. The real adapter surfaces the aggregate
// output_token and the included native output image token; a schema-free tariff
// additively double bills them (27 + 25 = 52 on top of the 20 input = 72), while
// the frozen provider-family schema must classify the same both-priced tariff as
// a typed overlap with no payable quantity lines.
func TestOpenAIUsageEvidence_FlatOutputImageTokenDoubleBilledFailsClosed(t *testing.T) {
	t.Parallel()
	callID, observation := wireFlatChatEvidence(t,
		wireFlatOutputImageChatUsage("output_image_tokens", "5"), "out-image")
	assertNativeOutputImageTokens(t, observation, "5")
	call, leg, policy := wireRetailCall(t, callID, observation)

	// Control: without the frozen schema the identical input and evidence rate
	// additively, proving the overlap is driven by the explicit relationship.
	legacyResult, legacyErr := rateWireRetail(t, call, leg, policy, wireTariff(t, wireOutputAggregatePlusImageRules()))
	if legacyErr != nil {
		t.Fatalf("schema-free control must rate additively: %v", legacyErr)
	}
	if total := wireValuationTotal(legacyResult.InferenceValuation); total != "72/0" {
		t.Fatalf("schema-free control total=%s, want additive 72 (10*2 + 9*3 + 5*5)", total)
	}

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireOutputAggregatePlusImageRules()))
	if !wireOverlapRejected(err) {
		t.Fatalf("aggregate+output-image tariff err=%v, want typed overlap; total=%s",
			err, wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("aggregate+output-image completeness=%q, want conflict", result.InferenceValuation.Completeness)
	}
	// Only the overlapping output aggregate and its included native output image
	// token are suppressed; the unrelated input aggregate stays payable.
	var outputPaid, inputPaid bool
	for _, line := range result.InferenceValuation.Lines {
		if line.Amount == nil || line.Component == nil || line.Amount.Coefficient == "0" {
			continue
		}
		if line.Component.Direction == sdkmetering.DirectionOutput {
			outputPaid = true
		}
		if line.Component.Direction == sdkmetering.DirectionInput && line.Component.Component == sdkmetering.ComponentInputToken {
			inputPaid = true
		}
	}
	if outputPaid {
		t.Fatalf("conflicting output quantity lines must not be payable: %+v", result.InferenceValuation.Lines)
	}
	if !inputPaid {
		t.Fatalf("unrelated input aggregate must stay payable: %+v", result.InferenceValuation.Lines)
	}
	if total := wireValuationTotal(result.InferenceValuation); total != "20/0" {
		t.Fatalf("conflict valuation payable total=%s, want unrelated input 20", total)
	}
}

// TestOpenAIUsageEvidence_OutputAggregateOnlyExcludesIncludedImageToken proves
// the subset edge is a partial containment, not a required partition member: an
// aggregate-only tariff whose parent output_token is priced and whose included
// native output image token has no rule must stay complete (the included child
// is not independently billable), never a false partial.
func TestOpenAIUsageEvidence_OutputAggregateOnlyExcludesIncludedImageToken(t *testing.T) {
	t.Parallel()
	callID, observation := wireFlatChatEvidence(t,
		wireFlatOutputImageChatUsage("output_image_tokens", "5"), "aggregate-only")
	assertNativeOutputImageTokens(t, observation, "5")
	call, leg, policy := wireRetailCall(t, callID, observation)

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireOutputAggregateOnlyRules()))
	if err != nil {
		t.Fatalf("aggregate-only tariff with a priced parent must rate: %v", err)
	}
	if result.InferenceValuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("aggregate-only completeness=%q, want complete", result.InferenceValuation.Completeness)
	}
	if total := wireValuationTotal(result.InferenceValuation); total != "47/0" {
		t.Fatalf("aggregate-only total=%s, want 47 (10*2 + 9*3)", total)
	}
	if amount := wireComponentAmount(t, result.InferenceValuation, sdkmetering.DirectionOutput, sdkmetering.ComponentImageToken); amount != nil && amount.Coefficient != "0" {
		t.Fatalf("included native output image token must not be independently billed: %v", amount)
	}
}

// TestOpenAIUsageEvidence_OutputImageTokenChildOnlyStaysPartial proves the
// subset edge does not make a subset child a complete-coverage proof: a tariff
// that prices only the native output image token leaves the aggregate parent
// unpriced, so the valuation stays a typed partial and the child line remains
// payable at its literal 25/0, exactly as before the slice.
func TestOpenAIUsageEvidence_OutputImageTokenChildOnlyStaysPartial(t *testing.T) {
	t.Parallel()
	callID, observation := wireFlatChatEvidence(t,
		wireFlatOutputImageChatUsage("output_image_tokens", "5"), "child-only")
	assertNativeOutputImageTokens(t, observation, "5")
	call, leg, policy := wireRetailCall(t, callID, observation)

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireOutputImageOnlyRules()))
	if wireOverlapRejected(err) {
		t.Fatalf("child-only tariff must not be a typed overlap: %v", err)
	}
	if err == nil {
		t.Fatalf("unpriced aggregate cannot falsely complete; total=%s", wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessPartial {
		t.Fatalf("child-only completeness=%q, want partial", result.InferenceValuation.Completeness)
	}
	amount := wireComponentAmount(t, result.InferenceValuation, sdkmetering.DirectionOutput, sdkmetering.ComponentImageToken)
	if amount == nil || amount.CanonicalString() != "25/0" {
		t.Fatalf("child-only output image amount=%v, want 25 (5*5)", amount)
	}
}

// TestOpenAIUsageEvidence_OutputImageAbsentNullZeroNotOverlap covers the
// absent/null/explicit-zero evidence shapes through the real adapter. Absent and
// null are not an observable quantity; an explicit zero is present but carries
// no payable amount. None may manufacture a priced overlap: the aggregate-only
// total stays complete at 47/0.
func TestOpenAIUsageEvidence_OutputImageAbsentNullZeroNotOverlap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		chatJSON string
		hasImage bool
	}{
		{name: "absent", chatJSON: wireFlatOutputImageChatNoEvidence()},
		{name: "null", chatJSON: wireFlatOutputImageChatUsage("output_image_tokens", "null")},
		{name: "explicit_zero", chatJSON: wireFlatOutputImageChatUsage("output_image_tokens", "0"), hasImage: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			callID, observation := wireFlatChatEvidence(t, tc.chatJSON, "shape-"+tc.name)
			if !tc.hasImage {
				assertNoOutputImageToken(t, observation)
			}
			call, leg, policy := wireRetailCall(t, callID, observation)

			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireOutputAggregatePlusImageRules()))
			if wireOverlapRejected(err) {
				t.Fatalf("%s output image must not be a priced overlap: %v", tc.name, err)
			}
			if err != nil {
				t.Fatalf("%s aggregate+image tariff must rate: %v", tc.name, err)
			}
			if result.InferenceValuation.Completeness != economics.CompletenessComplete {
				t.Fatalf("%s completeness=%q, want complete", tc.name, result.InferenceValuation.Completeness)
			}
			if total := wireValuationTotal(result.InferenceValuation); total != "47/0" {
				t.Fatalf("%s total=%s, want 47", tc.name, total)
			}
		})
	}
}

// TestOpenAIUsageEvidence_OutputImageAliasesShareCanonicalOverlap proves every
// recognized flat compatible-provider output image token alias resolves to the
// same canonical native child, so the single frozen subset edge rejects the
// aggregate+image double bill regardless of which alias spelling the provider
// used.
func TestOpenAIUsageEvidence_OutputImageAliasesShareCanonicalOverlap(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"output_image_tokens", "image_output_tokens", "completion_image_tokens"} {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			callID, observation := wireFlatChatEvidence(t, wireFlatOutputImageChatUsage(key, "5"), "alias-"+key)
			assertNativeOutputImageTokens(t, observation, "5")
			call, leg, policy := wireRetailCall(t, callID, observation)

			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireOutputAggregatePlusImageRules()))
			if !wireOverlapRejected(err) {
				t.Fatalf("%s alias err=%v, want typed overlap; total=%s",
					key, err, wireValuationTotal(result.InferenceValuation))
			}
			if result.InferenceValuation.Completeness != economics.CompletenessConflict {
				t.Fatalf("%s alias completeness=%q, want conflict", key, result.InferenceValuation.Completeness)
			}
		})
	}
}

// TestOpenAIUsageEvidence_OutputImageConflictingAliasFirstWinsAndOverlaps proves
// a response carrying two conflicting aliases keeps exactly one native output
// image token measure (the first declared alias wins, matching the adapter's
// existing alias policy), retains its raw lexeme, and still participates in the
// typed overlap.
func TestOpenAIUsageEvidence_OutputImageConflictingAliasFirstWinsAndOverlaps(t *testing.T) {
	t.Parallel()
	chatJSON := `{"id":"chat-out-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":9,"total_tokens":19,"output_image_tokens":5,"image_output_tokens":9}}`
	callID, observation := wireFlatChatEvidence(t, chatJSON, "alias-conflict")
	assertNativeOutputImageTokens(t, observation, "5")

	var count int
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionOutput {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("conflicting output image aliases produced %d native measures, want 1: %+v", count, observation.Measures)
	}

	call, leg, policy := wireRetailCall(t, callID, observation)
	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireOutputAggregatePlusImageRules()))
	if !wireOverlapRejected(err) {
		t.Fatalf("conflicting alias err=%v, want typed overlap; total=%s",
			err, wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("conflicting alias completeness=%q, want conflict", result.InferenceValuation.Completeness)
	}
}

// TestNativeUsageMeasuresDoesNotFabricateOutputImageTokens proves the optional
// output partition edge is not fed by an invented Chat completion image field: a
// hypothetical nested completion_tokens_details.image_tokens member (or
// Responses output detail image_tokens) must not create a native output image
// token measure, so the output optional partition edge can only ever bind to the
// recognized flat provider vocabulary. The nested Chat
// prompt_tokens_details.image_tokens input measure remains intact.
func TestNativeUsageMeasuresDoesNotFabricateOutputImageTokens(t *testing.T) {
	t.Parallel()
	raw := `{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"image_tokens":3}}`
	measures := openaiusage.NativeUsageMeasures(raw)
	for _, measure := range measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionOutput {
			t.Fatalf("completion_tokens_details.image_tokens must not fabricate an output image token: %+v", measure)
		}
	}
	var inputImage bool
	for _, measure := range measures {
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionInput {
			inputImage = true
		}
	}
	if !inputImage {
		t.Fatalf("nested prompt_tokens_details.image_tokens input measure missing: %+v", measures)
	}
}
