package runtimebundle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func TestCompileGeneration_NilHandlerRollsBackCandidate(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "nilh", "x", "nilh:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: func(context.Context, runtimebundle.RequestPlane) (http.Handler, error) {
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected nil handler rejection")
	}
	if !strings.Contains(err.Error(), "nil handler") {
		t.Fatalf("err=%v", err)
	}
	if ps.Closed() {
		t.Fatal("process closed on nil handler")
	}
	ok, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "nilh2", "y", "nilh2:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		t.Fatalf("recompile after nil-handler rollback: %v", err)
	}
	t.Cleanup(func() { _ = ok.Close() })
}

func TestCompileGeneration_DeepFreezeRegistrationsAndYAML(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	cand := stubCandidateConfig(t, "freeze", "freeze-text", "freeze:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	origBackendNode := cand.Plugins.Backends[0].Config

	var planeRegs []lipsdk.Registration
	var stackCfg *config.Config
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, plane runtimebundle.RequestPlane) (http.Handler, error) {
			planeRegs = plane.Registrations()
			stackCfg = plane.StackConfig()
			if stackCfg == nil {
				t.Fatal("StackConfig returned nil")
			}
			return stdhttp.ComposeRequestPlane(ctx, plane)
		},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	cand.Routing.DefaultRoute = "mutated:gone"
	cand.Plugins.Backends[0].Config = genYAMLNode(t, `text: "mutated-backend"`)
	if len(origBackendNode.Content) > 0 {
		origBackendNode.Content[0].Value = "poison"
	}
	if len(planeRegs) > 0 {
		planeRegs[0].ID = "mutated-reg"
		planeRegs[0].Config.Node = genYAMLNode(t, `{"poison":true}`)
	}
	returned := bundle.Registrations()
	if len(returned) == 0 {
		t.Fatal("expected registrations")
	}
	returned[0].ID = "mutated-returned"
	returned[0].Config.Node = genYAMLNode(t, `{"poison":"returned"}`)
	frontends := bundle.FrozenFrontends()
	frontends[0].ID = "mutated-fe"
	frontends[0].Config = genYAMLNode(t, `{"label":"mutated"}`)
	if stackCfg != nil {
		stackCfg.Routing.DefaultRoute = "stack-mutated"
		if len(stackCfg.Plugins.Frontends) > 0 {
			stackCfg.Plugins.Frontends[0].ID = "stack-fe"
		}
	}

	body := postResponses(t, bundle.Handler(), "stub-default")
	if !strings.Contains(body, "freeze-text") {
		t.Fatalf("handler followed mutated YAML: %s", body)
	}
	if bundle.Routing().DefaultRoute != "freeze:stub-default" {
		t.Fatalf("routing mutated: %q", bundle.Routing().DefaultRoute)
	}
	if got := bundle.FrozenFrontends()[0].ID; got != "openai-responses" {
		t.Fatalf("frontend id mutated: %q", got)
	}
	if got := bundle.Registrations()[0].ID; strings.Contains(got, "mutated") {
		t.Fatalf("registration id mutated: %q", got)
	}
	rr := httptest.NewRecorder()
	bundle.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz after mutation: %d", rr.Code)
	}
}

