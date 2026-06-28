package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
)

// capturingBackend records the canonical call passed to Open so attempt-open tests can
// assert that the shaped call is the one used for negotiation and backend open.
type capturingBackend struct {
	captured func(lipapi.Call)
	stream   lipapi.ManagedEventStream
}

func (b *capturingBackend) Open(ctx context.Context, call lipapi.Call, c routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	if b.captured != nil {
		b.captured(call)
	}
	if b.stream != nil {
		return b.stream, nil
	}
	return lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}

func interleavedBackend(caps lipapi.BackendCaps, capture func(lipapi.Call)) *execbackend.Backend {
	return &execbackend.Backend{
		Caps: caps,
		TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		}),
		Open: func(ctx context.Context, call lipapi.Call, c routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if capture != nil {
				capture(call)
			}
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
	}
}

func recoverableInterleavedBackend(capture func(lipapi.Call)) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
		TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		}),
		Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if capture != nil {
				capture(call)
			}
			return nil, lipapi.RecoverablePreOutputError(errors.New("temp"))
		},
	}
}

func interleavedBaseCall(selector string) *lipapi.Call {
	return &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: selector},
		Tools:      []lipapi.ToolDef{{Name: "search", Description: "search the web"}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("plan this")},
		}},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
	}
}

// TestExecutor_OpenAttempt_ShapesThinkerCallBeforeOpen proves task 5.1: a thinker candidate
// receives instructions and tool suppression after route selection and before capability
// negotiation, and the shaped call is the one passed to backend Open. The advanced cycle
// cursor is persisted at the route-selection-authoritative point.
func TestExecutor_OpenAttempt_ShapesThinkerCallBeforeOpen(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotMu sync.Mutex
	var gotCall lipapi.Call
	capture := func(c lipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"thinker-be": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				capture,
			),
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step and emit a memo."},
	}

	// [thinker]thinker-be:m builds a single-entry cycle that selects the thinker on first request.
	call := interleavedBaseCall("[thinker]thinker-be:m")
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	if len(shaped.Tools) != 0 {
		t.Fatalf("thinker call must reach backend Open with no tools, got %d", len(shaped.Tools))
	}
	if shaped.ToolChoice.Mode != "" {
		t.Fatalf("thinker call must reach backend Open with zero ToolChoice, got %q", shaped.ToolChoice.Mode)
	}
	if len(shaped.Instructions) == 0 {
		t.Fatal("thinker call must reach backend Open with thinker instructions prepended")
	}
	if !strings.Contains(textOf(shaped.Instructions[0]), "Think step by step") {
		t.Fatalf("thinker instructions not prepended: %+v", shaped.Instructions[0])
	}

	// Cycle cursor persisted after successful thinker backend open.
	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if state.Cycle.IsEmpty() {
		t.Fatal("cycle state must be persisted after thinker attempt opens")
	}
	if state.Cycle.SelectorKey == "" {
		t.Fatal("persisted cycle must carry selector key")
	}
	if len(state.Cycle.Sequence) != 1 || state.Cycle.Sequence[0].Role != interleavedstate.RoleThinker {
		t.Fatalf("persisted cycle sequence mismatch: %+v", state.Cycle.Sequence)
	}
}

