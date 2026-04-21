package runtimebundle

import (
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// BuildOptions configures optional composition-root dependencies for Build.
type BuildOptions struct {
	// HTTPClient is the shared upstream HTTP client for backends that need outbound HTTP (Bedrock, ACP).
	// When nil, Build uses httpclient.Standard().
	HTTPClient *http.Client
	// Clock overrides time sources for the executor and routing-health circuit breaker. Tests only.
	Clock func() time.Time
	// PluginRegistry selects which bundled factories Build uses for backends. When nil, [pluginreg.Default] is used.
	PluginRegistry *pluginreg.Registry
}
