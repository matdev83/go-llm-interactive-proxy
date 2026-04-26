package runtimebundle_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func registerProfiledBackend(t *testing.T, reg *pluginreg.Registry, factoryID string, mode pluginreg.BackendCredentialMode) {
	t.Helper()
	err := reg.RegisterBackendWithProfile(factoryID, func(yaml.Node, *http.Client) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
				return nil, nil
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: mode})
	if err != nil {
		t.Fatal(err)
	}
}

func buildWithProfiledBackend(t *testing.T, address string, authMode config.AuthMode, mode pluginreg.BackendCredentialMode) error {
	t.Helper()
	factoryID := "profiled-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	registerProfiledBackend(t, reg, factoryID, mode)
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: address, AuthMode: authMode},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	return err
}

func TestBuild_oauthUserBackendRequiresSingleUserLoopback(t *testing.T) {
	t.Parallel()
	if err := buildWithProfiledBackend(t, "127.0.0.1:8080", "", pluginreg.CredentialOAuthUser); err != nil {
		t.Fatalf("loopback single-user should allow oauth_user backend: %v", err)
	}
	err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialOAuthUser)
	if err == nil || !strings.Contains(err.Error(), "oauth_user") {
		t.Fatalf("want oauth_user non-local rejection, got %v", err)
	}
}

func TestBuild_unknownBackendCredentialModeRejectedOutsideSingleUser(t *testing.T) {
	t.Parallel()
	err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialUnknown)
	if err == nil || !strings.Contains(err.Error(), "unknown credential mode") {
		t.Fatalf("want unknown credential mode rejection, got %v", err)
	}
}

func TestBuild_staticBackendCredentialModeAllowsExternalAuth(t *testing.T) {
	t.Parallel()
	if err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialStatic); err != nil {
		t.Fatal(err)
	}
}
