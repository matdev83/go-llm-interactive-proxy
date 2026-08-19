package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/discovery"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	sdkmanifest "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
	"gopkg.in/yaml.v3"
)

// ExecuteSession is the host-facing configured plugin session (adapter seam).
type ExecuteSession = adapter.ExecuteSession

// ValidatedExport is one catalog-approved factory export ready for generic registration.
type ValidatedExport struct {
	Kind        string
	Profile     pluginreg.BackendSecurityProfile
	ExecProfile lipsdk.BackendExecutionProfile
	Artifact    *trust.VerifiedArtifact
	Model       processhost.ProcessModel
	Sharing     processhost.SharingOptions
}

// DialSessionRequest is the peer-authenticated configure input for a discovered factory.
type DialSessionRequest struct {
	Conn        net.Conn
	Peer        processhost.PeerIdentity
	Generation  uint64
	Secrets     backendplugin.SecretBundle
	ConfigYAML  []byte
	InstanceID  string
	FactoryKind string
	Policy      backendplugin.RuntimePolicy
}

// DialSessionFunc establishes a configured ExecuteSession after peer auth.
// Tests inject an in-process FakeService bridge; production uses adapter.DialConfiguredSession.
type DialSessionFunc func(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error)

// DiscoveredInstallOptions configures InstallDiscoveredExports.
type DiscoveredInstallOptions struct {
	DialSession   DialSessionFunc
	RuntimePolicy backendplugin.RuntimePolicy
}

// DiscoveredPluginInstall is an optional BuildOptions payload that installs
// validated catalog exports before backend construction.
type DiscoveredPluginInstall struct {
	Host    *processhost.Host
	Exports []ValidatedExport
	Options DiscoveredInstallOptions
	// Trusted holds every verified artifact bound to the shared staging root.
	// Exports reference the same pointers; the ownership holder must close all
	// of them before removing staging (Windows locks staged executables).
	Trusted []*trust.VerifiedArtifact
}

func installDiscoveredPlugins(opts *BuildOptions) error {
	if opts == nil || opts.DiscoveredPlugins == nil {
		return nil
	}
	if err := InstallDiscoveredExports(
		opts.PluginRegistry,
		opts.DiscoveredPlugins.Host,
		opts.DiscoveredPlugins.Exports,
		opts.DiscoveredPlugins.Options,
	); err != nil {
		return fmt.Errorf("runtimebundle: discovered plugins: %w", err)
	}
	return nil
}

// InstallDiscoveredExports registers each validated export as a host-backed
// lifecycle factory. Duplicate kinds and collisions with already-registered
// builtins fail closed. There is no connector-specific switch.
func InstallDiscoveredExports(
	reg *pluginreg.Registry,
	host *processhost.Host,
	exports []ValidatedExport,
	opt DiscoveredInstallOptions,
) error {
	return installDiscoveredExportsWithPool(reg, host, exports, opt, nil)
}

