package runtime_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type voidWorkspaceResolver struct{}

func (voidWorkspaceResolver) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{}, nil
}

func interleavedSecureFingerprintKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func interleavedSecureExecutor(t *testing.T, backends map[string]execbackend.Backend) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := interleavedSecureFingerprintKey()
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: fk,
		StoreDurable:   true,
		ResumeWindow:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWorkspaceResolver{}}),
	})
	ex := &runtime.Executor{
		Store:                   st,
		Bus:                     hooks.New(hooks.Config{}),
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		SessionDenialMapper:     lipapidenial.MapToSessionDenial,
		SyntheticLocalPrincipal: false,
		Rand:                    routing.NewSeededRng(2),
		Backends:                backends,
		Now:                     func() time.Time { return time.Unix(3000, 0) },
		InterleavedConfig: interleavedthinking.ShapeConfig{
			Instructions:          "Think step by step.",
			StreamToClient:        "hidden",
			MaxMemoBytes:          4096,
			RegularTurnsRemaining: 2,
		},
		MemoStore: interleavedthinking.NewMemoStore(4096),
	}
	return ex, st
}

func principalCtx(id string) context.Context {
	return execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: id})
}

// TestExecutor_InterleavedSecureSession_AuthorizedResumePreservesMemo proves task 8.3:
// an authorized secure-session resume restores interleaved state and applies stored memo
// on the executor continuation path.
func TestExecutor_InterleavedSecureSession_AuthorizedResumePreservesMemo(t *testing.T) {
	t.Parallel()

	const (
		selector = "[thinker]thinker-be:m^exec-be:m"
		memoBody = "authorized-resume memo"
	)

	var execCapture lipapi.Call
	var captureMu sync.Mutex
	captureExec := func(c lipapi.Call) {
		captureMu.Lock()
		execCapture = c
		captureMu.Unlock()
	}
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, captureExec, func() lipapi.ManagedEventStream {
			return executorTextStream("exec answer")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() lipapi.ManagedEventStream {
			return thinkerMemoStream(memoBody)
		}),
	}
	ex, st := interleavedSecureExecutor(t, backends)
	ownerCtx := principalCtx("owner-authorized")

	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(ownerCtx, first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := interleavedBaseCall(selector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(ownerCtx, second)
	if err != nil {
		t.Fatalf("authorized resume execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("second collect: %v", err)
	}

	captureMu.Lock()
	captured := execCapture
	captureMu.Unlock()
	if len(captured.Instructions) == 0 || !strings.Contains(textOf(captured.Instructions[0]), memoBody) {
		t.Fatalf("authorized resume must inject stored memo, got instructions %+v", execCapture.Instructions)
	}
	postState, err := st.FetchInterleavedState(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch post-state: %v", err)
	}
	if postState.MemoRef == nil {
		t.Fatal("authorized resume must preserve memo reference on A-leg")
	}
}

// TestExecutor_InterleavedSecureSession_DeniedResumeDoesNotApplyMemo proves task 8.3:
// a denied secure-session resume must not open backends or apply stored interleaved memo state.
func TestExecutor_InterleavedSecureSession_DeniedResumeDoesNotApplyMemo(t *testing.T) {
	t.Parallel()

	const (
		selector = "[thinker]thinker-be:m^exec-be:m"
		memoBody = "denied-resume secret memo"
	)

	var opens atomic.Int32
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	recordOpen := func(_ lipapi.Call) { opens.Add(1) }
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, recordOpen, func() lipapi.ManagedEventStream {
			return executorTextStream("exec answer")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, recordOpen, func() lipapi.ManagedEventStream {
			return thinkerMemoStream(memoBody)
		}),
	}
	ex, st := interleavedSecureExecutor(t, backends)
	ownerCtx := principalCtx("owner-denied")

	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(ownerCtx, first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	secondOwner := interleavedBaseCall(selector)
	resumeInterleavedCall(first, secondOwner)
	ownerStream, err := ex.Execute(ownerCtx, secondOwner)
	if err != nil {
		t.Fatalf("owner second execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), ownerStream); err != nil {
		t.Fatalf("owner second collect: %v", err)
	}
	preOpens := opens.Load()

	attack := interleavedBaseCall(selector)
	resumeInterleavedCall(first, attack)
	if _, err := ex.Execute(principalCtx("attacker"), attack); err == nil {
		t.Fatal("expected denied resume for wrong principal")
	} else if !lipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial, got %T %v", err, err)
	}
	if opens.Load() != preOpens {
		t.Fatalf("denied resume must not open backends: before=%d after=%d", preOpens, opens.Load())
	}

	postState, err := st.FetchInterleavedState(context.Background(), first.Session.ALegID)
	if err != nil {
		t.Fatalf("fetch owner state: %v", err)
	}
	if postState.MemoRef == nil {
		t.Fatal("denied turn must not clear owner's stored memo reference")
	}
	stored, ok, err := ex.MemoStore.Get(context.Background(), interleavedthinking.Scope(first.Session.ALegID), *postState.MemoRef)
	if err != nil || !ok {
		t.Fatalf("owner memo lookup: ok=%v err=%v", ok, err)
	}
	if stored.Memo != memoBody {
		t.Fatalf("owner memo must remain intact: got %q want %q", stored.Memo, memoBody)
	}
}

// TestExecutor_InterleavedSessionIsolation_UnrelatedSessionDoesNotInjectMemo proves task 8.3:
// memo state captured on one A-leg must not be injected into an unrelated session's attempts.
func TestExecutor_InterleavedSessionIsolation_UnrelatedSessionDoesNotInjectMemo(t *testing.T) {
	t.Parallel()

	const (
		selector = "[thinker]thinker-be:m^exec-be:m"
		memoA    = "iso-session-A-only"
		memoB    = "iso-session-B-only"
	)

	var opensMu sync.Mutex
	var sessionBCalls []lipapi.Call
	var sessionBStarted atomic.Bool
	recordCalls := func(c lipapi.Call) {
		if !sessionBStarted.Load() {
			return
		}
		opensMu.Lock()
		sessionBCalls = append(sessionBCalls, c)
		opensMu.Unlock()
	}

	thinkerBody := memoA
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, recordCalls, func() lipapi.ManagedEventStream {
			return executorTextStream("exec answer")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, recordCalls, func() lipapi.ManagedEventStream {
			return thinkerMemoStream(thinkerBody)
		}),
	}
	ex, _ := interleavedExecutor(t, backends)

	firstA := interleavedBaseCall(selector)
	streamA1, err := ex.Execute(context.Background(), firstA)
	if err != nil {
		t.Fatalf("session A first: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), streamA1); err != nil {
		t.Fatalf("session A first collect: %v", err)
	}
	secondA := interleavedBaseCall(selector)
	resumeInterleavedCall(firstA, secondA)
	streamA2, err := ex.Execute(context.Background(), secondA)
	if err != nil {
		t.Fatalf("session A second: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), streamA2); err != nil {
		t.Fatalf("session A second collect: %v", err)
	}

	thinkerBody = memoB
	sessionBStarted.Store(true)
	firstB := interleavedBaseCall(selector)
	streamB1, err := ex.Execute(context.Background(), firstB)
	if err != nil {
		t.Fatalf("session B first: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), streamB1); err != nil {
		t.Fatalf("session B first collect: %v", err)
	}
	if firstB.Session.ALegID == firstA.Session.ALegID {
		t.Fatal("unrelated session must receive a distinct A-leg")
	}

	secondB := interleavedBaseCall(selector)
	resumeInterleavedCall(firstB, secondB)
	streamB2, err := ex.Execute(context.Background(), secondB)
	if err != nil {
		t.Fatalf("session B second: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), streamB2); err != nil {
		t.Fatalf("session B second collect: %v", err)
	}

	opensMu.Lock()
	calls := append([]lipapi.Call(nil), sessionBCalls...)
	opensMu.Unlock()
	for i, call := range calls {
		for _, msg := range call.Instructions {
			txt := textOf(msg)
			if strings.Contains(txt, memoA) {
				t.Fatalf("session B call[%d] leaked session A memo: %q", i, txt)
			}
		}
	}
	injectedB := false
	for _, call := range calls {
		for _, msg := range call.Instructions {
			if strings.Contains(textOf(msg), memoB) {
				injectedB = true
			}
		}
	}
	if !injectedB {
		t.Fatal("session B must inject its own memo on continuation, not session A's")
	}
}

// TestExecutor_InterleavedStaleSelectorResetPreservesMemo proves task 8.3:
// when the selector changes, stale cycle state resets while stored memo state remains usable.
func TestExecutor_InterleavedStaleSelectorResetPreservesMemo(t *testing.T) {
	t.Parallel()

	const (
		oldSelector = "[thinker]other-be:m^exec-be:m"
		newSelector = "[thinker]exec-be:m^exec-be:m"
		newKey      = "exec-be:m^exec-be:m"
		memoBody    = "stale-selector memo"
	)

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)

	var gotCall lipapi.Call
	capture := func(c lipapi.Call) { gotCall = c }

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"exec-be": *interleavedBackendWithStream(caps, capture, func() lipapi.ManagedEventStream {
				return executorTextStream("exec answer")
			}),
		},
		InterleavedConfig: interleavedthinking.ShapeConfig{
			Instructions:          "Think step by step.",
			RegularTurnsRemaining: 2,
		},
		MemoStore: memoStore,
	}

	first := interleavedBaseCall(oldSelector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("seed execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("seed collect: %v", err)
	}
	aLegID := first.Session.ALegID

	memoRef, err := memoStore.Put(context.Background(), interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  memoBody,
		SourceSelector:        oldSelector,
		Backend:               "exec-be",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleCycle := interleavedstate.CycleState{
		SelectorKey: "other-be:m^exec-be:m",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "other-be:m", Role: interleavedstate.RoleExecutor},
			{Key: "exec-be:m", Role: interleavedstate.RoleThinker},
		},
		NextIndex: 1,
	}
	if err := st.SetInterleavedState(context.Background(), aLegID, interleavedstate.State{
		Cycle:   staleCycle,
		MemoRef: &memoRef,
	}); err != nil {
		t.Fatal(err)
	}

	second := interleavedBaseCall(newSelector)
	resumeInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("selector-change execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if !strings.Contains(textOf(gotCall.Instructions[0]), memoBody) {
		t.Fatalf("stale cycle reset must still inject memo: %+v", gotCall.Instructions)
	}

	postState, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch post-state: %v", err)
	}
	if postState.MemoRef == nil || postState.MemoRef.Key != memoRef.Key {
		t.Fatalf("memo reference corrupted: %+v", postState.MemoRef)
	}
	if postState.Cycle.SelectorKey != newKey {
		t.Fatalf("cycle must reset to new selector key: got %q want %q", postState.Cycle.SelectorKey, newKey)
	}
	if postState.Cycle.MatchesSelector(staleCycle.SelectorKey) {
		t.Fatal("stale selector key must not remain authoritative after reset")
	}
	if postState.Cycle.NextIndex == 0 {
		t.Fatal("cycle cursor must advance after executor pick")
	}
}
