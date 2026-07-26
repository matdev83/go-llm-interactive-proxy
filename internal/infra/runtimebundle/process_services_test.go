package runtimebundle_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/terminalwork/workstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func processServicesTestConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		Server: config.ServerConfig{
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 1024,
		},
	}
}

func TestProcessServices_TwoCandidatesShareIdentities(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
			Active:   false,
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	bus1 := hooks.New(hooks.Config{})
	c1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     bus1,
	})
	if err != nil {
		t.Fatalf("CompileCandidate #1: %v", err)
	}
	bus2 := hooks.New(hooks.Config{})
	c2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     bus2,
	})
	if err != nil {
		t.Fatalf("CompileCandidate #2: %v", err)
	}

	if c1 == nil || c2 == nil {
		t.Fatal("expected both candidates")
	}
	if c1.Executor == nil || c2.Executor == nil {
		t.Fatal("expected executors on both candidates")
	}
	if c1.Executor == c2.Executor {
		t.Fatal("candidates must own distinct executors")
	}

	// Process service identity reuse (req 6.2–6.5, 6.9).
	if ps.Metrics == nil || ps.Metrics.Registry == nil {
		t.Fatal("expected process metrics registry")
	}
	if c1.Metrics != ps.Metrics || c2.Metrics != ps.Metrics {
		t.Fatalf("candidates must reuse process Metrics identity: ps=%p c1=%p c2=%p", ps.Metrics, c1.Metrics, c2.Metrics)
	}
	if c1.Metrics.Registry != ps.Metrics.Registry {
		t.Fatal("candidate must not open a duplicate Prometheus registry")
	}
	if c1.Store != ps.Continuity || c2.Store != ps.Continuity {
		t.Fatalf("candidates must reuse process Continuity store: ps=%p c1=%p c2=%p", ps.Continuity, c1.Store, c2.Store)
	}
	if c1.SecureSessionStore != ps.SecureSessions || c2.SecureSessionStore != ps.SecureSessions {
		t.Fatal("candidates must reuse process SecureSessions store")
	}
	if c1.DecodeAdmission != ps.DecodeAdmission || c2.DecodeAdmission != ps.DecodeAdmission {
		t.Fatal("candidates must reuse process DecodeAdmission")
	}
	if c1.PluginRegistry != ps.FactoryCatalog || c2.PluginRegistry != reg {
		t.Fatal("candidates must reuse process FactoryCatalog / PluginRegistry")
	}
	if c1.DatabasePools != ps.DatabasePools || c2.DatabasePools != ps.DatabasePools {
		t.Fatal("candidates must reuse process DatabasePools")
	}
	if c1.TerminalWorkProcessor != ps.TerminalWorkProcessor || c2.TerminalWorkProcessor != ps.TerminalWorkProcessor {
		t.Fatal("candidates must reuse process TerminalWorkProcessor")
	}
	if ps.Tracing.Shutdown == nil {
		t.Fatal("expected process Tracing.Shutdown")
	}
	if c1.ProcessTracingShutdown != nil {
		t.Fatal("candidate must not own process tracing shutdown")
	}
}

func TestProcessServices_CandidateCloseDoesNotCloseSharedServices(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	log := testkit.DiscardLogger()
	var tracingClosed atomic.Bool
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error {
				tracingClosed.Store(true)
				return nil
			},
			Active: false,
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}

	if err := cand.Close(); err != nil {
		t.Fatalf("Candidate.Close: %v", err)
	}
	if tracingClosed.Load() {
		t.Fatal("candidate close must not invoke process tracing shutdown")
	}
	if ps.Metrics == nil || ps.Metrics.Registry == nil {
		t.Fatal("process metrics must survive candidate close")
	}
	if ps.Continuity == nil {
		t.Fatal("process continuity store must survive candidate close")
	}
	if ps.Closed() {
		t.Fatal("ProcessServices must not be closed by candidate Close")
	}

	// Process services remain usable for a second candidate after first close.
	cand2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate after candidate close: %v", err)
	}
	if err := cand2.Close(); err != nil {
		t.Fatalf("cand2.Close: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if !ps.Closed() {
		t.Fatal("expected ProcessServices closed")
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

func TestProcessServices_PartialStartupDisposesReverseOrder(t *testing.T) {
	t.Parallel()

	order := make([]string, 0, 4)

	err := runtimebundle.DisposeProcessClosersForTest([]func() error{
		func() error {
			order = append(order, "first")
			return nil
		},
		func() error {
			order = append(order, "second")
			return errors.New("second failed")
		},
		func() error {
			order = append(order, "third")
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected joined disposal error")
	}
	if !strings.Contains(err.Error(), "second failed") {
		t.Fatalf("expected disposal error to include closer failure, got %v", err)
	}
	want := []string{"third", "second", "first"}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("dispose order=%v want reverse registration %v", order, want)
	}
}

func TestProcessServices_BuildCompatibilityRetainsAggregateCleanup(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Metrics == nil {
		t.Fatal("Build compatibility must still expose Metrics")
	}
	if b.DecodeAdmission == nil {
		t.Fatal("Build compatibility must still expose DecodeAdmission")
	}
	if b.Store == nil {
		t.Fatal("Build compatibility must still expose Store")
	}
	// Pure in-memory builds may have an empty closer bag (historical semantics).
	// When closers exist, reverse-order disposal must remain safe.
	for _, c := range reverseClosers(b.Closers) {
		if err := c(); err != nil {
			t.Fatalf("aggregate closer: %v", err)
		}
	}
}

func TestProcessServices_OwnershipDeferredSharedMutableDocumented(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if ps.DeferredSharedMutable.OwnershipNote != "" {
		t.Fatalf("task 2.4 must resolve DeferredSharedMutable; got note %q", ps.DeferredSharedMutable.OwnershipNote)
	}
	if ps.ALegLifecycle == nil || ps.AffinityStore == nil || ps.CandidateHealth == nil || ps.ExtensionState == nil {
		t.Fatal("expected hoisted A-leg, affinity, health, and extension state on ProcessServices")
	}
}

func TestProcessServices_DuplicateCompileDoesNotDuplicateTerminalWork(t *testing.T) {
	t.Parallel()

	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "tw-process-dup"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := processServicesTestConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: pluginreg.NewRegistry(),
			Production: runtimebundle.ProductionOptions{
				TerminalWorkStore: store,
				TerminalWorkProviders: []terminalworkapp.EffectProvider{
					stubEffectProvider{id: "prov-process"},
				},
				TerminalWorkOwnerID: "process-worker",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.TerminalWorkProcessor == nil {
		t.Fatal("expected process terminal-work processor")
	}

	c1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c1.TerminalWorkProcessor != ps.TerminalWorkProcessor || c2.TerminalWorkProcessor != ps.TerminalWorkProcessor {
		t.Fatal("duplicate terminal-work processors across candidates")
	}
	_ = c1.Close()
	_ = c2.Close()
}

func TestBootstrap_ProcessServicesCompatibility(t *testing.T) {
	t.Parallel()

	// Ensure tracing.Result shape still wires through ProcessTracing without
	// requiring callers to change BootstrapResult fields.
	res := tracing.Result{Shutdown: func(context.Context) error { return nil }, Active: false}
	pt := runtimebundle.ProcessTracing{Shutdown: res.Shutdown, Active: res.Active}
	if pt.Shutdown == nil {
		t.Fatal("ProcessTracing must accept tracing.Result fields")
	}
}

func reverseClosers(in []func() error) []func() error {
	out := make([]func() error, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}
