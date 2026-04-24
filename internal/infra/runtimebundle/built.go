package runtimebundle

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// Built holds assembled runtime dependencies for the standard distribution composition root.
type Built struct {
	Executor *runtime.Executor
	Store    b2bua.Store
	Closers  []func() error
	// EffectiveDefaultRoute is the selector used when clients omit explicit routing (see config.EffectiveDefaultRouteSelector), after model_aliases expansion when configured.
	// [Build] sets this from config and BuildOptions.WireModel (or pluginreg.DefaultWireModel when WireModel is nil).
	EffectiveDefaultRoute string
	// UpstreamHTTP is the shared outbound HTTP client passed to backends that need upstream HTTP.
	// Successful [Build] always sets this (explicit [BuildOptions.HTTPClient] or the default from httpclient).
	UpstreamHTTP *http.Client
	// PluginRegistry is the registry used to construct backends and must be used when mounting frontends
	// or composing features. [Build] sets this from [BuildOptions.PluginRegistry].
	PluginRegistry *pluginreg.Registry
	// Metrics is non-nil when observability.metrics.enabled; [stdhttp.RunWithRuntime] uses it for /metrics and HTTP middleware.
	Metrics *metrics.Bundle
	// RuntimeSnapshot is the execution binding for feature stages and facades (design §15B).
	// Treat as read-only for the lifetime of this Built; see [extensions.RequestRuntimeSnapshot].
	RuntimeSnapshot *extensions.RequestRuntimeSnapshot
	// HTTPAuthProviders is copied from [BuildOptions] for stdhttp wiring (transport auth, R4).
	HTTPAuthProviders []httpauth.Provider
}
