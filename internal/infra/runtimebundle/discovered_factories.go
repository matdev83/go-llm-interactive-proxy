package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/discovery"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	sdkmanifest "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
	"gopkg.in/yaml.v3"
)

// ExecuteSession is the host-facing configured plugin session (adapter seam).
type ExecuteSession = adapter.ExecuteSession

// ValidatedExport is one catalog-approved factory export ready for generic registration.
type ValidatedExport struct {
	Kind     string
	Profile  pluginreg.BackendSecurityProfile
	Artifact *trust.VerifiedArtifact
	Model    processhost.ProcessModel
	Sharing  processhost.SharingOptions
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
		fn := func(instanceID string, n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
			return buildDiscoveredBackend(host, export, factoryKind, instanceID, n, dial, policy)
		}
		if err := reg.RegisterDiscoveredLifecycleBackendWithProfile(kind, fn, export.Profile); err != nil {
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
) (pluginreg.BackendBuildResult, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return pluginreg.BackendBuildResult{}, fmt.Errorf("runtimebundle: discovered backend %q: empty instance id", factoryKind)
	}
	raw, err := encodeOpaqueYAML(n)
	if err != nil {
		return pluginreg.BackendBuildResult{}, fmt.Errorf("runtimebundle: discovered backend %q: encode config: %w", factoryKind, err)
	}

	// Per-instance overlap across generations must not collide on Host's
	// InstanceID map: mint a unique activation handle while dialing with the
	// logical configured instance id (req 8.8 / reload candidate coexistence).
	hostInstanceID := instanceID
	if export.Model == processhost.ProcessModelPerInstance {
		hostInstanceID = fmt.Sprintf("%s#%d", instanceID, discoveredActivationSeq.Add(1))
	}

	var session ExecuteSession
	var profile backendplugin.ResolvedProfile
	act, err := host.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID:  hostInstanceID,
		Artifact:    export.Artifact,
		Model:       export.Model,
		Sharing:     export.Sharing,
		FactoryKind: factoryKind,
		ConfigYAML:  raw,
		Policy:      policy,
		DialAndConfigure: func(ctx context.Context, conn net.Conn, peer processhost.PeerIdentity, generation uint64, secrets backendplugin.SecretBundle, configYAML []byte) error {
			sess, prof, err := dial(ctx, DialSessionRequest{
				Conn:        conn,
				Peer:        peer,
				Generation:  generation,
				Secrets:     secrets,
				ConfigYAML:  configYAML,
				InstanceID:  instanceID,
				FactoryKind: factoryKind,
				Policy:      policy,
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
		return pluginreg.BackendBuildResult{}, fmt.Errorf("runtimebundle: discovered backend %q instance %q: activate: %w", factoryKind, instanceID, err)
	}
	if session == nil {
		_ = act.Cleanup()
		return pluginreg.BackendBuildResult{}, fmt.Errorf("runtimebundle: discovered backend %q instance %q: nil session after configure", factoryKind, instanceID)
	}

	generation := act.Generation
	prefixes := append([]string(nil), profile.RoutePrefixes...)
	if len(prefixes) == 0 {
		prefixes = []string{factoryKind}
	}
	br := adapter.Build(session, profile, adapter.Options{
		InstanceID:    instanceID,
		RoutePrefixes: prefixes,
		InvalidateGeneration: func() {
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
	return pluginreg.BackendBuildResult{Backend: br.Backend, Cleanup: cleanup}, nil
}

// discoveredActivationSeq mints unique Host Activate handles for per_instance
// overlap across candidate generations that share a logical instance id.
var discoveredActivationSeq atomic.Uint64

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
			Kind:     e.ExportKind,
			Profile:  profile,
			Artifact: tr.Artifact,
			Model:    model,
			Sharing:  sharing,
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
	if err := exp.CredentialMode.Validate(); err != nil {
		return pluginreg.BackendSecurityProfile{}, err
	}
	if err := exp.AccessScope.Validate(); err != nil {
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
