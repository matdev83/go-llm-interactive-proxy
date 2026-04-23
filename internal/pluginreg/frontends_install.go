package pluginreg

import (
	"net/http"

	frontanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func mountOpenAIResponses(mux *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
	mux.Handle("/v1/responses", &frontopenairesponses.Handler{
		Exec:                 opts.Exec,
		DefaultRouteSelector: opts.DefaultRoute,
		MaxRequestBodyBytes:  opts.MaxRequestBodyBytes,
		TrafficPorts:         opts.TrafficPorts,
	})
	return nil
}

func mountOpenAILegacy(mux *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
	mux.Handle("/v1/chat/completions", &frontopenailegacy.Handler{
		Exec:                 opts.Exec,
		DefaultRouteSelector: opts.DefaultRoute,
		MaxRequestBodyBytes:  opts.MaxRequestBodyBytes,
		TrafficPorts:         opts.TrafficPorts,
	})
	return nil
}

func mountAnthropic(mux *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
	mux.Handle("/v1/messages", &frontanthropic.Handler{
		Exec:                 opts.Exec,
		DefaultRouteSelector: opts.DefaultRoute,
		MaxRequestBodyBytes:  opts.MaxRequestBodyBytes,
		TrafficPorts:         opts.TrafficPorts,
	})
	return nil
}

func mountGemini(mux *http.ServeMux, opts lipsdk.FrontendMountOptions) error {
	h := &frontgemini.Handler{
		Exec:                 opts.Exec,
		DefaultRouteSelector: opts.DefaultRoute,
		MaxRequestBodyBytes:  opts.MaxRequestBodyBytes,
		TrafficPorts:         opts.TrafficPorts,
	}
	// Register API-prefix routes only (avoid catch-all "/" shadowing unrelated paths).
	mux.Handle("/v1beta/", h)
	mux.Handle("/v1beta1/", h)
	return nil
}
