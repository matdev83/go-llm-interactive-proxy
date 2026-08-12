package adapter

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// finalizeBillingResponseToEvent maps terminal provider evidence to the
// compatibility usage event. It is a read-only adapter boundary: absent values
// remain absent, explicit zero remains present zero, and unknown evidence never
// receives invented authoritative provenance.
func finalizeBillingResponseToEvent(response backendplugin.FinalizeBillingResponse, dedupeKey string) (lipapi.Event, error) {
	if err := validateUsageEvidencePresence(response.Usage); err != nil {
		return lipapi.Event{}, err
	}
	source, authority := evidenceQuality(strings.TrimSpace(response.EvidenceQuality))
	ev := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		UsagePresence: response.Usage.Presence,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    source,
			Authority: authority,
			DedupeKey: strings.TrimSpace(dedupeKey),
		},
	}
	if response.Usage.InputTokens != nil {
		ev.InputTokens = int(*response.Usage.InputTokens)
	}
	if response.Usage.OutputTokens != nil {
		ev.OutputTokens = int(*response.Usage.OutputTokens)
	}
	if response.Usage.CacheReadTokens != nil {
		ev.CacheReadTokens = int(*response.Usage.CacheReadTokens)
	}
	if response.Usage.CacheWriteTokens != nil {
		ev.CacheWriteTokens = int(*response.Usage.CacheWriteTokens)
	}
	if response.Usage.ReasoningTokens != nil {
		ev.ReasoningTokens = int(*response.Usage.ReasoningTokens)
	}
	if response.Usage.TotalTokens != nil {
		ev.TotalTokens = int(*response.Usage.TotalTokens)
	}
	return ev, nil
}

func validateUsageEvidencePresence(e backendplugin.UsageEvidence) error {
	fields := []struct {
		name    string
		value   *int64
		present bool
	}{
		{"input_tokens", e.InputTokens, e.Presence.InputTokens},
		{"output_tokens", e.OutputTokens, e.Presence.OutputTokens},
		{"cache_read_tokens", e.CacheReadTokens, e.Presence.CacheReadTokens},
		{"cache_write_tokens", e.CacheWriteTokens, e.Presence.CacheWriteTokens},
		{"reasoning_tokens", e.ReasoningTokens, e.Presence.ReasoningTokens},
		{"total_tokens", e.TotalTokens, e.Presence.TotalTokens},
	}
	for _, field := range fields {
		if field.present != (field.value != nil) {
			return fmt.Errorf("backend adapter: usage presence mismatch for %s", field.name)
		}
		if field.value != nil && *field.value < 0 {
			return fmt.Errorf("backend adapter: negative finalized quantity for %s", field.name)
		}
	}
	return nil
}

func evidenceQuality(quality string) (lipapi.UsageSource, lipapi.UsageAuthority) {
	switch strings.ToLower(quality) {
	case "provider", "authoritative", "provider_reported":
		return lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative
	case "count_api", "provider_count_api":
		return lipapi.UsageSourceProviderCountAPI, lipapi.UsageAuthorityAuthoritative
	case "estimated", "estimator", "local_estimator":
		return lipapi.UsageSourceLocalEstimator, lipapi.UsageAuthorityEstimated
	case "tokenizer", "local_tokenizer":
		return lipapi.UsageSourceLocalTokenizer, lipapi.UsageAuthorityEstimated
	default:
		return lipapi.UsageSourceUnknown, lipapi.UsageAuthorityUnknown
	}
}
