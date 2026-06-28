package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestOpenPlannedCandidate_MaxAttemptsDoesNotPersistCycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "cycle-budget")
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("[thinker]thinker:m^exec:m")
	if err != nil {
		t.Fatal(err)
	}
	ex := &Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"exec": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
					Operation: lipapi.OperationOpenAIChatCompletions,
					Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
				}),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					t.Fatal("backend must not open when attempt budget is exhausted")
					return nil, nil
				},
			},
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "think"},
	}
	ttft := newTTFTBudget(ex.now(), sel)
	_, err = ex.tryPlanOpenOnce(attemptOpenParams{
		ctx:         ctx,
		bus:         ex.Bus,
		traceID:     "cycle-budget-test",
		aLegID:      aLeg.ALegID,
		baseline:    lipapi.Call{Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		sel:         sel,
		session:     &routing.SessionRoutingState{},
		excluded:    map[string]struct{}{},
		rng:         routing.NewSeededRng(1),
		budget:      &attemptBudget{max: 0},
		ttft:        &ttft,
		interleaved: interleavedstate.State{},
	})
	if !errors.Is(err, lipapi.ErrMaxRouteAttempts) {
		t.Fatalf("want ErrMaxRouteAttempts, got %v", err)
	}
	state, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.IsEmpty() {
		t.Fatalf("cycle must not persist when budget blocks open, got %+v", state)
	}
}

func TestTryPlanOpenOnce_ParallelAllLegsFailPreservesInterleavedState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "parallel-fail-state")
	if err != nil {
		t.Fatal(err)
	}
	const selector = "[thinker]thinker-be:m^fail1:m!fail2:m"
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatal(err)
	}
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
	})
	ex := &Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"fail1": {Caps: caps, TransportCaps: transport, Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return ParallelPreWinFailStream{}, nil
			}},
			"fail2": {Caps: caps, TransportCaps: transport, Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return ParallelPreWinFailStream{}, nil
			}},
			"good": {Caps: caps, TransportCaps: transport, Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				t.Fatal("good backend must not open when parallel soft-fails without failover arm")
				return nil, nil
			}},
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step."},
	}
	seededCycle := interleavedstate.CycleState{
		SelectorKey: "thinker-be:m^parallel:fail1:m!fail2:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "parallel:fail1:m!fail2:m", Role: interleavedstate.RoleExecutor},
			{Key: "thinker-be:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: 0,
	}
	interleaved := interleavedstate.State{Cycle: seededCycle}
	excluded := map[string]struct{}{}
	ttft := newTTFTBudget(ex.now(), sel)
	p := attemptOpenParams{
		ctx:         ctx,
		bus:         ex.Bus,
		traceID:     "parallel-fail-state",
		aLegID:      aLeg.ALegID,
		baseline:    lipapi.Call{Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming}},
		sel:         sel,
		session:     &routing.SessionRoutingState{},
		excluded:    excluded,
		rng:         routing.NewSeededRng(2),
		budget:      &attemptBudget{max: 8},
		ttft:        &ttft,
		interleaved: interleaved,
	}
	out1, err := ex.tryPlanOpenOnce(p)
	if err != nil {
		t.Fatalf("first plan/open: %v", err)
	}
	if out1.opened {
		t.Fatal("first iteration must soft-fail parallel race without winner")
	}
	if out1.interleaved.Cycle.IsEmpty() {
		t.Fatal("parallel all-legs-fail must preserve advanced interleaved cycle in result")
	}
	stored, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !out1.interleaved.Cycle.Equal(stored.Cycle) {
		t.Fatalf("threaded cycle must match persisted cycle, got %+v stored %+v", out1.interleaved.Cycle, stored.Cycle)
	}
	if stored.Cycle.NextIndex == seededCycle.NextIndex {
		t.Fatal("parallel open must advance persisted thinker cycle before legs run")
	}
}

