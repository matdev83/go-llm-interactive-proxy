package openaiusage_test

// R7-C1 (PR #659 adversarial repair): provider-family usage schema binding and
// intact wire proof.
//
// These vectors bind the frozen OpenAI Chat/Responses inclusion schema declared
// in the openaiusage package into a real immutable tariff snapshot and drive the
// production adapter -> retail selection -> rating path. They prove that:
//
//   - the schema relates the host-neutral aggregate input/output token keys to
//     the native directional text/audio child keys the adapter actually emits,
//     across the two schema identities;
//   - a both-priced tariff over intact aggregate+child evidence fails closed as
//     a typed overlap instead of posting the additive 123 total;
//   - a child-only tariff over a complete frozen partition rates the literal
//     disjoint child total (74/0) with mixed directional text/audio rates;
//   - absent/null/zero detail shapes fail closed (absent/null) or rate the
//     complete partition (explicit zero), and a conflicting flat audio alias
//     never overrides the native nested detail;
//   - schema-free legacy tariffs stay additive, so inclusion is never inferred
//     or auto-bound to unrelated providers.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const wireResponsesSuccess = `{"id":"resp-audio","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"text_tokens":8,"audio_tokens":3,"images":0},"output_tokens_details":{"text_tokens":5,"audio_tokens":4,"images":0}}}`

// TestNativeUsageInclusionSchemaDeclaresDirectionalTextAudioPartition pins the
// frozen provider-family schema shape: explicit partition edges for the
// directional text/audio children, plus OPTIONAL partition edges for the
// native image token members (input and output), and the bounded subset edges
// for the included cached (input), reasoning (output) and cache-write (input)
// subsets. The cache-write subset has two edges because the standard native
// member and the compatible-provider extension fallback are emitted under
// different schema identities; the adapter never lets both bind the same
// observation. The optional partition kind is deliberate: image tokens are a
// disjoint member of the prompt/completion partition (so text+audio+image rates
// completely, and an aggregate+image tariff must fail closed), but the member
// is reported only when the request/response carried image media, so an omitted
// image member keeps the text/audio partition complete without inventing a
// provider zero. The cached/reasoning/cache-write subsets are partial
// containments, so an aggregate-only tariff over them stays complete and a
// child-only tariff over one can never prove the parent's complete coverage.
// The output image token edge only ever binds the recognized compatible-provider
// flat alias vocabulary the adapter emits; the native OpenAI output detail has
// no image token member. The cached/reasoning subset children share the default
// inclusion schema identity with their aggregates because the adapter emits them
// there (providerTokenMeasures), while the native text/audio/image/cache-write
// children live under the native detail schema. It validates the published
// schema set exactly as BuildTariffSnapshotWithSchemas will.
func TestNativeUsageInclusionSchemaDeclaresDirectionalTextAudioPartition(t *testing.T) {
	t.Parallel()
	schemas := openaiusage.NativeUsageInclusionSchemas()
	if err := sdkmetering.ValidateComponentSchemas(schemas); err != nil {
		t.Fatalf("frozen inclusion schema must validate: %v", err)
	}
	if len(schemas) != 1 {
		t.Fatalf("schema count = %d, want 1", len(schemas))
	}
	schema := schemas[0]
	if schema.ID != openaiusage.NativeUsageInclusionSchemaID {
		t.Fatalf("schema id = %q, want %q", schema.ID, openaiusage.NativeUsageInclusionSchemaID)
	}
	if len(schema.Relationships) != 10 {
		t.Fatalf("relationship count = %d, want 10: %+v", len(schema.Relationships), schema.Relationships)
	}
	type edge struct {
		kind        sdkmetering.RelationshipKind
		direction   string
		parent      string
		child       string
		childSchema string
		optional    bool
	}
	seen := make(map[edge]struct{})
	partitionEdges, subsetEdges := 0, 0
	for _, relationship := range schema.Relationships {
		switch relationship.Kind {
		case sdkmetering.RelationshipPartition:
			partitionEdges++
		case sdkmetering.RelationshipSubset:
			subsetEdges++
		default:
			t.Fatalf("relationship kind = %q, want partition or subset", relationship.Kind)
		}
		if relationship.Optional && relationship.Kind != sdkmetering.RelationshipPartition {
			t.Fatalf("optional %q relationship is not a partition member: %+v", relationship.Kind, relationship)
		}
		if relationship.Parent.SchemaID != sdkmetering.DefaultInclusionSchemaID {
			t.Fatalf("parent schema = %q, want %q", relationship.Parent.SchemaID, sdkmetering.DefaultInclusionSchemaID)
		}
		switch relationship.Child.SchemaID {
		case openaiusage.NativeUsageSchemaID, sdkmetering.DefaultInclusionSchemaID:
		default:
			t.Fatalf("child schema = %q, want %q or %q", relationship.Child.SchemaID,
				openaiusage.NativeUsageSchemaID, sdkmetering.DefaultInclusionSchemaID)
		}
		if relationship.Parent.Unit != sdkmetering.UnitToken || relationship.Child.Unit != sdkmetering.UnitToken {
			t.Fatalf("edge units = %q/%q, want token", relationship.Parent.Unit, relationship.Child.Unit)
		}
		if relationship.Parent.Direction != relationship.Child.Direction {
			t.Fatalf("edge direction mismatch: %q -> %q", relationship.Parent.Direction, relationship.Child.Direction)
		}
		seen[edge{relationship.Kind, string(relationship.Parent.Direction), relationship.Parent.Component, relationship.Child.Component, relationship.Child.SchemaID, relationship.Optional}] = struct{}{}
	}
	for _, want := range []edge{
		{sdkmetering.RelationshipPartition, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentTextToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipPartition, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentAudioToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipPartition, "output", sdkmetering.ComponentOutputToken, sdkmetering.ComponentTextToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipPartition, "output", sdkmetering.ComponentOutputToken, sdkmetering.ComponentAudioToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipPartition, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentImageToken, openaiusage.NativeUsageSchemaID, true},
		{sdkmetering.RelationshipPartition, "output", sdkmetering.ComponentOutputToken, sdkmetering.ComponentImageToken, openaiusage.NativeUsageSchemaID, true},
		{sdkmetering.RelationshipSubset, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentCacheReadInputToken, sdkmetering.DefaultInclusionSchemaID, false},
		{sdkmetering.RelationshipSubset, "output", sdkmetering.ComponentOutputToken, sdkmetering.ComponentReasoningOutputToken, sdkmetering.DefaultInclusionSchemaID, false},
		{sdkmetering.RelationshipSubset, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentCacheWriteInputToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipSubset, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentCacheWriteInputToken, sdkmetering.DefaultInclusionSchemaID, false},
	} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("schema missing edge %+v: %+v", want, seen)
		}
	}
	if partitionEdges != 6 {
		t.Fatalf("partition edge count = %d, want 4 text/audio + 2 optional image partition edges", partitionEdges)
	}
	if subsetEdges != 4 {
		t.Fatalf("subset edge count = %d, want 4 included native/default subset edges", subsetEdges)
	}
}

