package runtimebundle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/terminalwork/workstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

type secretStoreError struct {
	*workstore.MemoryStore
}

func (s secretStoreError) CheckReadiness(context.Context) error {
	return errors.New("connection failed: password=SUPER_SECRET_DB_PWD host=db.internal")
}

type secretListError struct {
	*workstore.MemoryStore
}

func (s secretListError) List(context.Context, terminalwork.ListQuery) (terminalwork.ListPage, error) {
	return terminalwork.ListPage{}, errors.New("list failed: token=SUPER_SECRET_LIST_TOKEN")
}

func TestPhase45_TerminalWorkReadinessIncludesBacklogAndSafeStoreError(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 8, 30, 0, 0, time.UTC)
	mem, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "ready-safe",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	store := secretStoreError{MemoryStore: mem}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Testing.Clock = func() time.Time { return clock }
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stubEffectProvider{id: "quota"},
		},
		TerminalWorkOwnerID: "ready-worker",
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
		RequestID:  "req-ready",
		AttemptID:  "a-1",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	ready := built.TerminalWorkReadiness(context.Background())
	if !ready.Configured {
		t.Fatal("expected configured")
	}
	if !ready.BacklogKnown {
		t.Fatal("BacklogKnown want true after successful snapshot")
	}
	if ready.Backlog < 1 {
		t.Fatalf("Backlog=%d want >=1 pending terminal work", ready.Backlog)
	}
	if ready.StoreReady {
		t.Fatal("store readiness check must fail")
	}
	if ready.StoreError != "" {
		t.Fatalf("StoreError must stay empty; got %q", ready.StoreError)
	}
	if ready.ErrorCode != string(cp.ReasonBackingUnavailable) {
		t.Fatalf("ErrorCode=%q want %q", ready.ErrorCode, cp.ReasonBackingUnavailable)
	}
	if strings.Contains(ready.ErrorCode, "SUPER_SECRET") || strings.Contains(ready.ErrorCode, "password=") {
		t.Fatalf("ErrorCode leaked raw content: %q", ready.ErrorCode)
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
		if c.State != cp.CapabilityUnavailable && c.State != cp.CapabilityDegraded {
			t.Fatalf("terminal_recovery state=%q want unavailable|degraded", c.State)
		}
	}
	if !found {
		t.Fatal("expected terminal_recovery component")
	}
}

func TestPhase45_TerminalWorkReadinessSnapshotErrorSafeAndUnknownBacklog(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 8, 35, 0, 0, time.UTC)
	mem, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "ready-list-fail",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	store := secretListError{MemoryStore: mem}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Testing.Clock = func() time.Time { return clock }
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stubEffectProvider{id: "quota"},
		},
		TerminalWorkOwnerID: "ready-list-worker",
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
	ready := built.TerminalWorkReadiness(context.Background())
	if !ready.Configured {
		t.Fatal("expected configured")
	}
	if ready.BacklogKnown {
		t.Fatal("BacklogKnown want false when snapshot fails")
	}
	if ready.Backlog != 0 {
		t.Fatalf("Backlog=%d want 0 when unknown", ready.Backlog)
	}
	if ready.StoreError != "" {
		t.Fatalf("StoreError must stay empty; got %q", ready.StoreError)
	}
	if ready.ErrorCode != string(cp.ReasonBackingUnavailable) {
		t.Fatalf("ErrorCode=%q want %q", ready.ErrorCode, cp.ReasonBackingUnavailable)
	}
	if strings.Contains(ready.ErrorCode, "SUPER_SECRET") || strings.Contains(ready.ErrorCode, "token=") {
		t.Fatalf("ErrorCode leaked raw content: %q", ready.ErrorCode)
	}
}
