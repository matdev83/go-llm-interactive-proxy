package featurehost

// Package-local lifecycle seams for staged-construction coverage.
//
// The staged-construction injection used below lives entirely in this _test.go
// scope plus the unexported ProcessInput.buildSteps field: no external caller
// (including generic runtimebundle) can inject constructors or closers
// (Tasks 2.1/2.3, Requirements 2.5/8.4).

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	corestate "github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
	compactiondetect "github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/state"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/compaction"
)

func TestProcess_CloseIdempotencyCounting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	var closeCount atomic.Int32
	rt.registerCloser(func() error {
		closeCount.Add(1)
		return nil
	})

	if err := rt.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("closer called %d times, want 1", got)
	}

	// Second Close must be idempotent and not invoke closer again.
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("closer called %d times after second Close, want 1", got)
	}
}

func TestProcess_PartialConstructionRollback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var order []string

	step1Close := func() error {
		order = append(order, "step1")
		return nil
	}
	step2Close := func() error {
		order = append(order, "step2")
		return errors.New("step2 close error")
	}

	expectedErr := errors.New("construction step 3 failed")

	in := ProcessInput{
		Logger: slog.Default(),
		buildSteps: []constructionStep{
			{
				Name: "step1",
				Construct: func(r *Runtime) error {
					r.registerCloser(step1Close)
					return nil
				},
			},
			{
				Name: "step2",
				Construct: func(r *Runtime) error {
					r.registerCloser(step2Close)
					return nil
				},
			},
			{
				Name: "step3",
				Construct: func(r *Runtime) error {
					return expectedErr
				},
			},
		},
	}

	_, err := NewProcess(ctx, in)
	if err == nil {
		t.Fatal("expected construction error")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error containing %v, got %v", expectedErr, err)
	}

	// Unwind must be in reverse order: step2 before step1
	if len(order) != 2 || order[0] != "step2" || order[1] != "step1" {
		t.Fatalf("expected rollback order [step2, step1], got %v", order)
	}
}

func TestProcess_CleanupErrorAggregationAndOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	in := ProcessInput{
		Logger: slog.Default(),
	}

	rt, err := NewProcess(ctx, in)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	var order []string
	err1 := errors.New("err1")
	err2 := errors.New("err2")

	rt.registerCloser(func() error {
		order = append(order, "first")
		return err1
	})
	rt.registerCloser(func() error {
		order = append(order, "second")
		return err2
	})

	closeErr := rt.Close()
	if closeErr == nil {
		t.Fatal("expected combined error on Close")
	}
	if !errors.Is(closeErr, err1) || !errors.Is(closeErr, err2) {
		t.Fatalf("expected closeErr to contain err1 and err2, got %v", closeErr)
	}

	// Reverse order: second registered should be closed first
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Fatalf("expected close order [second, first], got %v", order)
	}
}

func TestRuntime_DualOwnershipSignalsObservable(t *testing.T) {
	t.Parallel()

	// Proves the exact signals ValidateProcessFeatureOwnership relies on flip
	// under genuine dual ownership built with the REAL legacy constructor and
	// the REAL closer registration. Pre-handoff, no production path can create
	// this state (proven structurally by
	// TestFeatureHost_NoPreHandoffFeatureConstruction and behaviorally by
	// TestProcessFeatureOwnership_DualConstructorWiringRejected, which assert
	// absence across representative process inputs). When the Task 7.3 handoff
	// lands a real second constructor, THESE signals are what the ownership
	// validator observes to reject dual wiring.
	ctx := context.Background()
	r, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Precondition: pre-handoff Runtime owns nothing.
	if got := r.TerminalDecisionPolicy(); got != nil {
		t.Fatalf("expected no featurehost-owned policy pre-handoff, observed %p", got)
	}
	if got := r.ClosersCount(); got != 0 {
		t.Fatalf("expected zero featurehost-owned closers pre-handoff, observed %d", got)
	}

	// Simulate the Task 7.3 defect with the real constructor + real registration.
	store := terminaldecisionpolicy.NewStore(terminaldecisionpolicy.Config{})
	r.terminalPolicy = store
	r.registerCloser(store.Close)

	// Both ownership signals must flip: this is the observable dual state the
	// validator rejects with ErrDualConstructorWiring.
	if got := r.TerminalDecisionPolicy(); got == nil {
		t.Fatal("expected featurehost-owned policy to be observable after second construction")
	} else if got != store {
		t.Fatalf("expected observed policy %p to be the constructed store %p", got, store)
	}
	if got := r.ClosersCount(); got != 1 {
		t.Fatalf("expected 1 featurehost-owned closer after second construction, observed %d", got)
	}
}

