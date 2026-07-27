package runtimebundle_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/discovery"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	sdkmanifest "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
	"gopkg.in/yaml.v3"
)

const syntheticUnknownKind = "synthetic-unknown-kind-xyz"

func TestDiscovered_InstallUnknownKindAndInvokeOpen(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9001},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })

	var gotInstance string
	var gotYAML []byte
	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	export := runtimebundle.ValidatedExport{
		Kind: syntheticUnknownKind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone,
			AccessScope:    pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "digest-synthetic-unknown"},
		Model:    processhost.ProcessModelPerInstance,
	}
	err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{export}, runtimebundle.DiscoveredInstallOptions{
		DialSession: func(ctx context.Context, req runtimebundle.DialSessionRequest) (runtimebundle.ExecuteSession, backendplugin.ResolvedProfile, error) {
			gotInstance = req.InstanceID
			gotYAML = append([]byte(nil), req.ConfigYAML...)
			inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
				InstanceID:    req.InstanceID,
				FactoryKind:   req.FactoryKind,
				ConfigYAML:    req.ConfigYAML,
				Secrets:       req.Secrets,
				Negotiation:   backendplugin.Negotiation{Compatible: true},
				RuntimePolicy: req.Policy,
			})
			if err != nil {
				return nil, backendplugin.ResolvedProfile{}, err
			}
			profile, err := inst.Resolve(ctx, nil)
			if err != nil {
				return nil, backendplugin.ResolvedProfile{}, err
			}
			return inst, profile, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prof, ok := reg.BackendSecurityProfile(syntheticUnknownKind)
	if !ok || prof.CredentialMode != pluginreg.CredentialNone || prof.AccessScope != pluginreg.BackendAccessLocalOnly {
		t.Fatalf("security profile = %+v ok=%v", prof, ok)
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("opaque_key: delivered\n"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(syntheticUnknownKind, "inst-syn-1", cfgNode, http.DefaultClient, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if res.Cleanup != nil {
			_ = res.Cleanup()
		}
	})
	if gotInstance != "inst-syn-1" {
		t.Fatalf("instance id = %q, want inst-syn-1", gotInstance)
	}
	if !strings.Contains(string(gotYAML), "opaque_key: delivered") {
		t.Fatalf("config yaml = %q, want opaque payload", gotYAML)
	}

	stream, err := res.Backend.Open(context.Background(), lipapi.Call{
		ID:         "req-syn",
		Session:    lipapi.SessionRef{ALegID: "aleg"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "inst-syn-1", Model: "fake-model"},
		Key:     "inst-syn-1:fake-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var sawText bool
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventTextDelta {
			sawText = true
		}
	}
	if !sawText {
		t.Fatal("expected text delta from FakeService invoke path")
	}
}

func TestDiscovered_DuplicateKindRejected(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend(syntheticUnknownKind, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9002},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind:     syntheticUnknownKind,
		Profile:  pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		Artifact: &trust.VerifiedArtifact{DigestHex: "dup"},
		Model:    processhost.ProcessModelPerInstance,
	}}, runtimebundle.DiscoveredInstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v, want duplicate rejection", err)
	}
}

func TestDiscovered_DuplicateInstallRejected(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9003},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	exports := []runtimebundle.ValidatedExport{{
		Kind:     "dynamic-kind-a",
		Profile:  pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		Artifact: &trust.VerifiedArtifact{DigestHex: "a1"},
		Model:    processhost.ProcessModelPerInstance,
	}, {
		Kind:     "dynamic-kind-a",
		Profile:  pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		Artifact: &trust.VerifiedArtifact{DigestHex: "a2"},
		Model:    processhost.ProcessModelPerInstance,
	}}
	err := runtimebundle.InstallDiscoveredExports(reg, host, exports, runtimebundle.DiscoveredInstallOptions{
		DialSession: stubDialSession(t),
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		t.Fatalf("err = %v, want duplicate", err)
	}
}

func TestDynamic_DiscoveryCatalogInstallBuildBackendWithLifecycle(t *testing.T) {
	t.Parallel()
	kind := "dynamic-catalog-kind-" + strings.ReplaceAll(t.Name(), "/", "-")
	d := discovery.Descriptor{
		SafeID: "r/dynamic.json",
		Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.golip.dynamic.test", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{
				Kind:           kind,
				CredentialMode: backendplugin.CredentialModeNone,
				AccessScope:    backendplugin.AccessScopeLocalOnly,
				ProcessSharing: backendplugin.ProcessSharingPerInstance,
			}},
		},
	}
	art := &trust.VerifiedArtifact{DigestHex: "catalog-digest"}
	snap, err := catalog.Resolve(catalog.Input{
		Discovered:  []discovery.Descriptor{d},
		TrustBySafe: map[string]trust.VerifyResult{"r/dynamic.json": {Reason: trust.ReasonOK, Artifact: art}},
		HostMajor:   1, HostMinor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	exports, err := runtimebundle.CollectInstallableExports(snap, map[string]trust.VerifyResult{
		"r/dynamic.json": {Reason: trust.ReasonOK, Artifact: art},
	}, []discovery.Descriptor{d})
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 1 || exports[0].Kind != kind {
		t.Fatalf("exports=%+v", exports)
	}

	reg := pluginreg.NewRegistry()
	_ = reg.RegisterBackend("openai-responses", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	})
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9004},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	if err := runtimebundle.InstallDiscoveredExports(reg, host, exports, runtimebundle.DiscoveredInstallOptions{
		DialSession: inProcessFakeDial(fake),
	}); err != nil {
		t.Fatal(err)
	}
	available := append([]string{"openai-responses"}, kind)
	if err := pluginreg.ResolveEnabledFactoryIDs([]string{kind}, available); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	_ = yaml.Unmarshal([]byte("from: catalog\n"), &cfgNode)
	res, err := reg.BuildBackendWithLifecycle(kind, "dyn-inst", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleanup != nil {
		t.Cleanup(func() { _ = res.Cleanup() })
	}
	if res.Backend.Open == nil {
		t.Fatal("expected Open after discovery→catalog→Install→BuildBackendWithLifecycle")
	}
}