func TestTryPlanOpenOnce_ParallelAllLegsFailFailoverToPrimaryInSamePass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "parallel-failover-primary")
	if err != nil {
		t.Fatal(err)
	}
	const selector = "[thinker]thinker-be:m^fail1:m!fail2:m|good:m"
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatal(err)
	}
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
	})
	var goodOpens int
	ex := &Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"fail1": {Caps: caps, TransportCaps: transport, Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return ParallelPreWinFailStream{}, nil
			}},
			"fail2": {Caps: caps, TransportCaps: transport, Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return ParallelPreWinFailStream{}, nil
			}},
			"good": {Caps: caps, TransportCaps: transport, Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				goodOpens++
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "good-wins"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			}},
			"thinker-be": {Caps: caps, TransportCaps: transport, Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				t.Fatal("thinker branch must not open when parallel soft-fails into primary failover")
				return nil, nil
			}},
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step."},
	}
	interleaved := interleavedstate.State{Cycle: interleavedstate.CycleState{
		SelectorKey: "thinker-be:m^parallel:fail1:m!fail2:m|good:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "parallel:fail1:m!fail2:m", Role: interleavedstate.RoleExecutor},
			{Key: "thinker-be:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: 0,
	}}
	excluded := map[string]struct{}{}
	ttft := newTTFTBudget(ex.now(), sel)
	var lastParallelFailure error
	p := attemptOpenParams{
		ctx:                 ctx,
		bus:                 ex.Bus,
		traceID:             "parallel-failover-primary",
		aLegID:              aLeg.ALegID,
		baseline:            lipapi.Call{Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming}},
		sel:                 sel,
		session:             &routing.SessionRoutingState{},
		excluded:            excluded,
		rng:                 routing.NewSeededRng(2),
		budget:              &attemptBudget{max: 8},
		ttft:                &ttft,
		interleaved:         interleaved,
		lastParallelFailure: &lastParallelFailure,
	}
	out, err := ex.tryPlanOpenOnce(p)
	if err != nil {
		t.Fatalf("plan/open: %v", err)
	}
	if !out.opened {
		t.Fatal("same-pass tryPlanOpenOnce must open primary after parallel soft-fail")
	}
	if lastParallelFailure != nil {
		t.Fatal("parallel failure context must clear after successful primary open")
	}
	if goodOpens != 1 {
		t.Fatalf("good backend opens: got %d want 1", goodOpens)
	}
	if out.cand.Primary.Backend != "good" {
		t.Fatalf("opened backend: got %q want good", out.cand.Primary.Backend)
	}
}

var errInjectedCyclePersist = errors.New("injected: cycle persist failed")

type failInterleavedPersistStore struct {
	*b2bua.MemoryStore
}

func (s *failInterleavedPersistStore) SetInterleavedState(context.Context, string, interleavedstate.State) error {
	return errInjectedCyclePersist
}