func TestProcess_CompactionConstructionCounted(t *testing.T) {
	// NOT Parallel: swaps the package-level constructor seams below.
	// Sequential tests complete before parallel siblings resume, so the swap
	// cannot race with other tests in this package.
	var detCount, coordCount, portCount atomic.Int32
	origDet, origCoord, origPort := newCompactionDetector, newBranchCoordinator, newCompactionParentPort
	t.Cleanup(func() {
		newCompactionDetector, newBranchCoordinator, newCompactionParentPort = origDet, origCoord, origPort
	})
	newCompactionDetector = func(c compactiondetect.Config) *compactiondetect.Detector {
		detCount.Add(1)
		return origDet(c)
	}
	newBranchCoordinator = func(ctx context.Context, cfg state.Config) (*state.BranchCoordinator, error) {
		coordCount.Add(1)
		return origCoord(ctx, cfg)
	}
	newCompactionParentPort = func(coord *state.BranchCoordinator) (*compaction.ParentPort, error) {
		portCount.Add(1)
		return origPort(coord)
	}

	newCountedProcess := func(t *testing.T) *Runtime {
		t.Helper()
		r, err := NewProcess(context.Background(), ProcessInput{Logger: slog.Default(), ExtensionState: corestate.NewMem(nil)})
		if err != nil {
			t.Fatalf("NewProcess: %v", err)
		}
		return r
	}

	r1 := newCountedProcess(t)
	t.Cleanup(func() { _ = r1.Close() })

	// Exactly one successful construction per resource per process.
	if got := detCount.Load(); got != 1 {
		t.Fatalf("detector constructions = %d, want 1", got)
	}
	if got := coordCount.Load(); got != 1 {
		t.Fatalf("coordinator constructions = %d, want 1", got)
	}
	if got := portCount.Load(); got != 1 {
		t.Fatalf("parent-port constructions = %d, want 1", got)
	}

	// Presence: each owned resource is instantiated.
	if r1.compactionDetector == nil {
		t.Fatal("expected non-nil compactionDetector on Runtime")
	}
	if r1.branchCoordinator == nil {
		t.Fatal("expected non-nil branchCoordinator on Runtime")
	}
	if r1.compactionParentPort == nil {
		t.Fatal("expected non-nil compactionParentPort on Runtime")
	}

	// None of the three transferred resources implements io.Closer.
	if _, ok := any(r1.compactionDetector).(io.Closer); ok {
		t.Fatal("CompactionDetector must not implement io.Closer")
	}
	if _, ok := any(r1.branchCoordinator).(io.Closer); ok {
		t.Fatal("BranchCoordinator must not implement io.Closer")
	}
	if _, ok := any(r1.compactionParentPort).(io.Closer); ok {
		t.Fatal("CompactionParentPort must not implement io.Closer")
	}

	// Distinct instances across processes prove per-process 1:1 construction.
	r2 := newCountedProcess(t)
	t.Cleanup(func() { _ = r2.Close() })
	if got := detCount.Load(); got != 2 {
		t.Fatalf("detector constructions after 2 processes = %d, want 2", got)
	}
	if got := coordCount.Load(); got != 2 {
		t.Fatalf("coordinator constructions after 2 processes = %d, want 2", got)
	}
	if got := portCount.Load(); got != 2 {
		t.Fatalf("parent-port constructions after 2 processes = %d, want 2", got)
	}
	if r1.branchCoordinator == r2.branchCoordinator {
		t.Fatalf("branchCoordinator identical across processes: %p", r1.branchCoordinator)
	}
	if r1.compactionParentPort == r2.compactionParentPort {
		t.Fatalf("compactionParentPort identical across processes: %p", r1.compactionParentPort)
	}

	// Zero constructions across overlapping generations on the same process.
	if _, err := r1.CompileGeneration(context.Background(), GenerationInput{}); err != nil {
		t.Fatalf("CompileGeneration #1: %v", err)
	}
	if _, err := r1.CompileGeneration(context.Background(), GenerationInput{}); err != nil {
		t.Fatalf("CompileGeneration #2: %v", err)
	}
	if got := detCount.Load(); got != 2 {
		t.Fatalf("detector constructions after overlapping generations = %d, want 2", got)
	}
	if got := coordCount.Load(); got != 2 {
		t.Fatalf("coordinator constructions after overlapping generations = %d, want 2", got)
	}
	if got := portCount.Load(); got != 2 {
		t.Fatalf("parent-port constructions after overlapping generations = %d, want 2", got)
	}
}

