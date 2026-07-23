package runtimebundle_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

func TestInitialGeneration_BootstrapPublishesGenerationOne(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")
	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath:      cfgPath,
		Mode:            runtimebundle.BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildBootstrap: %v", err)
	}
	bootstrapServeCleanup(t, res)

	if res.ProcessServices == nil || res.GenerationManager == nil || res.InitialGeneration == nil {
		t.Fatal("expected process services, manager, and initial generation")
	}
	if res.InitialGeneration.ID() != 1 {
		t.Fatalf("id=%d want 1", res.InitialGeneration.ID())
	}
	st := res.InitialGeneration.Status()
	if st.Meta.Label != "startup" || st.Meta.TriggerKind != "startup" {
		t.Fatalf("meta=%+v", st.Meta)
	}
	if st.Meta.PublicFingerprint == "" {
		t.Fatal("expected public fingerprint")
	}
	if st.Lifecycle != runtimehost.GenActive {
		t.Fatalf("lifecycle=%v", st.Lifecycle)
	}

	lease, ok := res.GenerationManager.Acquire()
	if !ok || lease.Handler() == nil {
		t.Fatal("expected acquireable generation handler")
	}
	st = lease.Status()
	if st.Meta.ID != 1 || st.Meta.PublicFingerprint == "" {
		lease.Release()
		t.Fatalf("lease status=%+v", st)
	}
	lease.Release()

	d := runtimehost.NewGenerationDispatcher(res.GenerationManager)
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/not-a-route", nil))
	if rr.Code == http.StatusServiceUnavailable {
		t.Fatalf("dispatcher unavailable: %d", rr.Code)
	}
}

func TestInitialGeneration_CompileFailureRollsBackProcessServices(t *testing.T) {
	t.Parallel()
	assertBootstrapPartialCleanupOnComposeFailure(t)
}

// TestBootstrapPartialCleanup_ComposeFailureClosesOwnersOnce characterizes Task 1.3
// partial bootstrap cleanup: after a later compose step fails, already-acquired
// ProcessServices are closed, generation handles are absent, and tracing handoff
// is cleared so callers cannot double-close.
func TestBootstrapPartialCleanup_ComposeFailureClosesOwnersOnce(t *testing.T) {
	t.Parallel()
	assertBootstrapPartialCleanupOnComposeFailure(t)
}

func TestInitialGeneration_BootstrapServeRequiresHandlerComposer(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")
	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath: cfgPath,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		bootstrapServeCleanup(t, res)
		t.Fatal("expected nil HandlerComposer failure")
	}
	if !strings.Contains(err.Error(), "HandlerComposer") {
		t.Fatalf("error=%v want HandlerComposer requirement", err)
	}
}

func assertBootstrapPartialCleanupOnComposeFailure(t *testing.T) {
	t.Helper()
	cfgPath := filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")
	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath: cfgPath,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
		HandlerComposer: func(context.Context, *config.Config, *slog.Logger, stdhttp.StandardHTTPInput) (http.Handler, error) {
			return nil, errors.New("compose boom")
		},
	})
	if err == nil {
		t.Fatal("expected compose failure")
	}
	if res.ProcessServices != nil && !res.ProcessServices.Closed() {
		t.Fatal("process services must close on compile failure")
	}
	if res.GenerationManager != nil || res.InitialGeneration != nil {
		t.Fatal("failed bootstrap must not leave generation host handles")
	}
	// Failure owns tracing teardown and clears the handoff so callers cannot
	// double-close (joinInitialFailureCleanup reverse-order contract).
	if res.ShutdownTracing != nil {
		t.Fatal("failed bootstrap must clear ShutdownTracing after owned cleanup")
	}
}

func TestInitialGeneration_FeatureLifecycleStartStopOnceNoAppOwnership(t *testing.T) {
	// Not parallel: overrides submitnoop lifecycle probe factory globally.
	probe := &submitnoop.LifecycleProbe{}
	submitnoop.SetLifecycleProbeFactoryForTest(func() lipplugin.Lifecycle { return probe })
	t.Cleanup(func() { submitnoop.SetLifecycleProbeFactoryForTest(nil) })
	cfgPath := writeLifecycleProbeConfig(t)

	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath:      cfgPath,
		Mode:            runtimebundle.BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildBootstrap: %v", err)
	}
	if probe.StartCount() != 1 {
		t.Fatalf("lifecycle starts=%d want 1 (double-register would be 2)", probe.StartCount())
	}
	if probe.StopCount() != 0 {
		t.Fatalf("lifecycle stops=%d want 0 before retire", probe.StopCount())
	}

	if err := res.App.Start(context.Background()); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
	if probe.StartCount() != 1 {
		t.Fatalf("App must not own/start feature lifecycles: starts=%d", probe.StartCount())
	}

	if err := res.GenerationManager.ShutdownDetached(context.Background(), runtimehost.NewLifecycleWorker()); err != nil {
		t.Fatalf("ShutdownDetached: %v", err)
	}
	if err := res.ProcessServices.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	res.App.Shutdown(context.Background())
	if res.ShutdownTracing != nil {
		_ = res.ShutdownTracing(context.Background())
	}

	if probe.StartCount() != 1 || probe.StopCount() != 1 {
		t.Fatalf("after generation retire: starts=%d stops=%d want 1/1", probe.StartCount(), probe.StopCount())
	}
}

func writeLifecycleProbeConfig(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(src),
		"    - id: submit-noop\n      enabled: true\n      config: {}",
		"    - id: submit-noop\n      enabled: true\n      config:\n        lifecycle_probe: true",
		1)
	if text == string(src) {
		t.Fatal("failed to inject lifecycle_probe into dogfood config")
	}
	path := filepath.Join(t.TempDir(), "lifecycle-probe.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
