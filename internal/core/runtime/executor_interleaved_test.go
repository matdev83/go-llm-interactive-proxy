package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type interleavedOpenRecord struct {
	backend string
	role    interleavedstate.Role
	call    lipapi.Call
}

// TestExecutor_HiddenInterleavedEndToEnd proves task 8.1: one composed hidden-interleaved
// continuation exercises selector parse, thinker selection, memo capture, persisted state,
// executor continuation selection, and final client stream output; skipping any phase fails.
func TestExecutor_HiddenInterleavedEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		selector    = "[thinker]thinker-be:m^exec-be:m"
		selectorKey = "thinker-be:m^exec-be:m"
		memoBody    = "e2e hidden plan"
		execAnswer  = "e2e executor answer"
	)

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var opensMu sync.Mutex
	var turnOpens []interleavedOpenRecord
	recordOpen := func(backend string, call lipapi.Call) {
		opensMu.Lock()
		turnOpens = append(turnOpens, interleavedOpenRecord{backend: backend, call: call})
		opensMu.Unlock()
	}

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, func(c lipapi.Call) {
			recordOpen("exec-be", c)
		}, func() lipapi.ManagedEventStream {
			return executorTextStream(execAnswer)
		}),
		"thinker-be": *interleavedBackendWithStream(caps, func(c lipapi.Call) {
			recordOpen("thinker-be", c)
		}, func() lipapi.ManagedEventStream {
			return thinkerMemoStream(memoBody)
		}),
	}

	ex := &runtime.Executor{
		Store:    st,
		Bus:      hooks.New(hooks.Config{}),
		Rand:     routing.NewSeededRng(2),
		Backends: backends,
		InterleavedConfig: interleavedthinking.ShapeConfig{
			Instructions:          "Think step by step.",
			StreamToClient:        "hidden",
			MaxMemoBytes:          4096,
			RegularTurnsRemaining: 2,
		},
		MemoStore: memoStore,
	}

	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	aLegID := first.Session.ALegID
	if aLegID == "" {
		t.Fatal("first execute must set A-leg id")
	}

	preState, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch pre-state: %v", err)
	}
	if preState.Cycle.SelectorKey != selectorKey {
		t.Fatalf("selector parse: SelectorKey got %q want %q", preState.Cycle.SelectorKey, selectorKey)
	}
	if preState.Cycle.IsEmpty() {
		t.Fatal("selector parse: cycle state must be persisted after first turn")
	}
	if len(preState.Cycle.Sequence) != 2 {
		t.Fatalf("selector parse: cycle sequence len got %d want 2: %+v", len(preState.Cycle.Sequence), preState.Cycle.Sequence)
	}
	if preState.Cycle.Sequence[0].Role != interleavedstate.RoleExecutor || preState.Cycle.Sequence[0].Key != "exec-be:m" {
		t.Fatalf("selector parse: first cycle entry got %+v want exec-be:m executor", preState.Cycle.Sequence[0])
	}
	if preState.Cycle.Sequence[1].Role != interleavedstate.RoleThinker || preState.Cycle.Sequence[1].Key != "thinker-be:m" {
		t.Fatalf("selector parse: second cycle entry got %+v want thinker-be:m thinker", preState.Cycle.Sequence[1])
	}

	opensMu.Lock()
	turnOpens = nil
	opensMu.Unlock()

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	collected, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("second collect: %v", err)
	}

	opensMu.Lock()
	secondTurnOpens := append([]interleavedOpenRecord(nil), turnOpens...)
	opensMu.Unlock()

	if len(secondTurnOpens) < 2 {
		t.Fatalf("continuation: want thinker then executor opens, got %d: %+v", len(secondTurnOpens), secondTurnOpens)
	}
	if secondTurnOpens[0].backend != "thinker-be" {
		t.Fatalf("thinker selection: first open got %q want thinker-be", secondTurnOpens[0].backend)
	}
	thinkerCall := secondTurnOpens[0].call
	if len(thinkerCall.Tools) != 0 {
		t.Fatalf("thinker selection: shaped call must suppress tools, got %d", len(thinkerCall.Tools))
	}
	if !strings.Contains(textOf(thinkerCall.Instructions[0]), "Think step by step") {
		t.Fatalf("thinker selection: instructions not shaped: %+v", thinkerCall.Instructions)
	}

	var continuationExec *interleavedOpenRecord
	for _, open := range secondTurnOpens[1:] {
		if open.backend == "exec-be" {
			continuationExec = &open
			break
		}
	}
	if continuationExec == nil {
		t.Fatal("executor continuation: exec-be must open after thinker capture")
	}
	if len(continuationExec.call.Tools) != 1 {
		t.Fatalf("executor continuation: must keep tools, got %d", len(continuationExec.call.Tools))
	}
	injected := textOf(continuationExec.call.Instructions[0])
	if !strings.Contains(injected, memoBody) {
		t.Fatalf("executor continuation: memo not injected: %q", injected)
	}
	if !strings.Contains(injected, interleavedthinking.MemoContextOpenTag) {
		t.Fatalf("executor continuation: memo context tags missing: %q", injected)
	}

	if got := collected.Reasoning.String(); got != "" {
		t.Fatalf("stream output: hidden mode must not surface reasoning, got %q", got)
	}
	if got := collected.Text.String(); got != execAnswer {
		t.Fatalf("stream output: text got %q want %q", got, execAnswer)
	}
	if strings.Contains(collected.Text.String(), interleavedthinking.MemoOpenTag) {
		t.Fatal("stream output: memo wrapper must not reach client")
	}

	postState, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("stored state: fetch failed: %v", err)
	}
	if postState.MemoRef == nil {
		t.Fatal("stored state: memo reference must persist after hidden capture")
	}
	if postState.Cycle.SelectorKey != selectorKey {
		t.Fatalf("stored state: SelectorKey got %q want %q", postState.Cycle.SelectorKey, selectorKey)
	}
	if postState.Cycle.IsEmpty() {
		t.Fatal("stored state: cycle must persist through continuation")
	}

	stored, ok, err := memoStore.Get(context.Background(), interleavedthinking.Scope(aLegID), *postState.MemoRef)
	if err != nil || !ok {
		t.Fatalf("memo capture: lookup ok=%v err=%v", ok, err)
	}
	if stored.Memo != memoBody {
		t.Fatalf("memo capture: got %q want %q", stored.Memo, memoBody)
	}
	if stored.ExtractionSource != interleavedthinking.ExtractionSourceBlock {
		t.Fatalf("memo capture: extraction source got %q want block", stored.ExtractionSource)
	}
	if stored.VisibleToClient {
		t.Fatal("memo capture: hidden mode must not mark memo VisibleToClient")
	}

	attempts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	var thinkerAttempts, execAttempts int
	for _, att := range attempts {
		switch att.BackendID {
		case "thinker-be":
			thinkerAttempts++
		case "exec-be":
			execAttempts++
		}
	}
	if thinkerAttempts != 1 {
		t.Fatalf("lineage: thinker attempts got %d want 1", thinkerAttempts)
	}
	if execAttempts < 2 {
		t.Fatalf("lineage: executor attempts got %d want at least 2", execAttempts)
	}

	var suppressedThinker bool
	for _, entry := range postState.Cycle.Sequence {
		if entry.Role == interleavedstate.RoleThinker {
			suppressedThinker = true
		}
	}
	if !suppressedThinker {
		t.Fatalf("stored state: cycle must retain thinker entry for suppression semantics: %+v", postState.Cycle.Sequence)
	}
}

