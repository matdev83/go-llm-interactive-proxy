package configsource

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// LoadEffective reads one stable snapshot from s and runs the shared pure
// effective-configuration pipeline. Filesystem ownership remains in this
// driving adapter; core/config only sees bounded bytes and explicit options.
func (s *FixedSource) LoadEffective(ctx context.Context, active *ActiveSourceVersion, opts config.LoadEffectiveOptions) (*config.EffectiveConfig, AtomicResult, error) {
	if s == nil {
		return nil, "", fmt.Errorf("read config: nil fixed source")
	}
	if opts.ConfigDir == "" {
		opts.ConfigDir = filepath.Dir(s.AbsolutePath())
	}
	snap, result, err := s.ReadStable(ctx, active)
	if err != nil {
		return nil, result, fmt.Errorf("read config: %w", err)
	}
	effective, err := config.LoadEffective(ctx, snap.Bytes, opts)
	if err != nil {
		return nil, result, err
	}
	return effective, result, nil
}

// LoadEffectiveFromPath resolves one fixed source and delegates to LoadEffective.
func LoadEffectiveFromPath(ctx context.Context, path string, active *ActiveSourceVersion, opts config.LoadEffectiveOptions) (*config.EffectiveConfig, AtomicResult, error) {
	source, err := NewFixedSource(path, 0)
	if err != nil {
		return nil, "", fmt.Errorf("read config: %w", err)
	}
	return source.LoadEffective(ctx, active, opts)
}
