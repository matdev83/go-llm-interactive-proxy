package lipruntime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testConfigPath(t *testing.T) string {
	t.Helper()
	return bpkit.WriteDogfoodLocalStubConfig(t)
}

func TestClose_HonorsDeadlineWithoutPrematureProcessClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := Build(ctx, Options{ConfigPath: testConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rt.host == nil || rt.host.Manager == nil || rt.host.Process == nil {
		t.Fatal("nil host composition")
	}

	lease, ok := rt.host.Manager.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinSSE)
	if !ok {
		t.Fatal("pin")
	}
	lease.Release()

	closeCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err = rt.Close(closeCtx)
	if err == nil {
		t.Fatal("expected deadline while pinned")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline", err)
	}
	if rt.host.Process.Closed() {
		t.Fatal("ProcessServices must not close while generation pinned")
	}
	if !rt.host.Manager.HasOpenGenerations() {
		t.Fatal("pinned generation must remain open")
	}

	// Facades remain usable for status; reload rejects after BeginShutdown.
	_ = rt.ReloadStatus()
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView must remain non-nil after Close")
	}
	late := rt.Reload(context.Background(), ReloadTrigger{Kind: TriggerAPI, SafeActor: "after-close"})
	if late.Category != ResultCanceled {
		t.Fatalf("reload after Close=%q want canceled", late.Category)
	}

	pin.Release()
	// A timed-out Close is retryable: after the blocker drains, the same public
	// method must finish generation, process-service, and tracing teardown.
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	if err := rt.Close(retryCtx); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if rt.host.Manager.HasOpenGenerations() {
		t.Fatal("retry Close left generations open")
	}
	if !rt.host.Process.Closed() {
		t.Fatal("retry Close did not close ProcessServices")
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("successful Close must be idempotent: %v", err)
	}
}

func TestClose_ConcurrentWithReloadStatusExecute(t *testing.T) {
	t.Parallel()
	cfgPath := testConfigPath(t)
	for i := range 50 {
		ctx := context.Background()
		rt, err := Build(ctx, Options{ConfigPath: cfgPath, LogWriter: io.Discard})
		if err != nil {
			t.Fatalf("iter %d Build: %v", i, err)
		}
		view := rt.ExecutorView()
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = rt.Close(cctx)
		}()
		go func() {
			defer wg.Done()
			_ = rt.Reload(context.Background(), ReloadTrigger{Kind: TriggerAPI, SafeActor: "race"})
		}()
		go func() {
			defer wg.Done()
			_ = rt.ReloadStatus()
			_ = rt.ReloadControl()
		}()
		go func() {
			defer wg.Done()
			if view != nil {
				_, _ = view.Execute(context.Background(), &lipapi.Call{})
			}
		}()
		wg.Wait()
		// Idempotent second Close must not panick or nil-deref.
		_ = rt.Close(context.Background())
		if rt.ExecutorView() == nil || rt.host == nil || rt.reload == nil {
			t.Fatalf("iter %d facade pointers must remain non-nil", i)
		}
	}
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := Build(ctx, Options{ConfigPath: testConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !rt.Ready() {
		t.Fatal("runtime must be ready before Close")
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if rt.Ready() {
		t.Fatal("runtime must not report ready after Close")
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView must survive Close")
	}
}
