package stdhttp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	cpadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/controlplane"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// startTestApp starts app and registers Shutdown cleanup. Lifecycle only —
// callers compose HTTP separately via ComposeStandardHTTP.
func startTestApp(t *testing.T, ctx context.Context, app *runtime.App) {
	t.Helper()
	if app == nil {
		t.Fatal("nil app")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start app: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		app.Shutdown(shutdownCtx)
	})
}

// frontendInputForTest builds a focused Frontends group from cfg + executor/registry.
func frontendInputForTest(cfg *config.Config, ex *runtime.Executor, reg *pluginreg.Registry) HTTPFrontendInput {
	route := ""
	var maxBody int64
	var preKA lipsdk.FrontendKeepaliveConfig
	var plugins []config.PluginConfig
	if cfg != nil {
		route = DefaultRouteSelector(cfg)
		maxBody = cfg.Server.EffectiveMaxRequestBodyBytes()
		ka := cfg.Server.EffectivePreRequestKeepalive()
		preKA = lipsdk.FrontendKeepaliveConfig{Enabled: ka.Enabled, Interval: ka.Interval}
		plugins = cfg.Plugins.Frontends
	}
	return HTTPFrontendInput{
		Executor:             ex,
		Registry:             reg,
		DefaultRouteSelector: route,
		Plugins:              httpcontract.ClonePluginConfigs(plugins),
		MaxRequestBodyBytes:  maxBody,
		PreRequestKeepalive:  preKA,
	}
}

// candidateHTTPInput projects a real CandidateRuntime into focused StandardHTTPInput
// groups without an intermediate Built-mirroring struct.
func candidateHTTPInput(cfg *config.Config, cand *runtimebundle.CandidateRuntime, regs []lipsdk.Registration) StandardHTTPInput {
	if cand == nil {
		return StandardHTTPInput{}
	}
	route := strings.TrimSpace(cand.EffectiveDefaultRoute)
	if route == "" && cfg != nil {
		route = DefaultRouteSelector(cfg)
	}
	var maxBody int64
	var preKA lipsdk.FrontendKeepaliveConfig
	var plugins []config.PluginConfig
	if cfg != nil {
		maxBody = cfg.Server.EffectiveMaxRequestBodyBytes()
		ka := cfg.Server.EffectivePreRequestKeepalive()
		preKA = lipsdk.FrontendKeepaliveConfig{Enabled: ka.Enabled, Interval: ka.Interval}
		plugins = cfg.Plugins.Frontends
	}
	return StandardHTTPInput{
		Core: HTTPCoreInput{Executor: cand.Executor},
		Security: HTTPSecurityInput{
			HTTPAuthProviders:    httpcontract.CloneHTTPAuthProviders(cand.HTTPAuthProviders),
			SecureSessionStore:   cand.SecureSessionStore,
			UsageAuthority:       cpadmin.AdaptAccountingAuthorityQueries(cand.UsageAuthority),
			ConcurrencyAuthority: cpadmin.AdaptConcurrencyAuthorityQueries(cand.ConcurrencyAuthority),
		},
		Operations: HTTPOperationsInput{
			Metrics:              cand.Metrics,
			Store:                cand.Store,
			SecretGuardInventory: cand.SecretGuardInventory,
			ControlPlaneQueries:  cpadmin.AdaptControlPlaneQueries(cand.ControlPlaneQueries),
			ReadinessReport:      cpadmin.AdaptReadinessReport(cand.ReadinessReport),
			TokenAccountingAdmin: adminaccounting.AdaptCountCallService(cand.TokenAccountingAdmin),
			Registrations:        httpcontract.CloneRegistrations(regs),
		},
		Models: HTTPModelInput{
			CatalogRuntime:       cand.CatalogRuntime,
			ModelRegistryRuntime: cand.ModelRegistryRuntime,
		},
		Frontends: HTTPFrontendInput{
			Executor:             cand.Executor,
			Registry:             cand.PluginRegistry,
			DefaultRouteSelector: route,
			RoutePrefixes:        httpcontract.CloneStrings(cand.RoutePrefixes),
			Plugins:              httpcontract.ClonePluginConfigs(plugins),
			MaxRequestBodyBytes:  maxBody,
			DecodeAdmission:      cand.DecodeAdmission,
			TrafficPorts:         httpcontract.TrafficPortsFromSnapshot(cand.RuntimeSnapshot),
			PreRequestKeepalive:  preKA,
		},
	}
}

func compileTestCandidate(t *testing.T, cfg *config.Config, reg *pluginreg.Registry) (*runtimebundle.ProcessServices, *runtimebundle.CandidateRuntime) {
	t.Helper()
	if reg == nil {
		reg = pluginreg.NewRegistry()
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
	})
	if err != nil {
		_ = ps.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cand.Close()
		_ = ps.Close()
	})
	return ps, cand
}
