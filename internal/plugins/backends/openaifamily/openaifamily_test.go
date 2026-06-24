package openaifamily_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

func testProfile() openaifamily.Profile {
	return openaifamily.Profile{
		ID:                     "testbackend",
		DefaultBaseURL:         "http://localhost:9999/v1",
		DefaultDummyCredential: "testbackend",
		Transport:              openaifamily.TransportChatOnly,
		ModelResolution:        openaifamily.ModelResolutionStripBackendPrefix,
		Inventory:              openaifamily.InventoryCatalogAware,
	}
}

func testCandidate(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: model}}
}

func testCall() lipapi.Call {
	return lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
}

func TestApplyDefaults_profileDefaults(t *testing.T) {
	t.Parallel()
	profile := testProfile()
	cfg := openaifamily.ApplyDefaults(profile, openaifamily.Config{})
	if cfg.BaseURL != profile.DefaultBaseURL {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if !openaifamily.DiscoveryCatalog(cfg.Discovery) {
		t.Fatal("expected catalog enabled by default")
	}
	if cfg.Discovery.CatalogURL != "https://models.dev/api.json" {
		t.Fatalf("CatalogURL = %q", cfg.Discovery.CatalogURL)
	}
	if cfg.Discovery.Timeout != 15*time.Second {
		t.Fatalf("Timeout = %v", cfg.Discovery.Timeout)
	}
}

func TestApplyDefaults_profileCatalogAndTimeoutOverride(t *testing.T) {
	t.Parallel()
	profile := openaifamily.Profile{
		ID:                      "custom",
		DefaultBaseURL:          "http://localhost:1/v1",
		DefaultCatalogURL:       "https://catalog.example/api.json",
		DefaultDiscoveryTimeout: 30 * time.Second,
	}
	cfg := openaifamily.ApplyDefaults(profile, openaifamily.Config{})
	if cfg.Discovery.CatalogURL != "https://catalog.example/api.json" {
		t.Fatalf("CatalogURL = %q", cfg.Discovery.CatalogURL)
	}
	if cfg.Discovery.Timeout != 30*time.Second {
		t.Fatalf("Timeout = %v", cfg.Discovery.Timeout)
	}
}

func TestEffectiveCredentials_dummyFallbackAndExplicitKey(t *testing.T) {
	t.Parallel()
	profile := testProfile()
	cfg := openaifamily.Config{}
	key, _, _ := openaifamily.EffectiveCredentials(profile, cfg)
	if key != "testbackend" {
		t.Fatalf("dummy credential = %q", key)
	}

	cfg = openaifamily.Config{APIKey: "my-secret"}
	key, _, _ = openaifamily.EffectiveCredentials(profile, cfg)
	if key != "my-secret" {
		t.Fatalf("explicit credential = %q", key)
	}
}

func TestResolveModelForPrefix_stripsIDOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"testbackend/llama-3", "llama-3"},
		{"testbackend:llama-3", "llama-3"},
		{"llama-3", "llama-3"},
		{"meta/llama-3", "meta/llama-3"},
		{"openai/gpt-4o", "openai/gpt-4o"},
	}
	for _, tc := range cases {
		got := openaifamily.ResolveModelForPrefix("testbackend", testCandidate(tc.in), lipapi.Call{})
		if got != tc.want {
			t.Fatalf("resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveModel_directPolicyPreservesBackendLikePrefix(t *testing.T) {
	t.Parallel()
	got := openaifamily.ResolveModel(
		openaifamily.ModelResolutionDirect,
		"testbackend",
		testCandidate("testbackend:llama-3"),
		lipapi.Call{},
	)
	if got != "testbackend:llama-3" {
		t.Fatalf("direct model = %q", got)
	}
}

func TestModelFromCall_extensionFallback(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openailegacy.model": json.RawMessage(`"legacy-model"`),
		},
	}
	got := openaifamily.ModelFromCall(testCandidate(""), call)
	if got != "legacy-model" {
		t.Fatalf("legacy model = %q", got)
	}

	call = lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openairesponses.model": json.RawMessage(`"responses-model"`),
		},
	}
	got = openaifamily.ModelFromCall(testCandidate(""), call)
	if got != "responses-model" {
		t.Fatalf("responses model = %q", got)
	}
}

