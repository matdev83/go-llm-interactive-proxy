package standardplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/providerprofiles"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestExpandProviderProfileRows_bindsCatalogDataWithoutChangingCustomRows(t *testing.T) {
	t.Parallel()
	var profileNode, customNode yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &profileNode); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte("backend_prefix: private\nbase_url: https://private.example/v1\n"), &customNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{ID: "profile-instance", Kind: ProviderProfileKind, Enabled: true, Config: profileNode},
		{ID: "custom-instance", Kind: CustomOpenAIResponsesCompatibleID, Enabled: true, Config: customNode},
	}}}
	before := cfg.Plugins.Backends[1].Config.Value
	profileBefore := cfg.Plugins.Backends[0].Config.Value
	prepared, err := ExpandProviderProfileRows(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Plugins.Backends[0].Kind != ProviderProfileKind {
		t.Fatalf("source profile kind=%q", cfg.Plugins.Backends[0].Kind)
	}
	if cfg.Plugins.Backends[0].Config.Value != profileBefore {
		t.Fatal("source profile config changed")
	}
	if prepared.Plugins.Backends[0].Kind != CustomOpenAIResponsesCompatibleID {
		t.Fatalf("profile family kind=%q", prepared.Plugins.Backends[0].Kind)
	}
	if prepared.Plugins.Backends[1].Kind != CustomOpenAIResponsesCompatibleID {
		t.Fatal("custom row changed kind")
	}
	if !bytes.Equal([]byte(before), []byte(cfg.Plugins.Backends[1].Config.Value)) {
		t.Fatal("custom row config changed")
	}
}

func TestExpandProviderProfileRows_rejectsUnknownProfileBeforeActivation(t *testing.T) {
	t.Parallel()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile_id: does-not-exist\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "missing", Kind: ProviderProfileKind, Enabled: true, Config: node}}}}
	if _, err := ExpandProviderProfileRows(cfg); err == nil {
		t.Fatal("unknown profile accepted")
	}
}

func TestBuildProviderProfileBackend_certifiesExecutableMapping(t *testing.T) {
	t.Setenv("PROFILE_CERT_KEY", "profile-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%q, want /v1/responses", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request JSON: %v; body=%s", err, body)
		}
		if request.Model != "native-model" {
			t.Errorf("request model mapping=%q, want native-model", request.Model)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer profile-secret" {
			t.Errorf("authorization=%q", got)
		}
		if got := r.Header.Get("X-Provider-Client"); got != "profile-cert" {
			t.Errorf("safe header=%q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"profile-response","object":"response","created_at":1,"status":"completed","model":"native-model","output":[{"type":"message","id":"message-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"profile-ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)

	profile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "certified-profile",
		Family:     providerprofiles.FamilyOpenAIResponses,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "PROFILE_CERT_KEY"},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryStatic,
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
		},
		Tokenizer: providerprofiles.TokenizerAccounting{TokenizerID: "cl100k_base", Source: providerprofiles.AccountingLocalTokenizer},
	}
	profile.Headers = []providerprofiles.SafeHeader{{Name: "X-Provider-Client", Value: "profile-cert"}}
	profile.Models.Static = []providerprofiles.Model{{CanonicalID: "certified-profile/model", NativeID: "native-model", DisplayName: "Certified model"}}
	compiled, err := CompileProviderProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := BuildProviderProfileBackend(compiled, "profile-instance", server.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	models, err := backend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Models) != 1 || models.Models[0].NativeID != "native-model" || models.Models[0].CanonicalID != "certified-profile/model" {
		t.Fatalf("model mapping=%+v", models.Models)
	}
	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "profile-instance", Model: "native-model"}})
	if err != nil {
		t.Fatal(err)
	}
	collected, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Text.String() != "profile-ok" {
		t.Fatalf("mapped response text=%q", collected.Text.String())
	}
}