// TestNativeUsageInclusionSchemaBindsAdapterWireKeys proves the schema is bound
// to the adapter's real wire-family vocabulary rather than a hand-written
// assumption. Every declared child key must be a key the adapter actually emits
// for native/detail measures, and every declared parent key must be a key the
// adapter actually emits for the ordinary aggregate totals. A drift on either
// side fails this test instead of silently mis-binding the partition. The
// cached/reasoning subset children are emitted by providerTokenMeasures under
// the default inclusion schema (not the native detail schema), so they are bound
// from the provider evidence draft alongside the aggregate parents.
func TestNativeUsageInclusionSchemaBindsAdapterWireKeys(t *testing.T) {
	t.Parallel()
	schema := openaiusage.NativeUsageInclusionSchema()

	raw := `{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,"output_image_tokens":1,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2,"cached_tokens":4,"cache_write_tokens":5},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"reasoning_tokens":3}}`
	nativeKeys := make(map[string]struct{})
	for _, measure := range openaiusage.NativeUsageMeasures(raw) {
		switch measure.Key.Component {
		case sdkmetering.ComponentTextToken, sdkmetering.ComponentAudioToken, sdkmetering.ComponentImageToken, sdkmetering.ComponentCacheWriteInputToken:
			key, err := measure.Key.Normalize()
			if err != nil {
				t.Fatalf("native key normalize: %v", err)
			}
			nativeKeys[key.CanonicalKey()] = struct{}{}
		}
	}
	if len(nativeKeys) != 7 {
		t.Fatalf("adapter native text/audio/image/cache-write keys = %d, want 7: %+v", len(nativeKeys), nativeKeys)
	}

	event := lipapi.Event{
		Kind:             lipapi.EventUsageDelta,
		InputTokens:      13,
		OutputTokens:     9,
		CacheReadTokens:  4,
		CacheWriteTokens: 5,
		ReasoningTokens:  3,
		TotalTokens:      22,
		UsagePresence: lipapi.UsagePresence{
			InputTokens: true, OutputTokens: true,
			CacheReadTokens: true, CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
		},
		RawUsageJSON: raw,
	}
	aggregateKeys := make(map[string]struct{})
	defaultDetailKeys := make(map[string]struct{})
	for _, measure := range openaiusage.ProviderEvidenceDraft(event, "openai.chat.v2", "bind-src").Measures {
		key, err := measure.Key.Normalize()
		if err != nil {
			t.Fatalf("provider measure key normalize: %v", err)
		}
		switch measure.Key.Component {
		case sdkmetering.ComponentInputToken, sdkmetering.ComponentOutputToken:
			aggregateKeys[key.CanonicalKey()] = struct{}{}
		case sdkmetering.ComponentCacheReadInputToken, sdkmetering.ComponentReasoningOutputToken:
			defaultDetailKeys[key.CanonicalKey()] = struct{}{}
		case sdkmetering.ComponentCacheWriteInputToken:
			// The standard member is emitted under the native detail schema; the
			// extension fallback is emitted under the default inclusion schema.
			// Both edges must be bound, so only the default-inclusion spelling is
			// collected here (the native spelling is bound from NativeUsageMeasures).
			if measure.Key.SchemaID == sdkmetering.DefaultInclusionSchemaID {
				defaultDetailKeys[key.CanonicalKey()] = struct{}{}
			}
		}
	}
	if len(aggregateKeys) != 2 {
		t.Fatalf("adapter aggregate keys = %d, want 2: %+v", len(aggregateKeys), aggregateKeys)
	}
	if len(defaultDetailKeys) != 3 {
		t.Fatalf("adapter default-schema cached/reasoning/cache-write keys = %d, want 3: %+v", len(defaultDetailKeys), defaultDetailKeys)
	}

	childBound, parentBound := 0, 0
	for _, relationship := range schema.Relationships {
		child, _ := relationship.Child.Normalize()
		parent, _ := relationship.Parent.Normalize()
		if _, ok := nativeKeys[child.CanonicalKey()]; ok {
			childBound++
		} else if _, ok := defaultDetailKeys[child.CanonicalKey()]; ok {
			childBound++
		} else {
			t.Fatalf("schema child %s is not bound to any adapter-emitted key", child.CanonicalKey())
		}
		if _, ok := aggregateKeys[parent.CanonicalKey()]; ok {
			parentBound++
		} else {
			t.Fatalf("schema parent %s is not bound to an adapter aggregate key", parent.CanonicalKey())
		}
	}
	if childBound != 10 {
		t.Fatalf("schema children not bound to adapter keys: bound=%d want 10", childBound)
	}
	if parentBound != 10 {
		t.Fatalf("schema parents not bound to adapter aggregate keys: bound=%d want 10", parentBound)
	}
}

