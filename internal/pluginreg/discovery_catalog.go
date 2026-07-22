package pluginreg

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrDiscoveryFrozen is returned when reload tries to rescan trusted plugin
// directories or install connector artifacts after the process-owned discovery
// catalog was fixed at startup (req 7.3, 8.7).
var ErrDiscoveryFrozen = errors.New("pluginreg: discovery/trust catalog is startup-fixed")

// BackendReloadPolicy describes whether a factory kind may own concurrent
// candidate/active instance handles under runtime reload (req 8.8).
type BackendReloadPolicy struct {
	// AllowsCandidateOverlap is true when old and new configured instance
	// handles may coexist. False marks shared-process exclusive kinds that
	// require restart_required before publication when a live instance exists.
	AllowsCandidateOverlap bool
}

// RegisterDiscoveredBackend records an executable/factory kind discovered at
// process startup. After FreezeDiscovery, no further discovery registration,
// directory rescan, or artifact installation is permitted.
func (r *Registry) RegisterDiscoveredBackend(
	id string,
	fn BackendFactory,
	profile BackendSecurityProfile,
	policy BackendReloadPolicy,
) error {
	if r == nil {
		return fmt.Errorf("pluginreg: nil registry")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.discoveryFrozen {
		return fmt.Errorf("%w: register discovered %q", ErrDiscoveryFrozen, strings.TrimSpace(id))
	}
	r.ensureMaps()
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterDiscoveredBackend: empty id")
	}
	if fn == nil {
		return fmt.Errorf("pluginreg: RegisterDiscoveredBackend: nil factory")
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
	r.backends[id] = fn
	r.backendProfiles[id] = profile
	r.reloadPolicies[id] = policy
	r.discovered[id] = struct{}{}
	return nil
}

// FreezeDiscovery marks the discovery/trust catalog as process-owned and
// startup-fixed. Idempotent.
func (r *Registry) FreezeDiscovery() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	r.discoveryFrozen = true
}

// DiscoveryFrozen reports whether the discovery/trust catalog is startup-fixed.
func (r *Registry) DiscoveryFrozen() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.discoveryFrozen
}

// RescanTrustedDirectories is the reload-forbidden discovery entrypoint.
// After FreezeDiscovery it records the attempt and returns ErrDiscoveryFrozen
// without scanning paths or mutating the catalog (req 8.7).
func (r *Registry) RescanTrustedDirectories(paths []string) error {
	_ = paths
	if r == nil {
		return fmt.Errorf("pluginreg: nil registry")
	}
	r.rescanAttempts.Add(1)
	r.mu.RLock()
	frozen := r.discoveryFrozen
	r.mu.RUnlock()
	if frozen {
		return ErrDiscoveryFrozen
	}
	// Pre-freeze rescans are no-ops in the initial implementation: discovery is
	// explicit registration only. Callers must RegisterDiscoveredBackend then FreezeDiscovery.
	return nil
}

// InstallConnectorArtifact is the reload-forbidden install entrypoint.
// After FreezeDiscovery it records the attempt and returns ErrDiscoveryFrozen
// without downloading or executing artifacts (req 8.7).
func (r *Registry) InstallConnectorArtifact(path string) error {
	_ = path
	if r == nil {
		return fmt.Errorf("pluginreg: nil registry")
	}
	r.installAttempts.Add(1)
	r.mu.RLock()
	frozen := r.discoveryFrozen
	r.mu.RUnlock()
	if frozen {
		return ErrDiscoveryFrozen
	}
	return fmt.Errorf("pluginreg: connector installation is not supported in-process")
}

// RescanAttempts returns how many times RescanTrustedDirectories was invoked.
func (r *Registry) RescanAttempts() int64 {
	if r == nil {
		return 0
	}
	return r.rescanAttempts.Load()
}

// InstallAttempts returns how many times InstallConnectorArtifact was invoked.
func (r *Registry) InstallAttempts() int64 {
	if r == nil {
		return 0
	}
	return r.installAttempts.Load()
}

// BackendReloadPolicy returns the reload/overlap policy for a factory kind.
// Built-in RegisterBackend kinds default to AllowsCandidateOverlap=true.
func (r *Registry) BackendReloadPolicy(factoryID string) (BackendReloadPolicy, bool) {
	if r == nil {
		return BackendReloadPolicy{}, false
	}
	factoryID = strings.TrimSpace(factoryID)
	if factoryID == "" {
		return BackendReloadPolicy{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if pol, ok := r.reloadPolicies[factoryID]; ok {
		return pol, true
	}
	if _, ok := r.backends[factoryID]; ok {
		return BackendReloadPolicy{AllowsCandidateOverlap: true}, true
	}
	return BackendReloadPolicy{}, false
}

// IsDiscoveredBackend reports whether factoryID was registered via discovery.
func (r *Registry) IsDiscoveredBackend(factoryID string) bool {
	if r == nil {
		return false
	}
	factoryID = strings.TrimSpace(factoryID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.discovered[factoryID]
	return ok
}

// DiscoveredBackendIDs returns discovered factory kind ids in sorted order.
func (r *Registry) DiscoveredBackendIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.discovered))
	for id := range r.discovered {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
