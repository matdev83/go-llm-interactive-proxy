package interleavedthinking

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	if !reflect.DeepEqual(res.Call.ToolChoice, in.ToolChoice) {
		t.Fatalf("executor ToolChoice must be unchanged: got %+v want %+v", res.Call.ToolChoice, in.ToolChoice)
	}
	if len(res.Call.Instructions) != len(in.Instructions) {
		t.Fatalf("executor instructions must be unchanged: got %d want %d", len(res.Call.Instructions), len(in.Instructions))
	}
	if !reflect.DeepEqual(res.Call.Messages, in.Messages) {
		t.Fatalf("executor messages must be unchanged: got %+v want %+v", res.Call.Messages, in.Messages)
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

// --- executor memo injection ---

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
		ExtractionSource:      ExtractionSourceFull,
	}
}

func TestShapeCall_ExecutorInjectsMemoAtTail(t *testing.T) {
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
	// Tail injection: the memo lands on the last conversation message, and the
	// prefix (instructions + all earlier messages) is untouched.
	if len(res.Call.Instructions) != len(in.Instructions) {
		t.Fatalf("tail injection must not touch Instructions, got %d want %d", len(res.Call.Instructions), len(in.Instructions))
	}
	if len(res.Call.Messages) != 1 {
		t.Fatalf("executor call must keep one message, got %d", len(res.Call.Messages))
	}
	text := res.Call.Messages[0].Parts[0].Text
	if !strings.HasPrefix(text, "plan this\n\n---\n[Session Steering Guidance]\n") {
		t.Fatalf("injected memo must be tail-anchored after the original text: %q", text)
	}
	if !contains(text, state.Memo) {
		t.Fatalf("injected memo must contain memo body: %q", text)
	}
	if res.Call.Messages[0].Metadata["source"] != MetadataSourceInterleavedThinking ||
		res.Call.Messages[0].Metadata["kind"] != MetadataKindThinkerMemoTail {
		t.Fatalf("injected message must carry traceability metadata: %+v", res.Call.Messages[0].Metadata)
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

func TestShapeCall_ExecutorTailInjectionPreservesPrefixMessages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	in.Messages = []lipapi.Message{
		{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("system prompt")}},
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("first user turn")}},
		{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("assistant answer")}},
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("second user turn")}},
	}
	wantPrefix := make([]lipapi.Message, len(in.Messages))
	copy(wantPrefix, in.Messages)

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
		t.Fatal("memo must be injected")
	}
	if len(res.Call.Messages) != len(wantPrefix) {
		t.Fatalf("message count must stay stable on user tail, got %d want %d", len(res.Call.Messages), len(wantPrefix))
	}
	// Messages[0..n-2] are byte-identical: prompt-cache prefix preserved.
	for i := 0; i < len(wantPrefix)-1; i++ {
		if !reflect.DeepEqual(res.Call.Messages[i], wantPrefix[i]) {
			t.Fatalf("prefix message %d must stay byte-identical: got %+v want %+v", i, res.Call.Messages[i], wantPrefix[i])
		}
	}
	last := res.Call.Messages[len(res.Call.Messages)-1]
	if last.Role != lipapi.RoleUser || !strings.Contains(last.Parts[0].Text, "second user turn") {
		t.Fatalf("last message must be the original user tail, got %+v", last)
	}
	if !strings.HasPrefix(last.Parts[0].Text, "second user turn\n\n---\n[Session Steering Guidance]\n") {
		t.Fatalf("guidance must be appended to the last user message: %q", last.Parts[0].Text)
	}
}

