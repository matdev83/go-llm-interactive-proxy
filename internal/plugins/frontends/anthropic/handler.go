package anthropic

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

const (
	HeaderRouteSelector    = routeselect.HeaderRouteSelector
	HeaderAnthropicVersion = "anthropic-version"
)

// Handler wires HTTP POST /v1/messages to decode → executor → encode.
type Handler struct {
	Exec lipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	// RoutePrefixes are backend route-selector prefixes accepted from body model.
	RoutePrefixes       routeselect.PrefixSet
	MaxRequestBodyBytes int64
	Log                 *slog.Logger
	TrafficPorts        traffic.PortBundle
	DecodeAdmission     lipsdk.DecodeAdmission
	PreRequestKeepalive lipsdk.FrontendKeepaliveConfig
	Config              Config

	pipe frontendpipe.Spec[EncodeOptions]
}

func (h *Handler) spec() *frontendpipe.Spec[EncodeOptions] {
	if h.pipe.Exec != nil || h.pipe.Decode != nil {
		return &h.pipe
	}
	h.pipe = frontendpipe.Spec[EncodeOptions]{
		Config: frontendpipe.Config{
			Exec:                 h.Exec,
			DefaultRouteSelector: h.DefaultRouteSelector,
			RoutePrefixes:        h.RoutePrefixes,
			MaxRequestBodyBytes:  h.MaxRequestBodyBytes,
			Log:                  h.Log,
			TrafficPorts:         h.TrafficPorts,
			DecodeAdmission:      h.DecodeAdmission,
			PreRequestKeepalive:  h.PreRequestKeepalive,
			FrontendID:           ID,
		},
		Wire:               WireErrors{},
		RouteFromBodyModel: true,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if strings.HasSuffix(path, "/messages") || path == "/messages" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			decoded, err := DecodeMessageRequest(dctx.Body, DecodeOptions{
				RouteSelector:    dctx.RouteSelector,
				AnthropicVersion: dctx.AnthropicVersion,
				Headers:          dctx.Headers,
			})
			if err != nil {
				return nil, err
			}
			return &frontendpipe.Decoded{Call: decoded.Call, Stream: decoded.Stream, RouteSelector: dctx.RouteSelector}, nil
		},
		BuildEncodeOpts: func(call *lipapi.Call, _ bool) EncodeOptions {
			return EncodeOptions{
				MessageID:                "msg_" + diag.StableCallToken(call),
				ExposeLipUsageExtensions: h.Config.ExposeLipUsageExtensions,
			}
		},
		WriteStream:    WriteStreamSSE,
		WriteNonStream: WriteNonStreamJSON,
	}
	return &h.pipe
}

// ServeHTTP implements Messages create on POST …/messages.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	frontendpipe.ServeHTTP(h.spec(), w, r)
}
