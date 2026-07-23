package stdhttp

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	cpadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/controlplane"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

// StandardHTTPInput and its groups are exact aliases of the cycle-neutral
// internal/stdhttp/contract definitions (task 3.4). runtimebundle builds the
// same types directly from internal/stdhttp/contract without importing root
// stdhttp; both packages share this one shape.
type StandardHTTPInput = httpcontract.StandardHTTPInput
type HTTPCoreInput = httpcontract.HTTPCoreInput
type HTTPSecurityInput = httpcontract.HTTPSecurityInput
type HTTPOperationsInput = httpcontract.HTTPOperationsInput
type HTTPModelInput = httpcontract.HTTPModelInput
type HTTPFrontendInput = httpcontract.HTTPFrontendInput

type trafficSnapshot = httpcontract.TrafficSnapshot

func standardHTTPInputFromBuilt(built *runtimebundle.Built, cfg *config.Config, registrations []lipsdk.Registration) StandardHTTPInput {
	if built == nil {
		built = &runtimebundle.Built{}
	}
	route := strings.TrimSpace(built.EffectiveDefaultRoute)
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
		Core: HTTPCoreInput{Executor: built.Executor},
		Security: HTTPSecurityInput{
			HTTPAuthProviders:    cloneHTTPAuthProviders(built.HTTPAuthProviders),
			SecureSessionStore:   built.SecureSessionStore,
			UsageAuthority:       cpadmin.AdaptAccountingAuthorityQueries(built.UsageAuthority),
			ConcurrencyAuthority: cpadmin.AdaptConcurrencyAuthorityQueries(built.ConcurrencyAuthority),
		},
		Operations: HTTPOperationsInput{
			Metrics:              built.Metrics,
			Store:                built.Store,
			SecretGuardInventory: built.SecretGuardInventory,
			ControlPlaneQueries:  cpadmin.AdaptControlPlaneQueries(built.ControlPlaneQueries),
			ReadinessReport:      cpadmin.AdaptReadinessReport(built.ReadinessReport),
			TokenAccountingAdmin: adminaccounting.AdaptCountCallService(built.TokenAccountingAdmin),
			Registrations:        cloneRegistrations(registrations),
		},
		Models: HTTPModelInput{
			CatalogRuntime:       built.CatalogRuntime,
			ModelRegistryRuntime: built.ModelRegistryRuntime,
		},
		Frontends: HTTPFrontendInput{
			Executor:             built.Executor,
			Registry:             built.PluginRegistry,
			DefaultRouteSelector: route,
			RoutePrefixes:        cloneStrings(built.RoutePrefixes),
			Plugins:              clonePluginConfigs(plugins),
			MaxRequestBodyBytes:  maxBody,
			DecodeAdmission:      built.DecodeAdmission,
			TrafficPorts:         trafficPortsFromSnapshot(built.RuntimeSnapshot),
			PreRequestKeepalive:  preKA,
		},
	}
}

func standardHTTPInputFromRequestPlane(plane runtimebundle.RequestPlane) StandardHTTPInput {
	cfg := plane.StackConfig()
	route := strings.TrimSpace(plane.Routing().DefaultRoute)
	if route == "" && cfg != nil {
		route = DefaultRouteSelector(cfg)
	}
	var maxBody int64
	var preKA lipsdk.FrontendKeepaliveConfig
	if cfg != nil {
		maxBody = cfg.Server.EffectiveMaxRequestBodyBytes()
		ka := cfg.Server.EffectivePreRequestKeepalive()
		preKA = lipsdk.FrontendKeepaliveConfig{Enabled: ka.Enabled, Interval: ka.Interval}
	}
	routing := plane.Routing()
	return StandardHTTPInput{
		Core: HTTPCoreInput{Executor: plane.Executor()},
		Security: HTTPSecurityInput{
			HTTPAuthProviders:    cloneHTTPAuthProviders(plane.HTTPAuthProviders()),
			SecureSessionStore:   plane.SecureSessionStore(),
			UsageAuthority:       cpadmin.AdaptAccountingAuthorityQueries(plane.UsageAuthority()),
			ConcurrencyAuthority: cpadmin.AdaptConcurrencyAuthorityQueries(plane.ConcurrencyAuthority()),
		},
		Operations: HTTPOperationsInput{
			Metrics:              plane.Metrics(),
			Store:                plane.Store(),
			SecretGuardInventory: plane.SecretGuardInventory(),
			ControlPlaneQueries:  cpadmin.AdaptControlPlaneQueries(plane.ControlPlaneQueries()),
			ReadinessReport:      cpadmin.AdaptReadinessReport(plane.ReadinessReport()),
			TokenAccountingAdmin: adminaccounting.AdaptCountCallService(plane.TokenAccountingAdmin()),
			Registrations:        cloneRegistrations(plane.Registrations()),
		},
		Models: HTTPModelInput{
			CatalogRuntime:       plane.CatalogRuntime(),
			ModelRegistryRuntime: plane.ModelRegistryRuntime(),
		},
		Frontends: HTTPFrontendInput{
			Executor:             plane.Executor(),
			Registry:             plane.PluginRegistry(),
			DefaultRouteSelector: route,
			RoutePrefixes:        cloneStrings(routing.RoutePrefixes),
			Plugins:              clonePluginConfigs(plane.Frontends()),
			MaxRequestBodyBytes:  maxBody,
			DecodeAdmission:      plane.DecodeAdmission(),
			TrafficPorts:         trafficPortsFromSnapshot(plane.RuntimeSnapshot()),
			PreRequestKeepalive:  preKA,
		},
	}
}

func trafficPortsFromSnapshot(snap trafficSnapshot) traffic.PortBundle {
	return httpcontract.TrafficPortsFromSnapshot(snap)
}

func cloneHTTPAuthProviders(in []httpauth.Provider) []httpauth.Provider {
	return httpcontract.CloneHTTPAuthProviders(in)
}
func cloneStrings(in []string) []string { return httpcontract.CloneStrings(in) }
func cloneTrafficRedactors(in []traffic.Redactor) []traffic.Redactor {
	return httpcontract.CloneTrafficRedactors(in)
}
func clonePluginConfigs(in []config.PluginConfig) []config.PluginConfig {
	return httpcontract.ClonePluginConfigs(in)
}
func cloneRegistrations(in []lipsdk.Registration) []lipsdk.Registration {
	return httpcontract.CloneRegistrations(in)
}
func cloneYAMLNode(n yaml.Node) yaml.Node { return httpcontract.CloneYAMLNode(n) }
