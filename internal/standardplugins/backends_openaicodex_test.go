package standardplugins

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
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
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\naccess_token: sk-codex\naccount_id: acct-99\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
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
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
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

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := `
access_token: sk-codex
models:
  items:
    - canonical_id: openai-codex/gpt-5.3-codex-spark
      native_id: gpt-5.3-codex-spark
    - canonical_id: openai-codex/gpt-5.4
      native_id: gpt-5.4
`
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := nativeIDs(snap.Models); !slices.Equal(got, []string{"gpt-5.3-codex-spark", "gpt-5.4"}) {
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

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\nauth_json_path: " + authPath + "\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
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
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\napi_keys:\n  - first-key\n  - second-key\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
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
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\ncredentials:\n  - id: acct-1\n    api_key: cred-token\n    remote_account_id: acct-remote\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
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
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{OpenAICodex: []string{"env-codex", "env-codex-2"}}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.LatestRequest().Authorization != "Bearer env-codex" {
		t.Fatalf("authorization: %q", srv.LatestRequest().Authorization)
	}
}

func TestOpenAICodexBackendFactory_ignoresDefaultTemperatureYAML(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\naccess_token: sk-codex\ndefault_temperature: 0.7\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	es, err := be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
	if err != nil {
		t.Fatalf("default_temperature must not be plumbed or rejected: %v", err)
	}
	drainOpenAICodexStream(t, es)
	body := srv.LatestRequest().Body
	if _, ok := body["temperature"]; ok {
		t.Fatalf("temperature must not appear in payload: %#v", body)
	}
}

func TestOpenAICodexBackendFactory_transportHTTPSFromYAML(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\naccess_token: sk-codex\ntransport: https\nwebsocket_fallback_cooldown_seconds: 1\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	es, err := be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
	if err != nil {
		t.Fatal(err)
	}
	drainOpenAICodexStream(t, es)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("transport = %q, want https", got)
	}
}

func TestOpenAICodexBackendFactory_invalidTransportFromYAML(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(refbackend.New(refbackend.Config{Token: "sk-codex"}).Handler())
	t.Cleanup(ts.Close)
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\naccess_token: sk-codex\ntransport: quic\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
	if err == nil {
		t.Fatal("expected invalid transport config error")
	}
}

func drainOpenAICodexStream(t *testing.T, es lipapi.ManagedEventStream) {
	t.Helper()
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
}

