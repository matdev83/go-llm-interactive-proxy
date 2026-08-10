package diag

// CompatibleBackendRow is a bounded operator projection for one configured
// built-in compatible backend instance. Values never include credential secrets
// or raw opaque config payloads.
type CompatibleBackendRow struct {
	Origin      string `json:"origin"`
	InstanceID  string `json:"instance_id"`
	FactoryKind string `json:"factory_kind"`
	Enabled     bool   `json:"enabled"`
	Prefix      string `json:"prefix,omitempty"`
	// Profile is the configured protocol profile for OpenResponses-compatible
	// backends (e.g. "2026-04-24"). Empty for protocol-agnostic compatible rows.
	Profile           string `json:"profile,omitempty"`
	EndpointIdentity  string `json:"endpoint_identity,omitempty"`
	AuthConfigured    bool   `json:"auth_configured"`
	TokenizerID       string `json:"tokenizer_id,omitempty"`
	ConcurrencyPolicy string `json:"concurrency_policy,omitempty"`
	// Capabilities is the sanitized set of declared semantic capabilities for
	// OpenResponses-compatible backends. Empty for protocol-agnostic rows.
	Capabilities    []string                   `json:"capabilities,omitempty"`
	Conformance     string                     `json:"conformance,omitempty"`
	InventoryState  string                     `json:"inventory_state,omitempty"`
	InventoryHealth *CompatibleInventoryHealth `json:"inventory_health,omitempty"`
	ConfigError     string                     `json:"config_error,omitempty"`
}
