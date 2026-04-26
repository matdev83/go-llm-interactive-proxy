// Package pluginreg holds registry-driven factories for the standard distribution (backends, frontends, features).
package pluginreg

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"gopkg.in/yaml.v3"
)

// FrontendMount is the stable SDK-named contract (see pkg/lipsdk).
type FrontendMount = lipsdk.FrontendMount

// backendFactory builds a backend from opaque per-plugin YAML and the composition-root HTTP client.
type backendFactory func(n yaml.Node, upstreamHTTP *http.Client) (execbackend.Backend, error)

// BackendCredentialMode describes how a backend obtains upstream credentials.
type BackendCredentialMode string

const (
	// CredentialStatic uses operator-configured static credentials such as API keys.
	CredentialStatic BackendCredentialMode = "static"
	// CredentialWorkload uses workload identity from the local runtime environment.
	CredentialWorkload BackendCredentialMode = "workload"
	// CredentialOAuthUser uses user-scoped OAuth credentials and is allowed only for loopback single-user runs.
	CredentialOAuthUser BackendCredentialMode = "oauth_user"
	// CredentialUnknown means the factory did not declare a credential posture.
	CredentialUnknown BackendCredentialMode = "unknown"
)

// BackendSecurityProfile is stable startup metadata for backend credential posture validation.
type BackendSecurityProfile struct {
	CredentialMode BackendCredentialMode
}

// FeatureFactory builds a versioned feature bundle from opaque plugin YAML.
type FeatureFactory func(n yaml.Node) (lipfeature.FeatureBundle, error)

// Registry holds bundled plugin factories for one composition root. The zero value is an
// empty registry: lookups behave like an empty bundle, and the first Register* call lazily
// allocates internal maps (same observable behavior as [NewRegistry]). Use [NewRegistry] and
// [InstallStandardBundleOn] to assemble isolated bundles for each composition root.
type Registry struct {
	mu              sync.RWMutex
	backends        map[string]backendFactory
	backendProfiles map[string]BackendSecurityProfile
	frontends       map[string]FrontendMount
	features        map[string]FeatureFactory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		backends:        map[string]backendFactory{},
		backendProfiles: map[string]BackendSecurityProfile{},
		frontends:       map[string]FrontendMount{},
		features:        map[string]FeatureFactory{},
	}
}

func (r *Registry) ensureMaps() {
	if r.backends == nil {
		r.backends = map[string]backendFactory{}
	}
	if r.backendProfiles == nil {
		r.backendProfiles = map[string]BackendSecurityProfile{}
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
	return r.RegisterBackendWithProfile(id, fn, BackendSecurityProfile{CredentialMode: CredentialUnknown})
}

// RegisterBackendWithProfile records a backend factory with credential posture metadata.
func (r *Registry) RegisterBackendWithProfile(id string, fn backendFactory, profile BackendSecurityProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterBackend: empty id")
	}
	if _, exists := r.backends[id]; exists {
		return fmt.Errorf("pluginreg: duplicate backend registration: %s", id)
	}
	if profile.CredentialMode == "" {
		profile.CredentialMode = CredentialUnknown
	}
	r.backends[id] = fn
	r.backendProfiles[id] = profile
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
// BackendSecurityProfile returns credential posture metadata for a registered backend factory.
func (r *Registry) BackendSecurityProfile(factoryID string) (BackendSecurityProfile, bool) {
	factoryID = strings.TrimSpace(factoryID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.backendProfiles[factoryID]
	return profile, ok
}

func (r *Registry) BuildBackend(factoryID string, n yaml.Node, upstreamHTTP *http.Client) (execbackend.Backend, error) {
	factoryID = strings.TrimSpace(factoryID)

	r.mu.RLock()
	fn, ok := r.backends[factoryID]
	r.mu.RUnlock()
	if !ok {
		return execbackend.Backend{}, fmt.Errorf("pluginreg: unknown backend plugin %q", factoryID)
	}
	return fn(n, upstreamHTTP)
}

// MountFrontend registers routes for one enabled frontend plugin on r.
func (r *Registry) MountFrontend(id string, mux *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
	r.mu.RLock()
	fn, ok := r.frontends[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("pluginreg: unknown frontend plugin %q", id)
	}
	return fn(
		mux,
		opts,
	)
}

// BuildFeatureHooks merges enabled feature plugins into hook configuration; see [Registry.MergeFeatureSurface]
// for session openers and workspace resolvers. [Registry.BuildFeatureBundle] constructs one bundle from YAML.
func (r *Registry) BuildFeatureBundle(factoryKey string, n yaml.Node) (lipfeature.FeatureBundle, error) {
	factoryKey = strings.TrimSpace(factoryKey)
	r.mu.RLock()
	fn, ok := r.features[factoryKey]
	r.mu.RUnlock()
	if !ok {
		return lipfeature.FeatureBundle{}, fmt.Errorf("pluginreg: unknown feature plugin %q", factoryKey)
	}
	b, err := fn(n)
	if err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("pluginreg: feature %q: %w", factoryKey, err)
	}
	if err := b.Validate(); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("pluginreg: feature %q: validate: %w", factoryKey, err)
	}
	return b, nil
}
