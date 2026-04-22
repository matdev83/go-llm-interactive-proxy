package runtimebundle

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// Built holds assembled runtime dependencies for the standard distribution composition root.
type Built struct {
	Executor *runtime.Executor
	Store    b2bua.Store
	Closers  []func() error
	// EffectiveDefaultRoute is the selector used when clients omit explicit routing (see routing.EffectiveDefaultRouteSelector).
	// [Build] sets this from config and BuildOptions.WireModel (or pluginreg.DefaultWireModel when WireModel is nil).
	EffectiveDefaultRoute string
	// UpstreamHTTP is the shared outbound HTTP client passed to backends that need upstream HTTP.
	// Successful [Build] always sets this (explicit [BuildOptions.HTTPClient] or the default from httpclient).
	UpstreamHTTP *http.Client
	// PluginRegistry is the registry used to construct backends and must be used when mounting frontends
	// or composing features. [Build] sets this from [BuildOptions.PluginRegistry].
	PluginRegistry *pluginreg.Registry
}
