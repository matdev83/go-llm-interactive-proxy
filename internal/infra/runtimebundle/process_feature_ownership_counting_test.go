package runtimebundle

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

type testBackgroundProcessRunner struct{}

func (testBackgroundProcessRunner) Execute(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
}

// ProcessFeatureSnapshot captures the unique pointers of all process-scoped feature resources.
// This is the ownership-counting test seam for Phase 1 and later wave migrations.
type ProcessFeatureSnapshot struct {
	KeepwarmPolicy         *keepwarm.PolicyStore
	KeepwarmRegistry       *keepwarm.ManagerRegistry
	TerminalDecisionPolicy *terminaldecisionpolicy.Store
	CompactionDetector     runtime.CompactionDetector
	BranchCoordinator      *compactioncontinuity.BranchCoordinator
	CompactionParentPort   *compactioncompose.CompactionContinuityParentPort
	BackgroundAux          *BackgroundAuxScheduler
}

// CaptureProcessFeatureSnapshot extracts the current process feature resources from ProcessServices.
func CaptureProcessFeatureSnapshot(ps *ProcessServices) ProcessFeatureSnapshot {
	if ps == nil {
		return ProcessFeatureSnapshot{}
	}
	return ProcessFeatureSnapshot{
		KeepwarmPolicy:         ps.KeepwarmPolicy,
		KeepwarmRegistry:       ps.KeepwarmRegistry,
		TerminalDecisionPolicy: ps.TerminalDecisionPolicy,
		CompactionDetector:     ps.CompactionDetector,
		BranchCoordinator:      ps.BranchCoordinator,
		CompactionParentPort:   ps.CompactionParentPort,
		BackgroundAux:          ps.BackgroundAux,
	}
}

// AssertAllPresent asserts that each enabled process-scoped feature resource is instantiated.
func (s ProcessFeatureSnapshot) AssertAllPresent(t *testing.T) {
	t.Helper()
	if s.KeepwarmPolicy == nil {
		t.Fatal("expected non-nil KeepwarmPolicy on ProcessServices")
	}
	if s.KeepwarmRegistry == nil {
		t.Fatal("expected non-nil KeepwarmRegistry on ProcessServices")
	}
	if s.TerminalDecisionPolicy == nil {
		t.Fatal("expected non-nil TerminalDecisionPolicy on ProcessServices")
	}
	if s.CompactionDetector == nil {
		t.Fatal("expected non-nil CompactionDetector on ProcessServices")
	}
	if s.BranchCoordinator == nil {
		t.Fatal("expected non-nil BranchCoordinator on ProcessServices")
	}
	if s.CompactionParentPort == nil {
		t.Fatal("expected non-nil CompactionParentPort on ProcessServices")
	}
	if s.BackgroundAux == nil {
		t.Fatal("expected non-nil BackgroundAux on ProcessServices")
	}
}

// AssertIdentical asserts that two snapshots refer to the exact same physical instances.
func (s ProcessFeatureSnapshot) AssertIdentical(t *testing.T, other ProcessFeatureSnapshot, stage string) {
	t.Helper()
	if s.KeepwarmPolicy != other.KeepwarmPolicy {
		t.Fatalf("%s: KeepwarmPolicy instance changed: %p vs %p", stage, s.KeepwarmPolicy, other.KeepwarmPolicy)
	}
	if s.KeepwarmRegistry != other.KeepwarmRegistry {
		t.Fatalf("%s: KeepwarmRegistry instance changed: %p vs %p", stage, s.KeepwarmRegistry, other.KeepwarmRegistry)
	}
	if s.TerminalDecisionPolicy != other.TerminalDecisionPolicy {
		t.Fatalf("%s: TerminalDecisionPolicy instance changed: %p vs %p", stage, s.TerminalDecisionPolicy, other.TerminalDecisionPolicy)
	}
	if s.CompactionDetector != other.CompactionDetector {
		t.Fatalf("%s: CompactionDetector instance changed: %p vs %p", stage, s.CompactionDetector, other.CompactionDetector)
	}
	if s.BranchCoordinator != other.BranchCoordinator {
		t.Fatalf("%s: BranchCoordinator instance changed: %p vs %p", stage, s.BranchCoordinator, other.BranchCoordinator)
	}
	if s.CompactionParentPort != other.CompactionParentPort {
		t.Fatalf("%s: CompactionParentPort instance changed: %p vs %p", stage, s.CompactionParentPort, other.CompactionParentPort)
	}
	if s.BackgroundAux != other.BackgroundAux {
		t.Fatalf("%s: BackgroundAux instance changed: %p vs %p", stage, s.BackgroundAux, other.BackgroundAux)
	}
}

