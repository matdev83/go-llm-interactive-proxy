package stdhttp

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	ssessiondiag "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	cpadmin "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/controlplane"
	adminaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/tokenaccounting"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipcp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

// StandardHTTPInput is the focused, lifecycle-free HTTP composition projection.
type StandardHTTPInput struct {
	Core       HTTPCoreInput
	Security   HTTPSecurityInput
	Operations HTTPOperationsInput
	Models     HTTPModelInput
	Frontends  HTTPFrontendInput
}

type HTTPCoreInput struct {
	Executor *runtime.Executor
}

// HTTPSecurityInput uses adapter-owned narrow interfaces (no root core app imports).
type HTTPSecurityInput struct {
	HTTPAuthProviders    []httpauth.Provider
	SecureSessionStore   ssessiondiag.Store
	UsageAuthority       cpadmin.AccountingAuthorityQueries
	ConcurrencyAuthority cpadmin.ConcurrencyAuthorityQueries
}

type HTTPOperationsInput struct {
	Metrics              *metrics.Bundle
	Store                diag.AttemptLoader
	SecretGuardInventory *diag.InventoryExtras
	ControlPlaneQueries  lipcp.Queries
	ReadinessReport      lipcp.ReadinessReportReader
	TokenAccountingAdmin adminaccounting.Service
	Registrations        []lipsdk.Registration
}

type HTTPModelInput struct {
	CatalogRuntime       *modelcatalog.CatalogRuntime
	ModelRegistryRuntime *modelregistry.Runtime
}

type HTTPFrontendInput struct {
	Executor             *runtime.Executor
	Registry             *pluginreg.Registry
	DefaultRouteSelector string
	RoutePrefixes        []string
	Plugins              []config.PluginConfig
	MaxRequestBodyBytes  int64
	DecodeAdmission      lipsdk.DecodeAdmission
	TrafficPorts         traffic.PortBundle
	PreRequestKeepalive  lipsdk.FrontendKeepaliveConfig
}

type trafficSnapshot interface {
	RawCapture() traffic.RawCaptureSink
	TrafficObserver() traffic.Observer
	TrafficRedactors() []traffic.Redactor
}

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
	if snap == nil {
		return traffic.PortBundle{}
	}
	return traffic.PortBundle{Raw: snap.RawCapture(), Obs: snap.TrafficObserver(), Red: cloneTrafficRedactors(snap.TrafficRedactors())}
}

func cloneHTTPAuthProviders(in []httpauth.Provider) []httpauth.Provider {
	if in == nil {
		return nil
	}
	return append([]httpauth.Provider(nil), in...)
}
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}
func cloneTrafficRedactors(in []traffic.Redactor) []traffic.Redactor {
	if in == nil {
		return nil
	}
	return append([]traffic.Redactor(nil), in...)
}
func clonePluginConfigs(in []config.PluginConfig) []config.PluginConfig {
	if in == nil {
		return nil
	}
	out := make([]config.PluginConfig, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Config = cloneYAMLNode(in[i].Config)
	}
	return out
}
func cloneRegistrations(in []lipsdk.Registration) []lipsdk.Registration {
	if in == nil {
		return nil
	}
	out := make([]lipsdk.Registration, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Config.Node = cloneYAMLNode(in[i].Config.Node)
	}
	return out
}
func cloneYAMLNode(n yaml.Node) yaml.Node {
	out := n
	if n.Alias != nil {
		cloned := cloneYAMLNode(*n.Alias)
		out.Alias = &cloned
	}
	if len(n.Content) == 0 {
		out.Content = nil
		return out
	}
	out.Content = make([]*yaml.Node, len(n.Content))
	for i, c := range n.Content {
		if c == nil {
			continue
		}
		cloned := cloneYAMLNode(*c)
		out.Content[i] = &cloned
	}
	return out
}