func TestResolveFlavor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		call lipapi.Call
		want openaicompat.Flavor
	}{
		{
			name: "upstream flavor responses",
			call: lipapi.Call{
				Extensions: map[string]json.RawMessage{
					openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
				},
			},
			want: openaicompat.FlavorResponses,
		},
		{
			name: "openairesponses extension",
			call: lipapi.Call{
				Extensions: map[string]json.RawMessage{
					"openairesponses.model": json.RawMessage(`"m"`),
				},
			},
			want: openaicompat.FlavorResponses,
		},
		{
			name: "openailegacy extension",
			call: lipapi.Call{
				Extensions: map[string]json.RawMessage{
					"openailegacy.model": json.RawMessage(`"m"`),
				},
			},
			want: openaicompat.FlavorChat,
		},
		{
			name: "default chat",
			call: lipapi.Call{},
			want: openaicompat.FlavorChat,
		},
	}
	for _, tc := range cases {
		got := openaifamily.ResolveFlavor(tc.call)
		if got != tc.want {
			t.Fatalf("%s: flavor = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTransportCaps_chatOnlyVsBoth(t *testing.T) {
	t.Parallel()
	chatOnly := openaifamily.TransportCaps(openaifamily.TransportChatOnly)
	if chatOnly.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("chat-only must not support responses")
	}
	if !chatOnly.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("chat-only must support chat streaming")
	}

	both := openaifamily.TransportCaps(openaifamily.TransportChatAndResponses)
	if !both.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("both must support responses streaming")
	}
	if !both.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("both must support chat non-streaming")
	}
}

func TestNew_chatOnlyProfileRejectsResponses(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		refbackend.NewHandler(refbackend.Config{AllowMissingBearer: true}).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	profile := testProfile()
	profile.Transport = openaifamily.TransportChatOnly
	be := openaifamily.New(profile, openaifamily.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})

	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
		},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
	_, err := be.Open(context.Background(), call, testCandidate("testbackend:local-model"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "testbackend: responses API is not available") {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p == "/v1/responses" {
			t.Fatalf("unexpected /v1/responses hit, paths=%v", paths)
		}
	}
}

func TestNew_nativeModelOverrideBeforeUpstreamCall(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedModel string

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			defer mu.Unlock()
			var payload struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(b, &payload)
			capturedModel = payload.Model
		},
		AllowMissingBearer: true,
	}))
	t.Cleanup(srv.Close)

	profile := testProfile()
	be := openaifamily.New(profile, openaifamily.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
	es, err := be.Open(context.Background(), call, testCandidate("testbackend:local-model"))
	if err != nil {
		t.Fatal(err)
	}
	_ = es.Close()

	mu.Lock()
	defer mu.Unlock()
	if capturedModel != "local-model" {
		t.Fatalf("upstream model = %q", capturedModel)
	}
}

func TestNew_catalogAwareInventoryMappingAndFallback(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-oss:120b"},{"id":"unknown-local"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"id":"openai","models":[{"id":"gpt-oss:120b"}]}}`))
	}))
	t.Cleanup(catalogSrv.Close)

	profile := testProfile()
	be := openaifamily.New(profile, openaifamily.Config{
		BaseURL:    modelsSrv.URL + "/v1",
		HTTPClient: modelsSrv.Client(),
		Discovery: openaifamily.DiscoveryConfig{
			CatalogURL: catalogSrv.URL,
		},
	})
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"gpt-oss:120b":  "openai/gpt-oss:120b",
		"unknown-local": "testbackend/unknown-local",
	}
	for _, model := range snap.Models {
		if got, ok := want[model.NativeID]; !ok || model.CanonicalID != got {
			t.Fatalf("model = %+v", model)
		}
		delete(want, model.NativeID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %+v", want)
	}
}

func TestNew_openAICompatibleInventoryPolicy(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	profile := testProfile()
	profile.Inventory = openaifamily.InventoryOpenAICompatible
	be := openaifamily.New(profile, openaifamily.Config{
		BaseURL:    modelsSrv.URL + "/v1",
		HTTPClient: modelsSrv.Client(),
	})
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "testbackend/gpt-4o" || snap.Models[0].NativeID != "gpt-4o" {
		t.Fatalf("models = %+v", snap.Models)
	}
}

func TestNew_appliesRequestAndClientOptions(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedHeader string
	var capturedBody string

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestHeaders: func(h http.Header) {
			mu.Lock()
			defer mu.Unlock()
			capturedHeader = h.Get("X-Test-Client")
		},
		OnRequestBody: func(b []byte) {
			mu.Lock()
			defer mu.Unlock()
			capturedBody = string(b)
		},
		AllowMissingBearer: true,
	}))
	t.Cleanup(srv.Close)

	profile := testProfile()
	profile.ClientOptions = func(lipapi.Call) []option.RequestOption {
		return []option.RequestOption{option.WithHeader("X-Test-Client", "yes")}
	}
	profile.RequestOptions = func(lipapi.Call) []option.RequestOption {
		return []option.RequestOption{option.WithJSONSet("max_tokens", 19)}
	}
	be := openaifamily.New(profile, openaifamily.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), testCall(), testCandidate("testbackend:local-model"))
	if err != nil {
		t.Fatal(err)
	}
	_ = es.Close()

	mu.Lock()
	defer mu.Unlock()
	if capturedHeader != "yes" {
		t.Fatalf("client header = %q", capturedHeader)
	}
	if !strings.Contains(capturedBody, `"max_tokens":19`) {
		t.Fatalf("request option not applied body=%s", capturedBody)
	}
}
