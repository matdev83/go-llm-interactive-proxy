package pluginreg

import (
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"gopkg.in/yaml.v3"
)

func TestRegistry_zeroValueRegisterBackend(t *testing.T) {
	t.Parallel()
	var r Registry
	id := "zero-value-backend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterBackend(id, func(yaml.Node, *http.Client) (lipsdk.BackendBuild, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterBackend(id, func(yaml.Node, *http.Client) (lipsdk.BackendBuild, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistry_zeroValueRegisterFrontend(t *testing.T) {
	t.Parallel()
	var r Registry
	id := "zero-value-frontend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterFrontend(id, func(*http.ServeMux, yaml.Node, lipsdk.ExecutorView, string, int64) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistry_zeroValueRegisterFeature(t *testing.T) {
	t.Parallel()
	var r Registry
	id := "zero-value-feature-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterFeature(id, func(yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
		return hooks.Config{}, nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}
