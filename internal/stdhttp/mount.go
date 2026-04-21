// Package stdhttp registers bundled frontend HTTP handlers on a ServeMux (standard distribution wiring).
package stdhttp

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	frontanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
)

// MountBundledFrontends registers all v1 bundled frontend protocol handlers on mux.
func MountBundledFrontends(mux *http.ServeMux, exec *runtime.Executor, defaultRouteSelector string) {
	if mux == nil || exec == nil {
		return
	}
	mux.Handle("/v1/responses", &frontopenairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: defaultRouteSelector,
	})
	mux.Handle("/v1/chat/completions", &frontopenailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: defaultRouteSelector,
	})
	mux.Handle("/v1/messages", &frontanthropic.Handler{
		Exec:                 exec,
		DefaultRouteSelector: defaultRouteSelector,
	})
	mux.Handle("/", &frontgemini.Handler{
		Exec:                 exec,
		DefaultRouteSelector: defaultRouteSelector,
	})
}
