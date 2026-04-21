package continuity

import (
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/sqlitestore"
)

// OpenStore is the composition-root factory for b2bua.Store from continuity settings.
func OpenStore(cfg config.ContinuityConfig) (b2bua.Store, error) {
	switch strings.ToLower(strings.TrimSpace(storeBackendName(cfg))) {
	case "sqlite":
		path := strings.TrimSpace(cfg.SQLitePath)
		if path == "" {
			return nil, fmt.Errorf("continuity: sqlite_path is required when store is \"sqlite\"")
		}
		return sqlitestore.Open(path)
	case "memory":
		if !cfg.InMemory {
			return nil, fmt.Errorf("continuity: in_memory=false is not valid when store is \"memory\"")
		}
		return newMemoryStoreFromContinuity(cfg)
	default:
		s := strings.TrimSpace(cfg.Store)
		if s == "" {
			s = "(empty)"
		}
		return nil, fmt.Errorf("continuity: store %q is not supported (supported: memory, sqlite)", s)
	}
}

func storeBackendName(cfg config.ContinuityConfig) string {
	if strings.TrimSpace(cfg.Store) == "" {
		return "memory"
	}
	return cfg.Store
}

func newMemoryStoreFromContinuity(cfg config.ContinuityConfig) (b2bua.Store, error) {
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
	if cfg.MaxLegs != 0 {
		opts.MaxLegs = cfg.MaxLegs
	}
	return b2bua.NewMemoryStore(opts), nil
}

// NewMemoryStore is equivalent to OpenStore for the supported in-memory configuration.
// Prefer OpenStore at new composition sites.
func NewMemoryStore(cfg config.ContinuityConfig) (b2bua.Store, error) {
	return OpenStore(cfg)
}
