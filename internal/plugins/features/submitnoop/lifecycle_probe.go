package submitnoop

import (
	"context"
	"sync/atomic"

	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

var _ lipplugin.Lifecycle = (*LifecycleProbe)(nil)

// LifecycleProbe is an optional no-op lifecycle returned when HookConfig.LifecycleProbe is true.
type LifecycleProbe struct {
	started atomic.Bool
	stopped atomic.Bool
}

// WasStarted reports whether Start has completed successfully.
func (l *LifecycleProbe) WasStarted() bool { return l.started.Load() }

// WasStopped reports whether Stop has completed successfully.
func (l *LifecycleProbe) WasStopped() bool { return l.stopped.Load() }

func (l *LifecycleProbe) Start(context.Context) error {
	if l == nil {
		return nil
	}
	l.started.Store(true)
	return nil
}

func (l *LifecycleProbe) Stop(context.Context) error {
	if l == nil {
		return nil
	}
	l.stopped.Store(true)
	return nil
}
