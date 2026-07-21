package runtimehost_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

type fakePlane struct {
	closed  atomic.Bool
	kinds   map[string]int
	closeCh chan struct{}
	handler http.Handler
}

func newFakePlane(kinds map[string]int) *fakePlane {
	return &fakePlane{kinds: kinds, handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
}

func (p *fakePlane) Handler() http.Handler         { return p.handler }
func (p *fakePlane) Quiesce(context.Context) error { return nil }
func (p *fakePlane) Close() error {
	if p.closed.Swap(true) {
		return runtimehost.ErrAlreadyClosed
	}
	if p.closeCh != nil {
		close(p.closeCh)
	}
	return nil
}
func (p *fakePlane) BackendFactoryKindCounts() map[string]int {
	if p == nil || p.kinds == nil {
		return nil
	}
	out := make(map[string]int, len(p.kinds))
	for k, v := range p.kinds {
		out[k] = v
	}
	return out
}

type stageGate struct {
	mu      sync.Mutex
	enter   chan struct{}
	release chan struct{}
}

func newStageGate() *stageGate {
	return &stageGate{enter: make(chan struct{}), release: make(chan struct{})}
}

func (g *stageGate) WaitEnter(t *testing.T) {
	t.Helper()
	select {
	case <-g.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stage enter")
	}
}

func (g *stageGate) Release() { close(g.release) }

func (g *stageGate) Hold(ctx context.Context) error {
	select {
	case <-g.enter:
	default:
		close(g.enter)
	}
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type fakeSource struct {
	path   string
	snap   configsource.SourceSnapshot
	atomic configsource.AtomicResult
	err    error
	gate   *stageGate
	calls  atomic.Int64
}

func (s *fakeSource) AbsolutePath() string { return s.path }
func (s *fakeSource) ReadStable(ctx context.Context, _ *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error) {
	s.calls.Add(1)
	if s.gate != nil {
		if err := s.gate.Hold(ctx); err != nil {
			return configsource.SourceSnapshot{}, "", err
		}
	}
	return s.snap, s.atomic, s.err
}

type controllableCompiler struct {
	gate     *stageGate
	err      error
	panicMsg string
	kinds    map[string]int
	onPlane  func(*fakePlane)
	calls    atomic.Int64
	liveSeen atomic.Value // map[string]int
}

func (c *controllableCompiler) Compile(ctx context.Context, _ *config.Config, live map[string]int) (runtimehost.PublishedRequestPlane, error) {
	c.calls.Add(1)
	if live != nil {
		cp := make(map[string]int, len(live))
		for k, v := range live {
			cp[k] = v
		}
		c.liveSeen.Store(cp)
	}
	if c.panicMsg != "" {
		panic(c.panicMsg)
	}
	if c.gate != nil {
		if err := c.gate.Hold(ctx); err != nil {
			return nil, err
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	p := newFakePlane(c.kinds)
	if c.onPlane != nil {
		c.onPlane(p)
	}
	return p, nil
}

func baseEffective(fp string, digest byte) *config.EffectiveConfig {
	var d [32]byte
	d[0] = digest
	return &config.EffectiveConfig{
		Config: &config.Config{},
		Identity: config.EffectiveIdentity{
			PrivateDigest:     d,
			PublicFingerprint: fp,
		},
		LoadedAt: time.Now().UTC(),
	}
}

func newTestCoordinator(t *testing.T, mgr *runtimehost.Manager, src *fakeSource, loader runtimehost.EffectiveLoader, compile runtimehost.CandidateCompiler, active *config.EffectiveConfig) *runtimehost.Coordinator {
	t.Helper()
	if mgr == nil {
		mgr = runtimehost.NewManager(8, nil)
		g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1}))
		g0.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-startup", TriggerKind: "startup"})
		if err := mgr.Publish(g0); err != nil {
			t.Fatalf("publish gen1: %v", err)
		}
	}
	if src == nil {
		src = &fakeSource{path: "/fixed/startup/config.yaml", snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}, atomic: configsource.AtomicEligible}
	}
	if loader == nil {
		loader = runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return baseEffective("fp-new", 2), nil
		})
	}
	if compile == nil {
		compile = &controllableCompiler{kinds: map[string]int{"local-stub": 1}}
	}
	if active == nil {
		active = baseEffective("fp-startup", 1)
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Compile:         compile,
		Manager:         mgr,
		Timeout:         time.Second,
		ActiveEffective: active,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

func TestCoordinator_BusyRejectsAPIWhileActive(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
		atomic: configsource.AtomicEligible,
		gate:   gate,
	}
	c := newTestCoordinator(t, nil, src, nil, nil, nil)

	done := make(chan configreload.ReloadResult, 1)
	go func() {
		done <- c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	}()
	gate.WaitEnter(t)

	busy := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if busy.Category != configreload.ResultBusy {
		t.Fatalf("busy category=%q", busy.Category)
	}
	if st := c.Status(); !st.Busy || st.ActiveGeneration != 1 {
		t.Fatalf("status=%+v", st)
	}

	gate.Release()
	res := <-done
	if res.Category != configreload.ResultPublished {
		t.Fatalf("first result=%q", res.Category)
	}
	if c.Status().Busy {
		t.Fatal("expected not busy after completion")
	}
}

