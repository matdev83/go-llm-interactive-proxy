package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
)

// Validate checks plugin identity rules and continuity/store consistency after decoding.
// It does not validate model_aliases; call routing.ValidateModelAliasesConfig after LoadFile,
// or rely on runtimebundle.Build.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	if cfg.ModelAliases == nil {
		cfg.ModelAliases = []ModelAliasConfig{}
	}
	if err := validatePluginSlice("plugins.frontends", cfg.Plugins.Frontends); err != nil {
		return err
	}
	if err := validatePluginSlice("plugins.backends", cfg.Plugins.Backends); err != nil {
		return err
	}
	if err := validatePluginSlice("plugins.features", cfg.Plugins.Features); err != nil {
		return err
	}
	if err := ValidateBackendDiscovery(cfg); err != nil {
		return err
	}
	if err := validateDatabaseConfig(cfg); err != nil {
		return err
	}
	if err := validateContinuityStores(cfg); err != nil {
		return err
	}
	if err := validateLogging(cfg); err != nil {
		return err
	}
	if err := validateDiagnosticsPaths(cfg); err != nil {
		return err
	}
	if err := validateObservability(cfg); err != nil {
		return err
	}
	if err := validateControlPlane(cfg); err != nil {
		return err
	}
	if err := validateDiagnosticsSecret(cfg); err != nil {
		return err
	}
	if err := ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		return err
	}
	if err := validateHTTPClient(cfg); err != nil {
		return err
	}
	if err := validateServer(cfg); err != nil {
		return err
	}
	if err := validateHTTPHeaders(cfg); err != nil {
		return err
	}
	if err := validateAccessAuth(cfg); err != nil {
		return err
	}
	if _, err := CompileGeoIP(cfg.Access.GeoIP); err != nil {
		return err
	}
	if err := validateSecureSession(cfg); err != nil {
		return err
	}
	if err := validateModelCatalog(cfg); err != nil {
		return err
	}
	if err := validateModelInventory(cfg); err != nil {
		return err
	}
	if _, err := EffectiveStreamRecoveryAutoResume(cfg, StreamRecoveryOverrides{}); err != nil {
		return err
	}
	if err := validateAccounting(cfg); err != nil {
		return err
	}
	if err := validateMetering(cfg); err != nil {
		return err
	}
	if err := validateRoutingHealth(cfg); err != nil {
		return err
	}
	if err := validateInterleaved(cfg); err != nil {
		return err
	}
	if err := validateAgentLoopGuard(cfg); err != nil {
		return err
	}
	if err := validateRoutingAffinity(cfg); err != nil {
		return err
	}
	if err := validateRoutingExecutionComposition(cfg); err != nil {
		return err
	}
	if err := validateRoutingOverrideAdmin(cfg); err != nil {
		return err
	}
	if err := identity.Validate(&cfg.Identity); err != nil {
		return err
	}
	// After store-specific errors so operators see DSN/path issues before pool bounds.
	return validatePostgresPoolBound(cfg)
}