func TestOpenAICodexBackendFactory_apiKeyAliasForAccessToken(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{Token: "from-api-key", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	yamlText := "base_url: " + ts.URL + "/backend-api/codex\napi_key: from-api-key\n"
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.LatestRequest().Authorization != "Bearer from-api-key" {
		t.Fatalf("authorization: %q", srv.LatestRequest().Authorization)
	}
}

func latestCodexVerbosity(t *testing.T, srv *refbackend.Server) string {
	t.Helper()
	body := srv.LatestRequest().Body
	if body == nil {
		return ""
	}
	text, _ := body["text"].(map[string]any)
	if text == nil {
		return ""
	}
	v, _ := text["verbosity"].(string)
	return v
}

func openCodexTurn(t *testing.T, be execbackend.Backend, call lipapi.Call) {
	t.Helper()
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOpenAICodexStream(t, es)
}

func buildOpenAICodexBackend(t *testing.T, ts *httptest.Server, yamlText string) execbackend.Backend {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var cfg yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend("openai-codex", cfg, ts.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	return be
}

// TestOpenAICodexBackendFactory_earlySessionBumpDefaultOnForFirstFiveTurns verifies
// the bump is enabled by default (no config): the first 5 turns of a conversation
// emit text.verbosity=high when no explicit per-request verbosity is set, and turn 6
// reverts to the normal resolution (no forced verbosity).
func TestOpenAICodexBackendFactory_earlySessionBumpDefaultOnForFirstFiveTurns(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\n")

	mkCall := func() lipapi.Call {
		return lipapi.Call{
			Session:  lipapi.SessionRef{ContinuityKey: "conv-bump-e2e"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		}
	}
	for turn := 1; turn <= 5; turn++ {
		openCodexTurn(t, be, mkCall())
		if got := latestCodexVerbosity(t, srv); got != "high" {
			t.Fatalf("turn %d: verbosity = %q, want high", turn, got)
		}
	}
	openCodexTurn(t, be, mkCall())
	if got := latestCodexVerbosity(t, srv); got == "high" {
		t.Fatalf("turn 6: verbosity = high, want not high (early window elapsed)")
	}
}

// TestOpenAICodexBackendFactory_earlySessionBumpDisabledOptOut verifies the bump
// can be disabled via early_session_verbosity_bump_disabled: true, so the first
// turn does not force high verbosity.
func TestOpenAICodexBackendFactory_earlySessionBumpDisabledOptOut(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\nearly_session_verbosity_bump_disabled: true\n")

	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-bump-disabled"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	openCodexTurn(t, be, call)
	if got := latestCodexVerbosity(t, srv); got == "high" {
		t.Fatalf("disabled: turn 1 verbosity = high, want not forced (opt-out)")
	}
}

// TestOpenAICodexBackendFactory_earlySessionBumpTurnsTwoRevertsByTurnThree verifies
// early_session_verbosity_bump_turns narrows the window: turns 1 and 2 are high,
// turn 3 reverts to normal resolution.
func TestOpenAICodexBackendFactory_earlySessionBumpTurnsTwoRevertsByTurnThree(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\nearly_session_verbosity_bump_turns: 2\n")

	mkCall := func() lipapi.Call {
		return lipapi.Call{
			Session:  lipapi.SessionRef{ContinuityKey: "conv-bump-two"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		}
	}
	for _, turn := range []int{1, 2} {
		openCodexTurn(t, be, mkCall())
		if got := latestCodexVerbosity(t, srv); got != "high" {
			t.Fatalf("turn %d: verbosity = %q, want high", turn, got)
		}
	}
	openCodexTurn(t, be, mkCall())
	if got := latestCodexVerbosity(t, srv); got == "high" {
		t.Fatalf("turn 3: verbosity = high, want not high (window of 2 elapsed)")
	}
}

// TestOpenAICodexBackendFactory_earlySessionBumpExplicitVerbosityWins verifies an
// explicit per-request verbosity (here the request-body option, which the planner
// merges from URI params too) is honored even on turn 1, overriding the bump.
func TestOpenAICodexBackendFactory_earlySessionBumpExplicitVerbosityWins(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\n")

	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-bump-explicit"},
		Options:  lipapi.GenerationOptions{Verbosity: lipapi.VerbosityLow},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	openCodexTurn(t, be, call)
	if got := latestCodexVerbosity(t, srv); got != "low" {
		t.Fatalf("explicit low must win on turn 1, got %q", got)
	}
}

// TestOpenAICodexBackendFactory_midSessionBumpDefaultOnForTurns10And20 verifies
// the periodic mid-session bump is enabled by default and fires on turns 10 and
// 20 while leaving the surrounding turns at normal verbosity.
func TestOpenAICodexBackendFactory_midSessionBumpDefaultOnForTurns10And20(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\n")
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-mid-default"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	for turn := 1; turn <= 20; turn++ {
		openCodexTurn(t, be, call)
		got := latestCodexVerbosity(t, srv)
		switch {
		case turn <= 5 || turn == 10 || turn == 20:
			if got != "high" {
				t.Fatalf("turn %d: verbosity = %q, want high", turn, got)
			}
		default:
			if got == "high" {
				t.Fatalf("turn %d: verbosity = high, want normal", turn)
			}
		}
	}
}

// TestOpenAICodexBackendFactory_defaultVerbosityIsOverriddenByEarlyAndMidBumps
// verifies that the YAML factory wiring preserves default_verbosity on the
// non-bump turns while the early and mid-session bump windows still force high.
func TestOpenAICodexBackendFactory_defaultVerbosityIsOverriddenByEarlyAndMidBumps(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\ndefault_verbosity: low\nearly_session_verbosity_bump_turns: 2\nmid_session_verbosity_bump_frequency: 6\n")
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-default-verbosity"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	for turn := 1; turn <= 6; turn++ {
		openCodexTurn(t, be, call)
		got := latestCodexVerbosity(t, srv)
		switch turn {
		case 1, 2, 6:
			if got != "high" {
				t.Fatalf("turn %d: verbosity = %q, want high", turn, got)
			}
		default:
			if got != "low" {
				t.Fatalf("turn %d: verbosity = %q, want low", turn, got)
			}
		}
	}
}

// TestOpenAICodexBackendFactory_midSessionBumpDisabledOptOut verifies the
// mid-session bump can be disabled independently of the early-session bump.
func TestOpenAICodexBackendFactory_midSessionBumpDisabledOptOut(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\nmid_session_verbosity_bump_disabled: true\n")
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-mid-disabled"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	for turn := 1; turn <= 10; turn++ {
		openCodexTurn(t, be, call)
	}
	if got := latestCodexVerbosity(t, srv); got == "high" {
		t.Fatalf("disabled mid-session bump must not force high on turn 10")
	}
}

// TestOpenAICodexBackendFactory_midSessionBumpDisabledAllowsOutOfWindowFrequency
// verifies that an early-only configuration can still load when the mid-session
// bump is disabled, even if the configured cadence would otherwise be invalid.
func TestOpenAICodexBackendFactory_midSessionBumpDisabledAllowsOutOfWindowFrequency(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\nearly_session_verbosity_bump_turns: 10\nmid_session_verbosity_bump_disabled: true\nmid_session_verbosity_bump_frequency: 10\n")
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-mid-disabled-frequency"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	openCodexTurn(t, be, call)
	if got := latestCodexVerbosity(t, srv); got != "high" {
		t.Fatalf("disabled mid-session config should still allow the early bump, got %q", got)
	}
}

// TestOpenAICodexBackendFactory_midSessionBumpExplicitVerbosityWins verifies that
// an explicit per-request verbosity wins even on a frequency turn and the
// conversation still advances to the next scheduled mid-session hit.
func TestOpenAICodexBackendFactory_midSessionBumpExplicitVerbosityWins(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\nearly_session_verbosity_bump_disabled: true\nmid_session_verbosity_bump_frequency: 6\n")
	plainCall := lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-mid-explicit"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	for turn := 1; turn <= 5; turn++ {
		openCodexTurn(t, be, plainCall)
	}
	explicitCall := plainCall
	explicitCall.Options = lipapi.GenerationOptions{Verbosity: lipapi.VerbosityLow}
	openCodexTurn(t, be, explicitCall)
	if got := latestCodexVerbosity(t, srv); got != "low" {
		t.Fatalf("explicit low must win on turn 6, got %q", got)
	}
	for turn := 7; turn <= 11; turn++ {
		openCodexTurn(t, be, plainCall)
	}
	openCodexTurn(t, be, plainCall)
	if got := latestCodexVerbosity(t, srv); got != "high" {
		t.Fatalf("turn 12 should hit the 6-turn cadence, got %q", got)
	}
}

// TestOpenAICodexBackendFactory_midSessionBumpRejectsInvalidFrequency verifies
// that a mid-session frequency at or below the early-session window is rejected.
func TestOpenAICodexBackendFactory_midSessionBumpRejectsInvalidFrequency(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := buildOpenAICodexBackend(t, ts, "base_url: "+ts.URL+"/backend-api/codex\naccess_token: sk-codex\nearly_session_verbosity_bump_turns: 10\nmid_session_verbosity_bump_frequency: 10\n")
	_, err := be.Open(context.Background(), lipapi.Call{
		Session:  lipapi.SessionRef{ContinuityKey: "conv-mid-invalid"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}})
	if err == nil {
		t.Fatal("expected invalid mid-session frequency error")
	}
	if !strings.Contains(err.Error(), "mid_session_verbosity_bump_frequency") {
		t.Fatalf("error = %v", err)
	}
}
