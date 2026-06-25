package pluginreg

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestStandardBackendBundle_includesOpenAICodex(t *testing.T) {
	t.Parallel()

	be := StandardBackendBundle(UpstreamAPIKeys{})
	got := make([]string, 0, len(be.Backends))
	for _, entry := range be.Backends {
		got = append(got, entry.ID)
	}
	if !slices.Contains(got, "openai-codex") {
		t.Fatalf("backend IDs = %#v, missing openai-codex", got)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openAICodexAccessToken(t *testing.T) { //nolint:paralleltest // mutates process env via t.Setenv
	clearAllProviderEnv(t)
	clearNumberedEnv(t, "OPENAI_CODEX_ACCESS_TOKEN")
	clearNumberedEnv(t, "OPENAI_CODEX_API_KEY")
	t.Setenv("OPENAI_CODEX_ACCESS_TOKEN", "codex-1")
	t.Setenv("OPENAI_CODEX_ACCESS_TOKEN_2", "codex-2")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"codex-1", "codex-2"}
	if !reflect.DeepEqual(got.OpenAICodex, want) {
		t.Fatalf("OpenAICodex keys: %#v want %#v", got.OpenAICodex, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openAICodexFallsBackToAPIKey(t *testing.T) { //nolint:paralleltest // mutates process env via t.Setenv
	clearAllProviderEnv(t)
	clearNumberedEnv(t, "OPENAI_CODEX_ACCESS_TOKEN")
	clearNumberedEnv(t, "OPENAI_CODEX_API_KEY")
	t.Setenv("OPENAI_CODEX_ACCESS_TOKEN", "")
	t.Setenv("OPENAI_CODEX_API_KEY", "codex-api")
	t.Setenv("OPENAI_CODEX_API_KEY_2", "codex-api-2")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"codex-api", "codex-api-2"}
	if !reflect.DeepEqual(got.OpenAICodex, want) {
		t.Fatalf("OpenAICodex keys: %#v want %#v", got.OpenAICodex, want)
	}
}

func TestOpenAICodexBackendFactory_buildsFromYAMLAndHitsRefEmulator(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "wired-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\naccess_token: sk-codex\naccount_id: acct-99\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		ID: "call-1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err := es.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = es.Close()
	got := srv.LatestRequest()
	if got.Authorization != "Bearer sk-codex" {
		t.Fatalf("authorization: %q", got.Authorization)
	}
	if got.ChatGPTAccountID != "acct-99" {
		t.Fatalf("account id: %q", got.ChatGPTAccountID)
	}
}

func TestOpenAICodexBackendFactory_configuredModelsFlowToInventory(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := `
access_token: sk-codex
models:
  items:
    - canonical_id: openai-codex/gpt-5.3-codex
      native_id: gpt-5.3-codex
    - canonical_id: openai-codex/gpt-5.4
      native_id: gpt-5.4
`
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, nil, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := nativeIDs(snap.Models); !slices.Equal(got, []string{"gpt-5.3-codex", "gpt-5.4"}) {
		t.Fatalf("native IDs = %#v", got)
	}
}

func TestOpenAICodexBackendFactory_authJSONPath(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "yaml-auth-json", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"yaml-auth-json"},"account_id":"acct-yaml"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\nauth_json_path: " + authPath + "\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.LatestRequest()
	if got.Authorization != "Bearer yaml-auth-json" {
		t.Fatalf("authorization: %q", got.Authorization)
	}
	if got.ChatGPTAccountID != "acct-yaml" {
		t.Fatalf("account id: %q", got.ChatGPTAccountID)
	}
}

func TestOpenAICodexBackendFactory_apiKeysFirstKeyUsed(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "first-key", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\napi_keys:\n  - first-key\n  - second-key\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.LatestRequest().Authorization != "Bearer first-key" {
		t.Fatalf("authorization: %q", srv.LatestRequest().Authorization)
	}
}

func TestOpenAICodexBackendFactory_credentialsAPIKey(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "cred-token", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\ncredentials:\n  - id: acct-1\n    api_key: cred-token\n    remote_account_id: acct-remote\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.LatestRequest().Authorization != "Bearer cred-token" {
		t.Fatalf("authorization: %q", srv.LatestRequest().Authorization)
	}
}

func TestOpenAICodexBackendFactory_envFallbackWhenYAMLHasNoKeys(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "env-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{OpenAICodex: []string{"env-codex", "env-codex-2"}}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.LatestRequest().Authorization != "Bearer env-codex" {
		t.Fatalf("authorization: %q", srv.LatestRequest().Authorization)
	}
}

func TestOpenAICodexBackendFactory_apiKeyAliasForAccessToken(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "from-api-key", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\napi_key: from-api-key\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.LatestRequest().Authorization != "Bearer from-api-key" {
		t.Fatalf("authorization: %q", srv.LatestRequest().Authorization)
	}
}
