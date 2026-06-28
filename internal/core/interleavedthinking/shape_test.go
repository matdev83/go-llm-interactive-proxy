package interleavedthinking

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func baseCall() lipapi.Call {
	return lipapi.Call{
		ID: "req-1",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("plan this")}},
		},
		Tools: []lipapi.ToolDef{
			{Name: "search", Description: "search the web"},
		},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto},
	}
}

func thinkerCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "openai-responses", Model: "gpt-4o"},
		Key:             "openai-responses:gpt-4o",
		InterleavedRole: interleavedstate.RoleThinker,
		SelectorKey:     "openai-responses:gpt-4o[thinker]",
	}
}

func executorCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "openai-responses", Model: "gpt-4o-mini"},
		Key:             "openai-responses:gpt-4o-mini",
		InterleavedRole: interleavedstate.RoleExecutor,
	}
}

func noneCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "openai-responses", Model: "gpt-4o-mini"},
		Key:             "openai-responses:gpt-4o-mini",
		InterleavedRole: interleavedstate.RoleNone,
	}
}

func toolsEqual(a, b []lipapi.ToolDef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Description != b[i].Description {
			return false
		}
		if (len(a[i].Parameters) == 0) != (len(b[i].Parameters) == 0) {
			return false
		}
		if len(a[i].Parameters) > 0 && string(a[i].Parameters) != string(b[i].Parameters) {
			return false
		}
	}
	return true
}