func validateSecureSession(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	ss := &cfg.SecureSession
	if ss.Enabled != nil && !*ss.Enabled {
		return fmt.Errorf(
			"secure_session.enabled: false is no longer supported; remove the field " +
				"(secure sessions default on) or set secure_session.enabled: true",
		)
	}
	if err := normalizeEnum(&ss.Store, "secure_session.store", "memory", "memory", "sqlite", "postgres"); err != nil {
		return err
	}
	store := ss.Store
	key := strings.TrimSpace(ss.TokenFingerprintKey)
	if store == "sqlite" || store == "postgres" {
		if len(key) < 32 {
			return fmt.Errorf("secure_session.token_fingerprint_key: must be at least 32 characters when store is %s", store)
		}
	} else if key != "" && len(key) < 32 {
		return fmt.Errorf(
			"secure_session.token_fingerprint_key: when set, must be at least 32 characters " +
				"(memory store may omit the key for a process-local ephemeral fingerprint)",
		)
	}
	rw := strings.TrimSpace(ss.ResumeWindow)
	if err := parsePositiveDurationField("secure_session.resume_window", rw); err != nil {
		return err
	}
	if err := normalizeEnum(&ss.AuditDurability, "secure_session.audit_durability", "best_effort", "best_effort", "durable"); err != nil {
		return err
	}
	audit := ss.AuditDurability
	if audit == "durable" {
		if store != "sqlite" && store != "postgres" {
			return fmt.Errorf(
				"secure_session.audit_durability: durable requires a durable secure_session.store "+
					"(sqlite or postgres), not %q",
				store,
			)
		}
	}
	if store == "sqlite" {
		path := strings.TrimSpace(ss.SQLitePath)
		if path == "" {
			return fmt.Errorf("secure_session.sqlite_path: required when store is \"sqlite\"")
		}
		if strings.ContainsAny(path, "\x00?#&") {
			return fmt.Errorf("secure_session.sqlite_path: must not contain NUL, ?, #, or & (ambiguous with SQLite URI query)")
		}
	}
	if store == "postgres" {
		dsn := strings.TrimSpace(ss.PostgresDSN)
		if dsn == "" {
			return fmt.Errorf("secure_session.postgres_dsn: required when store is \"postgres\"")
		}
		if strings.Contains(dsn, "\x00") {
			return fmt.Errorf("secure_session.postgres_dsn: must not contain NUL")
		}
	} else if d := strings.TrimSpace(ss.PostgresDSN); d != "" {
		return fmt.Errorf("secure_session.postgres_dsn: may only be set when store is \"postgres\" (got %q)", store)
	}

	if err := normalizeEnum(&ss.NonDurableWarning, "secure_session.non_durable_warning", "log", "silent", "log", "strict"); err != nil {
		return err
	}

	if err := normalizeEnum(&ss.RedactionDefault, "secure_session.redaction_default", "standard", "standard", "strict"); err != nil {
		return err
	}

	if ss.DiagnosticsExposeSummaries {
		p := strings.TrimSpace(ss.DiagnosticsPathPrefix)
		if p == "" {
			return fmt.Errorf("secure_session.diagnostics_path_prefix: required when diagnostics_expose_summaries is true")
		}
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("secure_session.diagnostics_path_prefix: must start with /")
		}
		// Shared secret for these routes on non-loopback binds is enforced by
		// [ValidateProtectedDiagnosticsPosture] (surface name secure_session_summaries).
	}
	if !ss.DiagnosticsExposeSummaries {
		if p := strings.TrimSpace(ss.DiagnosticsPathPrefix); p != "" && !strings.HasPrefix(p, "/") {
			return fmt.Errorf("secure_session.diagnostics_path_prefix: must start with /")
		}
	}
	if err := normalizeEnum(&ss.WorkspaceResolveOnError, "secure_session.workspace_resolve_on_error", "fail_open", "fail_open", "fail_closed"); err != nil {
		return err
	}
	if err := parsePositiveDurationField("secure_session.sql_query_cache_ttl", ss.SQLQueryCacheTTL); err != nil {
		return err
	}
	if ss.SQLQueryCacheMaxEntries < 0 {
		return fmt.Errorf("secure_session.sql_query_cache_max_entries: must be >= 0")
	}
	return nil
}

