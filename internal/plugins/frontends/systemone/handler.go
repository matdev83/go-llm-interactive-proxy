package systemone

import (
	"context"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func handlerSpec(opts lipsdk.FrontendMountOptions, cfg Config) *frontendpipe.Spec[struct{}] {
	prefixes := routeselect.NewPrefixSet(opts.RoutePrefixes)
	return &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: opts.Exec, DefaultRouteSelector: opts.DefaultRoute, RoutePrefixes: prefixes,
			MaxRequestBodyBytes: opts.MaxRequestBodyBytes, DecodeAdmission: opts.DecodeAdmission,
			TrafficPorts: opts.TrafficPorts, HTTPHeaders: opts.HTTPHeaders, FrontendID: ID,
		},
		Wire: WireErrors{}, ClassifyExecute: classifyExecute, RouteFromBodyModel: true,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, path == "/v1/systemone"
		},
		ResolveRouteSelector: func(_ *http.Request, body []byte, _ frontendpipe.PathMatch) string {
			// Route only from the body model/operator default, not client headers.
			return prefixes.FromModelOrDefault(body, opts.DefaultRoute)
		},
		Decode: func(ctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			call, err := decodeRequest(ctx.Body, cfg.MaxQuestions)
			if err != nil {
				return nil, err
			}
			if ctx.RouteSelector != "" {
				call.Route.Selector = ctx.RouteSelector
			}
			return &frontendpipe.Decoded{Call: call, RouteSelector: call.Route.Selector}, nil
		},
		BuildEncodeOpts: func(*frontendpipe.Decoded) struct{} { return struct{}{} },
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, stream lipapi.EventStream, _ struct{}) error {
			return writeResponse(ctx, w, call, stream)
		},
	}
}
