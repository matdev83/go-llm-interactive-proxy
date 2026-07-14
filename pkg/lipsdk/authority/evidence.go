package authority

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// SafeEvidence is denial/allow evidence without secrets, raw payloads, or
// credentials (requirements 12.8, 14.x). Prefer stable categories and identifiers.
type SafeEvidence struct {
	Category   string            `json:"category,omitempty"`
	Code       string            `json:"code,omitempty"`
	Message    string            `json:"message,omitempty"`
	RuleID     string            `json:"rule_id,omitempty"`
	ProviderID string            `json:"provider_id,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
}

// EvidenceSink projects authority outcomes into policy-decision and control-plane
// evidence without exposing storage or HTTP concerns (requirement 12.1).
// Closed enterprise modules inject implementations through lipruntime.Options.
type EvidenceSink interface {
	RecordPolicyDecision(ctx context.Context, record policydecision.Record) error
	RecordAccountingAuthority(ctx context.Context, event controlplane.Event) error
}
