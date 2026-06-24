package continuity_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
)

func TestNewMemoryStoreFromConfig_inMemoryFalse(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{InMemory: false})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "in_memory=false") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStoreFromConfig_invalidTTL(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{
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

func TestNewMemoryStoreFromConfig_negativeTTL(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{
		InMemory: true,
		TTL:      "-1h",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNewMemoryStoreFromConfig_rejectsNegativeMaxLegs(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{
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

func TestNewMemoryStoreFromConfig_happyPath(t *testing.T) {
	t.Parallel()
	store, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{
		InMemory: true,
		TTL:      "24h",
		MaxLegs:  42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestNewMemoryStoreFromConfig_zeroContinuityConfig(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{})
	if err == nil {
		t.Fatal("expected error when InMemory is false (Go zero value)")
	}
}
