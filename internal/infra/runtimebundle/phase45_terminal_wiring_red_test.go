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
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// Composition-root wiring proof without production ForTest seams:
// injection + IntentService accept path + readiness/metrics publish.
func TestPhase45_BuildWiresTerminalWorkIntoExecutorAndReadiness(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase45-wire",
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
		TerminalWorkOwnerID: "bundle-phase45",
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
	if built.Executor == nil || built.Executor.TerminalWork == nil {
		t.Fatal("Build must inject IntentService into Executor.AccountingRuntime.TerminalWork")
	}
	if built.TerminalWorkProcessor == nil || !built.TerminalWorkProcessor.Running() {
		t.Fatal("Build must own processor lifecycle (running after Build)")
	}
	if built.TerminalWorkQueries == nil || built.TerminalWorkMetrics == nil {
		t.Fatal("Build must expose QueryService and MetricsObserver")
	}
	if built.ReadinessReport == nil {
		t.Fatal("expected ReadinessReport")
	}
	if built.Metrics == nil || built.Metrics.TerminalWork == nil {
		t.Fatal("metrics.Bundle must wire TerminalWorkProm")
	}

	// Persist durable intent via injected IntentService (no ForTest settle API).
	if err := built.Executor.TerminalWork.AcceptSettleFailure(context.Background(), terminalworkapp.SettleFailureInput{
		RequestID:  "req-wire",
		AttemptID:  "a-1",
		TraceID:    "tr-wire",
		ProviderID: "quota",
		Handles:    []string{"quota-h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := built.TerminalWorkQueries.List(context.Background(), terminalworkapp.WorkQuery{
		RequestID: "req-wire",
		Class:     terminalworkapp.QueryClassPendingTerminalWork,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) == 0 {
		t.Fatal("expected durable pending work after AcceptSettleFailure")
	}
	ready := built.TerminalWorkReadiness(context.Background())
	if !ready.BacklogKnown {
		t.Fatal("BacklogKnown want true")
	}
	if ready.Backlog < 1 {
		t.Fatalf("readiness Backlog=%d want >=1", ready.Backlog)
	}
	if err := built.PublishTerminalWorkMetrics(context.Background()); err != nil {
		t.Fatalf("PublishTerminalWorkMetrics: %v", err)
	}
	families, err := built.Metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundBacklog := false
	for _, f := range families {
		if f.GetName() == "lip_terminal_work_backlog" {
			foundBacklog = true
			if len(f.Metric) == 0 || f.Metric[0].Gauge == nil || f.Metric[0].Gauge.GetValue() < 1 {
				t.Fatalf("published backlog gauge not updated")
			}
		}
	}
	if !foundBacklog {
		t.Fatal("missing lip_terminal_work_backlog after publish")
	}

	report, err := built.ReadinessReport.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range report.Components {
		if c.Component != cp.ReadinessComponentTerminalRecovery {
			continue
		}
		found = true
		if c.State != cp.CapabilityDegraded || c.Reason != cp.ReasonPendingTerminalWork {
			t.Fatalf("terminal_recovery=%+v want degraded/pending_terminal_work", c)
		}
	}
	if !found {
		t.Fatal("expected terminal_recovery component")
	}
	_ = sdk.WorkStatePending
}