// TestExecutor_OpenAttempt_InjectorCallReceivesMemoBeforeOpen proves task 5.1: an executor
// candidate receives the latest memo as planning context before capability negotiation and
// backend Open, and the updated memo reference is persisted at injection time.
func TestExecutor_OpenAttempt_InjectorCallReceivesMemoBeforeOpen(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var gotMu sync.Mutex
	var gotCall lipapi.Call
	capture := func(c lipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"exec-be": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				capture,
			),
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step."},
		MemoStore:         memoStore,
	}

	// First request: thinker-only selector creates the A-leg and persists a cycle.
	first := interleavedBaseCall("[thinker]exec-be:m")
	if _, err := ex.Execute(context.Background(), first); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	aLegID := first.Session.ALegID
	if aLegID == "" {
		t.Fatal("first execute must set A-leg id on call session")
	}

	// Seed a memo for the A-leg scope and record its reference on the A-leg interleaved state.
	memoRef, err := memoStore.Put(context.Background(), interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "plan: do the thing",
		SourceSelector:        "[thinker]exec-be:m",
		Backend:               "exec-be",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("memo put: %v", err)
	}
	if err := st.SetInterleavedState(context.Background(), aLegID, interleavedstate.State{
		Cycle:   interleavedstate.CycleState{SelectorKey: "[thinker]exec-be:m", Sequence: []interleavedstate.CycleEntry{{Key: "exec-be:m", Role: interleavedstate.RoleThinker}}, NextIndex: 0},
		MemoRef: &memoRef,
	}); err != nil {
		t.Fatalf("seed interleaved state: %v", err)
	}

	// Second request: resume the same A-leg and select the executor branch of a thinker-aware
	// weighted selector. The stored cycle key mismatches the new selector, so the planner resets
	// and picks the executor branch (first entry), which receives the memo.
	second := interleavedBaseCall("[thinker]exec-be:m^exec-be:m")
	second.Session = lipapi.SessionRef{
		AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
		ALegID:                 aLegID,
		ClientSessionID:        first.Session.ClientSessionID,
		ResumeToken:            first.Session.ResumeToken,
	}
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	if len(shaped.Instructions) == 0 {
		t.Fatal("executor call must reach backend Open with injected memo instructions")
	}
	if !strings.Contains(textOf(shaped.Instructions[0]), "plan: do the thing") {
		t.Fatalf("memo not injected into executor call: %+v", shaped.Instructions[0])
	}
	if !strings.Contains(textOf(shaped.Instructions[0]), interleavedthinking.MemoContextOpenTag) {
		t.Fatalf("injected memo must be wrapped with memo context tags: %+v", shaped.Instructions[0])
	}
	if len(shaped.Tools) != 1 {
		t.Fatalf("executor call must keep tools, got %d", len(shaped.Tools))
	}

	// Updated memo reference persisted at injection time: budget decremented and version bumped.
	postState, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch post-state: %v", err)
	}
	if postState.MemoRef == nil || postState.MemoRef.Key != memoRef.Key {
		t.Fatalf("memo reference not persisted: %+v", postState.MemoRef)
	}
	if postState.MemoRef.Version <= memoRef.Version {
		t.Fatalf("persisted memo reference version not bumped: got %d want > %d", postState.MemoRef.Version, memoRef.Version)
	}
	stored, ok, err := memoStore.Get(context.Background(), interleavedthinking.Scope(aLegID), *postState.MemoRef)
	if err != nil || !ok {
		t.Fatalf("memo lookup after injection: ok=%v err=%v", ok, err)
	}
	if stored.RegularTurnsRemaining != 1 {
		t.Fatalf("memo budget must be decremented after injection: got %d want 1", stored.RegularTurnsRemaining)
	}
	if stored.InjectedCount != 1 {
		t.Fatalf("memo injection count must be incremented: got %d want 1", stored.InjectedCount)
	}
}

// TestExecutor_OpenAttempt_ThinkerCycleCursorAdvancesAfterSuccessfulOpen proves the durable
// thinker cycle cursor is persisted only after backend Open succeeds. A recoverable pre-output
// Open failure on the first planned candidate (bad:m) does not roll back or advance persistence;
// failover to ok:m succeeds and the final NextIndex reflects the successful open.
func TestExecutor_OpenAttempt_ThinkerCycleCursorAdvancesAfterSuccessfulOpen(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotMu sync.Mutex
	var opened []string
	capture := func(backend string) func(lipapi.Call) {
		return func(lipapi.Call) {
			gotMu.Lock()
			opened = append(opened, backend)
			gotMu.Unlock()
		}
	}

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"bad": recoverableInterleavedBackend(capture("bad")),
			"ok": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				capture("ok"),
			),
			"thinker-be": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				func(lipapi.Call) {},
			),
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step."},
	}

	selector := "[thinker]thinker-be:m^bad:m^ok:m"
	wantSeq := []interleavedstate.CycleEntry{
		{Key: "bad:m", Role: interleavedstate.RoleExecutor},
		{Key: "ok:m", Role: interleavedstate.RoleExecutor},
		{Key: "thinker-be:m", Role: interleavedstate.RoleThinker},
	}
	wantKey := "thinker-be:m^bad:m^ok:m"

	call := interleavedBaseCall(selector)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute with failover: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	openedCopy := append([]string(nil), opened...)
	gotMu.Unlock()
	if len(openedCopy) != 2 || openedCopy[0] != "bad" || openedCopy[1] != "ok" {
		t.Fatalf("open order: got %+v want [bad ok]", openedCopy)
	}

	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if state.Cycle.SelectorKey != wantKey {
		t.Fatalf("selector key: got %q want %q", state.Cycle.SelectorKey, wantKey)
	}
	if len(state.Cycle.Sequence) != len(wantSeq) {
		t.Fatalf("sequence len: got %d want %d", len(state.Cycle.Sequence), len(wantSeq))
	}
	for i, want := range wantSeq {
		got := state.Cycle.Sequence[i]
		if got.Key != want.Key || got.Role != want.Role {
			t.Fatalf("sequence[%d]: got %+v want %+v", i, got, want)
		}
	}
	// Cycle cursor persisted only after successful backend open; bad excluded on recoverable
	// failure without advancing; ok consumed on failover retry before its Open succeeded.
	if state.Cycle.NextIndex != 2 {
		t.Fatalf("NextIndex: got %d want 2 (ok planning iteration consumed after successful open)", state.Cycle.NextIndex)
	}
}

