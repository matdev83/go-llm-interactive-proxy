package pluginreg

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"gopkg.in/yaml.v3"
)

func TestRegistry_zeroValueRegisterBackend(t *testing.T) {
	t.Parallel()
	var r Registry
	id := "zero-value-backend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterBackend(id, func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterBackend(id, func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistry_zeroValueRegisterFrontend(t *testing.T) {
	t.Parallel()
	var r Registry
	id := "zero-value-frontend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterFrontend(id, func(*http.ServeMux, lipsdk.FrontendMountOptions) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistry_zeroValueRegisterFeature(t *testing.T) {
	t.Parallel()
	var r Registry
	id := "zero-value-feature-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterFeature(id, FeatureFactoryFromHooks(func(yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
		return hooks.Config{}, nil, nil
	})); err != nil {
		t.Fatal(err)
	}
}

type noopWireAuthRenderer struct{}

func (noopWireAuthRenderer) RenderAuthError(ctx context.Context, in lipsdk.AuthErrorRenderInput) lipsdk.AuthErrorRenderResult {
	_ = ctx
	_ = in
	return lipsdk.AuthErrorRenderResult{}
}

func TestRegistry_RegisterAuthErrorRenderer_duplicate(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.RegisterAuthErrorRenderer("gemini", noopWireAuthRenderer{}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterAuthErrorRenderer("gemini", noopWireAuthRenderer{}); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistry_RegisterAuthErrorRenderer_nilSkips(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.RegisterAuthErrorRenderer("anthropic", nil); err != nil {
		t.Fatal(err)
	}
	if r.AuthErrorRenderers() != nil {
		t.Fatalf("expected nil map, got %#v", r.AuthErrorRenderers())
	}
}