// installDiscoveredExportsWithPool is the private composition seam used by
// discovered-install preparation. The pool is captured lexically by each
// factory closure; public callers retain the established isolated path.
func installDiscoveredExportsWithPool(
	reg *pluginreg.Registry,
	host *processhost.Host,
	exports []ValidatedExport,
	opt DiscoveredInstallOptions,
	resourcePool *backendResourcePool,
) error {
	if reg == nil {
		return fmt.Errorf("runtimebundle: InstallDiscoveredExports: nil registry")
	}
	if host == nil {
		return fmt.Errorf("runtimebundle: InstallDiscoveredExports: nil process host")
	}
	dial := opt.DialSession
	if dial == nil {
		dial = defaultDialSession
	}
	policy := opt.RuntimePolicy
	policy.DisableTransportRetries = true

	// activationSeq mints unique per-instance Host Activate handles (install-owned).
	activationSeq := new(atomic.Uint64)

	seen := make(map[string]struct{}, len(exports))
	for _, exp := range exports {
		kind := strings.TrimSpace(exp.Kind)
		if kind == "" {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: empty export kind")
		}
		if _, dup := seen[kind]; dup {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: duplicate export kind %q", kind)
		}
		seen[kind] = struct{}{}
		if reg.HasBackend(kind) {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: duplicate backend registration: %s", kind)
		}
		if exp.Artifact == nil {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: kind %q: nil verified artifact", kind)
		}
		if exp.Model != processhost.ProcessModelPerInstance && exp.Model != processhost.ProcessModelSharedArtifact {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: kind %q: invalid process model %q", kind, exp.Model)
		}

		export := exp
		factoryKind := kind
		var fn pluginreg.LifecycleBackendFactory
		if export.Model == processhost.ProcessModelPerInstance {
			// Only overlap-safe per-instance factories capture the private pool;
			// shared-artifact factories retain their existing isolated path.
			pool := resourcePool
			fn = func(instanceID string, n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
				return buildDiscoveredBackend(host, export, factoryKind, instanceID, n, dial, policy, activationSeq, pool)
			}
		} else {
			fn = func(instanceID string, n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
				return buildDiscoveredBackend(host, export, factoryKind, instanceID, n, dial, policy, activationSeq, nil)
			}
		}
		if err := reg.RegisterDiscoveredLifecycleBackendWithProfiles(kind, fn, export.Profile, export.ExecProfile); err != nil {
			return err
		}
		// Shared-process exclusive kinds cannot overlap candidate/active handles.
		overlap := export.Model != processhost.ProcessModelSharedArtifact
		if err := reg.SetBackendReloadPolicy(kind, pluginreg.BackendReloadPolicy{
			AllowsCandidateOverlap: overlap,
		}); err != nil {
			return err
		}
	}
	return nil
}

func buildDiscoveredBackend(
	host *processhost.Host,
	export ValidatedExport,
	factoryKind, instanceID string,
	n yaml.Node,
	dial DialSessionFunc,
	policy backendplugin.RuntimePolicy,
	activationSeq *atomic.Uint64,
	resourcePool *backendResourcePool,
) (pluginreg.BackendBuildResult, error) {
	input, err := prepareDiscoveredPhysicalInput(export, factoryKind, instanceID, n, policy)
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	if resourcePool == nil {
		backend, cleanup, err := buildDiscoveredPhysical(context.Background(), host, export, input, dial, activationSeq, func(generation uint64) {
			_ = host.InvalidateProcessGeneration(generation)
		})
		if err != nil {
			return pluginreg.BackendBuildResult{}, err
		}
		return pluginreg.BackendBuildResult{Backend: backend, Cleanup: cleanup}, nil
	}

	identity, shareable, identityErr := physicalIdentity(input)
	if identityErr != nil || !shareable {
		// An incomplete identity is deliberately non-shareable. Preserve the
		// existing generation-local construction path rather than turning an
		// optimization eligibility failure into a new runtime error.
		backend, cleanup, err := buildDiscoveredPhysical(context.Background(), host, export, input, dial, activationSeq, func(generation uint64) {
			_ = host.InvalidateProcessGeneration(generation)
		})
		if err != nil {
			return pluginreg.BackendBuildResult{}, err
		}
		return pluginreg.BackendBuildResult{Backend: backend, Cleanup: cleanup}, nil
	}

	return resourcePool.Acquire(context.Background(), identity, func(buildCtx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		return buildDiscoveredPhysical(buildCtx, host, export, input, dial, activationSeq, func(generation uint64) {
			resourcePool.Invalidate(identity, incarnation)
			_ = host.InvalidateProcessGeneration(generation)
		})
	})
}

