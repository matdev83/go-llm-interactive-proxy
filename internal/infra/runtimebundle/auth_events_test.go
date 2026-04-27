package runtimebundle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"

	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
)

func TestBuild_authEventDispatcher_nonNil(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.AuthEventDispatcher == nil {
		t.Fatal("expected AuthEventDispatcher")
	}
}

func TestBuild_authEventDelivery_customRequiresAuthEventSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth:       config.AuthConfig{EventDelivery: "custom"},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err == nil || !errors.Is(err, runtimebundle.ErrAuthEventSinkRequired) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrAuthEventSinkRequired, err)
	}
}

type sliceSink struct {
	got []string
}

func (s *sliceSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error {
	s.got = append(s.got, "auth")
	return nil
}

func (s *sliceSink) OnSessionStart(context.Context, sdkauth.SessionStartEvent) error {
	s.got = append(s.got, "session")
	return nil
}

func TestBuild_authEventDelivery_customUsesInjectedSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth:       config.AuthConfig{EventDelivery: "custom"},
	}
	custom := &sliceSink{}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		AuthEventSink:  custom,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.AuthEventDispatcher.DispatchAuthDecision(ctx, sdkauth.AuthDecisionEvent{}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(custom.got) != 1 || custom.got[0] != "auth" {
		t.Fatalf("custom sink calls: %#v", custom.got)
	}
}

func TestBuild_authEventDelivery_disabledNilSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth:       config.AuthConfig{EventDelivery: "disabled"},
	}
	sink := &sliceSink{}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		AuthEventSink:  sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.AuthEventDispatcher.DispatchAuthDecision(ctx, sdkauth.AuthDecisionEvent{}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(sink.got) != 0 {
		t.Fatalf("disabled mode should not call injected sink, got %#v", sink.got)
	}
}

func TestBuild_authEventFailurePolicy_failClosed(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth: config.AuthConfig{
			EventFailurePolicy: "fail_closed",
		},
	}
	errSink := &errSink{}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		AuthEventSink:  errSink,
	})
	if err != nil {
		t.Fatal(err)
	}
	// default delivery uses slog sink, not errSink — need custom + err sink to test policy.
	cfg2 := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth: config.AuthConfig{
			EventDelivery:      "custom",
			EventFailurePolicy: "fail_closed",
		},
	}
	b2, err := runtimebundle.Build(cfg2, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		AuthEventSink:  errSink,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b2.AuthEventDispatcher.DispatchAuthDecision(ctx, sdkauth.AuthDecisionEvent{}); err == nil {
		t.Fatal("expected fail_closed sink error")
	}
	// default path still succeeds with internal slog sink
	if err := b.AuthEventDispatcher.DispatchAuthDecision(ctx, sdkauth.AuthDecisionEvent{}); err != nil {
		t.Fatalf("default slog path: %v", err)
	}
}

type errSink struct{}

func (errSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error {
	return errors.New("sink error")
}

func (errSink) OnSessionStart(context.Context, sdkauth.SessionStartEvent) error { return nil }
