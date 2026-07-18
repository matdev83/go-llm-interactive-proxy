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

type stubEffectProvider struct {
	id string
}

func (p stubEffectProvider) ProviderID() string { return p.id }
func (p stubEffectProvider) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}
}
func (p stubEffectProvider) Version() string { return "1" }
func (p stubEffectProvider) Invoke(context.Context, terminalwork.WorkRecord, string) error {
	return nil
}

func TestBuild_TerminalWorkOwnershipFromProductionOptions(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "tw-build"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore: store,
		TerminalWorkProviders: []terminalworkapp.EffectProvider{
			stubEffectProvider{id: "prov-a"},
		},
		TerminalWorkOwnerID:       "bundle-worker",
		TerminalWorkTickInterval:  50 * time.Millisecond,
		TerminalWorkRenewInterval: 25 * time.Millisecond,
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
	if built.TerminalWorkProcessor == nil {
		t.Fatal("expected TerminalWorkProcessor ownership")
	}
	if built.TerminalWorkRegistry == nil {
		t.Fatal("expected TerminalWorkRegistry ownership")
	}
	if _, err := built.TerminalWorkRegistry.Resolve("prov-a", sdk.WorkKindSettleRequestProvider); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ready := built.TerminalWorkReadiness(context.Background())
	if !ready.Configured || !ready.StoreReady {
		t.Fatalf("readiness=%+v", ready)
	}
	if err := built.TerminalWorkProcessor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready = built.TerminalWorkReadiness(context.Background())
	if !ready.Running {
		t.Fatal("expected running after Start")
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := built.TerminalWorkProcessor.Shutdown(shutCtx); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_TerminalWorkAbsentWithoutStore(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, c := range built.Closers {
			_ = c()
		}
	})
	if built.TerminalWorkProcessor != nil || built.TerminalWorkRegistry != nil {
		t.Fatal("terminal work must stay nil without injected store")
	}
	if built.TerminalWorkReadiness(context.Background()).Configured {
		t.Fatal("readiness must be unconfigured without store")
	}
}

func TestBuild_TerminalWorkUnresolvedProviders(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "tw-unresolved",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalwork.WorkRecord{
		WorkID:         "w-ghost",
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk-ghost"},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStateIntent,
		ProviderID:     "ghost",
		Lifecycle:      terminalwork.LifecycleCorrelation{RequestID: "r", AttemptID: "a", TraceID: "t"},
		Versions:       terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "ghost", RatingID: "r1"},
		Payload:        []byte(`{}`),
	}
	if err := store.AppendIntent(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{WorkID: rec.WorkID, Now: clock}); err != nil {
		t.Fatal(err)
	}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Production = runtimebundle.ProductionOptions{
		TerminalWorkStore:   store,
		TerminalWorkOwnerID: "bundle-worker",
	}
	opts.Testing.Clock = func() time.Time { return clock }
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, c := range built.Closers {
			_ = c()
		}
	})
	if err := built.TerminalWorkProcessor.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := built.TerminalWorkReadiness(context.Background())
	if len(ready.UnresolvedProviderIDs) != 1 || ready.UnresolvedProviderIDs[0] != "ghost" {
		t.Fatalf("unresolved=%v", ready.UnresolvedProviderIDs)
	}
}