func validateServer(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	applyDefaultServerListenAddress(cfg)
	applyDefaultServerLimits(cfg)
	s := cfg.Server
	// Listener posture for no_auth vs broad binds is enforced in validateAccessAuth via
	// accessmode.ValidatePosture (combines server.auth_mode, access.mode, and listeners).
	switch cfg.EffectiveServerAuthMode() {
	case AuthModeNoAuth, AuthModeExternal:
	default:
		return fmt.Errorf("server.auth_mode: want no_auth or external, got %q", s.AuthMode)
	}
	if s.MaxConcurrentDecodes < 0 {
		return fmt.Errorf("server.max_concurrent_decodes: must be >= 0")
	}
	if s.MaxInflightDecodeBytes < 0 {
		return fmt.Errorf("server.max_inflight_decode_bytes: must be >= 0")
	}
	if s.MaxPendingWireEvents < 0 {
		return fmt.Errorf("server.max_pending_wire_events: must be >= 0")
	}
	if s.MaxInflightDecodeBytes < s.EffectiveMaxRequestBodyBytesForBudget() {
		return fmt.Errorf("server.max_inflight_decode_bytes: must be >= max single request body (%d bytes)", s.EffectiveMaxRequestBodyBytesForBudget())
	}
	for _, chk := range []struct {
		name string
		val  string
	}{
		{"read_header_timeout", s.ReadHeaderTimeout},
		{"read_timeout", s.ReadTimeout},
		{"write_timeout", s.WriteTimeout},
		{"idle_timeout", s.IdleTimeout},
		{"shutdown_timeout", s.ShutdownTimeout},
		{"pre_request_keepalive.interval", s.PreRequestKeepalive.Interval},
	} {
		if err := parsePositiveDurationOptional("server."+chk.name, chk.val); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPClient(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	hc := cfg.HTTPClient
	if hc.MaxIdleConns != nil && *hc.MaxIdleConns < 1 {
		return fmt.Errorf("http_client.max_idle_conns: must be >= 1 when set")
	}
	if hc.MaxIdleConnsPerHost != nil && *hc.MaxIdleConnsPerHost < 1 {
		return fmt.Errorf("http_client.max_idle_conns_per_host: must be >= 1 when set")
	}
	parseDur := parsePositiveDurationOptional
	for _, chk := range []struct {
		name string
		val  string
	}{
		{"idle_conn_timeout", hc.IdleConnTimeout},
		{"response_header_timeout", hc.ResponseHeaderTimeout},
		{"dial_timeout", hc.DialTimeout},
		{"keep_alive", hc.KeepAlive},
		{"tls_handshake_timeout", hc.TLSHandshakeTimeout},
		{"expect_continue_timeout", hc.ExpectContinueTimeout},
		{"client_timeout", hc.ClientTimeout},
	} {
		if err := parseDur("http_client."+chk.name, chk.val); err != nil {
			return err
		}
	}
	return nil
}

func validateDiagnosticsSecret(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	s := strings.TrimSpace(cfg.Diagnostics.SharedSecret)
	if s != "" && len(s) < 12 {
		return fmt.Errorf("diagnostics.shared_secret: must be at least 12 characters when set")
	}
	return nil
}

// rejectHTTPPathDotDot rejects configured URL paths that contain a ".." segment. Such values are
// unnecessary for mux mounts, confuse overlap validation, and are a foot-gun at HTTP boundaries.
func rejectHTTPPathDotDot(fieldName, p string) error {
	if slices.Contains(strings.Split(p, "/"), "..") {
		return fmt.Errorf("%s: must not contain .. path segments", fieldName)
	}
	return nil
}

func validateDiagnosticsPaths(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	norm := func(p string) string {
		return strings.TrimSuffix(strings.TrimSpace(p), "/")
	}
	check := func(name, p string) (string, error) {
		p = strings.TrimSpace(p)
		if p == "" {
			return "", nil
		}
		if !strings.HasPrefix(p, "/") {
			return "", fmt.Errorf("diagnostics.%s: must start with /", name)
		}
		if err := rejectHTTPPathDotDot("diagnostics."+name, p); err != nil {
			return "", err
		}
		return norm(p), nil
	}
	paths := make([]string, 0, 8)
	add := func(s string) error {
		if s == "" {
			return nil
		}
		for _, existing := range paths {
			if s == existing || strings.HasPrefix(s, existing+"/") || strings.HasPrefix(existing, s+"/") {
				return fmt.Errorf("diagnostics: paths %q and %q overlap or duplicate (normalize trailing slashes)", existing, s)
			}
		}
		paths = append(paths, s)
		return nil
	}
	hp, err := check("health_path", cfg.Diagnostics.HealthPath)
	if err != nil {
		return err
	}
	if err := add(hp); err != nil {
		return err
	}
	ap, err := check("attempts_path", cfg.Diagnostics.AttemptsPath)
	if err != nil {
		return err
	}
	if err := add(ap); err != nil {
		return err
	}
	ip, err := check("inventory_path", cfg.Diagnostics.InventoryPath)
	if err != nil {
		return err
	}
	if err := add(ip); err != nil {
		return err
	}
	rt, err := check("route_trace_path", cfg.Diagnostics.RouteTracePath)
	if err != nil {
		return err
	}
	if err := add(rt); err != nil {
		return err
	}
	pp, err := check("pprof_path", cfg.Diagnostics.PprofPath)
	if err != nil {
		return err
	}
	if err := add(pp); err != nil {
		return err
	}
	if cfg.SecureSessionEffectivelyEnabled() {
		ssp := strings.TrimSpace(cfg.SecureSession.DiagnosticsPathPrefix)
		if ssp != "" {
			if !strings.HasPrefix(ssp, "/") {
				return fmt.Errorf("secure_session.diagnostics_path_prefix: must start with /")
			}
			if err := rejectHTTPPathDotDot("secure_session.diagnostics_path_prefix", ssp); err != nil {
				return err
			}
			ssp = strings.TrimSuffix(ssp, "/")
			if err := add(ssp); err != nil {
				return err
			}
		}
	}
	mp, err := checkObservabilityMetricsPath(cfg)
	if err != nil {
		return err
	}
	if err := add(mp); err != nil {
		return err
	}
	mcd := strings.TrimSpace(cfg.ModelCatalog.DiagnosticsPath)
	if mcd != "" {
		if !strings.HasPrefix(mcd, "/") {
			return fmt.Errorf("model_catalog.diagnostics_path: must start with /")
		}
		if err := rejectHTTPPathDotDot("model_catalog.diagnostics_path", mcd); err != nil {
			return err
		}
		mcd = norm(mcd)
		if err := add(mcd); err != nil {
			return err
		}
	}
	mid := strings.TrimSpace(cfg.ModelInventory.DiagnosticsPath)
	if mid != "" {
		if !strings.HasPrefix(mid, "/") {
			return fmt.Errorf("model_inventory.diagnostics_path: must start with /")
		}
		if err := rejectHTTPPathDotDot("model_inventory.diagnostics_path", mid); err != nil {
			return err
		}
		mid = norm(mid)
		if err := add(mid); err != nil {
			return err
		}
	}
	// accounting.admin.path, authority query, control_plane query, routing
	// override admin, and billing reports are protected operator mounts and
	// must satisfy the same dot-segment and overlap rules.
	for _, mount := range protectedMountPaths {
		if err := validateProtectedMountPath(cfg, mount, norm, add); err != nil {
			return err
		}
	}
	return nil
}

func checkObservabilityMetricsPath(cfg *Config) (string, error) {
	if cfg == nil || !cfg.Observability.Metrics.Enabled {
		return "", nil
	}
	p := strings.TrimSpace(cfg.Observability.Metrics.Path)
	if p == "" {
		return "", nil
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("observability.metrics.path: must start with /")
	}
	if err := rejectHTTPPathDotDot("observability.metrics.path", p); err != nil {
		return "", err
	}
	return strings.TrimSuffix(p, "/"), nil
}

func validateObservability(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if cfg.Observability.Metrics.Enabled {
		p := strings.TrimSpace(cfg.Observability.Metrics.Path)
		if p == "" {
			return fmt.Errorf("observability.metrics.path: required when observability.metrics.enabled is true")
		}
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("observability.metrics.path: must start with /")
		}
		if err := rejectHTTPPathDotDot("observability.metrics.path", p); err != nil {
			return err
		}
	}
	if cfg.Observability.Tracing.Enabled {
		if sr := cfg.Observability.Tracing.SampleRatio; sr != nil {
			r := *sr
			if r <= 0 || r > 1 {
				return fmt.Errorf("observability.tracing.sample_ratio: must be > 0 and <= 1 when set (got %v)", r)
			}
		}
	}
	return nil
}

func normalizeLogging(cfg *Config) {
	if strings.TrimSpace(cfg.Logging.Level) == "" {
		cfg.Logging.Level = "info"
	}
	if strings.TrimSpace(cfg.Logging.Format) == "" {
		cfg.Logging.Format = "json"
	}
}

func validateLogging(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	normalizeLogging(cfg)
	lvl := strings.ToLower(strings.TrimSpace(cfg.Logging.Level))
	switch lvl {
	case "debug", "info", "warn", "error":
		cfg.Logging.Level = lvl
	default:
		return fmt.Errorf("logging.level: unknown %q (want debug, info, warn, error)", cfg.Logging.Level)
	}
	f := strings.ToLower(strings.TrimSpace(cfg.Logging.Format))
	switch f {
	case "json", "text":
		cfg.Logging.Format = f
	default:
		return fmt.Errorf("logging.format: unknown %q (want json, text)", cfg.Logging.Format)
	}
	for i, p := range cfg.Logging.AccessLogSkipPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			return fmt.Errorf("logging.access_log_skip_paths[%d]: empty entry", i)
		}
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("logging.access_log_skip_paths[%d]: must start with /", i)
		}
		if err := rejectHTTPPathDotDot(fmt.Sprintf("logging.access_log_skip_paths[%d]", i), p); err != nil {
			return err
		}
		cfg.Logging.AccessLogSkipPaths[i] = p
	}
	return nil
}

