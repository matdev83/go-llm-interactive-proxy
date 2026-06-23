package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/ollama"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var (
	boolTrue  = true
	boolFalse = false
)

func TestInventory_localModeEnumeratesLocalOnly(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"llama3:latest", "shared:tag"},
		Version:     "0.13.3",
	}))
	t.Cleanup(local.Close)

	var cloudHits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits.Add(1)
		_, _ = w.Write([]byte(`{"models":[{"name":"deepseek-v3.2"}]}`))
	}))
	t.Cleanup(cloud.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeLocal,
		Discovery: DiscoveryConfig{
			Enabled:  &boolTrue,
			Catalog:  &boolFalse,
			CloudURL: cloud.URL,
		},
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cloudHits.Load() != 0 {
		t.Fatalf("cloud discovery invoked %d times", cloudHits.Load())
	}
	if len(snap.Models) != 2 {
		t.Fatalf("models = %+v", snap.Models)
	}
	want := map[string]string{
		"llama3:latest": "meta/llama3:latest",
		"shared:tag":    "unknown/shared:tag",
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

func TestInventory_localIgnoresCloudToggle(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"llama3:latest"},
		Version:     "0.13.3",
	}))
	t.Cleanup(local.Close)

	var cloudHits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits.Add(1)
		_, _ = w.Write([]byte(`{"models":[{"name":"deepseek-v3.2"}]}`))
	}))
	t.Cleanup(cloud.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeLocal,
		Discovery: DiscoveryConfig{
			Enabled:  &boolTrue,
			Local:    &boolTrue,
			Cloud:    &boolTrue,
			Catalog:  &boolFalse,
			CloudURL: cloud.URL,
		},
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cloudHits.Load() != 0 {
		t.Fatalf("cloud discovery invoked %d times", cloudHits.Load())
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "llama3:latest" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if len(snap.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", snap.Warnings)
	}
}

func TestInventory_cloudModeEnumeratesCloudOnly(t *testing.T) {
	t.Parallel()

	var localHits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			localHits.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(local.Close)

	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"deepseek-v3.2"},{"name":"shared:tag"}]}`))
	}))
	t.Cleanup(cloud.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeCloud,
		Discovery: DiscoveryConfig{
			Enabled:  &boolTrue,
			Catalog:  &boolFalse,
			CloudURL: cloud.URL,
		},
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if localHits.Load() != 0 {
		t.Fatalf("local discovery invoked %d times", localHits.Load())
	}
	if len(snap.Models) != 2 {
		t.Fatalf("models = %+v", snap.Models)
	}
	want := map[string]string{
		"deepseek-v3.2": "deepseek/deepseek-v3.2",
		"shared:tag":    "unknown/shared:tag",
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

func TestInventory_cloudIgnoresLocalToggle(t *testing.T) {
	t.Parallel()

	var localHits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			localHits.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(local.Close)

	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"deepseek-v3.2"}]}`))
	}))
	t.Cleanup(cloud.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeCloud,
		Discovery: DiscoveryConfig{
			Enabled:  &boolTrue,
			Local:    &boolTrue,
			Cloud:    &boolTrue,
			Catalog:  &boolFalse,
			CloudURL: cloud.URL,
		},
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if localHits.Load() != 0 {
		t.Fatalf("local discovery invoked %d times", localHits.Load())
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "deepseek-v3.2" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if len(snap.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", snap.Warnings)
	}
}

func TestInventory_cloudCatalogMapsKnownModels(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{Version: "0.13.3"}))
	t.Cleanup(local.Close)

	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"gemma3:4b"},{"name":"gpt-oss:120b"},{"name":"unknown-cloud-model"}]}`))
	}))
	t.Cleanup(cloud.Close)
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"id":"openai","models":[{"id":"gpt-oss:120b"}]},"google":{"id":"google","models":[{"id":"gemma3:4b"}]}}`))
	}))
	t.Cleanup(catalog.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeCloud,
		Discovery: DiscoveryConfig{
			Enabled:      &boolTrue,
			Capabilities: &boolFalse,
			CloudURL:     cloud.URL,
			CatalogURL:   catalog.URL,
		},
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"gemma3:4b":           "google/gemma3:4b",
		"gpt-oss:120b":        "openai/gpt-oss:120b",
		"unknown-cloud-model": "unknown/unknown-cloud-model",
	}
	for _, model := range snap.Models {
		if got, ok := want[model.NativeID]; !ok || model.CanonicalID != got {
			t.Fatalf("native %q canonical = %q, want %q", model.NativeID, model.CanonicalID, got)
		}
		delete(want, model.NativeID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %+v", want)
	}
}

