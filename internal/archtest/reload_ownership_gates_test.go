package archtest

import (
	"bytes"
	"go/ast"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Fixture proofs: representative forbidden source shapes must be detected by the
// reload ownership scanners (task 1.1). These do not depend on the live tree.

func TestReloadOwnership_ForbiddenFixturesDetected(t *testing.T) {
	t.Parallel()

	t.Run("second_tracer_bootstrap", func(t *testing.T) {
		t.Parallel()
		src := `package p
import (
	"go.opentelemetry.io/otel"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
)
func bad() { otel.SetTracerProvider(nil); tracing.Init(nil, nil) }
`
		res, err := scanReloadOwnershipSource("fixture_tracer.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.TracerBootstraps) < 2 {
			t.Fatalf("expected >=2 tracer bootstraps, got %v", res.TracerBootstraps)
		}
	})

	t.Run("aliased_tracer_bootstrap", func(t *testing.T) {
		t.Parallel()
		src := `package p
import otelapi "go.opentelemetry.io/otel"
func bad() { otelapi.SetTracerProvider(nil) }
`
		res, err := scanReloadOwnershipSource("fixture_tracer_alias.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.TracerBootstraps) == 0 {
			t.Fatalf("expected aliased otel.SetTracerProvider detection, got %v", res.TracerBootstraps)
		}
	})

	t.Run("metrics_in_generation_path", func(t *testing.T) {
		t.Parallel()
		src := `package p
import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
func compileGeneration() { _ = metrics.NewBundle(nil, nil); _ = metrics.NewRegistry() }
`
		res, err := scanReloadOwnershipSource("generation_compile.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MetricsConstructions) < 2 {
			t.Fatalf("expected metrics constructions, got %v", res.MetricsConstructions)
		}
	})

	t.Run("aliased_metrics_and_prometheus_registry", func(t *testing.T) {
		t.Parallel()
		src := `package p
import (
	m "github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
	col "github.com/prometheus/client_golang/prometheus/collectors"
)
func compileGeneration() {
	_ = m.NewRegistry()
	_ = prom.NewRegistry()
	_ = col.NewGoCollector()
	_ = col.NewProcessCollector(col.ProcessCollectorOpts{})
}
`
		res, err := scanReloadOwnershipSource("generation_metrics_alias.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MetricsConstructions) < 4 {
			t.Fatalf("expected aliased metrics/prometheus/collectors detection, got %v", res.MetricsConstructions)
		}
	})

	t.Run("duplicate_process_worker", func(t *testing.T) {
		t.Parallel()
		src := `package p
import terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
func compile() {
	_, _ = terminalworkapp.NewProcessor(nil, nil, cfg)
	_, _ = terminalworkapp.NewProcessor(nil, nil, cfg)
}
`
		res, err := scanReloadOwnershipSource("generation_worker.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.ProcessWorkers) < 2 {
			t.Fatalf("expected duplicate NewProcessor, got %v", res.ProcessWorkers)
		}
	})

	t.Run("aliased_process_worker", func(t *testing.T) {
		t.Parallel()
		src := `package p
import tw "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
func compile() { _, _ = tw.NewProcessor(nil, nil, cfg) }
`
		res, err := scanReloadOwnershipSource("generation_worker_alias.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.ProcessWorkers) == 0 {
			t.Fatalf("expected aliased NewProcessor detection, got %v", res.ProcessWorkers)
		}
	})

	t.Run("active_runtime_mutation_setter", func(t *testing.T) {
		t.Parallel()
		src := `package p
func mutate(exec *Executor, active *RuntimeGeneration) {
	exec.SetPreflight(nil)
	active.SetHandler(nil)
	exec.Metrics = nil
}
`
		res, err := scanReloadOwnershipSource("mutate_active.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) < 3 {
			t.Fatalf("expected mutation setters, got %v", res.MutationSetters)
		}
	})

	t.Run("active_runtime_mutation_alias_bypass", func(t *testing.T) {
		t.Parallel()
		src := `package p
func mutate(current *Executor) {
	current.SetPreflight(nil)
	rt := current
	rt.SetHandler(nil)
	rt.Metrics = nil
}
`
		res, err := scanReloadOwnershipSource("mutate_alias.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) < 3 {
			t.Fatalf("expected alias/intermediate mutation detection, got %v", res.MutationSetters)
		}
	})

	t.Run("file_watcher_and_poller", func(t *testing.T) {
		t.Parallel()
		src := `package p
import (
	"github.com/fsnotify/fsnotify"
	"time"
)
func watchConfigReload() {
	_, _ = fsnotify.NewWatcher()
	_ = time.NewTicker(time.Second)
}
`
		res, err := scanReloadOwnershipSource("watcher.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) < 2 {
			t.Fatalf("expected watcher mechanisms, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("aliased_watcher_import", func(t *testing.T) {
		t.Parallel()
		src := `package p
import notify "github.com/fsnotify/fsnotify"
func start() { _, _ = notify.NewWatcher() }
`
		res, err := scanReloadOwnershipSource("watcher_alias.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) < 2 {
			t.Fatalf("expected aliased fsnotify import+call detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("watcher_in_future_runtimehost_path", func(t *testing.T) {
		t.Parallel()
		src := `package runtimehost
import (
	"github.com/fsnotify/fsnotify"
	"time"
)
func pollConfig() {
	_, _ = fsnotify.NewWatcher()
	_ = time.NewTicker(time.Second)
}
`
		res, err := scanReloadOwnershipSource("internal/infra/runtimehost/poller.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) < 2 {
			t.Fatalf("expected future-path watcher/poller detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("legitimate_catalog_refresh_ticker_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package runtimebundle
import "time"
func runModelCatalogRefreshLoop() {
	ticker := time.NewTicker(time.Second)
	_ = ticker
}
`
		res, err := scanReloadOwnershipSource("internal/infra/runtimebundle/modelcatalog_refresh_loop.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("model/catalog refresh ticker must remain allowed, got %v", res.WatcherMechanisms)
		}
	})
}

// Remediation RED fixtures: each subtest documents a concrete bypass that the
// hardened scanners must catch (second adversarial review of task 1.1).
func TestReloadOwnership_RemediationBypassFixtures(t *testing.T) {
	t.Parallel()

	t.Run("time_After_poll_loop", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import (
	"os"
	"time"
)
func syncSource() {
	for {
		_, _ = os.Stat("/etc/lip/config.yaml")
		<-time.After(time.Second)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/adapter.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected time.After poll loop detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("time_NewTimer_poll_loop", func(t *testing.T) {
		t.Parallel()
		src := `package runtimehost
import (
	"os"
	"time"
)
func syncSource() {
	for {
		tm := time.NewTimer(time.Second)
		<-tm.C
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/infra/runtimehost/sync.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected time.NewTimer poll loop detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("time_Sleep_poll_loop", func(t *testing.T) {
		t.Parallel()
		src := `package management
import (
	"os"
	"time"
)
func syncSource() {
	for {
		time.Sleep(time.Second)
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/stdhttp/management/sync.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected time.Sleep poll loop detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("time_AfterFunc_reschedule", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import (
	"os"
	"time"
)
func arm() {
	var tick func()
	tick = func() {
		_, _ = os.Stat("/etc/lip/config.yaml")
		time.AfterFunc(time.Second, tick)
	}
	tick()
}
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/arm.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected time.AfterFunc reschedule detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("os_Stat_mtime_poll_loop", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import (
	"os"
	"time"
)
func syncSource() {
	for {
		_, _ = os.Stat("/etc/lip/config.yaml")
		time.Sleep(time.Second)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/mtime.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected os.Stat mtime poll detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("rjeczalik_notify_watcher", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import "github.com/rjeczalik/notify"
func start() { _ = notify.Watch("/etc/lip", nil) }
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/notify.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected rjeczalik/notify detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("local_watcher_wrapper_and_func_alias", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import "github.com/fsnotify/fsnotify"
func start() {
	mk := fsnotify.NewWatcher
	_, _ = mk()
}
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/wrap.go", src)
		if err != nil {
			t.Fatal(err)
		}
		foundAliasCall := false
		for _, w := range res.WatcherMechanisms {
			if strings.Contains(w, "NewWatcher") || strings.Contains(w, "func-value") || strings.Contains(w, "alias") {
				foundAliasCall = true
				break
			}
		}
		// Import alone is insufficient: the indirect mk() call must be attributed.
		callHits := 0
		for _, w := range res.WatcherMechanisms {
			if strings.Contains(w, ":") && !strings.HasPrefix(w, "import ") {
				callHits++
			}
		}
		if !foundAliasCall || callHits == 0 {
			t.Fatalf("expected function-value watcher alias call detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neutral_named_future_package_ticker", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func maintain() {
	for {
		_, _ = os.Stat("/etc/lip/config.yaml")
		time.Sleep(time.Second)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/maintain.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected neutral-path repeated stat/sleep poll detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("oneshot_config_read_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import "os"
func LoadOnce(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	_ = st
	return os.ReadFile(path)
}
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/oneshot.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("one-shot config read must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("mutation_method_receiver", func(t *testing.T) {
		t.Parallel()
		src := `package p
func (e *Executor) rewire() { e.SetPreflight(nil) }
`
		res, err := scanReloadOwnershipSource("mutate_recv.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("expected method-receiver mutation detection, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_typed_var_and_getExecutor", func(t *testing.T) {
		t.Parallel()
		src := `package p
func getExecutor() *Executor { return nil }
func mutate() {
	var current *Executor
	current = getExecutor()
	current.SetHandler(nil)
	rt := getExecutor()
	rt.UpdateRoutes(nil)
}
`
		res, err := scanReloadOwnershipSource("mutate_get.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) < 2 {
			t.Fatalf("expected getExecutor/typed-var mutation detection, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_named_type_alias", func(t *testing.T) {
		t.Parallel()
		src := `package p
type ActiveExec = Executor
func mutate(a *ActiveExec) { a.ResetPolicy(nil) }
`
		res, err := scanReloadOwnershipSource("mutate_alias_type.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("expected named type-alias mutation detection, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_map_slice_field", func(t *testing.T) {
		t.Parallel()
		src := `package p
func mutate(exec *Executor) {
	exec.Routes["x"] = nil
	exec.Backends[0] = nil
	exec.Cfg.Name = "x"
}
`
		res, err := scanReloadOwnershipSource("mutate_index.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) < 3 {
			t.Fatalf("expected map/slice/field mutation detection, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_Update_Replace_Configure", func(t *testing.T) {
		t.Parallel()
		src := `package p
func mutate(g *RuntimeGeneration) {
	g.UpdateHandler(nil)
	g.ReplaceApp(nil)
	g.Configure(nil)
	g.SwapMetrics(nil)
}
`
		res, err := scanReloadOwnershipSource("mutate_verbs.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) < 4 {
			t.Fatalf("expected Update/Replace/Configure/Swap detection, got %v", res.MutationSetters)
		}
	})

	t.Run("function_value_alias_tracer_metrics_worker", func(t *testing.T) {
		t.Parallel()
		src := `package p
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/prometheus/client_golang/prometheus"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
)
func compile() {
	initTracing := tracing.Init
	newMetrics := metrics.NewBundle
	newRegistry := prometheus.NewRegistry
	newWorker := terminalworkapp.NewProcessor
	_, _ = initTracing(nil, nil)
	_ = newMetrics(nil, nil)
	_ = newRegistry()
	_, _ = newWorker(nil, nil, cfg)
}
`
		res, err := scanReloadOwnershipSource("alias_calls.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.TracerBootstraps) == 0 || len(res.MetricsConstructions) < 2 || len(res.ProcessWorkers) == 0 {
			t.Fatalf("expected function-value alias detection; tracers=%v metrics=%v workers=%v",
				res.TracerBootstraps, res.MetricsConstructions, res.ProcessWorkers)
		}
	})
}

// Remediation 1.1b: type-aware active-runtime mutation and precise poll/watch
// control-flow gates (second-pass adversarial findings A/B).
func TestReloadOwnership_RemediationTypeAwareMutationAndPollFlow(t *testing.T) {
	t.Parallel()

	t.Run("mutation_exact_Set_on_active_runtime", func(t *testing.T) {
		t.Parallel()
		src := `package p
func mutate(exec *Executor) { exec.Set(nil) }
`
		res, err := scanReloadOwnershipSource("mutate_exact_set.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("expected exact Set mutator on active runtime, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_immediate_accessor_call", func(t *testing.T) {
		t.Parallel()
		src := `package p
func getExecutor() *Executor { return nil }
type Host struct{}
func (h *Host) Current() *Runtime { return nil }
func mutate(host *Host) {
	getExecutor().SetHandler(nil)
	host.Current().UpdateRoutes(nil)
}
`
		res, err := scanReloadOwnershipSource("mutate_immediate.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) < 2 {
			t.Fatalf("expected immediate accessor mutation detection, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_builtin_delete_clear_copy", func(t *testing.T) {
		t.Parallel()
		src := `package p
func mutate(exec *Executor, src map[string]any) {
	delete(exec.Routes, "x")
	clear(exec.Routes)
	copy(exec.Backends, src)
}
`
		res, err := scanReloadOwnershipSource("mutate_builtins.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) < 3 {
			t.Fatalf("expected delete/clear/copy on active-runtime containers, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_unrelated_getWidget_and_cache_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Widget struct{}
type Cache struct{}
func getWidget() *Widget { return nil }
func mutate(currentCache *Cache) {
	getWidget().Set(nil)
	w := getWidget()
	w.SetHandler(nil)
	currentCache.Update(nil)
	delete(currentCache.Items, "x")
	clear(currentCache.Items)
	copy(currentCache.Buf, nil)
}
`
		res, err := scanReloadOwnershipSource("mutate_unrelated.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("unrelated widget/cache mutations must not trigger, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_RuntimeGeneration_and_Built_types", func(t *testing.T) {
		t.Parallel()
		src := `package p
func mutate(g *RuntimeGeneration, b *Built, r *Runtime, a *App) {
	g.SetHandler(nil)
	b.Configure(nil)
	r.ReplaceRoutes(nil)
	a.SwapMetrics(nil)
}
`
		res, err := scanReloadOwnershipSource("mutate_approved_types.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) < 4 {
			t.Fatalf("expected approved runtime type mutations, got %v", res.MutationSetters)
		}
	})

	t.Run("poll_aliased_os_ReadFile_Open_in_timed_loop", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func loop() {
	read := os.ReadFile
	open := os.Open
	openFile := os.OpenFile
	for {
		_, _ = read("/etc/lip/config.yaml")
		_, _ = open("/etc/lip/config.yaml")
		_, _ = openFile("/etc/lip/config.yaml", 0, 0)
		<-time.After(time.Second)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/alias_read_loop.go", src)
		if err != nil {
			t.Fatal(err)
		}
		foundAliasProbe := false
		for _, w := range res.WatcherMechanisms {
			if strings.Contains(w, "ReadFile") || strings.Contains(w, "Open") || strings.Contains(w, "func-value") {
				foundAliasProbe = true
				break
			}
		}
		if !foundAliasProbe {
			t.Fatalf("expected aliased os.ReadFile/Open/OpenFile probe detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_ticker_created_outside_consumed_inside", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func pollConfig() {
	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/ticker_flow.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected outside-loop ticker consumed inside loop, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_neutral_path_repeated_stat_read", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for {
		_, _ = os.Stat("/etc/lip/config.yaml")
		_, _ = os.ReadFile("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/run.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected neutral-path repeated stat/read polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_AfterFunc_reschedule_with_source_probe", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func arm() {
	var tick func()
	tick = func() {
		_, _ = os.Stat("/etc/lip/config.yaml")
		time.AfterFunc(time.Second, tick)
	}
	tick()
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/arm.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected AfterFunc self-reschedule with source probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_oneshot_time_After_timeout_in_configsource_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import (
	"context"
	"time"
)
func LoadWithTimeout(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return context.DeadlineExceeded
	}
}
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/timeout.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("one-shot time.After timeout must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_oneshot_NewTimer_bounded_op_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package runtimehost
import "time"
func boundedOp() {
	tm := time.NewTimer(5 * time.Second)
	<-tm.C
}
`
		res, err := scanReloadOwnershipSource("internal/infra/runtimehost/bounded.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("one-shot NewTimer must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_oneshot_ReadFile_Open_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import "os"
func Load(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	_ = f
	return os.ReadFile(path)
}
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/load.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("one-shot os.ReadFile/Open must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_unrelated_metrics_housekeeping_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package metrics
import "time"
func scrapeMetrics() {
	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		gather()
	}
}
func housekeeping() {
	for {
		time.Sleep(time.Minute)
		cleanup()
	}
}
`
		res, err := scanReloadOwnershipSource("internal/infra/metrics/scrape.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("unrelated metrics/housekeeping loops must remain allowed, got %v", res.WatcherMechanisms)
		}
	})
}

// Remediation 1.1b attempt 2: package-aware/cross-file mutation indexing and
// evidence-based poll/control-flow (independent narrow review findings).
func TestReloadOwnership_Remediation11bPackageAwareAndPollEvidence(t *testing.T) {
	t.Parallel()

	t.Run("mutation_qualified_runtime_Executor", func(t *testing.T) {
		t.Parallel()
		src := `package runtimebundle
import runtime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
func mutate(exec *runtime.Executor) { exec.SetPreflight(nil) }
`
		res, err := scanReloadOwnershipSource("internal/infra/runtimebundle/mutate_qualified.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("expected qualified *runtime.Executor mutation, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_qualified_func_returning_Executor", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/core/runtime/get.go": `package runtime
type Executor struct{}
func (e *Executor) SetHandler(v any) {}
func GetExecutor() *Executor { return nil }
`,
			"internal/infra/runtimebundle/mutate_get.go": `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
func bad() { runtime.GetExecutor().SetHandler(nil) }
`,
		}
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("expected runtime.GetExecutor().SetHandler mutation, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_cross_file_host_Current", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/infra/runtimehost/host.go": `package runtimehost
type Host struct{}
type RuntimeGeneration struct{}
func (h *Host) Current() *RuntimeGeneration { return nil }
func (g *RuntimeGeneration) SetHandler(v any) {}
`,
			"internal/infra/runtimehost/mutate.go": `package runtimehost
func mutate(host *Host) { host.Current().SetHandler(nil) }
`,
		}
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("expected cross-file host.Current().SetHandler mutation, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_snapshotgen_RuntimeGeneration_negative", func(t *testing.T) {
		t.Parallel()
		src := `package p
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
func mutate(g *snapshotgen.RuntimeGeneration) { g.SetHandler(nil) }
`
		res, err := scanReloadOwnershipSource("internal/core/policy/mutate_snap.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("snapshotgen.RuntimeGeneration must not count as active runtime, got %v", res.MutationSetters)
		}
	})

	t.Run("poll_config_path_var_and_alias", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	path := "/etc/lip/config.yaml"
	cfg := path
	for {
		_, _ = os.Stat(cfg)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/path_alias.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected config path var/alias repeated probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_paren_os_ReadFile_alias", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func loop() {
	read := (os.ReadFile)
	for {
		_, _ = read("/etc/lip/config.yaml")
		<-time.After(time.Second)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/paren_read.go", src)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, w := range res.WatcherMechanisms {
			if strings.Contains(w, "ReadFile") || strings.Contains(w, "func-value") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected parenthesized os.ReadFile alias probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_three_clause_infinite_for", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; ; i++ {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/three_clause.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected for i:=0;;i++ config probe detection, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_ticker_channel_alias", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func poll() {
	ticker := time.NewTicker(time.Second)
	ticks := ticker.C
	for range ticks {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/tick_alias.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected ticker channel alias range poll, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_named_AfterFunc_callback_cycle", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func tick() {
	_, _ = os.Stat("/etc/lip/config.yaml")
	time.AfterFunc(time.Second, tick)
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/named_tick.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected named AfterFunc callback cycle, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_unrelated_nested_Executor_field", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Widget struct{ Executor *Helper }
type Helper struct{ Name string }
func mutate(w *Widget) { w.Executor.Name = "x" }
`
		res, err := scanReloadOwnershipSource("mutate_nested_exec.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("unrelated nested field named Executor must not trigger, got %v", res.MutationSetters)
		}
	})

	t.Run("neg_unrelated_syncCache_loop", func(t *testing.T) {
		t.Parallel()
		src := `package cache
import "time"
func syncCache() {
	for {
		time.Sleep(time.Minute)
		cleanup()
	}
}
`
		res, err := scanReloadOwnershipSource("internal/infra/cache/sync_cache.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("unrelated syncCache housekeeping must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_validateConfigTimeout_one_iteration", func(t *testing.T) {
		t.Parallel()
		src := `package configsource
import (
	"os"
	"time"
)
func validateConfigTimeout() {
	tm := time.NewTimer(5 * time.Second)
	for {
		<-tm.C
		_, _ = os.Stat("/etc/lip/config.yaml")
		break
	}
}
`
		res, err := scanReloadOwnershipSource("internal/infra/configsource/validate_timeout.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("one-iteration validateConfigTimeout must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_finite_two_stage_AfterFunc_chain", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func stage1() {
	time.AfterFunc(time.Second, stage2)
}
func stage2() {
	_, _ = os.Stat("/etc/lip/config.yaml")
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/two_stage.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("finite two-stage AfterFunc chain must remain allowed, got %v", res.WatcherMechanisms)
		}
	})
}

// Remediation 1.1b2: config-path provenance, time.Tick / induction-variable
// repeatability, and callback-cycle precision (poll analysis only).
func TestReloadOwnership_Remediation11b2ConfigPathProvenanceAndCallbackPrecision(t *testing.T) {
	t.Parallel()

	t.Run("poll_package_const_config_path_loop", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
const configPath = "/etc/lip/config.yaml"
func run() {
	for {
		_, _ = os.Stat(configPath)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/const_path.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected package const config path repeated probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_func_param_config_literal_via_caller", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func poll(path string) {
	for {
		_, _ = os.Stat(path)
	}
}
func start() { poll("/etc/lip/config.yaml") }
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/param_path.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected function-parameter config path via in-package caller, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_configsource_receiver_field_repeated_probe", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/infra/configsource/file.go": `package configsource
import (
	"os"
	"time"
)
type FileSource struct {
	path string
}
func NewFileSource(path string) *FileSource {
	return &FileSource{path: path}
}
func (s *FileSource) Watch() {
	for {
		_, _ = os.Stat(s.path)
		time.Sleep(time.Second)
	}
}
`,
			"internal/infra/configsource/file_test.go": `package configsource
import "testing"
func TestCompile(t *testing.T) {
	s := NewFileSource("/etc/lip/config.yaml")
	_ = s
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected configsource receiver field repeated probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_direct_and_aliased_time_Tick", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func direct() {
	for range time.Tick(time.Second) {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
func aliased() {
	ticks := time.Tick(time.Second)
	for range ticks {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/time_tick.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected direct and aliased time.Tick channel loops, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_for_i_lt_1_without_increment_repeated", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; i < 1; {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/no_inc.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected for i:=0; i<1; {} without advance to count as repeated, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_receiver_method_AfterFunc_self_cycle", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/poller.go": `package helper
import (
	"os"
	"time"
)
type poller struct {
	configPath string
	interval   time.Duration
}
func (p *poller) tick() {
	_, _ = os.Stat(p.configPath)
	time.AfterFunc(p.interval, p.tick)
}
func NewPoller(path string) *poller {
	return &poller{configPath: path, interval: time.Second}
}
`,
			"internal/pkg/helper/poller_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) {
	p := NewPoller("/etc/lip/config.yaml")
	_ = p.tick
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected receiver-method AfterFunc self-cycle with config probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_two_method_callback_cycle_with_config", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/cycle.go": `package helper
import (
	"os"
	"time"
)
type scheduler struct {
	configPath string
}
func (s *scheduler) a() {
	_, _ = os.Stat(s.configPath)
	time.AfterFunc(time.Second, s.b)
}
func (s *scheduler) b() {
	time.AfterFunc(time.Second, s.a)
}
`,
			"internal/pkg/helper/cycle_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) {
	s := &scheduler{configPath: "/etc/lip/config.yaml"}
	_ = s.a
	_ = s.b
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected two-method AfterFunc cycle with config evidence, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_config_path_reassigned_to_cache_before_probe", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	path := "/etc/lip/config.yaml"
	path = "/var/cache/lip/blob"
	for {
		_, _ = os.Stat(path)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/reassign_cache.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("config path reassigned to cache before probe must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_shadowed_config_path_replaced", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	path := "/etc/lip/config.yaml"
	{
		path := "/var/cache/lip/blob"
		for {
			_, _ = os.Stat(path)
		}
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/shadow_path.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("shadowed non-config path must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_genuine_one_iteration_with_increment", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; i < 1; i++ {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/one_iter.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("genuine one-iteration loop with increment must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_oneshot_param_field_const_config_read", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
const configPath = "/etc/lip/config.yaml"
type holder struct{ path string }
func NewHolder(path string) *holder { return &holder{path: path} }
func loadOnce(path string, h *holder) {
	_, _ = os.Stat(path)
	_, _ = os.Stat(h.path)
	_, _ = os.Stat(configPath)
}
func start() {
	h := NewHolder("/etc/lip/config.yaml")
	loadOnce("/etc/lip/config.yaml", h)
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/oneshot_paths.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("one-shot config read via param/field/const must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_oneshot_config_plus_unrelated_housekeeping_cycle", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func loadOnce() {
	_, _ = os.Stat("/etc/lip/config.yaml")
}
func housekeep() {
	cleanup()
	time.AfterFunc(time.Second, housekeep)
}
func cleanup() {}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/oneshot_housekeep.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("unrelated one-shot config + housekeeping cycle must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_finite_two_stage_AfterFunc_still_allowed", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func stage1() {
	time.AfterFunc(time.Second, stage2)
}
func stage2() {
	_, _ = os.Stat("/etc/lip/config.yaml")
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/two_stage_b2.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("finite two-stage AfterFunc chain must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_unrelated_time_Tick_metrics_loop", func(t *testing.T) {
		t.Parallel()
		src := `package metrics
import "time"
func scrape() {
	for range time.Tick(time.Second) {
		gather()
	}
}
func gather() {}
`
		res, err := scanReloadOwnershipSource("internal/infra/metrics/tick_scrape.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("unrelated time.Tick metrics loop must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	// Cross-function false-positive: one-shot config arm that schedules an
	// independent housekeeping cycle must not report config polling.
	t.Run("neg_oneshot_arm_schedules_unrelated_cycle", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import (
	"os"
	"time"
)
func arm() {
	_, _ = os.Stat("/etc/lip/config.yaml")
	time.AfterFunc(time.Second, housekeep)
}
func housekeep() {
	cleanup()
	time.AfterFunc(time.Second, housekeep)
}
func cleanup() {}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/arm_housekeep.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("one-shot config arm scheduling unrelated cycle must remain allowed, got %v", res.WatcherMechanisms)
		}
	})
}

// Remediation 1.1b2a: statement-ordered config-path provenance and
// direction/value-aware loop finiteness (path/loop analysis only).
func TestReloadOwnership_Remediation11b2aProvenanceAndLoopFiniteness(t *testing.T) {
	t.Parallel()

	t.Run("poll_caller_local_literal_to_param", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func poll(path string) {
	for {
		_, _ = os.Stat(path)
	}
}
func start() {
	p := "/etc/lip/config.yaml"
	poll(p)
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/caller_local_param.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected caller-local config literal via parameter, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_caller_local_literal_via_constructor_receiver_field", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/host.go": `package helper
import "os"
type host struct{ path string }
func newHost(path string) *host { return &host{path: path} }
func (h *host) run() {
	for {
		_, _ = os.Stat(h.path)
	}
}
func start() {
	p := "/etc/lip/config.yaml"
	h := newHost(p)
	h.run()
}
`,
			"internal/pkg/helper/host_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) {
	_ = start
	var h *host
	_ = h.run
	_ = newHost
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected caller-local config via constructor receiver field, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_body_local_config_decl_before_probe", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for {
		p := "/etc/lip/config.yaml"
		_, _ = os.Stat(p)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/body_local_config.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected body-local config declaration before probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_decrement_wrong_direction_repeated", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; i < 1; i-- {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/wrong_dir_dec.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected wrong-direction decrement loop to count as repeated, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_no_post_i_lt_1_repeated", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; i < 1; {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/no_post_b2a.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected no-post i<1 loop to count as repeated, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_loop_shadow_cache_before_probe", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	p := "/etc/lip/config.yaml"
	for {
		p := "/var/cache/lip/blob"
		_, _ = os.Stat(p)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/loop_shadow_cache.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("loop shadow to cache path before probe must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_loop_reassign_cache_before_probe", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	p := "/etc/lip/config.yaml"
	for {
		p = "/var/cache/lip/blob"
		_, _ = os.Stat(p)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/loop_reassign_cache.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("loop reassignment to cache before probe must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_direct_cache_lip_blob_recurring_probe", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for {
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/cache_lip_blob.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("/var/cache/lip/blob must not count as config evidence, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_one_iter_increment_and_decrement_directions", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func up() {
	for i := 0; i < 1; i++ {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
func down() {
	for i := 1; i > 0; i-- {
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/one_iter_dirs.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("valid one-iteration increment/decrement must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_body_writes_induction_not_oneshot", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; i < 1; i++ {
		i = 0
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/body_write_ind.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("body write to induction variable must not claim one-shot, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_oneshot_caller_local_config_literal", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func loadOnce(path string) {
	_, _ = os.Stat(path)
}
func start() {
	p := "/etc/lip/config.yaml"
	loadOnce(p)
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/oneshot_caller_local.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("one-shot caller-local config literal must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	// --- Remediation 1.1b2a attempt 2: recurrent states, init scope, parallel
	// assign, goto cycles, lexical induction identity, call-propagation fixpoint.

	t.Run("poll_recurrent_reassign_config_after_probe", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	p := "/var/cache/lip/blob"
	for {
		_, _ = os.Stat(p)
		p = "/etc/lip/config.yaml"
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/recurrent_config_after.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("recurrent iterations probing config must be detected, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_transient_config_then_recurrent_cache", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	p := "/etc/lip/config.yaml"
	for {
		_, _ = os.Stat(p)
		p = "/var/cache/lip/blob"
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/transient_config_only.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("config probed only on transient first iteration must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_if_init_shadow_outer_config_persists", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run(cond bool) {
	outer := "/etc/lip/config.yaml"
	for {
		if outer := "/var/cache/lip/blob"; cond {
			_ = outer
		}
		_, _ = os.Stat(outer)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/if_init_shadow_keep_outer.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("if-init shadow must not kill outer config before probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_if_init_shadow_outer_cache_persists", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run(cond bool) {
	outer := "/var/cache/lip/blob"
	for {
		if outer := "/etc/lip/config.yaml"; cond {
			_ = outer
		}
		_, _ = os.Stat(outer)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/if_init_shadow_keep_cache.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("if-init config shadow must not leak into outer cache probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_switch_init_shadow_outer_cache", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run(v int) {
	outer := "/var/cache/lip/blob"
	for {
		switch outer := "/etc/lip/config.yaml"; v {
		case 1:
			_ = outer
		}
		_, _ = os.Stat(outer)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/switch_init_shadow_cache.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("switch-init config shadow must not leak into outer cache probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_typeswitch_init_shadow_outer_cache", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run(x any) {
	outer := "/var/cache/lip/blob"
	for {
		switch outer := "/etc/lip/config.yaml"; x.(type) {
		case int:
			_ = outer
		}
		_, _ = os.Stat(outer)
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/typeswitch_init_shadow_cache.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("type-switch-init config shadow must not leak into outer cache probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_parallel_assign_swap_config_into_probe", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/parallel_swap_pos.go": `package helper
import "os"
func run() {
	p := "/etc/lip/config.yaml"
	q := "/var/cache/lip/blob"
	p, q = q, p
	for {
		_, _ = os.Stat(q)
	}
}
`,
			"internal/pkg/helper/parallel_swap_pos_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("parallel swap must move config into probed var, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_parallel_assign_swap_cache_into_probe", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/parallel_swap_neg.go": `package helper
import "os"
func run() {
	p := "/var/cache/lip/blob"
	q := "/etc/lip/config.yaml"
	p, q = q, p
	for {
		_, _ = os.Stat(q)
	}
}
`,
			"internal/pkg/helper/parallel_swap_neg_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("parallel swap to cache before probe must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_parallel_assign_swap_shadowed_decls", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/parallel_swap_shadow.go": `package helper
import "os"
func run() {
	p := "/etc/lip/config.yaml"
	q := "/var/cache/lip/blob"
	_ = q
	{
		p := "/var/cache/lip/blob"
		q := "/etc/lip/config.yaml"
		p, q = q, p
		_ = p
		_ = q
	}
	for {
		_, _ = os.Stat(p)
	}
}
`,
			"internal/pkg/helper/parallel_swap_shadow_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("shadowed parallel swap must not kill outer config poll, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_backward_goto_invalidates_oneshot", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_back.go": `package helper
import "os"
func run() {
	for i := 0; i < 1; i++ {
	again:
		_, _ = os.Stat("/etc/lip/config.yaml")
		goto again
	}
}
`,
			"internal/pkg/helper/goto_back_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("backward goto cycle must invalidate one-shot induction, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_forward_goto_oneshot_still_allowed", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_fwd.go": `package helper
import "os"
func run() {
	for i := 0; i < 1; i++ {
		goto after
	after:
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`,
			"internal/pkg/helper/goto_fwd_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("forward goto without cycle must not report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_induction_block_shadow_write", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; i < 1; i++ {
		{
			i := 0
			i++
		}
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/ind_block_shadow.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("block shadow write must not invalidate outer induction, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_induction_closure_param_shadow", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; i < 1; i++ {
		func(i int) {
			i++
		}(0)
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/ind_closure_shadow.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("closure-param shadow write must not invalidate outer induction, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_induction_range_shadow", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	xs := []int{1}
	for i := 0; i < 1; i++ {
		for i := range xs {
			i++
			_ = i
		}
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/ind_range_shadow.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("range shadow must not invalidate outer induction, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_induction_true_outer_write", func(t *testing.T) {
		t.Parallel()
		src := `package helper
import "os"
func run() {
	for i := 0; i < 1; i++ {
		{
			i = 0
		}
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`
		res, err := scanReloadOwnershipSource("internal/pkg/helper/ind_outer_write.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("true outer induction write must invalidate one-shot, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_fixpoint_32_reverse_chain_config", func(t *testing.T) {
		t.Parallel()
		var b strings.Builder
		b.WriteString("package helper\nimport \"os\"\n")
		for i := 31; i >= 0; i-- {
			if i == 31 {
				b.WriteString("func f31(path string) {\n\tfor {\n\t\t_, _ = os.Stat(path)\n\t}\n}\n")
				continue
			}
			b.WriteString("func f")
			b.WriteString(itoaDec(i))
			b.WriteString("(path string) { f")
			b.WriteString(itoaDec(i + 1))
			b.WriteString("(path) }\n")
		}
		b.WriteString("func start() {\n\tp := \"/etc/lip/config.yaml\"\n\tf0(p)\n}\n")
		src := b.String()
		files := map[string]string{
			"internal/pkg/helper/chain32_pos.go": src,
			"internal/pkg/helper/chain32_pos_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = start }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("32-function reverse chain with config must converge, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_fixpoint_32_reverse_chain_cache", func(t *testing.T) {
		t.Parallel()
		var b strings.Builder
		b.WriteString("package helper\nimport \"os\"\n")
		for i := 31; i >= 0; i-- {
			if i == 31 {
				b.WriteString("func f31(path string) {\n\tfor {\n\t\t_, _ = os.Stat(path)\n\t}\n}\n")
				continue
			}
			b.WriteString("func f")
			b.WriteString(itoaDec(i))
			b.WriteString("(path string) { f")
			b.WriteString(itoaDec(i + 1))
			b.WriteString("(path) }\n")
		}
		b.WriteString("func start() {\n\tp := \"/var/cache/lip/blob\"\n\tf0(p)\n}\n")
		src := b.String()
		files := map[string]string{
			"internal/pkg/helper/chain32_neg.go": src,
			"internal/pkg/helper/chain32_neg_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = start }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("32-function reverse cache chain must remain allowed, got %v", res.WatcherMechanisms)
		}
	})
}

// Remediation 1.1b2a attempt 3: for-init construct scope, path-specific
// terminating-branch evidence, and in-body goto reachability.
func TestReloadOwnership_Remediation11b2aFinalForInitTerminateGoto(t *testing.T) {
	t.Parallel()

	t.Run("neg_ForInit_define_shadow_false_cond_outer_cache", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/for_init_shadow_cache.go": `package helper
import "os"
func run() {
	p := "/var/cache/lip/blob"
	for p := "/etc/lip/config.yaml"; false; {
		_ = p
	}
	for {
		_, _ = os.Stat(p)
	}
}
`,
			"internal/pkg/helper/for_init_shadow_cache_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("for-init := shadow must not leak config into outer cache probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_ForInit_define_shadow_false_cond_outer_config", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/for_init_shadow_config.go": `package helper
import "os"
func run() {
	p := "/etc/lip/config.yaml"
	for p := "/var/cache/lip/blob"; false; {
		_ = p
	}
	for {
		_, _ = os.Stat(p)
	}
}
`,
			"internal/pkg/helper/for_init_shadow_config_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("for-init := cache shadow must not kill outer config poll, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_ForInit_assign_false_cond_persists_outer_config", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/for_init_assign_config.go": `package helper
import "os"
func run() {
	p := "/var/cache/lip/blob"
	for p = "/etc/lip/config.yaml"; false; {
		_ = p
	}
	for {
		_, _ = os.Stat(p)
	}
}
`,
			"internal/pkg/helper/for_init_assign_config_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("for-init = config with false cond must persist to outer probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_ForInit_assign_false_cond_persists_outer_cache", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/for_init_assign_cache.go": `package helper
import "os"
func run() {
	p := "/etc/lip/config.yaml"
	for p = "/var/cache/lip/blob"; false; {
		_ = p
	}
	for {
		_, _ = os.Stat(p)
	}
}
`,
			"internal/pkg/helper/for_init_assign_cache_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("for-init = cache with false cond must clear outer config, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_ForInit_define_body_exec_no_leak", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/for_init_body_exec.go": `package helper
import "os"
func run() {
	p := "/var/cache/lip/blob"
	for p := "/etc/lip/config.yaml"; true; {
		_ = p
		break
	}
	for {
		_, _ = os.Stat(p)
	}
}
`,
			"internal/pkg/helper/for_init_body_exec_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("for-init := with body exec must not leak into outer cache probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_ForInit_nested_define_shadows", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/for_init_nested.go": `package helper
import "os"
func run() {
	p := "/var/cache/lip/blob"
	for p := "/etc/lip/config.yaml"; false; {
		for p := "/etc/lip/config.yaml"; false; {
			_ = p
		}
		_ = p
	}
	for {
		_, _ = os.Stat(p)
	}
}
`,
			"internal/pkg/helper/for_init_nested_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("nested for-init := shadows must not leak into outer cache, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Terminate_config_then_Break_cache_recurrent", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_break.go": `package helper
import "os"
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			break
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/term_break_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("config probe only on break path must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Terminate_config_then_Continue_recurrent", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_continue.go": `package helper
import "os"
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			continue
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/term_continue_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("config probe then continue must report recurrent polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Terminate_config_then_Return", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_return.go": `package helper
import "os"
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			return
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/term_return_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("config probe then return must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Terminate_if_else_config_break_vs_cache", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_ifelse.go": `package helper
import "os"
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			break
		} else {
			_, _ = os.Stat("/var/cache/lip/blob")
		}
	}
}
`,
			"internal/pkg/helper/term_ifelse_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("if/else config-break vs cache fallthrough must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Terminate_nested_Break_only_inner", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_nested_break.go": `package helper
import "os"
func run() {
	for {
		for {
			_, _ = os.Stat("/etc/lip/config.yaml")
			break
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/term_nested_break_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("inner break after config must not pollute outer recurrent cache, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Terminate_labeled_Break_exits", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_labeled_break.go": `package helper
import "os"
func run(stop bool) {
loop:
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			break loop
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/term_labeled_break_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("labeled break after config must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Terminate_labeled_Continue_recurrent", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_labeled_continue.go": `package helper
import "os"
func run(stop bool) {
loop:
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			continue loop
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/term_labeled_continue_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("labeled continue after config must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Goto_forward_in_body_not_exit", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_fwd_inbody.go": `package helper
import "os"
func run() {
	for {
		goto probe
	probe:
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`,
			"internal/pkg/helper/goto_fwd_inbody_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("forward in-body goto to config probe must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Goto_backward_in_body_positive", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_back_inbody.go": `package helper
import "os"
func run() {
	for {
	again:
		_, _ = os.Stat("/etc/lip/config.yaml")
		goto again
	}
}
`,
			"internal/pkg/helper/goto_back_inbody_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("backward in-body goto config probe must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Goto_forward_skips_config_probe", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_skip_probe.go": `package helper
import "os"
func run() {
	for {
		goto after
		_, _ = os.Stat("/etc/lip/config.yaml")
	after:
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/goto_skip_probe_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("goto over unreachable config probe must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Goto_from_body_to_outer_label", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_body_to_outer.go": `package helper
import "os"
func run(once bool) {
	for {
		if once {
			_, _ = os.Stat("/etc/lip/config.yaml")
			goto done
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
done:
	return
}
`,
			"internal/pkg/helper/goto_body_to_outer_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("goto from body to outer label after config must remain allowed, got %v", res.WatcherMechanisms)
		}
	})
}

// Remediation 1.1b2aX: built-in panic termination, nested break/continue target
// resolution through switch/select/loops, and nested in-body goto labels.
func TestReloadOwnership_PanicNestedBreakNestedGoto(t *testing.T) {
	t.Parallel()

	t.Run("neg_Panic_builtin_terminates_config_branch", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_panic.go": `package helper
import "os"
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/term_panic_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("config probe then built-in panic must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Panic_shadowed_function_returns_recurrent", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/term_panic_shadow.go": `package helper
import "os"
func run(stop bool) {
	panic := func(v any) {}
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/term_panic_shadow_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("shadowed panic function that returns must report recurrent polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_NestedBreak_unlabeled_switch_break_recurrent", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/switch_break.go": `package helper
import "os"
func run(mode int) {
	for {
		switch mode {
		case 1:
			_, _ = os.Stat("/etc/lip/config.yaml")
			break
		}
	}
}
`,
			"internal/pkg/helper/switch_break_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("unlabeled break of switch must leave loop recurrent with config, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_NestedBreak_labeled_switch_break_recurrent", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/switch_break_label.go": `package helper
import "os"
func run(mode int) {
	for {
	sw:
		switch mode {
		case 1:
			_, _ = os.Stat("/etc/lip/config.yaml")
			break sw
		}
	}
}
`,
			"internal/pkg/helper/switch_break_label_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("labeled break of switch must leave loop recurrent with config, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_NestedBreak_select_break_recurrent", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/select_break.go": `package helper
import "os"
func run(ch <-chan int) {
	for {
		select {
		case <-ch:
			_, _ = os.Stat("/etc/lip/config.yaml")
			break
		}
	}
}
`,
			"internal/pkg/helper/select_break_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("unlabeled break of select must leave loop recurrent with config, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_NestedBreak_inner_loop_break_outer_continues", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/inner_loop_break.go": `package helper
import "os"
func run() {
	for {
		for {
			break
		}
		_, _ = os.Stat("/etc/lip/config.yaml")
	}
}
`,
			"internal/pkg/helper/inner_loop_break_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("inner-loop break must not prevent outer config backedge, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_NestedBreak_analyzed_loop_labeled_break", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/loop_labeled_break.go": `package helper
import "os"
func run(stop bool) {
loop:
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			break loop
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/loop_labeled_break_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("labeled break of analyzed loop after config must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_ControlTarget_labeled_continue_recurrent", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/loop_labeled_continue.go": `package helper
import "os"
func run(stop bool) {
loop:
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			continue loop
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/loop_labeled_continue_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("labeled continue of analyzed loop after config must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_ControlTarget_labeled_continue_outer_from_inner", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/continue_outer_from_inner.go": `package helper
import "os"
func run(stop bool) {
outer:
	for {
		for {
			if stop {
				_, _ = os.Stat("/etc/lip/config.yaml")
				continue outer
			}
			break
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/continue_outer_from_inner_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("config only on continue-outer from opaque inner must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_NestedGoto_forward_nested_block", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_nested_fwd.go": `package helper
import "os"
func run() {
	for {
		if true {
			goto nested
		nested:
			_, _ = os.Stat("/etc/lip/config.yaml")
		}
	}
}
`,
			"internal/pkg/helper/goto_nested_fwd_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("forward goto to nested-block label must report config polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_NestedGoto_backward_nested_block", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_nested_back.go": `package helper
import "os"
func run() {
	for {
		{
		again:
			_, _ = os.Stat("/etc/lip/config.yaml")
			goto again
		}
	}
}
`,
			"internal/pkg/helper/goto_nested_back_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("backward goto to nested-block label must report config polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_NestedGoto_skip_over_nested_config", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_nested_skip.go": `package helper
import "os"
func run() {
	for {
		{
			goto after
			_, _ = os.Stat("/etc/lip/config.yaml")
		after:
			_, _ = os.Stat("/var/cache/lip/blob")
		}
	}
}
`,
			"internal/pkg/helper/goto_nested_skip_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("goto skipping nested config probe must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_NestedGoto_outer_target_exits", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/goto_nested_outer.go": `package helper
import "os"
func run(once bool) {
	for {
		if once {
			_, _ = os.Stat("/etc/lip/config.yaml")
			goto done
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
done:
	return
}
`,
			"internal/pkg/helper/goto_nested_outer_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("goto to outer label after config must remain allowed, got %v", res.WatcherMechanisms)
		}
	})
}

// Remediation 1.1b2aX: panic terminality only when the identifier resolves to the
// predeclared universe built-in — package-level and enclosing lexical shadows return.
func TestReloadOwnership_PanicShadowResolution(t *testing.T) {
	t.Parallel()

	t.Run("poll_PanicShadow_package_func_same_file", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_pkg_func.go": `package helper
import "os"
func panic(v any) {}
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_pkg_func_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run; _ = panic }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("package-level func panic shadow must report recurrent polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_package_func_cross_file", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_pkg_cross_decl.go": `package helper
func panic(v any) {}
`,
			"internal/pkg/helper/panic_pkg_cross_use.go": `package helper
import "os"
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_pkg_cross_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run; _ = panic }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("cross-file package-level func panic shadow must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_package_var_func", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_pkg_var.go": `package helper
import "os"
var panic = func(v any) {}
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_pkg_var_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run; _ = panic }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("package-level var panic shadow must report recurrent polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_enclosing_block_local", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_enclosing_block.go": `package helper
import "os"
func run(stop bool) {
	{
		panic := func(v any) {}
		for {
			if stop {
				_, _ = os.Stat("/etc/lip/config.yaml")
				panic("stop")
			}
			_, _ = os.Stat("/var/cache/lip/blob")
		}
	}
}
`,
			"internal/pkg/helper/panic_enclosing_block_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("enclosing-block local panic shadow must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_func_param", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_param.go": `package helper
import "os"
func run(panic func(any), stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_param_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("function parameter panic shadow must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_if_init", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_if_init.go": `package helper
import "os"
func run(stop bool) {
	for {
		if panic := func(v any) {}; stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_if_init_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("if-init panic shadow must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_for_init", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_for_init.go": `package helper
import "os"
func run(stop bool) {
	for panic := func(v any) {}; ; {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_for_init_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("for-init panic shadow must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_func_lit_param", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_funclit_param.go": `package helper
import "os"
func run(stop bool) {
	handler := func(panic func(any), stop bool) {
		for {
			if stop {
				_, _ = os.Stat("/etc/lip/config.yaml")
				panic("stop")
			}
			_, _ = os.Stat("/var/cache/lip/blob")
		}
	}
	handler(func(any) {}, stop)
}
`,
			"internal/pkg/helper/panic_funclit_param_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("function-literal parameter panic shadow must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_func_lit_local", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_funclit_local.go": `package helper
import "os"
func run(stop bool) {
	handler := func(stop bool) {
		panic := func(v any) {}
		for {
			if stop {
				_, _ = os.Stat("/etc/lip/config.yaml")
				panic("stop")
			}
			_, _ = os.Stat("/var/cache/lip/blob")
		}
	}
	handler(stop)
}
`,
			"internal/pkg/helper/panic_funclit_local_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("function-literal local panic shadow must report polling, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_PanicShadow_inner_block_then_builtin_terminates", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_scope_boundary.go": `package helper
import "os"
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			{
				panic := func(v any) {}
				panic("shadow-returns")
			}
			panic("builtin-terminates")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_scope_boundary_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("inner shadow must not leak; later built-in panic must terminate, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Panic_builtin_unshadowed_terminates", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_builtin_neg.go": `package helper
import "os"
func run(stop bool) {
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_builtin_neg_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("unshadowed built-in panic must remain terminating, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_PanicShadow_selector_method_not_builtin", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_selector.go": `package helper
import "os"
type killer struct{}
func (killer) panic(v any) {}
func run(stop bool) {
	var k killer
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			k.panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_selector_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("selector/method panic must not be classified terminal, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_PanicShadow_other_alias_does_not_affect_builtin", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/panic_other_alias.go": `package helper
import "os"
func run(stop bool) {
	boom := func(v any) {}
	_ = boom
	for {
		if stop {
			_, _ = os.Stat("/etc/lip/config.yaml")
			panic("stop")
		}
		_, _ = os.Stat("/var/cache/lip/blob")
	}
}
`,
			"internal/pkg/helper/panic_other_alias_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("alias named other than panic must not affect built-in resolution, got %v", res.WatcherMechanisms)
		}
	})
}

// itoaDec formats a small non-negative int without strconv (test helper only).
func itoaDec(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// Remediation 1.1b2b: canonical callback identities, transitive helper config
// probes, and lexical/flow-sensitive function-alias kills (poll analysis only).
func TestReloadOwnership_Remediation11b2bCanonicalCallbackHelperAlias(t *testing.T) {
	t.Parallel()

	t.Run("poll_Helper_one_hop_repeated_loop", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/onehop.go": `package helper
import "os"
func probe() { _, _ = os.Stat("/etc/lip/config.yaml") }
func run() {
	for {
		probe()
	}
}
`,
			"internal/pkg/helper/onehop_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run; _ = probe }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected one-hop helper config probe in repeated loop, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Helper_three_hop_repeated_loop", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/threehop.go": `package helper
import "os"
func probe3(path string) { _, _ = os.Stat(path) }
func probe2(path string) { probe3(path) }
func probe1(path string) { probe2(path) }
func run() {
	for {
		probe1("/etc/lip/config.yaml")
	}
}
`,
			"internal/pkg/helper/threehop_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected three-hop helper chain config probe in repeated loop, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Callback_AfterFunc_Helper_probe_chain", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/cb_probe.go": `package helper
import (
	"os"
	"time"
)
type poller struct {
	configPath string
	interval   time.Duration
}
func probe2(path string) { _, _ = os.Stat(path) }
func probe1(path string) { probe2(path) }
func (p *poller) tick() {
	probe1(p.configPath)
	time.AfterFunc(p.interval, p.tick)
}
func NewPoller(path string) *poller {
	return &poller{configPath: path, interval: time.Second}
}
`,
			"internal/pkg/helper/cb_probe_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) {
	p := NewPoller("/etc/lip/config.yaml")
	_ = p.tick
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected receiver AfterFunc cycle with transitive helper probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_SCC_multi_method_transitive_Helper", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/scc_helper.go": `package helper
import (
	"os"
	"time"
)
type scheduler struct {
	configPath string
}
func probe2(path string) { _, _ = os.Stat(path) }
func probe1(path string) { probe2(path) }
func (s *scheduler) a() {
	probe1(s.configPath)
	time.AfterFunc(time.Second, s.b)
}
func (s *scheduler) b() {
	time.AfterFunc(time.Second, s.a)
}
`,
			"internal/pkg/helper/scc_helper_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) {
	s := &scheduler{configPath: "/etc/lip/config.yaml"}
	_ = s.a
	_ = s.b
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected multi-method SCC with transitive helper probe, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Alias_parenthesized_os_Stat_retained", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/alias_paren.go": `package helper
import "os"
func run() {
	stat := (os.Stat)
	configPath := "/etc/lip/config.yaml"
	for {
		_, _ = stat(configPath)
	}
}
`,
			"internal/pkg/helper/alias_paren_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected parenthesized os.Stat alias retained into loop, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Alias_to_Alias_retains_os_Stat", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/alias_chain.go": `package helper
import "os"
func run() {
	a := os.Stat
	b := a
	configPath := "/etc/lip/config.yaml"
	for {
		_, _ = b(configPath)
	}
}
`,
			"internal/pkg/helper/alias_chain_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected alias-to-alias flow retaining os.Stat, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("poll_Alias_branch_may_retain_os_Stat", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/alias_branch.go": `package helper
import "os"
func cacheStat(name string) (os.FileInfo, error) { return nil, nil }
func run(cond bool) {
	stat := os.Stat
	if cond {
		stat = cacheStat
	}
	configPath := "/etc/lip/config.yaml"
	for {
		_, _ = stat(configPath)
	}
}
`,
			"internal/pkg/helper/alias_branch_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) == 0 {
			t.Fatalf("expected conservative branch retaining os.Stat into loop, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Callback_same_method_name_unrelated_receivers", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/tick_collision.go": `package helper
import (
	"os"
	"time"
)
type configArm struct{}
func (c *configArm) tick() {
	_, _ = os.Stat("/etc/lip/config.yaml")
}
type housekeeper struct{}
func (h *housekeeper) tick() {
	cleanup()
	time.AfterFunc(time.Second, h.tick)
}
func cleanup() {}
func armOnce() {
	var c configArm
	c.tick()
}
`,
			"internal/pkg/helper/tick_collision_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) {
	_ = armOnce
	var h housekeeper
	_ = h.tick
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("unrelated receivers both named tick must not conflate, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Callback_same_method_name_different_packages", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/cfgprobe/arm.go": `package cfgprobe
import "os"
type Arm struct{}
func (a *Arm) tick() {
	_, _ = os.Stat("/etc/lip/config.yaml")
}
`,
			"internal/pkg/cfgprobe/arm_test.go": `package cfgprobe
import "testing"
func TestCompile(t *testing.T) {
	var a Arm
	_ = a.tick
}
`,
			"internal/pkg/housekeep/cycle.go": `package housekeep
import "time"
type Keeper struct{}
func (k *Keeper) tick() {
	noop()
	time.AfterFunc(time.Second, k.tick)
}
func noop() {}
`,
			"internal/pkg/housekeep/cycle_test.go": `package housekeep
import "testing"
func TestCompile(t *testing.T) {
	var k Keeper
	_ = k.tick
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("same method name in different packages must not conflate, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Alias_kill_before_loop", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/alias_kill.go": `package helper
import "os"
func cacheStat(name string) (os.FileInfo, error) { return nil, nil }
func run() {
	stat := os.Stat
	stat = cacheStat
	configPath := "/etc/lip/config.yaml"
	for {
		_, _ = stat(configPath)
	}
}
`,
			"internal/pkg/helper/alias_kill_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("alias reassigned to cacheStat before loop must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Alias_shadowed_cache_helper", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/alias_shadow.go": `package helper
import "os"
func cacheStat(name string) (os.FileInfo, error) { return nil, nil }
func run() {
	stat := os.Stat
	_ = stat
	{
		stat := cacheStat
		configPath := "/etc/lip/config.yaml"
		for {
			_, _ = stat(configPath)
		}
	}
}
`,
			"internal/pkg/helper/alias_shadow_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("shadowed stat bound to cache helper must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Helper_finite_chain_no_repetition", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/finite_helper.go": `package helper
import "os"
func probe2(path string) { _, _ = os.Stat(path) }
func probe1(path string) { probe2(path) }
func run() {
	probe1("/etc/lip/config.yaml")
}
`,
			"internal/pkg/helper/finite_helper_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = run }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("finite helper chain without repetition must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_Callback_oneshot_arm_schedules_unrelated_SCC", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/arm_scc.go": `package helper
import (
	"os"
	"time"
)
type keeper struct{}
func (k *keeper) tick() {
	cleanup()
	time.AfterFunc(time.Second, k.tick)
}
func cleanup() {}
func arm() {
	_, _ = os.Stat("/etc/lip/config.yaml")
	var k keeper
	time.AfterFunc(time.Second, k.tick)
}
`,
			"internal/pkg/helper/arm_scc_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) { _ = arm }
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("oneshot config arm scheduling unrelated housekeeping SCC must remain allowed, got %v", res.WatcherMechanisms)
		}
	})

	t.Run("neg_SCC_housekeeping_Helper_no_config", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/pkg/helper/hk_scc.go": `package helper
import "time"
type keeper struct{}
func sweep() {}
func (k *keeper) a() {
	sweep()
	time.AfterFunc(time.Second, k.b)
}
func (k *keeper) b() {
	time.AfterFunc(time.Second, k.a)
}
`,
			"internal/pkg/helper/hk_scc_test.go": `package helper
import "testing"
func TestCompile(t *testing.T) {
	var k keeper
	_ = k.a
	_ = k.b
}
`,
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.WatcherMechanisms) != 0 {
			t.Fatalf("housekeeping SCC with non-config helpers must remain allowed, got %v", res.WatcherMechanisms)
		}
	})
}

// Remediation 1.1b1: interface-return active-runtime identity and lexical
// declaration-scope mutation tracking (mutation analysis only).
func TestReloadOwnership_Remediation11b1InterfaceAndLexicalScope(t *testing.T) {
	t.Parallel()

	t.Run("mutation_interface_Current_Executor_positive", func(t *testing.T) {
		t.Parallel()
		src := `package runtimehost
import coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"

type Host interface {
	Current() *coreruntime.Executor
}

func mutate(h Host) {
	h.Current().SetToolCallFinalizers(nil)
}
`
		res, err := scanReloadOwnershipSource("internal/infra/runtimehost/iface_host.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("expected interface Current()*Executor mutation, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_interface_Current_Widget_negative", func(t *testing.T) {
		t.Parallel()
		src := `package runtimehost
type Widget struct{}
type Host interface {
	Current() *Widget
}
func mutate(h Host) {
	h.Current().SetToolCallFinalizers(nil)
}
`
		res, err := scanReloadOwnershipSource("internal/infra/runtimehost/iface_widget.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("unrelated interface Current()*Widget must not trigger, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_nested_block_shadow_then_outer", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Widget struct{}
func (w *Widget) SetName(s string) {}
func (e *Executor) SetToolCallFinalizers(v any) {}
func mutate(exec *Executor) {
	{
		exec := &Widget{}
		exec.SetName("safe")
	}
	exec.SetToolCallFinalizers(nil)
}
`
		res, err := scanReloadOwnershipSource("mutate_block_shadow.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 1 {
			t.Fatalf("expected only outer active mutation (not shadowed SetName), got %v", res.MutationSetters)
		}
		if !strings.Contains(res.MutationSetters[0], "SetToolCallFinalizers") {
			t.Fatalf("expected SetToolCallFinalizers hit, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_if_init_shadow_negative", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Widget struct{}
func (w *Widget) SetName(s string) {}
func mutate(exec *Executor) {
	if exec := (&Widget{}); exec != nil {
		exec.SetName("safe")
	}
}
`
		res, err := scanReloadOwnershipSource("mutate_if_init_shadow.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("if-init Widget shadow must not trigger, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_for_init_shadow_negative", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Widget struct{}
func (w *Widget) SetName(s string) {}
func mutate(exec *Executor) {
	for exec := (&Widget{}); exec != nil; {
		exec.SetName("safe")
		break
	}
}
`
		res, err := scanReloadOwnershipSource("mutate_for_init_shadow.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("for-init Widget shadow must not trigger, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_func_literal_param_shadow_negative", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Widget struct{}
func (w *Widget) SetName(s string) {}
func mutate(exec *Executor) {
	_ = func(exec *Widget) {
		exec.SetName("safe")
	}
}
`
		res, err := scanReloadOwnershipSource("mutate_func_lit_shadow.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("func-literal param Widget shadow must not trigger, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_reassign_active_to_unrelated_untracks", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Executor struct{}
type Widget struct{}
type Setter interface{ SetName(string) }
func (w *Widget) SetName(s string) {}
func (e *Executor) SetName(s string) {}
func mutate(exec *Executor) {
	var x Setter = exec
	x = &Widget{}
	x.SetName("safe")
}
`
		res, err := scanReloadOwnershipSource("mutate_reassign_untrack.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("active reassigned to unrelated must not trigger, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_reassign_unrelated_from_active_tracks", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Executor struct{}
type Widget struct{}
type Setter interface{ SetName(string) }
func (w *Widget) SetName(s string) {}
func (e *Executor) SetName(s string) {}
func mutate(exec *Executor) {
	var y Setter = &Widget{}
	y = exec
	y.SetName("blocked")
}
`
		res, err := scanReloadOwnershipSource("mutate_reassign_track.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("unrelated reassigned from active must detect mutation, got %v", res.MutationSetters)
		}
	})
}

// Remediation 1.1b1 final: deterministic cross-package embedded interfaces,
// function-literal named-result shadowing, and control-flow-safe lexical state.
func TestReloadOwnership_Remediation11b1FinalDeterminismAndControlFlow(t *testing.T) {
	t.Parallel()

	crossPkgEmbeddedOverlay := func() map[string]string {
		return map[string]string{
			"internal/core/runtime/executor.go": `package runtime
type Executor struct{}
func (e *Executor) SetToolCallFinalizers(v any) {}
`,
			"internal/contracts/provider.go": `package contracts
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
type Source interface {
	Current() *runtime.Executor
}
type Provider interface {
	Source
}
type Unrelated interface {
	Widget() *struct{ Name string }
}
`,
			"internal/infra/runtimehost/host.go": `package runtimehost
import "github.com/matdev83/go-llm-interactive-proxy/internal/contracts"
type Host interface {
	contracts.Provider
}
func mutate(h Host) {
	h.Current().SetToolCallFinalizers(nil)
}
type HostUnrelated interface {
	contracts.Unrelated
}
func mutateUnrelated(h HostUnrelated) {
	_ = h.Widget()
}
`,
		}
	}

	t.Run("mutation_cross_package_embedded_interface_deterministic", func(t *testing.T) {
		t.Parallel()
		const rounds = 64
		for i := 0; i < rounds; i++ {
			res, err := scanReloadOwnershipOverlay(crossPkgEmbeddedOverlay())
			if err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
			if len(res.MutationSetters) == 0 {
				t.Fatalf("round %d/%d: expected Host->Provider->Source embedded Current()*Executor mutation, got %v",
					i+1, rounds, res.MutationSetters)
			}
			for _, hit := range res.MutationSetters {
				if strings.Contains(hit, "Widget") {
					t.Fatalf("round %d: unrelated embedded interface must stay negative, got %v", i, res.MutationSetters)
				}
			}
		}
	})

	t.Run("mutation_func_literal_named_result_shadow", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Widget struct{}
func (w *Widget) SetName(s string) {}
func (e *Executor) SetToolCallFinalizers(v any) {}
func outer(exec *Executor) {
	_ = func() (exec *Widget) {
		exec.SetName("safe")
		return
	}
	exec.SetToolCallFinalizers(nil)
}
`
		res, err := scanReloadOwnershipSource("mutate_func_lit_named_result.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 1 {
			t.Fatalf("expected only outer Executor mutation (named-result Widget shadow negative), got %v", res.MutationSetters)
		}
		if !strings.Contains(res.MutationSetters[0], "SetToolCallFinalizers") {
			t.Fatalf("expected outer SetToolCallFinalizers, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_func_literal_multiple_named_results_and_unnamed", func(t *testing.T) {
		t.Parallel()
		src := `package p
type Widget struct{}
func (w *Widget) SetName(s string) {}
func (e *Executor) SetToolCallFinalizers(v any) {}
func outer(exec *Executor) {
	_ = func() (exec *Widget, err error) {
		exec.SetName("safe")
		return
	}
	_ = func() (*Widget, error) {
		return nil, nil
	}
	exec.SetToolCallFinalizers(nil)
}
`
		res, err := scanReloadOwnershipSource("mutate_func_lit_multi_results.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 1 {
			t.Fatalf("expected only outer mutation with multi/unnamed results, got %v", res.MutationSetters)
		}
	})

	reassignFixturePrelude := `package p
type Executor struct{}
type Widget struct{}
type Setter interface{ SetName(string) }
func (w *Widget) SetName(s string) {}
func (e *Executor) SetName(s string) {}
func (e *Executor) SetToolCallFinalizers(v any) {}
`

	t.Run("mutation_branch_conditional_reassign_no_else_still_detects", func(t *testing.T) {
		t.Parallel()
		src := reassignFixturePrelude + `
func mutate(exec *Executor, cond bool) {
	var x Setter = exec
	if cond {
		x = &Widget{}
	}
	x.SetName("maybe")
}
`
		res, err := scanReloadOwnershipSource("mutate_branch_no_else.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("conditional Widget reassignment without else must still detect, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_branch_both_reassign_widget_untracks", func(t *testing.T) {
		t.Parallel()
		src := reassignFixturePrelude + `
func mutate(exec *Executor, cond bool) {
	var x Setter = exec
	if cond {
		x = &Widget{}
	} else {
		x = &Widget{}
	}
	x.SetName("safe")
}
`
		res, err := scanReloadOwnershipSource("mutate_branch_both.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("both branches reassign to Widget may untrack, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_closure_reassign_outer_still_detects", func(t *testing.T) {
		t.Parallel()
		src := reassignFixturePrelude + `
func mutate(exec *Executor) {
	var x Setter = exec
	_ = func() {
		x = &Widget{}
	}
	x.SetName("blocked")
}
`
		res, err := scanReloadOwnershipSource("mutate_closure_reassign.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("uninvoked closure reassignment must not clear outer tracking, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_inside_closure_on_captured_active", func(t *testing.T) {
		t.Parallel()
		src := reassignFixturePrelude + `
func mutate(exec *Executor) {
	var x Setter = exec
	_ = func() {
		x.SetName("blocked")
	}
}
`
		res, err := scanReloadOwnershipSource("mutate_closure_inner.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("mutation inside closure on captured active must detect, got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_loop_conditional_reassign_still_detects", func(t *testing.T) {
		t.Parallel()
		src := reassignFixturePrelude + `
func mutate(exec *Executor, n int) {
	var x Setter = exec
	for i := 0; i < n; i++ {
		x = &Widget{}
	}
	x.SetName("maybe")
}
`
		res, err := scanReloadOwnershipSource("mutate_loop_reassign.go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("loop conditional reassignment must still detect (zero-iteration path), got %v", res.MutationSetters)
		}
	})

	t.Run("mutation_reassignment_fixtures_compile", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"internal/archtestfixture/reassign/reassign.go": `package reassign
type Executor struct{}
type Widget struct{}
type Setter interface{ SetName(string) }
func (w *Widget) SetName(s string) {}
func (e *Executor) SetName(s string) {}
func (e *Executor) SetToolCallFinalizers(v any) {}
func Untrack(exec *Executor) {
	var x Setter = exec
	x = &Widget{}
	x.SetName("safe")
}
func Track(exec *Executor) {
	var y Setter = &Widget{}
	y = exec
	y.SetName("blocked")
}
func BranchNoElse(exec *Executor, cond bool) {
	var x Setter = exec
	if cond {
		x = &Widget{}
	}
	x.SetName("maybe")
}
func BranchBoth(exec *Executor, cond bool) {
	var x Setter = exec
	if cond {
		x = &Widget{}
	} else {
		x = &Widget{}
	}
	x.SetName("safe")
}
func ClosureReassign(exec *Executor) {
	var x Setter = exec
	_ = func() { x = &Widget{} }
	x.SetName("blocked")
}
func ClosureInner(exec *Executor) {
	var x Setter = exec
	_ = func() { x.SetName("blocked") }
}
func LoopReassign(exec *Executor, n int) {
	var x Setter = exec
	for i := 0; i < n; i++ {
		x = &Widget{}
	}
	x.SetName("maybe")
}
`,
			"internal/archtestfixture/reassign/reassign_test.go": `package reassign
import "testing"
func TestFixturesCompile(t *testing.T) {
	e := &Executor{}
	Untrack(e)
	Track(e)
	BranchNoElse(e, true)
	BranchBoth(e, false)
	ClosureReassign(e)
	ClosureInner(e)
	LoopReassign(e, 0)
}
`,
			"internal/archtestfixture/embediface/runtime/executor.go": `package runtime
type Executor struct{}
func (e *Executor) SetToolCallFinalizers(v any) {}
`,
			"internal/archtestfixture/embediface/contracts/provider.go": `package contracts
import "github.com/matdev83/go-llm-interactive-proxy/internal/archtestfixture/embediface/runtime"
type Source interface{ Current() *runtime.Executor }
type Provider interface{ Source }
type Unrelated interface{ Widget() *struct{ Name string } }
`,
			"internal/archtestfixture/embediface/runtimehost/host.go": `package runtimehost
import "github.com/matdev83/go-llm-interactive-proxy/internal/archtestfixture/embediface/contracts"
type Host interface{ contracts.Provider }
func Mutate(h Host) { h.Current().SetToolCallFinalizers(nil) }
type HostUnrelated interface{ contracts.Unrelated }
func MutateUnrelated(h HostUnrelated) { _ = h.Widget() }
`,
			"internal/archtestfixture/embediface/runtimehost/host_test.go": `package runtimehost
import "testing"
type host struct{}
func (host) Current() *struct{} { return nil }
`,
		}
		// Fix embediface test — Host needs a real stub; keep packages type-checkable via go test on contracts/runtime only.
		files["internal/archtestfixture/embediface/runtimehost/host_test.go"] = `package runtimehost
import (
	"testing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/archtestfixture/embediface/contracts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/archtestfixture/embediface/runtime"
)
type stubHost struct{}
func (stubHost) Current() *runtime.Executor { return &runtime.Executor{} }
type stubUnrelated struct{}
func (stubUnrelated) Widget() *struct{ Name string } { return nil }
func TestEmbedCompile(t *testing.T) {
	var _ contracts.Provider = stubHost{}
	Mutate(stubHost{})
	MutateUnrelated(stubUnrelated{})
}
`
		compileReloadOwnershipFixtureModule(t, files)
	})
}

// Remediation 1.1b1x: canonical cross-package embedded-interface identity must
// not collide with an unrelated destination-local type of the same short name.
func TestReloadOwnership_EmbeddedInterfaceCanonicalIdentityCollision(t *testing.T) {
	t.Parallel()

	positiveCollisionOverlay := func() map[string]string {
		return map[string]string{
			"internal/core/runtime/executor.go": `package runtime
type Executor struct{}
func (e *Executor) SetToolCallFinalizers(v any) {}
`,
			"internal/contracts/provider.go": `package contracts
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
type Provider interface {
	Current() *runtime.Executor
}
`,
			"internal/infra/runtimehost/host.go": `package runtimehost
import "github.com/matdev83/go-llm-interactive-proxy/internal/contracts"
type Widget struct{}
func (w *Widget) SetName(s string) {}
// Unrelated local Provider shares the short name with contracts.Provider.
type Provider interface {
	Current() *Widget
}
type Host interface {
	contracts.Provider
}
func mutate(h Host) {
	h.Current().SetToolCallFinalizers(nil)
}
`,
		}
	}

	negativeCollisionOverlay := func() map[string]string {
		return map[string]string{
			"internal/core/runtime/executor.go": `package runtime
type Executor struct{}
func (e *Executor) SetToolCallFinalizers(v any) {}
func (e *Executor) SetName(s string) {}
`,
			"internal/contracts/provider.go": `package contracts
type Widget struct{}
func (w *Widget) SetName(s string) {}
type Provider interface {
	Current() *Widget
}
`,
			"internal/infra/runtimehost/host.go": `package runtimehost
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/contracts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)
// Unrelated local Provider returns *runtime.Executor; Host embeds contracts.Provider.
type Provider interface {
	Current() *runtime.Executor
}
type Host interface {
	contracts.Provider
}
func safe(h Host) {
	h.Current().SetName("safe")
}
`,
		}
	}

	t.Run("positive_external_provider_hidden_by_local_provider", func(t *testing.T) {
		t.Parallel()
		res, err := scanReloadOwnershipOverlay(positiveCollisionOverlay())
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) == 0 {
			t.Fatalf("expected Host->contracts.Provider Current()*Executor mutation despite local Provider collision, got %v", res.MutationSetters)
		}
		for _, hit := range res.MutationSetters {
			if !strings.Contains(hit, "SetToolCallFinalizers") {
				t.Fatalf("expected SetToolCallFinalizers via contracts.Provider, got %v", res.MutationSetters)
			}
		}
	})

	t.Run("negative_external_provider_not_polluted_by_local_provider", func(t *testing.T) {
		t.Parallel()
		res, err := scanReloadOwnershipOverlay(negativeCollisionOverlay())
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("contracts.Provider Current()*Widget must not inherit local Provider Executor methods, got %v", res.MutationSetters)
		}
	})

	t.Run("positive_exact_provider_collision_compiles", func(t *testing.T) {
		t.Parallel()
		files := positiveExactProviderCollisionCompileFiles()
		assertExactSameNameProviderCollision(t, files,
			"internal/archtestfixture/collision/contracts/provider.go",
			"internal/archtestfixture/collision/runtimehost/host.go",
		)
		compileReloadOwnershipFixtureModule(t, files)
	})

	t.Run("negative_exact_provider_collision_compiles_and_scans", func(t *testing.T) {
		t.Parallel()
		files := negativeExactProviderCollisionFiles()
		assertExactSameNameProviderCollision(t, files,
			"internal/contracts/provider.go",
			"internal/infra/runtimehost/host.go",
		)
		if _, ok := files["internal/core/runtime/executor.go"]; !ok {
			t.Fatalf("exact negative collision fixture missing internal/core/runtime/executor.go (keys=%v)", mapKeys(files))
		}
		compileReloadOwnershipFixtureModule(t, files)
		res, err := scanReloadOwnershipOverlay(files)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MutationSetters) != 0 {
			t.Fatalf("exact negative Provider/Provider collision must not report active-runtime mutation, got %v", res.MutationSetters)
		}
	})

	t.Run("collision_stress_count", func(t *testing.T) {
		t.Parallel()
		const rounds = 100
		for i := 0; i < rounds; i++ {
			pos, err := scanReloadOwnershipOverlay(positiveCollisionOverlay())
			if err != nil {
				t.Fatalf("positive round %d: %v", i, err)
			}
			if len(pos.MutationSetters) == 0 {
				t.Fatalf("positive round %d/%d: expected mutation, got %v", i+1, rounds, pos.MutationSetters)
			}
			neg, err := scanReloadOwnershipOverlay(negativeCollisionOverlay())
			if err != nil {
				t.Fatalf("negative round %d: %v", i, err)
			}
			if len(neg.MutationSetters) != 0 {
				t.Fatalf("negative round %d/%d: expected no mutation, got %v", i+1, rounds, neg.MutationSetters)
			}
		}
	})
}

// positiveExactProviderCollisionCompileFiles is the maintained positive compile
// proof: Host embeds contracts.Provider (*Executor) while a destination-local
// Provider returns *Widget — exact short-name collision.
func positiveExactProviderCollisionCompileFiles() map[string]string {
	return map[string]string{
		"internal/archtestfixture/collision/runtime/executor.go": `package runtime
type Executor struct{}
func (e *Executor) SetToolCallFinalizers(v any) {}
func (e *Executor) SetName(s string) {}
`,
		"internal/archtestfixture/collision/contracts/provider.go": `package contracts
import "github.com/matdev83/go-llm-interactive-proxy/internal/archtestfixture/collision/runtime"
type Provider interface {
	Current() *runtime.Executor
}
`,
		"internal/archtestfixture/collision/runtimehost/host.go": `package runtimehost
import "github.com/matdev83/go-llm-interactive-proxy/internal/archtestfixture/collision/contracts"
type Widget struct{}
func (w *Widget) SetName(s string) {}
// Unrelated local Provider shares the short name with contracts.Provider.
type Provider interface {
	Current() *Widget
}
type Host interface {
	contracts.Provider
}
func Mutate(h Host) {
	h.Current().SetToolCallFinalizers(nil)
}
`,
		"internal/archtestfixture/collision/runtimehost/host_test.go": `package runtimehost
import (
	"testing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/archtestfixture/collision/contracts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/archtestfixture/collision/runtime"
)
type stubHost struct{}
func (stubHost) Current() *runtime.Executor { return &runtime.Executor{} }
func TestCollisionCompile(t *testing.T) {
	var _ contracts.Provider = stubHost{}
	Mutate(stubHost{})
	_ = Provider(nil)
}
`,
	}
}

// negativeExactProviderCollisionFiles is the exact hermetic negative collision:
// contracts.Provider and runtimehost.Provider share the short name Provider;
// Host embeds contracts.Provider (Current()*Widget) so SetName is safe.
func negativeExactProviderCollisionFiles() map[string]string {
	return map[string]string{
		"internal/core/runtime/executor.go": `package runtime
type Executor struct{}
func (*Executor) SetName(string) {}
`,
		"internal/contracts/provider.go": `package contracts
type Widget struct{}
func (*Widget) SetName(string) {}
type Provider interface {
	Current() *Widget
}
`,
		"internal/infra/runtimehost/host.go": `package runtimehost
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/contracts"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)
// Exact same short name as contracts.Provider.
type Provider interface {
	Current() *coreruntime.Executor
}
type Host interface {
	contracts.Provider
}
func safe(h Host) {
	h.Current().SetName("safe")
}
`,
		"internal/infra/runtimehost/host_test.go": `package runtimehost
import (
	"testing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/contracts"
)
type stubHost struct{}
func (stubHost) Current() *contracts.Widget { return &contracts.Widget{} }
func TestNegativeCollisionCompile(t *testing.T) {
	var _ contracts.Provider = stubHost{}
	safe(stubHost{})
	_ = Provider(nil)
}
`,
	}
}

// assertExactSameNameProviderCollision fails unless the named paths exist and
// declare type Provider (exact short-name collision), with no WidgetProvider or
// LocalExecutorProvider stand-ins.
func assertExactSameNameProviderCollision(t *testing.T, files map[string]string, requiredPaths ...string) {
	t.Helper()
	for _, p := range requiredPaths {
		src, ok := files[p]
		if !ok {
			t.Fatalf("exact Provider/Provider collision fixture missing path %s (keys=%v)", p, mapKeys(files))
		}
		if !strings.Contains(src, "type Provider interface") {
			t.Fatalf("%s must declare type Provider interface for exact same-name collision, got:\n%s", p, src)
		}
		if strings.Contains(src, "WidgetProvider") || strings.Contains(src, "LocalExecutorProvider") {
			t.Fatalf("%s must not use WidgetProvider/LocalExecutorProvider stand-ins; exact short name Provider required, got:\n%s", p, src)
		}
	}
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// compileReloadOwnershipFixtureModule writes files into a temporary module with
// path github.com/matdev83/go-llm-interactive-proxy and runs `go test ./...`.
// It is hermetic: no repository files are modified.
func compileReloadOwnershipFixtureModule(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	mod := "module github.com/matdev83/go-llm-interactive-proxy\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("fixture module failed to compile/test:\n%s\nerr=%v", buf.String(), err)
	}
}

func TestReloadOwnership_CleanFixtureHasNoViolations(t *testing.T) {
	t.Parallel()
	src := `package p
func serve() {
	_ = other.NewTicker(1)
}
`
	res, err := scanReloadOwnershipSource("clean.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.TracerBootstraps)+len(res.MetricsConstructions)+len(res.ProcessWorkers)+
		len(res.MutationSetters)+len(res.WatcherMechanisms) != 0 {
		t.Fatalf("clean fixture unexpectedly dirty: %+v", res)
	}
}

func TestReloadOwnership_LiveTreeProcessServiceUniqueness(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	tracerSites := scanProductionCalls(t, root, productionScanRoots(), findTracerBootstraps)
	allowedTracer := map[string]int{
		"internal/infra/runtimebundle/bootstrap_plan.go:tracing.Init": 1,
		"internal/infra/tracing/tracing.go:otel.SetTracerProvider":    1,
	}
	assertAllowedCallSites(t, "tracer bootstrap", tracerSites, allowedTracer)

	metricsSites := scanProductionCalls(t, root, productionScanRoots(), findMetricsConstructions)
	allowedMetrics := map[string]int{
		"internal/infra/runtimebundle/build_observability.go:metrics.NewBundle": 1,
		"internal/infra/metrics/registry.go:prometheus.NewRegistry":             1,
		"internal/infra/metrics/registry.go:collectors.NewGoCollector":          1,
		"internal/infra/metrics/registry.go:collectors.NewProcessCollector":     1,
	}
	assertAllowedCallSites(t, "metrics construction", metricsSites, allowedMetrics)

	workerSites := scanProductionCalls(t, root, productionScanRoots(), findProcessWorkerConstructions)
	// Filter same-package NewProcessor definition sites inside terminalwork/app.
	filteredWorkers := filterSitesOutside(workerSites, "internal/core/terminalwork/app/")
	allowedWorkers := map[string]int{
		"internal/infra/runtimebundle/terminal_work.go:terminalworkapp.NewProcessor": 1,
	}
	assertAllowedCallSites(t, "process worker", filteredWorkers, allowedWorkers)
}

func TestReloadOwnership_LiveTreeNoConfigWatcher(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := map[string]bool{
		// Legitimate non-config refresh / worker tickers (req 9.8).
		"internal/infra/runtimebundle/modelcatalog_refresh_loop.go":  true,
		"internal/infra/runtimebundle/modelregistry_refresh_loop.go": true,
		"internal/core/terminalwork/app/ticker.go":                   true,
		"internal/core/terminalwork/app/processor.go":                true,
	}
	files := map[string]string{}
	err := walkProductionGoFiles(root, func(rel, path string, src []byte) error {
		files[filepath.ToSlash(rel)] = string(src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := scanReloadOwnershipOverlay(files)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	seen := map[string]bool{}
	for _, w := range res.WatcherMechanisms {
		parts := strings.SplitN(w, ": ", 2)
		rel := ""
		msg := w
		if strings.HasPrefix(w, "import ") {
			// Import hits lack a file path; skip attributing to a specific file.
			// They are still violations unless every importing file is allowlisted,
			// which is checked via call-site hits below.
			continue
		}
		if len(parts) == 2 {
			pos := parts[0]
			msg = parts[1]
			rel = pos
			if i := strings.LastIndex(pos, ":"); i >= 0 {
				rel = pos[:i]
				if j := strings.LastIndex(rel, ":"); j >= 0 {
					rel = rel[:j]
				}
			}
			rel = filepath.ToSlash(rel)
		}
		if rel == "" || allow[rel] {
			continue
		}
		key := rel + ": " + msg
		if seen[key] {
			continue
		}
		seen[key] = true
		bad = append(bad, key)
	}
	// Also flag watcher imports on non-allowlisted production files.
	err = walkProductionGoFiles(root, func(rel, path string, src []byte) error {
		rel = filepath.ToSlash(rel)
		if allow[rel] {
			return nil
		}
		_, f, err := parseGoSource(path, string(src))
		if err != nil {
			return err
		}
		for _, imp := range fileImportPaths(f) {
			if isWatcherImportPath(imp) {
				key := rel + ": import " + imp
				if !seen[key] {
					seen[key] = true
					bad = append(bad, key)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("config watcher/polling mechanisms forbidden (req 1.6):\n%s", strings.Join(bad, "\n"))
	}
}

func TestReloadOwnership_LiveTreeNoActiveRuntimeMutationInComposition(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := map[string]bool{
		// Startup diagnostics wiring mutates the executor before serve; split in
		// later generation-compile work rather than refactoring here (task 1.1).
		"internal/stdhttp/mount_diagnostics.go:in.Exec.RouteTrace=": true,
	}

	allFiles := map[string]string{}
	composition := map[string]bool{}
	err := walkProductionGoFiles(root, func(rel, path string, src []byte) error {
		rel = filepath.ToSlash(rel)
		allFiles[rel] = string(src)
		if strings.HasPrefix(rel, "internal/infra/runtimebundle/") ||
			strings.HasPrefix(rel, "internal/stdhttp/") ||
			strings.HasPrefix(rel, "cmd/lipstd/") ||
			strings.Contains(rel, "/runtimehost/") ||
			strings.Contains(rel, "/configsource/") {
			composition[rel] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Package-aware overlay over the whole production tree so cross-file and
	// cross-package active-runtime identities resolve during composition gates.
	res, err := scanReloadOwnershipOverlay(allFiles)
	if err != nil {
		t.Fatal(err)
	}

	var bad []string
	seen := map[string]bool{}
	for _, v := range res.MutationSetters {
		parts := strings.SplitN(v, ": ", 2)
		if len(parts) != 2 {
			continue
		}
		pos := parts[0] // path:line:col (path may contain dirs)
		sym := parts[1]
		// Strip :line:col suffix to recover the source path.
		rel := pos
		if i := strings.LastIndex(pos, ":"); i >= 0 {
			rel = pos[:i]
			if j := strings.LastIndex(rel, ":"); j >= 0 {
				rel = rel[:j]
			}
		}
		rel = filepath.ToSlash(rel)
		if !composition[rel] {
			continue
		}
		key := rel + ":" + sym
		if allow[key] || seen[key] {
			continue
		}
		seen[key] = true
		bad = append(bad, key)
	}
	if len(bad) != 0 {
		t.Fatalf("active runtime mutation setters forbidden outside allowlisted startup wiring:\n%s", strings.Join(bad, "\n"))
	}
}

func TestProcessService_CoreExcludesReloadDrivingAdapters(t *testing.T) {
	t.Parallel()
	var rules []forbiddenDep
	for _, sub := range reloadCoreForbiddenImportSubstrs {
		rules = append(rules, forbiddenDep{
			Substr: sub,
			ErrMsg: "internal/core must not import " + sub,
		})
	}
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, rules)
	for _, sub := range reloadCoreForbiddenDirectImports {
		assertDirectImportsExclude(t, "./internal/core/...", sub,
			"internal/core must not directly import "+sub)
	}
}

func TestProcessService_CoreFilesystemImportAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	// Temporary baseline: known production filesystem imports that later tasks
	// must move out of core. Any new filesystem import outside this list fails.
	allow := map[string]bool{
		"internal/core/codexcatalog/fallback.go:embed":              true,
		"internal/core/codexcatalog/fallback.go:os":                 true,
		"internal/core/codexcatalog/discovery.go:os":                true,
		"internal/core/codexcatalog/discovery.go:path/filepath":     true,
		"internal/core/diag/debug_summary.go:os":                    true,
		"internal/core/concurrencyauthority/app/renew_errors.go:os": true,
		"internal/core/config/stream_recovery.go:os":                true,
		"internal/core/config/loader.go:os":                         true,
		"internal/core/config/loader.go:path/filepath":              true,
		"internal/core/config/interleaved.go:os":                    true,
		"internal/core/config/interleaved.go:path/filepath":         true,
	}
	got := coreFilesystemImports(t, root)
	var bad []string
	for site := range got {
		if !allow[site] {
			bad = append(bad, "unexpected filesystem import "+site)
		}
	}
	for site := range allow {
		if !got[site] {
			bad = append(bad, "missing baseline filesystem import "+site)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("core filesystem import allowlist drift:\n%s", strings.Join(bad, "\n"))
	}
}

func TestProcessService_CoreFilesystemImportRejectsNew(t *testing.T) {
	t.Parallel()
	allow := map[string]bool{
		"internal/core/config/loader.go:os": true,
	}
	simulated := map[string]bool{
		"internal/core/config/loader.go:os":           true,
		"internal/core/runtime/host.go:path/filepath": true, // new forbidden
	}
	var bad []string
	for site := range simulated {
		if !allow[site] {
			bad = append(bad, site)
		}
	}
	if len(bad) != 1 || bad[0] != "internal/core/runtime/host.go:path/filepath" {
		t.Fatalf("expected new forbidden filesystem import to be rejected, got %v", bad)
	}
}

type callFinder func(fset *token.FileSet, f *ast.File) []string

func productionScanRoots() []string {
	return []string{"cmd", "internal", "pkg"}
}

func walkProductionGoFiles(root string, fn func(rel, abs string, src []byte) error) error {
	for _, top := range productionScanRoots() {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if name == "vendor" || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return fn(rel, path, src)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func scanProductionCalls(t *testing.T, root string, dirs []string, find callFinder) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "testdata" || info.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fset, f, err := parseGoSource(path, string(src))
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for _, hit := range find(fset, f) {
				parts := strings.SplitN(hit, ": ", 2)
				sym := hit
				if len(parts) == 2 {
					sym = parts[1]
				}
				counts[filepath.ToSlash(rel)+":"+sym]++
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func filterSitesOutside(sites map[string]int, prefix string) map[string]int {
	out := make(map[string]int)
	for k, v := range sites {
		if strings.HasPrefix(k, prefix) {
			continue
		}
		out[k] = v
	}
	return out
}

func assertAllowedCallSites(t *testing.T, label string, got map[string]int, allowed map[string]int) {
	t.Helper()
	var bad []string
	for site, n := range got {
		want, ok := allowed[site]
		if !ok {
			bad = append(bad, "unexpected "+label+" site "+site)
			continue
		}
		if n != want {
			bad = append(bad, "site "+site+" count="+itoa(n)+" want="+itoa(want))
		}
	}
	for site := range allowed {
		if _, ok := got[site]; !ok {
			bad = append(bad, "missing required "+label+" site "+site)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("%s uniqueness gate failed:\n%s\ngot=%v", label, strings.Join(bad, "\n"), got)
	}
}

func normalizeMutationKey(rel, violation string) string {
	parts := strings.SplitN(violation, ": ", 2)
	sym := violation
	if len(parts) == 2 {
		sym = parts[1]
	}
	return filepath.ToSlash(rel) + ":" + sym
}

func coreFilesystemImports(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	base := filepath.Join(root, "internal", "core")
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, f, err := parseGoSource(path, string(src))
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			switch p {
			case "os", "path/filepath", "io/fs", "embed":
				out[rel+":"+p] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
