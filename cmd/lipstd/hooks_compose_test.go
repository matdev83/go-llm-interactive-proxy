package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func registerStandardBundleForTest(t *testing.T) {
	t.Helper()
	pluginreg.RegisterStandardBundle()
}

func TestFeatureHooksFromReferenceConfig_chainsAndPassThrough(t *testing.T) {
	t.Parallel()
	registerStandardBundleForTest(t)

	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	regs := config.RegistrationsFromConfig(cfg)
	hookCfg, _, err := pluginreg.BuildFeatureHooks(regs)
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
	registerStandardBundleForTest(t)

	_, _, err := pluginreg.BuildFeatureHooks([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "unknown-feature", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected error for unknown feature id")
	}
}

func TestRuntimeNew_withComposedHooks(t *testing.T) {
	t.Parallel()
	registerStandardBundleForTest(t)

	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	regs := config.RegistrationsFromConfig(cfg)
	hookCfg, _, err := pluginreg.BuildFeatureHooks(regs)
	if err != nil {
		t.Fatalf("feature hooks: %v", err)
	}

	app, err := runtime.New(runtime.Options{
		Config:        cfg,
		Registrations: regs,
		Mandatory:     mandatoryStandardPlugins(),
		Hooks:         hookCfg,
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ns, nrq, nrs, nt := app.HookBus().HookChainLengths()
	if ns != 1 || nrq != 1 || nrs != 1 || nt != 1 {
		t.Fatalf("hook counts: submit=%d request=%d response=%d tools=%d", ns, nrq, nrs, nt)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
}
