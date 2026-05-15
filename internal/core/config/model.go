package config

import (
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config contains only core-owned runtime settings and opaque plugin config payloads.
//
// A decoded Config is not self-validating: [Validate] checks core fields (plugins, continuity, logging, etc.) but does
// not validate model_aliases. After [LoadFile], call routing.ValidateModelAliasesConfig(cfg) from package
// internal/core/routing before wiring; composition (for example internal/infra/runtimebundle.Build) compiles
// model_aliases via routing.NewAliasResolver. Default route selector resolution is [EffectiveDefaultRouteSelector]
// in this package (see effective_default_route.go).
type Config struct {
	Server         ServerConfig         `yaml:"server"`
	Access         AccessConfig         `yaml:"access"`
	Auth           AuthConfig           `yaml:"auth"`
	Logging        LoggingConfig        `yaml:"logging"`
	Diagnostics    DiagnosticsConfig    `yaml:"diagnostics"`
	Observability  ObservabilityConfig  `yaml:"observability"`
	HTTPClient     HTTPClientConfig     `yaml:"http_client"`
	Database       DatabaseConfig       `yaml:"database"`
	Routing        RoutingConfig        `yaml:"routing"`
	Continuity     ContinuityConfig     `yaml:"continuity"`
	SecureSession  SecureSessionConfig  `yaml:"secure_session"`
	StreamRecovery StreamRecoveryConfig `yaml:"stream_recovery"`
	Hooks          HooksConfig          `yaml:"hooks"`
	Accounting     AccountingConfig     `yaml:"accounting"`
	Plugins        PluginsConfig        `yaml:"plugins"`
	ModelAliases   []ModelAliasConfig   `yaml:"model_aliases"`
	ModelCatalog   ModelCatalogConfig   `yaml:"model_catalog"`
}

type AccountingConfig struct {
	Enabled bool   `yaml:"enabled"`
	Mode    string `yaml:"mode"`
	// CountTimeout bounds provider/local count calls; empty selects the composition-root default.
	CountTimeout  string                        `yaml:"count_timeout"`
	Tokenizer     AccountingTokenizerConfig     `yaml:"tokenizer"`
	Preflight     AccountingPreflightConfig     `yaml:"preflight"`
	Ledger        AccountingLedgerConfig        `yaml:"ledger"`
	Admin         AccountingAdminConfig         `yaml:"admin"`
	Observability AccountingObservabilityConfig `yaml:"observability"`
	// StrictAuthoritative rejects backend wiring unless every configured backend can provide authoritative usage.
	StrictAuthoritative bool                    `yaml:"strict_authoritative"`
	Pricing             AccountingPricingConfig `yaml:"pricing"`
}

type AccountingTokenizerConfig struct {
	DefaultEncoding string            `yaml:"default_encoding"`
	ModelMappings   map[string]string `yaml:"model_mappings"`
}

type AccountingPreflightConfig struct {
	Mode                 string `yaml:"mode"`
	MaxInputTokens       int64  `yaml:"max_input_tokens"`
	MaxOutputTokens      int64  `yaml:"max_output_tokens"`
	MaxContextTokens     int64  `yaml:"max_context_tokens"`
	ClampMaxOutputTokens bool   `yaml:"clamp_max_output_tokens"`
}

type AccountingLedgerConfig struct {
	Store       string `yaml:"store"`
	SQLitePath  string `yaml:"sqlite_path"`
	PostgresDSN string `yaml:"postgres_dsn"`
	WritePolicy string `yaml:"write_policy"`
}

type AccountingAdminConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Path         string `yaml:"path"`
	MaxBodyBytes int64  `yaml:"max_body_bytes"`
}

type AccountingObservabilityConfig struct {
	Enabled bool `yaml:"enabled"`
}

type AccountingPricingConfig struct {
	Currency       string                       `yaml:"currency"`
	CatalogVersion string                       `yaml:"catalog_version"`
	Models         []AccountingModelPriceConfig `yaml:"models"`
}

type AccountingModelPriceConfig struct {
	Backend              string `yaml:"backend"`
	Model                string `yaml:"model"`
	InputPer1M           string `yaml:"input_per_1m"`
	CachedInputPer1M     string `yaml:"cached_input_per_1m"`
	CacheWriteInputPer1M string `yaml:"cache_write_input_per_1m"`
	OutputPer1M          string `yaml:"output_per_1m"`
	ReasoningOutputPer1M string `yaml:"reasoning_output_per_1m"`
}

