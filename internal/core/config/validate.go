package config

import (
	"fmt"
	"strings"
	"time"
)

// Validate checks plugin identity rules and continuity/store consistency after decoding.
// It does not validate model_aliases; call routing.ValidateModelAliasesConfig after LoadFile, or rely on runtimebundle.Build.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
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
	if err := validateHTTPClient(cfg); err != nil {
		return err
	}
	if err := validateServer(cfg); err != nil {
		return err
	}
	return validateRoutingHealth(cfg)
}

func validateServer(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	s := cfg.Server
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
	mp, err := checkObservabilityMetricsPath(cfg)
	if err != nil {
		return err
	}
	if err := add(mp); err != nil {
		return err
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
	store := strings.ToLower(strings.TrimSpace(cfg.Continuity.Store))
	if cfg.Continuity.InMemory {
		store = "memory"
	}
	if store == "" {
		store = "memory"
	}
	if store != "sqlite" {
		if cfg.Continuity.MaxLegs < 0 {
			return fmt.Errorf("continuity: max_legs must be >= 0 for memory store")
		}
		return nil
	}
	path := strings.TrimSpace(cfg.Continuity.SQLitePath)
	if path == "" {
		return fmt.Errorf("continuity: sqlite_path is required when store is \"sqlite\"")
	}
	if strings.ContainsAny(path, "\x00?#&") {
		return fmt.Errorf("continuity.sqlite_path: must not contain NUL, ?, #, or & (ambiguous with SQLite URI query)")
	}
	if strings.TrimSpace(cfg.Continuity.TTL) != "" {
		return fmt.Errorf("continuity: ttl is not supported for sqlite store (memory-only); remove ttl or use store: memory")
	}
	if cfg.Continuity.MaxLegs != 0 {
		return fmt.Errorf("continuity: max_legs is not supported for sqlite store (memory-only); remove max_legs or use store: memory")
	}
	return nil
}
