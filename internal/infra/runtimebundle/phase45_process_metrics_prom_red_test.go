package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/terminalwork/workstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func TestPhase45_ProcessDueUpdatesPromGaugesAndTransitionCounters(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 10, 10, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-prom-proc",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Observability.Metrics.Enabled = true
	opts := baseAuthorityOptions(t, nil)
	opts.Testing.Clock = func() time.Time { return clock }
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stubEffectProvider{id: "quota"},
		},
		TerminalWorkOwnerID:      "prom-worker",
		TerminalWorkTickInterval: time.Hour,
		TerminalWorkClaimLimit:   10,
	}
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, c := range built.Closers {
			_ = c()
		}
	})
	if err := built.Executor.TerminalWork.AcceptSettleFailure(context.Background(), terminalworkapp.SettleFailureInput{
		RequestID:  "req-prom-proc",
		AttemptID:  "a-1",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	before := gatherTerminalWork(t, built)
	if err := built.TerminalWorkProcessor.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	after := gatherTerminalWork(t, built)
	if after.transitionsTotal <= before.transitionsTotal {
		t.Fatalf("transitions_total before=%v after=%v want increase", before.transitionsTotal, after.transitionsTotal)
	}
	if after.completed < 1 {
		t.Fatalf("completed gauge=%v want >=1 after ProcessDue", after.completed)
	}
	done, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-prom-proc",
		State:     sdk.WorkStateCompleted,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Records) == 0 {
		t.Fatal("expected completed work")
	}
	if after.backlog > before.backlog {
		t.Fatalf("backlog rose after complete: before=%v after=%v", before.backlog, after.backlog)
	}
}

type twGather struct {
	backlog          float64
	completed        float64
	transitionsTotal float64
}

func gatherTerminalWork(t *testing.T, built *runtimebundle.Built) twGather {
	t.Helper()
	families, err := built.Metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out twGather
	for _, f := range families {
		switch f.GetName() {
		case "lip_terminal_work_backlog":
			if len(f.Metric) > 0 && f.Metric[0].Gauge != nil {
				out.backlog = f.Metric[0].Gauge.GetValue()
			}
		case "lip_terminal_work_completed":
			if len(f.Metric) > 0 && f.Metric[0].Gauge != nil {
				out.completed = f.Metric[0].Gauge.GetValue()
			}
		case "lip_terminal_work_transitions_total":
			for _, m := range f.Metric {
				if m.Counter != nil {
					out.transitionsTotal += m.Counter.GetValue()
				}
			}
		}
	}
	return out
}