// DatabaseConfig is optional connection pool tuning for managed PostgreSQL handles
// opened by the proxy (see internal/infra/db). Omitted or zero values preserve driver defaults.
type DatabaseConfig struct {
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime string `yaml:"conn_max_idle_time"`
}

// ModelAliasConfig is one regexp-based rewrite of an incoming route selector (see internal/core/routing/aliases.go).
type ModelAliasConfig struct {
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
}

// ObservabilityConfig toggles Prometheus metrics and OpenTelemetry tracing.
type ObservabilityConfig struct {
	Metrics MetricsConfig `yaml:"metrics"`
	Tracing TracingConfig `yaml:"tracing"`
}

// MetricsConfig controls the Prometheus /metrics endpoint.
type MetricsConfig struct {
	// Enabled exposes lip_http_* metrics and process/go collectors when true.
	// When false (zero value), no /metrics handler is registered (legacy behavior).
	Enabled bool `yaml:"enabled"`
	// Path is the HTTP path for Prometheus scraping (e.g. "/metrics"). Empty defaults to /metrics in LoadFile.
	Path string `yaml:"path"`
	// ExemplarsEnabled attaches trace_id exemplars to selected histograms and enables OpenMetrics on /metrics.
	ExemplarsEnabled bool `yaml:"exemplars_enabled"`
}

// TracingConfig enables OpenTelemetry traces (incoming otelhttp + OTLP export via standard OTEL_* env vars).
type TracingConfig struct {
	// Enabled turns on SDK wiring, W3C propagation, and outbound HTTP tracing on the shared upstream client.
	Enabled bool `yaml:"enabled"`
	// ServiceName sets otel resource service.name when non-empty; otherwise OTEL_SERVICE_NAME or "lipstd".
	ServiceName string `yaml:"service_name"`
	// SampleRatio when set and strictly between 0 and 1 applies ParentBased(TraceIDRatioBased) for root spans.
	// When nil or 1, the SDK default sampler applies (typically full sampling for new roots).
	SampleRatio *float64 `yaml:"sample_ratio"`
}

// HTTPClientConfig tunes the shared outbound HTTP client used for upstream LLM calls.
type HTTPClientConfig struct {
	// TrustEnvironmentProxy when true (default) uses http.ProxyFromEnvironment for outbound requests.
	// When false, the transport ignores HTTP_PROXY/HTTPS_PROXY/NO_PROXY (reduces deputy risk if env is untrusted).
	// Omitted or null in YAML defaults to true in [LoadFile] / [EffectiveTrustEnvironmentProxy].
	TrustEnvironmentProxy *bool `yaml:"trust_environment_proxy"`
	// MaxIdleConns is the Transport MaxIdleConns pool cap. Omit to use the bundled default (~100).
	MaxIdleConns *int `yaml:"max_idle_conns,omitempty"`
	// MaxIdleConnsPerHost defaults to 64 when omitted (Go's default of 2 is usually too low for LLM APIs).
	MaxIdleConnsPerHost *int `yaml:"max_idle_conns_per_host,omitempty"`
	// IdleConnTimeout is a Go duration string (e.g. "90s"). Empty uses the httpclient default.
	IdleConnTimeout string `yaml:"idle_conn_timeout"`
	// ResponseHeaderTimeout bounds waiting for response headers (e.g. "60s"). Empty uses default.
	ResponseHeaderTimeout string `yaml:"response_header_timeout"`
	// DialTimeout is the net.Dialer Timeout for establishing connections (e.g. "30s").
	DialTimeout string `yaml:"dial_timeout"`
	// KeepAlive is the net.Dialer KeepAlive interval (e.g. "30s").
	KeepAlive string `yaml:"keep_alive"`
	// TLSHandshakeTimeout caps TLS handshakes (e.g. "10s").
	TLSHandshakeTimeout string `yaml:"tls_handshake_timeout"`
	// ExpectContinueTimeout is the Transport expect-continue timeout (e.g. "1s").
	ExpectContinueTimeout string `yaml:"expect_continue_timeout"`
	// ClientTimeout is [http.Client.Timeout] for the full request including body (e.g. "120s").
	ClientTimeout string `yaml:"client_timeout"`
}