// TestNativeUsageInclusionSchemaParticipatesInTariffIdentity proves the frozen
// provider schema is material tariff identity: publishing the same rules with
// and without the schema yields different content hashes, and the returned
// schema slice is a fresh deep copy each call.
func TestNativeUsageInclusionSchemaParticipatesInTariffIdentity(t *testing.T) {
	t.Parallel()
	rules := []economics.RatingRule{wireLinearRule("input", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken), "2")}
	ref := economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "openai-wire-tariff", Version: "v1"}, RaterID: "test-rater"}
	legacy, err := economics.BuildTariffSnapshot(ref, "USD", rules)
	if err != nil {
		t.Fatalf("BuildTariffSnapshot: %v", err)
	}
	withSchema, err := economics.BuildTariffSnapshotWithSchemas(ref, "USD", rules, openaiusage.NativeUsageInclusionSchemas())
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	if legacy.Content.ContentHash == withSchema.Content.ContentHash {
		t.Fatal("frozen provider schema must participate in the tariff content hash")
	}

	first := openaiusage.NativeUsageInclusionSchemas()
	first[0].Relationships[0].Child.Component = "mutated"
	second := openaiusage.NativeUsageInclusionSchemas()
	if second[0].Relationships[0].Child.Component == "mutated" {
		t.Fatal("NativeUsageInclusionSchemas must return a fresh deep copy")
	}
}

