package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func testRegistryWithStdBundle(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestFeatureHooksFromReferenceConfig_chainsAndPassThrough(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)

	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatalf("model_aliases: %v", err)
	}

	regs := config.RegistrationsFromConfig(cfg)
	hookCfg, _, err := reg.BuildFeatureHooks(regs)
	if err != nil {
		t.Fatalf("feature hooks: %v", err)
	}

	bus := hooks.New(hookCfg)
	ns, nrq, nrs, nt := bus.HookChainLengths()
	if ns != 1 || nrq != 1 || nrs != 1 || nt != 1 {
		t.Fatalf("hook counts: submit=%d request=%d response=%d tools=%d", ns, nrq, nrs, nt)
	}

	call := &lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Route: lipapi.RouteIntent{Selector: "stub:x"},
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := bus.RunSubmit(context.Background(), call, &sdkhooks.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if err := bus.RunRequestPartHooks(context.Background(), call, sdkhooks.PartMeta{TraceID: "t"}); err != nil {
		t.Fatalf("RunRequestPartHooks: %v", err)
	}
	ev := &lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"}
	if err := bus.RunResponsePartHooks(context.Background(), ev, sdkhooks.PartMeta{TraceID: "t"}); err != nil {
		t.Fatalf("RunResponsePartHooks: %v", err)
	}
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "n"}
	res := bus.ApplyToolReactors(context.Background(), te, sdkhooks.ToolMeta{TraceID: "t"})
	if !res.Emit || res.Event != te {
		t.Fatalf("ApplyToolReactors: %+v", res)
	}
}

func TestFeatureHooksFromRegistrations_unknownEnabledFeature(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)

	_, _, err := reg.BuildFeatureHooks([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "unknown-feature", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected error for unknown feature id")
	}
}

func TestNewBootstrapApp_withComposedHooks(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)

	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatalf("model_aliases: %v", err)
	}
	regs := config.RegistrationsFromConfig(cfg)
	hookCfg, _, err := reg.BuildFeatureHooks(regs)
	if err != nil {
		t.Fatalf("feature hooks: %v", err)
	}

	app, err := runtimebundle.NewBootstrapApp(runtimebundle.BootstrapOptions{
		Config:        cfg,
		Logger:        testkit.DiscardLogger(),
		Registrations: regs,
		Mandatory:     mandatoryStandardPlugins(),
		Hooks:         hookCfg,
	})
	if err != nil {
		t.Fatalf("NewBootstrapApp: %v", err)
	}
	ns, nrq, nrs, nt := app.HookBus().HookChainLengths()
	if ns != 1 || nrq != 1 || nrs != 1 || nt != 1 {
		t.Fatalf("hook counts: submit=%d request=%d response=%d tools=%d", ns, nrq, nrs, nt)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
}
