package config

// AccessConfig selects deployment access posture (single-user vs multi-user).
// Empty Mode is normalized to single_user during validation/load.
type AccessConfig struct {
	Mode string `yaml:"mode"`
}

// AuthLocalAPIKeyRecord is one operator-configured API key (secret material belongs
// in config files only; validation and redaction are handled elsewhere).
// Key must be at least 16 Unicode code points after trimming (enforced with core auth validation).
type AuthLocalAPIKeyRecord struct {
	KeyID       string `yaml:"key_id"`
	PrincipalID string `yaml:"principal_id"`
	Key         string `yaml:"key"`
	// Attribution carries optional operator-controlled safe attribution for this key.
	// Missing optional fields remain unknown (no inference). Raw secrets and transport
	// headers must never be placed here.
	Attribution AuthLocalAttribution `yaml:"attribution"`
}

// AuthLocalAttribution mirrors [coreauth.LocalAttribution] for YAML decoding. Zero values
// mean "not configured".
type AuthLocalAttribution struct {
	DisplayName    string            `yaml:"display_name"`
	AuthMethod     string            `yaml:"auth_method"`
	TenantID       string            `yaml:"tenant_id"`
	OrganizationID string            `yaml:"organization_id"`
	WorkspaceID    string            `yaml:"workspace_id"`
	ProjectID      string            `yaml:"project_id"`
	DepartmentID   string            `yaml:"department_id"`
	CostCenterID   string            `yaml:"cost_center_id"`
	Roles          []string          `yaml:"roles"`
	SafeClaims     map[string]string `yaml:"safe_claims"`
	PolicyLabels   map[string]string `yaml:"policy_labels"`
}

// AuthRemoteConfig holds opaque placeholders for future remote auth wiring.
// No network clients are constructed from these fields in the OSS core.
type AuthRemoteConfig struct {
	Endpoint string `yaml:"endpoint"`
	Handler  string `yaml:"handler"`
}

// AuthConfig selects authentication handler, required level, event delivery policy,
// local key material, and remote delegation placeholders.
type AuthConfig struct {
	Handler            string `yaml:"handler"`
	RequiredLevel      string `yaml:"required_level"`
	EventFailurePolicy string `yaml:"event_failure_policy"`
	// EventDelivery selects how auth/session events are delivered: default (structured log sink),
	// disabled (no sink; explicit no delivery), or custom (requires BuildOptions.AuthEventSink at wiring).
	// Empty behaves like default.
	EventDelivery string                  `yaml:"event_delivery"`
	LocalAPIKeys  []AuthLocalAPIKeyRecord `yaml:"local_api_keys"`
	Remote        AuthRemoteConfig        `yaml:"remote"`
}