// TestOpenAIUsageEvidence_LegacySchemaFreeTariffStaysAdditive is the control
// proving inclusion is never inferred or auto-bound: without the frozen schema
// the same intact evidence and both-priced tariff rates additively at 123/0 with
// no typed overlap. The child-only legacy tariff stays a typed partial because
// nothing declares the complete partition.
func TestOpenAIUsageEvidence_LegacySchemaFreeTariffStaysAdditive(t *testing.T) {
	t.Parallel()
	server := wireAudioServer(t)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "legacy"))[0]

	call, leg, policy := wireRetailCall(t, callID, observation)
	fullResult, fullErr := rateWireRetail(t, call, leg, policy, wireTariff(t, wireFullInclusionRules()))
	if wireOverlapRejected(fullErr) {
		t.Fatalf("schema-free tariff must not be typed as an overlap: %v", fullErr)
	}
	if fullErr != nil {
		t.Fatalf("schema-free additive tariff must rate: %v", fullErr)
	}
	if total := wireValuationTotal(fullResult.InferenceValuation); total != "123/0" {
		t.Fatalf("schema-free aggregate+child total=%s, want additive 123", total)
	}

	childResult, childErr := rateWireRetail(t, call, leg, policy, wireTariff(t, wireChildPartitionRules()))
	if childErr == nil {
		t.Fatalf("schema-free child-only tariff must stay a typed partial; got total %s", wireValuationTotal(childResult.InferenceValuation))
	}
	if childResult.InferenceValuation.Completeness != economics.CompletenessPartial {
		t.Fatalf("schema-free child-only completeness=%q, want partial", childResult.InferenceValuation.Completeness)
	}
}

// TestOpenAIUsageEvidence_FrozenSchemaBothPricedFailsClosed is the intact wire
// both-priced vector for the frozen schema: aggregate totals and native children
// are both priced, so the real retail rater must return the typed overlap and
// must not post the additive 123 total.
func TestOpenAIUsageEvidence_FrozenSchemaBothPricedFailsClosed(t *testing.T) {
	t.Parallel()
	server := wireAudioServer(t)
	defer server.Close()
	for _, path := range []string{"chat", "responses"} {
		path := path
		t.Run(path, func(t *testing.T) {
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, path, wireIdentityForCall(callID, path+"-both"))[0]
			call, leg, policy := wireRetailCall(t, callID, observation)
			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireFullInclusionRules()))
			if !wireOverlapRejected(err) {
				t.Fatalf("%s both-priced error=%v, want typed overlap; total=%s", path, err, wireValuationTotal(result.InferenceValuation))
			}
			if result.InferenceValuation.Completeness != economics.CompletenessConflict {
				t.Fatalf("%s both-priced completeness=%q, want conflict", path, result.InferenceValuation.Completeness)
			}
		})
	}
}

// TestOpenAIUsageEvidence_ChildOnlyMixedDirectionalRates pins the literal
// child-only result over the frozen partition with distinct per-direction and
// per-modality rates: text-in 8*2=16, audio-in 3*5=15, text-out 5*3=15,
// audio-out 4*7=28, total 74/0.
func TestOpenAIUsageEvidence_ChildOnlyMixedDirectionalRates(t *testing.T) {
	t.Parallel()
	server := wireAudioServer(t)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "mixed"))[0]
	call, leg, policy := wireRetailCall(t, callID, observation)

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionRules()))
	if err != nil {
		t.Fatalf("child-only frozen-partition rating must be complete, got %v; lines=%+v", err, result.InferenceValuation.Lines)
	}
	if !wireDisjointPartition(result.InferenceValuation) {
		t.Fatalf("child-only total=%s lines=%d, want the disjoint 74/0 text/audio partition",
			wireValuationTotal(result.InferenceValuation), len(result.InferenceValuation.Lines))
	}
	for _, want := range []struct {
		direction sdkmetering.FlowDirection
		component string
		amount    string
	}{
		{sdkmetering.DirectionInput, sdkmetering.ComponentTextToken, "16/0"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken, "15/0"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken, "15/0"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken, "28/0"},
	} {
		amount := wireComponentAmount(t, result.InferenceValuation, want.direction, want.component)
		if amount == nil || amount.CanonicalString() != want.amount {
			t.Fatalf("%s/%s amount=%v, want %s", want.direction, want.component, amount, want.amount)
		}
	}
}

