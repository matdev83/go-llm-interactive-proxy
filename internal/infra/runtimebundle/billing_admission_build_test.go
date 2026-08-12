package runtimebundle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	coreRuntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type (
	allowBillingAdmission struct{}
	allowBillingHandoff   struct{}
)

func (allowBillingHandoff) AppendUsageRecord(context.Context, billing.TurnUsageRecord) error {
	return nil
}

func (allowBillingAdmission) Authorize(context.Context, coreRuntime.BillingAdmissionInput) (billing.Authorization, error) {
	return billing.Authorization{}, nil
}

func TestBuildBillingAdmissionRequiredSucceedsWithAdapter(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Production: runtimebundle.ProductionOptions{
			BillingAdmission:         allowBillingAdmission{},
			BillingAdmissionRequired: true,
		},
	})
	if built.Executor().BillingAdmission == nil {
		t.Fatal("required billing adapter was not wired")
	}
}

func TestBuildWiresOptionalBillingTerminalHandoff(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Routing: config.RoutingConfig{MaxAttempts: 1}, Continuity: config.ContinuityConfig{InMemory: true}, Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}}}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry(), Production: runtimebundle.ProductionOptions{
		BillingTerminalHandoff: allowBillingHandoff{},
		BillingIdentity: coreRuntime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "account" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLegID string) string { return "auth:" + aLegID },
		},
	}})
	if built.Executor().BillingTerminalHandoff == nil {
		t.Fatal("billing terminal handoff was not wired")
	}
}

func TestBuildBillingTerminalHandoffRequiresAuthorizationIdentity(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Routing: config.RoutingConfig{MaxAttempts: 1}, Continuity: config.ContinuityConfig{InMemory: true}, Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}}}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry(), Production: runtimebundle.ProductionOptions{
		BillingTerminalHandoff: allowBillingHandoff{},
		BillingIdentity: coreRuntime.BillingIdentity{
			AccountID: func(context.Context, lipapi.Call) string { return "account" },
		},
	}})
	if !errors.Is(err, runtimebundle.ErrBillingTerminalIdentityRequired) {
		t.Fatalf("error = %v, want %v", err, runtimebundle.ErrBillingTerminalIdentityRequired)
	}
}

func TestBuildBillingAdmissionRequiredFailsWithoutAdapter(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Production: runtimebundle.ProductionOptions{
			BillingAdmissionRequired: true,
		},
	})
	if !errors.Is(err, runtimebundle.ErrBillingAdmissionRequired) {
		t.Fatalf("error = %v, want %v", err, runtimebundle.ErrBillingAdmissionRequired)
	}
}
