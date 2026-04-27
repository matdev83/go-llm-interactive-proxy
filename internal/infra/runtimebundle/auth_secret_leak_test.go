package runtimebundle_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/authevent"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	stdhttpauth "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

func assertNoAuthFixtureLeak(tb testing.TB, haystacks ...string) {
	tb.Helper()
	for _, h := range haystacks {
		for _, s := range authevent.AuthLeakFixtureSecrets {
			if strings.Contains(h, s) {
				tb.Fatalf("fixture secret leaked into output: substring of %q found in:\n%s", s, h)
			}
		}
	}
}

func TestBuild_authPaths_noFixtureSecretLeakageInLogsOrHTTPBodies(t *testing.T) {
	t.Parallel()
	secret := authevent.AuthLeakFixtureSecrets[0]
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBackendsOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "k1", PrincipalID: "u1", Key: secret},
			},
		},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}

	h := stdhttpauth.Middleware(nil, b.HTTPAuthProviders, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run for deny cases in this test")
	}))

	// Missing credentials
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assertNoAuthFixtureLeak(t, rec1.Body.String(), logBuf.String())

	// Wrong bearer (must not echo configured secret material)
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req2.Header.Set("Authorization", "Bearer wrong-not-the-secret")
	h.ServeHTTP(rec2, req2)
	assertNoAuthFixtureLeak(t, rec2.Body.String(), logBuf.String())

	// Allow path: principal logs should still not echo raw secret
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req3.Header.Set("Authorization", "Bearer "+secret)
	var inner bool
	h2 := stdhttpauth.Middleware(nil, b.HTTPAuthProviders, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		inner = true
		if _, ok := httpauth.PrincipalFromContext(r.Context()); !ok {
			t.Error("expected principal")
		}
	}))
	h2.ServeHTTP(rec3, req3)
	if !inner {
		t.Fatal("expected allow through inner")
	}
	assertNoAuthFixtureLeak(t, rec3.Body.String(), logBuf.String())
}

func TestValidate_authLocalAPIKeyRecord_errorsDoNotEchoRawSecret(t *testing.T) {
	t.Parallel()
	secret := authevent.AuthLeakFixtureSecrets[0]
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "", PrincipalID: "u1", Key: secret},
			},
		},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	assertNoAuthFixtureLeak(t, err.Error())
}