func TestBuildProviderProfileBackend_anthropicV1ModelsQuirkUsesProfilePath(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/provider/model-catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-profile","display_name":"Profile Claude"}]}`)
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: "+
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-profile","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}`+
				"\n\n"+
				"event: content_block_start\ndata: "+
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+
				"\n\n"+
				"event: content_block_delta\ndata: "+
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`+
				"\n\n"+
				"event: content_block_stop\ndata: "+
				`{"type":"content_block_stop","index":0}`+
				"\n\n"+
				"event: message_delta\ndata: "+
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":1}}`+
				"\n\n"+
				"event: message_stop\ndata: "+
				`{"type":"message_stop"}`+
				"\n\n")
		default:
			t.Errorf("unexpected path=%q", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	profile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "anthropic-profile",
		Family:     providerprofiles.FamilyAnthropic,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL, PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthNone},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryFamilyDefault,
			Path:      "/provider/model-catalog",
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
		},
		Tokenizer: providerprofiles.TokenizerAccounting{Source: providerprofiles.AccountingLocalTokenizer},
		Quirks:    []providerprofiles.QuirkID{providerprofiles.QuirkAnthropicV1Models},
	}
	compiled, err := CompileProviderProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := BuildProviderProfileBackend(compiled, "anthropic-profile-instance", server.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	models, err := backend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Models) != 1 || models.Models[0].NativeID != "claude-profile" || models.Models[0].CanonicalID != "anthropic-profile/claude-profile" {
		t.Fatalf("models=%+v", models.Models)
	}

	call := lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		Invocation: lipapi.Invocation{DeliveryMode: lipapi.DeliveryModeNonStreaming, TransportMode: lipapi.TransportModeNonStreaming},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic-profile-instance", Model: "claude-profile"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
}

func TestBuildProviderProfileBackend_anthropicV1ModelsQuirkRejectsEncodedTraversal(t *testing.T) {
	t.Parallel()
	profile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "anthropic-profile",
		Family:     providerprofiles.FamilyAnthropic,
		Endpoint:   providerprofiles.Endpoint{BaseURL: "https://api.example.invalid", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthNone},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryFamilyDefault,
			Path:      "/%2e%2e/models",
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
		},
		Quirks: []providerprofiles.QuirkID{providerprofiles.QuirkAnthropicV1Models},
	}
	if err := providerprofiles.Validate(profile); err == nil {
		t.Fatal("encoded traversal accepted")
	}
}

func TestProjectProviderProfileDiagnostics_returnsCatalogRows(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{ID: "prof-1", Kind: "provider-profile", Enabled: true},
	}}}
	diags := ProjectProviderProfileDiagnostics(cfg)
	if len(diags) == 0 {
		t.Fatal("expected diagnostic rows for provider-profile backend")
	}
	if diags[0].Origin != "embedded_provider_profile_catalog" {
		t.Fatalf("origin=%q", diags[0].Origin)
	}
}

func TestProviderProfile_ProductionRegistryPath_PreservesCapabilityCeilingAndZeroUpstreamWork(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-test-key")
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-1","output":[]}`)
	}))
	t.Cleanup(server.Close)

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &node); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{
					ID:      "groq-instance",
					Kind:    ProviderProfileKind,
					Enabled: true,
					Config:  node,
				},
			},
		},
	}

	sourceKindBefore := cfg.Plugins.Backends[0].Kind
	sourceConfigBefore := cfg.Plugins.Backends[0].Config.Value

	prepared, err := PrepareProviderProfiles(cfg)
	if err != nil {
		t.Fatalf("PrepareProviderProfiles: %v", err)
	}

	// 1. Assert source config immutability
	if cfg.Plugins.Backends[0].Kind != sourceKindBefore {
		t.Fatalf("source config kind mutated: got %q, want %q", cfg.Plugins.Backends[0].Kind, sourceKindBefore)
	}
	if cfg.Plugins.Backends[0].Config.Value != sourceConfigBefore {
		t.Fatal("source config value mutated")
	}

	// 2. Drive through same backend registry lifecycle used by candidate compilation
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := reg.BuildBackendWithLifecycle(
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	// Assert backend prefix matches profile ID
	if !slices.Contains(backend.BackendPrefixes, "groq") {
		t.Fatalf("expected backend prefix 'groq', got %v", backend.BackendPrefixes)
	}

	// 3. Prove compiled disabled capabilities are preserved (vision, documents, reasoning, parallel_tool_calls disabled)
	if _, ok := backend.Caps[lipapi.CapabilityVision]; ok {
		t.Fatalf("expected CapabilityVision to be disabled in backend.Caps by compiled profile, but it was enabled")
	}
	if _, ok := backend.Caps[lipapi.CapabilityDocuments]; ok {
		t.Fatalf("expected CapabilityDocuments to be disabled in backend.Caps by compiled profile, but it was enabled")
	}
	if _, ok := backend.Caps[lipapi.CapabilityReasoning]; ok {
		t.Fatalf("expected CapabilityReasoning to be disabled in backend.Caps by compiled profile, but it was enabled")
	}

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:      lipapi.PartImageRef,
				ImageRef:  "https://example.com/test.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "groq-instance", Model: "groq-model"},
	}
	resolved := backend.ResolveCaps(context.Background(), call, cand)
	if _, ok := resolved[lipapi.CapabilityVision]; ok {
		t.Fatalf("expected CapabilityVision to be disabled in resolved caps, but it was enabled")
	}

	// 4. Demonstrate zero upstream work when a disabled required capability is rejected
	ex := runtime.TestExecutor()
	ex.Backends = map[string]execbackend.Backend{"groq-instance": backend}
	callWithRoute := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "groq-instance:groq-model"},
		Messages: call.Messages,
	}
	_, execErr := ex.Execute(context.Background(), callWithRoute)
	if execErr == nil {
		t.Fatal("expected execution error when required capability (vision) is disabled")
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("expected zero upstream calls on rejected capability, got %d", calls)
	}
}