// TestOpenAIUsageEvidence_ChildOnlyDetailShapeFailClosed covers the absent,
// null and explicit-zero native audio detail shapes through the real adapter.
// An absent or null declared partition child cannot prove the parent partition
// and keeps the typed partial; an explicit-zero child is present and complete,
// so the child-only tariff stays complete at the literal 59/0.
func TestOpenAIUsageEvidence_ChildOnlyDetailShapeFailClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		chatJSON     string
		wantComplete bool
		wantTotal    string
		wantInputTxt string
	}{
		{
			name:         "absent",
			chatJSON:     `{"id":"chat","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":11},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`,
			wantComplete: false,
			wantTotal:    "65/0",
			wantInputTxt: "22/0",
		},
		{
			name:         "null",
			chatJSON:     `{"id":"chat","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":11,"audio_tokens":null},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`,
			wantComplete: false,
			wantTotal:    "65/0",
			wantInputTxt: "22/0",
		},
		{
			name:         "explicit_zero",
			chatJSON:     `{"id":"chat","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":9,"total_tokens":17,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":0},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`,
			wantComplete: true,
			wantTotal:    "59/0",
			wantInputTxt: "16/0",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chatJSON, wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "shape-"+tc.name))[0]
			call, leg, policy := wireRetailCall(t, callID, observation)

			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionRules()))
			if tc.wantComplete {
				if err != nil {
					t.Fatalf("explicit-zero child must stay complete, got %v; lines=%+v", err, result.InferenceValuation.Lines)
				}
				if result.InferenceValuation.Completeness != economics.CompletenessComplete {
					t.Fatalf("explicit-zero completeness=%q, want complete", result.InferenceValuation.Completeness)
				}
			} else {
				if err == nil {
					t.Fatalf("incomplete detail shape must fail closed; total=%s", wireValuationTotal(result.InferenceValuation))
				}
				if result.InferenceValuation.Completeness != economics.CompletenessPartial {
					t.Fatalf("incomplete detail completeness=%q, want partial", result.InferenceValuation.Completeness)
				}
			}
			if total := wireValuationTotal(result.InferenceValuation); total != tc.wantTotal {
				t.Fatalf("child-only total=%s, want %s", total, tc.wantTotal)
			}
			amount := wireComponentAmount(t, result.InferenceValuation, sdkmetering.DirectionInput, sdkmetering.ComponentTextToken)
			if amount == nil || amount.CanonicalString() != tc.wantInputTxt {
				t.Fatalf("input text amount=%v, want %s (input child stays payable)", amount, tc.wantInputTxt)
			}
		})
	}
}

// TestOpenAIUsageEvidence_ConflictingFlatAudioAliasDoesNotFlatten proves the
// adapter keeps the native nested detail authoritative when a conflicting flat
// alias is also present, and that the frozen partition then rates the native
// value rather than the alias.
func TestOpenAIUsageEvidence_ConflictingFlatAudioAliasDoesNotFlatten(t *testing.T) {
	t.Parallel()
	chatJSON := `{"id":"chat","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"input_audio_tokens":99,"output_audio_tokens":99,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`
	server := wireUsageServer(t, chatJSON, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "alias"))[0]
	assertDirectionalAudio(t, []sdkmetering.Observation{observation}, map[string]string{
		"input/audio_token":  "3",
		"output/audio_token": "4",
	})

	call, leg, policy := wireRetailCall(t, callID, observation)
	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionRules()))
	if err != nil {
		t.Fatalf("alias fixture child-only rating must be complete, got %v", err)
	}
	if !wireDisjointPartition(result.InferenceValuation) {
		t.Fatalf("alias fixture total=%s, want native 74/0 partition", wireValuationTotal(result.InferenceValuation))
	}
}

