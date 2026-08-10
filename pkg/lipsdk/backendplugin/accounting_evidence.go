package backendplugin

import (
	"strings"
)

// ValidateAccountingEvidence validates the bounded, provider-billable sideband
// payload. Presence is authoritative: a nil counter is omitted, not zero.
func ValidateAccountingEvidence(e AccountingEvidence) error {
	if e.Plane != AccountingPlaneProviderBillable || e.Source == AccountingSourceUnknown || e.Authority == AccountingAuthorityUnknown {
		return ErrInvalidFrame
	}
	if strings.TrimSpace(e.DedupeKey) == "" || len(e.DedupeKey) > int(DefaultMaxAccountingDedupeKeyBytes) {
		return ErrInvalidFrame
	}
	if !e.Presence.Any() {
		return ErrInvalidFrame
	}
	for _, value := range []*int64{e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.ReasoningTokens, e.TotalTokens} {
		if value != nil && *value < 0 {
			return ErrInvalidFrame
		}
	}
	if e.Presence.InputTokens != (e.InputTokens != nil) || e.Presence.OutputTokens != (e.OutputTokens != nil) ||
		e.Presence.CacheReadTokens != (e.CacheReadTokens != nil) || e.Presence.CacheWriteTokens != (e.CacheWriteTokens != nil) ||
		e.Presence.ReasoningTokens != (e.ReasoningTokens != nil) || e.Presence.TotalTokens != (e.TotalTokens != nil) {
		return ErrInvalidFrame
	}
	return nil
}