func TestInventory_localUnknownFallbackCanonical(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"unknown:latest"},
		Version:     "0.13.3",
	}))
	t.Cleanup(local.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeLocal,
		Discovery: DiscoveryConfig{
			Enabled: &boolTrue,
			Catalog: &boolFalse,
		},
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models = %+v", snap.Models)
	}
	if snap.Models[0].NativeID != "unknown:latest" || snap.Models[0].CanonicalID != "unknown/unknown:latest" {
		t.Fatalf("model = %+v", snap.Models[0])
	}
}

func TestInventory_keywordFallbackCanonicalIDs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		native string
		want   string
	}{
		{"nemotron-3-ultra", "nvidia/nemotron-3-ultra"},
		{"gpt-oss:20b", "openai/gpt-oss:20b"},
		{"gemma4:31b", "google/gemma4:31b"},
		{"nemotron-3-ultra-cloud", "nvidia/nemotron-3-ultra"},
		{"gpt-oss:20b-cloud", "openai/gpt-oss:20b"},
		{"claude-sonnet-4-cloud", "anthropic/claude-sonnet-4"},
		{"gemini-2.5-pro-cloud", "google/gemini-2.5-pro"},
		{"gemma4:31b-cloud", "google/gemma4:31b"},
		{"banana-cloud", "google/banana"},
		{"kimi-k2.7-code-cloud", "moonshotai/kimi-k2.7-code"},
		{"glm-5.2-cloud", "z-ai/glm-5.2"},
		{"fable-cloud", "anthropic/fable"},
		{"qwen3-coder-next-cloud", "qwen/qwen3-coder-next"},
		{"deepseek-v4-pro-cloud", "deepseek/deepseek-v4-pro"},
		{"minimax-m3-cloud", "minimax/minimax-m3"},
		{"mimo-vl-cloud", "xiaomi/mimo-vl"},
		{"devstral-2:123b-cloud", "mistralai/devstral-2:123b"},
		{"devstral-small-2:24b-cloud", "mistralai/devstral-small-2:24b"},
		{"rnj-1:8b-cloud", "essentialai/rnj-1:8b"},
		{"ministral-3:14b-cloud", "mistralai/ministral-3:14b"},
		{"mistral-large-3:675b-cloud", "mistralai/mistral-large-3:675b"},
		{"llama3:latest", "meta/llama3:latest"},
	}

	for _, tc := range cases {
		t.Run(tc.native, func(t *testing.T) {
			t.Parallel()

			got := canonicalIDForNative(backendModeCloud, tc.native, nil)
			if got != tc.want {
				t.Fatalf("canonicalIDForNative(%q) = %q, want %q", tc.native, got, tc.want)
			}
		})
	}
}

func TestInventory_localCatalogMapsKnownModels(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"gemma3:4b", "gpt-oss:120b"},
		Version:     "0.13.3",
	}))
	t.Cleanup(local.Close)
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"id":"openai","models":[{"id":"gpt-oss:120b"}]},"google":{"id":"google","models":[{"id":"gemma3:4b"}]}}`))
	}))
	t.Cleanup(catalog.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeLocal,
		Discovery: DiscoveryConfig{
			Enabled:      &boolTrue,
			Capabilities: &boolFalse,
			CatalogURL:   catalog.URL,
		},
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"gemma3:4b":    "google/gemma3:4b",
		"gpt-oss:120b": "openai/gpt-oss:120b",
	}
	for _, model := range snap.Models {
		if got, ok := want[model.NativeID]; !ok || model.CanonicalID != got {
			t.Fatalf("native %q canonical = %q, want %q", model.NativeID, model.CanonicalID, got)
		}
		delete(want, model.NativeID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %+v", want)
	}
}

func TestInventory_localAllowedSourceDisabledReturnsError(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"llama3:latest"},
		Version:     "0.13.3",
	}))
	t.Cleanup(local.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeLocal,
		Discovery: DiscoveryConfig{
			Enabled: &boolTrue,
			Local:   &boolFalse,
			Catalog: &boolFalse,
		},
	})

	_, err := p.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected error when local source disabled")
	}
	if !strings.Contains(err.Error(), "no models") {
		t.Fatalf("error = %v", err)
	}
}

func TestInventory_cloudAllowedSourceDisabledReturnsError(t *testing.T) {
	t.Parallel()

	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"deepseek-v3.2"}]}`))
	}))
	t.Cleanup(cloud.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    "http://localhost/v1",
		NativeRoot: "http://localhost",
		HTTPClient: cloud.Client(),
		Mode:       backendModeCloud,
		Discovery: DiscoveryConfig{
			Enabled:  &boolTrue,
			Cloud:    &boolFalse,
			Catalog:  &boolFalse,
			CloudURL: cloud.URL,
		},
	})

	_, err := p.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected error when cloud source disabled")
	}
	if !strings.Contains(err.Error(), "no models") {
		t.Fatalf("error = %v", err)
	}
}