// EffectiveTrustEnvironmentProxy returns whether outbound calls should honor process proxy environment variables.
func (c *Config) EffectiveTrustEnvironmentProxy() bool {
	if c == nil || c.HTTPClient.TrustEnvironmentProxy == nil {
		return true
	}
	return *c.HTTPClient.TrustEnvironmentProxy
}

// LoggingConfig controls process-wide slog output and optional HTTP access logs.
type LoggingConfig struct {
	// Level is one of: debug, info, warn, error (case-insensitive). Empty defaults to info in LoadFile/Validate.
	Level string `yaml:"level"`
	// Format is json or text (case-insensitive). Empty defaults to json in LoadFile/Validate.
	Format string `yaml:"format"`
	// AddSource adds source file/line to each record when true.
	AddSource bool `yaml:"add_source"`
	// AccessLog emits one structured line per HTTP request when true.
	AccessLog bool `yaml:"access_log"`
	// AccessLogSkipPaths are URL path prefixes (must start with /) for which access logs are suppressed.
	AccessLogSkipPaths []string `yaml:"access_log_skip_paths"`
	// AccessLogIncludeRawPath when true adds the full URL path to access logs (higher cardinality).
	// Default false: only route_group.
	AccessLogIncludeRawPath bool `yaml:"access_log_include_raw_path"`
}

// HooksConfig carries core hook-bus tuning (not plugin opaque payloads).
type HooksConfig struct {
	// ToolReactorErrorPolicy is one of: fail_open (default), fail_closed, swallow_event.
	ToolReactorErrorPolicy string `yaml:"tool_reactor_error_policy"`
}

type AuthMode string

const (
	// AuthModeNoAuth permits unauthenticated local single-user traffic only on explicit loopback binds.
	AuthModeNoAuth AuthMode = "no_auth"
	// AuthModeExternal requires an injected or configured auth layer and may bind non-loopback interfaces.
	AuthModeExternal AuthMode = "external"
)

type ServerConfig struct {
	Address  string   `yaml:"address"`
	AuthMode AuthMode `yaml:"auth_mode"`
	// MaxRequestBodyBytes caps HTTP request bodies for bundled frontends. Zero selects
	// each handler's default limit (see internal/plugins/frontends/reqbody).
	MaxRequestBodyBytes int64 `yaml:"max_request_body_bytes"`
	// ReadHeaderTimeout is a Go duration string (e.g. "10s") for [http.Server.ReadHeaderTimeout].
	// Empty defaults to 10s (historical stdhttp behavior).
	ReadHeaderTimeout string `yaml:"read_header_timeout"`
	// ReadTimeout is [http.Server.ReadTimeout] (full request body read + per-connection read deadlines).
	// Empty defaults to 30s.
	ReadTimeout string `yaml:"read_timeout"`
	// WriteTimeout is [http.Server.WriteTimeout]. Empty defaults to 120s.
	WriteTimeout string `yaml:"write_timeout"`
	// IdleTimeout is [http.Server.IdleTimeout]. Empty defaults to 120s.
	IdleTimeout string `yaml:"idle_timeout"`
	// MaxPendingWireEvents caps backend adapter-internal pending-event queues per stream (0 = unlimited).
	MaxPendingWireEvents int `yaml:"max_pending_wire_events"`
}

const (
	defaultServerReadHeaderTimeout = 10 * time.Second
	defaultServerReadTimeout       = 30 * time.Second
	defaultServerWriteTimeout      = 120 * time.Second
	defaultServerIdleTimeout       = 120 * time.Second
)