func TestExecutor_OpenAttempt_MemoCommitWaitsForSuccessfulOpen(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	var gotMu sync.Mutex
	var opened []string
	var gotCall lipapi.Call
	capture := func(backend string) func(lipapi.Call) {
		return func(call lipapi.Call) {
			gotMu.Lock()
			opened = append(opened, backend)
			gotCall = call
			gotMu.Unlock()
		}
	}

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"bad": recoverableInterleavedBackend(nil),
			"ok": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				capture("ok"),
			),
			"thinker-be": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				func(lipapi.Call) {},
			),
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step."},
		MemoStore:         memoStore,
	}

	call := interleavedBaseCall("ok:m")
	firstStream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("create A-leg: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("collect first: %v", err)
	}
	aLegID := call.Session.ALegID
	memoRef, err := memoStore.Put(context.Background(), interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "memo survives failed open",
		RegularTurnsRemaining: 1,
	})
	if err != nil {
		t.Fatalf("memo put: %v", err)
	}
	if err := st.SetInterleavedState(context.Background(), aLegID, interleavedstate.State{MemoRef: &memoRef}); err != nil {
		t.Fatalf("seed interleaved state: %v", err)
	}
	gotMu.Lock()
	opened = nil
	gotCall = lipapi.Call{}
	gotMu.Unlock()

	second := interleavedBaseCall("[thinker]thinker-be:m^bad:m^ok:m")
	resumeInterleavedCall(call, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("execute with failover: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	openedCopy := append([]string(nil), opened...)
	shaped := gotCall
	gotMu.Unlock()
	if len(openedCopy) != 1 || openedCopy[0] != "ok" {
		t.Fatalf("expected only successful backend to open, got %+v", openedCopy)
	}
	if len(shaped.Instructions) == 0 || !strings.Contains(textOf(shaped.Instructions[0]), "memo survives failed open") {
		t.Fatalf("successful backend must receive memo after failed candidate, got %+v", shaped.Instructions)
	}
	state, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	stored, ok, err := memoStore.Get(context.Background(), interleavedthinking.Scope(aLegID), *state.MemoRef)
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if stored.RegularTurnsRemaining != 0 || stored.InjectedCount != 1 {
		t.Fatalf("memo must be committed exactly once by successful open, got budget=%d injected=%d", stored.RegularTurnsRemaining, stored.InjectedCount)
	}
}

// TestExecutor_OpenAttempt_NonThinkerSelectorInert proves task 5.1: a non-thinker selector
// with interleaved thinking configured does not mutate the call (Req 3.4) and does not
// persist any interleaved state (Req 10.2).
func TestExecutor_OpenAttempt_NonThinkerSelectorInert(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var gotMu sync.Mutex
	var gotCall lipapi.Call
	capture := func(c lipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"stub": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				capture,
			),
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step."},
		MemoStore:         memoStore,
	}

	call := interleavedBaseCall("stub:m")
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	if len(shaped.Instructions) != 0 {
		t.Fatalf("non-thinker call must not receive thinker instructions, got %d", len(shaped.Instructions))
	}
	if len(shaped.Tools) != 1 {
		t.Fatalf("non-thinker call must keep tools, got %d", len(shaped.Tools))
	}
	if shaped.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("non-thinker call must keep tool choice, got %q", shaped.ToolChoice.Mode)
	}

	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if !state.IsEmpty() {
		t.Fatalf("non-thinker selector must not persist interleaved state, got %+v", state)
	}
}

// TestExecutor_OpenAttempt_DisabledConfigInert proves Req 3.5/10.2: with no interleaved
// config and no memo store, the attempt-open path is identical to a deployment without the
// feature even when the selector contains a [thinker] branch (cycle still advances but no
// shaping or memo state is applied).
func TestExecutor_OpenAttempt_DisabledConfigInertForTools(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var gotMu sync.Mutex
	var gotCall lipapi.Call
	capture := func(c lipapi.Call) {
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"thinker-be": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				capture,
			),
		},
	}

	call := interleavedBaseCall("[thinker]thinker-be:m")
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	// Disabled config: interleavedEnabled is false, so no shaping, no persistence.
	if len(shaped.Tools) != 1 {
		t.Fatalf("disabled config must not suppress tools, got %d", len(shaped.Tools))
	}
	if len(shaped.Instructions) != 0 {
		t.Fatalf("disabled config must not prepend instructions, got %d", len(shaped.Instructions))
	}
	state, err := st.FetchInterleavedState(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if !state.IsEmpty() {
		t.Fatalf("disabled config must not persist interleaved state, got %+v", state)
	}
}

