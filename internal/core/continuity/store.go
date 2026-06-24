package continuity

import (
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// NewMemoryStoreFromConfig creates an in-memory b2bua.Store from the memory-related
// fields of a ContinuityConfig. It validates InMemory, TTL, and MaxLegs.
// This is a pure-core helper with no infra/db dependency.
func NewMemoryStoreFromConfig(cfg config.ContinuityConfig) (b2bua.Store, error) {
	if !cfg.InMemory {
		return nil, fmt.Errorf("continuity: in_memory=false is not valid when store is \"memory\"")
	}
	opts := b2bua.MemoryStoreOptions{}
	if s := strings.TrimSpace(cfg.TTL); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("continuity.ttl: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("continuity.ttl must be non-negative")
		}
		opts.TTL = d
	}
	if cfg.MaxLegs < 0 {
		return nil, fmt.Errorf("continuity: max_legs must be >= 0")
	}
	if cfg.MaxLegs != 0 {
		opts.MaxLegs = cfg.MaxLegs
	}
	return b2bua.NewMemoryStore(opts)
}
