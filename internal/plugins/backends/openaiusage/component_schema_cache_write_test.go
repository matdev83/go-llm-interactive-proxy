package openaiusage_test

// R7-C1E (PR #659 adversarial repair): bounded standard cache-write token
// mapping and frozen overlap slice.
//
// Primary provider-family containment (verified against the official OpenAI
// SDK type contract in this worktree's module cache,
// github.com/openai/openai-go/v3@v3.61.0):
//
//   - Chat: CompletionUsagePromptTokensDetails carries
//     `cache_write_tokens int64` (`json:"cache_write_tokens"`, completion.go
//     line 241) alongside cached_tokens/audio_tokens/image_tokens/text_tokens.
//     The comment on that member is "The unadjusted number of prompt tokens
//     written to cache", so it is a prompt/input subset like cached_tokens.
//   - Responses: ResponseUsageInputTokensDetails carries
//     `cache_write_tokens int64` (`json:"cache_write_tokens" api:"required"`,
//     responses/response.go line 24071) alongside cached_tokens.
//
// The standard member is therefore a real provider family wire field, not an
// invented location. The adapter's native usage mapping (NativeUsageMeasures /
// NativeUsageEvidence) is the provider-family detail path, so the standard
// member maps onto the host-neutral input/cache_write_input_token component
// under openai.usage.v2 and retains its exact provider wire path. The
// non-standard x_lip_cache_write_tokens extension remains a compatible-provider
// fallback only and must never stand in when the standard member is present.
//
// The frozen OpenAI provider-family inclusion schema declares the input
// aggregate -> cache-write subset edge (in the matching schema identity) so a
// tariff that prices both the ordinary prompt total and its included
// cache-write tokens is a typed overlap rather than an additive double bill.
// Because it is a subset edge and not a partition member, an aggregate-only
// tariff stays complete and a child-only tariff stays a typed partial.
//
// Every vector drives the real adapter (OpenChat/OpenResponses) -> provider
// evidence buffer -> frozen schema -> retail selection/rating path, never a
// hand-built measure slice.

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// wireChatCacheWriteSuccess is the true Chat shape carrying the standard
// prompt_tokens_details.cache_write_tokens member.
const wireChatCacheWriteSuccess = `{"id":"chat-cache-write","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"cache_write_tokens":6}}}`

// wireResponsesCacheWriteSuccess is the true Responses shape carrying the
// standard input_tokens_details.cache_write_tokens member.
const wireResponsesCacheWriteSuccess = `{"id":"resp-cache-write","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"cache_write_tokens":6}}}`

// wireChatCacheReadWriteSuccess carries both the recognized cached_tokens
// subset and the standard cache_write_tokens member so the deliberately
// different aggregate/cache-read/cache-write rates cannot be conflated.
const wireChatCacheReadWriteSuccess = `{"id":"chat-cache-read-write","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":6}}}`

// wireChatCacheWriteExtension is the recognized non-standard extension
// fallback shape: no standard member, only x_lip_cache_write_tokens.
const wireChatCacheWriteExtension = `{"id":"chat-cache-write-ext","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"x_lip_cache_write_tokens":6}}}`

// wireChatCacheWriteUsage builds the Chat cache-write shape with a
// caller-supplied prompt_tokens_details object so absent/null/zero/malformed
// members can be exercised.
func wireChatCacheWriteUsage(promptDetails string) string {
	return `{"id":"chat-cache-write","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":` +
		promptDetails + `}}`
}

// wireCacheWriteAggregateOnlyRules prices only the inclusive aggregate parents;
// input and output rates are deliberately distinct so an aggregate sum cannot
// coincide with any cache-write line amount by accident.
func wireCacheWriteAggregateOnlyRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("input-aggregate", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken), "2"),
		wireLinearRule("output-aggregate", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken), "3"),
	}
}

// wireCacheWriteBothPricedRules prices the inclusive input aggregate and its
// included native input cache-write tokens at deliberately distinct rates. The
// expected additive double bill is 11*2 + 9*3 + 6*7 = 91, which no single
// aggregate amount can coincide with.
func wireCacheWriteBothPricedRules() []economics.RatingRule {
	return append(wireCacheWriteAggregateOnlyRules(),
		wireLinearRule("input-cache-write", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken), "7"))
}

// wireCacheWriteChildOnlyRules prices only the native input cache-write subset
// and deliberately omits the inclusive aggregate parents.
func wireCacheWriteChildOnlyRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("input-cache-write", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken), "7"),
	}
}

// wireCacheReadWriteRules prices the aggregate parents plus the recognized
// cache-read and standard cache-write subsets at deliberately different rates:
// 11*2 + 9*3 + 4*5 + 6*7 = 111.
func wireCacheReadWriteRules() []economics.RatingRule {
	return append(wireCacheWriteBothPricedRules(),
		wireLinearRule("input-cache-read", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken), "5"))
}

