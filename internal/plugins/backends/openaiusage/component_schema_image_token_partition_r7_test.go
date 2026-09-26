package openaiusage_test

// R7 (PR #666 CodeRabbit 4098229959, native image-token overlap): the OpenAI
// Chat/Responses wire family reports prompt_tokens_details.image_tokens as a
// directional input image TOKEN quantity that is disjoint from text_tokens and
// audio_tokens; together they account for the ordinary prompt total. The frozen
// provider-family schema previously declared image tokens as a subset of the
// text/audio partition, so a tariff that priced the true text+audio+image
// partition was misclassified as an ErrSchemaOverlapConflict with no payable
// lines. These vectors pin the corrected optional-partition semantics through
// the real adapter -> provider evidence buffer -> frozen schema -> retail
// rater, and pin that absence, null and explicit zero stay distinguishable.

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// wireVisionChatWithOutputImage is a consistent Chat vision shape whose input
// and output partitions both include a native image token member:
// input 13 = text 8 + audio 3 + image 2, output 11 = text 5 + audio 4 + image 2.
const wireVisionChatWithOutputImage = `{"id":"chat-vision-out-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":11,"total_tokens":24,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4},"output_image_tokens":2}}`

// wireChildPartitionWithImageTokenRules prices the native directional
// text/audio/image token partition at deliberately distinct rates, with no
// aggregate parent rule. It extends wireChildPartitionRules (which prices the
// image COUNT component) to price the image TOKEN component the nested wire
// detail actually emits.
func wireChildPartitionWithImageTokenRules(includeOutputImage bool) []economics.RatingRule {
	rules := []economics.RatingRule{
		wireLinearRule("input-text", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentTextToken), "2"),
		wireLinearRule("output-text", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken), "3"),
		wireLinearRule("input-audio", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken), "5"),
		wireLinearRule("output-audio", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken), "7"),
		wireLinearRule("input-image-token", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentImageToken), "1"),
	}
	if includeOutputImage {
		rules = append(rules, wireLinearRule("output-image-token", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentImageToken), "1"))
	}
	return rules
}

// TestOpenAIUsageEvidence_ChatImageTokenCompletesTextAudioPartition is the
// primary RED vector. The real Chat adapter surfaces the disjoint native input
// image token alongside the text/audio partition; a child-only tariff that
// prices text, audio and the image token must rate completely with the image
// rate included (16 + 15 + 15 + 28 + 2 = 76/0) instead of failing closed as an
// overlap with no payable lines.
func TestOpenAIUsageEvidence_ChatImageTokenCompletesTextAudioPartition(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireVisionChatSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "vision-image-token"))[0]
	assertNativeInputImageTokens(t, observation, "2")

	call, leg, policy := wireRetailCall(t, callID, observation)
	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionWithImageTokenRules(false)))
	if err != nil {
		t.Fatalf("text+audio+image-token tariff must rate, got %v; completeness=%q total=%s lines=%+v",
			err, result.InferenceValuation.Completeness, wireValuationTotal(result.InferenceValuation), result.InferenceValuation.Lines)
	}
	if result.InferenceValuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q, want complete; total=%s", result.InferenceValuation.Completeness, wireValuationTotal(result.InferenceValuation))
	}
	if total := wireValuationTotal(result.InferenceValuation); total != "76/0" {
		t.Fatalf("total=%s, want 76/0 (16+15+15+28+2)", total)
	}
	amount := wireComponentAmount(t, result.InferenceValuation, sdkmetering.DirectionInput, sdkmetering.ComponentImageToken)
	if amount == nil || amount.CanonicalString() != "2/0" {
		t.Fatalf("input image token amount=%v, want 2/0", amount)
	}
}

