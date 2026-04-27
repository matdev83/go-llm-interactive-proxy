package continuity_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
)

func TestOpenStoreContext_nilContext(t *testing.T) {
	t.Parallel()
	_, err := continuity.OpenStoreContext(nil, &config.Config{Continuity: config.ContinuityConfig{ //nolint:staticcheck // contract: nil ctx
		InMemory: true,
		Store:    "memory",
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenStoreContext_nilConfig(t *testing.T) {
	t.Parallel()
	_, err := continuity.OpenStoreContext(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil config") {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenStore_nilConfig(t *testing.T) {
	t.Parallel()
	_, err := continuity.OpenStore(nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil config") {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenStore_sqlite_requiresPath(t *testing.T) {
	t.Parallel()
	_, err := continuity.OpenStore(&config.Config{Continuity: config.ContinuityConfig{
		InMemory: false,
		Store:    "sqlite",
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "sqlite_path") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStore_inMemoryFalse(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStore(config.ContinuityConfig{InMemory: false})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "in_memory=false") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStore_invalidTTL(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStore(config.ContinuityConfig{
		InMemory: true,
		TTL:      "not-a-duration",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "continuity.ttl") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStore_negativeTTL(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStore(config.ContinuityConfig{
		InMemory: true,
		TTL:      "-1h",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOpenStore_memory_rejectsNegativeMaxLegs(t *testing.T) {
	t.Parallel()
	_, err := continuity.OpenStore(&config.Config{Continuity: config.ContinuityConfig{
		InMemory: true,
		MaxLegs:  -1,
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "max_legs") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStore_happyPath(t *testing.T) {
	t.Parallel()
	cfg := config.ContinuityConfig{
		InMemory: true,
		TTL:      "24h",
		MaxLegs:  42,
	}
	store, err := continuity.NewMemoryStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
	store2, err := continuity.OpenStore(&config.Config{Continuity: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if store2 == nil {
		t.Fatal("nil store from OpenStore")
	}
}

func TestOpenStore_postgres_doesNotLeakPasswordInError(t *testing.T) {
	t.Parallel()
	const secret = "SECRET_PASSWORD_XY"
	dsn := "postgres://user:" + secret + "@127.0.0.1:1/nosuchdb?sslmode=disable"
	_, err := continuity.OpenStore(&config.Config{
		Continuity: config.ContinuityConfig{
			InMemory:    false,
			Store:       "postgres",
			PostgresDSN: dsn,
		},
		Database: config.DatabaseConfig{MaxOpenConns: 2},
	})
	if err == nil {
		t.Fatal("expected error for unreachable postgres")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("error leaked password: %s", msg)
	}
	if !strings.Contains(msg, "continuity") {
		t.Fatalf("want continuity context in error: %s", msg)
	}
}

func TestNewMemoryStore_zeroContinuityConfig(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStore(config.ContinuityConfig{})
	if err == nil {
		t.Fatal("expected error when InMemory is false (Go zero value)")
	}
}
