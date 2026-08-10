package openailegacy

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// Mount registers the legacy OpenAI chat completions handler on mux.
func Mount(mux *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
	cfg, err := DecodeConfig(opts.PluginCfg)
	if err != nil {
		return err
	}
	claims, err := RouteClaims(ID)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		mux.Handle(claim.Path, &Handler{
			Exec:                 opts.Exec,
			DefaultRouteSelector: opts.DefaultRoute,
			RoutePrefixes:        routeselect.NewPrefixSet(opts.RoutePrefixes),
			MaxRequestBodyBytes:  opts.MaxRequestBodyBytes,
			DecodeAdmission:      opts.DecodeAdmission,
			TrafficPorts:         opts.TrafficPorts,
			PreRequestKeepalive:  opts.PreRequestKeepalive,
			Config:               cfg,
		})
	}
	return nil
}
