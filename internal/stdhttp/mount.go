// Package stdhttp registers bundled frontend HTTP handlers on a ServeMux (standard distribution wiring).
package stdhttp

import (
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// geminiFrontendID is the factory ID for the Gemini frontend plugin. It is used
// to defer Gemini route registration (broad /v1beta/ prefixes) after narrower
// protocol handlers. Sync tests in mount_constants_test.go verify alignment
// with the canonical value in the gemini package.
const geminiFrontendID = "gemini"

// MountBundledFrontendsInput carries wiring for [MountBundledFrontends].
type MountBundledFrontendsInput struct {
	Mux       *http.ServeMux
	Frontends HTTPFrontendInput
}

// MountBundledFrontends registers enabled frontend protocol handlers from config on mux.
// Gemini is mounted under /v1beta/ and /v1beta1/ only (after other prefixes when present).
// MaxRequestBodyBytes is forwarded to handlers; zero means each handler's default body cap.
// Mux, Frontends.Executor, and Frontends.Registry must be non-nil.
func MountBundledFrontends(in MountBundledFrontendsInput) error {
	fe := in.Frontends
	if in.Mux == nil {
		return fmt.Errorf("stdhttp: nil mux")
	}
	if fe.Executor == nil {
		return fmt.Errorf("stdhttp: nil exec")
	}
	if fe.Registry == nil {
		return fmt.Errorf("stdhttp: nil plugin registry")
	}
	mountALegCancel(in.Mux, fe)
	specific := []config.PluginConfig{}
	geminiLast := []config.PluginConfig{}
	for _, p := range fe.Plugins {
		if !p.Enabled {
			continue
		}
		if p.FactoryID() == geminiFrontendID {
			geminiLast = append(geminiLast, p)
			continue
		}
		specific = append(specific, p)
	}
	ordered := append(specific, geminiLast...)
	return callMount(func() error {
		for _, p := range ordered {
			if err := fe.Registry.MountFrontend(
				p.FactoryID(),
				in.Mux,
				lipsdk.FrontendMountOptions{
					PluginCfg:           p.Config,
					Exec:                fe.Executor,
					DefaultRoute:        fe.DefaultRouteSelector,
					RoutePrefixes:       fe.RoutePrefixes,
					MaxRequestBodyBytes: fe.MaxRequestBodyBytes,
					DecodeAdmission:     fe.DecodeAdmission,
					TrafficPorts:        fe.TrafficPorts,
					PreRequestKeepalive: fe.PreRequestKeepalive,
				},
			); err != nil {
				return err
			}
		}
		return nil
	})
}
