package pluginreg

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
)

// OpenContinuityStore selects and opens the configured continuity store for the standard bundle.
// Composition roots should use this instead of calling continuity.OpenStore directly.
func OpenContinuityStore(cfg config.ContinuityConfig) (b2bua.Store, error) {
	return continuity.OpenStore(cfg)
}