// TestOpenAIUsageEvidence_ChatImageTokenPartitionMixed covers the mixed shape
// where both the input nested image tokens and the recognized flat output image
// token coexist with their text/audio partitions. All six directional members
// must stay payable once at their literal rates (78/0), never an overlap.
func TestOpenAIUsageEvidence_ChatImageTokenPartitionMixed(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireVisionChatWithOutputImage, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "vision-mixed"))[0]
	assertNativeInputImageTokens(t, observation, "2")

	call, leg, policy := wireRetailCall(t, callID, observation)
	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionWithImageTokenRules(true)))
	if err != nil {
		t.Fatalf("mixed text+audio+image-token tariff must rate, got %v; total=%s lines=%+v",
			err, wireValuationTotal(result.InferenceValuation), result.InferenceValuation.Lines)
	}
	if total := wireValuationTotal(result.InferenceValuation); total != "78/0" {
		t.Fatalf("mixed total=%s, want 78/0 (16+15+15+28+2+2)", total)
	}
	for _, want := range []struct {
		direction sdkmetering.FlowDirection
		component string
		amount    string
	}{
		{sdkmetering.DirectionInput, sdkmetering.ComponentTextToken, "16/0"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken, "15/0"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentImageToken, "2/0"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken, "15/0"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken, "28/0"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentImageToken, "2/0"},
	} {
		amount := wireComponentAmount(t, result.InferenceValuation, want.direction, want.component)
		if amount == nil || amount.CanonicalString() != want.amount {
			t.Fatalf("%s/%s amount=%v, want %s", want.direction, want.component, amount, want.amount)
		}
	}
}

// wireAbsentImageChatSuccess and wireAbsentImageResponsesSuccess are the clean
// audio-capable shapes with no image member at all (and no other priced detail
// member), so an omitted image token is the only evidence difference.
const wireAbsentImageChatSuccess = `{"id":"chat-no-image","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

const wireAbsentImageResponsesSuccess = `{"id":"resp-no-image","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"text_tokens":8,"audio_tokens":3},"output_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

// TestOpenAIUsageEvidence_ImageTokenAbsentStillCompletes covers the absent-image
// shapes through the real adapter. An omitted native image token is not a
// member of the reported partition, so the text/audio partition stays complete
// at 74/0; absence must never be coerced into a synthetic zero image member.
func TestOpenAIUsageEvidence_ImageTokenAbsentStillCompletes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		path    string
		chat    string
		respons string
	}{
		{name: "chat", path: "chat", chat: wireAbsentImageChatSuccess, respons: wireResponsesSuccess},
		{name: "responses", path: "responses", chat: wireVisionChatSuccess, respons: wireAbsentImageResponsesSuccess},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chat, tc.respons)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, tc.path, wireIdentityForCall(callID, "absent-image-"+tc.name))[0]

			call, leg, policy := wireRetailCall(t, callID, observation)
			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionWithImageTokenRules(true)))
			if err != nil {
				t.Fatalf("%s absent image must still complete, got %v; total=%s", tc.name, err, wireValuationTotal(result.InferenceValuation))
			}
			if result.InferenceValuation.Completeness != economics.CompletenessComplete {
				t.Fatalf("%s absent image completeness=%q, want complete", tc.name, result.InferenceValuation.Completeness)
			}
			if total := wireValuationTotal(result.InferenceValuation); total != "74/0" {
				t.Fatalf("%s absent image total=%s, want 74/0", tc.name, total)
			}
		})
	}
}

// TestOpenAIUsageEvidence_AggregatePlusImageTokenStillFailsClosed pins that the
// corrected image partition membership does not weaken aggregate overlap
// safety: a tariff pricing the ordinary aggregate parent and its included
// native image token is still a typed overlap, never an additive double bill.
func TestOpenAIUsageEvidence_AggregatePlusImageTokenStillFailsClosed(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireVisionChatSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "agg-image-token"))[0]

	call, leg, policy := wireRetailCall(t, callID, observation)
	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireAggregatePlusImageRules()))
	if !wireOverlapRejected(err) {
		t.Fatalf("aggregate+image-token err=%v, want typed overlap; total=%s",
			err, wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("aggregate+image-token completeness=%q, want conflict", result.InferenceValuation.Completeness)
	}
}
