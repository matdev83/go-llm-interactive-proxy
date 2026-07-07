package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"

	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// noopAuthSink is a self-contained delegate auth sink whose dispatch always succeeds,
// so the control-plane recorder adapter can fan out and persist normalized events.
type noopAuthSink struct{}

func (noopAuthSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error { return nil }

func (noopAuthSink) OnSessionStart(context.Context, sdkauth.SessionStartEvent) error { return nil }

// TestBuild_ControlPlaneClockFlowsToRecorder proves that BuildOptions.Clock is
// wired into the control-plane normalizer/recorder: a recorded auth decision
// whose source event has a zero timestamp gets OccurredAt = injectedClock.Now().
// Without the wiring, the recorder falls back to SystemClock (time.Now), so the
// recorded OccurredAt would not equal the injected fixed time T.
func TestBuild_ControlPlaneClockFlowsToRecorder(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	clockFn := func() time.Time { return fixedTime }

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		ControlPlane: config.ControlPlaneConfig{
			Enabled:         true,
			Store:           "memory",
			RecordingPolicy: "best_effort",
			Query:           config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: "/cp"},
		},
		Diagnostics: config.DiagnosticsConfig{SharedSecret: "test-secret-1234"},
		Auth:        config.AuthConfig{EventDelivery: "custom"},
	}

	built, err := runtimebundle.Build(
		cfg,
		hooks.New(hooks.Config{}),
		testkit.DiscardLogger(),
		&runtimebundle.BuildOptions{
			PluginRegistry: pluginreg.NewRegistry(),
			Auth:           runtimebundle.AuthOptions{AuthEventSink: noopAuthSink{}},
			Testing:        runtimebundle.TestingOptions{Clock: clockFn},
		},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() {
		for _, c := range built.Closers {
			_ = c()
		}
	}()

	if built.ControlPlaneQueries == nil {
		t.Fatal("expected ControlPlaneQueries to be wired")
	}

	ctx := context.Background()
	// Zero-value AuthDecisionEvent -> Time is zero -> normalizer substitutes clock.Now().
	if err := built.AuthEventDispatcher.DispatchAuthDecision(ctx, sdkauth.AuthDecisionEvent{}); err != nil {
		t.Fatalf("DispatchAuthDecision: %v", err)
	}

	page, err := built.ControlPlaneQueries.Events(ctx, cp.EventQuery{
		Limit:      10,
		Visibility: cp.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("Events query: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 recorded event, got %d", len(page.Items))
	}
	if !page.Items[0].OccurredAt.Equal(fixedTime) {
		t.Fatalf("expected OccurredAt %v, got %v", fixedTime, page.Items[0].OccurredAt)
	}
}
