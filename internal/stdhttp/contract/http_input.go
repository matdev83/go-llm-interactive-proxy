// Package contract defines the cycle-neutral HTTP composition input value
// types shared by runtimebundle (canonical construction) and stdhttp
// (canonical and transitional composition). It owns no lifecycle, imports no
// runtimebundle, and imports no root stdhttp (task 3.4 dependency-direction
// contract).
package contract

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	ssessiondiag "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
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
	// GenerationContext is the runtime-owned generation lifecycle context. It
	// cancels when the generation begins shutdown (quiesce/drain) and is passed
	// to every mounted frontend through [lipsdk.FrontendMountOptions] so
	// long-lived transports (WebSocket sessions) close exactly once on retire.
	GenerationContext context.Context
	// FrontendRouteClaims optionally maps a frontend factory id to a provider
	// that computes the normalized owner-aware route claims for one frontend
	// instance. When set, [stdhttp.MountBundledFrontends] validates route
	// ownership (owner-aware claims + canonical path takeover) in a
	// generation-scoped RouteRegistry BEFORE any ServeMux handler is mounted; a
	// conflict fails the candidate atomically with both-owner diagnostics.
	// Frontends without a provider participate in no route-ownership
	// validation, so the seam stays fully generic plugin architecture.
	FrontendRouteClaims map[string]FrontendRouteClaims
}

// FrontendRouteClaims computes the normalized owner-aware route claims for one
// enabled frontend instance. instanceID is the immutable owner identity used
// for the route-ownership registry; cfg is the plugin-local config subtree.
type FrontendRouteClaims func(instanceID string, cfg yaml.Node) ([]RouteClaim, error)

// TrafficSnapshot is the narrow capability a generation extension snapshot
// exposes for traffic-port projection. Both runtimebundle's frozen extension
// snapshot and stdhttp test fixtures satisfy it structurally.
type TrafficSnapshot interface {
	RawCapture() traffic.RawCaptureSink
	TrafficObserver() traffic.Observer
	TrafficRedactors() []traffic.Redactor
}

// TrafficPortsFromSnapshot projects a TrafficSnapshot into an immutable
// traffic.PortBundle with a defensive redactor-slice copy.
func TrafficPortsFromSnapshot(snap TrafficSnapshot) traffic.PortBundle {
	if snap == nil {
		return traffic.PortBundle{}
	}
	return traffic.PortBundle{Raw: snap.RawCapture(), Obs: snap.TrafficObserver(), Red: CloneTrafficRedactors(snap.TrafficRedactors())}
}

// CloneHTTPAuthProviders returns a defensive copy of an auth provider slice.
func CloneHTTPAuthProviders(in []httpauth.Provider) []httpauth.Provider {
	if in == nil {
		return nil
	}
	return append([]httpauth.Provider(nil), in...)
}

// CloneStrings returns a defensive copy of a string slice.
func CloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

// CloneTrafficRedactors returns a defensive copy of a redactor slice.
func CloneTrafficRedactors(in []traffic.Redactor) []traffic.Redactor {
	if in == nil {
		return nil
	}
	return append([]traffic.Redactor(nil), in...)
}

// ClonePluginConfigs returns a deep defensive copy of plugin configuration
// rows, including their nested YAML config nodes.
func ClonePluginConfigs(in []config.PluginConfig) []config.PluginConfig {
	if in == nil {
		return nil
	}
	out := make([]config.PluginConfig, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Config = CloneYAMLNode(in[i].Config)
	}
	return out
}

// CloneRegistrations returns a deep defensive copy of plugin registrations,
// including their nested YAML config nodes.
func CloneRegistrations(in []lipsdk.Registration) []lipsdk.Registration {
	if in == nil {
		return nil
	}
	out := make([]lipsdk.Registration, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Config.Node = CloneYAMLNode(in[i].Config.Node)
	}
	return out
}

// CloneYAMLNode returns a deep defensive copy of a YAML node, including
// aliases and nested content.
func CloneYAMLNode(n yaml.Node) yaml.Node {
	out := n
	if n.Alias != nil {
		cloned := CloneYAMLNode(*n.Alias)
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
		cloned := CloneYAMLNode(*c)
		out.Content[i] = &cloned
	}
	return out
}
