package pluginreg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/lmstudio"
	"gopkg.in/yaml.v3"
)

func TestStandardBackends_includeLmstudioFactory(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	p, ok := reg.BackendSecurityProfile(lmstudio.ID)
	if !ok || p.CredentialMode != CredentialNone {
		t.Fatalf("profile for %q: ok=%v mode=%q", lmstudio.ID, ok, p.CredentialMode)
	}
}

func TestBuildBackend_lmstudio_emptyConfig(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(`{}`), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(lmstudio.ID, node, nil, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected backend Open")
	}
}

func TestBuildBackend_lmstudio_catalogMapsModels(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-oss:120b"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"id":"openai","models":[{"id":"gpt-oss:120b"}]}}`))
	}))
	t.Cleanup(catalogSrv.Close)

	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`base_url: %s
discovery:
  catalog_url: %s
`, modelsSrv.URL, catalogSrv.URL)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(lmstudio.ID, node, modelsSrv.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "gpt-oss:120b" || snap.Models[0].CanonicalID != "openai/gpt-oss:120b" {
		t.Fatalf("models = %+v", snap.Models)
	}
}
