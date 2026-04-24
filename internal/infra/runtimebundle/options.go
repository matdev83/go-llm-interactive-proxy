package runtimebundle

import (
	"net/http"
	"time"

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
	// HTTPAuthProviders runs in [internal/stdhttp] before frontend decode (R4). Nil or empty skips auth.
	HTTPAuthProviders []httpauth.Provider
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
