package continuity_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
)

func TestOpenStore_sqlite_requiresPath(t *testing.T) {
	t.Parallel()
	_, err := continuity.OpenStore(config.ContinuityConfig{
		InMemory: true,
		Store:    "sqlite",
	})
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
	_, err := continuity.OpenStore(config.ContinuityConfig{
		InMemory: true,
		MaxLegs:  -1,
	})
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
	store2, err := continuity.OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if store2 == nil {
		t.Fatal("nil store from OpenStore")
	}
}

func TestNewMemoryStore_zeroContinuityConfig(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStore(config.ContinuityConfig{})
	if err == nil {
		t.Fatal("expected error when InMemory is false (Go zero value)")
	}
}
