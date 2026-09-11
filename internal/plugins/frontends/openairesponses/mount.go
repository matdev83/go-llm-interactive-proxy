package openairesponses

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// Mount registers the OpenAI Responses API handler on mux.
func Mount(mux *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
	cfg, err := DecodeConfig(opts.PluginCfg)
	if err != nil {
		return err
	}
	var lpCfg frontendpipe.LargePayloadConfig
	if c, ok := opts.LargePayload.(frontendpipe.LargePayloadConfig); ok {
		lpCfg = c
	}
	mux.Handle("/v1/responses", &Handler{
		Exec:                    opts.Exec,
		DefaultRouteSelector:    opts.DefaultRoute,
		RoutePrefixes:           routeselect.NewPrefixSet(opts.RoutePrefixes),
		MaxRequestBodyBytes:     opts.MaxRequestBodyBytes,
		DecodeAdmission:         opts.DecodeAdmission,
		TrafficPorts:            opts.TrafficPorts,
		PreRequestKeepalive:     opts.PreRequestKeepalive,
		HTTPHeaders:             opts.HTTPHeaders,
		StreamKeepaliveInterval: opts.StreamKeepaliveInterval,
		Config:                  cfg,
		LargePayload:            lpCfg,
	})
	return nil
}