func textOf(m lipapi.Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Kind == lipapi.PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

type oaRtx struct {
	runs    *atomic.Int32
	order   *[]string
	orderMu *sync.Mutex
	marker  string
}

func (r oaRtx) ID() string                        { return "oa-rtx" }
func (r oaRtx) Order() int                        { return 10 }
func (r oaRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (r oaRtx) Handle(_ context.Context, call *lipapi.Call, _ request.RequestMeta, _ request.Services) error {
	if r.runs != nil {
		r.runs.Add(1)
	}
	if r.order != nil && r.orderMu != nil {
		r.orderMu.Lock()
		*r.order = append(*r.order, "rtx")
		r.orderMu.Unlock()
	}
	if r.marker != "" && len(call.Messages) > 0 && len(call.Messages[0].Parts) > 0 {
		call.Messages[0].Parts[0].Text = r.marker + call.Messages[0].Parts[0].Text
	}
	return nil
}

type oaGate struct {
	runs    *atomic.Int32
	order   *[]string
	orderMu *sync.Mutex
}

func (g oaGate) ID() string                        { return "oa-gate" }
func (g oaGate) Order() int                        { return 0 }
func (g oaGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (g oaGate) Handle(_ context.Context, _ completion.Meta, _ completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	if g.runs != nil {
		g.runs.Add(1)
	}
	if g.order != nil && g.orderMu != nil {
		g.orderMu.Lock()
		*g.order = append(*g.order, "gate")
		g.orderMu.Unlock()
	}
	return completion.PassOriginalOutcome(), nil
}

func TestExecutor_OpenAttempt_InterleavedShapingRunsAfterTransformsBeforeCompletionGates(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var rtxRuns atomic.Int32
	var gateRuns atomic.Int32
	var orderMu sync.Mutex
	var stageOrder []string

	var gotMu sync.Mutex
	var gotCall lipapi.Call
	capture := func(c lipapi.Call) {
		orderMu.Lock()
		stageOrder = append(stageOrder, "open")
		orderMu.Unlock()
		gotMu.Lock()
		gotCall = c
		gotMu.Unlock()
	}

	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: voidWorkspaceResolver{},
		RequestTransforms: []request.Transform{oaRtx{
			runs:    &rtxRuns,
			order:   &stageOrder,
			orderMu: &orderMu,
			marker:  "rtx:",
		}},
		CompletionGates: []completion.Gate{oaGate{
			runs:    &gateRuns,
			order:   &stageOrder,
			orderMu: &orderMu,
		}},
	})

	ex := &runtime.Executor{
		Store:           st,
		Bus:             bus,
		RuntimeSnapshot: snap,
		Rand:            routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"thinker-be": *interleavedBackend(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				capture,
			),
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "Think step by step and emit a memo."},
	}

	call := interleavedBaseCall("[thinker]thinker-be:m")
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if rtxRuns.Load() != 1 {
		t.Fatalf("request transform runs: got %d want 1", rtxRuns.Load())
	}
	if gateRuns.Load() != 1 {
		t.Fatalf("completion gate runs: got %d want 1", gateRuns.Load())
	}

	orderMu.Lock()
	gotOrder := append([]string(nil), stageOrder...)
	orderMu.Unlock()
	if len(gotOrder) != 3 || gotOrder[0] != "rtx" || gotOrder[1] != "open" || gotOrder[2] != "gate" {
		t.Fatalf("stage order: got %v want [rtx open gate]", gotOrder)
	}

	gotMu.Lock()
	shaped := gotCall
	gotMu.Unlock()

	if got := textOf(shaped.Messages[0]); got != "rtx:plan this" {
		t.Fatalf("backend open must see request-transform mutation, got user text %q", got)
	}
	if len(shaped.Tools) != 0 {
		t.Fatalf("thinker shaping must suppress tools after transforms, got %d", len(shaped.Tools))
	}
	if shaped.ToolChoice.Mode != "" {
		t.Fatalf("thinker shaping must clear tool choice after transforms, got %q", shaped.ToolChoice.Mode)
	}
	if len(shaped.Instructions) == 0 || !strings.Contains(textOf(shaped.Instructions[0]), "Think step by step") {
		t.Fatalf("thinker shaping must prepend instructions after transforms, got %+v", shaped.Instructions)
	}
}