func validateRoutingHealth(cfg *Config) error {
	cb := cfg.Routing.Health.CircuitBreaker
	if !cb.Enabled {
		return nil
	}
	if cb.FailureThreshold < 1 {
		return fmt.Errorf("routing.health.circuit_breaker: failure_threshold must be >= 1 when enabled")
	}
	raw := strings.TrimSpace(cb.OpenFor)
	if err := parsePositiveDurationField("routing.health.circuit_breaker.open_for", raw); err != nil {
		return err
	}
	return nil
}

func validateRoutingAffinity(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	store := strings.ToLower(strings.TrimSpace(cfg.Routing.Affinity.Store))
	if store == "" {
		store = "memory"
	}
	if err := normalizeEnum(&store, "routing.affinity.store", "memory", "memory"); err != nil {
		return err
	}
	if err := normalizeEnum(&cfg.Routing.Affinity.MissingIdentity, "routing.affinity.missing_identity", "fail_closed", "ignore", "fail_closed"); err != nil {
		return err
	}
	cfg.Routing.Affinity.Store = store
	return nil
}

func validatePluginSlice(section string, rows []PluginConfig) error {
	seen := make(map[string]struct{})
	for _, p := range rows {
		id := p.InstanceID()
		if id == "" {
			return fmt.Errorf("%s: plugin row requires non-empty id", section)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%s: duplicate plugin instance id %q", section, id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(p.FactoryID()) == "" {
			return fmt.Errorf("%s: plugin %q missing factory kind (set kind or id)", section, id)
		}
	}
	return nil
}

