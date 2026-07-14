package authority

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
