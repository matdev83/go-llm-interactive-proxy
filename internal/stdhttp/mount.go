// Package stdhttp registers bundled frontend HTTP handlers on a ServeMux (standard distribution wiring).
package stdhttp

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
)

// MountBundledFrontends registers enabled frontend protocol handlers from config on mux.
// Gemini is mounted under /v1beta/ and /v1beta1/ only (after other prefixes when present).
// maxRequestBodyBytes is forwarded to handlers; zero means each handler's default body cap.
func MountBundledFrontends(mux *http.ServeMux, exec *runtime.Executor, defaultRouteSelector string, plugins []config.PluginConfig, maxRequestBodyBytes int64) error {
	if mux == nil || exec == nil {
		return nil
	}
	var specific, geminiLast []config.PluginConfig
	for _, p := range plugins {
		if !p.Enabled {
			continue
		}
		if p.ID == frontgemini.ID {
			geminiLast = append(geminiLast, p)
			continue
		}
		specific = append(specific, p)
	}
	ordered := append(specific, geminiLast...)
	for _, p := range ordered {
		if err := pluginreg.MountFrontend(p.ID, mux, p.Config, exec, defaultRouteSelector, maxRequestBodyBytes); err != nil {
			return err
		}
	}
	return nil
}

// MountBundledFrontendsLegacy mounts all bundled frontends unconditionally (tests and minimal callers).
func MountBundledFrontendsLegacy(mux *http.ServeMux, exec *runtime.Executor, defaultRouteSelector string) {
	_ = MountBundledFrontends(mux, exec, defaultRouteSelector, allBundledFrontendsEnabled(), 0)
}

func allBundledFrontendsEnabled() []config.PluginConfig {
	return []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openai-legacy", Enabled: true},
		{ID: "anthropic", Enabled: true},
		{ID: "gemini", Enabled: true},
	}
}