// wireCacheWriteExtensionOverlapRules prices the inclusive input aggregate and
// the default-inclusion cache-write key the extension fallback emits.
func wireCacheWriteExtensionOverlapRules() []economics.RatingRule {
	return append(wireCacheWriteAggregateOnlyRules(),
		wireLinearRule("input-cache-write", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken), "7"))
}

func assertNativeInputCacheWrite(t *testing.T, observation sdkmetering.Observation, want string) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentCacheWriteInputToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		if measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("native input cache_write unit = %q, want %q", measure.Key.Unit, sdkmetering.UnitToken)
		}
		if measure.Key.SchemaID != "openai.usage.v2" {
			t.Fatalf("native input cache_write schema = %q, want openai.usage.v2", measure.Key.SchemaID)
		}
		if measure.Value == nil || measure.Value.Coefficient != want {
			t.Fatalf("native input cache_write = %+v, want %s", measure.Value, want)
		}
		return
	}
	t.Fatalf("native input cache_write measure missing: %+v", observation.Measures)
}

func assertNoDefaultInputCacheWrite(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentCacheWriteInputToken &&
			measure.Key.SchemaID == sdkmetering.DefaultInclusionSchemaID {
			t.Fatalf("unexpected default-inclusion cache_write measure: %+v", measure)
		}
	}
}

func assertCacheWriteEvidence(t *testing.T, observation sdkmetering.Observation, wantPath, wantLexeme string) {
	t.Helper()
	for _, field := range observation.Evidence {
		if field.Path != wantPath {
			continue
		}
		if !field.Present || field.Lexeme != wantLexeme {
			t.Fatalf("cache_write evidence %q = %+v, want present lexeme %q", wantPath, field, wantLexeme)
		}
		return
	}
	t.Fatalf("cache_write evidence path %q missing: %+v", wantPath, observation.Evidence)
}

// TestOpenAIUsageEvidence_CacheWriteStandardFieldObserved is the primary
// R7-C1E RED vector for the standard member. The real adapter must surface the
// standard Chat prompt_tokens_details.cache_write_tokens (and Responses
// input_tokens_details.cache_write_tokens) as an observed native input
// cache-write measure and retain its exact provider wire path.
func TestOpenAIUsageEvidence_CacheWriteStandardFieldObserved(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		path     string
		chat     string
		respons  string
		wantPath string
	}{
		{
			name: "chat", path: "chat",
			chat: wireChatCacheWriteSuccess, respons: wireResponsesSuccess,
			wantPath: "$.usage.prompt_tokens_details.cache_write_tokens",
		},
		{
			name: "responses", path: "responses",
			chat: wireVisionChatSuccess, respons: wireResponsesCacheWriteSuccess,
			wantPath: "$.usage.input_tokens_details.cache_write_tokens",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chat, tc.respons)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, tc.path, wireIdentityForCall(callID, "cache-write-"+tc.name))[0]

			assertNativeInputCacheWrite(t, observation, "6")
			assertCacheWriteEvidence(t, observation, tc.wantPath, "6")
			assertNoDefaultInputCacheWrite(t, observation)
		})
	}
}

// TestOpenAIUsageEvidence_CacheWriteBothPricedFailsClosed is the primary
// R7-C1E overlap vector. A schema-free tariff additively double bills the
// ordinary input aggregate and its included standard cache-write tokens
// (11*2 + 9*3 + 6*7 = 91/0), while the frozen provider-family schema must
// classify the same both-priced tariff as a typed overlap.
func TestOpenAIUsageEvidence_CacheWriteBothPricedFailsClosed(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireChatCacheWriteSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-overlap"))[0]
	assertNativeInputCacheWrite(t, observation, "6")
	call, leg, policy := wireRetailCall(t, callID, observation)

	legacyResult, legacyErr := rateWireRetail(t, call, leg, policy, wireTariff(t, wireCacheWriteBothPricedRules()))
	if legacyErr != nil {
		t.Fatalf("schema-free control must rate additively: %v", legacyErr)
	}
	if total := wireValuationTotal(legacyResult.InferenceValuation); total != "91/0" {
		t.Fatalf("schema-free control total=%s, want additive 91 (11*2 + 9*3 + 6*7)", total)
	}

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCacheWriteBothPricedRules()))
	if !wireOverlapRejected(err) {
		t.Fatalf("aggregate+cache-write err=%v, want typed overlap; total=%s",
			err, wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("aggregate+cache-write completeness=%q, want conflict", result.InferenceValuation.Completeness)
	}
	if total := wireValuationTotal(result.InferenceValuation); total == "91/0" {
		t.Fatalf("aggregate+cache-write tariff must not post the additive 91 total")
	}
}

