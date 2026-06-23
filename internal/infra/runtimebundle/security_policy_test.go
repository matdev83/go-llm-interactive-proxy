package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
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
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, nil
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: mode})
	if err != nil {
		t.Fatal(err)
	}
}

type billingBackendOptions struct {
	Finalizer bool
	Supported bool
}

func registerBillingBackend(t *testing.T, reg *pluginreg.Registry, factoryID string, opts billingBackendOptions) {
	t.Helper()
	err := reg.RegisterBackend(factoryID, func(yaml.Node, *http.Client) (execbackend.Backend, error) {
		be := execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
			BillingFinalizationSupported: opts.Supported,
		}
		if opts.Finalizer {
			be.FinalizeBilling = func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error) {
				return lipapi.Event{Kind: lipapi.EventUsageDelta, CostSource: "provider_reported"}, nil
			}
		}
		return be, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuild_strictAuthoritativeAccountingRequiresBackendBillingFinalizer(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		finalizer bool
		supported bool
		wantErr   bool
	}{
		{name: "missing finalizer", finalizer: false, wantErr: true},
		{name: "flag without finalizer", supported: true, finalizer: false, wantErr: true},
		{name: "has finalizer", finalizer: true, wantErr: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			factoryID := "billing-" + strings.ReplaceAll(t.Name(), "/", "-")
			reg := pluginreg.NewRegistry()
			registerBillingBackend(t, reg, factoryID, billingBackendOptions{
				Finalizer: tt.finalizer,
				Supported: tt.supported,
			})
			cfg := &config.Config{
				Routing:    config.RoutingConfig{MaxAttempts: 3},
				Continuity: config.ContinuityConfig{InMemory: true},
				Accounting: config.AccountingConfig{StrictAuthoritative: true},
				Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
					Kind: factoryID, ID: "be", Enabled: true,
				}}},
			}

			_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
				PluginRegistry: reg,
			})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "strict_authoritative requires billing finalizer") {
					t.Fatalf("Build err = %v, want strict_authoritative billing finalizer error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build err = %v", err)
			}
		})
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
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	if !config.IsExplicitLoopbackListenAddress(address) {
		cfg.Access = config.AccessConfig{Mode: "multi_user"}
		cfg.Auth = config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"}
		opts.RemoteDecider = &testkit.StubRemoteDecider{}
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), opts)
	return err
}

func TestBuild_oauthUserBackend_allowsOnSingleUserLoopback(t *testing.T) {
	t.Parallel()
	if err := buildWithProfiledBackend(t, "127.0.0.1:8080", "", pluginreg.CredentialOAuthUser); err != nil {
		t.Fatalf("loopback single-user should allow oauth_user backend: %v", err)
	}
}

func TestBuild_oauthUserBackend_rejectsOnNonLoopbackMultiUser(t *testing.T) {
	t.Parallel()
	err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialOAuthUser)
	if err == nil || !errors.Is(err, runtimebundle.ErrOAuthUserDisallowedMultiUser) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrOAuthUserDisallowedMultiUser, err)
	}
}

func TestBuild_oauthUserBackendAllowedWhenSingleUserAccessExternalAuthLoopback(t *testing.T) {
	t.Parallel()
	factoryID := "profiled-oauth-single-user-external-loopback"
	reg := pluginreg.NewRegistry()
	registerProfiledBackend(t, reg, factoryID, pluginreg.CredentialOAuthUser)
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SingleUserLocalMode() {
		t.Fatal("precondition: SingleUserLocalMode must be false when server.auth_mode is external")
	}
	mode, err := cfg.EffectiveAccessMode()
	if err != nil || mode != accessmode.ModeSingleUser {
		t.Fatalf("EffectiveAccessMode: want single_user, got mode=%v err=%v", mode, err)
	}
	_, err = runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		RemoteDecider:  &testkit.StubRemoteDecider{},
	})
	if err != nil {
		t.Fatalf("single_user access with external auth on loopback must allow oauth_user backend: %v", err)
	}
}

func TestBuild_unknownBackendCredentialMode_rejectsOnNonLoopbackMultiUser(t *testing.T) {
	t.Parallel()
	err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialUnknown)
	if err == nil || !errors.Is(err, runtimebundle.ErrUnknownCredentialMultiUser) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrUnknownCredentialMultiUser, err)
	}
}

func TestBuild_staticBackendCredentialModeAllowsExternalAuth(t *testing.T) {
	t.Parallel()
	if err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialStatic); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_noneBackendCredentialModeAllowsExternalAuth(t *testing.T) {
	t.Parallel()
	if err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialNone); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_unsupportedBackendCredentialMode_rejects(t *testing.T) {
	t.Parallel()
	err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.BackendCredentialMode("totally_bogus"))
	if err == nil || !errors.Is(err, runtimebundle.ErrUnsupportedBackendCredentialMode) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrUnsupportedBackendCredentialMode, err)
	}
}
