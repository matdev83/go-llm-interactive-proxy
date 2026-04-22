// Package stdhttp registers bundled frontend HTTP handlers on a ServeMux (standard distribution wiring).
package stdhttp

import (
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
)

// MountBundledFrontends registers enabled frontend protocol handlers from config on mux.
// Gemini is mounted under /v1beta/ and /v1beta1/ only (after other prefixes when present).
// maxRequestBodyBytes is forwarded to handlers; zero means each handler's default body cap.
// reg selects which frontend factories to use; nil returns an error.
func MountBundledFrontends(mux *http.ServeMux, exec *runtime.Executor, defaultRouteSelector string, plugins []config.PluginConfig, maxRequestBodyBytes int64, reg *pluginreg.Registry) error {
	if mux == nil || exec == nil {
		return nil
	}
	if reg == nil {
		return fmt.Errorf("stdhttp: nil plugin registry")
	}
	var specific, geminiLast []config.PluginConfig
	for _, p := range plugins {
		if !p.Enabled {
			continue
		}
		if p.FactoryID() == frontgemini.ID {
			geminiLast = append(geminiLast, p)
			continue
		}
		specific = append(specific, p)
	}
	ordered := append(specific, geminiLast...)
	for _, p := range ordered {
		if err := reg.MountFrontend(p.FactoryID(), mux, p.Config, exec, defaultRouteSelector, maxRequestBodyBytes); err != nil {
			return err
		}
	}
	return nil
}

// MountBundledFrontendsLegacy mounts all bundled frontends unconditionally (tests and minimal callers).
func MountBundledFrontendsLegacy(mux *http.ServeMux, exec *runtime.Executor, defaultRouteSelector string, reg *pluginreg.Registry) error {
	return MountBundledFrontends(mux, exec, defaultRouteSelector, allBundledFrontendsEnabled(), 0, reg)
}

func allBundledFrontendsEnabled() []config.PluginConfig {
	return []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openai-legacy", Enabled: true},
		{ID: "anthropic", Enabled: true},
		{ID: "gemini", Enabled: true},
	}
}