// TestOpenAIUsageEvidence_CacheReadWriteRatesStayDistinct proves the standard
// cache-write subset is not conflated with the recognized cache-read subset:
// with deliberately different aggregate/cache-read/cache-write rates the frozen
// both-priced tariff is a typed overlap instead of the additive 111, and the
// child-only tariff keeps the two subsets independently payable.
func TestOpenAIUsageEvidence_CacheReadWriteRatesStayDistinct(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireChatCacheReadWriteSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-read-write"))[0]
	assertNativeInputCacheWrite(t, observation, "6")
	call, leg, policy := wireRetailCall(t, callID, observation)

	legacyResult, legacyErr := rateWireRetail(t, call, leg, policy, wireTariff(t, wireCacheReadWriteRules()))
	if legacyErr != nil {
		t.Fatalf("schema-free control must rate additively: %v", legacyErr)
	}
	if total := wireValuationTotal(legacyResult.InferenceValuation); total != "111/0" {
		t.Fatalf("schema-free control total=%s, want additive 111 (11*2 + 9*3 + 4*5 + 6*7)", total)
	}
	if amount := wireComponentAmount(t, legacyResult.InferenceValuation, sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken); amount == nil || amount.CanonicalString() != "42/0" {
		t.Fatalf("schema-free cache-write amount=%v, want 42 (6*7)", amount)
	}
	if amount := wireComponentAmount(t, legacyResult.InferenceValuation, sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken); amount == nil || amount.CanonicalString() != "20/0" {
		t.Fatalf("schema-free cache-read amount=%v, want 20 (4*5)", amount)
	}

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCacheReadWriteRules()))
	if !wireOverlapRejected(err) {
		t.Fatalf("aggregate+cache-read+cache-write err=%v, want typed overlap; total=%s",
			err, wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("aggregate+cache-read+cache-write completeness=%q, want conflict", result.InferenceValuation.Completeness)
	}
}

// TestOpenAIUsageEvidence_CacheWriteExtensionAliasOverlaps proves the
// non-standard x_lip_cache_write_tokens extension fallback is governed by the
// same frozen subset edge: an aggregate+extension cache-write tariff must be a
// typed overlap instead of an additive double bill.
func TestOpenAIUsageEvidence_CacheWriteExtensionAliasOverlaps(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireChatCacheWriteExtension, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-ext"))[0]
	call, leg, policy := wireRetailCall(t, callID, observation)

	legacyResult, legacyErr := rateWireRetail(t, call, leg, policy, wireTariff(t, wireCacheWriteExtensionOverlapRules()))
	if legacyErr != nil {
		t.Fatalf("schema-free control must rate additively: %v", legacyErr)
	}
	if total := wireValuationTotal(legacyResult.InferenceValuation); total != "91/0" {
		t.Fatalf("schema-free extension control total=%s, want additive 91", total)
	}

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCacheWriteExtensionOverlapRules()))
	if !wireOverlapRejected(err) {
		t.Fatalf("aggregate+extension cache-write err=%v, want typed overlap; total=%s",
			err, wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("aggregate+extension cache-write completeness=%q, want conflict", result.InferenceValuation.Completeness)
	}
}

// TestOpenAIUsageEvidence_CacheWriteAggregateOnlyStaysComplete proves the
// subset edge is a partial containment: an aggregate-only tariff whose
// inclusive parents are priced and whose cache-write subset has no rule stays
// complete at the literal aggregate total 49/0 and never lets the included
// subset become independently billable.
func TestOpenAIUsageEvidence_CacheWriteAggregateOnlyStaysComplete(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireChatCacheWriteSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-agg"))[0]
	call, leg, policy := wireRetailCall(t, callID, observation)

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCacheWriteAggregateOnlyRules()))
	if err != nil {
		t.Fatalf("aggregate-only tariff must rate complete, got %v; lines=%+v", err, result.InferenceValuation.Lines)
	}
	if result.InferenceValuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("aggregate-only completeness=%q, want complete", result.InferenceValuation.Completeness)
	}
	if total := wireValuationTotal(result.InferenceValuation); total != "49/0" {
		t.Fatalf("aggregate-only total=%s, want 49 (11*2 + 9*3)", total)
	}
	if amount := wireComponentAmount(t, result.InferenceValuation, sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken); amount != nil && amount.Coefficient != "0" {
		t.Fatalf("included cache-write must not be independently payable: %v", amount)
	}
}