func TestCompileGeneration_FeatureSurfaceNoStartupLeakOrDuplicate(t *testing.T) {
	t.Parallel()
	startupLife := &overlapLife{}
	candLife := &overlapLife{}
	var startupObs, prodObs, candObs atomic.Int32
	var startupHook, candHook atomic.Int32

	cfg := processBaseConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry:    stdFactoryCatalog(t),
			FeatureLifecycles: []lipplugin.Lifecycle{startupLife},
			Extensions: runtimebundle.ExtensionsOptions{
				TrafficObservers: []traffic.Observer{countTrafficObs{n: &startupObs}},
				RequestTransforms: []request.Transform{countTransform{
					id: "startup-hook",
					n:  &startupHook,
				}},
			},
			Production: runtimebundle.ProductionOptions{
				TrafficObservers: []traffic.Observer{countTrafficObs{n: &prodObs}},
			},
		},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	probeTraffic := func(plane runtimebundle.RequestPlane) {
		t.Helper()
		snap := plane.RuntimeSnapshot()
		if snap == nil || snap.TrafficObserver() == nil {
			t.Fatal("expected traffic observer")
		}
		startupObs.Store(0)
		prodObs.Store(0)
		candObs.Store(0)
		_ = snap.TrafficObserver().OnObservation(context.Background(), traffic.Observation{Leg: traffic.LegCTP})
		if got := startupObs.Load(); got != 0 {
			t.Fatalf("startup observer leaked calls=%d", got)
		}
		if got := prodObs.Load(); got != 1 {
			t.Fatalf("production observer calls=%d want 1", got)
		}
		if got := candObs.Load(); got != 1 {
			t.Fatalf("candidate observer calls=%d want 1", got)
		}
		startupHook.Store(0)
		candHook.Store(0)
		for _, tr := range snap.RequestTransforms() {
			_ = tr.Handle(context.Background(), &lipapi.Call{}, request.RequestMeta{}, request.Services{})
		}
		if got := startupHook.Load(); got != 0 {
			t.Fatalf("startup request transform leaked calls=%d", got)
		}
		if got := candHook.Load(); got != 1 {
			t.Fatalf("candidate request transform calls=%d want 1", got)
		}
	}

	a, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "feat-a", "a", "feat-a:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []lipplugin.Lifecycle{candLife},
			Extensions: runtimebundle.ExtensionsOptions{
				TrafficObservers:  []traffic.Observer{countTrafficObs{n: &candObs}},
				RequestTransforms: []request.Transform{countTransform{id: "cand-hook", n: &candHook}},
			},
		},
		Compose: func(ctx context.Context, plane runtimebundle.RequestPlane) (http.Handler, error) {
			probeTraffic(plane)
			return stdhttp.ComposeRequestPlane(ctx, plane)
		},
	})
	if err != nil {
		t.Fatalf("compile A: %v", err)
	}
	if startupLife.starts.Load() != 0 {
		t.Fatalf("startup lifecycle started=%d", startupLife.starts.Load())
	}
	if candLife.starts.Load() != 1 {
		t.Fatalf("candidate lifecycle starts=%d", candLife.starts.Load())
	}

	b, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "feat-b", "b", "feat-b:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		// Intentionally empty candidate surface: must not reuse startup lifecycles/hooks.
		Compose: func(ctx context.Context, plane runtimebundle.RequestPlane) (http.Handler, error) {
			snap := plane.RuntimeSnapshot()
			startupObs.Store(0)
			prodObs.Store(0)
			candObs.Store(0)
			_ = snap.TrafficObserver().OnObservation(context.Background(), traffic.Observation{Leg: traffic.LegCTP})
			if startupObs.Load() != 0 {
				t.Fatal("empty candidate retained startup traffic observer")
			}
			if prodObs.Load() != 1 {
				t.Fatalf("production observer calls=%d", prodObs.Load())
			}
			if candObs.Load() != 0 {
				t.Fatal("empty candidate retained prior candidate observer")
			}
			if len(snap.RequestTransforms()) != 0 {
				t.Fatalf("empty candidate retained transforms=%d", len(snap.RequestTransforms()))
			}
			return stdhttp.ComposeRequestPlane(ctx, plane)
		},
	})
	if err != nil {
		t.Fatalf("compile B: %v", err)
	}
	if startupLife.starts.Load() != 0 {
		t.Fatalf("startup lifecycle started on empty candidate=%d", startupLife.starts.Load())
	}
	if candLife.starts.Load() != 1 {
		t.Fatalf("candidate A lifecycle starts mutated=%d", candLife.starts.Load())
	}
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if candLife.stops.Load() != 1 {
		t.Fatalf("candidate stops=%d", candLife.stops.Load())
	}
	if startupLife.stops.Load() != 0 {
		t.Fatalf("startup lifecycle stopped=%d", startupLife.stops.Load())
	}
}

func TestCompileGeneration_BundleQuiesceBeforeClose_LifecycleStopOnce(t *testing.T) {
	t.Parallel()
	life := &overlapLife{}
	ps := newProcessForGeneration(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "retire", "retire-text", "retire:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{FeatureLifecycles: []lipplugin.Lifecycle{life}},
		Compose:       stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	if life.starts.Load() != 1 {
		t.Fatalf("starts=%d", life.starts.Load())
	}
	before := bundle.ResourceCount()
	if before == 0 {
		t.Fatal("expected resources")
	}
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	afterQuiesce := bundle.ResourceCount()
	if afterQuiesce > before {
		t.Fatalf("quiesce increased resources %d -> %d", before, afterQuiesce)
	}
	if life.stops.Load() != 0 {
		t.Fatal("lifecycle Stop must not run during Quiesce")
	}

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareRequestPlane("retire", bundle)
	mustPublishGen(t, m, g)
	mustPublishGen(t, m, m.Prepare("next"))

	worker := runtimehost.NewLifecycleWorker()
	if err := worker.Retire(context.Background(), g, bundle); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("lifecycle stops=%d want 1", life.stops.Load())
	}
	if g.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v", g.Lifecycle())
	}
	if err := worker.Retire(context.Background(), g, bundle); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second retire: %v", err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("doubled stops=%d", life.stops.Load())
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("bundle close doubled stops=%d", life.stops.Load())
	}
}

