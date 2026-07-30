package diag

// CompatibleBackendRow is a bounded operator projection for one configured
// built-in compatible backend instance. Values never include credential secrets
// or raw opaque config payloads.
type CompatibleBackendRow struct {
	Origin            string `json:"origin"`
	InstanceID        string `json:"instance_id"`
	FactoryKind       string `json:"factory_kind"`
	Enabled           bool   `json:"enabled"`
	Prefix            string `json:"prefix,omitempty"`
	EndpointIdentity  string `json:"endpoint_identity,omitempty"`
	AuthConfigured    bool   `json:"auth_configured"`
	TokenizerID       string `json:"tokenizer_id,omitempty"`
	ConcurrencyPolicy string `json:"concurrency_policy,omitempty"`
	// InventoryState is config intent for structural/offline projection only.
	InventoryState  string                     `json:"inventory_state,omitempty"`
	InventoryHealth *CompatibleInventoryHealth `json:"inventory_health,omitempty"`
	ConfigError     string                     `json:"config_error,omitempty"`
}