// TestExecutor_VisibleInterleavedEndToEnd proves task 8.2: one composed visible-interleaved
// continuation exercises visible thinker output, memo sanitization, memo storage, executor
// continuation, and final stream termination; wrapper tags must not surface and executor
// output must follow thinker reasoning.
func TestExecutor_VisibleInterleavedEndToEnd(t *testing.T) {
	t.Parallel()

	const (
		selector    = "[thinker]thinker-be:m^exec-be:m"
		selectorKey = "thinker-be:m^exec-be:m"
		memoBody    = "e2e visible plan"
		execAnswer  = "e2e executor answer"
	)

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var opensMu sync.Mutex
	var turnOpens []interleavedOpenRecord
	recordOpen := func(backend string, call lipapi.Call) {
		opensMu.Lock()
		turnOpens = append(turnOpens, interleavedOpenRecord{backend: backend, call: call})
		opensMu.Unlock()
	}

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, func(c lipapi.Call) {
			recordOpen("exec-be", c)
		}, func() lipapi.ManagedEventStream {
			return executorTextStream(execAnswer)
		}),
		"thinker-be": *interleavedBackendWithStream(caps, func(c lipapi.Call) {
			recordOpen("thinker-be", c)
		}, func() lipapi.ManagedEventStream {
			return thinkerMemoStream(memoBody)
		}),
	}

	ex := &runtime.Executor{
		Store:    st,
		Bus:      hooks.New(hooks.Config{}),
		Rand:     routing.NewSeededRng(2),
		Backends: backends,
		InterleavedConfig: interleavedthinking.ShapeConfig{
			Instructions:          "Think step by step.",
			StreamToClient:        "visible",
			MaxMemoBytes:          4096,
			RegularTurnsRemaining: 2,
		},
		MemoStore: memoStore,
	}

	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	aLegID := first.Session.ALegID
	if aLegID == "" {
		t.Fatal("first execute must set A-leg id")
	}

	opensMu.Lock()
	turnOpens = nil
	opensMu.Unlock()

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	var sawReasoningBeforeExec bool
	var sawResponseFinished bool
	var gotReasoning strings.Builder
	var gotText strings.Builder
	ctx := context.Background()
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		switch ev.Kind {
		case lipapi.EventReasoningDelta:
			if gotText.Len() > 0 {
				t.Fatal("visible thinker reasoning must stream before executor text")
			}
			if strings.Contains(ev.Delta, interleavedthinking.MemoOpenTag) ||
				strings.Contains(ev.Delta, interleavedthinking.MemoCloseTag) {
				t.Fatalf("memo sanitization: wrapper tags leaked into visible reasoning: %q", ev.Delta)
			}
			gotReasoning.WriteString(ev.Delta)
			sawReasoningBeforeExec = true
		case lipapi.EventTextDelta:
			if strings.Contains(ev.Delta, interleavedthinking.MemoOpenTag) ||
				strings.Contains(ev.Delta, interleavedthinking.MemoCloseTag) {
				t.Fatalf("memo sanitization: wrapper tags leaked into executor text: %q", ev.Delta)
			}
			gotText.WriteString(ev.Delta)
		case lipapi.EventResponseFinished:
			sawResponseFinished = true
			if !sawReasoningBeforeExec {
				t.Fatal("stream termination: expected visible thinker reasoning before finish")
			}
			if gotText.String() != execAnswer {
				t.Fatalf("stream termination: text at finish got %q want %q", gotText.String(), execAnswer)
			}
		}
	}
	_ = stream.Close()

	if !sawResponseFinished {
		t.Fatal("stream termination: must emit ResponseFinished")
	}
	if got := gotReasoning.String(); got != memoBody {
		t.Fatalf("visible thinker output: got %q want %q", got, memoBody)
	}
	if got := gotText.String(); got != execAnswer {
		t.Fatalf("executor output: got %q want %q", got, execAnswer)
	}

	opensMu.Lock()
	secondTurnOpens := append([]interleavedOpenRecord(nil), turnOpens...)
	opensMu.Unlock()

	if len(secondTurnOpens) < 2 {
		t.Fatalf("continuation: want thinker then executor opens, got %d: %+v", len(secondTurnOpens), secondTurnOpens)
	}
	if secondTurnOpens[0].backend != "thinker-be" {
		t.Fatalf("thinker selection: first open got %q want thinker-be", secondTurnOpens[0].backend)
	}
	thinkerCall := secondTurnOpens[0].call
	if len(thinkerCall.Tools) != 0 {
		t.Fatalf("thinker selection: shaped call must suppress tools, got %d", len(thinkerCall.Tools))
	}

	var continuationExec *interleavedOpenRecord
	for _, open := range secondTurnOpens[1:] {
		if open.backend == "exec-be" {
			continuationExec = &open
			break
		}
	}
	if continuationExec == nil {
		t.Fatal("executor continuation: exec-be must open after visible thinker capture")
	}
	if len(continuationExec.call.Tools) != 1 {
		t.Fatalf("executor continuation: must keep tools, got %d", len(continuationExec.call.Tools))
	}
	for _, msg := range continuationExec.call.Instructions {
		txt := textOf(msg)
		if strings.Contains(txt, memoBody) || strings.Contains(txt, interleavedthinking.MemoContextOpenTag) {
			t.Fatalf("executor continuation: visible memo must not be re-injected: %q", txt)
		}
	}

	postState, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("stored state: fetch failed: %v", err)
	}
	if postState.MemoRef == nil {
		t.Fatal("memo storage: memo reference must persist after visible capture")
	}
	if postState.Cycle.SelectorKey != selectorKey {
		t.Fatalf("stored state: SelectorKey got %q want %q", postState.Cycle.SelectorKey, selectorKey)
	}

	stored, ok, err := memoStore.Get(context.Background(), interleavedthinking.Scope(aLegID), *postState.MemoRef)
	if err != nil || !ok {
		t.Fatalf("memo storage: lookup ok=%v err=%v", ok, err)
	}
	if stored.Memo != memoBody {
		t.Fatalf("memo storage: got %q want %q", stored.Memo, memoBody)
	}
	if stored.ExtractionSource != interleavedthinking.ExtractionSourceBlock {
		t.Fatalf("memo storage: extraction source got %q want block", stored.ExtractionSource)
	}
	if !stored.VisibleToClient {
		t.Fatal("memo storage: visible mode must mark memo VisibleToClient")
	}
	if stored.InjectedCount != 0 {
		t.Fatalf("memo storage: visible continuation must not inject memo, InjectedCount=%d", stored.InjectedCount)
	}

	attempts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	var thinkerAttempts, execAttempts int
	for _, att := range attempts {
		switch att.BackendID {
		case "thinker-be":
			thinkerAttempts++
		case "exec-be":
			execAttempts++
		}
	}
	if thinkerAttempts != 1 {
		t.Fatalf("lineage: thinker attempts got %d want 1", thinkerAttempts)
	}
	if execAttempts < 2 {
		t.Fatalf("lineage: executor attempts got %d want at least 2", execAttempts)
	}
}

