package runtimehost_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
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
	maps.Copy(out, p.kinds)
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
		maps.Copy(cp, live)
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
	for i := range 32 {
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

// Effective no-op after an atomic path replace must still advance the coordinator's
// active source identity. Otherwise a later in-place rewrite of the new inode is
// compared against the pre-rename handle and incorrectly classified as AtomicEligible
// (req 2.9 / source_non_atomic_update).
func TestCoordinator_EffectiveNoopAdvancesActiveSource_RejectsInPlaceEdit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	bodyA := []byte("server:\n  address: \"127.0.0.1:0\"\n")
	if err := os.WriteFile(path, bodyA, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snap1, res, err := src.ReadStable(ctx, nil)
	if err != nil || res != configsource.AtomicEligible {
		t.Fatalf("startup read: res=%q err=%v", res, err)
	}
	activeSrc := &configsource.ActiveSourceVersion{
		HandleIdentity: snap1.HandleIdentity,
		PrivateDigest:  snap1.PrivateDigest,
	}
	activeEff := baseEffective("fp-same", 7)
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		return baseEffective("fp-same", 7), nil
	})
	compile := &controllableCompiler{kinds: nil}
	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1}))
	g0.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-startup", TriggerKind: "startup"})
	if err := mgr.Publish(g0); err != nil {
		t.Fatalf("publish gen1: %v", err)
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Compile:         compile,
		Manager:         mgr,
		Timeout:         time.Second,
		ActiveEffective: activeEff,
		ActiveSource:    activeSrc,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	bodyB := []byte("server:\n  address: \"127.0.0.1:0\"\n# reorder-noop\n")
	tmp := filepath.Join(dir, "config.yaml.tmp")
	if err := os.WriteFile(tmp, bodyB, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	noop := c.Reload(ctx, configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if noop.Category != configreload.ResultNoop {
		t.Fatalf("effective noop category=%q", noop.Category)
	}
	if compile.calls.Load() != 0 {
		t.Fatalf("compile called on effective noop: %d", compile.calls.Load())
	}

	bodyC := []byte("server:\n  address: \"127.0.0.1:9\"\n")
	if err := os.WriteFile(path, bodyC, 0o600); err != nil {
		t.Fatal(err)
	}
	rejected := c.Reload(ctx, configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if rejected.Category != configreload.ResultSourceIntegrity {
		t.Fatalf("in-place rewrite after effective noop: category=%q want %q", rejected.Category, configreload.ResultSourceIntegrity)
	}
	if rejected.ReasonCategory != configreload.StageRead {
		t.Fatalf("reason=%q want %q", rejected.ReasonCategory, configreload.StageRead)
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

// Phase 6.2 certification: last-good retention across the pre-publication fault
// matrix (source empty/partial, decode/validate/classify/compile/retention).
func TestCoordinator_LastGoodFaultMatrix_SourceDecodeClassifyCompileRetention(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		src      *fakeSource
		loader   runtimehost.EffectiveLoader
		classify func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
		compile  *controllableCompiler
		mgrMax   int
		want     configreload.ResultCategory
	}{
		{
			name: "source_empty",
			src: &fakeSource{
				path: "/fixed/startup/config.yaml",
				err:  &configsource.IntegrityError{Category: configsource.CategoryEmpty},
			},
			want: configreload.ResultSourceIntegrity,
		},
		{
			name: "source_partial_unreadable",
			src: &fakeSource{
				path: "/fixed/startup/config.yaml",
				err:  &configsource.IntegrityError{Category: configsource.CategoryPartialUnreadable},
			},
			want: configreload.ResultSourceIntegrity,
		},
		{
			name: "source_non_atomic_inplace",
			src: &fakeSource{
				path: "/fixed/startup/config.yaml",
				err:  &configsource.IntegrityError{Category: configsource.CategoryNonAtomicUpdate},
			},
			want: configreload.ResultSourceIntegrity,
		},
		{
			name: "decode_malformed_yaml",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("server: [")},
			},
			loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
				return nil, &config.LoadError{Category: config.CategoryMalformedYAML}
			}),
			want: configreload.ResultInvalid,
		},
		{
			name: "validate_unknown_core_field",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
				return nil, &config.LoadError{Category: config.CategoryUnknownCoreField}
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
					RestartRequiredFields: []string{"server.address", "plugins.backends"},
					TotalBlocked:          2,
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
		{
			name: "retention_blocked",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			compile: &controllableCompiler{},
			mgrMax:  0,
			want:    configreload.ResultRetentionBlocked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			maxRetained := 8
			if tc.mgrMax != 0 || tc.name == "retention_blocked" {
				maxRetained = tc.mgrMax
			}
			mgr := runtimehost.NewManager(maxRetained, nil)
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
				t.Fatalf("last-good active mutated to %d", mgr.Active().ID())
			}
		})
	}
}