func TestProviderProfile_ProductionRegistryPath_PreservesBoundedSafeHeaders(t *testing.T) {
	t.Setenv("SAFE_HEADER_KEY", "secret-key")
	var receivedClientHeader atomic.Pointer[string]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("X-Provider-Client")
		receivedClientHeader.Store(&h)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-header","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	t.Cleanup(server.Close)

	testProfile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "safe-header-profile",
		Family:     providerprofiles.FamilyOpenAIResponses,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "SAFE_HEADER_KEY"},
		Headers:    []providerprofiles.SafeHeader{{Name: "X-Provider-Client", Value: "lip-safe-client"}},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryStatic,
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
			Static:    []providerprofiles.Model{{CanonicalID: "safe-header-profile/m1", NativeID: "m1", DisplayName: "Model 1"}},
		},
	}
	catalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testProfile})
	if err != nil {
		t.Fatal(err)
	}

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: safe-header-profile\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "safe-header-instance", Kind: ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}

	prepared, err := PrepareProviderProfilesWithCatalog(cfg, catalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := reg.BuildBackendWithLifecycle(
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "safe-header-instance", Model: "m1"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	hdr := receivedClientHeader.Load()
	if hdr == nil || *hdr != "lip-safe-client" {
		got := "<nil>"
		if hdr != nil {
			got = *hdr
		}
		t.Fatalf("expected header X-Provider-Client: 'lip-safe-client', got %q", got)
	}
}

func TestProviderProfile_ProductionRegistryPath_AnthropicAlternateModelPathQuirk(t *testing.T) {
	t.Setenv("ANTHROPIC_TEST_KEY", "anthropic-key")
	var requestedPath atomic.Pointer[string]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		requestedPath.Store(&path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-alt-model","display_name":"Alt Claude"}]}`)
	}))
	t.Cleanup(server.Close)

	testProfile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "anthropic-alt-path-profile",
		Family:     providerprofiles.FamilyAnthropic,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL, PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthAPIKeyEnv, EnvVar: "ANTHROPIC_TEST_KEY"},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryFamilyDefault,
			Path:      "/provider/model-catalog",
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
		},
		Quirks: []providerprofiles.QuirkID{providerprofiles.QuirkAnthropicV1Models},
	}
	catalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testProfile})
	if err != nil {
		t.Fatal(err)
	}

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: anthropic-alt-path-profile\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "anthropic-alt-instance", Kind: ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}

	prepared, err := PrepareProviderProfilesWithCatalog(cfg, catalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := reg.BuildBackendWithLifecycle(
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	if _, err := backend.ModelInventory.LoadModels(context.Background()); err != nil {
		t.Fatalf("LoadModels: %v", err)
	}

	gotPath := requestedPath.Load()
	if gotPath == nil || *gotPath != "/provider/model-catalog" {
		got := "<nil>"
		if gotPath != nil {
			got = *gotPath
		}
		t.Fatalf("expected discovery path '/provider/model-catalog', got %q", got)
	}
}

func TestProviderProfile_ProductionRegistryPath_OpenResponsesCapabilitiesAndDialects(t *testing.T) {
	t.Setenv("OR_TEST_KEY", "or-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	testProfile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "openresponses-profile",
		Family:     providerprofiles.FamilyOpenResponses,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL, PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "OR_TEST_KEY"},
		Capabilities: providerprofiles.CapabilityOverrides{
			Disable: []lipapi.Capability{lipapi.CapabilityCompaction},
		},
		Dialects: providerprofiles.DialectOverrides{
			Item: []lipapi.DialectRequirement{{Dialect: "openresponses.2026-04-24", Implementor: "test-impl"}},
		},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryStatic,
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
			Static:    []providerprofiles.Model{{CanonicalID: "openresponses-profile/m1", NativeID: "m1", DisplayName: "Model 1"}},
		},
	}
	catalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testProfile})
	if err != nil {
		t.Fatal(err)
	}

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: openresponses-profile\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "openresponses-instance", Kind: ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}

	prepared, err := PrepareProviderProfilesWithCatalog(cfg, catalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := reg.BuildBackendWithLifecycle(
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	// Assert compiled disabled capability
	if _, ok := backend.Caps[lipapi.CapabilityCompaction]; ok {
		t.Fatalf("expected CapabilityCompaction to be disabled by compiled profile, but it was enabled in backend.Caps")
	}

	// Assert dialect requirement preserved
	if len(backend.DialectSupport.ItemDialects) == 0 || backend.DialectSupport.ItemDialects[0].Implementor != "test-impl" {
		t.Fatalf("expected dialect item implementor 'test-impl', got %+v", backend.DialectSupport.ItemDialects)
	}
}

func TestProviderProfile_CustomCompatibleSameInstanceID_RetainsGenericSemantics(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-1","output":[]}`)
	}))
	t.Cleanup(server.Close)

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	// 1. Prepare and build a provider profile with instance ID "shared-instance-id"
	var profileNode yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &profileNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "shared-instance-id", Kind: ProviderProfileKind, Enabled: true, Config: profileNode},
			},
		},
	}
	prepared, err := PrepareProviderProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resProfile, err := reg.BuildBackendWithLifecycle(
		prepared.Plugins.Backends[0].FactoryID(),
		prepared.Plugins.Backends[0].InstanceID(),
		prepared.Plugins.Backends[0].Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Profile backend has Vision disabled
	if _, ok := resProfile.Backend.Caps[lipapi.CapabilityVision]; ok {
		t.Fatal("expected CapabilityVision to be disabled on groq profile backend")
	}

	// 2. Build an arbitrary custom-*-compatible row with the EXACT SAME instance ID "shared-instance-id"
	var customNode yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: custom_pfx\nbase_url: "+server.URL+"/v1\n"), &customNode); err != nil {
		t.Fatal(err)
	}
	resCustom, err := reg.BuildBackendWithLifecycle(
		CustomOpenAIResponsesCompatibleID,
		"shared-instance-id",
		customNode,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("build generic custom backend: %v", err)
	}

	// 3. Generic custom-compatible backend MUST retain generic compatible semantics:
	// - It must have backend_prefix "custom_pfx", NOT "groq"
	if len(resCustom.Backend.BackendPrefixes) != 1 || resCustom.Backend.BackendPrefixes[0] != "custom_pfx" {
		t.Fatalf("expected prefix ['custom_pfx'], got %v", resCustom.Backend.BackendPrefixes)
	}
	// - It must NOT be subject to the groq capability ceiling; CapabilityVision remains enabled
	if _, ok := resCustom.Backend.Caps[lipapi.CapabilityVision]; !ok {
		t.Fatal("expected CapabilityVision to be ENABLED on generic custom compatible backend, but it was disabled")
	}
}