func TestCompileGeneration_RequestPlaneBoundOnPublishAcquire(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "bind", "bind-text", "bind:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		t.Fatal(err)
	}

	m := runtimehost.NewManager(2, nil)
	g := m.BeginPrepareRequestPlane("bind", bundle)
	if g.RequestPlane() != bundle {
		t.Fatal("begin-prepare must bind request plane")
	}
	if err := g.AttachRequestPlane(bundle); !errors.Is(err, runtimehost.ErrRequestPlaneAlreadyBound) {
		t.Fatalf("rebind: %v", err)
	}
	if err := g.MarkPrepared(); err != nil {
		t.Fatal(err)
	}
	mustPublishGen(t, m, g)
	if err := g.AttachRequestPlane(bundle); !errors.Is(err, runtimehost.ErrIllegalTransition) {
		t.Fatalf("post-publish rebind: %v", err)
	}

	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	defer lease.Release()
	if lease.RequestPlane() != bundle {
		t.Fatal("lease must identify exact request-plane bundle")
	}
	if lease.Handler() == nil {
		t.Fatal("lease handler is nil")
	}
	body := postResponses(t, lease.Handler(), "stub-default")
	if !strings.Contains(body, "bind-text") {
		t.Fatalf("body=%s", body)
	}

	// Retention rejection closes attached bundle exactly once.
	m2 := runtimehost.NewManager(1, nil)
	g1 := m2.Prepare("keep")
	mustPublishGen(t, m2, g1)
	lease2, ok := m2.Acquire()
	if !ok {
		t.Fatal("acquire keep")
	}
	pin, ok := lease2.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("pin")
	}
	mustPublishGen(t, m2, m2.Prepare("fill"))
	blocked, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "blocked", "z", "blocked:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		t.Fatal(err)
	}
	cand := m2.PrepareRequestPlane("blocked", blocked)
	pubErr := m2.Publish(cand)
	if !errors.Is(pubErr, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("want retention blocked, got %v", pubErr)
	}
	if cand.CloseCount() != 1 {
		t.Fatalf("close count=%d", cand.CloseCount())
	}
	if err := blocked.Close(); err != nil {
		t.Fatal(err)
	}
	pin.Release()
	_ = bundle.Close()
}

func TestCompileCandidate_DoesNotPruneProcessPools(t *testing.T) {
	t.Parallel()
	cfg := processBaseConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: stdFactoryCatalog(t),
			Testing: runtimebundle.TestingOptions{
				PostgresPoolOpener: func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
					sqlDB, err := sql.Open("sqlite", ":memory:")
					if err != nil {
						return nil, err
					}
					opened, err := db.NewBunDB(sqlDB, db.DialectSQLite)
					if err != nil {
						return nil, err
					}
					return opened, nil
				},
			},
		},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	unclaimed, err := ps.DatabasePools.Open(context.Background(), "postgres://candidate-compile/unclaimed", db.PoolSettings{})
	if err != nil {
		t.Fatalf("open unclaimed: %v", err)
	}
	before := ps.DatabasePools.Len()
	if before < 1 {
		t.Fatal("expected unclaimed pool registered")
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("pool-%d", i)
			candCfg := stubCandidateConfig(t, id, "p", id+":stub-default", []config.PluginConfig{
				{ID: "openai-responses", Enabled: true},
			})
			c, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
				Process:   ps,
				Bus:       hooks.New(hooks.Config{}),
				Candidate: candCfg,
			})
			if err != nil {
				errCh <- err
				return
			}
			if err := c.Close(); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("compile: %v", err)
	}
	if ps.DatabasePools.Len() != before {
		t.Fatalf("pool len=%d want %d (candidate compile must not prune)", ps.DatabasePools.Len(), before)
	}
	if err := unclaimed.PingContext(context.Background()); err != nil {
		t.Fatalf("unclaimed pool closed by candidate compile: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}
	if !ps.Closed() {
		t.Fatal("process close must own pools")
	}
}

func mustPublishGen(t *testing.T, m *runtimehost.Manager, g *runtimehost.Generation) {
	t.Helper()
	if err := m.Publish(g); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

type countTrafficObs struct{ n *atomic.Int32 }

func (c countTrafficObs) OnObservation(context.Context, traffic.Observation) error {
	c.n.Add(1)
	return nil
}

type countTransform struct {
	id string
	n  *atomic.Int32
}

func (c countTransform) ID() string                        { return c.id }
func (c countTransform) Order() int                        { return 0 }
func (c countTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (c countTransform) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	c.n.Add(1)
	return nil
}
