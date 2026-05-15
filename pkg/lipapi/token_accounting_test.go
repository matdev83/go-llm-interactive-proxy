package lipapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestTokenAccounting_zeroValueCompatibility(t *testing.T) {
	t.Parallel()

	var meta lipapi.UsageAccountingMetadata
	if meta.Plane != lipapi.UsagePlaneUnknown {
		t.Fatalf("plane zero value: %q", meta.Plane)
	}
	if meta.Source != lipapi.UsageSourceUnknown {
		t.Fatalf("source zero value: %q", meta.Source)
	}
	if meta.Authority != lipapi.UsageAuthorityUnknown {
		t.Fatalf("authority zero value: %q", meta.Authority)
	}

	ev := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 3}
	if len(ev.UsageScopes) != 0 {
		t.Fatalf("zero-value usage scopes: %#v", ev.UsageScopes)
	}
}

func TestEventUsageDelta_scopedUsagePreservesLegacyTotals(t *testing.T) {
	t.Parallel()

	ev := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  10,
		OutputTokens: 4,
		UsageScopes: []lipapi.ScopedUsageDelta{
			{
				InputTokens:  10,
				OutputTokens: 4,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:     lipapi.UsagePlaneProviderBillable,
					Source:    lipapi.UsageSourceProviderReported,
					Authority: lipapi.UsageAuthorityAuthoritative,
					Tokenizer: lipapi.TokenizerRef{Type: "provider", ID: "openai", Version: "2026-01", Source: "response.usage", ModelUsed: "gpt-x"},
				},
			},
			{
				InputTokens:  8,
				OutputTokens: 4,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane:     lipapi.UsagePlaneClientVisible,
					Source:    lipapi.UsageSourceProxyAdjusted,
					Authority: lipapi.UsageAuthorityDelegated,
				},
			},
		},
	}

	if ev.InputTokens != 10 || ev.OutputTokens != 4 {
		t.Fatalf("legacy totals changed: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	if len(ev.UsageScopes) != 2 {
		t.Fatalf("usage scopes: %#v", ev.UsageScopes)
	}
	if ev.UsageScopes[0].Accounting.Plane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("provider plane: %#v", ev.UsageScopes[0].Accounting)
	}
	if ev.UsageScopes[1].Accounting.Plane != lipapi.UsagePlaneClientVisible {
		t.Fatalf("client plane: %#v", ev.UsageScopes[1].Accounting)
	}
}

func TestEventUsageDelta_jsonRoundTrip(t *testing.T) {
	t.Parallel()

	in := lipapi.Event{
		Kind:             lipapi.EventUsageDelta,
		InputTokens:      5,
		OutputTokens:     2,
		CacheReadTokens:  1,
		Accounting:       lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative},
		UsageScopes:      []lipapi.ScopedUsageDelta{{InputTokens: 5, OutputTokens: 2, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated}}},
		RawUsageJSON:     `{"input_tokens":5}`,
		CostNanoUnits:    1200,
		Currency:         "USD",
		CostSource:       "provider",
		ReasoningTokens:  3,
		CacheWriteTokens: 4,
		TotalTokens:      7,
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out lipapi.Event
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != lipapi.EventUsageDelta || out.InputTokens != 5 || out.OutputTokens != 2 {
		t.Fatalf("legacy fields: %#v", out)
	}
	if out.Accounting.Plane != lipapi.UsagePlaneProviderBillable || out.UsageScopes[0].Accounting.Source != lipapi.UsageSourceLocalTokenizer {
		t.Fatalf("accounting metadata: %#v", out)
	}
}

func TestFixedEventStream_deepCopiesScopedUsage(t *testing.T) {
	t.Parallel()

	events := []lipapi.Event{{
		Kind:        lipapi.EventUsageDelta,
		UsageScopes: []lipapi.ScopedUsageDelta{{InputTokens: 1, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable}}},
	}}
	stream := lipapi.NewFixedEventStream(events)
	events[0].UsageScopes[0].InputTokens = 99

	out, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out.UsageScopes[0].InputTokens = 42

	stream2 := lipapi.NewFixedEventStream([]lipapi.Event{out})
	out.UsageScopes[0].InputTokens = 77
	out2, err := stream2.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out2.UsageScopes[0].InputTokens != 42 {
		t.Fatalf("usage scopes were not deep-copied: %#v", out2.UsageScopes)
	}
}

func TestValidateEventEnvelope_rejectsOversizedAccountingFields(t *testing.T) {
	t.Parallel()

	ev := &lipapi.Event{
		Kind: lipapi.EventUsageDelta,
		Accounting: lipapi.UsageAccountingMetadata{
			Tokenizer: lipapi.TokenizerRef{ID: strings.Repeat("x", lipapi.MaxRefStringBytes+1)},
		},
	}
	if err := lipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error")
	}
}
