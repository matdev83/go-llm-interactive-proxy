// Package main is a separate-module external connector compile fixture. It
// uses only public backend-plugin SDK and host packages.
package main

import (
	"context"
	"fmt"
	"net"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/host"
)

type fakeConnector struct{}

func (*fakeConnector) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{ProtocolMajor: 1, PluginID: "external-fake", Version: "v1"}, nil
}

func (*fakeConnector) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return nil, nil
}

var _ backendplugin.Service = (*fakeConnector)(nil)
var _ func(context.Context, net.Conn, string, string, []byte, backendplugin.SecretBundle, backendplugin.RuntimePolicy) (*host.Session, backendplugin.ResolvedProfile, error) = host.DialConfiguredSession

func main() { fmt.Println("external_connector: ok") }
