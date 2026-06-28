package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
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
	if err := validateAccessAuth(cfg); err != nil {
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
	if err := validateRoutingHealth(cfg); err != nil {
		return err
	}
	if err := validateInterleaved(cfg); err != nil {
		return err
	}
	return validateRoutingAffinity(cfg)
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
	store := strings.ToLower(strings.TrimSpace(ss.Store))
	if store == "" {
		store = "memory"
		ss.Store = "memory"
	}
	switch store {
	case "memory", "sqlite", "postgres":
	default:
		return fmt.Errorf("secure_session.store: want memory, sqlite, or postgres, got %q", ss.Store)
	}
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
	if rw != "" {
		d, err := time.ParseDuration(rw)
		if err != nil {
			return fmt.Errorf("secure_session.resume_window: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("secure_session.resume_window: must be a positive duration")
		}
	}
	audit := strings.ToLower(strings.TrimSpace(ss.AuditDurability))
	if audit == "" {
		audit = "best_effort"
	}
	if audit != "best_effort" && audit != "durable" {
		return fmt.Errorf("secure_session.audit_durability: want best_effort or durable, got %q", ss.AuditDurability)
	}
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

	nd := strings.ToLower(strings.TrimSpace(ss.NonDurableWarning))
	if nd == "" {
		nd = "log"
	}
	switch nd {
	case "silent", "log", "strict":
	default:
		return fmt.Errorf("secure_session.non_durable_warning: want silent, log, or strict, got %q", ss.NonDurableWarning)
	}

	red := strings.ToLower(strings.TrimSpace(ss.RedactionDefault))
	if red == "" {
		red = "standard"
	}
	if red != "standard" && red != "strict" {
		return fmt.Errorf("secure_session.redaction_default: want standard or strict, got %q", ss.RedactionDefault)
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
	wsErr := strings.ToLower(strings.TrimSpace(ss.WorkspaceResolveOnError))
	if wsErr == "" {
		wsErr = "fail_open"
	}
	if wsErr != "fail_open" && wsErr != "fail_closed" {
		return fmt.Errorf(
			"secure_session.workspace_resolve_on_error: want fail_open or fail_closed, got %q",
			ss.WorkspaceResolveOnError,
		)
	}
	if ttlRaw := strings.TrimSpace(ss.SQLQueryCacheTTL); ttlRaw != "" {
		d, err := time.ParseDuration(ttlRaw)
		if err != nil {
			return fmt.Errorf("secure_session.sql_query_cache_ttl: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("secure_session.sql_query_cache_ttl: must be a positive duration")
		}
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
	s := cfg.Server
	// Listener posture for no_auth vs broad binds is enforced in validateAccessAuth via
	// accessmode.ValidatePosture (combines server.auth_mode, access.mode, and listeners).
	switch cfg.EffectiveServerAuthMode() {
	case AuthModeNoAuth, AuthModeExternal:
	default:
		return fmt.Errorf("server.auth_mode: want no_auth or external, got %q", s.AuthMode)
	}
	if s.MaxPendingWireEvents < 0 {
		return fmt.Errorf("server.max_pending_wire_events: must be >= 0")
	}
	parse := func(name, val string) error {
		val = strings.TrimSpace(val)
		if val == "" {
			return nil
		}
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("server.%s: invalid duration %q", name, val)
		}
		if d <= 0 {
			return fmt.Errorf("server.%s: duration must be positive", name)
		}
		return nil
	}
	for _, chk := range []struct {
		name string
		val  string
	}{
		{"read_header_timeout", s.ReadHeaderTimeout},
		{"read_timeout", s.ReadTimeout},
		{"write_timeout", s.WriteTimeout},
		{"idle_timeout", s.IdleTimeout},
		{"pre_request_keepalive.interval", s.PreRequestKeepalive.Interval},
	} {
		if err := parse(chk.name, chk.val); err != nil {
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
	parseDur := func(name, s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("http_client.%s: invalid duration %q", name, s)
		}
		if d <= 0 {
			return fmt.Errorf("http_client.%s: duration must be positive", name)
		}
		return nil
	}
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
		if err := parseDur(chk.name, chk.val); err != nil {
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
	default:
		return fmt.Errorf("logging.level: unknown %q (want debug, info, warn, error)", cfg.Logging.Level)
	}
	f := strings.ToLower(strings.TrimSpace(cfg.Logging.Format))
	switch f {
	case "json", "text":
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
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("routing.health.circuit_breaker.open_for: %w", err)
	}
	if d <= 0 {
		return fmt.Errorf("routing.health.circuit_breaker.open_for: must be a positive duration")
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
	switch store {
	case "memory":
	default:
		return fmt.Errorf("routing.affinity.store: want memory, got %q", cfg.Routing.Affinity.Store)
	}
	missing := strings.ToLower(strings.TrimSpace(cfg.Routing.Affinity.MissingIdentity))
	if missing == "" {
		missing = "fail_closed"
	}
	switch missing {
	case "ignore", "fail_closed":
	default:
		return fmt.Errorf("routing.affinity.missing_identity: want ignore or fail_closed, got %q", cfg.Routing.Affinity.MissingIdentity)
	}
	cfg.Routing.Affinity.Store = store
	cfg.Routing.Affinity.MissingIdentity = missing
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
		d, err := time.ParseDuration(ui)
		if err != nil {
			return fmt.Errorf("model_catalog.update_interval: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf(
				"model_catalog.update_interval: must be positive when " +
					"model_catalog.external_updates_enabled is true",
			)
		}
	}
	if su := strings.TrimSpace(mc.SourceURL); su != "" {
		u, err := url.Parse(su)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("model_catalog.source_url: invalid URL")
		}
	}
	ft := strings.TrimSpace(mc.FetchTimeout)
	if ft != "" {
		d, err := time.ParseDuration(ft)
		if err != nil {
			return fmt.Errorf("model_catalog.fetch_timeout: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("model_catalog.fetch_timeout: must be a positive duration when set")
		}
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

func validateAccounting(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	a := &cfg.Accounting
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode == "" {
		mode = "provider_first"
		a.Mode = mode
	}
	switch mode {
	case "provider_first", "local_only", "provider_required", "advisory":
	default:
		return fmt.Errorf("accounting.mode: want provider_first, local_only, provider_required, or advisory, got %q", a.Mode)
	}
	if raw := strings.TrimSpace(a.CountTimeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("accounting.count_timeout: invalid duration %q", raw)
		}
		if d <= 0 {
			return fmt.Errorf("accounting.count_timeout: duration must be positive")
		}
	}
	if err := validateAccountingTokenizer(a); err != nil {
		return err
	}
	if mode == "provider_required" && hasLocalTokenizerFallback(a.Tokenizer) {
		return fmt.Errorf("accounting.mode: provider_required cannot configure local tokenizer fallback")
	}
	if err := validateAccountingPreflight(a); err != nil {
		return err
	}
	if err := validateAccountingLedger(a); err != nil {
		return err
	}
	if err := validateAccountingAdmin(a); err != nil {
		return err
	}
	if len(a.Pricing.Models) > 0 {
		_, err := accounting.NewPriceCatalog(AccountingPriceCatalogConfig(a.Pricing))
		if err != nil {
			return fmt.Errorf("accounting.pricing: %w", err)
		}
	}
	return nil
}

func validateAccountingTokenizer(a *AccountingConfig) error {
	if strings.Contains(a.Tokenizer.DefaultEncoding, "\x00") {
		return fmt.Errorf("accounting.tokenizer.default_encoding: must not contain NUL")
	}
	for model, encoding := range a.Tokenizer.ModelMappings {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("accounting.tokenizer.model_mappings: model key must be non-empty")
		}
		if strings.TrimSpace(encoding) == "" {
			return fmt.Errorf("accounting.tokenizer.model_mappings[%q]: encoding must be non-empty", model)
		}
		if strings.Contains(model, "\x00") || strings.Contains(encoding, "\x00") {
			return fmt.Errorf("accounting.tokenizer.model_mappings[%q]: must not contain NUL", model)
		}
	}
	return nil
}

func hasLocalTokenizerFallback(t AccountingTokenizerConfig) bool {
	return strings.TrimSpace(t.DefaultEncoding) != "" || len(t.ModelMappings) > 0
}

func validateAccountingPreflight(a *AccountingConfig) error {
	mode := strings.ToLower(strings.TrimSpace(a.Preflight.Mode))
	if mode == "" {
		return nil
	}
	switch mode {
	case "required", "advisory":
	default:
		return fmt.Errorf("accounting.preflight.mode: want required or advisory, got %q", a.Preflight.Mode)
	}
	for _, chk := range []struct {
		name  string
		value int64
	}{
		{"max_input_tokens", a.Preflight.MaxInputTokens},
		{"max_output_tokens", a.Preflight.MaxOutputTokens},
		{"max_context_tokens", a.Preflight.MaxContextTokens},
	} {
		if chk.value < 0 {
			return fmt.Errorf("accounting.preflight.%s: must be >= 0", chk.name)
		}
	}
	return nil
}

func validateAccountingLedger(a *AccountingConfig) error {
	store := strings.ToLower(strings.TrimSpace(a.Ledger.Store))
	if store == "" {
		store = "memory"
		a.Ledger.Store = store
	}
	switch store {
	case "memory", "sqlite", "postgres":
	default:
		return fmt.Errorf("accounting.ledger.store: want memory, sqlite, or postgres, got %q", a.Ledger.Store)
	}
	if store == "sqlite" && strings.TrimSpace(a.Ledger.SQLitePath) == "" {
		return fmt.Errorf("accounting.ledger.sqlite_path: required when store is \"sqlite\"")
	}
	if store == "postgres" && strings.TrimSpace(a.Ledger.PostgresDSN) == "" {
		return fmt.Errorf("accounting.ledger.postgres_dsn: required when store is \"postgres\"")
	}
	if store != "postgres" && strings.TrimSpace(a.Ledger.PostgresDSN) != "" {
		return fmt.Errorf("accounting.ledger.postgres_dsn: may only be set when store is \"postgres\" (got %q)", store)
	}
	policy := strings.ToLower(strings.TrimSpace(a.Ledger.WritePolicy))
	if policy == "" {
		policy = "required"
		a.Ledger.WritePolicy = policy
	}
	if policy != "required" && policy != "best_effort" {
		return fmt.Errorf("accounting.ledger.write_policy: want required or best_effort, got %q", a.Ledger.WritePolicy)
	}
	return nil
}

func validateAccountingAdmin(a *AccountingConfig) error {
	if !a.Admin.Enabled {
		return nil
	}
	path := strings.TrimSpace(a.Admin.Path)
	if path == "" {
		return fmt.Errorf("accounting.admin.path: required when admin is enabled")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("accounting.admin.path: must start with /")
	}
	if a.Admin.MaxBodyBytes < 0 {
		return fmt.Errorf("accounting.admin.max_body_bytes: must be >= 0")
	}
	return nil
}

func AccountingPriceCatalogConfig(cfg AccountingPricingConfig) accounting.PriceCatalogConfig {
	models := make([]accounting.ModelPriceConfig, 0, len(cfg.Models))
	for _, row := range cfg.Models {
		models = append(models, accounting.ModelPriceConfig{
			Backend:              row.Backend,
			Model:                row.Model,
			InputPer1M:           row.InputPer1M,
			CachedInputPer1M:     row.CachedInputPer1M,
			CacheWriteInputPer1M: row.CacheWriteInputPer1M,
			OutputPer1M:          row.OutputPer1M,
			ReasoningOutputPer1M: row.ReasoningOutputPer1M,
		})
	}
	return accounting.PriceCatalogConfig{
		Version:  cfg.CatalogVersion,
		Currency: cfg.Currency,
		Models:   models,
	}
}