// prepareDiscoveredPhysicalInput freezes the effective input used for both
// identity and physical construction. Provider-specific parsing remains in the
// connector; this boundary only preserves the opaque Configure bytes and the
// host-owned policy/artifact/process facts.
func prepareDiscoveredPhysicalInput(
	export ValidatedExport,
	factoryKind, instanceID string,
	n yaml.Node,
	policy backendplugin.RuntimePolicy,
) (backendResourcePhysicalInput, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return backendResourcePhysicalInput{}, fmt.Errorf("runtimebundle: discovered backend %q: empty instance id", factoryKind)
	}
	raw, err := encodeOpaqueYAML(n)
	if err != nil {
		return backendResourcePhysicalInput{}, fmt.Errorf("runtimebundle: discovered backend %q: encode config: %w", factoryKind, err)
	}
	artifactDigest := ""
	if export.Artifact != nil {
		artifactDigest = export.Artifact.DigestHex
	}
	return backendResourcePhysicalInput{
		InstanceID:     instanceID,
		FactoryKind:    factoryKind,
		ArtifactDigest: artifactDigest,
		ProcessModel:   export.Model,
		ConfigureYAML:  raw,
		RuntimePolicy:  policy,
	}, nil
}

// buildDiscoveredPhysical performs one new host activation and adapter build.
// The caller supplies the context so pooled construction is owned by the pool,
// while the public isolated path retains its existing background lifetime.
func buildDiscoveredPhysical(
	ctx context.Context,
	host *processhost.Host,
	export ValidatedExport,
	input backendResourcePhysicalInput,
	dial DialSessionFunc,
	activationSeq *atomic.Uint64,
	invalidateGeneration func(uint64),
) (execbackend.Backend, func() error, error) {
	// Per-instance overlap across generations must not collide on Host's
	// InstanceID map: mint a unique activation handle while dialing with the
	// logical configured instance id.
	hostInstanceID := input.InstanceID
	if export.Model == processhost.ProcessModelPerInstance {
		hostInstanceID = fmt.Sprintf("%s#%d", input.InstanceID, activationSeq.Add(1))
	}

	var session ExecuteSession
	var profile backendplugin.ResolvedProfile
	act, err := host.Activate(ctx, processhost.ActivateRequest{
		InstanceID:  hostInstanceID,
		Artifact:    export.Artifact,
		Model:       export.Model,
		Sharing:     export.Sharing,
		FactoryKind: input.FactoryKind,
		ConfigYAML:  input.ConfigureYAML,
		Secrets:     input.Secrets,
		Policy:      input.RuntimePolicy,
		DialAndConfigure: func(ctx context.Context, conn net.Conn, peer processhost.PeerIdentity, generation uint64, secrets backendplugin.SecretBundle, configYAML []byte) error {
			sess, prof, err := dial(ctx, DialSessionRequest{
				Conn:        conn,
				Peer:        peer,
				Generation:  generation,
				Secrets:     secrets,
				ConfigYAML:  configYAML,
				InstanceID:  input.InstanceID,
				FactoryKind: input.FactoryKind,
				Policy:      input.RuntimePolicy,
			})
			if err != nil {
				return err
			}
			session = sess
			profile = prof
			return nil
		},
	})
	if err != nil {
		return execbackend.Backend{}, nil, fmt.Errorf("runtimebundle: discovered backend %q instance %q: activate: %w", input.FactoryKind, input.InstanceID, err)
	}
	if session == nil {
		_ = act.Cleanup()
		return execbackend.Backend{}, nil, fmt.Errorf("runtimebundle: discovered backend %q instance %q: nil session after configure", input.FactoryKind, input.InstanceID)
	}

	generation := act.Generation
	prefixes := append([]string(nil), profile.RoutePrefixes...)
	if len(prefixes) == 0 {
		prefixes = []string{input.FactoryKind}
	}
	neg := backendplugin.Negotiation{Compatible: true}
	if ns, ok := session.(adapter.NegotiatedSession); ok {
		neg = ns.Negotiation()
	}
	br := adapter.Build(session, profile, adapter.Options{
		InstanceID:    input.InstanceID,
		RoutePrefixes: prefixes,
		Negotiation:   neg,
		InvalidateGeneration: func() {
			if invalidateGeneration != nil {
				invalidateGeneration(generation)
				return
			}
			_ = host.InvalidateProcessGeneration(generation)
		},
	})
	cleanup := func() error {
		var out error
		if br != nil {
			out = errors.Join(out, br.Cleanup())
		}
		if act.Cleanup != nil {
			out = errors.Join(out, act.Cleanup())
		}
		return out
	}
	if br == nil {
		_ = cleanup()
		return execbackend.Backend{}, nil, fmt.Errorf("runtimebundle: discovered backend %q instance %q: nil adapter build", input.FactoryKind, input.InstanceID)
	}
	return br.Backend, cleanup, nil
}

