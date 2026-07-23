package stdhttp

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
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
