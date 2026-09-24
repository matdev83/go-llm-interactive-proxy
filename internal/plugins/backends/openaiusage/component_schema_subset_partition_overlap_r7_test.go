package openaiusage_test

// R7 (PR #659 adversarial repair, minimum double-charge blocker): real
// OpenAI Chat adapter -> provider evidence buffer -> explicitly published
// frozen inclusion schema -> retail rater.
//
// OpenAI prompt_tokens_details reports an included cached_tokens subset while
// the native text/audio pair forms a complete partition of the ordinary prompt
// total. The frozen provider-family schema declares both. When the tariff prices
// the complete text/audio partition AND the included cached subset but leaves
// the aggregate parent unpriced, the cached tokens are already inside the
// partition, so the rater must fail closed as a typed overlap instead of posting
// the additive 8*2 + 3*5 + 4*7 = 59 double charge. The output/reasoning and
// input/cache-write sibling slices are the same shape.
//
// Every vector drives the production adapter path, never a hand-built measure
// slice.

import (
	"errors"
	"testing"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// wireSubsetPartitionChatCachedSuccess is the exact cited Chat shape:
// prompt_tokens 11 = text 8 + audio 3, with an included cached subset of 4.
const wireSubsetPartitionChatCachedSuccess = `{"id":"chat-subset-partition-cached","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"cached_tokens":4},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

// wireSubsetPartitionChatReasoningSuccess is the output-side analogue: the
// ordinary completion total carries the included reasoning subset while the
// native output text/audio pair is a complete partition.
const wireSubsetPartitionChatReasoningSuccess = `{"id":"chat-subset-partition-reasoning","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"reasoning_tokens":3}}}`

// wireSubsetPartitionChatCacheWriteSuccess is the standard cache-write sibling:
// the ordinary prompt total carries the included standard cache_write_tokens
// subset (emitted under the native detail schema) alongside the text/audio
// partition.
const wireSubsetPartitionChatCacheWriteSuccess = `{"id":"chat-subset-partition-cache-write","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"cache_write_tokens":5},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

func wireSubsetPartitionAssertNotPayable(t *testing.T, result corebilling.RetailRatingResult, direction sdkmetering.FlowDirection, component string) {
	t.Helper()
	if amount := wireComponentAmount(t, result.InferenceValuation, direction, component); amount != nil && amount.Coefficient != "0" {
		t.Fatalf("conflicting %s/%s must not be payable, got %v in %+v", direction, component, amount, result.InferenceValuation.Lines)
	}
}

// TestOpenAIUsageEvidence_PricedSubsetConflictsWithCompletePartition is the
// intact-wire RED vector. Before the repair the cached fixture posted the
// additive 59 total; after the repair the frozen complete partition plus priced
// subset is a typed fail-closed overlap.
func TestOpenAIUsageEvidence_PricedSubsetConflictsWithCompletePartition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		chatJSON   string
		rules      []economics.RatingRule
		additive   string
		direction  sdkmetering.FlowDirection
		partition  []string
		subsetComp string
	}{
		{
			name:     "input_cached",
			chatJSON: wireSubsetPartitionChatCachedSuccess,
			rules: []economics.RatingRule{
				wireLinearRule("input-text", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentTextToken), "2"),
				wireLinearRule("input-audio", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken), "5"),
				wireLinearRule("input-cache-read", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken), "7"),
			},
			additive:   "59/0",
			direction:  sdkmetering.DirectionInput,
			partition:  []string{sdkmetering.ComponentTextToken, sdkmetering.ComponentAudioToken},
			subsetComp: sdkmetering.ComponentCacheReadInputToken,
		},
		{
			name:     "output_reasoning",
			chatJSON: wireSubsetPartitionChatReasoningSuccess,
			rules: []economics.RatingRule{
				wireLinearRule("output-text", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken), "3"),
				wireLinearRule("output-audio", wireComponentKey(sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken), "7"),
				wireLinearRule("output-reasoning", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentReasoningOutputToken), "11"),
			},
			additive:   "76/0",
			direction:  sdkmetering.DirectionOutput,
			partition:  []string{sdkmetering.ComponentTextToken, sdkmetering.ComponentAudioToken},
			subsetComp: sdkmetering.ComponentReasoningOutputToken,
		},
		{
			name:     "input_cache_write",
			chatJSON: wireSubsetPartitionChatCacheWriteSuccess,
			rules: []economics.RatingRule{
				wireLinearRule("input-text", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentTextToken), "2"),
				wireLinearRule("input-audio", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken), "5"),
				wireLinearRule("input-cache-write", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheWriteInputToken), "9"),
			},
			additive:   "76/0",
			direction:  sdkmetering.DirectionInput,
			partition:  []string{sdkmetering.ComponentTextToken, sdkmetering.ComponentAudioToken},
			subsetComp: sdkmetering.ComponentCacheWriteInputToken,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chatJSON, wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, tc.name))[0]
			call, leg, policy := wireRetailCall(t, callID, observation)

			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, tc.rules))
			if !errors.Is(err, corebilling.ErrSchemaOverlapConflict) {
				t.Fatalf("%s error=%v, want ErrSchemaOverlapConflict; total=%s lines=%+v",
					tc.name, err, wireValuationTotal(result.InferenceValuation), result.InferenceValuation.Lines)
			}
			if result.InferenceValuation.Completeness != economics.CompletenessConflict {
				t.Fatalf("%s completeness=%q, want conflict", tc.name, result.InferenceValuation.Completeness)
			}
			if total := wireValuationTotal(result.InferenceValuation); total == tc.additive {
				t.Fatalf("%s must not post the additive double charge %s", tc.name, tc.additive)
			}
			wireSubsetPartitionAssertNotPayable(t, result, tc.direction, tc.subsetComp)
			for _, component := range tc.partition {
				wireSubsetPartitionAssertNotPayable(t, result, tc.direction, component)
			}
		})
	}
}

// wireZeroAudioSubsetChatSuccess is the exact zero-boundary Chat shape Astra
// cited: the ordinary prompt total is 8, the native text/audio partition is
// 8 + 0, and the included cached subset is 4. The observed zero audio token is
// an explicit, complete partition member, so the positively priced cached
// subset still double-charges inside the complete partition.
const wireZeroAudioSubsetChatSuccess = `{"id":"chat-zero-audio-subset","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":9,"total_tokens":17,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":0,"cached_tokens":4},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

// TestOpenAIUsageEvidence_ObservedZeroPartitionChildStillConflicts is the
// intact-wire zero-boundary RED vector. Before the repair the rater posted the
// additive input text 8*2 + cached 4*7 = 44 double charge because the zero
// audio child was excluded from the positive-payable set. After the repair the
// explicitly observed zero audio member still proves the complete text/audio
// partition, so the priced cached subset fails closed as the typed overlap.
func TestOpenAIUsageEvidence_ObservedZeroPartitionChildStillConflicts(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireZeroAudioSubsetChatSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "zero-audio"))[0]

	// The adapter must surface an explicit observed-zero input audio measure;
	// otherwise this vector is not exercising the observed-zero boundary.
	assertObservedZeroInputAudio(t, observation)

	call, leg, policy := wireRetailCall(t, callID, observation)
	rules := []economics.RatingRule{
		wireLinearRule("input-text", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentTextToken), "2"),
		wireLinearRule("input-audio", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken), "5"),
		wireLinearRule("input-cache-read", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken), "7"),
	}
	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, rules))
	if !errors.Is(err, corebilling.ErrSchemaOverlapConflict) {
		t.Fatalf("observed zero audio error=%v, want ErrSchemaOverlapConflict; total=%s lines=%+v",
			err, wireValuationTotal(result.InferenceValuation), result.InferenceValuation.Lines)
	}
	if result.InferenceValuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("observed zero audio completeness=%q, want conflict", result.InferenceValuation.Completeness)
	}
	if total := wireValuationTotal(result.InferenceValuation); total == "44/0" {
		t.Fatalf("observed zero audio must not post the additive double charge 44/0")
	}
	wireSubsetPartitionAssertNotPayable(t, result, sdkmetering.DirectionInput, sdkmetering.ComponentTextToken)
	wireSubsetPartitionAssertNotPayable(t, result, sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken)
}

func assertObservedZeroInputAudio(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Direction != sdkmetering.DirectionInput || measure.Key.Component != sdkmetering.ComponentAudioToken {
			continue
		}
		if measure.Quality != sdkmetering.QualityObserved || measure.Value == nil || measure.Value.Coefficient != "0" {
			t.Fatalf("input audio measure = %+v, want explicit observed zero", measure)
		}
		return
	}
	t.Fatalf("explicit observed-zero input audio measure missing: %+v", observation.Measures)
}
