package runtimebundle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

func TestOpenContinuityStore_nilContext(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.OpenContinuityStore(nil, &config.Config{Continuity: config.ContinuityConfig{ //nolint:staticcheck // contract: nil ctx
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

func TestOpenContinuityStore_nilConfig(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.OpenContinuityStore(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil config") {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenContinuityStore_sqlite_requiresPath(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{Continuity: config.ContinuityConfig{
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

func TestOpenContinuityStore_memory_rejectsNegativeMaxLegs(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{Continuity: config.ContinuityConfig{
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

func TestOpenContinuityStore_memory_happyPath(t *testing.T) {
	t.Parallel()
	cfg := config.ContinuityConfig{
		InMemory: true,
		TTL:      "24h",
		MaxLegs:  42,
	}
	store, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{Continuity: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
	store2, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{Continuity: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if store2 == nil {
		t.Fatal("nil store from OpenContinuityStore")
	}
}

func TestOpenContinuityStore_postgres_doesNotLeakPasswordInError(t *testing.T) {
	t.Parallel()
	const secret = "SECRET_PASSWORD_XY"
	dsn := "postgres://user:" + secret + "@127.0.0.1:1/nosuchdb?sslmode=disable"
	_, err := runtimebundle.OpenContinuityStore(context.Background(), &config.Config{
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

func TestNewMemoryContinuityStore_inMemoryFalse(t *testing.T) {
	t.Parallel()
	store, err := runtimebundle.NewMemoryContinuityStore(config.ContinuityConfig{InMemory: false})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestNewMemoryContinuityStore_zeroContinuityConfig(t *testing.T) {
	t.Parallel()
	store, err := runtimebundle.NewMemoryContinuityStore(config.ContinuityConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}