func TestCoordinator_BusyAndCoalesceNeverExceedOneActiveCompile(t *testing.T) {
	t.Parallel()
	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64
	gate := newStageGate()
	compile := &controllableCompiler{
		gate:  gate,
		kinds: map[string]int{"local-stub": 1},
	}
	// Wrap compile to count concurrency around the gated Hold.
	base := compile
	wrapped := runtimehost.FuncCompiler(func(ctx context.Context, cfg *config.Config, live map[string]int) (runtimehost.PublishedRequestPlane, error) {
		n := concurrent.Add(1)
		for {
			cur := maxConcurrent.Load()
			if n <= cur || maxConcurrent.CompareAndSwap(cur, n) {
				break
			}
		}
		defer concurrent.Add(-1)
		return base.Compile(ctx, cfg, live)
	})
	digest := byte(10)
	loaderN := atomic.Int64{}
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := loaderN.Add(1)
		return baseEffective("fp-c"+string(rune('a'+n)), digest+byte(n)), nil
	})
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, loader, wrapped, baseEffective("fp-old", 1))

	done := make(chan configreload.ReloadResult, 1)
	go func() {
		done <- c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	}()
	gate.WaitEnter(t)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kind := configreload.TriggerAPI
			if i%2 == 0 {
				kind = configreload.TriggerSIGHUP
			}
			_ = c.Reload(context.Background(), configreload.ReloadTrigger{Kind: kind})
		}(i)
	}
	time.Sleep(20 * time.Millisecond) // let hammer land while first is in compile
	gate.Release()
	<-done
	wg.Wait()

	// Drain any coalesced follow-up that may still be finishing.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && c.Status().Busy {
		time.Sleep(5 * time.Millisecond)
	}
	if maxConcurrent.Load() > 1 {
		t.Fatalf("max concurrent compiles=%d want <=1", maxConcurrent.Load())
	}
}

func TestCoordinator_NoopLeavesActiveGenerationUnchanged(t *testing.T) {
	t.Parallel()
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicNoop,
		snap:   configsource.SourceSnapshot{Bytes: []byte("same")},
	}
	c := newTestCoordinator(t, nil, src, nil, nil, nil)
	before := c.Status().ActiveGeneration
	res := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if res.Category != configreload.ResultNoop {
		t.Fatalf("category=%q", res.Category)
	}
	if res.ActiveGeneration != before || c.Status().ActiveGeneration != before {
		t.Fatalf("active changed: res=%d status=%d before=%d", res.ActiveGeneration, c.Status().ActiveGeneration, before)
	}
}

func TestCoordinator_NoopOnMatchingEffectiveFingerprint(t *testing.T) {
	t.Parallel()
	active := baseEffective("fp-same", 7)
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("reordered")},
	}
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		return baseEffective("fp-same", 7), nil
	})
	compile := &controllableCompiler{kinds: nil}
	c := newTestCoordinator(t, nil, src, loader, compile, active)
	res := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if res.Category != configreload.ResultNoop {
		t.Fatalf("category=%q", res.Category)
	}
	if compile.calls.Load() != 0 {
		t.Fatalf("compile called on noop: %d", compile.calls.Load())
	}
}

