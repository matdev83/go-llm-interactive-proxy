package plugin_test

import (
	"context"
	"testing"

	lipsdkplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// multiLifecycle exercises that Lifecycle can be implemented by a struct with
// no other plugin surface (spec-bundle style contract check).
type multiLifecycle struct {
	started int
	stopped int
}

func (m *multiLifecycle) Start(context.Context) error {
	m.started++
	return nil
}

func (m *multiLifecycle) Stop(context.Context) error {
	m.stopped++
	return nil
}

func TestLifecycle_startStopRoundTrip(t *testing.T) {
	t.Parallel()
	var _ lipsdkplugin.Lifecycle = (*multiLifecycle)(nil)
	var m multiLifecycle
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if m.started != 1 || m.stopped != 1 {
		t.Fatalf("calls: started=%d stopped=%d", m.started, m.stopped)
	}
}
