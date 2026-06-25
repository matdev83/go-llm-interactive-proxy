package pluginreg

import (
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestRegistriesDoNotShareFactories(t *testing.T) {
	t.Parallel()
	a := NewRegistry()
	b := NewRegistry()
	id := "isolated-backend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := a.RegisterBackend(id, func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.BuildBackend(id, yaml.Node{}, nil, BackendFactoryDeps{}); err == nil {
		t.Fatal("expected empty registry b to miss factory registered only on a")
	}
}

func TestDuplicateRegistrationScopedPerRegistry(t *testing.T) {
	t.Parallel()
	r1 := NewRegistry()
	r2 := NewRegistry()
	id := "dup-scope-" + strings.ReplaceAll(t.Name(), "/", "-")
	fn := func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}
	if err := r1.RegisterBackend(id, fn); err != nil {
		t.Fatal(err)
	}
	if err := r1.RegisterBackend(id, fn); err == nil {
		t.Fatal("expected duplicate error on same registry")
	}
	if err := r2.RegisterBackend(id, fn); err != nil {
		t.Fatalf("same id on independent registry: %v", err)
	}
}
