package runtimebundle

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)

type interleavedRuntime struct {
	shape     interleavedthinking.ShapeConfig
	memoStore interleavedthinking.MemoStore
}

func interleavedRuntimeFromConfig(cfg *config.Config) (interleavedRuntime, error) {
	var zero interleavedRuntime
	if cfg == nil || !cfg.Interleaved.Enabled {
		return zero, nil
	}
	instructions, err := config.ResolveInterleavedInstructions(cfg)
	if err != nil {
		return zero, err
	}
	ic := cfg.Interleaved
	return interleavedRuntime{
		shape: interleavedthinking.ShapeConfig{
			Instructions:          instructions,
			StreamToClient:        ic.EffectiveStreamToClient(),
			RegularTurnsRemaining: ic.EffectiveRegularTurnsRemaining(),
			MaxMemoBytes:          ic.EffectiveMaxMemoBytes(),
		},
		// Memo bodies are process-local in the standard runtime. Durable continuity
		// stores persist only the cycle cursor and MemoRef; restart loss is acceptable.
		memoStore: interleavedthinking.NewMemoStore(ic.EffectiveMaxMemoBytes()),
	}, nil
}

func interleavedExecutorRuntime(cfg *config.Config) (runtime.InterleavedRuntime, error) {
	interleaved, err := interleavedRuntimeFromConfig(cfg)
	if err != nil {
		return runtime.InterleavedRuntime{}, fmt.Errorf("runtimebundle: interleaved: %w", err)
	}
	return runtime.InterleavedRuntime{
		InterleavedConfig: interleaved.shape,
		MemoStore:         interleaved.memoStore,
	}, nil
}
