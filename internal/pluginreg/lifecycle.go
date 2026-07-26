package pluginreg

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"gopkg.in/yaml.v3"
)

// BackendBuildResult is a composition-owned backend construction outcome.
// Cleanup is optional and owned by runtimebundle assembly, not by core Backend.
type BackendBuildResult struct {
	Backend execbackend.Backend
	Cleanup func() error
}

// LifecycleBackendFactory builds a backend with optional composition-owned cleanup.
// instanceID is the configured backend row id (routing/executor key); discovered
// host-backed factories use it for Activate/Configure. Static factories may ignore it.
type LifecycleBackendFactory func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps BackendFactoryDeps) (BackendBuildResult, error)

// RegisterLifecycleBackend records a lifecycle-aware builtin backend factory.
func (r *Registry) RegisterLifecycleBackend(id string, fn LifecycleBackendFactory) error {
	return r.RegisterLifecycleBackendWithProfile(id, fn, BackendSecurityProfile{CredentialMode: CredentialUnknown})
}

// RegisterLifecycleBackendWithProfile records a lifecycle-aware builtin factory with security profile.
func (r *Registry) RegisterLifecycleBackendWithProfile(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile) error {
	return r.registerLifecycleBackendWithSource(id, fn, profile, BackendSourceBuiltin)
}

// RegisterDiscoveredLifecycleBackendWithProfile records a lifecycle-aware factory
// installed from a trusted discovered artifact. Inspect treats these as non-builtins.
func (r *Registry) RegisterDiscoveredLifecycleBackendWithProfile(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile) error {
	return r.registerLifecycleBackendWithSource(id, fn, profile, BackendSourceDiscovered)
}

func (r *Registry) registerLifecycleBackendWithSource(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile, source BackendRegistrationSource) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterLifecycleBackend: empty id")
	}
	if fn == nil {
		return fmt.Errorf("pluginreg: RegisterLifecycleBackend: nil factory")
	}
	if _, exists := r.backends[id]; exists {
		return fmt.Errorf("pluginreg: duplicate backend registration: %s", id)
	}
	if profile.CredentialMode == "" {
		profile.CredentialMode = CredentialUnknown
	}
	if profile.AccessScope == "" {
		profile.AccessScope = BackendAccessAny
	}
	if source == "" {
		source = BackendSourceBuiltin
	}
	r.lifecycleBackends[id] = fn
	// Plain BuildBackend must not invent an empty instance id for lifecycle
	// factories (discovered host-backed kinds fail closed without a row id).
	r.backends[id] = func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, fmt.Errorf("pluginreg: backend %q requires BuildBackendWithLifecycle (instance id required)", id)
	}
	r.backendProfiles[id] = profile
	r.backendSources[id] = source
	return nil
}

// BuildBackendWithLifecycle constructs a backend and optional cleanup for assembly.
// instanceID is the configured backend instance id for the enabled row.
func (r *Registry) BuildBackendWithLifecycle(
	factoryID string,
	instanceID string,
	n yaml.Node,
	upstreamHTTP *http.Client,
	deps BackendFactoryDeps,
) (BackendBuildResult, error) {
	factoryID = strings.TrimSpace(factoryID)
	instanceID = strings.TrimSpace(instanceID)
	r.mu.RLock()
	lf, hasLife := r.lifecycleBackends[factoryID]
	fn, ok := r.backends[factoryID]
	r.mu.RUnlock()
	if !ok {
		return BackendBuildResult{}, fmt.Errorf("pluginreg: unknown backend plugin %q", factoryID)
	}
	if hasLife {
		return lf(instanceID, n, upstreamHTTP, deps)
	}
	be, err := fn(n, upstreamHTTP, deps)
	if err != nil {
		return BackendBuildResult{}, err
	}
	return BackendBuildResult{Backend: be}, nil
}
