package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

func TestToolEventClassificationState_enrichAndInherit(t *testing.T) {
	t.Parallel()
	var st toolEventClassificationState

	start := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "Read"}
	st.enrich(&start)
	if start.Category != lipapi.ToolCategoryFileRead || start.MayMutateLocalFS {
		t.Fatalf("start = (%q,%v), want (file_read,false)", start.Category, start.MayMutateLocalFS)
	}

	delta := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1"}
	st.enrich(&delta)
	if delta.Category != lipapi.ToolCategoryFileRead || delta.MayMutateLocalFS {
		t.Fatalf("delta = (%q,%v), want (file_read,false)", delta.Category, delta.MayMutateLocalFS)
	}

	fin := lipapi.ToolEvent{Kind: lipapi.ToolEventFinished, ToolCallID: "c1"}
	st.enrich(&fin)
	if fin.Category != lipapi.ToolCategoryFileRead || fin.MayMutateLocalFS {
		t.Fatalf("fin = (%q,%v), want (file_read,false)", fin.Category, fin.MayMutateLocalFS)
	}
}

func TestToolEventClassificationState_orphanNameless(t *testing.T) {
	t.Parallel()
	var st toolEventClassificationState
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1"}
	st.enrich(&te)
	if te.Category != lipapi.ToolCategoryUnknown || !te.MayMutateLocalFS {
		t.Fatalf("orphan = (%q,%v), want (unknown,true)", te.Category, te.MayMutateLocalFS)
	}
}

func TestToolEventClassificationState_interleavedIDs(t *testing.T) {
	t.Parallel()
	var st toolEventClassificationState

	aStart := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "a", ToolName: "read"}
	bStart := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "b", ToolName: "bash"}
	st.enrich(&aStart)
	st.enrich(&bStart)

	aDelta := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "a"}
	st.enrich(&aDelta)
	if aDelta.Category != lipapi.ToolCategoryFileRead || aDelta.MayMutateLocalFS {
		t.Fatalf("a delta = (%q,%v), want (file_read,false)", aDelta.Category, aDelta.MayMutateLocalFS)
	}

	bDelta := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "b"}
	st.enrich(&bDelta)
	if bDelta.Category != lipapi.ToolCategoryOSCommand || !bDelta.MayMutateLocalFS {
		t.Fatalf("b delta = (%q,%v), want (os_command,true)", bDelta.Category, bDelta.MayMutateLocalFS)
	}
}

func TestToolEventClassificationState_forgetPreventsStaleReuse(t *testing.T) {
	t.Parallel()
	var st toolEventClassificationState
	start := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "read"}
	st.enrich(&start)
	st.forget("c1")

	reuse := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1"}
	st.enrich(&reuse)
	if reuse.Category != lipapi.ToolCategoryUnknown || !reuse.MayMutateLocalFS {
		t.Fatalf("reuse = (%q,%v), want (unknown,true)", reuse.Category, reuse.MayMutateLocalFS)
	}
}

func TestToolEventClassificationState_clear(t *testing.T) {
	t.Parallel()
	var st toolEventClassificationState
	start := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "read"}
	st.enrich(&start)
	st.clear()

	delta := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1"}
	st.enrich(&delta)
	if delta.Category != lipapi.ToolCategoryUnknown || !delta.MayMutateLocalFS {
		t.Fatalf("after clear = (%q,%v), want (unknown,true)", delta.Category, delta.MayMutateLocalFS)
	}
}

// recordingToolReactor records every ToolEvent it observes so tests can assert the
// classification that reaches the tool-reactor seam.
type recordingToolReactor struct {
	seen []lipapi.ToolEvent
}

func (r *recordingToolReactor) ID() string { return "rec" }
func (r *recordingToolReactor) Order() int { return 0 }
func (r *recordingToolReactor) HandleToolEvent(_ context.Context, te lipapi.ToolEvent, _ sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	r.seen = append(r.seen, te)
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

// renamingResponseHook renames tool events after the tool-reactor phase.
type renamingResponseHook struct{ to string }

func (renamingResponseHook) ID() string                        { return "resp-rename" }
func (renamingResponseHook) Order() int                        { return 0 }
func (renamingResponseHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h renamingResponseHook) HandleEvent(_ context.Context, ev *lipapi.Event, _ sdkhooks.PartMeta) error {
	if ev.ToolCallID != "" {
		ev.ToolName = h.to
	}
	return nil
}

// renameFinalizer rewrites a completed tool call to a fixed name.
type renameFinalizer struct{ to string }

func (renameFinalizer) ID() string { return "rename-fin" }
func (renameFinalizer) Order() int { return 0 }
func (f renameFinalizer) Finalize(_ context.Context, call toolcall.CompletedCall, _ lipapi.ToolDef, _ []lipapi.ToolDef, _ toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionRewrite, ToolName: f.to, ArgsJSON: call.ArgsJSON}, nil
}

