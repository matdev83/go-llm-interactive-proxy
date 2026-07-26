package runtimebundle_test

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

func TestBuild_cursorSDKCloserRunsOnPartialBuildRollback(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe := fakebridge.BuildExe(t)
	ws := t.TempDir()

	base := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(base, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var closeOrder []string
	var closeN atomic.Int32
	var innerN atomic.Int32
	var capturedClose func() error
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend(cursorsdk.ID, func(node yaml.Node, hc *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		be, err := base.BuildBackend(cursorsdk.ID, node, hc, deps)
		if err != nil {
			return execbackend.Backend{}, err
		}
		inner := be.Close
		be.Close = func() error {
			closeN.Add(1)
			mu.Lock()
			closeOrder = append(closeOrder, "cursorsdk")
			mu.Unlock()
			if inner != nil {
				innerN.Add(1)
				return inner()
			}
			return nil
		}
		capturedClose = be.Close
		return be, nil
	}); err != nil {
		t.Fatal(err)
	}
	constructErr := errors.New("forced later backend failure")
	if err := reg.RegisterBackend("fail-later", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, constructErr
	}); err != nil {
		t.Fatal(err)
	}

	sdkYAML := fmt.Sprintf("api_key: rollback-key\nbridge_executable: %q\ndefault_workspace: %q\n", exe, ws)
	var sdkNode yaml.Node
	if err := yaml.Unmarshal([]byte(sdkYAML), &sdkNode); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: cursorsdk.ID, ID: "cursor-sdk", Enabled: true, Config: sdkNode},
			{Kind: "fail-later", ID: "boom", Enabled: true, Config: empty},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected partial-build failure")
	}
	if !errors.Is(err, constructErr) {
		t.Fatalf("want constructErr joined, got %v", err)
	}
	mu.Lock()
	got := append([]string(nil), closeOrder...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "cursorsdk" {
		t.Fatalf("rollback close order=%v want [cursorsdk]", got)
	}
	if gotN := closeN.Load(); gotN != 1 {
		t.Fatalf("wrapper Close invocations=%d want 1 after partial-build rollback", gotN)
	}
	if gotInner := innerN.Load(); gotInner != 1 {
		t.Fatalf("underlying Close attempts=%d want 1 after rollback", gotInner)
	}
	if capturedClose == nil {
		t.Fatal("expected captured Close")
	}
	if err := capturedClose(); err != nil {
		t.Fatalf("direct second Close: %v", err)
	}
	if gotN := closeN.Load(); gotN != 2 {
		t.Fatalf("wrapper Close invocations=%d want 2 after direct second Close", gotN)
	}
	if gotInner := innerN.Load(); gotInner != 2 {
		t.Fatalf("underlying Close attempts=%d want 2 after direct second Close", gotInner)
	}
}

func TestBuild_cursorSDKCloserIdempotentOnSuccessfulBuild(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe := fakebridge.BuildExe(t)
	ws := t.TempDir()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	sdkYAML := fmt.Sprintf("api_key: shutdown-key\nbridge_executable: %q\ndefault_workspace: %q\n", exe, ws)
	var sdkNode yaml.Node
	if err := yaml.Unmarshal([]byte(sdkYAML), &sdkNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 2},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: cursorsdk.ID, ID: "cursor-sdk", Enabled: true, Config: sdkNode},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	be, ok := built.Executor().Backends["cursor-sdk"]
	if !ok {
		t.Fatal("expected cursor-sdk backend")
	}
	_ = be
	for i := range 2 {
		if err := built.Close(); err != nil {
			t.Fatalf("close pass %d: %v", i, err)
		}
	}
}
