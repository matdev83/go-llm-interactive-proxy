package testkit

import (
	"os"
	"strings"
	"testing"
)

// Environment variable names for optional PostgreSQL integration tests.
const (
	// LIPTestPostgresDSN is the preferred DSN for integration tests (spec bun-database-abstraction).
	LIPTestPostgresDSN = "LIP_TEST_POSTGRES_DSN"
	// LIPManagedPostgresDSN is a legacy alias still accepted by [PostgresTestDSN].
	LIPManagedPostgresDSN = "LIP_MANAGED_POSTGRES_DSN"
)

// PostgresTestDSN returns a non-empty managed PostgreSQL DSN when LIPTestPostgresDSN or
// LIPManagedPostgresDSN is set. If both are set, LIPTestPostgresDSN wins.
func PostgresTestDSN() (dsn string, ok bool) {
	a := strings.TrimSpace(os.Getenv(LIPTestPostgresDSN))
	if a != "" {
		return a, true
	}
	b := strings.TrimSpace(os.Getenv(LIPManagedPostgresDSN))
	if b != "" {
		return b, true
	}
	return "", false
}

// SkipUnlessPostgres skips the test when no integration DSN is configured.
func SkipUnlessPostgres(t *testing.T) string {
	t.Helper()
	dsn, ok := PostgresTestDSN()
	if !ok {
		t.Skipf("set %s (or legacy %s) to run PostgreSQL integration test", LIPTestPostgresDSN, LIPManagedPostgresDSN)
	}
	return dsn
}
