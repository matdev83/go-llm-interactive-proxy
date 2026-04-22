package runtimebundle

import (
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// BuildOptions configures composition-root dependencies for Build.
// PluginRegistry is required; other fields are optional (see Build for nil defaults).
type BuildOptions struct {
	// HTTPClient is the shared upstream HTTP client for backends that need outbound HTTP (Bedrock, ACP).
	// When nil, Build uses httpclient.Standard().
	HTTPClient *http.Client
	// Clock overrides time sources for the executor and routing-health circuit breaker. Tests only.
	Clock func() time.Time
	// PluginRegistry selects which bundled factories Build uses for backends. Required; nil fails [Build].
	PluginRegistry *pluginreg.Registry
	// WireModel resolves default upstream model ids when computing the effective default route selector.
	// When nil, Build uses pluginreg.DefaultWireModel (standard distribution).
	WireModel routing.WireModelForBackend
}