func TestProcess_BranchCoordinatorBehaviorAndGenerationStability(t *testing.T) {
	t.Parallel()

	// Process-owned coordinator behavior plus stability across generations,
	// previously covered through the public accessor from runtimebundle and
	// now kept inside package featurehost with the unexported fields.
	ctx := context.Background()
	r, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), ExtensionState: corestate.NewMem(nil)})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	key, err := state.NewBranchKey("session-parent", "a-parent", "principal-1")
	if err != nil {
		t.Fatalf("NewBranchKey: %v", err)
	}
	if _, err := r.branchCoordinator.Capture(ctx, key); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, err := r.branchCoordinator.CommitCapsule(ctx, key, 0, []byte(`{"parent":true}`), [32]byte{1}, "source-1"); err != nil {
		t.Fatalf("CommitCapsule: %v", err)
	}

	first := r.branchCoordinator
	for range 2 {
		if _, err := r.CompileGeneration(ctx, GenerationInput{}); err != nil {
			t.Fatalf("CompileGeneration: %v", err)
		}
		if r.branchCoordinator != first {
			t.Fatal("generation compilation must not replace process branch coordinator")
		}
		if r.compactionParentPort == nil {
			t.Fatal("generation compilation must not drop process parent port")
		}
	}
	got, ok, err := r.branchCoordinator.Snapshot(ctx, key)
	if err != nil || !ok || got.Revision != 1 || string(got.CapsuleJSON) != `{"parent":true}` {
		t.Fatalf("coordinator state = %#v, found=%v, err=%v", got, ok, err)
	}
}

// TestProcess_CompactionResourceDistinctness_InternalSeam documents and verifies that
// BranchCoordinator and ParentPort instances are distinct across processes.
//
// Documentation: BranchCoordinator and ParentPort have no direct exported accessor or
// CorePorts field on GenerationOutput outside featurehost (only CompactionDetector is
// exposed via GenerationOutput.CorePorts.CompactionDetector). The runtime consumes ParentPort
// internally in generation.go via bindCompactionContinuity, passing it to
// featurecontinuity.FeatureBundleWithPort. Outside featurehost, the parent port is encased in
// the unexported field of the returned Plugin preserver.
// Therefore, direct pointer distinctness of the owned BranchCoordinator and ParentPort
// instances across processes is verified directly on the Runtime instances via this package-local seam.
func TestProcess_CompactionResourceDistinctness_InternalSeam(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	rt1, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess #1: %v", err)
	}
	t.Cleanup(func() { _ = rt1.Close() })

	rt2, err := NewProcess(ctx, ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess #2: %v", err)
	}
	t.Cleanup(func() { _ = rt2.Close() })

	if rt1.branchCoordinator == nil || rt2.branchCoordinator == nil {
		t.Fatal("expected non-nil branchCoordinator")
	}
	if rt1.branchCoordinator == rt2.branchCoordinator {
		t.Fatalf("branchCoordinator identical across processes: %p", rt1.branchCoordinator)
	}

	if rt1.compactionParentPort == rt2.compactionParentPort {
		t.Fatalf("compactionParentPort identical across processes: %p", rt1.compactionParentPort)
	}
	if rt1.compactionDetector == rt2.compactionDetector {
		t.Fatalf("compactionDetector identical across processes: %p", rt1.compactionDetector)
	}
}