func TestCoordinator_LastGood_AtomicRenameThenPublish(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	v1 := []byte("version: one\n")
	if err := os.WriteFile(path, v1, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snap1, res1, err := src.ReadStable(context.Background(), nil)
	if err != nil || res1 != configsource.AtomicEligible {
		t.Fatalf("bootstrap read: res=%q err=%v", res1, err)
	}

	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1}))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	compile := &controllableCompiler{kinds: map[string]int{"local-stub": 1}}
	var loadN atomic.Int64
	loader := runtimehost.FuncEffectiveLoader(func(_ context.Context, raw []byte) (*config.EffectiveConfig, error) {
		n := loadN.Add(1)
		return baseEffective("fp-"+string(raw), byte(n+1)), nil
	})
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:  src,
		Loader:  loader,
		Compile: compile,
		Manager: mgr,
		Timeout: time.Second,
		ActiveEffective: &config.EffectiveConfig{
			Config: &config.Config{},
			Identity: config.EffectiveIdentity{
				PrivateDigest:     snap1.PrivateDigest,
				PublicFingerprint: "fp-startup",
			},
		},
		ActiveSource: &configsource.ActiveSourceVersion{
			HandleIdentity: snap1.HandleIdentity,
			PrivateDigest:  snap1.PrivateDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// In-place rewrite must retain last-good.
	if err := os.WriteFile(path, []byte("version: torn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inplace := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if inplace.Category != configreload.ResultSourceIntegrity {
		t.Fatalf("inplace category=%q", inplace.Category)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("inplace mutated active=%d", mgr.Active().ID())
	}

	// Atomic rename replacement is eligible and publishes.
	tmp := filepath.Join(dir, "config.yaml.tmp")
	if err := os.WriteFile(tmp, []byte("version: two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	pub := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if pub.Category != configreload.ResultPublished {
		t.Fatalf("atomic rename category=%q reason=%q", pub.Category, pub.ReasonCategory)
	}
	if mgr.Active().ID() != 2 {
		t.Fatalf("active=%d want 2", mgr.Active().ID())
	}
}

func TestCoordinator_RestartRequired_MixedNoPartialApply_RequiresRetrigger(t *testing.T) {
	t.Parallel()
	activeCfg := &config.Config{
		Access:  config.AccessConfig{Mode: "single_user"},
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "stub:m"},
	}
	mixedCfg := &config.Config{
		Access:  config.AccessConfig{Mode: "multi_user"}, // startup-only
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 5, DefaultRoute: "stub:m"}, // reloadable
	}
	fixedCfg := &config.Config{
		Access:  config.AccessConfig{Mode: "single_user"},
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 5, DefaultRoute: "stub:m"},
	}

	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1}))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	compile := &controllableCompiler{kinds: map[string]int{"local-stub": 1}}
	var candidate atomic.Pointer[config.Config]
	candidate.Store(mixedCfg)
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		cfg := candidate.Load()
		var d [32]byte
		d[0] = byte(cfg.Routing.MaxAttempts)
		if cfg.Access.Mode == "multi_user" {
			d[1] = 9
		}
		return &config.EffectiveConfig{
			Config: cfg,
			Identity: config.EffectiveIdentity{
				PrivateDigest:     d,
				PublicFingerprint: cfg.Access.Mode + "-" + string(rune('0'+cfg.Routing.MaxAttempts)),
			},
		}, nil
	})
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("candidate")},
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Classify:        configreload.ClassifyEffective,
		Compile:         compile,
		Manager:         mgr,
		Timeout:         time.Second,
		ActiveEffective: &config.EffectiveConfig{Config: activeCfg, Identity: config.EffectiveIdentity{PublicFingerprint: "active", PrivateDigest: [32]byte{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	blocked := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if blocked.Category != configreload.ResultRestartRequired {
		t.Fatalf("mixed category=%q want restart_required", blocked.Category)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("partial apply leaked: active=%d", mgr.Active().ID())
	}
	if compile.calls.Load() != 0 {
		t.Fatalf("compile must not run on restart-required, calls=%d", compile.calls.Load())
	}

	// Correction alone does nothing until an explicit retrigger.
	candidate.Store(fixedCfg)
	if mgr.Active().ID() != 1 {
		t.Fatal("active mutated without retrigger")
	}
	pub := c.Reload(context.Background(), configreload.ReloadTrigger{Kind: configreload.TriggerAPI})
	if pub.Category != configreload.ResultPublished {
		t.Fatalf("retrigger after correction category=%q reason=%q", pub.Category, pub.ReasonCategory)
	}
	if mgr.Active().ID() != 2 {
		t.Fatalf("active=%d want 2 after explicit retrigger", mgr.Active().ID())
	}
	if compile.calls.Load() != 1 {
		t.Fatalf("compile calls=%d want 1", compile.calls.Load())
	}
}
