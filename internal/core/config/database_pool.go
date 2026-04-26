package config

import (
	"fmt"
	"strings"
	"time"
)

// DatabasePoolSettings holds validated optional *sql.DB pool tuning from [DatabaseConfig].
// Zero values mean unset (driver defaults). Use [ParseDatabasePoolSettings] after YAML decode
// and alongside [Validate] for full config checks.
type DatabasePoolSettings struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func validateDatabasePoolSettings(s DatabasePoolSettings) error {
	if s.MaxOpenConns < 0 {
		return fmt.Errorf("invalid pool max open conns: %d", s.MaxOpenConns)
	}
	if s.MaxIdleConns < 0 {
		return fmt.Errorf("invalid pool max idle conns: %d", s.MaxIdleConns)
	}
	if s.ConnMaxLifetime < 0 {
		return fmt.Errorf("invalid pool conn max lifetime: %s", s.ConnMaxLifetime)
	}
	if s.ConnMaxIdleTime < 0 {
		return fmt.Errorf("invalid pool conn max idle time: %s", s.ConnMaxIdleTime)
	}
	return nil
}

// ParseDatabasePoolSettings parses [DatabaseConfig] into [DatabasePoolSettings] and validates
// numeric and duration fields.
func ParseDatabasePoolSettings(d DatabaseConfig) (DatabasePoolSettings, error) {
	lifetime, err := parseOptionalDBDuration("conn_max_lifetime", d.ConnMaxLifetime)
	if err != nil {
		return DatabasePoolSettings{}, err
	}
	idle, err := parseOptionalDBDuration("conn_max_idle_time", d.ConnMaxIdleTime)
	if err != nil {
		return DatabasePoolSettings{}, err
	}
	ps := DatabasePoolSettings{
		MaxOpenConns:    d.MaxOpenConns,
		MaxIdleConns:    d.MaxIdleConns,
		ConnMaxLifetime: lifetime,
		ConnMaxIdleTime: idle,
	}
	if err := validateDatabasePoolSettings(ps); err != nil {
		return DatabasePoolSettings{}, fmt.Errorf("database: %w", err)
	}
	return ps, nil
}

func parseOptionalDBDuration(name, raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("database.%s: invalid duration %q: %w", name, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("database.%s: duration must be non-negative", name)
	}
	return d, nil
}