func TestShapeCall_ExecutorTailInjectionOnToolMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	in.Messages = []lipapi.Message{
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("run the tool")}},
		{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
			Kind:       lipapi.PartJSON,
			ToolCallID: "call-1",
			ToolName:   "search",
			Content:    json.RawMessage(`{"q":"x"}`),
		}}},
		{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
			Kind:       lipapi.PartToolResult,
			ToolCallID: "call-1",
			ToolName:   "search",
			Text:       "tool outcome",
		}}},
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
	if !res.MemoInjected {
		t.Fatal("memo must be injected on a tool tail")
	}
	last := res.Call.Messages[len(res.Call.Messages)-1]
	if last.Role != lipapi.RoleTool {
		t.Fatalf("last message role must stay tool, got %q", last.Role)
	}
	if len(last.Parts) != 2 {
		t.Fatalf("tool tail must gain exactly one part, got %d: %+v", len(last.Parts), last.Parts)
	}
	if last.Parts[0].Kind != lipapi.PartToolResult || last.Parts[0].Text != "tool outcome" {
		t.Fatalf("tool result part must stay byte-identical: %+v", last.Parts[0])
	}
	added := last.Parts[1]
	if added.Kind != lipapi.PartText || !strings.HasPrefix(added.Text, "\n\n---\n[Session Steering Guidance]\n") {
		t.Fatalf("guidance must be a new text part on the tool message: %+v", added)
	}
	if !contains(added.Text, injectableMemoState().Memo) {
		t.Fatalf("guidance part must contain memo body: %q", added.Text)
	}
	if last.Metadata["source"] != MetadataSourceInterleavedThinking || last.Metadata["kind"] != MetadataKindThinkerMemoTail {
		t.Fatalf("tool message must carry tail metadata: %+v", last.Metadata)
	}
	if err := res.Call.Validate(); err != nil {
		t.Fatalf("shaped call must validate: %v", err)
	}
}

func TestShapeCall_ExecutorTailInjectionMultipartContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	in.Messages = []lipapi.Message{
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{
			lipapi.TextPart("first part"),
			{Kind: lipapi.PartImageRef, ImageRef: "img://1", ImageMIME: "image/png"},
		}},
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
	if !res.MemoInjected {
		t.Fatal("memo must be injected on multipart content")
	}
	last := res.Call.Messages[len(res.Call.Messages)-1]
	if len(last.Parts) != 3 {
		t.Fatalf("multipart tail must gain exactly one part, got %d: %+v", len(last.Parts), last.Parts)
	}
	// Original parts byte-identical.
	if last.Parts[0].Text != "first part" || last.Parts[1].ImageRef != "img://1" {
		t.Fatalf("multipart prefix parts changed: %+v", last.Parts)
	}
	added := last.Parts[2]
	if added.Kind != lipapi.PartText || !strings.Contains(added.Text, "\n\n---\n[Session Steering Guidance]\n") {
		t.Fatalf("guidance must be a new text part with the separator: %+v", added)
	}
	if !contains(added.Text, injectableMemoState().Memo) {
		t.Fatalf("guidance part must contain memo body: %q", added.Text)
	}
}

func TestShapeCall_ExecutorEmptyMessagesCreatesUserMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	in.Messages = nil
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
		t.Fatal("memo must be injected when messages are empty")
	}
	if len(res.Call.Messages) != 1 {
		t.Fatalf("empty conversation must gain one user message, got %d", len(res.Call.Messages))
	}
	only := res.Call.Messages[0]
	if only.Role != lipapi.RoleUser {
		t.Fatalf("standalone message role must be user, got %q", only.Role)
	}
	if only.Parts[0].Text != "[Session Steering Guidance]\n"+injectableMemoState().Memo {
		t.Fatalf("standalone message content: %q", only.Parts[0].Text)
	}
	if only.Metadata["kind"] != MetadataKindThinkerMemoTail {
		t.Fatalf("standalone message must carry traceability metadata: %+v", only.Metadata)
	}
}

func TestShapeCall_ExecutorAssistantTailAppendsUserMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	in.Messages = []lipapi.Message{
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("q")}},
		{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("a")}},
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
	if !res.MemoInjected {
		t.Fatal("memo must be injected on assistant tail")
	}
	if len(res.Call.Messages) != 3 {
		t.Fatalf("assistant tail must gain one message, got %d", len(res.Call.Messages))
	}
	if !reflect.DeepEqual(res.Call.Messages[0], in.Messages[0]) || !reflect.DeepEqual(res.Call.Messages[1], in.Messages[1]) {
		t.Fatalf("prefix messages must stay byte-identical: %+v", res.Call.Messages[:2])
	}
	added := res.Call.Messages[2]
	if added.Role != lipapi.RoleUser || added.Parts[0].Text != "[Session Steering Guidance]\n"+injectableMemoState().Memo {
		t.Fatalf("appended assistant-turn message: %+v", added)
	}
	if added.Metadata["kind"] != MetadataKindThinkerMemoTail {
		t.Fatalf("appended message must carry tail metadata: %+v", added.Metadata)
	}
}

