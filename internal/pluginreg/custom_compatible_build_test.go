package pluginreg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func customCompatibleRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func customCompatibleTestCall(op lipapi.Operation) lipapi.Call {
	return lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
		Invocation: lipapi.Invocation{
			Operation:     op,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
}

func TestBuildBackend_customOpenAILegacyCompatible_usesBackendPrefixAndChatTransport(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer yaml-key" {
			t.Errorf("Authorization = %q", got)
			http.Error(w, "unexpected authorization", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"openai/gpt-4o-mini"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: my-legacy
base_url: %s/v1
api_key: yaml-key
`, modelsSrv.URL)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(CustomOpenAILegacyCompatibleID, node, modelsSrv.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if be.Open == nil {
		t.Fatal("expected backend Open")
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-legacy" {
		t.Fatalf("BackendPrefixes = %#v, want [my-legacy]", be.BackendPrefixes)
	}

	caps := execbackend.EffectiveTransportCaps(context.Background(), be, customCompatibleTestCall(lipapi.OperationOpenAIChatCompletions), routing.AttemptCandidate{})
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("expected chat streaming supported")
	}
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected chat non-streaming supported")
	}
	if caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("custom legacy compatible must not expose responses transport")
	}

	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceRemote)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "my-legacy/openai/gpt-4o-mini" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customOpenAIResponsesCompatible_usesBackendPrefixAndResponsesTransport(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer yaml-key" {
			t.Errorf("Authorization = %q", got)
			http.Error(w, "unexpected authorization", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: my-responses
base_url: %s/v1
api_key: yaml-key
`, modelsSrv.URL)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(CustomOpenAIResponsesCompatibleID, node, modelsSrv.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if be.Open == nil {
		t.Fatal("expected backend Open")
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-responses" {
		t.Fatalf("BackendPrefixes = %#v, want [my-responses]", be.BackendPrefixes)
	}

	caps := execbackend.EffectiveTransportCaps(context.Background(), be, customCompatibleTestCall(lipapi.OperationOpenAIResponses), routing.AttemptCandidate{})
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("expected responses streaming supported")
	}
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected responses non-streaming supported")
	}
	if caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("custom responses compatible must not expose chat-completions transport")
	}

	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceRemote)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "my-responses/gpt-4.1" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customOpenAIResponsesCompatible_staticModelsOverrideDiscovery(t *testing.T) {
	t.Parallel()

	reg := customCompatibleRegistry(t)
	raw := `backend_prefix: my-responses
base_url: http://127.0.0.1:9/v1
api_key: yaml-key
models:
  source: inline
  items:
    - canonical_id: my-responses/static-model
      native_id: static-model
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(CustomOpenAIResponsesCompatibleID, node, nil, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceStaticInline)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "static-model" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customAnthropicCompatible_usesBackendPrefixAndRemoteDiscovery(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("x-api-key"); got != "yaml-key" {
			t.Errorf("x-api-key = %q", got)
			http.Error(w, "unexpected api key", http.StatusUnauthorized)
			return
		}
		body := `{"data":[{"id":"claude-sonnet-4-20250514","display_name":"Claude Sonnet 4"}]}`
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(modelsSrv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: my-anthropic
base_url: %s
api_key: yaml-key
`, modelsSrv.URL)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(CustomAnthropicCompatibleID, node, modelsSrv.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-anthropic" {
		t.Fatalf("BackendPrefixes = %#v, want [my-anthropic]", be.BackendPrefixes)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceRemote)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "my-anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customAnthropicCompatible_staticModelsOverrideDiscovery(t *testing.T) {
	t.Parallel()

	reg := customCompatibleRegistry(t)
	raw := `backend_prefix: my-anthropic
base_url: http://127.0.0.1:9
api_key: yaml-key
models:
  source: inline
  items:
    - canonical_id: my-anthropic/static-claude
      native_id: static-claude
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(CustomAnthropicCompatibleID, node, nil, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceStaticInline)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "static-claude" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customAnthropicCompatible_missingBaseURLUsesCustomPrefix(t *testing.T) {
	t.Parallel()

	reg := customCompatibleRegistry(t)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: my-anthropic\napi_key: yaml-key\n"), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(CustomAnthropicCompatibleID, node, nil, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), customCompatibleTestCall(lipapi.OperationOpenAIChatCompletions), routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected missing base_url error")
	}
	if !strings.Contains(err.Error(), "my-anthropic: base_url is required") {
		t.Fatalf("error = %v, want custom prefix", err)
	}
}