// wireVisionChatSuccess is the official Chat CompletionUsage vision shape:
// prompt_tokens/prompt_tokens_details already include directional text/audio
// AND image input tokens. It is the exact-13/9/22 fixture Astra cited where the
// prompt details text 8 + audio 3 + image 2 account for the ordinary prompt
// total, so dropping the nested image tokens makes the remaining text/audio
// pair look like a complete partition of the parent.
const wireVisionChatSuccess = `{"id":"chat-vision","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

// TestOpenAIUsageEvidence_ChatNestedImageTokensCannotBeSilentlyIgnored is the
// R7-C1A vector. The real adapter maps prompt_tokens_details.image_tokens to a
// native directional input image token measure. A child-only tariff that prices
// the directional text/audio children has no image_token rate, so the present
// positive image tokens are unvalued: the valuation must fail closed partial
// and must never report the false complete child-only 74/0. The aggregate+child
// tariffs must likewise fail closed as a typed overlap rather than additively
// double bill the aggregate parent and its included native children.
//
// Evidence note: the exact nested wire path
// $.usage.prompt_tokens_details.image_tokens is allowlisted in the SDK, so the
// retained evidence keeps the provider's true field identity instead of a
// substitute canonical location.
func TestOpenAIUsageEvidence_ChatNestedImageTokensCannotBeSilentlyIgnored(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireVisionChatSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "vision"))[0]

	assertNativeInputImageTokens(t, observation, "2")
	assertWireEvidence(t, []sdkmetering.Observation{observation}, map[string]string{
		"$.usage.prompt_tokens_details.image_tokens": "2",
	})
	// The substitute canonical location must not be emitted for the nested
	// provider-family quantity: the exact provider wire identity is retained.
	for _, field := range observation.Evidence {
		if field.Path == "$.usage.input_image_tokens" {
			t.Fatalf("nested image token evidence used the substitute canonical path: %+v", field)
		}
	}

	call, leg, policy := wireRetailCall(t, callID, observation)

	// No image price: the present positive image tokens cannot be silently
	// ignored in a complete child-only valuation.
	childResult, childErr := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionRules()))
	if childResult.InferenceValuation.Completeness == economics.CompletenessComplete {
		t.Fatalf("unpriced native image tokens must not rate complete; total=%s lines=%+v",
			wireValuationTotal(childResult.InferenceValuation), childResult.InferenceValuation.Lines)
	}
	if childErr == nil {
		t.Fatalf("unpriced native image tokens must fail closed, got nil error and complete total %s",
			wireValuationTotal(childResult.InferenceValuation))
	}
	if childResult.InferenceValuation.Completeness != economics.CompletenessPartial {
		t.Fatalf("unpriced native image tokens completeness=%q, want partial",
			childResult.InferenceValuation.Completeness)
	}
	// The intact directional text/audio children stay payable at their literal
	// amounts even though the valuation is partial.
	for _, want := range []struct {
		direction sdkmetering.FlowDirection
		component string
		amount    string
	}{
		{sdkmetering.DirectionInput, sdkmetering.ComponentTextToken, "16/0"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken, "15/0"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken, "15/0"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken, "28/0"},
	} {
		amount := wireComponentAmount(t, childResult.InferenceValuation, want.direction, want.component)
		if amount == nil || amount.CanonicalString() != want.amount {
			t.Fatalf("partial child line %s/%s amount=%v, want %s", want.direction, want.component, amount, want.amount)
		}
	}

	// A tariff that prices the aggregate prompt total AND its included native
	// image tokens must fail closed as a typed overlap, never additively
	// double bill the same image work.
	imageOverlapResult, imageOverlapErr := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireAggregatePlusImageRules()))
	if !wireOverlapRejected(imageOverlapErr) {
		t.Fatalf("aggregate+image-token vision tariff err=%v, want typed overlap; total=%s",
			imageOverlapErr, wireValuationTotal(imageOverlapResult.InferenceValuation))
	}

	// The aggregate+children tariff must likewise fail closed as a typed
	// overlap, not post the additive aggregate+child double bill.
	fullResult, fullErr := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireFullInclusionRules()))
	if !wireOverlapRejected(fullErr) {
		t.Fatalf("aggregate+child vision tariff err=%v, want typed overlap; total=%s",
			fullErr, wireValuationTotal(fullResult.InferenceValuation))
	}
	if total := wireValuationTotal(fullResult.InferenceValuation); total == "123/0" {
		t.Fatalf("aggregate+child vision tariff must not post the additive 123 total")
	}
}

// TestOpenAIUsageEvidence_MalformedNestedImageTokensCannotComplete is the
// R7-C1B intact-wire vector for present-but-malformed Chat
// prompt_tokens_details.image_tokens shapes. The real adapter must surface an
// explicit unavailable input image token measure, and the real child-only
// retail rating must fail closed partial instead of the false complete 74/0
// text/audio total the remaining children would otherwise prove.
func TestOpenAIUsageEvidence_MalformedNestedImageTokensCannotComplete(t *testing.T) {
	t.Parallel()
	shapes := []struct {
		name      string
		imageJSON string
		forbidden string
	}{
		{name: "string", imageJSON: `"oops"`, forbidden: "oops"},
		{name: "negative", imageJSON: `-1`, forbidden: "-1"},
		{name: "fractional", imageJSON: `2.5`, forbidden: "2.5"},
		{name: "overflow", imageJSON: `1e999`, forbidden: "1e999"},
	}
	for _, shape := range shapes {
		shape := shape
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, wireChatVisionUsage(shape.imageJSON, ""), wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "malformed-"+shape.name))[0]

			assertUnavailableInputImageToken(t, observation)
			for _, field := range observation.Evidence {
				if field.Lexeme == shape.forbidden {
					t.Fatalf("malformed image token lexeme %q reached evidence: %+v", shape.forbidden, field)
				}
			}

			call, leg, policy := wireRetailCall(t, callID, observation)
			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionRules()))
			if err == nil {
				t.Fatalf("malformed present image tokens must fail closed; total=%s", wireValuationTotal(result.InferenceValuation))
			}
			if result.InferenceValuation.Completeness != economics.CompletenessPartial {
				t.Fatalf("malformed present image tokens completeness=%q, want partial; total=%s",
					result.InferenceValuation.Completeness, wireValuationTotal(result.InferenceValuation))
			}
		})
	}
}

// TestOpenAIUsageEvidence_MalformedNestedImageWithFlatAliasCannotComplete proves
// an invalid nested image token field is not silently supplanted by a valid
// compatible flat alias. Whether the flat alias prices a distinct component
// (input_image_count) or the same canonical image token component
// (input_image_tokens), the malformed nested field stays an explicit
// unavailable measure and the child-only rating cannot falsely complete.
func TestOpenAIUsageEvidence_MalformedNestedImageWithFlatAliasCannotComplete(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		flatAlias string
	}{
		{name: "image_count_alias", flatAlias: `"input_image_count":2,`},
		{name: "image_token_alias", flatAlias: `"input_image_tokens":5,`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, wireChatVisionUsage(`"oops"`, tc.flatAlias), wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "alias-"+tc.name))[0]

			assertUnavailableInputImageToken(t, observation)

			call, leg, policy := wireRetailCall(t, callID, observation)
			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionRules()))
			if err == nil {
				t.Fatalf("malformed nested image with flat alias must fail closed; total=%s",
					wireValuationTotal(result.InferenceValuation))
			}
			if result.InferenceValuation.Completeness != economics.CompletenessPartial {
				t.Fatalf("malformed nested image with flat alias completeness=%q, want partial; total=%s",
					result.InferenceValuation.Completeness, wireValuationTotal(result.InferenceValuation))
			}
		})
	}
}

// TestOpenAIUsageEvidence_NonCanonicalIntegerNestedImageLexemesCannotComplete is
// the R7-C1B reviewer follow-up intact-wire vector. The nested Chat
// prompt_tokens_details.image_tokens field is a discrete count. The adapter must
// not accept an integer-valued lexeme through a permissive decimal parser and
// then retain the original raw lexeme: the SDK safe-evidence count contract
// requires a canonical unsigned base-10 integer spelling within uint64, so a
// non-canonical raw lexeme would make Observation.Validate reject and the stock
// ProviderEvidenceBuffer drain the ENTIRE observation. This vector drives three
// such lexemes - exponent notation (1e3), a trailing-fraction zero (2.0) and a
// 20-digit value beyond the canonical count bound (99999999999999999999) - with
// no flat companion and with a valid flat companion alias, proving:
//
//   - the observation is not discarded (the intact wire path still yields it);
//   - the present-but-non-canonical nested field stays an explicit unavailable
//     input image token measure rather than a canonicalized observed count;
//   - the raw non-canonical lexeme never reaches evidence;
//   - a valid flat companion alias does not supersede the nested unavailable
//     value into a false complete child partition;
//   - the child-only retail rating fails closed partial.
func TestOpenAIUsageEvidence_NonCanonicalIntegerNestedImageLexemesCannotComplete(t *testing.T) {
	t.Parallel()
	lexemes := []struct {
		name      string
		imageJSON string
		rawLexeme string
	}{
		{name: "scientific_1e3", imageJSON: `1e3`, rawLexeme: "1e3"},
		{name: "trailing_fraction_zero_2_0", imageJSON: `2.0`, rawLexeme: "2.0"},
		{name: "twenty_digit_overflow", imageJSON: `99999999999999999999`, rawLexeme: "99999999999999999999"},
	}
	aliases := []struct {
		name      string
		flatAlias string
	}{
		{name: "without_alias", flatAlias: ""},
		{name: "with_image_token_alias", flatAlias: `"input_image_tokens":5,`},
		{name: "with_image_count_alias", flatAlias: `"input_image_count":2,`},
	}
	for _, lexeme := range lexemes {
		for _, alias := range aliases {
			lexeme, alias := lexeme, alias
			t.Run(lexeme.name+"_"+alias.name, func(t *testing.T) {
				t.Parallel()
				server := wireUsageServer(t, wireChatVisionUsage(lexeme.imageJSON, alias.flatAlias), wireResponsesSuccess)
				defer server.Close()
				callID := mustWireCallID(t)
				observation := openWireObservations(t, server, "chat",
					wireIdentityForCall(callID, "noncanon-"+lexeme.name+"-"+alias.name))[0]

				assertUnavailableInputImageToken(t, observation)
				for _, field := range observation.Evidence {
					if field.Lexeme == lexeme.rawLexeme {
						t.Fatalf("non-canonical count lexeme %q reached evidence: %+v", lexeme.rawLexeme, field)
					}
				}

				call, leg, policy := wireRetailCall(t, callID, observation)
				result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildPartitionRules()))
				if err == nil {
					t.Fatalf("non-canonical nested image tokens must fail closed; total=%s",
						wireValuationTotal(result.InferenceValuation))
				}
				if result.InferenceValuation.Completeness != economics.CompletenessPartial {
					t.Fatalf("non-canonical nested image tokens completeness=%q, want partial; total=%s",
						result.InferenceValuation.Completeness, wireValuationTotal(result.InferenceValuation))
				}
			})
		}
	}
}

// wireChatVisionUsage builds the exact-13/9/22 Chat vision fixture with the
// supplied raw image_tokens JSON value and an optional flat alias member inside
// the usage object.
func wireChatVisionUsage(imageJSON, flatAlias string) string {
	return `{"id":"chat-vision","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,` +
		flatAlias +
		`"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":` + imageJSON + `},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`
}

