package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestParseDatabasePoolSettings_zeroOK(t *testing.T) {
	t.Parallel()
	ps, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if ps.MaxOpenConns != 0 || ps.MaxIdleConns != 0 || ps.ConnMaxLifetime != 0 || ps.ConnMaxIdleTime != 0 {
		t.Fatalf("unexpected non-zero pool: %+v", ps)
	}
}

func TestParseDatabasePoolSettings_parsesDurations(t *testing.T) {
	t.Parallel()
	ps, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{
		MaxOpenConns:    3,
		ConnMaxLifetime: "30m",
		ConnMaxIdleTime: "5m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ps.MaxOpenConns != 3 {
		t.Fatalf("max open: %d", ps.MaxOpenConns)
	}
	if ps.ConnMaxLifetime != 30*time.Minute || ps.ConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("durations: %+v", ps)
	}
}

func TestParseDatabasePoolSettings_invalidDuration(t *testing.T) {
	t.Parallel()
	_, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{
		ConnMaxLifetime: "not-a-duration",
	})
	if err == nil || !strings.Contains(err.Error(), "conn_max_lifetime") {
		t.Fatalf("want conn_max_lifetime error, got %v", err)
	}
}

func TestParseDatabasePoolSettings_negativeMaxOpen(t *testing.T) {
	t.Parallel()
	_, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: -1})
	if err == nil || !strings.Contains(err.Error(), "database:") {
		t.Fatalf("want database pool error, got %v", err)
	}
}
