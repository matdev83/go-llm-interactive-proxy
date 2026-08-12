package standardplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/providerprofiles"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestExpandProviderProfileRows_bindsCatalogDataWithoutChangingCustomRows(t *testing.T) {
	t.Parallel()
	var profileNode, customNode yaml.Node
	if err := yaml.Unmarshal([]byte("profile: example-openai-responses\n"), &profileNode); err != nil {
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