func assertUnavailableInputImageToken(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	var found bool
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentImageToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		found = true
		if measure.Quality != sdkmetering.QualityUnavailable {
			t.Fatalf("input image_token measure quality=%q, want unavailable: %+v", measure.Quality, measure)
		}
		if measure.Value != nil {
			t.Fatalf("unavailable input image_token measure carries a value: %+v", measure.Value)
		}
	}
	if !found {
		t.Fatalf("no explicit unavailable input image_token measure: %+v", observation.Measures)
	}
}

// wireAggregatePlusImageRules prices the aggregate prompt/output totals and the
// included native input image tokens, with no text/audio child rules. It is the
// exact overlap shape the frozen subset edge must reject.
func wireAggregatePlusImageRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("input-aggregate", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken), "2"),
		wireLinearRule("output-aggregate", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken), "3"),
		wireLinearRule("input-image-token", wireComponentKey(sdkmetering.DirectionInput, sdkmetering.ComponentImageToken), "1"),
	}
}

func assertNativeInputImageTokens(t *testing.T, observation sdkmetering.Observation, want string) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentImageToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		if measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("native input image_token unit = %q, want %q", measure.Key.Unit, sdkmetering.UnitToken)
		}
		if measure.Value == nil || measure.Value.Coefficient != want {
			t.Fatalf("native input image_token = %+v, want %s", measure.Value, want)
		}
		return
	}
	t.Fatalf("native input image_token measure missing: %+v", observation.Measures)
}

