package geminigenerate

import (
	"testing"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"google.golang.org/genai"
)

func TestGeminiNativeMeasuresPreserveModalityDirectionCacheReasoningAndGroundedTool(t *testing.T) {
	u := &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:           20,
		CandidatesTokenCount:       8,
		ThoughtsTokenCount:         3,
		CachedContentTokenCount:    4,
		ToolUsePromptTokenCount:    2,
		TotalTokenCount:            33,
		PromptTokensDetails:        []*genai.ModalityTokenCount{{Modality: genai.MediaModalityText, TokenCount: 10}, {Modality: genai.MediaModalityImage, TokenCount: 10}},
		CandidatesTokensDetails:    []*genai.ModalityTokenCount{{Modality: genai.MediaModalityText, TokenCount: 8}},
		CacheTokensDetails:         []*genai.ModalityTokenCount{{Modality: genai.MediaModalityImage, TokenCount: 4}},
		ToolUsePromptTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityText, TokenCount: 2}},
	}
	measures := geminiNativeMeasures(u)
	if len(measures) != 6 {
		t.Fatalf("native Gemini measures = %d, want 6", len(measures))
	}
	hasInputImage, hasOutputText, hasCachedImage, hasGrounded := false, false, false, false
	for _, measure := range measures {
		switch {
		case measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionInput && dimensionValue(measure.Key, "evidence_plane") == "prompt":
			hasInputImage = true
		case measure.Key.Component == sdkmetering.ComponentTextToken && measure.Key.Direction == sdkmetering.DirectionOutput:
			hasOutputText = true
		case measure.Key.Component == sdkmetering.ComponentImageToken && dimensionValue(measure.Key, "evidence_plane") == "cache":
			hasCachedImage = true
		case measure.Key.Component == "grounded_tool_token":
			hasGrounded = true
		}
	}
	if !hasInputImage || !hasOutputText || !hasCachedImage || !hasGrounded {
		t.Fatalf("missing modality/plane measures: %+v", measures)
	}
	if ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: u}); ev == nil || !ev.UsagePresence.CacheReadTokens || !ev.UsagePresence.ReasoningTokens || ev.Accounting.Source != lipapi.UsageSourceProviderReported {
		t.Fatalf("usage event lost cache/reasoning/provider presence: %+v", ev)
	}
}

func TestGeminiEvidenceDraftWithTrafficTypeRetainsSingleServiceContext(t *testing.T) {
	t.Parallel()
	u := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 2, CandidatesTokenCount: 1, TotalTokenCount: 3, TrafficType: "on_demand"}
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: u})
	if ev == nil || ev.Accounting.ServiceContext != "on_demand" {
		t.Fatalf("usage event service context=%+v", ev)
	}
	b := coremetering.NewProviderEvidenceBuffer()
	b.Add(geminiEvidenceDraft(*ev, u))
	b.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BLegID: "b-leg"})
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("Gemini service-context observations=%d, want 1", len(observations))
	}
	count := 0
	for _, field := range observations[0].Evidence {
		if field.Path == "$.provider_schema.service_context" {
			count++
			if field.Lexeme != "on_demand" {
				t.Fatalf("service context lexeme=%q", field.Lexeme)
			}
		}
	}
	if count != 1 {
		t.Fatalf("service context evidence count=%d, want 1", count)
	}
}

func TestGeminiZeroUsageMetadataRetainsPresenceWithoutInventingPrice(t *testing.T) {
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{}})
	if ev == nil || !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens || !ev.UsagePresence.TotalTokens {
		t.Fatalf("zero provider usage must remain an explicit observation: %+v", ev)
	}
	if ev.CostPresent || ev.Accounting.Source != lipapi.UsageSourceProviderReported {
		t.Fatalf("Gemini usage must not invent monetary evidence: %+v", ev)
	}
}

func TestGeminiCacheOnlyUsageRetainsCachePresence(t *testing.T) {
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{CachedContentTokenCount: 4}})
	if ev == nil || !ev.UsagePresence.CacheReadTokens || ev.CacheReadTokens != 4 {
		t.Fatalf("cache-only Gemini usage lost cache evidence: %+v", ev)
	}
}

func TestGeminiNegativeUsageFieldRemainsUnavailable(t *testing.T) {
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: -1, CandidatesTokenCount: 2, TotalTokenCount: 2}})
	if ev == nil || ev.UsagePresence.InputTokens || ev.InputTokens != 0 {
		t.Fatalf("negative Gemini input became provider evidence: %+v", ev)
	}
}

func dimensionValue(key sdkmetering.ComponentKey, name string) string {
	for _, dimension := range key.Dimensions {
		if dimension.Name == name {
			return dimension.Value
		}
	}
	return ""
}
