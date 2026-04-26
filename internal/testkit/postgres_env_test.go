package testkit

import (
	"testing"
)

func TestPostgresTestDSN_empty(t *testing.T) {
	t.Setenv(LIPTestPostgresDSN, "")
	t.Setenv(LIPManagedPostgresDSN, "")
	dsn, ok := PostgresTestDSN()
	if ok || dsn != "" {
		t.Fatalf("got ok=%v dsn=%q", ok, dsn)
	}
}

func TestPostgresTestDSN_prefersNewName(t *testing.T) {
	t.Setenv(LIPTestPostgresDSN, "postgres://a/a")
	t.Setenv(LIPManagedPostgresDSN, "postgres://b/b")
	dsn, ok := PostgresTestDSN()
	if !ok || dsn != "postgres://a/a" {
		t.Fatalf("got ok=%v dsn=%q", ok, dsn)
	}
}

func TestPostgresTestDSN_fallsBackToLegacy(t *testing.T) {
	t.Setenv(LIPTestPostgresDSN, "")
	t.Setenv(LIPManagedPostgresDSN, " postgres://legacy/x ")
	dsn, ok := PostgresTestDSN()
	if !ok || dsn != "postgres://legacy/x" {
		t.Fatalf("got ok=%v dsn=%q", ok, dsn)
	}
}
