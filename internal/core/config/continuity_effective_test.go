package config_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestEffectiveContinuityStore_inMemoryForcesMemory(t *testing.T) {
	t.Parallel()
	s := config.EffectiveContinuityStore(config.ContinuityConfig{
		InMemory: true,
		Store:    "postgres",
	})
	if s != "memory" {
		t.Fatalf("got %q want memory", s)
	}
}

func TestEffectiveContinuityStore_emptyDefaultsMemory(t *testing.T) {
	t.Parallel()
	s := config.EffectiveContinuityStore(config.ContinuityConfig{InMemory: false})
	if s != "memory" {
		t.Fatalf("got %q want memory", s)
	}
}

func TestEffectiveContinuityStore_postgres(t *testing.T) {
	t.Parallel()
	s := config.EffectiveContinuityStore(config.ContinuityConfig{
		InMemory: false,
		Store:    "postgres",
	})
	if s != "postgres" {
		t.Fatalf("got %q want postgres", s)
	}
}