func validateContinuityStores(cfg *Config) error {
	store := EffectiveContinuityStore(cfg.Continuity)
	switch store {
	case "memory", "sqlite", "postgres":
	default:
		return fmt.Errorf("continuity.store: want memory, sqlite, or postgres, got %q", cfg.Continuity.Store)
	}
	if store == "memory" {
		if cfg.Continuity.MaxLegs < 0 {
			return fmt.Errorf("continuity: max_legs must be >= 0 for memory store")
		}
		if d := strings.TrimSpace(cfg.Continuity.PostgresDSN); d != "" {
			return fmt.Errorf("continuity.postgres_dsn: may only be set when continuity.store is \"postgres\"")
		}
		return nil
	}
	if d := strings.TrimSpace(cfg.Continuity.PostgresDSN); d != "" && store != "postgres" {
		return fmt.Errorf("continuity.postgres_dsn: may only be set when continuity.store is \"postgres\"")
	}
	if store == "sqlite" {
		path := strings.TrimSpace(cfg.Continuity.SQLitePath)
		if path == "" {
			return fmt.Errorf("continuity: sqlite_path is required when store is \"sqlite\"")
		}
		if strings.ContainsAny(path, "\x00?#&") {
			return fmt.Errorf(
				"continuity.sqlite_path: must not contain NUL, ?, #, or & " +
					"(ambiguous with SQLite URI query)",
			)
		}
		if strings.TrimSpace(cfg.Continuity.TTL) != "" {
			return fmt.Errorf(
				"continuity: ttl is not supported for sqlite store (memory-only); remove ttl or use store: memory",
			)
		}
		if cfg.Continuity.MaxLegs != 0 {
			return fmt.Errorf(
				"continuity: max_legs is not supported for sqlite store (memory-only); " +
					"remove max_legs or use store: memory",
			)
		}
		return nil
	}
	// store == "postgres"
	if strings.TrimSpace(cfg.Continuity.SQLitePath) != "" {
		return fmt.Errorf("continuity.sqlite_path: may only be set when store is \"sqlite\"")
	}
	if strings.TrimSpace(cfg.Continuity.TTL) != "" {
		return fmt.Errorf(
			"continuity: ttl is not supported for postgres store (memory-only); remove ttl or use store: memory",
		)
	}
	if cfg.Continuity.MaxLegs != 0 {
		return fmt.Errorf(
			"continuity: max_legs is not supported for postgres store (memory-only); " +
				"remove max_legs or use store: memory",
		)
	}
	dsn := strings.TrimSpace(cfg.Continuity.PostgresDSN)
	if dsn == "" {
		return fmt.Errorf("continuity.postgres_dsn: required when store is \"postgres\"")
	}
	if strings.Contains(dsn, "\x00") {
		return fmt.Errorf("continuity.postgres_dsn: must not contain NUL")
	}
	return nil
}

func validateDatabaseConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	_, err := ParseDatabasePoolSettings(cfg.Database)
	return err
}

func validatePostgresPoolBound(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	if usesManagedPostgres(cfg) && cfg.Database.MaxOpenConns <= 0 {
		return fmt.Errorf("database.max_open_conns: required when any store is postgres")
	}
	return nil
}

func validateModelCatalog(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	mc := &cfg.ModelCatalog
	if mc.Enabled {
		if strings.TrimSpace(mc.CachePath) == "" {
			return fmt.Errorf("model_catalog.cache_path: required when model_catalog.enabled is true")
		}
	}
	if mc.ExternalUpdatesEnabled {
		if strings.TrimSpace(mc.CachePath) == "" {
			return fmt.Errorf("model_catalog.cache_path: required when model_catalog.external_updates_enabled is true")
		}
		su := strings.TrimSpace(mc.SourceURL)
		if su == "" {
			return fmt.Errorf("model_catalog.source_url: required when model_catalog.external_updates_enabled is true")
		}
		u, err := url.Parse(su)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("model_catalog.source_url: invalid URL")
		}
		if u.Scheme != "https" {
			return fmt.Errorf("model_catalog.source_url: want https URL when model_catalog.external_updates_enabled is true")
		}
		ui := strings.TrimSpace(mc.UpdateInterval)
		if err := parsePositiveDurationFieldRequired("model_catalog.update_interval", ui); err != nil {
			if strings.Contains(err.Error(), "required") {
				return fmt.Errorf(
					"model_catalog.update_interval: must be positive when " +
						"model_catalog.external_updates_enabled is true",
				)
			}
			return err
		}
	}
	if su := strings.TrimSpace(mc.SourceURL); su != "" {
		u, err := url.Parse(su)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("model_catalog.source_url: invalid URL")
		}
	}
	ft := strings.TrimSpace(mc.FetchTimeout)
	if err := parsePositiveDurationField("model_catalog.fetch_timeout", ft); err != nil {
		if ft != "" && strings.Contains(err.Error(), "must be a positive duration") {
			return fmt.Errorf("model_catalog.fetch_timeout: must be a positive duration when set")
		}
		return err
	}
	posLimit := func(field string, v *int64) error {
		if v == nil {
			return nil
		}
		if *v <= 0 {
			return fmt.Errorf("model_catalog: %s must be positive when set", field)
		}
		return nil
	}
	for i, row := range mc.ModelOverrides {
		if strings.TrimSpace(row.Model) == "" {
			return fmt.Errorf("model_catalog.model_overrides[%d].model: required", i)
		}
		if err := posLimit("context_limit_tokens", row.ContextLimitTokens); err != nil {
			return err
		}
		if err := posLimit("input_limit_tokens", row.InputLimitTokens); err != nil {
			return err
		}
		if err := posLimit("output_limit_tokens", row.OutputLimitTokens); err != nil {
			return err
		}
	}
	for i, row := range mc.BackendModelOverrides {
		if strings.TrimSpace(row.Backend) == "" {
			return fmt.Errorf("model_catalog.backend_model_overrides[%d].backend: required", i)
		}
		if strings.TrimSpace(row.Model) == "" {
			return fmt.Errorf("model_catalog.backend_model_overrides[%d].model: required", i)
		}
		if err := posLimit("context_limit_tokens", row.ContextLimitTokens); err != nil {
			return err
		}
		if err := posLimit("input_limit_tokens", row.InputLimitTokens); err != nil {
			return err
		}
		if err := posLimit("output_limit_tokens", row.OutputLimitTokens); err != nil {
			return err
		}
	}
	return nil
}