func dispatchClientFacing(t *testing.T, s *retryRecvStream, ev lipapi.Event) {
	t.Helper()
	if _, _, err := s.dispatchClientFacingEvent(context.Background(), ev); err != nil {
		t.Fatalf("dispatch %s %s: %v", ev.Kind, ev.ToolCallID, err)
	}
}

func classificationAt(t *testing.T, te lipapi.ToolEvent, wantCat lipapi.ToolCategory, wantMutate bool) {
	t.Helper()
	if te.Category != wantCat || te.MayMutateLocalFS != wantMutate {
		t.Fatalf("id=%s kind=%s = (%q,%v), want (%q,%v)", te.ToolCallID, te.Kind, te.Category, te.MayMutateLocalFS, wantCat, wantMutate)
	}
}

// rewriteStartedName rewrites tool_call_started to a new name so later name-less
// fragments must inherit rememberEffective, not the original enrich result.
type rewriteStartedName struct{ to string }

func (r rewriteStartedName) ID() string { return "rename-started" }
func (r rewriteStartedName) Order() int { return 1 }
func (r rewriteStartedName) HandleToolEvent(_ context.Context, te lipapi.ToolEvent, _ sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	if te.Kind != lipapi.ToolEventStarted {
		return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
	}
	next := te
	next.ToolName = r.to
	return sdkhooks.ToolRewrite, next, nil
}

// swallowFinished drops ToolEventFinished so cleanup must run on the swallow path.
type swallowFinished struct{}

func (swallowFinished) ID() string { return "swallow-fin" }
func (swallowFinished) Order() int { return 1 }
func (swallowFinished) HandleToolEvent(_ context.Context, te lipapi.ToolEvent, _ sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	if te.Kind != lipapi.ToolEventFinished {
		return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
	}
	return sdkhooks.ToolSwallow, lipapi.ToolEvent{}, nil
}

// replaceStartedIDNameless replaces a start event with a different ID and no name.
type replaceStartedIDNameless struct{ to string }

func (r replaceStartedIDNameless) ID() string { return "replace-id" }
func (r replaceStartedIDNameless) Order() int { return 1 }
func (r replaceStartedIDNameless) HandleToolEvent(_ context.Context, te lipapi.ToolEvent, _ sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	if te.Kind != lipapi.ToolEventStarted {
		return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
	}
	return sdkhooks.ToolReplace, lipapi.ToolEvent{Kind: te.Kind, ToolCallID: r.to}, nil
}

func TestDispatchClientFacingEvent_lifecycleClassification(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	bus := hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})
	s := &retryRecvStream{bus: bus}

	events := []lipapi.Event{
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "Read"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"x":1}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1"},
	}
	for i, ev := range events {
		if _, _, err := s.dispatchClientFacingEvent(context.Background(), ev); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}

	if len(rec.seen) != 3 {
		t.Fatalf("reactor saw %d events, want 3", len(rec.seen))
	}
	for i, te := range rec.seen {
		if te.Category != lipapi.ToolCategoryFileRead || te.MayMutateLocalFS {
			t.Fatalf("event %d = (%q,%v), want (file_read,false)", i, te.Category, te.MayMutateLocalFS)
		}
	}
}

func TestDispatchClientFacingEvent_orphanNamelessIsConservative(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	bus := hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})
	s := &retryRecvStream{bus: bus}

	ev := lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`}
	if _, _, err := s.dispatchClientFacingEvent(context.Background(), ev); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(rec.seen) != 1 {
		t.Fatalf("reactor saw %d events, want 1", len(rec.seen))
	}
	if rec.seen[0].Category != lipapi.ToolCategoryUnknown || !rec.seen[0].MayMutateLocalFS {
		t.Fatalf("orphan = (%q,%v), want (unknown,true)", rec.seen[0].Category, rec.seen[0].MayMutateLocalFS)
	}
}

func TestDispatchClientFacingEvent_responsePartHookRenameRefreshesLifecycle(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	bus := hooks.New(hooks.Config{
		ToolReactors:      []sdkhooks.ToolReactor{rec},
		ResponsePartHooks: []sdkhooks.ResponsePartHook{renamingResponseHook{to: "bash"}},
	})
	s := &retryRecvStream{bus: bus}

	start := lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"}
	if _, _, err := s.dispatchClientFacingEvent(context.Background(), start); err != nil {
		t.Fatalf("start: %v", err)
	}
	delta := lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`}
	if _, _, err := s.dispatchClientFacingEvent(context.Background(), delta); err != nil {
		t.Fatalf("delta: %v", err)
	}

	if len(rec.seen) != 2 {
		t.Fatalf("reactor saw %d events, want 2", len(rec.seen))
	}
	// The reactor observes the pre-response-hook event (read -> file_read).
	if rec.seen[0].Category != lipapi.ToolCategoryFileRead || rec.seen[0].MayMutateLocalFS {
		t.Fatalf("start = (%q,%v), want (file_read,false)", rec.seen[0].Category, rec.seen[0].MayMutateLocalFS)
	}
	// The name-less delta inherits the post-rename classification (bash -> os_command).
	if rec.seen[1].Category != lipapi.ToolCategoryOSCommand || !rec.seen[1].MayMutateLocalFS {
		t.Fatalf("delta = (%q,%v), want (os_command,true) after response-hook rename", rec.seen[1].Category, rec.seen[1].MayMutateLocalFS)
	}
}

