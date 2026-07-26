package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestCompileCandidate_LedgerRollbackOnInjectedFailureLeavesNoResource(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()
	var closed atomic.Int32
	opts := &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	}
	// Register a generation-owned probe backend that records close.
	var empty yaml.Node
	_ = yaml.Unmarshal([]byte("{}"), &empty)
	if err := reg.RegisterBackend("probe-close", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			BackendPrefixes: []string{"probe"},
			ModelInventory: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: []modelinventory.Model{{
					CanonicalID: "probe/model",
					NativeID:    "model",
					DisplayName: "Probe",
				}},
			},
			Close: func() error {
				closed.Add(1)
				return nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.Backends = []config.PluginConfig{
		{Kind: "probe-close", ID: "probe", Enabled: true, Config: empty},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	_, err = runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		FaultInject: runtimebundle.CandidateFaultInject{
			After: "model",
		},
	})
	if err == nil {
		t.Fatal("expected injected candidate fault")
	}
	if !errors.Is(err, runtimebundle.ErrCandidateFaultInjected) {
		t.Fatalf("want fault injected, got %v", err)
	}
	if closed.Load() != 1 {
		t.Fatalf("probe close=%d want 1 after rollback", closed.Load())
	}
	if ps.Closed() {
		t.Fatal("process services must survive candidate rollback")
	}
}

func TestCandidateRuntime_DiscardClosesLedgerOnly(t *testing.T) {
	t.Parallel()
	var candClosed, processClosed atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("cand", runtimebundle.PhaseClose, func() error {
		candClosed.Add(1)
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	_ = func() error {
		processClosed.Add(1)
		return nil
	}

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("cand", cand)
	if err := g.Discard(); err != nil {
		t.Fatal(err)
	}
	if candClosed.Load() != 1 {
		t.Fatalf("candidate closes=%d", candClosed.Load())
	}
	if processClosed.Load() != 0 {
		t.Fatalf("process closes=%d", processClosed.Load())
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if candClosed.Load() != 1 {
		t.Fatalf("idempotent closes=%d", candClosed.Load())
	}
}

func TestCandidateRuntime_LifecycleWorkerQuiesceClose(t *testing.T) {
	t.Parallel()
	var quiesced, closed atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesced.Add(1)
		return nil
	})
	_ = ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
		closed.Add(1)
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("cand", cand)
	mustPublishHost(t, m, g)
	mustPublishHost(t, m, m.Prepare("next"))

	worker := runtimehost.NewLifecycleWorker()
	if err := worker.Retire(context.Background(), g, cand); err != nil {
		t.Fatal(err)
	}
	if quiesced.Load() != 1 || closed.Load() != 1 {
		t.Fatalf("quiesced=%d closed=%d", quiesced.Load(), closed.Load())
	}
	if g.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v", g.Lifecycle())
	}
}

func mustPublishHost(t *testing.T, m *runtimehost.Manager, g *runtimehost.Generation) {
	t.Helper()
	if err := m.Publish(g); err != nil {
		t.Fatalf("publish: %v", err)
	}
}
