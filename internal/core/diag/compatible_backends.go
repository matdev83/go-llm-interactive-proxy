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

// OpenResponsesFrontendRow is a bounded operator projection for one configured
// OpenResponses client-facing frontend instance. Values never include secrets
// or raw opaque config payloads.
type OpenResponsesFrontendRow struct {
	Origin      string `json:"origin"`
	InstanceID  string `json:"instance_id"`
	FactoryKind string `json:"factory_kind"`
	Enabled     bool   `json:"enabled"`
	Profile     string `json:"profile,omitempty"`
	// BasePath is the configured client-facing route prefix.
	BasePath string `json:"base_path,omitempty"`
	// WebSocketEnabled reports whether the WebSocket transport is enabled.
	WebSocketEnabled bool `json:"websocket_enabled"`
	// ContinuationStore is the configured persistence mode (e.g. "standard").
	ContinuationStore string `json:"continuation_store,omitempty"`
	ContinuationTTL   string `json:"continuation_ttl,omitempty"`
	// AllowedOrigins is the sanitized WebSocket origin allowlist.
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	// Capabilities is the sanitized client-facing semantic capability surface
	// of the pinned profile (ordered items, streaming, tools, compaction, and
	// WebSocket when the transport is enabled).
	Capabilities []string `json:"capabilities,omitempty"`
	// RouteClaims is the sanitized normalized method+path ownership snapshot.
	RouteClaims []string `json:"route_claims,omitempty"`
	// Conformance is the profile conformance status projection.
	Conformance string `json:"conformance,omitempty"`
	ConfigError string `json:"config_error,omitempty"`
}
