// Package pluginreg holds registry-driven factories for the standard distribution (backends, frontends, features).
package pluginreg

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"gopkg.in/yaml.v3"
)

// FrontendMount is the stable SDK-named contract (see pkg/lipsdk).
type FrontendMount = lipsdk.FrontendMount

// backendFactory builds a backend from opaque per-plugin YAML and the composition-root HTTP client.
type backendFactory func(n yaml.Node, upstreamHTTP *http.Client) (lipsdk.BackendBuild, error)

// FeatureFactory builds hook chains (and optional lifecycles) from opaque plugin YAML.
type FeatureFactory func(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error)

// Registry holds bundled plugin factories for one composition root. The zero value is an
// empty registry: lookups behave like an empty bundle, and the first Register* call lazily
// allocates internal maps (same observable behavior as [NewRegistry]). Use [NewRegistry] and
// [InstallStandardBundleOn] to assemble isolated bundles for each composition root.
type Registry struct {
	mu        sync.RWMutex
	backends  map[string]backendFactory
	frontends map[string]FrontendMount
	features  map[string]FeatureFactory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		backends:  map[string]backendFactory{},
		frontends: map[string]FrontendMount{},
		features:  map[string]FeatureFactory{},
	}
}

func (r *Registry) ensureMaps() {
	if r.backends == nil {
		r.backends = map[string]backendFactory{}
	}
	if r.frontends == nil {
		r.frontends = map[string]FrontendMount{}
	}
	if r.features == nil {
		r.features = map[string]FeatureFactory{}
	}
}

// RegisterBackend records a backend factory on r.
// Duplicate ids return an error: the standard bundle must register each id exactly once.
func (r *Registry) RegisterBackend(id string, fn backendFactory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterBackend: empty id")
	}
	if _, exists := r.backends[id]; exists {
		return fmt.Errorf("pluginreg: duplicate backend registration: %s", id)
	}
	r.backends[id] = fn
	return nil
}

// RegisterFrontend records a frontend mount on r.
func (r *Registry) RegisterFrontend(id string, fn FrontendMount) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterFrontend: empty id")
	}
	if _, exists := r.frontends[id]; exists {
		return fmt.Errorf("pluginreg: duplicate frontend registration: %s", id)
	}
	r.frontends[id] = fn
	return nil
}

// RegisterFeature records a feature factory on r.
func (r *Registry) RegisterFeature(id string, fn FeatureFactory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterFeature: empty id")
	}
	if _, exists := r.features[id]; exists {
		return fmt.Errorf("pluginreg: duplicate feature registration: %s", id)
	}
	r.features[id] = fn
	return nil
}

// BuildBackend constructs a backend from r using the factory id (plugin kind).
// upstreamHTTP is the shared outbound client from the composition root; nil is passed through
// to factories, which apply defaults where HTTP is required (e.g. Bedrock, ACP).
func (r *Registry) BuildBackend(factoryID string, n yaml.Node, upstreamHTTP *http.Client) (runtime.Backend, error) {
	factoryID = strings.TrimSpace(factoryID)

	r.mu.RLock()
	fn, ok := r.backends[factoryID]
	r.mu.RUnlock()
	if !ok {
		return runtime.Backend{}, fmt.Errorf("pluginreg: unknown backend plugin %q", factoryID)
	}
	v, err := fn(n, upstreamHTTP)
	if err != nil {
		return runtime.Backend{}, err
	}
	be, ok := v.(runtime.Backend)
	if !ok {
		return runtime.Backend{}, fmt.Errorf("pluginreg: backend %q factory returned %T, want runtime.Backend", factoryID, v)
	}
	return be, nil
}

// MountFrontend registers routes for one enabled frontend plugin on r.
func (r *Registry) MountFrontend(id string, mux *http.ServeMux, n yaml.Node, exec lipsdk.ExecutorView, defaultRoute string, maxRequestBodyBytes int64) error {
	r.mu.RLock()
	fn, ok := r.frontends[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("pluginreg: unknown frontend plugin %q", id)
	}
	return fn(mux, n, exec, defaultRoute, maxRequestBodyBytes)
}

// BuildFeatureHooks merges enabled feature plugins into a hook bus configuration using r.
func (r *Registry) BuildFeatureHooks(registrations []lipsdk.Registration) (hooks.Config, []lipplugin.Lifecycle, error) {
	var out hooks.Config
	var lifes []lipplugin.Lifecycle
	for _, reg := range registrations {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled {
			continue
		}
		factoryKey := reg.RegistryFactoryKey()
		r.mu.RLock()
		fn, ok := r.features[factoryKey]
		r.mu.RUnlock()
		if !ok {
			return hooks.Config{}, nil, fmt.Errorf("pluginreg: unknown enabled feature plugin %q", factoryKey)
		}
		h, lc, err := fn(reg.Config.Node)
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