func resumeInterleavedSession(origin, prev, next *lipapi.Call) {
	resumeInterleavedCall(prev, next)
	if origin != nil && strings.TrimSpace(next.Session.ResumeToken) == "" {
		next.Session.ResumeToken = origin.Session.ResumeToken
	}
}

func TestExecutor_VisibleMemoReinjectsOnLaterNormalExecutorTurn(t *testing.T) {
	t.Parallel()

	const (
		selector = "[thinker]thinker-be:m^[weight=2]exec-be:m"
		memoBody = "visible memo for later inject"
	)

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var opensMu sync.Mutex
	var turnOpens []interleavedOpenRecord
	recordOpen := func(backend string, call lipapi.Call, cand routing.AttemptCandidate) {
		opensMu.Lock()
		turnOpens = append(turnOpens, interleavedOpenRecord{backend: backend, role: cand.InterleavedRole, call: call})
		opensMu.Unlock()
	}

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	openWithCapture := func(backend string, newStream func() lipapi.ManagedEventStream) execbackend.Backend {
		return execbackend.Backend{
			Caps: caps,
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				recordOpen(backend, call, cand)
				return newStream(), nil
			},
		}
	}
	backends := map[string]execbackend.Backend{
		"exec-be": openWithCapture("exec-be", func() lipapi.ManagedEventStream {
			return executorTextStream("exec answer")
		}),
		"thinker-be": openWithCapture("thinker-be", func() lipapi.ManagedEventStream {
			return thinkerMemoStream(memoBody)
		}),
	}

	ex := &runtime.Executor{
		Store:    st,
		Bus:      hooks.New(hooks.Config{}),
		Rand:     routing.NewSeededRng(2),
		Backends: backends,
		InterleavedConfig: interleavedthinking.ShapeConfig{
			Instructions:          "Think step by step.",
			StreamToClient:        "visible",
			MaxMemoBytes:          4096,
			RegularTurnsRemaining: 2,
		},
		MemoStore: memoStore,
	}

	collectTurn := func(origin, prev, call *lipapi.Call) {
		t.Helper()
		if prev != nil {
			resumeInterleavedSession(origin, prev, call)
		}
		stream, err := ex.Execute(principalCtx("visible-memo-resume"), call)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if _, err := lipapi.Collect(context.Background(), stream); err != nil {
			t.Fatalf("collect: %v", err)
		}
	}

	turn1 := interleavedBaseCall(selector)
	collectTurn(nil, nil, turn1)
	aLegID := turn1.Session.ALegID

	preThinker, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch pre-thinker cycle: %v", err)
	}
	preThinker.Cycle.NextIndex = 2
	if err := st.SetInterleavedState(context.Background(), aLegID, preThinker); err != nil {
		t.Fatalf("seed thinker cycle cursor: %v", err)
	}

	opensMu.Lock()
	turnOpens = nil
	opensMu.Unlock()

	turn2 := interleavedBaseCall(selector)
	collectTurn(turn1, turn1, turn2)

	opensMu.Lock()
	visibleTurnOpens := append([]interleavedOpenRecord(nil), turnOpens...)
	turnOpens = nil
	opensMu.Unlock()

	if len(visibleTurnOpens) < 2 {
		t.Fatalf("visible turn: want thinker then continuation executor opens, got %d: %+v", len(visibleTurnOpens), visibleTurnOpens)
	}
	if visibleTurnOpens[0].backend != "thinker-be" {
		t.Fatalf("visible turn: first open got %q want thinker-be", visibleTurnOpens[0].backend)
	}

	var continuationExec *interleavedOpenRecord
	for _, open := range visibleTurnOpens[1:] {
		if open.backend == "exec-be" {
			continuationExec = &open
			break
		}
	}
	if continuationExec == nil {
		t.Fatal("visible turn: continuation executor open missing")
	}
	for _, msg := range continuationExec.call.Instructions {
		txt := textOf(msg)
		if strings.Contains(txt, interleavedthinking.MemoContextOpenTag) || strings.Contains(txt, memoBody) {
			t.Fatalf("immediate continuation executor must not inject visible memo: %q", txt)
		}
	}

	turn3 := interleavedBaseCall(selector)
	collectTurn(turn1, turn2, turn3)

	opensMu.Lock()
	laterTurnOpens := append([]interleavedOpenRecord(nil), turnOpens...)
	opensMu.Unlock()

	var laterExec *interleavedOpenRecord
	for _, open := range laterTurnOpens {
		if open.backend == "exec-be" {
			laterExec = &open
			break
		}
	}
	if laterExec == nil {
		t.Fatal("later normal turn: executor open missing")
	}
	if len(laterExec.call.Instructions) == 0 {
		t.Fatal("later normal executor must inject memo instructions")
	}
	injected := textOf(laterExec.call.Instructions[0])
	if !strings.Contains(injected, interleavedthinking.MemoContextOpenTag) {
		t.Fatalf("later normal executor must inject memo context tag: %q", injected)
	}
	if !strings.Contains(injected, memoBody) {
		t.Fatalf("later normal executor must inject memo body: %q", injected)
	}

	stored, ok, err := memoStore.Get(context.Background(), interleavedthinking.Scope(aLegID), *mustMemoRef(t, st, aLegID))
	if err != nil || !ok {
		t.Fatalf("memo lookup: ok=%v err=%v", ok, err)
	}
	if !stored.VisibleToClient {
		t.Fatal("memo must remain marked VisibleToClient")
	}
	if stored.InjectedCount != 1 {
		t.Fatalf("later normal turn must consume injection budget once, InjectedCount=%d", stored.InjectedCount)
	}
}

func mustMemoRef(t *testing.T, st *b2bua.MemoryStore, aLegID string) *interleavedstate.MemoRef {
	t.Helper()
	state, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if state.MemoRef == nil {
		t.Fatal("memo reference missing")
	}
	return state.MemoRef
}
