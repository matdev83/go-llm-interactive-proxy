package stdhttp

import (
	"context"
	"errors"
	"testing"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

type cancelSensitiveLifecycle struct{}

func (cancelSensitiveLifecycle) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (cancelSensitiveLifecycle) Stop(context.Context) error { return nil }

func TestRun_appStartReceivesRunContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := &coreconfig.Config{
		Server: coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing: coreconfig.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "openai-responses:gpt-4o-mini",
		},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
	}
	app, err := runtime.New(runtime.Options{
		Config:     cfg,
		Lifecycles: []lipplugin.Lifecycle{cancelSensitiveLifecycle{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = Run(ctx, cfg, app, nil)
	if err == nil {
		t.Fatal("expected error when ctx is cancelled before startup (app.Start must observe Run's ctx)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