// AssertDistinctOwnedResources asserts that all six feature-owned process resources have distinct
// physical pointers across two separate ProcessServices builds (proving constructors run once per process).
func (s ProcessFeatureSnapshot) AssertDistinctOwnedResources(t *testing.T, other ProcessFeatureSnapshot) {
	t.Helper()
	if s.KeepwarmPolicy == other.KeepwarmPolicy {
		t.Fatalf("KeepwarmPolicy pointer identical across distinct ProcessServices builds: %p", s.KeepwarmPolicy)
	}
	if s.KeepwarmRegistry == other.KeepwarmRegistry {
		t.Fatalf("KeepwarmRegistry pointer identical across distinct ProcessServices builds: %p", s.KeepwarmRegistry)
	}
	if s.TerminalDecisionPolicy == other.TerminalDecisionPolicy {
		t.Fatalf("TerminalDecisionPolicy pointer identical across distinct ProcessServices builds: %p", s.TerminalDecisionPolicy)
	}
	if s.CompactionDetector == other.CompactionDetector {
		t.Fatalf("CompactionDetector pointer identical across distinct ProcessServices builds: %p", s.CompactionDetector)
	}
	if s.BranchCoordinator == other.BranchCoordinator {
		t.Fatalf("BranchCoordinator pointer identical across distinct ProcessServices builds: %p", s.BranchCoordinator)
	}
	if s.CompactionParentPort == other.CompactionParentPort {
		t.Fatalf("CompactionParentPort pointer identical across distinct ProcessServices builds: %p", s.CompactionParentPort)
	}
}


func testProcessServicesOwnershipConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		Server: config.ServerConfig{
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 1024,
		},
	}
}