func TestProviderProfile_SequentialReloads_DoNotLeakOrStale(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-1","output":[]}`)
	}))
	t.Cleanup(server.Close)

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	// Cycle 1: Build generation with groq profile on instance "reload-id"
	var node1 yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &node1); err != nil {
		t.Fatal(err)
	}
	cfg1 := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "reload-id", Kind: ProviderProfileKind, Enabled: true, Config: node1},
			},
		},
	}
	prep1, err := PrepareProviderProfiles(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	res1, err := reg.BuildBackendWithLifecycle(
		prep1.Plugins.Backends[0].FactoryID(),
		prep1.Plugins.Backends[0].InstanceID(),
		prep1.Plugins.Backends[0].Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg1.Identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res1.Backend.Caps[lipapi.CapabilityVision]; ok {
		t.Fatal("cycle 1: expected vision disabled")
	}

	// Cycle 2 (reload): Replace instance "reload-id" with an arbitrary custom-compatible backend
	var customNode yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: custom_reload\nbase_url: "+server.URL+"/v1\n"), &customNode); err != nil {
		t.Fatal(err)
	}
	cfg2 := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "reload-id", Kind: CustomOpenAIResponsesCompatibleID, Enabled: true, Config: customNode},
			},
		},
	}
	prep2, err := PrepareProviderProfiles(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := reg.BuildBackendWithLifecycle(
		prep2.Plugins.Backends[0].FactoryID(),
		prep2.Plugins.Backends[0].InstanceID(),
		prep2.Plugins.Backends[0].Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg2.Identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that cycle 2 backend has generic semantics and did NOT leak/stale groq bindings
	if len(res2.Backend.BackendPrefixes) != 1 || res2.Backend.BackendPrefixes[0] != "custom_reload" {
		t.Fatalf("cycle 2: expected prefix ['custom_reload'], got %v", res2.Backend.BackendPrefixes)
	}
	if _, ok := res2.Backend.Caps[lipapi.CapabilityVision]; !ok {
		t.Fatal("cycle 2: expected vision enabled on generic custom compatible backend")
	}
}

func TestProviderProfile_CommentStripping_PreservesCapabilityCeilingViaAnchor(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-key")
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-1","output":[]}`)
	}))
	t.Cleanup(server.Close)

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "groq-strip-test", Kind: ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}
	prepared, err := PrepareProviderProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}

	backendRow := prepared.Plugins.Backends[0]
	// Deliberately strip all YAML comments
	backendRow.Config.HeadComment = ""
	backendRow.Config.LineComment = ""
	backendRow.Config.FootComment = ""
	for _, child := range backendRow.Config.Content {
		child.HeadComment = ""
		child.LineComment = ""
		child.FootComment = ""
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	res, err := reg.BuildBackendWithLifecycle(
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle after comment stripping: %v", err)
	}
	backend := res.Backend

	// Assert CapabilityVision is STILL disabled via Anchor-based profile resolution
	if _, ok := backend.Caps[lipapi.CapabilityVision]; ok {
		t.Fatal("expected CapabilityVision to be disabled even after comments were stripped")
	}

	// Assert zero upstream work when required capability is rejected
	ex := runtime.TestExecutor()
	ex.Backends = map[string]execbackend.Backend{"groq-strip-test": backend}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "groq-strip-test:llama-3.3-70b"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:      lipapi.PartImageRef,
				ImageRef:  "https://example.com/test.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	_, execErr := ex.Execute(context.Background(), call)
	if execErr == nil {
		t.Fatal("expected capability mismatch error")
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("expected zero upstream calls, got %d", calls)
	}
}

func TestBuildProviderProfileBackend_NilUpstream_FailsClosed(t *testing.T) {
	t.Parallel()
	profile, err := providerprofiles.EmbeddedProfile("groq")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := providerprofiles.CompileProfile(profile)
	if err != nil {
		t.Fatal(err)
	}

	_, err = BuildProviderProfileBackend(compiled, "groq-nil-upstream", nil, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected error on nil upstream client, got nil")
	}
	wantSubstr := "nil upstream client"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("expected error containing %q, got %q", wantSubstr, err.Error())
	}
}