func TestShapeCall_ThinkerPrependsInstructionsAndSuppressesTools(t *testing.T) {
	t.Parallel()
	in := baseCall()
	res, err := ShapeCall(context.Background(), ShapeInput{
		Call:      in,
		Candidate: thinkerCandidate(),
		Config:    ShapeConfig{Instructions: "Think step by step and emit a memo."},
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if err := res.Call.Validate(); err != nil {
		t.Fatalf("shaped thinker call must validate: %v", err)
	}
	if len(res.Call.Tools) != 0 {
		t.Fatalf("thinker call must have no tools, got %d", len(res.Call.Tools))
	}
	if res.Call.ToolChoice.Mode != "" || res.Call.ToolChoice.Name != "" {
		t.Fatalf("thinker call must have zero ToolChoice, got %+v", res.Call.ToolChoice)
	}
	if len(res.Call.Instructions) == 0 {
		t.Fatalf("thinker call must have prepended instructions")
	}
	first := res.Call.Instructions[0]
	if first.Role != lipapi.RoleSystem {
		t.Fatalf("prepended instruction role: got %q want %q", first.Role, lipapi.RoleSystem)
	}
	if len(first.Parts) != 1 || first.Parts[0].Kind != lipapi.PartText || first.Parts[0].Text != "Think step by step and emit a memo." {
		t.Fatalf("prepended instruction part: %+v", first.Parts)
	}
}

func TestShapeCall_ThinkerPrependsBeforeExistingInstructions(t *testing.T) {
	t.Parallel()
	in := baseCall()
	in.Instructions = []lipapi.Message{
		{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("existing system prompt")}},
	}
	res, err := ShapeCall(context.Background(), ShapeInput{
		Call:      in,
		Candidate: thinkerCandidate(),
		Config:    ShapeConfig{Instructions: "thinker plan"},
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(res.Call.Instructions) != 2 {
		t.Fatalf("want 2 instructions, got %d", len(res.Call.Instructions))
	}
	if res.Call.Instructions[0].Parts[0].Text != "thinker plan" {
		t.Fatalf("thinker instructions not first: %+v", res.Call.Instructions[0])
	}
	if res.Call.Instructions[1].Parts[0].Text != "existing system prompt" {
		t.Fatalf("existing instructions not preserved: %+v", res.Call.Instructions[1])
	}
}

func TestShapeCall_ThinkerSuppressesRequiredToolChoice(t *testing.T) {
	t.Parallel()
	in := baseCall()
	in.ToolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "search"}
	res, err := ShapeCall(context.Background(), ShapeInput{
		Call:      in,
		Candidate: thinkerCandidate(),
		Config:    ShapeConfig{Instructions: "plan"},
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if err := res.Call.Validate(); err != nil {
		t.Fatalf("shaped thinker call must validate after suppressing required tool choice: %v", err)
	}
	if res.Call.ToolChoice.Mode != "" || res.Call.ToolChoice.Name != "" {
		t.Fatalf("thinker call must have zero ToolChoice, got %+v", res.Call.ToolChoice)
	}
}

func TestShapeCall_ThinkerMissingInstructionsFails(t *testing.T) {
	t.Parallel()
	in := baseCall()
	_, err := ShapeCall(context.Background(), ShapeInput{
		Call:      in,
		Candidate: thinkerCandidate(),
		Config:    ShapeConfig{Instructions: "   "},
	})
	if !errors.Is(err, ErrThinkerInstructionsMissing) {
		t.Fatalf("want ErrThinkerInstructionsMissing, got %v", err)
	}
}

func TestShapeCall_ExecutorNotMutatedWhenNoMemo(t *testing.T) {
	t.Parallel()
	in := baseCall()
	res, err := ShapeCall(context.Background(), ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		Config:    ShapeConfig{Instructions: "plan"},
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if !toolsEqual(res.Call.Tools, in.Tools) {
		t.Fatalf("executor tools must be unchanged: got %+v want %+v", res.Call.Tools, in.Tools)
	}
	if res.Call.ToolChoice != in.ToolChoice {
		t.Fatalf("executor ToolChoice must be unchanged: got %+v want %+v", res.Call.ToolChoice, in.ToolChoice)
	}
	if len(res.Call.Instructions) != len(in.Instructions) {
		t.Fatalf("executor instructions must be unchanged: got %d want %d", len(res.Call.Instructions), len(in.Instructions))
	}
	if err := res.Call.Validate(); err != nil {
		t.Fatalf("executor call must still validate: %v", err)
	}
	if res.MemoInjected {
		t.Fatalf("MemoInjected must be false when no memo store/ref")
	}
	if res.MemoUpdate != nil {
		t.Fatalf("MemoUpdate must be nil when no injection")
	}
}

func TestShapeCall_NoneRoleNotMutated(t *testing.T) {
	t.Parallel()
	in := baseCall()
	res, err := ShapeCall(context.Background(), ShapeInput{
		Call:      in,
		Candidate: noneCandidate(),
		Config:    ShapeConfig{Instructions: "plan"},
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if !toolsEqual(res.Call.Tools, in.Tools) {
		t.Fatalf("non-thinker tools must be unchanged")
	}
	if len(res.Call.Instructions) != len(in.Instructions) {
		t.Fatalf("non-thinker instructions must be unchanged")
	}
}

func TestShapeCall_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	in := baseCall()
	in.Instructions = []lipapi.Message{
		{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("existing")}},
	}
	originalTools := len(in.Tools)
	originalInstrCount := len(in.Instructions)
	_, err := ShapeCall(context.Background(), ShapeInput{
		Call:      in,
		Candidate: thinkerCandidate(),
		Config:    ShapeConfig{Instructions: "plan"},
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(in.Tools) != originalTools {
		t.Fatalf("input Tools mutated: got %d want %d", len(in.Tools), originalTools)
	}
	if len(in.Instructions) != originalInstrCount {
		t.Fatalf("input Instructions mutated: got %d want %d", len(in.Instructions), originalInstrCount)
	}
	if in.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("input ToolChoice mutated: got %q", in.ToolChoice.Mode)
	}
}

func TestShapeCall_PreservesNullInstructionsForNonThinker(t *testing.T) {
	t.Parallel()
	in := baseCall()
	if in.Instructions != nil {
		t.Fatalf("test precondition: nil instructions")
	}
	res, err := ShapeCall(context.Background(), ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		Config:    ShapeConfig{Instructions: "plan"},
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if res.Call.Instructions != nil {
		t.Fatalf("non-thinker nil Instructions must stay nil, got %d", len(res.Call.Instructions))
	}
}

// --- Task 4.4: executor memo injection ---

// storeWithMemo seeds an in-memory memo store with one memo and returns the
// store, scope, and resolved memo ref.
func storeWithMemo(t *testing.T, state MemoState) (MemoStore, Scope, interleavedstate.MemoRef) {
	t.Helper()
	ctx := context.Background()
	store := NewMemoStore(8192)
	ref, err := store.Put(ctx, "session-1", state)
	if err != nil {
		t.Fatalf("seed memo: %v", err)
	}
	return store, Scope("session-1"), ref
}

func injectableMemoState() MemoState {
	return MemoState{
		Memo:                  "Step 1: fetch data. Step 2: summarize.",
		SourceSelector:        "openai-responses:gpt-4o[thinker]",
		Backend:               "openai-responses",
		Model:                 "gpt-4o",
		RequestID:             "req-1",
		RegularTurnsRemaining: 2,
		ExtractionSource:      ExtractionSourceBlock,
	}
}

func TestShapeCall_ExecutorInjectsMemoAsPlanningContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := injectableMemoState()
	store, scope, ref := storeWithMemo(t, state)

	in := baseCall()
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		Config:    ShapeConfig{Instructions: "plan"},
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if !res.MemoInjected {
		t.Fatal("MemoInjected must be true when a valid memo was injected")
	}
	if res.MemoUpdate == nil || res.MemoUpdate.Ref.Key != ref.Key {
		t.Fatalf("MemoUpdate must be returned for the memo ref: %+v", res.MemoUpdate)
	}
	if res.MemoUpdate.Ref.Version != ref.Version {
		t.Fatalf("MemoUpdate must carry the current ref until committed: got %d want %d", res.MemoUpdate.Ref.Version, ref.Version)
	}
	if len(res.Call.Instructions) != 1 {
		t.Fatalf("executor call must have one injected instruction, got %d", len(res.Call.Instructions))
	}
	injected := res.Call.Instructions[0]
	if injected.Role != lipapi.RoleSystem {
		t.Fatalf("injected memo role: got %q want %q", injected.Role, lipapi.RoleSystem)
	}
	if len(injected.Parts) != 1 || injected.Parts[0].Kind != lipapi.PartText {
		t.Fatalf("injected memo must be a single text part: %+v", injected.Parts)
	}
	text := injected.Parts[0].Text
	if !contains(text, MemoContextOpenTag) || !contains(text, MemoContextCloseTag) {
		t.Fatalf("injected memo must be wrapped with context tags: %q", text)
	}
	if !contains(text, state.Memo) {
		t.Fatalf("injected memo must contain memo body: %q", text)
	}
	if err := res.Call.Validate(); err != nil {
		t.Fatalf("shaped executor call must validate: %v", err)
	}

	// Budget decrement and injection count are pending until runtime commits the opened attempt.
	got, ok, err := store.Get(ctx, scope, ref)
	if err != nil || !ok {
		t.Fatalf("get after injection: ok=%v err=%v", ok, err)
	}
	if got.RegularTurnsRemaining != state.RegularTurnsRemaining {
		t.Fatalf("store budget must not decrement before commit: got %d want %d", got.RegularTurnsRemaining, state.RegularTurnsRemaining)
	}
	if got.InjectedCount != 0 {
		t.Fatalf("store InjectedCount must not increment before commit, got %d", got.InjectedCount)
	}
	if res.MemoUpdate.State.RegularTurnsRemaining != state.RegularTurnsRemaining-1 {
		t.Fatalf("pending budget must decrement by 1: got %d want %d", res.MemoUpdate.State.RegularTurnsRemaining, state.RegularTurnsRemaining-1)
	}
	if res.MemoUpdate.State.InjectedCount != 1 {
		t.Fatalf("pending InjectedCount must be 1, got %d", res.MemoUpdate.State.InjectedCount)
	}
}

func TestShapeCall_ExecutorInjectionPrependsBeforeExistingInstructions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	in.Instructions = []lipapi.Message{
		{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("system prompt")}},
	}
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(res.Call.Instructions) != 2 {
		t.Fatalf("want 2 instructions, got %d", len(res.Call.Instructions))
	}
	if !contains(res.Call.Instructions[0].Parts[0].Text, MemoContextOpenTag) {
		t.Fatalf("memo must be prepended first: %+v", res.Call.Instructions[0])
	}
	if res.Call.Instructions[1].Parts[0].Text != "system prompt" {
		t.Fatalf("existing instructions must be preserved after memo: %+v", res.Call.Instructions[1])
	}
}

func TestShapeCall_ExecutorExpiredMemoNotInjected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := injectableMemoState()
	state.RegularTurnsRemaining = 0
	store, scope, ref := storeWithMemo(t, state)

	in := baseCall()
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if res.MemoInjected {
		t.Fatal("expired memo must not be injected")
	}
	if len(res.Call.Instructions) != 0 {
		t.Fatalf("expired memo must not add instructions, got %d", len(res.Call.Instructions))
	}
	// Budget must not decrement when no injection occurred.
	got, _, err := store.Get(ctx, scope, ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RegularTurnsRemaining != 0 {
		t.Fatalf("expired budget must remain 0, got %d", got.RegularTurnsRemaining)
	}
	if got.InjectedCount != 0 {
		t.Fatalf("expired memo InjectedCount must remain 0, got %d", got.InjectedCount)
	}
}

func TestShapeCall_ExecutorVisibleMemoSuppressed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := injectableMemoState()
	state.VisibleToClient = true
	store, scope, ref := storeWithMemo(t, state)

	in := baseCall()
	res, err := ShapeCall(ctx, ShapeInput{
		Call:                in,
		Candidate:           executorCandidate(),
		MemoStore:           store,
		Scope:               scope,
		MemoRef:             &ref,
		SuppressVisibleMemo: true,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if res.MemoInjected {
		t.Fatal("visible memo must be suppressed for continuation executor")
	}
	if res.MemoOutcome != MemoOutcomeSkippedVisible {
		t.Fatalf("memo outcome: got %q want %q", res.MemoOutcome, MemoOutcomeSkippedVisible)
	}
	if len(res.Call.Instructions) != 0 {
		t.Fatalf("visible memo must not add instructions, got %d", len(res.Call.Instructions))
	}
	got, _, err := store.Get(ctx, scope, ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RegularTurnsRemaining != state.RegularTurnsRemaining {
		t.Fatalf("suppressed visible memo budget must not decrement: got %d want %d", got.RegularTurnsRemaining, state.RegularTurnsRemaining)
	}
	if got.InjectedCount != 0 {
		t.Fatalf("suppressed visible memo InjectedCount must remain 0, got %d", got.InjectedCount)
	}
}

func TestShapeCall_ExecutorVisibleMemoInjectedOnLaterTurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := injectableMemoState()
	state.VisibleToClient = true
	store, scope, ref := storeWithMemo(t, state)

	in := baseCall()
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if !res.MemoInjected {
		t.Fatal("visible memo must inject on later executor turn when suppression is not requested")
	}
	if res.MemoOutcome != MemoOutcomeInjected {
		t.Fatalf("memo outcome: got %q want %q", res.MemoOutcome, MemoOutcomeInjected)
	}
	if len(res.Call.Instructions) != 1 {
		t.Fatalf("executor call must have one injected instruction, got %d", len(res.Call.Instructions))
	}
}

func TestShapeCall_ExecutorDedupeAvoidsDuplicateMemo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	// Simulate a prior injection already present in the client's instructions.
	in.Instructions = []lipapi.Message{
		{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart(MemoContextOpenTag + "\n" + injectableMemoState().Memo + "\n" + MemoContextCloseTag)}},
	}
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if res.MemoInjected {
		t.Fatal("duplicate equivalent memo must not be re-injected")
	}
	if len(res.Call.Instructions) != 1 {
		t.Fatalf("dedupe must not add a second instruction, got %d", len(res.Call.Instructions))
	}
	got, _, err := store.Get(ctx, scope, ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RegularTurnsRemaining != injectableMemoState().RegularTurnsRemaining {
		t.Fatalf("dedupe budget must not decrement: got %d want %d", got.RegularTurnsRemaining, injectableMemoState().RegularTurnsRemaining)
	}
}

func TestShapeCall_ExecutorInjectsWhenRawMemoEchoedInUserMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	in.Messages = append(in.Messages, lipapi.Message{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart("Earlier planner said: " + injectableMemoState().Memo)},
	})
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if !res.MemoInjected {
		t.Fatal("raw memo echo in user message must not suppress wrapped injection")
	}
	if len(res.Call.Instructions) != 1 {
		t.Fatalf("executor call must have one injected instruction, got %d", len(res.Call.Instructions))
	}
}

func TestShapeCall_ExecutorMissingMemoRefNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(8192)

	in := baseCall()
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     "session-1",
		// MemoRef nil
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if res.MemoInjected || res.MemoUpdate != nil {
		t.Fatalf("nil MemoRef must be a no-op: %+v", res)
	}
	if len(res.Call.Instructions) != 0 {
		t.Fatalf("nil MemoRef must not inject, got %d", len(res.Call.Instructions))
	}
}

func TestShapeCall_ExecutorMissingMemoLookupNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(8192)
	ref := interleavedstate.MemoRef{Key: "no-such-memo", Version: 1}

	in := baseCall()
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     "session-1",
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if res.MemoInjected {
		t.Fatal("missing memo lookup must not inject")
	}
	if len(res.Call.Instructions) != 0 {
		t.Fatalf("missing memo lookup must not inject, got %d", len(res.Call.Instructions))
	}
}