func TestFinalizerRenamedLifecycleClassifiedNormally(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	bus := hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})
	s := &retryRecvStream{bus: bus}

	a := newToolCallAssembler([]toolcall.Finalizer{renameFinalizer{to: "bash"}}, 1024, []lipapi.ToolDef{{Name: "read"}})
	if a == nil {
		t.Fatal("assembler")
	}
	s.toolFinal = a

	meta := toolcall.Meta{}
	_, _ = a.ingest(context.Background(), lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"}, meta)
	_, _ = a.ingest(context.Background(), lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`}, meta)
	if _, err := a.ingest(context.Background(), lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "read"}, meta); err != nil {
		t.Fatalf("finish: %v", err)
	}

	for {
		ev, ok := s.popToolFinalDrain()
		if !ok {
			break
		}
		if _, _, err := s.dispatchClientFacingEvent(context.Background(), ev); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}

	if len(rec.seen) != 3 {
		t.Fatalf("reactor saw %d events, want 3", len(rec.seen))
	}
	for i, te := range rec.seen {
		if te.ToolName != "bash" {
			t.Fatalf("event %d ToolName=%q, want bash", i, te.ToolName)
		}
		if te.Category != lipapi.ToolCategoryOSCommand || !te.MayMutateLocalFS {
			t.Fatalf("event %d = (%q,%v), want (os_command,true)", i, te.Category, te.MayMutateLocalFS)
		}
	}
}

func TestDispatchClientFacingEvent_finishCleanupPreventsStaleReuse(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})}

	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`})

	if len(rec.seen) != 3 {
		t.Fatalf("reactor saw %d events, want 3", len(rec.seen))
	}
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[1], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[2], lipapi.ToolCategoryUnknown, true)
}

// Named finish still runs observeFinalName (non-empty ToolName) before forget.
// If those steps were swapped, the finish would re-remember the ID and the
// subsequent nameless reuse would incorrectly inherit file_read.
func TestDispatchClientFacingEvent_namedFinishCleanupPreventsStaleReuse(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})}

	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "read"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`})

	if len(rec.seen) != 3 {
		t.Fatalf("reactor saw %d events, want 3", len(rec.seen))
	}
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[1], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[2], lipapi.ToolCategoryUnknown, true)
}

func TestDispatchClientFacingEvent_swallowedFinishCleanupPreventsStaleReuse(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{
		ToolReactors: []sdkhooks.ToolReactor{rec, swallowFinished{}},
	})}

	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"})
	_, swallowed, err := s.dispatchClientFacingEvent(context.Background(), lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1"})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if !swallowed {
		t.Fatal("expected finished event to be swallowed")
	}
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`})

	if len(rec.seen) != 3 {
		t.Fatalf("reactor saw %d events, want 3", len(rec.seen))
	}
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[1], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[2], lipapi.ToolCategoryUnknown, true)
}

func TestDispatchClientFacingEvent_interleavedIDsDoNotCross(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})}

	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "a", ToolName: "read"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "b", ToolName: "bash"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "a", Delta: `{}`})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "b", Delta: `{}`})

	if len(rec.seen) != 4 {
		t.Fatalf("reactor saw %d events, want 4", len(rec.seen))
	}
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[1], lipapi.ToolCategoryOSCommand, true)
	classificationAt(t, rec.seen[2], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[3], lipapi.ToolCategoryOSCommand, true)
}

func TestDispatchClientFacingEvent_resetToolFinalClearsAbandonedState(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})}

	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"})
	s.resetToolFinal()
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`})

	if len(rec.seen) != 2 {
		t.Fatalf("reactor saw %d events, want 2", len(rec.seen))
	}
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[1], lipapi.ToolCategoryUnknown, true)
}

func TestDispatchClientFacingEvent_reactorRenameRefreshesLaterNamelessFragment(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{
		ToolReactors: []sdkhooks.ToolReactor{rec, rewriteStartedName{to: "bash"}},
	})}

	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`})

	if len(rec.seen) != 2 {
		t.Fatalf("reactor saw %d events, want 2", len(rec.seen))
	}
	// Recorder runs before the rename, so start is still the incoming read class.
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileRead, false)
	// Name-less delta inherits rememberEffective after the rename (bash).
	classificationAt(t, rec.seen[1], lipapi.ToolCategoryOSCommand, true)
}

