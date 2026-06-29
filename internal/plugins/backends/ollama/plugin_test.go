package ollama

import (
	"testing"
)

func TestNew(t *testing.T) {
	t.Parallel()

	cfg := Config{
		BaseURL: "http://localhost:11434",
	}

	be := New(cfg)

	if be.Open == nil {
		t.Fatal("expected Open to be populated")
	}

	if be.ResolveCaps == nil {
		t.Fatal("expected ResolveCaps to be populated")
	}

	if be.ResolveTransportCaps == nil {
		t.Fatal("expected ResolveTransportCaps to be populated")
	}
}

func TestNewCloud(t *testing.T) {
	t.Parallel()

	cfg := Config{
		BaseURL: "https://api.ollama.com",
	}

	be := NewCloud(cfg)

	if be.Open == nil {
		t.Fatal("expected Open to be populated")
	}

	if be.ResolveCaps == nil {
		t.Fatal("expected ResolveCaps to be populated")
	}

	if be.ResolveTransportCaps == nil {
		t.Fatal("expected ResolveTransportCaps to be populated")
	}
}