func wireUsageServer(t *testing.T, chatJSON, responsesJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = io.WriteString(w, chatJSON)
		case "/responses":
			_, _ = io.WriteString(w, responsesJSON)
		default:
			http.NotFound(w, r)
		}
	}))
}

// wireIdentityForCall binds the provider observation to the same trusted
// BillingCallID and B-leg lineage that wireRetailCall uses, so the retail
// selection accepts the observation as in-scope B-leg evidence.
func wireIdentityForCall(callID corebilling.BillingCallID, label string) coremetering.ObservationIdentity {
	return coremetering.ObservationIdentity{
		StoreID: "store", RequestID: label + "-request",
		CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: "a-leg", BLegID: "b-leg", AttemptID: "b-leg",
	}
}

func mustWireCallID(t *testing.T) corebilling.BillingCallID {
	t.Helper()
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatalf("NewBillingCallID: %v", err)
	}
	return callID
}

func wireComponentAmount(t *testing.T, valuation economics.Valuation, direction sdkmetering.FlowDirection, component string) *sdkmetering.Decimal {
	t.Helper()
	for i := range valuation.Lines {
		line := &valuation.Lines[i]
		if line.Component == nil {
			continue
		}
		if line.Component.Direction == direction && line.Component.Component == component {
			return line.Amount
		}
	}
	return nil
}