func TestShapeCall_ExecutorEmptyMemoSkipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := injectableMemoState()
	state.Memo = "   \n "
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
		t.Fatal("empty memo must not be injected")
	}
	if res.MemoOutcome != MemoOutcomeSkippedEmpty {
		t.Fatalf("memo outcome: got %q want %q", res.MemoOutcome, MemoOutcomeSkippedEmpty)
	}
	if !reflect.DeepEqual(res.Call.Messages, in.Messages) {
		t.Fatalf("empty memo must not mutate messages: %+v", res.Call.Messages)
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
	if !reflect.DeepEqual(res.Call.Messages, in.Messages) {
		t.Fatalf("expired memo must not mutate messages, got %+v", res.Call.Messages)
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
	if !reflect.DeepEqual(res.Call.Messages, in.Messages) {
		t.Fatalf("suppressed visible memo must not mutate messages, got %+v", res.Call.Messages)
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
	if len(res.Call.Messages) != 1 {
		t.Fatalf("executor call must keep one message, got %d", len(res.Call.Messages))
	}
}

func TestShapeCall_ExecutorDedupeAvoidsDuplicateMemo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	// Simulate a prior tail injection already present in the last message.
	prior := injectableMemoState().Memo
	in.Messages[0].Parts[0].Text = "plan this" + "\n\n---\n" + SessionSteeringGuidanceHeader + "\n" + prior

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
	if res.MemoOutcome != MemoOutcomeSkippedDuplicate {
		t.Fatalf("memo outcome: got %q want %q", res.MemoOutcome, MemoOutcomeSkippedDuplicate)
	}
	if !reflect.DeepEqual(res.Call.Messages, in.Messages) {
		t.Fatalf("dedupe must not mutate messages: %+v", res.Call.Messages)
	}
	got, _, err := store.Get(ctx, scope, ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RegularTurnsRemaining != injectableMemoState().RegularTurnsRemaining {
		t.Fatalf("dedupe budget must not decrement: got %d want %d", got.RegularTurnsRemaining, injectableMemoState().RegularTurnsRemaining)
	}
}

func TestShapeCall_ExecutorDedupeMatchesStandaloneForm(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, scope, ref := storeWithMemo(t, injectableMemoState())

	in := baseCall()
	// Prior standalone user-message injection (empty-conversation edge case).
	in.Messages = []lipapi.Message{{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart(SessionSteeringGuidanceHeader + "\n" + injectableMemoState().Memo)},
	}}
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
		t.Fatal("standalone prior injection must be deduplicated")
	}
	if res.MemoOutcome != MemoOutcomeSkippedDuplicate {
		t.Fatalf("memo outcome: got %q want %q", res.MemoOutcome, MemoOutcomeSkippedDuplicate)
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
		t.Fatal("raw memo echo in user message must not suppress guidance injection")
	}
	if len(res.Call.Messages) != 2 {
		t.Fatalf("executor call must keep both messages, got %d", len(res.Call.Messages))
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
	if !reflect.DeepEqual(res.Call.Messages, in.Messages) {
		t.Fatalf("nil MemoRef must not inject, got %+v", res.Call.Messages)
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
	if !reflect.DeepEqual(res.Call.Messages, in.Messages) {
		t.Fatalf("missing memo lookup must not inject, got %+v", res.Call.Messages)
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
	originalTailText := in.Messages[0].Parts[0].Text
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
	if in.Messages[0].Parts[0].Text != originalTailText {
		t.Fatalf("input tail message mutated: %q", in.Messages[0].Parts[0].Text)
	}
	if in.Messages[0].Metadata != nil {
		t.Fatalf("input tail message metadata mutated: %+v", in.Messages[0].Metadata)
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
	if !reflect.DeepEqual(res.Call.ToolChoice, in.ToolChoice) {
		t.Fatalf("executor ToolChoice must be preserved on injection: got %+v want %+v", res.Call.ToolChoice, in.ToolChoice)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
