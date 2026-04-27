package runtimebundle

import (
	"context"
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// BuildOptions configures composition-root dependencies for Build.
// PluginRegistry is required; other fields are optional (see Build for nil defaults).
type BuildOptions struct {
	// StartupContext, when non-nil, is the parent context for bounded startup I/O (e.g. Postgres
	// open and schema migrate for continuity and secure-session stores). When nil, Build uses
	// [context.Background] as the parent. It is not stored or used for per-request paths.
	StartupContext context.Context
	// HTTPClient is the shared upstream HTTP client for backends that need outbound HTTP (Bedrock, ACP).
	// When nil, Build uses httpclient.Standard().
	HTTPClient *http.Client
	// OutboundTracing when true wraps the upstream transport with OpenTelemetry HTTP propagation.
	// When HTTPClient is non-nil, Build clones the client before wrapping so caller-owned clients are not mutated.
	OutboundTracing bool
	// Clock overrides time sources for the executor and routing-health circuit breaker. Tests only.
	Clock func() time.Time
	// PluginRegistry selects which bundled factories Build uses for backends. Required; nil fails [Build].
	PluginRegistry *pluginreg.Registry
	// WireModel resolves default upstream model ids when computing the effective default route selector.
	// When nil, Build uses pluginreg.DefaultWireModel (standard distribution).
	WireModel config.WireModelForBackend
	// HTTPAuthProviders runs in [internal/stdhttp] before frontend decode (R4). When nil, empty,
	// or every entry is nil, [Build] composes providers from validated config instead of using
	// the override (so an accidental []Provider{nil} slice cannot disable authentication).
	// When at least one entry is non-nil, [Build] uses a clone of this slice only: no config-derived
	// auth is applied. Custom binaries must supply an equivalent policy chain; [cmd/lipstd] does
	// not set this field unless you intentionally replace transport auth at the composition root.
	HTTPAuthProviders []httpauth.Provider
	// AuthEventSink implements [auth.EventSink] when auth.event_delivery is "custom"; otherwise ignored.
	// For default/disabled delivery the dispatcher uses an internal slog sink or nil per config.
	AuthEventSink auth.EventSink
	// RemoteDecider is required when the effective auth handler is remote or required_level is api_key_sso.
	// The OSS standard binary does not construct remote transports; inject at the composition root.
	RemoteDecider auth.RemoteDecider
	// OSIdentity supplies OS principal material for local_noop. When nil, [Build] uses the default
	// infra [github.com/matdev83/go-llm-interactive-proxy/internal/infra/osidentity] provider.
	OSIdentity auth.OSIdentityProvider
	// AuthErrorRenderer is optional terminal HTTP mapping for auth failures; nil uses stdhttp defaults.
	AuthErrorRenderer httpauth.AuthErrorRenderer
	// AuthErrorRenderersByFrontend optional per auth-wire-frontend-id renderers (stdhttp/auth
	// DefaultFrontendIDFromRequest vocabulary). Non-nil entries override the same key from
	// [PluginRegistry.AuthErrorRenderers] when [Build] composes HTTP auth providers.
	AuthErrorRenderersByFrontend map[string]httpauth.AuthErrorRenderer
	// SessionOpeners and WorkspaceResolvers are merged from enabled feature bundles (task 5.1).
	SessionOpeners     []session.Opener
	WorkspaceResolvers []workspace.Resolver
	// ToolCatalogFilters and RequestTransforms are merged from enabled feature bundles (tasks 7–7.1).
	ToolCatalogFilters []toolcatalog.Filter
	RequestTransforms  []request.Transform
	RouteHintProviders []routehint.Provider
	CompletionGates    []completion.Gate
	TrafficObservers   []traffic.Observer
	RawCaptureSinks    []traffic.RawCaptureSink
	TrafficRedactors   []traffic.Redactor
	// SecureSessionStore is optional; when set on Built, stdhttp may mount secure-session diagnostics.
	SecureSessionStore app.Store
}
