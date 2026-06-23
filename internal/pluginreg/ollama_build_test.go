package pluginreg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/ollama"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
	"gopkg.in/yaml.v3"
)

func TestStandardBackends_includeOllamaFactory(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ollama.ID, ollama.CloudID} {
		p, ok := reg.BackendSecurityProfile(id)
		if !ok || p.CredentialMode != CredentialNone {
			t.Fatalf("profile for %q: ok=%v mode=%q", id, ok, p.CredentialMode)
		}
	}
}

func TestBuildBackend_ollamaCloud_emptyConfig(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := `responses_api: disabled`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(ollama.CloudID, node, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected backend Open")
	}
}

func TestBuildBackend_ollamaCloud_discoveryLocalDisabled(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"llama3:latest"},
		Version:     "0.13.3",
	}))
	t.Cleanup(local.Close)

	var localHits atomic.Int32
	localOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			localHits.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(localOnly.Close)

	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"cloud-only"}]}`))
	}))
	t.Cleanup(cloud.Close)

	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`base_url: %s/v1
responses_api: disabled
discovery:
  cloud_models_url: %s
`, localOnly.URL, cloud.URL)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(ollama.CloudID, node, local.Client())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "cloud-only" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if snap.Models[0].CanonicalID != "unknown/cloud-only" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if localHits.Load() != 0 {
		t.Fatalf("local discovery invoked %d times", localHits.Load())
	}
}

func TestBuildBackend_ollama_emptyConfig(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := `responses_api: disabled`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(ollama.ID, node, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected backend Open")
	}
}

func TestBuildBackend_ollama_discoveryCloudDisabled(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"llama3:latest"},
		Version:     "0.13.3",
	}))
	t.Cleanup(local.Close)

	var cloudHits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits.Add(1)
		_, _ = w.Write([]byte(`{"models":[{"name":"cloud-only"}]}`))
	}))
	t.Cleanup(cloud.Close)

	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`base_url: %s/v1
responses_api: disabled
discovery:
  local_models: true
  cloud_models: false
  cloud_models_url: %s
`, local.URL, cloud.URL)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(ollama.ID, node, local.Client())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "llama3:latest" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if cloudHits.Load() != 0 {
		t.Fatalf("cloud discovery invoked %d times", cloudHits.Load())
	}
}