func TestInventory_localAllowedSourceFailureReturnsError(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	t.Cleanup(local.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeLocal,
		Discovery: DiscoveryConfig{
			Enabled: &boolTrue,
			Local:   &boolTrue,
			Catalog: &boolFalse,
		},
	})

	_, err := p.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected error when enabled local source fails")
	}
	if !strings.Contains(err.Error(), "all enabled sources failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestInventory_capabilityMappingStored(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"llama3.2:latest"},
		Version:     "0.13.3",
		Capabilities: map[string][]string{
			"llama3.2:latest": {"completion", "vision"},
		},
	}))
	t.Cleanup(local.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeLocal,
		Discovery: DiscoveryConfig{
			Enabled:      &boolTrue,
			Local:        &boolTrue,
			Cloud:        &boolFalse,
			Catalog:      &boolFalse,
			Capabilities: &boolTrue,
		},
	})

	if _, err := p.LoadModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	caps := p.CapsForNative("llama3.2:latest")
	if _, ok := caps[lipapi.CapabilityVision]; !ok {
		t.Fatal("expected vision from capability probe")
	}
}

func TestInventory_discoveryTimeoutAppliesToModelDiscovery(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3:latest"}]}`))
	}))
	t.Cleanup(func() {
		close(release)
		local.Close()
	})

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		APIKey:     "ollama",
		HTTPClient: local.Client(),
		Mode:       backendModeLocal,
		Discovery: DiscoveryConfig{
			Enabled: &boolTrue,
			Local:   &boolTrue,
			Cloud:   &boolFalse,
			Catalog: &boolFalse,
			Timeout: 20 * time.Millisecond,
		},
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := p.LoadModels(context.Background())
		errCh <- err
	}()

	var err error
	select {
	case err = <-errCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("LoadModels did not honor discovery timeout")
	}
	if err == nil {
		t.Fatal("expected discovery timeout error")
	}
	select {
	case <-started:
	default:
		t.Fatal("expected local model discovery request")
	}
	if !strings.Contains(err.Error(), "all enabled sources failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestInventory_discoveryTimeoutAppliesToLazyCapabilityProbe(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			http.NotFound(w, r)
			return
		}
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"capabilities":["completion","vision"]}`))
	}))
	t.Cleanup(func() {
		close(release)
		local.Close()
	})

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    local.URL + "/v1",
		NativeRoot: local.URL,
		HTTPClient: local.Client(),
		Discovery:  DiscoveryConfig{Timeout: 20 * time.Millisecond},
	})

	capsCh := make(chan lipapi.BackendCaps, 1)
	go func() {
		capsCh <- p.ProbeCapsForNative(context.Background(), "llama3:latest")
	}()

	var caps lipapi.BackendCaps
	select {
	case caps = <-capsCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ProbeCapsForNative did not honor discovery timeout")
	}
	select {
	case <-started:
	default:
		t.Fatal("expected capability probe request")
	}
	if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected fallback streaming capability")
	}
	if _, ok := caps[lipapi.CapabilityVision]; ok {
		t.Fatal("expected timeout to fall back without vision capability")
	}
}
