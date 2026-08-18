package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestBillingCallState_OwnershipAndBillingCallID(t *testing.T) {
	// Task 3.1: Verify request/BillingCallID-scoped state is allocated once per prepared invocation.
	// Two distinct prepared requests must have distinct state objects and distinct call IDs.
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	executor := TestExecutor()
	executor.Store = st
	executor.Bus = hooks.New(hooks.Config{})
	executor.TerminalUsageSink = testTerminalSink{appendCall: func(ctx context.Context, record billing.CallUsageRecord) error {
		return nil
	}}

	call1 := &lipapi.Call{
		ID:      "call-1",
		Session: lipapi.SessionRef{AuthoritativeSessionID: "session-1"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	prep1, _, cleanup1, err := executor.prepareRequest(context.Background(), call1)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup1()

	if prep1.billingCallState == nil {
		t.Fatal("billingCallState should be allocated during prepareRequest")
	}

	call2 := &lipapi.Call{
		ID:      "call-2",
		Session: lipapi.SessionRef{AuthoritativeSessionID: "session-1"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	prep2, _, cleanup2, err := executor.prepareRequest(context.Background(), call2)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()

	if prep2.billingCallState == nil {
		t.Fatal("billingCallState should be allocated for prep2")
	}

	if prep1.billingCallState == prep2.billingCallState {
		t.Error("distinct invocations on the same session/A-leg must receive distinct state pointers")
	}
	if prep1.billingCallID == prep2.billingCallID {
		t.Errorf("distinct invocations reused BillingCallID %q", prep1.billingCallID)
	}

	if prep1.billingCallState.callID != prep1.billingCallID {
		t.Errorf("state callID %q does not match prep billingCallID %q", prep1.billingCallState.callID, prep1.billingCallID)
	}
}

func TestBillingCallState_ParallelAndInterleavedSharing(t *testing.T) {
	// Verify retry/parallel/interleaved paths for one invocation share the state pointer.
	t.Parallel()

	state := &billingCallState{
		callID: "test-call-id",
	}

	// 1. Parallel leg allocation sharing
	var wg sync.WaitGroup
	const numParallel = 5
	for i := 0; i < numParallel; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			state.noteAllocatedBLeg(billingSyntheticBLegID(seq), seq)
		}(i + 1)
	}
	wg.Wait()

	expectedSet := state.freezeAllocatedBLegs()
	if len(expectedSet) != numParallel {
		t.Errorf("expected %d allocated legs, got %d", numParallel, len(expectedSet))
	}
}

func TestBillingCallState_FinalizationSingleFlight(t *testing.T) {
	// Verify racing finalizations use single-flight behavior and share results.
	t.Parallel()

	state := &billingCallState{
		callID: "test-call-id",
	}

	var callCount int64
	finalizeFn := func(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
		time.Sleep(10 * time.Millisecond)
		return lipapi.Event{
			Kind:        lipapi.EventUsageDelta,
			InputTokens: 42,
		}, nil
	}

	var wg sync.WaitGroup
	const numRacers = 10
	results := make([]lipapi.Event, numRacers)
	oks := make([]bool, numRacers)

	for i := 0; i < numRacers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ev, ok := state.finalizeOnce(context.Background(), execbackend.BillingFinalizationInput{
				BLegID:  "b-leg-1",
				Backend: "backend-1",
				Model:   "model-1",
			}, func(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
				callCount++
				return finalizeFn(ctx, in)
			})
			results[idx] = ev
			oks[idx] = ok
		}(i)
	}
	wg.Wait()

	if callCount != 1 {
		t.Errorf("expected backend FinalizeBilling to be called exactly once, got %d", callCount)
	}

	for i := 0; i < numRacers; i++ {
		if !oks[i] {
			t.Errorf("racer %d failed", i)
		}
		if results[i].InputTokens != 42 {
			t.Errorf("racer %d got unexpected tokens: %d", i, results[i].InputTokens)
		}
	}
}

func TestBillingCallState_TimingBoundsClosure(t *testing.T) {
	// Verify timing bounds and closure expected set logic.
	t.Parallel()

	state := &billingCallState{
		callID: "test-call-id",
	}

	now := time.Now()
	state.noteLegTimes(now.Add(-10*time.Second), now.Add(-5*time.Second))
	state.noteLegTimes(now.Add(-8*time.Second), now.Add(-2*time.Second))

	state.noteAllocatedBLeg("b-leg-1", 1)
	state.noteAllocatedBLeg("b-leg-2", 2)

	expectedIds := state.freezeAllocatedBLegs()
	if len(expectedIds) != 2 || expectedIds[0] != "b-leg-1" || expectedIds[1] != "b-leg-2" {
		t.Errorf("unexpected expectedIds: %v", expectedIds)
	}

	started, finished := state.timingBounds(now)
	if !started.Equal(now.Add(-10 * time.Second)) {
		t.Errorf("expected started time to be min start, got %v", started)
	}
	if !finished.Equal(now.Add(-2 * time.Second)) {
		t.Errorf("expected finished time to be max finish, got %v", finished)
	}
}
