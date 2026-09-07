package featurehost

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/state"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/compaction"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

// Runtime is the standard-distribution feature host process facade.
// It owns only migrated process feature resources and disposes them
// in reverse acquisition order upon Close. Borrowed resources (BackgroundAux,
// DB pools, etc.) are never closed by Runtime (Requirement 8.5, design §7).
type Runtime struct {
	logger               *slog.Logger
	extState             lipstate.Store
	bgAux                *auxreq.BackgroundScheduler // borrowed, never closed
	terminalPolicy       *terminaldecisionpolicy.Store
	compactionDetector   runtime.CompactionDetector
	branchCoordinator    *state.BranchCoordinator
	compactionParentPort *compaction.ParentPort
	conversationStore    conversationview.Store
	closers              []func() error
	closeOnce            sync.Once
	closeErr             error
	closed               atomic.Bool
}

func (r *Runtime) registerCloser(closer func() error) {
	if closer != nil {
		r.closers = append(r.closers, closer)
	}
}

// CompactionDetector returns the featurehost-owned compaction detector.
// It is the single permitted detector observer: generic runtimebundle wires
// it into candidate executors (candidate_compile.go), and the transition
// guard observes it. Coordinator/parent-port have no such observer by design.
func (r *Runtime) CompactionDetector() runtime.CompactionDetector {
	if r == nil {
		return nil
	}
	return r.compactionDetector
}

// TerminalDecisionPolicy returns the featurehost-owned terminal decision policy store, if owned.
func (r *Runtime) TerminalDecisionPolicy() *terminaldecisionpolicy.Store {
	if r == nil {
		return nil
	}
	return r.terminalPolicy
}

// ClosersCount returns the number of closers registered on this Runtime.
func (r *Runtime) ClosersCount() int {
	if r == nil {
		return 0
	}
	return len(r.closers)
}

// Close disposes all process feature resources owned by this Runtime in reverse
// acquisition order. It is idempotent and aggregates any disposal errors.
// It never closes borrowed generic process resources.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		var out error
		for _, closer := range slices.Backward(r.closers) {
			if err := closer(); err != nil {
				out = errors.Join(out, fmt.Errorf("featurehost: dispose closer: %w", err))
			}
		}
		r.closeErr = out
		r.closed.Store(true)
	})
	return r.closeErr
}

// Closed reports whether [Runtime.Close] has completed.
func (r *Runtime) Closed() bool {
	if r == nil {
		return true
	}
	return r.closed.Load()
}
