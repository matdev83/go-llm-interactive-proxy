package pluginreg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestStandardBackendBundle_includesOpenCodeBackends(t *testing.T) {
	t.Parallel()

	be := StandardBackendBundle(UpstreamAPIKeys{})
	got := make([]string, 0, len(be.Backends))
	for _, entry := range be.Backends {
		got = append(got, entry.ID)
	}
	for _, want := range []string{"opencode-go", "opencode-zen"} {
		if !slices.Contains(got, want) {
			t.Fatalf("backend IDs = %#v, missing %q", got, want)
		}
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openCodeGo(t *testing.T) {
	clearAllProviderEnv(t)
	clearNumberedEnv(t, "OPENCODE_GO_API_KEY")
	t.Setenv("OPENCODE_GO_API_KEY", "go-1")
	t.Setenv("OPENCODE_GO_API_KEY_2", "go-2")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"go-1", "go-2"}
	if !reflect.DeepEqual(got.OpenCodeGo, want) {
		t.Fatalf("OpenCodeGo keys: %#v want %#v", got.OpenCodeGo, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openCodeZenPrefersOpenCodeAPIKey(t *testing.T) {
	clearAllProviderEnv(t)
	clearNumberedEnv(t, "OPENCODE_API_KEY")
	clearNumberedEnv(t, "OPENCODE_ZEN_API_KEY")
	t.Setenv("OPENCODE_API_KEY", "zen-primary")
	t.Setenv("OPENCODE_ZEN_API_KEY", "zen-alias")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"zen-primary"}
	if !reflect.DeepEqual(got.OpenCodeZen, want) {
		t.Fatalf("OpenCodeZen keys: %#v want %#v", got.OpenCodeZen, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openCodeZenFallsBackToZenAlias(t *testing.T) {
	clearAllProviderEnv(t)
	clearNumberedEnv(t, "OPENCODE_API_KEY")
	clearNumberedEnv(t, "OPENCODE_ZEN_API_KEY")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("OPENCODE_ZEN_API_KEY", "zen-fallback")
	t.Setenv("OPENCODE_ZEN_API_KEY_2", "zen-fallback-2")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"zen-fallback", "zen-fallback-2"}
	if !reflect.DeepEqual(got.OpenCodeZen, want) {
		t.Fatalf("OpenCodeZen keys: %#v want %#v", got.OpenCodeZen, want)
	}
}

func TestValidateCustomBackendPrefix_rejectsOpenCodeStandardPrefixes(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"opencode-go", "opencode-zen"} {
		err := validateCustomBackendPrefix(prefix)
		if err == nil {
			t.Fatalf("expected error for reserved backend_prefix %q", prefix)
		}
	}
}

func TestOpenCodeBackendFactory_usesExplicitVendorResolverDependency(t *testing.T) {
	t.Parallel()

	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"xiaomi/mimo-v2.5": {Source: modelcatalog.FactSourceCatalog},
	})
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
	}))
	t.Cleanup(srv.Close)

	var cfg yaml.Node
	yamlText := "base_url: " + srv.URL + "\napi_key: test\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("opencode-go", cfg, srv.Client(), BackendFactoryDeps{
		ModelVendorResolver: catalog.NewOpenCodeVendorResolver(
			modelcatalog.StaticActiveSnapshotProvider{Index: idx},
			true,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range snap.Models {
		if m.NativeID == "mimo-v2.5" && m.CanonicalID != "xiaomi/mimo-v2.5" {
			t.Fatalf("model = %+v", m)
		}
	}
}

func TestOpenCodeBackendFactory_goAndZenAreSeparateConnectors(t *testing.T) {
	t.Parallel()

	goSrv, goAuth := opencodeModelServer(t, `{"data":[{"id":"kimi-k2.7-code"}]}`)
	zenSrv, zenAuth := opencodeModelServer(t, `{"data":[{"id":"gpt-5.4"}]}`)
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{
		OpenCodeGo:  []string{"go-key"},
		OpenCodeZen: []string{"zen-key"},
	}); err != nil {
		t.Fatal(err)
	}

	goBackend := buildOpenCodeBackendForTest(t, reg, "opencode-go", goSrv)
	zenBackend := buildOpenCodeBackendForTest(t, reg, "opencode-zen", zenSrv)
	if !slices.Equal(goBackend.BackendPrefixes, []string{"opencode-go"}) {
		t.Fatalf("opencode-go prefixes = %#v", goBackend.BackendPrefixes)
	}
	if !slices.Equal(zenBackend.BackendPrefixes, []string{"opencode-zen"}) {
		t.Fatalf("opencode-zen prefixes = %#v", zenBackend.BackendPrefixes)
	}

	goSnap, err := goBackend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	zenSnap, err := zenBackend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *goAuth != "Bearer go-key" {
		t.Fatalf("opencode-go inventory auth = %q", *goAuth)
	}
	if *zenAuth != "Bearer zen-key" {
		t.Fatalf("opencode-zen inventory auth = %q", *zenAuth)
	}
	if got := nativeIDs(goSnap.Models); !slices.Equal(got, []string{"kimi-k2.7-code"}) {
		t.Fatalf("opencode-go native IDs = %#v", got)
	}
	if got := nativeIDs(zenSnap.Models); !slices.Equal(got, []string{"gpt-5.4"}) {
		t.Fatalf("opencode-zen native IDs = %#v", got)
	}
}

func opencodeModelServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func buildOpenCodeBackendForTest(t *testing.T, reg *Registry, id string, srv *httptest.Server) execbackend.Backend {
	t.Helper()
	var cfg yaml.Node
	if err := yaml.Unmarshal([]byte("base_url: "+srv.URL+"\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(id, cfg, srv.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("BuildBackend(%q): %v", id, err)
	}
	return be
}

func nativeIDs(models []modelinventory.Model) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.NativeID)
	}
	slices.Sort(out)
	return out
}
