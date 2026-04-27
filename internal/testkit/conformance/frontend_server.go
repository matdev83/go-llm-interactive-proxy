package conformance

import (
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	frontanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
)

// MountFrontend registers the bundled frontend handler on mux for conformance tests.
func MountFrontend(mux *http.ServeMux, frontendID string, exec *runtime.Executor, routeSelector string) error {
	switch frontendID {
	case "openai-responses":
		mux.Handle("/v1/responses", &frontopenairesponses.Handler{
			Exec:                 exec,
			DefaultRouteSelector: routeSelector,
		})
	case "openai-legacy":
		mux.Handle("/v1/chat/completions", &frontopenailegacy.Handler{
			Exec:                 exec,
			DefaultRouteSelector: routeSelector,
		})
	case "anthropic":
		mux.Handle("/v1/messages", &frontanthropic.Handler{
			Exec:                 exec,
			DefaultRouteSelector: routeSelector,
		})
	case "gemini":
		h := &frontgemini.Handler{
			Exec:                 exec,
			DefaultRouteSelector: routeSelector,
		}
		mux.Handle("/v1beta/", h)
		mux.Handle("/v1beta1/", h)
	default:
		return fmt.Errorf("unknown frontend id %q", frontendID)
	}
	return nil
}