// TestProcessFeatureResources_OwnershipCountingSeam satisfies Task 1.2 by verifying:
// 1. (a) Exactly one construction of borrowed BackgroundAux (directly counted via factory seam)
//    and presence of the six feature-owned resources constructed by NewProcessServices (design.md:232);
// 2. (b) Zero duplicate constructions across two overlapping CompileCandidate compiles;
// 3. (c) Exactly one physical close per closable resource at ps.Close, with second Close idempotent;
// 4. Explicit verification of genuinely non-closable resources (no Close method or registration).
func TestProcessFeatureResources_OwnershipCountingSeam(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := testProcessServicesOwnershipConfig()
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()

	// Injectable construction counter for BackgroundAux.
	var auxConstructionCount atomic.Int32
	countedNewBackgroundScheduler := func() (*BackgroundAuxScheduler, error) {
		s, err := auxreq.NewBackgroundScheduler(ctx, func() auxreq.ExecutorRunner {
			return testBackgroundProcessRunner{}
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
		if err == nil {
			auxConstructionCount.Add(1)
		}
		return s, err
	}

	scheduler, err := countedNewBackgroundScheduler()
	if err != nil {
		t.Fatalf("countedNewBackgroundScheduler: %v", err)
	}

	// 1. Process Construction: NewProcessServices constructs the six feature-owned resources;
	// BackgroundAux is borrowed via ProcessServicesInput (test-owned factory, counted separately; design.md:232).
	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:           cfg,
		Log:           log,
		Opts:          &BuildOptions{PluginRegistry: reg},
		BackgroundAux: scheduler,
	})
	if err != nil {
		_ = scheduler.Close()
		t.Fatalf("NewProcessServices: %v", err)
	}

	// Capture initial snapshot and assert all process resources are present.
	initialSnap := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertAllPresent(t)

	// Verify construction count for borrowed BackgroundAux (test-owned counted factory)
	// and presence of all feature resources. Single constructor execution for the six
	// feature-owned resources is verified via distinct pointers across separate builds
	// in TestProcessFeatureResources_DistinctProcessInstances.
	if got := auxConstructionCount.Load(); got != 1 {
		_ = ps.Close()
		t.Fatalf("BackgroundAux construction count = %d, want exactly 1", got)
	}


	// Assert explicitly non-closable resources do not implement io.Closer.
	if _, ok := any(ps.KeepwarmPolicy).(io.Closer); ok {
		_ = ps.Close()
		t.Fatal("KeepwarmPolicy must be genuinely non-closable (implements io.Closer unexpectedly)")
	}
	if _, ok := any(ps.KeepwarmRegistry).(io.Closer); ok {
		_ = ps.Close()
		t.Fatal("KeepwarmRegistry must be genuinely non-closable (implements io.Closer unexpectedly)")
	}
	if _, ok := any(ps.CompactionDetector).(io.Closer); ok {
		_ = ps.Close()
		t.Fatal("CompactionDetector must be genuinely non-closable (implements io.Closer unexpectedly)")
	}
	if _, ok := any(ps.BranchCoordinator).(io.Closer); ok {
		_ = ps.Close()
		t.Fatal("BranchCoordinator must be genuinely non-closable (implements io.Closer unexpectedly)")
	}
	if _, ok := any(ps.CompactionParentPort).(io.Closer); ok {
		_ = ps.Close()
		t.Fatal("CompactionParentPort must be genuinely non-closable (implements io.Closer unexpectedly)")
	}

	// Wrap closers of actually-closable process resources with injectable counters
	// to verify exactly-once physical Close execution and detect any double disposal.
	// In NewProcessServices:
	// - closer 0 is registered at process_services.go:85 (TerminalDecisionPolicy.Close)
	// - closer 1 is registered at background_aux_lifecycle.go:27 (BackgroundAux.Close)
	if len(ps.closers) < 2 {
		_ = ps.Close()
		t.Fatalf("ps.closers length = %d, expected at least 2 for TDP and Aux", len(ps.closers))
	}

	var terminalPolicyCloseCount atomic.Int32
	var backgroundAuxCloseCount atomic.Int32

	origTDPClose := ps.closers[0]
	ps.closers[0] = func() error {
		terminalPolicyCloseCount.Add(1)
		return origTDPClose()
	}

	origAuxClose := ps.closers[1]
	ps.closers[1] = func() error {
		backgroundAuxCloseCount.Add(1)
		return origAuxClose()
	}

	initialCloserCount := len(ps.closers)

	// Verify initial operational state of closable resources before candidate compile.
	policyKey := terminaldecisionpolicy.Key{
		SecureSessionIncarnation: "test-sess",
		ALegID:                   "test-aleg",
		FeatureID:                "terminal-decision",
	}
	policyAuth := terminaldecisionpolicy.Authority{
		SecureSessionIncarnation: "test-sess",
		ALegID:                   "test-aleg",
		Authorized:               true,
	}
	snap, err := ps.TerminalDecisionPolicy.Snapshot(ctx, policyAuth, policyKey, false)
	if err != nil {
		_ = ps.Close()
		t.Fatalf("TerminalDecisionPolicy.Effective before candidates: %v", err)
	}
	if snap.EffectiveEnabled {
		_ = ps.Close()
		t.Fatalf("expected false default effective state")
	}

	subID, err := ps.BackgroundAux.SubmitCollect(ctx, auxiliary.Request{Call: &lipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "initial-turn"})
	if err != nil {
		_ = ps.Close()
		t.Fatalf("BackgroundAux.SubmitCollect before candidates: %v", err)
	}
	if _, err := ps.BackgroundAux.Await(ctx, subID); err != nil {
		_ = ps.Close()
		t.Fatalf("BackgroundAux.Await before candidates: %v", err)
	}

	// 2. Overlapping Generation Compilation:
	// (b) Zero duplicate constructions across two overlapping CompileCandidate compiles.
	bus1 := hooks.New(hooks.Config{})
	c1, err := CompileCandidate(ctx, GenerationCompileInput{
		Process: ps,
		Bus:     bus1,
	})
	if err != nil {
		_ = ps.Close()
		t.Fatalf("CompileCandidate #1: %v", err)
	}

	// Assert zero duplicate constructions during Candidate #1 compile.
	if got := auxConstructionCount.Load(); got != 1 {
		_ = c1.Close()
		_ = ps.Close()
		t.Fatalf("BackgroundAux construction count after Candidate #1 = %d, want 1", got)
	}
	if len(ps.closers) != initialCloserCount {
		_ = c1.Close()
		_ = ps.Close()
		t.Fatalf("ps.closers grew during Candidate #1 compile: %d vs %d", len(ps.closers), initialCloserCount)
	}
	snapAfterGen1 := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterGen1, "after candidate #1 compile")

	// Compile Candidate Generation 2 overlapping with Generation 1 before Generation 1 closes.
	bus2 := hooks.New(hooks.Config{})
	c2, err := CompileCandidate(ctx, GenerationCompileInput{
		Process: ps,
		Bus:     bus2,
	})
	if err != nil {
		_ = c1.Close()
		_ = ps.Close()
		t.Fatalf("CompileCandidate #2: %v", err)
	}

	// Assert zero duplicate constructions during overlapping Candidate #2 compile.
	if got := auxConstructionCount.Load(); got != 1 {
		_ = c1.Close()
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("BackgroundAux construction count after Candidate #2 = %d, want 1", got)
	}
	if len(ps.closers) != initialCloserCount {
		_ = c1.Close()
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("ps.closers grew during Candidate #2 compile: %d vs %d", len(ps.closers), initialCloserCount)
	}
	snapAfterGen2 := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterGen2, "after candidate #2 compile (overlapping)")

	// Close Candidate Generation 1.
	if err := c1.Close(); err != nil {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("Candidate #1 Close: %v", err)
	}

	// Assert process resources were NOT closed by Candidate 1 Close.
	if ps.Closed() {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatal("ProcessServices was unexpectedly closed by Candidate #1 Close")
	}
	if terminalPolicyCloseCount.Load() != 0 {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("TerminalDecisionPolicy closed prematurely on Candidate #1 Close")
	}
	if backgroundAuxCloseCount.Load() != 0 {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("BackgroundAux closed prematurely on Candidate #1 Close")
	}
	snapAfterC1Close := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterC1Close, "after candidate #1 close")

	// Verify closable resources still accept calls while Generation 2 remains active.
	if _, err := ps.TerminalDecisionPolicy.Snapshot(ctx, policyAuth, policyKey, false); err != nil {
		_ = c2.Close()
		_ = ps.Close()
		t.Fatalf("TerminalDecisionPolicy unexpectedly failed after Candidate #1 Close: %v", err)
	}

	// Close Candidate Generation 2.
	if err := c2.Close(); err != nil {
		_ = ps.Close()
		t.Fatalf("Candidate #2 Close: %v", err)
	}

	// Assert process resources were NOT closed by Candidate 2 Close.
	if ps.Closed() {
		_ = ps.Close()
		t.Fatal("ProcessServices was unexpectedly closed by Candidate #2 Close")
	}
	if terminalPolicyCloseCount.Load() != 0 {
		_ = ps.Close()
		t.Fatalf("TerminalDecisionPolicy closed prematurely on Candidate #2 Close")
	}
	if backgroundAuxCloseCount.Load() != 0 {
		_ = ps.Close()
		t.Fatalf("BackgroundAux closed prematurely on Candidate #2 Close")
	}
	snapAfterC2Close := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterC2Close, "after candidate #2 close")

	// 3. Process Shutdown:
	// (c) Exactly one physical close per closable resource at ps.Close.
	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if !ps.Closed() {
		t.Fatal("ProcessServices.Closed() returned false after Close")
	}

	// Assert exactly one physical close per closable resource.
	if got := terminalPolicyCloseCount.Load(); got != 1 {
		t.Fatalf("TerminalDecisionPolicy physical close count = %d, want exactly 1", got)
	}
	if got := backgroundAuxCloseCount.Load(); got != 1 {
		t.Fatalf("BackgroundAux physical close count = %d, want exactly 1", got)
	}

	// Verify TerminalDecisionPolicy is physically closed (operations fail with ErrClosed).
	if _, err := ps.TerminalDecisionPolicy.Snapshot(ctx, policyAuth, policyKey, false); !errors.Is(err, terminaldecisionpolicy.ErrClosed) {
		t.Fatalf("TerminalDecisionPolicy.Effective after ps.Close() = %v, want ErrClosed", err)
	}

	// Verify BackgroundAux is physically closed (operations fail with ErrSchedulerClosed).
	if _, err := ps.BackgroundAux.SubmitCollect(ctx, auxiliary.Request{Call: &lipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "after-ps-close"}); !errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatalf("BackgroundAux.SubmitCollect after ps.Close() = %v, want ErrSchedulerClosed", err)
	}

	// 4. Close Idempotency:
	// (c) second Close is safe and does NOT invoke physical closers again.
	if err := ps.Close(); err != nil {
		t.Fatalf("idempotent ProcessServices.Close: %v", err)
	}
	if got := terminalPolicyCloseCount.Load(); got != 1 {
		t.Fatalf("TerminalDecisionPolicy physical close count after second Close = %d, want exactly 1", got)
	}
	if got := backgroundAuxCloseCount.Load(); got != 1 {
		t.Fatalf("BackgroundAux physical close count after second Close = %d, want exactly 1", got)
	}
}

