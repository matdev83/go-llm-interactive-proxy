package backendplugin_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestUsageEvidenceBuffer_BridgesProviderUsageWithPresence(t *testing.T) {
	t.Parallel()
	in, out, cache := int64(0), int64(7), int64(3)
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     int(in),
		OutputTokens:    int(out),
		CacheReadTokens: int(cache),
		UsagePresence:   lipapi.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-1",
		},
	}, "provider:stream")

	got := buffer.DrainAccountingEvidence()
	if len(got) != 1 {
		t.Fatalf("bridged evidence count=%d, want 1", len(got))
	}
	if got[0].InputTokens == nil || *got[0].InputTokens != in || got[0].OutputTokens == nil || *got[0].OutputTokens != out || got[0].CacheReadTokens == nil || *got[0].CacheReadTokens != cache {
		t.Fatalf("bridged counters=%+v", got[0])
	}
	if got[0].Presence != (lipapi.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true}) {
		t.Fatalf("presence=%+v", got[0].Presence)
	}
	if got[0].DedupeKey != "provider:req-1" || got[0].Source != backendplugin.AccountingSourceProviderReported {
		t.Fatalf("provider lineage=%+v", got[0])
	}
}

func TestUsageEvidenceBuffer_DeduplicatesExactReplayAndRejectsUnsafeUsage(t *testing.T) {
	t.Parallel()
	value := 4
	base := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: value,
		UsagePresence: lipapi.UsagePresence{InputTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-2",
		},
	}
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(base, "fallback")
	buffer.AddUsageEvent(base, "fallback")
	if got := buffer.DrainAccountingEvidence(); len(got) != 1 {
		t.Fatalf("exact replay count=%d, want 1", len(got))
	}

	unsafe := base
	unsafe.Accounting.Plane = lipapi.UsagePlaneClientVisible
	buffer.AddUsageEvent(unsafe, "fallback")
	if got := buffer.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("client-visible usage was bridged: %+v", got)
	}

	missingPresence := base
	missingPresence.UsagePresence = lipapi.UsagePresence{}
	buffer.AddUsageEvent(missingPresence, "fallback-2")
	if got := buffer.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("usage without explicit presence was bridged: %+v", got)
	}
}

func TestUsageEvidenceBuffer_PreservesChangedSourceAsSeparateV1Evidence(t *testing.T) {
	t.Parallel()
	first, second := 1, 2
	base := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: first,
		UsagePresence: lipapi.UsagePresence{InputTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:req-3",
		},
	}
	changed := base
	changed.InputTokens = second
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(base, "fallback")
	buffer.AddUsageEvent(changed, "fallback")
	if got := buffer.DrainAccountingEvidence(); len(got) != 2 {
		t.Fatalf("changed source count=%d, want 2 for host-side V1 reconciliation", len(got))
	}
}

func TestUsageEvidenceBufferRetainsABACorrectionAcrossDrains(t *testing.T) {
	t.Parallel()
	base := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		UsagePresence: lipapi.UsagePresence{InputTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "provider:aba",
		},
	}
	buffer := backendplugin.NewUsageEvidenceBuffer()
	for _, value := range []int{1, 2, 1} {
		event := base
		event.InputTokens = value
		buffer.AddUsageEvent(event, "fallback")
		got := buffer.DrainAccountingEvidence()
		if len(got) != 1 {
			t.Fatalf("input=%d evidence=%d, want one correction", value, len(got))
		}
	}
	last := base
	last.InputTokens = 1
	buffer.AddUsageEvent(last, "fallback")
	if got := buffer.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("consecutive A replay evidence=%d, want 0", len(got))
	}
}