func TestCoordinator_FaultMatrixPrePublication(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		src      *fakeSource
		loader   runtimehost.EffectiveLoader
		classify func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
		compile  *controllableCompiler
		want     configreload.ResultCategory
	}{
		{
			name: "read_source_integrity",
			src: &fakeSource{
				path: "/fixed/startup/config.yaml",
				err:  &configsource.IntegrityError{Category: configsource.CategoryMissing},
			},
			want: configreload.ResultSourceIntegrity,
		},
		{
			name: "load_invalid",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
				return nil, &config.LoadError{Category: config.CategoryMalformedYAML}
			}),
			want: configreload.ResultInvalid,
		},
		{
			name: "classify_restart_required",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			classify: func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error) {
				return nil, &configreload.RestartRequiredError{
					RestartRequiredFields: []string{"server.address"},
					TotalBlocked:          1,
				}
			},
			want: configreload.ResultRestartRequired,
		},
		{
			name: "compile_preparation_failed",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			compile: &controllableCompiler{err: errors.New("factory boom")},
			want:    configreload.ResultPreparationFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr := runtimehost.NewManager(8, nil)
			g0 := mgr.PrepareRequestPlane("startup", newFakePlane(nil))
			if err := mgr.Publish(g0); err != nil {
				t.Fatal(err)
			}
			loader := tc.loader
			if loader == nil {
				loader = runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
					return baseEffective("fp-cand", 9), nil
				})
			}
			compile := tc.compile
			if compile == nil {
				compile = &controllableCompiler{}
			}
			deps := runtimehost.CoordinatorDeps{
				Source:          tc.src,
				Loader:          loader,
				Classify:        tc.classify,
				Compile:         compile,
				Manager:         mgr,
				Timeout:         time.Second,
				ActiveEffective: baseEffective("fp-old", 1),
			}
			c, err := runtimehost.NewCoordinator(deps)
			if err != nil {
				t.Fatal(err)
			}
			res := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
			if res.Category != tc.want {
				t.Fatalf("category=%q want %q", res.Category, tc.want)
			}
			if mgr.Active().ID() != 1 {
				t.Fatalf("active generation changed to %d", mgr.Active().ID())
			}
		})
	}
}

func TestCoordinator_ShutdownCancelsAndPreventsLatePublication(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	compile := &controllableCompiler{gate: gate}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, nil, compile, nil)

	done := make(chan configreload.ReloadResult, 1)
	go func() {
		done <- c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	c.BeginShutdown()
	gate.Release()
	res := <-done
	if res.Category != configreload.ResultCanceled {
		t.Fatalf("category=%q want canceled", res.Category)
	}
	if c.Status().ActiveGeneration != 1 {
		t.Fatalf("active=%d", c.Status().ActiveGeneration)
	}

	late := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if late.Category != configreload.ResultCanceled {
		t.Fatalf("late category=%q", late.Category)
	}
}

