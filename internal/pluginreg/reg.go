// Package pluginreg holds registry-driven factories for the standard distribution (backends, frontends, features).
package pluginreg

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"gopkg.in/yaml.v3"
)

// BackendFactory and FrontendMount are stable SDK-named contracts (see pkg/lipsdk).
type (
	BackendFactory = lipsdk.BackendFactory
	FrontendMount  = lipsdk.FrontendMount
)

// FeatureFactory builds hook chains (and optional lifecycles) from opaque plugin YAML.
type FeatureFactory func(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error)

var (
	mu        sync.RWMutex
	backends  = map[string]BackendFactory{}
	frontends = map[string]FrontendMount{}
	features  = map[string]FeatureFactory{}
)

// RegisterBackend records a backend factory for the given plugin id (e.g. openai-responses).
// Duplicate ids panic: the standard bundle must register each id exactly once.
func RegisterBackend(id string, fn BackendFactory) {
	mu.Lock()
	defer mu.Unlock()
	if id == "" {
		panic("pluginreg: RegisterBackend: empty id")
	}
	if _, exists := backends[id]; exists {
		panic("pluginreg: duplicate backend registration: " + id)
	}
	backends[id] = fn
}

// RegisterFrontend records a frontend mount for the given plugin id.
func RegisterFrontend(id string, fn FrontendMount) {
	mu.Lock()
	defer mu.Unlock()
	if id == "" {
		panic("pluginreg: RegisterFrontend: empty id")
	}
	if _, exists := frontends[id]; exists {
		panic("pluginreg: duplicate frontend registration: " + id)
	}
	frontends[id] = fn
}

// RegisterFeature records a feature factory for the given plugin id.
func RegisterFeature(id string, fn FeatureFactory) {
	mu.Lock()
	defer mu.Unlock()
	if id == "" {
		panic("pluginreg: RegisterFeature: empty id")
	}
	if _, exists := features[id]; exists {
		panic("pluginreg: duplicate feature registration: " + id)
	}
	features[id] = fn
}

// BuildBackend constructs a backend from registry.
func BuildBackend(id string, n yaml.Node) (runtime.Backend, error) {
	mu.RLock()
	fn, ok := backends[id]
	mu.RUnlock()
	if !ok {
		return runtime.Backend{}, fmt.Errorf("pluginreg: unknown backend plugin %q", id)
	}
	v, err := fn(n)
	if err != nil {
		return runtime.Backend{}, err
	}
	be, ok := v.(runtime.Backend)
	if !ok {
		return runtime.Backend{}, fmt.Errorf("pluginreg: backend %q factory returned %T, want runtime.Backend", id, v)
	}
	return be, nil
}

// MountFrontend registers routes for one enabled frontend plugin.
func MountFrontend(id string, mux *http.ServeMux, n yaml.Node, exec lipsdk.ExecutorView, defaultRoute string, maxRequestBodyBytes int64) error {
	mu.RLock()
	fn, ok := frontends[id]
	mu.RUnlock()
	if !ok {
		return fmt.Errorf("pluginreg: unknown frontend plugin %q", id)
	}
	return fn(mux, n, exec, defaultRoute, maxRequestBodyBytes)
}

// BuildFeatureHooks merges enabled feature plugins into a hook bus configuration.
func BuildFeatureHooks(registrations []lipsdk.Registration) (hooks.Config, []lipplugin.Lifecycle, error) {
	var out hooks.Config
	var lifes []lipplugin.Lifecycle
	for _, r := range registrations {
		if r.Kind != lipsdk.PluginKindFeature || !r.Enabled {
			continue
		}
		mu.RLock()
		fn, ok := features[r.ID]
		mu.RUnlock()
		if !ok {
			return hooks.Config{}, nil, fmt.Errorf("pluginreg: unknown enabled feature plugin %q", r.ID)
		}
		h, lc, err := fn(r.Config.Node)
		if err != nil {
			return hooks.Config{}, nil, err
		}
		out.SubmitHooks = append(out.SubmitHooks, h.SubmitHooks...)
		out.RequestPartHooks = append(out.RequestPartHooks, h.RequestPartHooks...)
		out.ResponsePartHooks = append(out.ResponsePartHooks, h.ResponsePartHooks...)
		out.ToolReactors = append(out.ToolReactors, h.ToolReactors...)
		lifes = append(lifes, lc...)
	}
	return out, lifes, nil
}
