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
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminaldecisionpolicy"
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