func TestCoordinator_CoalesceAtMostOnePendingSignal(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	compile := &controllableCompiler{gate: gate, kinds: map[string]int{"local-stub": 1}}
	digest := byte(2)
	loaderCalls := atomic.Int64{}
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := loaderCalls.Add(1)
		return baseEffective("fp-"+string(rune('a'+n)), digest+byte(n)), nil
	})
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, loader, compile, baseEffective("fp-old", 1))

	done := make(chan configreload.ReloadResult, 1)
	go func() {
		done <- c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	}()
	gate.WaitEnter(t)

	b1 := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerSIGHUP})
	b2 := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerSIGHUP})
	b3 := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerSIGHUP})
	if b1.Category != configreload.ResultBusy || b2.Category != configreload.ResultBusy || b3.Category != configreload.ResultBusy {
		t.Fatalf("expected busy coalesce responses: %q %q %q", b1.Category, b2.Category, b3.Category)
	}
	st := c.Status()
	if !st.PendingSignal {
		t.Fatal("expected pending signal")
	}
	if st.CoalescedSignals < 1 {
		t.Fatalf("expected coalesced extras, got %d", st.CoalescedSignals)
	}

	// Unblock first attempt; follow-up coalesced attempt should run once more.
	gate.Release()
	first := <-done
	if first.Category != configreload.ResultPublished {
		t.Fatalf("first=%q", first.Category)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if compile.calls.Load() >= 2 && !c.Status().Busy && !c.Status().PendingSignal {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if compile.calls.Load() != 2 {
		t.Fatalf("compile calls=%d want 2 (one active + one coalesced)", compile.calls.Load())
	}
	if c.Status().PendingSignal {
		t.Fatal("pending signal should be drained")
	}
	if c.Status().ActiveGeneration != 3 {
		t.Fatalf("active=%d want 3", c.Status().ActiveGeneration)
	}
}

func TestCoordinator_RetentionRejectionRollsBack(t *testing.T) {
	t.Parallel()
	mgr := runtimehost.NewManager(0, nil) // max retained 0 → second publish blocked when active exists
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	var closedPlane atomic.Pointer[fakePlane]
	compile := &controllableCompiler{
		kinds: nil,
		onPlane: func(p *fakePlane) {
			p.closeCh = make(chan struct{})
			closedPlane.Store(p)
		},
	}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, mgr, src, nil, compile, baseEffective("fp-old", 1))
	res := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if res.Category != configreload.ResultRetentionBlocked {
		t.Fatalf("category=%q", res.Category)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("active=%d", mgr.Active().ID())
	}
	p := closedPlane.Load()
	if p == nil {
		t.Fatal("expected compiled plane")
	}
	select {
	case <-p.closeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("candidate plane not discarded on retention rejection")
	}
}

func TestCoordinator_PanicIsolationDoesNotMutateActive(t *testing.T) {
	t.Parallel()
	compile := &controllableCompiler{panicMsg: "boom-in-compile"}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, nil, compile, nil)
	res := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if res.Category != configreload.ResultInternalFailed {
		t.Fatalf("category=%q", res.Category)
	}
	if res.ReasonCategory != configreload.StagePanic {
		t.Fatalf("reason=%q", res.ReasonCategory)
	}
	if c.Status().ActiveGeneration != 1 {
		t.Fatalf("active=%d", c.Status().ActiveGeneration)
	}
}

func TestCoordinator_LiveFactoryKindsFromActiveAndRetained(t *testing.T) {
	t.Parallel()
	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1, "shared": 1}))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	// Occupy retention with another generation so LiveFactoryKinds sums both.
	g1plane := newFakePlane(map[string]int{"local-stub": 1})
	g1 := mgr.PrepareRequestPlane("prev", g1plane)
	if err := mgr.Publish(g1); err != nil {
		t.Fatal(err)
	}
	compile := &controllableCompiler{kinds: map[string]int{"local-stub": 1}}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, mgr, src, nil, compile, baseEffective("fp-old", 1))
	res := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if res.Category != configreload.ResultPublished {
		t.Fatalf("category=%q", res.Category)
	}
	live, _ := compile.liveSeen.Load().(map[string]int)
	if live["local-stub"] < 2 {
		t.Fatalf("liveFactoryKinds=%v want local-stub>=2 from active+retained", live)
	}
	if live["shared"] < 1 {
		t.Fatalf("liveFactoryKinds=%v missing shared from retained", live)
	}
}

func TestCoordinator_HostTimeoutIndependentOfClientCancel(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	compile := &controllableCompiler{gate: gate}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, nil, compile, nil)

	clientCtx, cancel := context.WithCancel(context.Background())
	done := make(chan configreload.ReloadResult, 1)
	go func() {
		done <- c.Reload(clientCtx, configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	cancel() // client disconnect must not cancel host-owned attempt
	gate.Release()
	res := <-done
	if res.Category != configreload.ResultPublished {
		t.Fatalf("category=%q after client cancel", res.Category)
	}
}