func TestUnknownKind_BuildFailsBeforeConstruction(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: syntheticUnknownKind, ID: "missing", Enabled: true,
		}}},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil || !strings.Contains(err.Error(), syntheticUnknownKind) {
		t.Fatalf("compile err = %v, want unknown kind", err)
	}
}

func TestDiscovered_BuildBackendEmptyInstanceIDRejected(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9010},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	kind := "empty-id-kind-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind:     kind,
		Profile:  pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		Artifact: &trust.VerifiedArtifact{DigestHex: "empty-id"},
		Model:    processhost.ProcessModelPerInstance,
	}}, runtimebundle.DiscoveredInstallOptions{DialSession: stubDialSession(t)}); err != nil {
		t.Fatal(err)
	}
	_, err := reg.BuildBackend(kind, yaml.Node{}, nil, pluginreg.BackendFactoryDeps{})
	if err == nil || !strings.Contains(err.Error(), "BuildBackendWithLifecycle") {
		t.Fatalf("BuildBackend err=%v, want fail-closed empty instance id", err)
	}
	_, err = reg.BuildBackendWithLifecycle(kind, "", yaml.Node{}, nil, pluginreg.BackendFactoryDeps{})
	if err == nil || !strings.Contains(err.Error(), "empty instance id") {
		t.Fatalf("BuildBackendWithLifecycle empty id err=%v", err)
	}
}

func TestDiscovered_SharedGenerationInvalidation(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	launcher := &processhost.TestLauncher{PID: 9011}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	kind := "shared-inv-" + strings.ReplaceAll(t.Name(), "/", "-")
	fake := &bpkit.FakeService{Mode: bpkit.ModeProcessExit}
	if err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind: kind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone, AccessScope: pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "shared-inv-digest"},
		Model:    processhost.ProcessModelSharedArtifact,
		Sharing:  processhost.SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
	}}, runtimebundle.DiscoveredInstallOptions{DialSession: inProcessFakeDial(fake)}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	_ = yaml.Unmarshal([]byte("k: v\n"), &cfgNode)
	resA, err := reg.BuildBackendWithLifecycle(kind, "inst-a", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if resA.Cleanup != nil {
			_ = resA.Cleanup()
		}
	})
	resB, err := reg.BuildBackendWithLifecycle(kind, "inst-b", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if resB.Cleanup != nil {
			_ = resB.Cleanup()
		}
	})
	if launcher.Launches.Load() != 1 {
		t.Fatalf("shared process launches=%d want 1", launcher.Launches.Load())
	}
	stream, err := resA.Backend.Open(context.Background(), lipapi.Call{
		ID: "inv", Session: lipapi.SessionRef{ALegID: "a"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "inst-a", Model: "m"}, Key: "inst-a:m"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected process-exit classified failure")
	}
	resC, err := reg.BuildBackendWithLifecycle(kind, "inst-c", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if resC.Cleanup != nil {
			_ = resC.Cleanup()
		}
	})
	if launcher.Launches.Load() != 2 {
		t.Fatalf("after host generation invalidation launches=%d want 2 (new shared generation)", launcher.Launches.Load())
	}
}

func TestDiscovered_NoConnectorSpecificSwitchInInstaller(t *testing.T) {
	t.Parallel()
	candidates := []string{
		"discovered_factories.go",
		filepath.Join("internal", "infra", "runtimebundle", "discovered_factories.go"),
	}
	var body []byte
	var err error
	for _, path := range candidates {
		body, err = os.ReadFile(path)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ToLower(string(body))
	for _, forbidden := range []string{
		`case "openai-codex"`,
		`case "codex"`,
		`case "opencode"`,
		`case "opencode-go"`,
		`case "acp"`,
		`case "cursor-sdk"`,
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("discovered installer must not contain connector switch %s", forbidden)
		}
	}
}

func stubDialSession(t *testing.T) runtimebundle.DialSessionFunc {
	t.Helper()
	return inProcessFakeDial(&bpkit.FakeService{Mode: bpkit.ModeValid})
}

func inProcessFakeDial(fake *bpkit.FakeService) runtimebundle.DialSessionFunc {
	return func(ctx context.Context, req runtimebundle.DialSessionRequest) (runtimebundle.ExecuteSession, backendplugin.ResolvedProfile, error) {
		inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
			InstanceID:    req.InstanceID,
			FactoryKind:   req.FactoryKind,
			ConfigYAML:    req.ConfigYAML,
			Secrets:       req.Secrets,
			Negotiation:   backendplugin.Negotiation{Compatible: true},
			RuntimePolicy: req.Policy,
		})
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		profile, err := inst.Resolve(ctx, nil)
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		return inst, profile, nil
	}
}