func TestDispatchClientFacingEvent_osCommandIgnoresReadOnlyCommandText(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})}

	payloads := []struct {
		id    string
		name  string
		delta string
	}{
		{"c-cat", "exec_command", `{"command":"cat README.md"}`},
		{"c-ls", "bash", `{"command":"ls -la"}`},
		{"c-rg", "execute_command", `{"command":"rg TODO"}`},
		{"c-git", "exec_command", `{"command":"git status"}`},
	}
	for _, p := range payloads {
		dispatchClientFacing(t, s, lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: p.id,
			ToolName:   p.name,
			Delta:      p.delta,
		})
	}
	if len(rec.seen) != len(payloads) {
		t.Fatalf("reactor saw %d events, want %d", len(rec.seen), len(payloads))
	}
	for i, te := range rec.seen {
		if te.ArgsDelta != payloads[i].delta {
			t.Fatalf("event %d ArgsDelta=%q, want %q", i, te.ArgsDelta, payloads[i].delta)
		}
		classificationAt(t, te, lipapi.ToolCategoryOSCommand, true)
	}
}

func TestDispatchClientFacingEvent_applyPatchIgnoresDeleteHunk(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{ToolReactors: []sdkhooks.ToolReactor{rec}})}
	deleteHunk := "*** Begin Patch\n*** Delete File: secret.txt\n*** End Patch\n"

	dispatchClientFacing(t, s, lipapi.Event{
		Kind:       lipapi.EventToolCallArgsDelta,
		ToolCallID: "c1",
		ToolName:   "apply_patch",
		Delta:      deleteHunk,
	})
	if len(rec.seen) != 1 {
		t.Fatalf("reactor saw %d events, want 1", len(rec.seen))
	}
	if rec.seen[0].ArgsDelta != deleteHunk {
		t.Fatalf("ArgsDelta not preserved")
	}
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileEdit, true)
}

func TestToolEventClassificationState_enrichIgnoresArgsDelta(t *testing.T) {
	t.Parallel()
	var st toolEventClassificationState
	te := lipapi.ToolEvent{
		Kind:       lipapi.ToolEventArgsDelta,
		ToolCallID: "c1",
		ToolName:   "exec_command",
		ArgsDelta:  `{"command":"cat README.md"}`,
	}
	st.enrich(&te)
	if te.Category != lipapi.ToolCategoryOSCommand || !te.MayMutateLocalFS {
		t.Fatalf("enrich(exec_command, cat) = (%q,%v), want (os_command,true)", te.Category, te.MayMutateLocalFS)
	}

	patch := lipapi.ToolEvent{
		Kind:       lipapi.ToolEventArgsDelta,
		ToolCallID: "c2",
		ToolName:   "apply_patch",
		ArgsDelta:  "*** Begin Patch\n*** Delete File: secret.txt\n*** End Patch\n",
	}
	st.enrich(&patch)
	if patch.Category != lipapi.ToolCategoryFileEdit || !patch.MayMutateLocalFS {
		t.Fatalf("enrich(apply_patch, delete hunk) = (%q,%v), want (file_edit,true)", patch.Category, patch.MayMutateLocalFS)
	}
}

// A later fragment that repeats the original backend name wins over a prior
// reactor rename (design invariant 2: non-empty effective name is authoritative).
func TestDispatchClientFacingEvent_namedFinishAfterReactorRenameUsesFinishName(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{
		ToolReactors: []sdkhooks.ToolReactor{rec, rewriteStartedName{to: "bash"}},
	})}

	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "read"})

	if len(rec.seen) != 3 {
		t.Fatalf("reactor saw %d events, want 3", len(rec.seen))
	}
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[1], lipapi.ToolCategoryOSCommand, true)
	classificationAt(t, rec.seen[2], lipapi.ToolCategoryFileRead, false)
}

func TestDispatchClientFacingEvent_changedIDNamelessReplacePoisonsSourceLifecycle(t *testing.T) {
	t.Parallel()
	rec := &recordingToolReactor{}
	s := &retryRecvStream{bus: hooks.New(hooks.Config{
		ToolReactors: []sdkhooks.ToolReactor{rec, replaceStartedIDNameless{to: "c2"}},
	})}

	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "read"})
	dispatchClientFacing(t, s, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`})

	if len(rec.seen) != 2 {
		t.Fatalf("reactor saw %d events, want 2", len(rec.seen))
	}
	classificationAt(t, rec.seen[0], lipapi.ToolCategoryFileRead, false)
	classificationAt(t, rec.seen[1], lipapi.ToolCategoryUnknown, true)
}
