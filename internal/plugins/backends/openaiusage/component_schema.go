package openaiusage

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	// NativeUsageSchemaID qualifies the native OpenAI-family usage detail
	// measures this adapter emits (for example text_token / audio_token). It is
	// the provider-family detail schema identity and is intentionally distinct
	// from the host-neutral aggregate schema
	// (metering.DefaultInclusionSchemaID) that carries the ordinary
	// input_token / output_token totals.
	NativeUsageSchemaID = "openai.usage.v2"

	// NativeUsageInclusionSchemaID names the immutable OpenAI Chat/Responses
	// provider-family inclusion schema. The schema relates the host-neutral
	// aggregate totals to their native directional text/audio details across
	// the two schema identities.
	NativeUsageInclusionSchemaID = "openai.usage.inclusion.v1"

	nativeUsageInclusionVersion = "1"
)

// NativeUsageInclusionSchema returns the frozen OpenAI Chat/Responses
// provider-family inclusion schema.
//
// OpenAI reports prompt_tokens / completion_tokens (and the Responses
// input_tokens / output_tokens) as totals that include their directional
// prompt_tokens_details / completion_tokens_details
// (input_tokens_details / output_tokens_details) children. For the
// audio-capable wire family the native token partition is the directional
// text/audio pair: text_tokens + audio_tokens account for the ordinary token
// total. This schema declares that explicit parent/child inclusion so the
// accounting rater can select one disjoint partition or reject a tariff that
// prices both sides, instead of additively double-charging the same work.
//
// Chat prompt_tokens_details additionally reports image_tokens (image input
// tokens present in the prompt). Image tokens are included in the ordinary
// prompt total just like text/audio, but they are declared as a subset child,
// not as a partition member: a model that reports only text/audio (no
// image_tokens) still has a complete text+audio partition of the prompt total,
// while a tariff that prices both the aggregate and its native image tokens is
// still rejected as an overlap instead of double charged. Declaring image
// tokens as a partition member would wrongly make every audio-only text/audio
// observation incomplete merely because the optional image field is absent.
//
// The native Chat/Responses output detail has no image token member, so the
// only supported source of a native directional output image token is the
// recognized compatible-provider flat alias family
// (output_image_tokens / image_output_tokens / completion_image_tokens), which
// the adapter maps onto the same canonical output image_token component. That
// flat measure is included in the ordinary completion/output total, so it gets
// the symmetric output subset edge. The edge is bounded by construction: it can
// only bind when the adapter actually emitted that native output image token,
// which only the recognized flat vocabulary produces; no output image field is
// fabricated from a nested completion detail, and no provider is asserted to
// emit one.
//
// cached_tokens and reasoning_tokens are likewise included prompt/completion
// subsets, so they get bounded subset edges against the ordinary aggregate:
// Chat prompt_tokens_details.cached_tokens and Responses
// input_tokens_details.cached_tokens are cached tokens "present in the prompt"
// (a subset of prompt_tokens/input_tokens), and
// completion_tokens_details.reasoning_tokens /
// output_tokens_details.reasoning_tokens are "still counted in the total
// completion tokens for purposes of billing, output, and context window
// limits" (a subset of completion_tokens/output_tokens). Because the adapter
// emits those two quantities under metering.DefaultInclusionSchemaID via
// providerTokenMeasures, the edges are declared in that same schema identity.
// They are subset children, not partition members: cached/reasoning tokens are
// not the text/audio partition, so an aggregate-only tariff stays complete and
// a child-only tariff over one of them can never prove the parent's complete
// coverage.
//
// cache_write_tokens is the same kind of included input subset. The official
// OpenAI Chat CompletionUsagePromptTokensDetails and Responses
// ResponseUsageInputTokensDetails both carry a standard cache_write_tokens
// member ("The unadjusted number of prompt tokens written to cache"), and the
// adapter's native usage mapping recognizes it as an input cache_write_input_token
// detail under the native detail schema. Its edge is therefore declared against
// the native child key. The non-standard x_lip_cache_write_tokens extension is
// only a compatible-provider fallback and is emitted under the default inclusion
// schema, so a second, symmetric subset edge is declared for that default
// identity. The two edges can never bind the same observation at once: the
// adapter suppresses the extension whenever the standard member is present.
// Like the cached/reasoning edges they are subsets, not partition members.
//
// The schema is deliberately narrow:
//
//   - accepted_prediction_tokens and rejected_prediction_tokens are
//     overlapping/orthogonal output subsets and are left undeclared. A tariff
//     pricing an aggregate plus one of these undeclared subsets remains
//     additively double-billable until that relationship is declared.
//   - Only the provider-family text/audio partition children, the included
//     native image token subsets (input and output), and the included cached /
//     reasoning / cache-write subsets are declared. Publication is opt-in per
//     tariff (BuildTariffSnapshotWithSchemas); it is never auto-bound to
//     unrelated providers or legacy schema-free tariffs.
//   - The rater still fails closed when a declared partition child is absent,
//     null or incomplete, or when two parents ambiguously share a child,
//     because the generic complete-partition proof requires every declared
//     child present and rateable in the same scope.
func NativeUsageInclusionSchema() metering.ComponentSchema {
	return metering.ComponentSchema{
		ID:      NativeUsageInclusionSchemaID,
		Version: nativeUsageInclusionVersion,
		Relationships: []metering.ComponentRelationship{
			{
				Kind:   metering.RelationshipPartition,
				Parent: nativeAggregateTokenKey(metering.DirectionInput, metering.ComponentInputToken),
				Child:  nativeDetailTokenKey(metering.DirectionInput, metering.ComponentTextToken),
			},
			{
				Kind:   metering.RelationshipPartition,
				Parent: nativeAggregateTokenKey(metering.DirectionInput, metering.ComponentInputToken),
				Child:  nativeDetailTokenKey(metering.DirectionInput, metering.ComponentAudioToken),
			},
			{
				Kind:   metering.RelationshipPartition,
				Parent: nativeAggregateTokenKey(metering.DirectionOutput, metering.ComponentOutputToken),
				Child:  nativeDetailTokenKey(metering.DirectionOutput, metering.ComponentTextToken),
			},
			{
				Kind:   metering.RelationshipPartition,
				Parent: nativeAggregateTokenKey(metering.DirectionOutput, metering.ComponentOutputToken),
				Child:  nativeDetailTokenKey(metering.DirectionOutput, metering.ComponentAudioToken),
			},
			{
				Kind:   metering.RelationshipSubset,
				Parent: nativeAggregateTokenKey(metering.DirectionInput, metering.ComponentInputToken),
				Child:  nativeDetailTokenKey(metering.DirectionInput, metering.ComponentImageToken),
			},
			{
				Kind:   metering.RelationshipSubset,
				Parent: nativeAggregateTokenKey(metering.DirectionOutput, metering.ComponentOutputToken),
				Child:  nativeDetailTokenKey(metering.DirectionOutput, metering.ComponentImageToken),
			},
			{
				// cached_tokens is included in the ordinary prompt/input total
				// (providerTokenMeasures emits it under the default inclusion
				// schema), so it is a bounded subset child of that aggregate.
				Kind:   metering.RelationshipSubset,
				Parent: nativeAggregateTokenKey(metering.DirectionInput, metering.ComponentInputToken),
				Child:  defaultInclusionTokenKey(metering.DirectionInput, metering.ComponentCacheReadInputToken),
			},
			{
				// reasoning_tokens is included in the ordinary completion/output
				// total; like the cached subset it is a partial containment, not
				// a required partition member.
				Kind:   metering.RelationshipSubset,
				Parent: nativeAggregateTokenKey(metering.DirectionOutput, metering.ComponentOutputToken),
				Child:  defaultInclusionTokenKey(metering.DirectionOutput, metering.ComponentReasoningOutputToken),
			},
			{
				// The standard Chat/Responses prompt_tokens_details /
				// input_tokens_details cache_write_tokens member is a native
				// input detail the adapter emits under the native detail schema.
				Kind:   metering.RelationshipSubset,
				Parent: nativeAggregateTokenKey(metering.DirectionInput, metering.ComponentInputToken),
				Child:  nativeDetailTokenKey(metering.DirectionInput, metering.ComponentCacheWriteInputToken),
			},
			{
				// The compatible-provider x_lip_cache_write_tokens extension
				// fallback is emitted under the default inclusion schema, so its
				// subset edge is declared against that identity. The adapter
				// suppresses the extension whenever the standard member is
				// present, so this edge and the native edge never bind the same
				// observation together.
				Kind:   metering.RelationshipSubset,
				Parent: nativeAggregateTokenKey(metering.DirectionInput, metering.ComponentInputToken),
				Child:  defaultInclusionTokenKey(metering.DirectionInput, metering.ComponentCacheWriteInputToken),
			},
		},
	}
}

// NativeUsageInclusionSchemas returns a fresh frozen schema slice suitable for
// economics.BuildTariffSnapshotWithSchemas. Each call returns a deep copy so a
// caller cannot mutate the package's provider-family declaration.
func NativeUsageInclusionSchemas() []metering.ComponentSchema {
	return []metering.ComponentSchema{NativeUsageInclusionSchema()}
}

func nativeAggregateTokenKey(direction metering.FlowDirection, component string) metering.ComponentKey {
	return defaultInclusionTokenKey(direction, component)
}

// defaultInclusionTokenKey is the host-neutral aggregate schema identity for a
// directional token component. The adapter emits the cached/reasoning integration
// quantities under this same identity (providerTokenMeasures), so their subset
// edges must be declared here rather than under the native detail schema.
func defaultInclusionTokenKey(direction metering.FlowDirection, component string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: direction,
		Component: component,
		Unit:      metering.UnitToken,
		SchemaID:  metering.DefaultInclusionSchemaID,
	}
}

func nativeDetailTokenKey(direction metering.FlowDirection, component string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: direction,
		Component: component,
		Unit:      metering.UnitToken,
		SchemaID:  NativeUsageSchemaID,
	}
}
