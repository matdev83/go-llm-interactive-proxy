package plugin_test

import (
	"context"
	"testing"

	lipsdkplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

type noopLifecycle struct{}

func (noopLifecycle) Start(context.Context) error { return nil }

func (noopLifecycle) Stop(context.Context) error { return nil }

func TestLifecycleContractIsImplementable(t *testing.T) {
	t.Parallel()

	var _ lipsdkplugin.Lifecycle = noopLifecycle{}
}