func TestTryPlanOpenOnce_ThinkerRecoverableOpenFailureDoesNotPersistCycleAdvance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "thinker-open-fail-cycle")
	if err != nil {
		t.Fatal(err)
	}
	const selector = "[thinker]bad-thinker:m^exec-be:m"
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatal(err)
	}
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
	})
	var opensMu sync.Mutex
	var opened []string
	ex := &Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"bad-thinker": {
				Caps: caps, TransportCaps: transport,
				Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					if cand.InterleavedRole != interleavedstate.RoleThinker {
						t.Fatalf("first open role: got %q want thinker", cand.InterleavedRole)
					}
					opensMu.Lock()
					opened = append(opened, "bad-thinker")
					opensMu.Unlock()
					return nil, lipapi.RecoverablePreOutputError(errors.New("thinker down"))
				},
			},
			"exec-be": {
				Caps: caps, TransportCaps: transport,
				Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opensMu.Lock()
					opened = append(opened, "exec-be")
					opensMu.Unlock()
					t.Fatal("executor must not open in same tryPlanOpenOnce pass after thinker recoverable open failure")
					return nil, nil
				},
			},
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step."},
	}
	thinkerIdx := 1
	seededCycle := interleavedstate.CycleState{
		SelectorKey: "bad-thinker:m^exec-be:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "exec-be:m", Role: interleavedstate.RoleExecutor},
			{Key: "bad-thinker:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: thinkerIdx,
	}
	seeded := interleavedstate.State{Cycle: seededCycle}
	if err := st.SetInterleavedState(ctx, aLeg.ALegID, seeded); err != nil {
		t.Fatal(err)
	}
	ttft := newTTFTBudget(ex.now(), sel)
	out, err := ex.tryPlanOpenOnce(attemptOpenParams{
		ctx:     ctx,
		bus:     ex.Bus,
		traceID: "thinker-open-fail-cycle",
		aLegID:  aLeg.ALegID,
		baseline: lipapi.Call{
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("plan this")},
			}},
			Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		},
		sel:         sel,
		session:     &routing.SessionRoutingState{},
		excluded:    map[string]struct{}{},
		rng:         routing.NewSeededRng(2),
		budget:      &attemptBudget{max: 8},
		ttft:        &ttft,
		interleaved: seeded,
	})
	if err != nil {
		t.Fatalf("tryPlanOpenOnce: %v", err)
	}
	if out.opened {
		t.Fatal("thinker recoverable open failure must not open a stream")
	}
	opensMu.Lock()
	gotOpens := append([]string(nil), opened...)
	opensMu.Unlock()
	if len(gotOpens) != 1 || gotOpens[0] != "bad-thinker" {
		t.Fatalf("opens: got %+v want [bad-thinker]", gotOpens)
	}
	if out.interleaved.Cycle.NextIndex != thinkerIdx {
		t.Fatalf("threaded cycle NextIndex: got %d want %d", out.interleaved.Cycle.NextIndex, thinkerIdx)
	}
	stored, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Cycle.NextIndex != thinkerIdx {
		t.Fatalf("persisted cycle NextIndex: got %d want %d (must not advance past failed thinker)", stored.Cycle.NextIndex, thinkerIdx)
	}
	if stored.Cycle.Sequence[thinkerIdx].Role != interleavedstate.RoleThinker {
		t.Fatalf("persisted cycle cursor must still point at thinker entry, got %+v", stored.Cycle.Sequence[stored.Cycle.NextIndex])
	}
}

func TestTryPlanOpenOnce_InterleavedCyclePersistFailureFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := &failInterleavedPersistStore{MemoryStore: base}
	aLeg, err := st.CreateALeg(ctx, "persist-fail")
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("[thinker]thinker:m^exec:m")
	if err != nil {
		t.Fatal(err)
	}
	var backendOpens int
	ex := &Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"exec": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
					Operation: lipapi.OperationOpenAIChatCompletions,
					Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
				}),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					backendOpens++
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "think"},
	}
	ttft := newTTFTBudget(ex.now(), sel)
	budget := &attemptBudget{max: 8}
	_, err = ex.tryPlanOpenOnce(attemptOpenParams{
		ctx:         ctx,
		bus:         ex.Bus,
		traceID:     "persist-fail",
		aLegID:      aLeg.ALegID,
		baseline:    lipapi.Call{Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		sel:         sel,
		session:     &routing.SessionRoutingState{},
		excluded:    map[string]struct{}{},
		rng:         routing.NewSeededRng(1),
		budget:      budget,
		ttft:        &ttft,
		interleaved: interleavedstate.State{},
	})
	if !errors.Is(err, errInjectedCyclePersist) {
		t.Fatalf("want errInjectedCyclePersist, got %v", err)
	}
	if budget.used != 1 {
		t.Fatalf("attempt budget used: got %d want 1", budget.used)
	}
	if backendOpens != 1 {
		t.Fatalf("backend must open before cycle persist failure, opens=%d", backendOpens)
	}
	state, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.IsEmpty() {
		t.Fatalf("cycle must not persist when SetInterleavedState fails, got %+v", state)
	}
}
