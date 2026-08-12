package adapter

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestAccountingEvidenceToEventPreservesPresenceAndAuthoritativeZero(t *testing.T) {
	t.Parallel()
	input, output, cacheRead, cacheWrite, reasoning, total := int64(4), int64(0), int64(2), int64(0), int64(1), int64(7)
	ev, err := accountingEvidenceToEvent(&backendplugin.AccountingEvidence{
		InputTokens:      &input,
		OutputTokens:     &output,
		CacheReadTokens:  &cacheRead,
		CacheWriteTokens: &cacheWrite,
		ReasoningTokens:  &reasoning,
		TotalTokens:      &total,
		Presence: backendplugin.UsagePresence{
			InputTokens: true, OutputTokens: true, CacheReadTokens: true,
			CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
		},
		Source:    backendplugin.AccountingSourceProviderReported,
		Authority: backendplugin.AccountingAuthorityAuthoritative,
		Plane:     backendplugin.AccountingPlaneProviderBillable,
		DedupeKey: "provider-charge-1",
	})
	if err != nil {
		t.Fatalf("accountingEvidenceToEvent: %v", err)
	}
	if ev.Kind != lipapi.EventUsageDelta || ev.Accounting.Plane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("event classification = %+v", ev)
	}
	if ev.Accounting.DedupeKey != "provider-charge-1" || ev.Accounting.Source != lipapi.UsageSourceProviderReported || ev.Accounting.Authority != lipapi.UsageAuthorityAuthoritative {
		t.Fatalf("event provenance = %+v", ev.Accounting)
	}
	if ev.InputTokens != 4 || ev.OutputTokens != 0 || ev.CacheReadTokens != 2 || ev.CacheWriteTokens != 0 || ev.ReasoningTokens != 1 || ev.TotalTokens != 7 {
		t.Fatalf("event quantities = %+v", ev)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens || !ev.UsagePresence.CacheReadTokens || !ev.UsagePresence.CacheWriteTokens || !ev.UsagePresence.ReasoningTokens || !ev.UsagePresence.TotalTokens {
		t.Fatalf("event presence = %+v", ev.UsagePresence)
	}
}

func TestAccountingEvidenceToEventRejectsAbsentPresenceMismatch(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	_, err := accountingEvidenceToEvent(&backendplugin.AccountingEvidence{
		OutputTokens: &zero,
		Presence:     backendplugin.UsagePresence{},
		Source:       backendplugin.AccountingSourceProviderReported,
		Authority:    backendplugin.AccountingAuthorityAuthoritative,
		Plane:        backendplugin.AccountingPlaneProviderBillable,
		DedupeKey:    "provider-charge-2",
	})
	if err == nil {
		t.Fatal("expected presence mismatch to be rejected")
	}
}

func TestFinalizeBillingResponseToEventPreservesPresenceAndZeroSemantics(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	input := int64(4)
	ev, err := finalizeBillingResponseToEvent(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens:  &input,
			OutputTokens: &zero,
			Presence:     backendplugin.UsagePresence{InputTokens: true, OutputTokens: true},
		},
		EvidenceQuality: "provider",
	}, "finalize-key")
	if err != nil {
		t.Fatalf("finalizeBillingResponseToEvent: %v", err)
	}
	if ev.InputTokens != 4 || ev.OutputTokens != 0 {
		t.Fatalf("quantities = %+v", ev)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens {
		t.Fatalf("presence = %+v", ev.UsagePresence)
	}
	if ev.Accounting.Source != lipapi.UsageSourceProviderReported || ev.Accounting.Authority != lipapi.UsageAuthorityAuthoritative {
		t.Fatalf("provenance = %+v", ev.Accounting)
	}
	if ev.Accounting.DedupeKey != "finalize-key" {
		t.Fatalf("dedupe = %q", ev.Accounting.DedupeKey)
	}
}

func TestFinalizeBillingResponseToEventRejectsPresentNilPointer(t *testing.T) {
	t.Parallel()
	_, err := finalizeBillingResponseToEvent(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			Presence: backendplugin.UsagePresence{InputTokens: true},
		},
		EvidenceQuality: "provider",
	}, "finalize-key")
	if err == nil {
		t.Fatal("expected Present=true with nil pointer to be rejected")
	}
}

func TestFinalizeBillingResponseToEventDoesNotInventAuthoritativeWhenQualityUnknown(t *testing.T) {
	t.Parallel()
	input := int64(1)
	ev, err := finalizeBillingResponseToEvent(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens: &input,
			Presence:    backendplugin.UsagePresence{InputTokens: true},
		},
	}, "finalize-key")
	if err != nil {
		t.Fatalf("finalizeBillingResponseToEvent: %v", err)
	}
	if ev.Accounting.Source != lipapi.UsageSourceUnknown || ev.Accounting.Authority != lipapi.UsageAuthorityUnknown {
		t.Fatalf("unknown quality must not invent authoritative provenance: %+v", ev.Accounting)
	}
}

func TestFinalizeBillingResponseToEventPreservesAbsentCounters(t *testing.T) {
	t.Parallel()
	input := int64(2)
	ev, err := finalizeBillingResponseToEvent(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens: &input,
			Presence:    backendplugin.UsagePresence{InputTokens: true},
		},
		EvidenceQuality: "estimated",
	}, "finalize-key")
	if err != nil {
		t.Fatalf("finalizeBillingResponseToEvent: %v", err)
	}
	if ev.UsagePresence.OutputTokens || ev.OutputTokens != 0 {
		t.Fatalf("absent output must remain absent, got %+v", ev)
	}
	if ev.Accounting.Authority != lipapi.UsageAuthorityEstimated {
		t.Fatalf("estimated quality authority = %q", ev.Accounting.Authority)
	}
}
