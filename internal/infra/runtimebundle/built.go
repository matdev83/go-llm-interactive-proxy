package runtimebundle

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
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
	// RoutePrefixes are backend route-selector prefixes accepted from frontend protocol model fields.
	RoutePrefixes []string
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
	// SecureSessionStore is optional; when non-nil with secure-session diagnostics config, stdhttp
	// mounts operator session routes (see [BuildOptions.SecureSessionStore]).
	SecureSessionStore app.Store
	// AuthEventDispatcher emits auth decision and session-start events per config policy.
	// Always non-nil after [Build]; the underlying sink may be nil when event delivery is disabled.
	AuthEventDispatcher *auth.EventDispatcher
	// CatalogRuntime is non-nil when model_catalog.enabled or external_updates_enabled started catalog I/O.
	CatalogRuntime *modelcatalog.CatalogRuntime
	// ModelRegistry is the loaded backend model inventory for fast canonical model routing lookups.
	ModelRegistry *modelregistry.Registry
	// ModelRegistryRuntime owns cached backend model inventory refresh and live lookup publication.
	ModelRegistryRuntime *modelregistry.Runtime
	// TokenAccountingAdmin is non-nil when accounting.admin.enabled wires the operator count service.
	TokenAccountingAdmin *accountingapp.Service
}
