package config

import (
	"fmt"
	"strings"
	"time"
)

// parsePositiveDurationField validates optional positive durations using wrapped
// parse errors (secure_session-style wording).
func parsePositiveDurationField(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if d <= 0 {
		return fmt.Errorf("%s: must be a positive duration", field)
	}
	return nil
}

// parsePositiveDurationOptional validates optional positive durations using
// invalid-duration quoting (server/http_client-style wording).
func parsePositiveDurationOptional(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid duration %q", field, raw)
	}
	if d <= 0 {
		return fmt.Errorf("%s: duration must be positive", field)
	}
	return nil
}

// parsePositiveDurationFieldRequired validates that raw is non-empty and positive.
func parsePositiveDurationFieldRequired(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%s: required", field)
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid duration %q", field, raw)
	}
	if d <= 0 {
		return fmt.Errorf("%s: duration must be positive", field)
	}
	return nil
}

// normalizeEnum lowercases/trims *field, applies defaultVal when empty, and rejects
// values outside allowed. On success *field holds the normalized value.
func normalizeEnum(field *string, fieldName, defaultVal string, allowed ...string) error {
	v := strings.ToLower(strings.TrimSpace(*field))
	if v == "" {
		v = defaultVal
	}
	for _, a := range allowed {
		if v == a {
			*field = v
			return nil
		}
	}
	return fmt.Errorf("%s: want %s, got %q", fieldName, strings.Join(allowed, " or "), *field)
}

type protectedMountPath struct {
	enabled func(*Config) bool
	path    func(*Config) string
	field   string
}

var protectedMountPaths = []protectedMountPath{
	{
		enabled: func(cfg *Config) bool { return cfg.Accounting.Admin.Enabled },
		path:    func(cfg *Config) string { return cfg.Accounting.Admin.Path },
		field:   "accounting.admin.path",
	},
	{
		enabled: func(cfg *Config) bool { return AuthorityQueryEffectivelyExposed(cfg) },
		path:    func(cfg *Config) string { return cfg.Accounting.Authority.Query.PathPrefix },
		field:   "accounting.authority.query.path_prefix",
	},
	{
		enabled: func(cfg *Config) bool { return ControlPlaneQueryEffectivelyExposed(cfg) },
		path:    func(cfg *Config) string { return cfg.ControlPlane.Query.PathPrefix },
		field:   "control_plane.query.path_prefix",
	},
}

func validateProtectedMountPath(cfg *Config, mount protectedMountPath, norm func(string) string, add func(string) error) error {
	if cfg == nil || !mount.enabled(cfg) {
		return nil
	}
	p := strings.TrimSpace(mount.path(cfg))
	if mount.field == "accounting.authority.query.path_prefix" || mount.field == "control_plane.query.path_prefix" {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("%s: must start with /", mount.field)
		}
	} else if p == "" {
		return nil
	} else if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("%s: must start with /", mount.field)
	}
	if err := rejectHTTPPathDotDot(mount.field, p); err != nil {
		return err
	}
	return add(norm(p))
}