func validateModelInventory(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	mi := &cfg.ModelInventory
	if strings.TrimSpace(mi.RefreshInterval) == "" {
		mi.RefreshInterval = DefaultModelInventoryRefreshInterval.String()
	}
	if strings.TrimSpace(mi.FetchTimeout) == "" {
		mi.FetchTimeout = DefaultModelInventoryFetchTimeout.String()
	}
	d, err := time.ParseDuration(strings.TrimSpace(mi.RefreshInterval))
	if err != nil {
		return fmt.Errorf("model_inventory.refresh_interval: %w", err)
	}
	if d < DefaultModelInventoryRefreshInterval {
		return fmt.Errorf("model_inventory.refresh_interval: must be at least %s", DefaultModelInventoryRefreshInterval)
	}
	ft, err := time.ParseDuration(strings.TrimSpace(mi.FetchTimeout))
	if err != nil {
		return fmt.Errorf("model_inventory.fetch_timeout: %w", err)
	}
	if ft <= 0 {
		return fmt.Errorf("model_inventory.fetch_timeout: must be a positive duration")
	}
	return nil
}

func validateRoutingExecutionComposition(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	raw := strings.TrimSpace(string(cfg.Routing.ExecutionCompositionPolicy))
	if raw == "" {
		return nil
	}
	switch ExecutionCompositionPolicy(raw) {
	case ExecutionCompositionSafe, ExecutionCompositionUnrestricted:
		return nil
	default:
		return fmt.Errorf("invalid routing.execution_composition_policy %q: must be %q or %q",
			cfg.Routing.ExecutionCompositionPolicy, ExecutionCompositionSafe, ExecutionCompositionUnrestricted)
	}
}