func TestProcessFeatureResources_ConcurrentOverlappingGenerations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := testProcessServicesOwnershipConfig()
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()

	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: &BuildOptions{PluginRegistry: reg},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	initialSnap := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertAllPresent(t)

	const concurrency = 4
	errCh := make(chan error, concurrency)
	cands := make([]*CandidateHTTPCompile, concurrency)

	for i := range concurrency {
		go func(idx int) {
			bus := hooks.New(hooks.Config{})
			c, err := CompileCandidate(ctx, GenerationCompileInput{
				Process: ps,
				Bus:     bus,
			})
			if err != nil {
				errCh <- err
				return
			}
			cands[idx] = c
			errCh <- nil
		}(i)
	}

	for range concurrency {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent CompileCandidate failed: %v", err)
		}
	}

	snapConcurrent := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapConcurrent, "after concurrent overlapping generation compiles")

	for _, c := range cands {
		if c != nil {
			_ = c.Close()
		}
	}

	snapAfterCloses := CaptureProcessFeatureSnapshot(ps)
	initialSnap.AssertIdentical(t, snapAfterCloses, "after concurrent candidates closed")
}

// TestProcessFeatureResources_DistinctProcessInstances proves that NewProcessServices constructs
// distinct physical instances of each of the six feature-owned resources for each process build
// (proving constructors run once per process without shared global or singleton state).
func TestProcessFeatureResources_DistinctProcessInstances(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	log := testkit.DiscardLogger()

	ps1, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  log,
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices #1: %v", err)
	}
	t.Cleanup(func() { _ = ps1.Close() })

	ps2, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  log,
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices #2: %v", err)
	}
	t.Cleanup(func() { _ = ps2.Close() })

	snap1 := CaptureProcessFeatureSnapshot(ps1)
	snap1.AssertAllPresent(t)

	snap2 := CaptureProcessFeatureSnapshot(ps2)
	snap2.AssertAllPresent(t)

	// Assert distinct pointers for all six feature-owned resources (Requirement 1.2, item a).
	snap1.AssertDistinctOwnedResources(t, snap2)

	// Also verify default-constructed BackgroundAux instances are distinct.
	if snap1.BackgroundAux == snap2.BackgroundAux {
		t.Fatalf("BackgroundAux pointer identical across distinct ProcessServices builds: %p", snap1.BackgroundAux)
	}
}

