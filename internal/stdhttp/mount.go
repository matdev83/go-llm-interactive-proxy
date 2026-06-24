// Package stdhttp registers bundled frontend HTTP handlers on a ServeMux (standard distribution wiring).
package stdhttp

import (
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// GeminiFrontendID is the factory ID for the Gemini frontend plugin. It is used
// to defer Gemini route registration (broad /v1beta/ prefixes) after narrower
// protocol handlers. Sync tests in mount_constants_test.go verify alignment
// with the canonical value in the gemini package.
const GeminiFrontendID = "gemini"

// MountBundledFrontendsInput carries wiring for [MountBundledFrontends].
type MountBundledFrontendsInput struct {
	Mux                  *http.ServeMux
	Exec                 *runtime.Executor
	DefaultRouteSelector string
	Plugins              []config.PluginConfig
	MaxRequestBodyBytes  int64
	PreRequestKeepalive  lipsdk.FrontendKeepaliveConfig
	Reg                  *pluginreg.Registry
	// TrafficPorts is optional four-leg wiring for client→proxy raw observation (task 10).
	TrafficPorts traffic.PortBundle
}

// MountBundledFrontends registers enabled frontend protocol handlers from config on mux.
// Gemini is mounted under /v1beta/ and /v1beta1/ only (after other prefixes when present).
// MaxRequestBodyBytes is forwarded to handlers; zero means each handler's default body cap.
// Mux, Exec, and Reg must be non-nil.
func MountBundledFrontends(in MountBundledFrontendsInput) error {
	if in.Mux == nil {
		return fmt.Errorf("stdhttp: nil mux")
	}
	if in.Exec == nil {
		return fmt.Errorf("stdhttp: nil exec")
	}
	if in.Reg == nil {
		return fmt.Errorf("stdhttp: nil plugin registry")
	}
	mountALegCancel(in.Mux, in.Exec)
	specific := []config.PluginConfig{}
	geminiLast := []config.PluginConfig{}
	for _, p := range in.Plugins {
		if !p.Enabled {
			continue
		}
		if p.FactoryID() == GeminiFrontendID {
			geminiLast = append(geminiLast, p)
			continue
		}
		specific = append(specific, p)
	}
	ordered := append(specific, geminiLast...)
	for _, p := range ordered {
		if err := in.Reg.MountFrontend(
			p.FactoryID(),
			in.Mux,
			lipsdk.FrontendMountOptions{
				PluginCfg:           p.Config,
				Exec:                in.Exec,
				DefaultRoute:        in.DefaultRouteSelector,
				MaxRequestBodyBytes: in.MaxRequestBodyBytes,
				TrafficPorts:        in.TrafficPorts,
				PreRequestKeepalive: in.PreRequestKeepalive,
			},
		); err != nil {
			return err
		}
	}
	return nil
}

// MountBundledFrontendsLegacy mounts all bundled frontends unconditionally (tests and minimal callers).
func MountBundledFrontendsLegacy(mux *http.ServeMux, exec *runtime.Executor, defaultRouteSelector string, reg *pluginreg.Registry) error {
	return MountBundledFrontends(MountBundledFrontendsInput{
		Mux:                  mux,
		Exec:                 exec,
		DefaultRouteSelector: defaultRouteSelector,
		Plugins:              allBundledFrontendsEnabled(),
		MaxRequestBodyBytes:  0,
		Reg:                  reg,
		TrafficPorts:         traffic.PortBundle{},
	})
}

func allBundledFrontendsEnabled() []config.PluginConfig {
	return []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openai-legacy", Enabled: true},
		{ID: "anthropic", Enabled: true},
		{ID: "gemini", Enabled: true},
	}
}
