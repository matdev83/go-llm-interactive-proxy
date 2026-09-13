package openaiusage

import (
	"encoding/json"
	"strings"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ProviderEvidenceDraft maps OpenAI-family usage into the host-only V2 seam.
// The family parser is intentionally kept here: core sees only neutral
// measures and cannot infer provider fields or prices.
func ProviderEvidenceDraft(ev lipapi.Event, mapping, sourceKey string) coremetering.ProviderEvidenceDraft {
	draft := coremetering.ProviderUsageEvent(ev, mapping, sourceKey)
	draft.Measures = append(draft.Measures, NativeUsageMeasures(ev.RawUsageJSON)...)
	draft.Evidence = append(draft.Evidence, NativeUsageEvidence(ev.RawUsageJSON)...)
	if ev.CostPresent && ev.Accounting.Source == lipapi.UsageSourceProviderReported && ev.CostNanoUnits >= 0 && strings.TrimSpace(ev.Currency) != "" {
		amount, raw := providerCostAmount(ev)
		if amount != nil {
			draft.Charges = append(draft.Charges, sdkmetering.ReportedCharge{
				ChargeItemID: "cost:" + draft.SourceEventKey,
				Amount:       amount,
				Currency:     strings.TrimSpace(ev.Currency),
				Kind:         sdkmetering.ChargeKindAggregate,
			})
			// Keep the provider's bounded amount lexeme when the response
			// actually surfaced it. A nano-unit fallback is sufficient for the
			// charge amount but must not masquerade as the provider's raw value.
			if raw != "" {
				draft.Evidence = append(draft.Evidence,
					sdkmetering.SafeEvidenceField{Path: "$.cost.amount", Lexeme: raw, Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse},
					sdkmetering.SafeEvidenceField{Path: "$.cost.currency", Lexeme: strings.TrimSpace(ev.Currency), Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse},
				)
			}
		}
	}
	return draft
}

func providerCostAmount(ev lipapi.Event) (*sdkmetering.Decimal, string) {
	raw := strings.TrimSpace(providerCostRawFromUsageJSON(ev.RawUsageJSON))
	if raw != "" {
		amount, err := sdkmetering.ParseDecimal(raw)
		if err != nil || strings.HasPrefix(amount.Coefficient, "-") {
			return nil, ""
		}
		return &amount, raw
	}
	amount := sdkmetering.DecimalFromNanoUnits(ev.CostNanoUnits)
	return &amount, ""
}

// AnnotateProviderContext attaches identifiers that were actually surfaced by
// the provider response. Empty values remain absent; no request identifier is
// synthesized from a local call ID.
func AnnotateProviderContext(ev *lipapi.Event, providerRequestID, serviceContext string) {
	if ev == nil {
		return
	}
	ev.Accounting.ProviderRequestID = strings.TrimSpace(providerRequestID)
	ev.Accounting.ServiceContext = strings.TrimSpace(serviceContext)
}

type nativeUsageSpec struct {
	keys      []string
	nested    string
	component string
	unit      string
	direction sdkmetering.FlowDirection
	path      string
	integer   bool
}

// nativeUsageSpecs contains fields used by OpenAI and compatible Responses /
// Chat endpoints. Aliases are accepted because compatible providers commonly
// expose the same quantity under prompt/input or completion/output names.
// No estimate or text-token conversion is performed.
var nativeUsageSpecs = []nativeUsageSpec{
	{keys: []string{"input_image_tokens", "image_input_tokens", "prompt_image_tokens"}, component: sdkmetering.ComponentImageToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_tokens", integer: true},
	{keys: []string{"output_image_tokens", "image_output_tokens", "completion_image_tokens"}, component: sdkmetering.ComponentImageToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_tokens", integer: true},
	{keys: []string{"input_audio_tokens", "audio_input_tokens", "prompt_audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_tokens", integer: true},
	{keys: []string{"output_audio_tokens", "audio_output_tokens", "completion_audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_tokens", integer: true},
	{keys: []string{"input_video_tokens", "video_input_tokens", "prompt_video_tokens"}, component: sdkmetering.ComponentVideoToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_tokens", integer: true},
	{keys: []string{"output_video_tokens", "video_output_tokens", "completion_video_tokens"}, component: sdkmetering.ComponentVideoToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_tokens", integer: true},
	{keys: []string{"input_document_tokens", "document_input_tokens", "prompt_document_tokens"}, component: sdkmetering.ComponentDocumentToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_document_tokens", integer: true},
	{keys: []string{"output_document_tokens", "document_output_tokens", "completion_document_tokens"}, component: sdkmetering.ComponentDocumentToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_document_tokens", integer: true},
	{keys: []string{"input_image_count", "image_input_count", "prompt_image_count"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_count", integer: true},
	{keys: []string{"output_image_count", "image_output_count", "completion_image_count"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_count", integer: true},
	{keys: []string{"input_audio_seconds", "audio_input_seconds", "prompt_audio_seconds"}, component: sdkmetering.ComponentAudio, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_seconds", integer: false},
	{keys: []string{"output_audio_seconds", "audio_output_seconds", "completion_audio_seconds"}, component: sdkmetering.ComponentAudio, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_seconds", integer: false},
	{keys: []string{"input_video_seconds", "video_input_seconds", "prompt_video_seconds"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_seconds", integer: false},
	{keys: []string{"output_video_seconds", "video_output_seconds", "completion_video_seconds"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_seconds", integer: false},
	{keys: []string{"input_video_frames", "video_input_frames", "prompt_video_frames"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitFrame, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_frames", integer: true},
	{keys: []string{"output_video_frames", "video_output_frames", "completion_video_frames"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitFrame, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_frames", integer: true},
	{keys: []string{"input_document_pages", "document_input_pages", "prompt_document_pages"}, component: sdkmetering.ComponentDocument, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionInput, path: "$.usage.input_document_pages", integer: true},
	{keys: []string{"output_document_pages", "document_output_pages", "completion_document_pages"}, component: sdkmetering.ComponentDocument, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_document_pages", integer: true},
	{keys: []string{"input_file_pages", "file_input_pages", "prompt_file_pages"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionInput, path: "$.usage.input_file_pages", integer: true},
	{keys: []string{"output_file_pages", "file_output_pages", "completion_file_pages"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_file_pages", integer: true},
	{keys: []string{"input_bytes", "request_bytes", "prompt_bytes"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitByte, direction: sdkmetering.DirectionInput, path: "$.usage.input_bytes", integer: true},
	{keys: []string{"output_bytes", "response_bytes", "completion_bytes"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitByte, direction: sdkmetering.DirectionOutput, path: "$.usage.output_bytes", integer: true},
	{nested: "input_tokens_details", keys: []string{"text_tokens"}, component: sdkmetering.ComponentTextToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_text_tokens", integer: true},
	{nested: "output_tokens_details", keys: []string{"text_tokens"}, component: sdkmetering.ComponentTextToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_text_tokens", integer: true},
	{nested: "input_tokens_details", keys: []string{"audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_tokens", integer: true},
	{nested: "output_tokens_details", keys: []string{"audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_tokens", integer: true},
	{nested: "input_tokens_details", keys: []string{"images"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_count", integer: true},
	{nested: "output_tokens_details", keys: []string{"images"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_count", integer: true},
}

// NativeUsageMeasures returns only provider-reported native units. Malformed,
// negative, fractional discrete and overflow values are omitted, preserving
// an unavailable quantity rather than coercing it to zero or text tokens.
func NativeUsageMeasures(raw string) []sdkmetering.Measure {
	fields, details := usageFields(raw)
	if len(fields) == 0 {
		return nil
	}
	measures := make([]sdkmetering.Measure, 0, len(nativeUsageSpecs))
	for _, spec := range nativeUsageSpecs {
		value, ok := nativeField(fields, details, spec)
		if !ok {
			continue
		}
		lexeme := strings.TrimSpace(string(value))
		decimal, err := sdkmetering.ParseDecimal(lexeme)
		if err != nil || strings.HasPrefix(lexeme, "-") || strings.HasPrefix(decimal.Coefficient, "-") || (spec.integer && decimal.Scale != 0) {
			continue
		}
		measure := sdkmetering.Measure{
			Key:   sdkmetering.ComponentKey{Direction: spec.direction, Component: spec.component, Unit: spec.unit, SchemaID: "openai.usage.v2"},
			Value: &decimal, Quality: sdkmetering.QualityObserved, MethodRef: "openai.usage.native.v2",
		}
		duplicate := false
		for _, prior := range measures {
			if prior.Key.CanonicalKey() == measure.Key.CanonicalKey() {
				duplicate = true
				break
			}
		}
		if !duplicate {
			measures = append(measures, measure)
		}
	}
	return measures
}

// NativeUsageEvidence returns bounded safe lexemes for the same native fields
// retained as measures. The canonical path is allowlisted by the V2 contract.
func NativeUsageEvidence(raw string) []sdkmetering.SafeEvidenceField {
	fields, details := usageFields(raw)
	if len(fields) == 0 {
		return nil
	}
	evidence := make([]sdkmetering.SafeEvidenceField, 0, len(nativeUsageSpecs))
	seen := make(map[string]struct{}, len(nativeUsageSpecs))
	for _, spec := range nativeUsageSpecs {
		value, ok := nativeField(fields, details, spec)
		if !ok {
			continue
		}
		lexeme := strings.TrimSpace(string(value))
		decimal, err := sdkmetering.ParseDecimal(lexeme)
		if err != nil || strings.HasPrefix(lexeme, "-") || strings.HasPrefix(decimal.Coefficient, "-") || (spec.integer && decimal.Scale != 0) {
			continue
		}
		if _, ok := seen[spec.path]; ok {
			// A single allowlisted path cannot retain two directional values;
			// the measure carries the direction, while the lexeme is retained
			// once for evidence.
			continue
		}
		seen[spec.path] = struct{}{}
		evidence = append(evidence, sdkmetering.SafeEvidenceField{Path: spec.path, Lexeme: lexeme, Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse})
	}
	return evidence
}

func usageFields(raw string) (map[string]json.RawMessage, map[string]map[string]json.RawMessage) {
	var root map[string]json.RawMessage
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &root) != nil {
		return nil, nil
	}
	fields := make(map[string]json.RawMessage, len(root)+8)
	details := make(map[string]map[string]json.RawMessage, 4)
	for key, value := range root {
		fields[key] = value
	}
	for _, nested := range []string{"prompt_tokens_details", "input_tokens_details", "completion_tokens_details", "output_tokens_details"} {
		var child map[string]json.RawMessage
		if value, ok := root[nested]; ok && json.Unmarshal(value, &child) == nil {
			details[nested] = child
			for key, value := range child {
				if _, exists := fields[key]; !exists {
					fields[key] = value
				}
			}
		}
	}
	return fields, details
}

func nativeField(fields map[string]json.RawMessage, details map[string]map[string]json.RawMessage, spec nativeUsageSpec) (json.RawMessage, bool) {
	if spec.nested != "" {
		return firstNativeField(details[spec.nested], spec.keys)
	}
	return firstNativeField(fields, spec.keys)
}

func firstNativeField(fields map[string]json.RawMessage, keys []string) (json.RawMessage, bool) {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || strings.TrimSpace(string(value)) == "" || strings.TrimSpace(string(value)) == "null" {
			continue
		}
		return value, true
	}
	return nil, false
}