func TestShapeCall_ExecutorEmptyScopeRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	_, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     "",
		MemoRef:   &ref,
	})
	if !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("want ErrEmptyScope, got %v", err)
	}
}

func TestShapeCall_ExecutorRespectsContextCancellation(t *testing.T) {
	t.Parallel()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	_, err := ShapeCall(canceledCtx(), ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err == nil {
		t.Fatal("canceled context must surface an error before store access")
	}
}

func TestShapeCall_ExecutorInjectionDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	in.Instructions = []lipapi.Message{
		{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("system prompt")}},
	}
	originalInstrCount := len(in.Instructions)
	_, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(in.Instructions) != originalInstrCount {
		t.Fatalf("input Instructions mutated: got %d want %d", len(in.Instructions), originalInstrCount)
	}
	if in.Instructions[0].Parts[0].Text != "system prompt" {
		t.Fatalf("input first instruction mutated: %q", in.Instructions[0].Parts[0].Text)
	}
}

func TestShapeCall_ExecutorInjectionPreservesTools(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	res, err := ShapeCall(ctx, ShapeInput{
		Call:      in,
		Candidate: executorCandidate(),
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if !toolsEqual(res.Call.Tools, in.Tools) {
		t.Fatalf("executor tools must be preserved on injection: got %+v want %+v", res.Call.Tools, in.Tools)
	}
	if res.Call.ToolChoice != in.ToolChoice {
		t.Fatalf("executor ToolChoice must be preserved on injection: got %+v want %+v", res.Call.ToolChoice, in.ToolChoice)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
