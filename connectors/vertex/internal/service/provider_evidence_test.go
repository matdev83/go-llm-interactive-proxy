package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestVertexUsageMapsModalityAndGroundedToolEvidence(t *testing.T) {
	u := &VertexUsageMetadata{
		PromptTokenCount: 20, CandidatesTokenCount: 8, TotalTokenCount: 32,
		CachedContentTokenCount: 4, ThoughtsTokenCount: 2, ToolUsePromptTokenCount: 1,
		PromptTokensDetails:        []VertexModalityTokenCount{{Modality: "TEXT", TokenCount: 12}, {Modality: "IMAGE", TokenCount: 8}},
		CandidatesTokensDetails:    []VertexModalityTokenCount{{Modality: "VIDEO", TokenCount: 8}},
		CacheTokensDetails:         []VertexModalityTokenCount{{Modality: "IMAGE", TokenCount: 4}},
		ToolUsePromptTokensDetails: []VertexModalityTokenCount{{Modality: "TEXT", TokenCount: 1}},
		ServiceTier:                "standard",
	}
	ev := usageEvent(u)
	if ev.Accounting.Source != lipapi.UsageSourceProviderReported || !ev.UsagePresence.CacheReadTokens || ev.CostPresent {
		t.Fatalf("Vertex usage metadata lost provider/presence semantics: %+v", ev)
	}
	draft := vertexEvidenceDraft(ev, u, "vertex.generate.v2")
	// The local parser retains six native modality/tool measures. The
	// executable bridge forwards only the representable V1 token subset and
	// does not coerce native units into text tokens.
	if len(draft.Measures) != 6 {
		t.Fatalf("Vertex native measures = %d, want 6", len(draft.Measures))
	}
	foundServiceContext := false
	for _, field := range draft.Evidence {
		if field.Path == "$.provider_schema.service_context" && field.Lexeme == "standard" {
			foundServiceContext = true
		}
	}
	if !foundServiceContext {
		t.Fatalf("Vertex service context evidence missing: %+v", draft.Evidence)
	}
	for _, measure := range draft.Measures {
		if err := measure.Validate(); err != nil {
			t.Fatalf("invalid Vertex measure %+v: %v", measure, err)
		}
	}
	if got := vertexUsageRawJSON(u); got == "" {
		t.Fatal("Vertex usage raw evidence was empty")
	}
}

func TestVertexUsageMalformedModalityIsUnavailable(t *testing.T) {
	u := &VertexUsageMetadata{PromptTokensDetails: []VertexModalityTokenCount{{Modality: "UNKNOWN", TokenCount: 2}, {Modality: "IMAGE", TokenCount: -1}}}
	if got := vertexNativeMeasures(u); len(got) != 0 {
		t.Fatalf("malformed Vertex modality details should be omitted: %+v", got)
	}
}

func TestVertexV1BridgeProjectsCanonicalUsageKey(t *testing.T) {
	input := 11
	output := 8
	total := 19
	stream := newSliceStream([]lipapi.Event{{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   input,
		OutputTokens:  output,
		TotalTokens:   total,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "vertex.generate.usage:stream",
		},
	}})
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Accounting.DedupeKey != "" {
		t.Fatalf("canonical Vertex usage retained durable key %q", ev.Accounting.DedupeKey)
	}
	evidence := stream.DrainAccountingEvidence()
	if len(evidence) != 1 || evidence[0].DedupeKey != "vertex.generate.usage:stream" {
		t.Fatalf("V1 Vertex evidence = %#v", evidence)
	}
}

func TestVertexUsagePreservesWireZeroVersusAbsent(t *testing.T) {
	var usage VertexUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":0,"totalTokenCount":0}`), &usage); err != nil {
		t.Fatal(err)
	}
	ev := usageEvent(&usage)
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.TotalTokens {
		t.Fatalf("explicit wire zero lost: %+v", ev.UsagePresence)
	}
	if ev.UsagePresence.OutputTokens || ev.UsagePresence.CacheReadTokens || ev.UsagePresence.ReasoningTokens {
		t.Fatalf("omitted Vertex counters became present: %+v", ev.UsagePresence)
	}
}

func TestVertexNegativeUsageFieldRemainsUnavailable(t *testing.T) {
	negative := -1
	ev := usageEvent(&VertexUsageMetadata{PromptTokenCount: negative, CandidatesTokenCount: 2, TotalTokenCount: 2})
	if ev.UsagePresence.InputTokens || ev.InputTokens != 0 {
		t.Fatalf("negative Vertex input became provider evidence: %+v", ev)
	}
}

func TestVertexNativeMeasuresDropsOverflowingModalityAggregate(t *testing.T) {
	t.Parallel()
	maxInt := int(^uint(0) >> 1)
	u := &VertexUsageMetadata{
		PromptTokensDetails: []VertexModalityTokenCount{
			{Modality: "TEXT", TokenCount: maxInt},
			{Modality: "TEXT", TokenCount: maxInt},
		},
	}
	for _, measure := range vertexNativeMeasures(u) {
		if measure.Value != nil && strings.HasPrefix(measure.Value.Coefficient, "-") {
			t.Fatalf("overflowing modality aggregate became negative: %+v", measure)
		}
	}
}
