package pluginreg

import (
	"net/http"

	frontanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func mountOpenAIResponses(mux *http.ServeMux, _ yaml.Node, exec lipsdk.ExecutorView, defaultRoute string, maxBody int64) error {
	mux.Handle("/v1/responses", &frontopenairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: defaultRoute,
		MaxRequestBodyBytes:  maxBody,
	})
	return nil
}

func mountOpenAILegacy(mux *http.ServeMux, _ yaml.Node, exec lipsdk.ExecutorView, defaultRoute string, maxBody int64) error {
	mux.Handle("/v1/chat/completions", &frontopenailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: defaultRoute,
		MaxRequestBodyBytes:  maxBody,
	})
	return nil
}

func mountAnthropic(mux *http.ServeMux, _ yaml.Node, exec lipsdk.ExecutorView, defaultRoute string, maxBody int64) error {
	mux.Handle("/v1/messages", &frontanthropic.Handler{
		Exec:                 exec,
		DefaultRouteSelector: defaultRoute,
		MaxRequestBodyBytes:  maxBody,
	})
	return nil
}

func mountGemini(mux *http.ServeMux, _ yaml.Node, exec lipsdk.ExecutorView, defaultRoute string, maxBody int64) error {
	h := &frontgemini.Handler{
		Exec:                 exec,
		DefaultRouteSelector: defaultRoute,
		MaxRequestBodyBytes:  maxBody,
	}
	// Register API-prefix routes only (avoid catch-all "/" shadowing unrelated paths).
	mux.Handle("/v1beta/", h)
	mux.Handle("/v1beta1/", h)
	return nil
}