func defaultDialSession(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
	return adapter.DialConfiguredSession(ctx, req.Conn, req.InstanceID, req.FactoryKind, req.ConfigYAML, req.Secrets, req.Policy)
}

func encodeOpaqueYAML(n yaml.Node) ([]byte, error) {
	if n.Kind == 0 {
		return nil, nil
	}
	return yaml.Marshal(&n)
}

// CollectInstallableExports converts a catalog snapshot into ValidatedExport values
// for kinds in StateDiscovered with verified artifacts and declared process models.
func CollectInstallableExports(
	snap catalog.Snapshot,
	trustBySafe map[string]trust.VerifyResult,
	discovered []discovery.Descriptor,
) ([]ValidatedExport, error) {
	bySafe := make(map[string]discovery.Descriptor, len(discovered))
	for _, d := range discovered {
		bySafe[d.SafeID] = d
	}
	var out []ValidatedExport
	for _, e := range snap.Entries {
		if (e.State != catalog.StateDiscovered && e.State != catalog.StateConfigured) || strings.TrimSpace(e.ExportKind) == "" {
			continue
		}
		tr, ok := trustBySafe[e.SafeID]
		if !ok || tr.Reason != trust.ReasonOK || tr.Artifact == nil {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: missing verified artifact", e.ExportKind)
		}
		d, ok := bySafe[e.SafeID]
		if !ok {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: missing discovery descriptor", e.ExportKind)
		}
		exp, ok := findManifestExport(d.Manifest, e.ExportKind)
		if !ok {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: missing manifest export", e.ExportKind)
		}
		sharing := processhost.SharingOptions{}
		if exp.ProcessSharing == backendplugin.ProcessSharingSharedArtifact {
			sharing = processhost.SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true}
		}
		model, err := processhost.ProcessModelFromSharing(exp.ProcessSharing, sharing)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: %w", e.ExportKind, err)
		}
		profile, err := securityProfileFromExport(exp)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: %w", e.ExportKind, err)
		}
		out = append(out, ValidatedExport{
			Kind:        e.ExportKind,
			Profile:     profile,
			ExecProfile: lipsdk.BackendExecutionProfile{Class: exp.ExecutionClass},
			Artifact:    tr.Artifact,
			Model:       model,
			Sharing:     sharing,
		})
	}
	return out, nil
}

func findManifestExport(m sdkmanifest.Manifest, kind string) (sdkmanifest.Export, bool) {
	for _, e := range m.Exports {
		if e.Kind == kind {
			return e, true
		}
	}
	return sdkmanifest.Export{}, false
}

func securityProfileFromExport(exp sdkmanifest.Export) (pluginreg.BackendSecurityProfile, error) {
	if err := backendplugin.ValidateCredentialMode(exp.CredentialMode); err != nil {
		return pluginreg.BackendSecurityProfile{}, err
	}
	if err := backendplugin.ValidateAccessScope(exp.AccessScope); err != nil {
		return pluginreg.BackendSecurityProfile{}, err
	}
	return pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.BackendCredentialMode(exp.CredentialMode),
		AccessScope:    pluginreg.BackendAccessScope(exp.AccessScope),
	}, nil
}

// UnionFactoryKinds returns the sorted-stable union of essential and discovered kinds.
func UnionFactoryKinds(essential []string, discovered []ValidatedExport) []string {
	seen := make(map[string]struct{}, len(essential)+len(discovered))
	out := make([]string, 0, len(essential)+len(discovered))
	for _, k := range essential {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	for _, e := range discovered {
		k := strings.TrimSpace(e.Kind)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}
