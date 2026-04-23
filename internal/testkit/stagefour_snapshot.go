package testkit

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// TestSnapshotOptions configures [NewTestRequestRuntimeSnapshot] for deterministic tests (task 4.3).
type TestSnapshotOptions struct {
	Generation      int64
	State           state.Store
	Aux             auxiliary.Client
	TrafficObserver traffic.Observer
	RawCapture      traffic.RawCaptureSink
	Workspace       workspace.Resolver
}

// NewTestRequestRuntimeSnapshot builds a snapshot for extension and stage tests using the same
// composition rules as production [extensions.NewRequestRuntimeSnapshot].
func NewTestRequestRuntimeSnapshot(bus *hooks.Bus, opts TestSnapshotOptions) *extensions.RequestRuntimeSnapshot {
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Generation:      opts.Generation,
		State:           opts.State,
		Aux:             opts.Aux,
		TrafficObserver: opts.TrafficObserver,
		RawCapture:      opts.RawCapture,
		Workspace:       opts.Workspace,
	})
}