func parseServerDurationOrDefault(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// EffectiveReadHeaderTimeout returns ReadHeaderTimeout or the default (10s).
func (s ServerConfig) EffectiveReadHeaderTimeout() time.Duration {
	return parseServerDurationOrDefault(s.ReadHeaderTimeout, defaultServerReadHeaderTimeout)
}

// EffectiveReadTimeout returns ReadTimeout or the default (30s).
func (s ServerConfig) EffectiveReadTimeout() time.Duration {
	return parseServerDurationOrDefault(s.ReadTimeout, defaultServerReadTimeout)
}

// EffectiveWriteTimeout returns WriteTimeout or the default (120s).
func (s ServerConfig) EffectiveWriteTimeout() time.Duration {
	return parseServerDurationOrDefault(s.WriteTimeout, defaultServerWriteTimeout)
}

// EffectiveIdleTimeout returns IdleTimeout or the default (120s).
func (s ServerConfig) EffectiveIdleTimeout() time.Duration {
	return parseServerDurationOrDefault(s.IdleTimeout, defaultServerIdleTimeout)
}

// EffectiveMaxRequestBodyBytes returns MaxRequestBodyBytes when positive, otherwise zero
// (callers treat zero as "use handler default").
func (s ServerConfig) EffectiveMaxRequestBodyBytes() int64 {
	if s.MaxRequestBodyBytes > 0 {
		return s.MaxRequestBodyBytes
	}
	return 0
}

type DiagnosticsConfig struct {
	Enabled      bool   `yaml:"enabled"`
	HealthPath   string `yaml:"health_path"`
	AttemptsPath string `yaml:"attempts_path"`
	// InventoryPath registers a JSON plugin inventory endpoint when non-empty (e.g. "/debug/inventory").
	InventoryPath string `yaml:"inventory_path"`
	// RouteTracePath registers a JSON ring buffer of recent routing decisions when non-empty.
	RouteTracePath string `yaml:"route_trace_path"`
	// PprofPath registers net/http/pprof handlers under this prefix when diagnostics.enabled is true
	// (e.g. "/debug/pprof"). Leave empty to disable. Do not expose publicly without access controls.
	PprofPath string `yaml:"pprof_path"`
	// SharedSecret when non-empty requires header X-LIP-Diagnostics-Secret on attempts, inventory,
	// route trace, and pprof routes (not on health). Use a long random value in production.
	SharedSecret string `yaml:"shared_secret"`
}

type RoutingConfig struct {
	MaxAttempts int `yaml:"max_attempts"`
	// DefaultRoute is the selector used when the client omits X-LIP-Route (e.g. "openai-responses:gpt-4o-mini").
	DefaultRoute string                `yaml:"default_route"`
	Health       RoutingHealthConfig   `yaml:"health"`
	Affinity     RoutingAffinityConfig `yaml:"affinity"`
}

type RoutingAffinityConfig struct {
	Store           string `yaml:"store"`
	MissingIdentity string `yaml:"missing_identity"`
}

type RoutingHealthConfig struct {
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

type CircuitBreakerConfig struct {
	Enabled          bool   `yaml:"enabled"`
	FailureThreshold int    `yaml:"failure_threshold"`
	OpenFor          string `yaml:"open_for"`
}

// SecureSessionConfig controls the core-owned secure session layer (resume proofs, durable evidence, diagnostics).
// When Enabled is omitted (nil), secure sessions default to on with store memory unless overridden.
// Explicit enabled: false is rejected by validation (legacy continuity-only executor path was removed).
type SecureSessionConfig struct {
	// Enabled turns on secure-session validation and runtime wiring (store, tokens, audit gates).
	// Omitted in YAML defaults to enabled; use a pointer so explicit false can be rejected at validation time.
	Enabled *bool `yaml:"enabled"`
	// Store is "memory" (non-durable), "sqlite" (local durable), or "postgres" (managed durable).
	// Empty is normalized to "memory" in [LoadFile] when Enabled.
	Store string `yaml:"store"`
	// SQLitePath is the database file path when store is "sqlite".
	SQLitePath string `yaml:"sqlite_path"`
	// PostgresDSN is the connection string when store is "postgres".
	PostgresDSN string `yaml:"postgres_dsn"`
	// ResumeWindow is a Go duration string for inactivity-based resume limits; empty means no fixed
	// window (policy default).
	ResumeWindow string `yaml:"resume_window"`
	// TokenFingerprintKey is deployment secret material used to HMAC resume-token fingerprints; required for sqlite store.
	TokenFingerprintKey string `yaml:"token_fingerprint_key"`
	// AuditDurability is "best_effort" or "durable"; durable requires a durable store (sqlite or postgres)
	// and a long token fingerprint key.
	AuditDurability string `yaml:"audit_durability"`
	// RedactionDefault is "standard" or "strict" for operator-visible session payloads (diagnostics);
	// invalid values rejected when enabled.
	RedactionDefault string `yaml:"redaction_default"`
	// DiagnosticsExposeSummaries registers operator session summary routes when true (requires DiagnosticsPathPrefix
	// and a non-empty diagnostics.shared_secret, same minimum length as other protected diagnostics routes).
	DiagnosticsExposeSummaries bool `yaml:"diagnostics_expose_summaries"`
	// DiagnosticsPathPrefix is the URL prefix for secure-session diagnostics (e.g. "/debug/sessions");
	// must start with "/".
	DiagnosticsPathPrefix string `yaml:"diagnostics_path_prefix"`
	// NonDurableWarning is "silent", "log", or "strict" when store is non-durable (memory): strict fails
	// validation when audit requires durability.
	NonDurableWarning string `yaml:"non_durable_warning"`
	// RequireWorkspaceID when true rejects secure-session turns when no workspace id was resolved
	// (maps to [WorkspaceMatchRequired] on BeginTurn; Req 11.1 / 11.6).
	RequireWorkspaceID bool `yaml:"require_workspace_id"`
	// WorkspaceResolveOnError is "fail_open" (default) or "fail_closed". When fail_closed, workspace
	// resolver errors reject the request instead of continuing with an empty workspace (Req 11.6).
	WorkspaceResolveOnError string `yaml:"workspace_resolve_on_error"`
	// ResumeTokenBindPrincipalOnly when true fingerprints resume tokens using only the authenticated
	// principal id (not agent digest or first-message digest), so benign client metadata drift
	// between turns does not invalidate bearer resumes.
	ResumeTokenBindPrincipalOnly bool `yaml:"resume_token_bind_principal_only"`
	// SQLQueryCacheTTL is a Go duration string enabling process-local TTL caching of session existence
	// and transcript_enabled reads in durable SQL secure-session stores. Empty disables caching.
	SQLQueryCacheTTL string `yaml:"sql_query_cache_ttl"`
	// SQLQueryCacheMaxEntries caps entries per logical cache when SQLQueryCacheTTL is set; zero uses a store default.
	SQLQueryCacheMaxEntries int `yaml:"sql_query_cache_max_entries"`
}

type ContinuityConfig struct {
	InMemory bool `yaml:"in_memory"`
	// Store names the continuity backing when InMemory is true. Empty is normalized to "memory" in LoadFile.
	// Use "sqlite" for local durable storage (requires sqlite_path) or "postgres" for managed durable
	// (requires postgres_dsn).
	Store string `yaml:"store"`
	// SQLitePath is the database file path when store is "sqlite".
	SQLitePath string `yaml:"sqlite_path"`
	// PostgresDSN is the connection string when store is "postgres".
	PostgresDSN string `yaml:"postgres_dsn"`
	// TTL is in-memory store only (A-leg eviction). Ignored by SQLite until pruning is implemented.
	TTL string `yaml:"ttl"`
	// MaxLegs is in-memory store only when TTL is empty. Must be >= 0. Ignored by SQLite until pruning exists.
	MaxLegs int `yaml:"max_legs"`
}

type PluginsConfig struct {
	Frontends []PluginConfig `yaml:"frontends"`
	Backends  []PluginConfig `yaml:"backends"`
	Features  []PluginConfig `yaml:"features"`
}

// PluginConfig keeps plugin-private config opaque to the core.
type PluginConfig struct {
	// Kind is the bundled factory id used for registry lookup (e.g. openai-responses).
	// When empty, ID is treated as both factory kind and instance id (legacy single-field configs).
	Kind string `yaml:"kind,omitempty"`
	// ID is the runtime instance id: routing keys, executor backend map keys, and duplicate detection.
	ID      string    `yaml:"id"`
	Enabled bool      `yaml:"enabled"`
	Config  yaml.Node `yaml:"config"`
}

// FactoryID returns the registry/factory identifier for this plugin row.
func (p PluginConfig) FactoryID() string {
	k := strings.TrimSpace(p.Kind)
	if k != "" {
		return k
	}
	return strings.TrimSpace(p.ID)
}

// InstanceID returns the configured runtime instance identifier (never empty for valid configs).
func (p PluginConfig) InstanceID() string {
	return strings.TrimSpace(p.ID)
}
