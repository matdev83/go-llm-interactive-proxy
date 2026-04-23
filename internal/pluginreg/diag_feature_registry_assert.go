package pluginreg

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"

// Compile-time check: *Registry must satisfy [diag.FeatureRegistry] for inventory extras wiring.
var _ diag.FeatureRegistry = (*Registry)(nil)
