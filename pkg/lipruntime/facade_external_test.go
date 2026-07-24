package lipruntime_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

type facadeQuerier struct{}

func (facadeQuerier) List(context.Context, metering.Query) (metering.Page, error) {
	return metering.Page{}, nil
}

func TestExternalFacade_ExecutorViewAndReadyBeforeAfterClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	view := rt.ExecutorView()
	if view == nil {
		t.Fatal("ExecutorView required before Close")
	}
	if !rt.Ready() {
		t.Fatal("Ready required before Close")
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView must remain non-nil after Close")
	}
	if rt.ExecutorView() != view {
		t.Fatal("ExecutorView identity must survive Close")
	}
	if rt.Ready() {
		t.Fatal("Ready must be false after Close")
	}
}

func TestExternalFacade_CapabilityAccessorsAndClosedVocabulary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath:       repoConfigPath(t),
		LogWriter:        io.Discard,
		TrafficObservers: []traffic.Observer{noopTraffic{}},
		UsageObservers:   []usage.Observer{noopUsage{}},
		MeteringQuerier:  facadeQuerier{},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	if !rt.HasTrafficObservers() || !rt.HasUsageObservers() {
		t.Fatal("production observer capabilities must derive from host attachment")
	}
	if !rt.HasProductionMeteringQuerier() || rt.MeteringQuerier() == nil {
		t.Fatal("metering querier capability must derive from host")
	}
	state := rt.ExecutableGenerationState()
	switch state {
	case controlplane.CapabilityReady, controlplane.CapabilityDisabled,
		controlplane.CapabilityUnavailable, controlplane.CapabilityDegraded:
	default:
		t.Fatalf("ExecutableGenerationState=%q outside closed vocabulary", state)
	}
}

func TestExternalFacade_ReloadStatusImportable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if rt.ReloadControl() == nil {
		t.Fatal("ReloadControl must be bound")
	}
	res := rt.Reload(ctx, lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI, SafeActor: "external"})
	if res.Category != lipruntime.ResultNoop && res.Category != lipruntime.ResultPublished {
		t.Fatalf("reload category=%q", res.Category)
	}
	st := rt.ReloadStatus()
	if st.ActiveGeneration < 1 {
		t.Fatalf("active generation=%d", st.ActiveGeneration)
	}
}

func TestExternalFacade_SnapshotRefreshSemantics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	before := rt.ReloadStatus().ActiveGeneration
	if err := rt.RefreshSnapshots(ctx); err != nil {
		t.Fatalf("RefreshSnapshots success path: %v", err)
	}
	if rt.ReloadStatus().ActiveGeneration != before {
		t.Fatal("RefreshSnapshots must not publish config generations")
	}
	empty := &lipruntime.Runtime{}
	if err := empty.RefreshSnapshots(ctx); err == nil {
		t.Fatal("RefreshSnapshots on empty Runtime must error")
	}
}

func TestExternalClose_RetryIdempotencyAtPublicBoundary(t *testing.T) {
	t.Parallel()
	host := &facadeFakeHost{
		ready:     true,
		closeErrs: []error{errors.New("tracing shutdown boom")},
	}
	rt := lipruntime.NewRuntimeWithHostForTest(host)
	err := rt.Close(context.Background())
	if err == nil || err.Error() != "tracing shutdown boom" {
		t.Fatalf("first Close err=%v", err)
	}
	if host.closeCalls.Load() != 1 {
		t.Fatalf("close calls=%d", host.closeCalls.Load())
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if host.closeCalls.Load() != 2 {
		t.Fatalf("retry must invoke host Close again; calls=%d", host.closeCalls.Load())
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if host.closeCalls.Load() != 2 {
		t.Fatalf("successful Close must not re-enter host; calls=%d", host.closeCalls.Load())
	}
}