// TestOpenAIUsageEvidence_CacheWriteChildOnlyStaysPartial proves the subset
// edge never proves the parent aggregate's complete coverage: a tariff that
// prices only the cache-write subset leaves the inclusive parent unpriced and
// must stay a typed partial, with the subset line payable at its literal 42/0.
func TestOpenAIUsageEvidence_CacheWriteChildOnlyStaysPartial(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireChatCacheWriteSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-child"))[0]
	call, leg, policy := wireRetailCall(t, callID, observation)

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCacheWriteChildOnlyRules()))
	if wireOverlapRejected(err) {
		t.Fatalf("child-only cache-write tariff must not be a typed overlap: %v", err)
	}
	if err == nil {
		t.Fatalf("unpriced aggregate cannot falsely complete; total=%s", wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessPartial {
		t.Fatalf("child-only completeness=%q, want partial", result.InferenceValuation.Completeness)
	}
	amount := wireComponentAmount(t, result.InferenceValuation, sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken)
	if amount == nil || amount.CanonicalString() != "42/0" {
		t.Fatalf("child-only cache-write amount=%v, want 42 (6*7)", amount)
	}
}

// TestOpenAIUsageEvidence_CacheWriteShapesAbsentNullZeroNotOverlap covers the
// absent, null and explicit-zero standard member shapes through the real
// adapter. Absent and null carry no provider quantity; an explicit zero is
// present but has no payable contribution. None may manufacture a priced
// overlap: the aggregate+cache-write tariff stays complete at the literal
// aggregate total 49/0.
func TestOpenAIUsageEvidence_CacheWriteShapesAbsentNullZeroNotOverlap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		promptDetails string
		hasMeasure    bool
	}{
		{name: "absent", promptDetails: `{}`},
		{name: "null", promptDetails: `{"cache_write_tokens":null}`},
		{name: "explicit_zero", promptDetails: `{"cache_write_tokens":0}`, hasMeasure: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, wireChatCacheWriteUsage(tc.promptDetails), wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-shape-"+tc.name))[0]
			if tc.hasMeasure {
				assertNativeInputCacheWrite(t, observation, "0")
			}
			call, leg, policy := wireRetailCall(t, callID, observation)

			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCacheWriteBothPricedRules()))
			if wireOverlapRejected(err) {
				t.Fatalf("%s cache-write must not be a priced overlap: %v", tc.name, err)
			}
			if err != nil {
				t.Fatalf("%s aggregate+cache-write tariff must rate: %v", tc.name, err)
			}
			if result.InferenceValuation.Completeness != economics.CompletenessComplete {
				t.Fatalf("%s completeness=%q, want complete", tc.name, result.InferenceValuation.Completeness)
			}
			if total := wireValuationTotal(result.InferenceValuation); total != "49/0" {
				t.Fatalf("%s total=%s, want 49", tc.name, total)
			}
		})
	}
}

// TestOpenAIUsageEvidence_CacheWriteMalformedStandardBeatsExtensionAlias proves
// a present-but-malformed standard cache_write_tokens member is authoritative:
// the observation is retained, the malformed native field is an explicit
// unavailable input cache-write measure that claims the component, and a
// conflicting valid x_lip_cache_write_tokens extension alias must neither stand
// in for it nor add a second observed default-inclusion measure. The child-only
// rating fails closed partial rather than falsely completing.
func TestOpenAIUsageEvidence_CacheWriteMalformedStandardBeatsExtensionAlias(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireChatCacheWriteUsage(`{"cache_write_tokens":"oops","x_lip_cache_write_tokens":6}`), wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-malformed"))[0]

	var found int
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentCacheWriteInputToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		found++
		if measure.Key.SchemaID != "openai.usage.v2" {
			t.Fatalf("malformed standard cache_write schema = %q, want the native detail schema", measure.Key.SchemaID)
		}
		if measure.Quality != sdkmetering.QualityUnavailable || measure.Value != nil {
			t.Fatalf("malformed standard cache_write measure = %+v, want unavailable with no value", measure)
		}
	}
	if found != 1 {
		t.Fatalf("malformed standard + extension produced %d input cache_write measures, want one unavailable: %+v", found, observation.Measures)
	}
	assertNoDefaultInputCacheWrite(t, observation)
	for _, field := range observation.Evidence {
		if field.Path == "$.usage.prompt_tokens_details.cache_write_tokens" || field.Lexeme == "6" {
			t.Fatalf("malformed standard + extension retained unauthorized evidence: %+v", field)
		}
	}

	call, leg, policy := wireRetailCall(t, callID, observation)
	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCacheWriteChildOnlyRules()))
	if err == nil {
		t.Fatalf("malformed standard cache-write must fail closed; total=%s", wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessPartial {
		t.Fatalf("malformed standard cache-write completeness=%q, want partial", result.InferenceValuation.Completeness)
	}
}
