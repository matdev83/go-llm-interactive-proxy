package testkit

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)

// ExecutorOption mutates an [runtime.ExecutorConfig] before construction.
type ExecutorOption func(*runtime.ExecutorConfig)

// WithStore sets the B2BUA continuity store.
func WithStore(store b2bua.Store) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Core.Store = store
	}
}

// WithBus sets the hook bus.
func WithBus(bus *hooks.Bus) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Extension.Bus = bus
	}
}

// WithBackends sets backend adapters keyed by routing primary backend id.
func WithBackends(backends map[string]execbackend.Backend) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Core.Backends = backends
	}
}

// WithRand sets the routing RNG.
func WithRand(rng routing.Rng) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Core.Rand = rng
	}
}

// WithDefaultBackend sets model-only default backend resolution.
func WithDefaultBackend(backend string) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Routing.DefaultBackend = backend
	}
}

// WithSelectorAliases sets selector alias rewriting.
func WithSelectorAliases(ar *routing.AliasResolver) ExecutorOption {
	return func(cfg *runtime.ExecutorConfig) {
		cfg.Routing.SelectorAliases = ar
	}
}

// NewTestExecutor constructs an executor from grouped options. Additional promoted
// fields can be set on the returned value when an option does not exist yet.
func NewTestExecutor(tb testing.TB, opts ...ExecutorOption) *runtime.Executor {
	tb.Helper()
	var cfg runtime.ExecutorConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return runtime.NewExecutor(cfg)
}

// PatchExecutor applies a mutation to an existing executor in tests (promoted fields).
func PatchExecutor(ex *runtime.Executor, patch func(*runtime.Executor)) *runtime.Executor {
	if ex == nil {
		ex = runtime.TestExecutor()
	}
	if patch != nil {
		patch(ex)
	}
	return ex
}
