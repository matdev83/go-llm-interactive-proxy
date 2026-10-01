package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// Spec: b-leg-path-virtualization Task 1.1. Requirements 4.5, 4.6, 8.4 and
// design.md sections "Existing Architecture and Placement" (step 11 response
// tool-call assembly/finalization), "Mandatory Buffering / Completeness
// Contract", "Error Handling" ("Mandatory assembly overflow: fail closed for
// that tool call"; "Numeric finalizer ordering alone is not sufficient if
// assembler fallback could bypass mandatory expansion; characterization tests
// define the required invariant"), and "Testing Strategy" (">64 KiB call proves
// mandatory expansion is not bypassed").
//
// This is a permanent characterization test of the CURRENT, still-broken
// behavior, and it is deliberately RED today.
//
// toolCallAssembler enforces one shared assembly cap
// (defaultToolCallFinalizationMaxArgsBytes, kept equal to toolcallrepair's
// repair.DefaultMaxArgsBytes by
// TestDefaultToolCallFinalizationMaxArgsBytesMatchCore) across every
// finalizer. When a completed tool call exceeds that cap, ingestDelta gives up
// on assembling the call, replays the original stream fragments onto drain, and
// marks the tool-call ID pass-through, so ingestFinished never calls
// finalizeCall at all. A finalizer that must see the complete arguments before
// the call may be released therefore never runs for an oversized call, and the
// original fragments reach the client unchanged.
//
// Consequences this test pins:
//   - requirements.md 4.6: a shared size limit silently skips expansion for a
//     call to which path virtualization applies;
//   - requirements.md 4.5: the call is not failed closed on a mandatory
//     expansion bound; the bypass is silent;
//   - requirements.md 8.4: the tool-call-repair size budget governs path
//     expansion, because both use the same shared cap.
//
// Task 7.2 (mandatory buffering semantics, after the Task 7.1 optional
// generic capability contract) must turn this test green by delivering the
// complete arguments to the mandatory finalizer. Task 7.2 must remove the RED
// condition; do not weaken these assertions to silence the failure.

const (
	// mandatoryRealRoot is the client-visible workspace root the expansion
	// finalizer must produce.
	mandatoryRealRoot = "/home/dev/workspaces/lip-path-virtualization-worktree"

	// mandatoryVirtualRoot is the fixed V1 reserved alias form (POSIX flavor,
	// 20-character workspace tag) a model may emit back to the proxy.
	mandatoryVirtualRoot = "/.__lip_v1__/w_0123456789abcdefghij/"

	mandatoryToolName = "read_file"

	// mandatoryOverflowReject is the OverflowPolicy a mandatory finalizer
	// declares; it mirrors design.md "Mandatory Buffering / Completeness
	// Contract".
	mandatoryOverflowReject = "reject"

	// mandatoryFirstFragmentBytes cuts the first args delta in the middle of
	// the reserved alias tag, so the alias only exists across fragment
	// boundaries and can never be matched on a single raw fragment.
	mandatoryFirstFragmentBytes = len(`{"path":"/.__lip_v1__/w_0`)
)

// mandatoryBufferingSpec mirrors the optional generic capability described in
// design.md "Mandatory Buffering / Completeness Contract" (BufferingRequirement
// / BufferingSpec / OverflowPolicy). It is declared test-locally because the
// SDK contract does not exist yet (Task 7.1): the point of this
// characterization is that the runtime currently has no way to read any such
// declaration from a finalizer.
type mandatoryBufferingSpec struct {
	maxArgsBytes int
	overflow     string
}

// mandatoryExpansionFin stands in for a finalizer that must receive the
// complete tool-call arguments before the call may be released, such as reverse
// expansion of a virtual path back to the real path. It records every
// invocation and the exact arguments it saw, so a silent bypass is observable,
// and it fails closed on its own declared mandatory bound.
type mandatoryExpansionFin struct {
	spec  mandatoryBufferingSpec
	calls int
	seen  []byte
}

func (*mandatoryExpansionFin) ID() string { return "mandatory-expansion" }

func (*mandatoryExpansionFin) Order() int { return 0 }

func (f *mandatoryExpansionFin) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	_ lipapi.ToolDef,
	_ []lipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	f.seen = append(f.seen[:0], call.ArgsJSON...)
	if f.spec.overflow == mandatoryOverflowReject && len(call.ArgsJSON) > f.spec.maxArgsBytes {
		return toolcall.Result{Action: toolcall.ActionReject, ReasonCode: toolcall.ReasonArgsTooLarge}, nil
	}
	expanded := bytes.Replace(call.ArgsJSON, []byte(mandatoryVirtualRoot), []byte(mandatoryRealRoot+"/"), 1)
	if bytes.Equal(expanded, call.ArgsJSON) {
		return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
	}
	return toolcall.Result{
		Action:     toolcall.ActionRewrite,
		ToolName:   call.ToolName,
		ArgsJSON:   expanded,
		ReasonCode: toolcall.ReasonValidPassThrough,
	}, nil
}

func newMandatoryExpansionFin() *mandatoryExpansionFin {
	// 1 MiB default mandatory bound from design.md, well above the shared
	// finalization cap this characterization exercises.
	return &mandatoryExpansionFin{spec: mandatoryBufferingSpec{maxArgsBytes: 1 << 20, overflow: mandatoryOverflowReject}}
}

func mandatoryArgsJSON(minBytes int) string {
	// The filler is an opaque content field, not a path-bearing target: it only
	// makes the completed call exceed the shared finalization cap.
	const (
		prefix = `{"path":"` + mandatoryVirtualRoot + `src/main.go","content":"`
		suffix = `"}`
	)
	pad := minBytes - len(prefix) - len(suffix)
	if pad < 0 {
		pad = 0
	}
	return prefix + strings.Repeat("x", pad) + suffix
}

// mandatoryExpandedArgsJSON is the document mandatoryExpansionFin must produce
// for the corresponding mandatoryArgsJSON document.
func mandatoryExpandedArgsJSON(minBytes int) string {
	return strings.Replace(mandatoryArgsJSON(minBytes), mandatoryVirtualRoot, mandatoryRealRoot+"/", 1)
}

// splitMandatoryArgsFragments splits an argument document into stream-shaped
// deltas, cutting the first one inside the reserved alias tag.
func splitMandatoryArgsFragments(argsJSON string) []string {
	const midFragmentBytes = 4096
	rest := argsJSON
	fragments := make([]string, 0, 3)
	if len(rest) > mandatoryFirstFragmentBytes {
		fragments = append(fragments, rest[:mandatoryFirstFragmentBytes])
		rest = rest[mandatoryFirstFragmentBytes:]
	}
	if len(rest) > midFragmentBytes {
		fragments = append(fragments, rest[:midFragmentBytes])
		rest = rest[midFragmentBytes:]
	}
	return append(fragments, rest)
}

// streamMandatoryToolCall drives one completed tool call through the assembler
// as started / args-delta* / finished and returns the argument deltas the client
// would observe. Held fragments are replayed from the drain queue; fragments the
// assembler stops holding continue straight to the client, so both are
// collected in release order.
func streamMandatoryToolCall(t *testing.T, a *toolCallAssembler, id, argsJSON string) (string, error) {
	t.Helper()
	ctx := context.Background()
	meta := toolcall.Meta{}
	var released strings.Builder

	if held, ingErr := a.ingest(ctx, lipapi.Event{
		Kind: lipapi.EventToolCallStarted, ToolCallID: id, ToolName: mandatoryToolName,
	}, meta); ingErr != nil || !held {
		t.Fatalf("started: held=%v err=%v", held, ingErr)
	}
	for _, fragment := range splitMandatoryArgsFragments(argsJSON) {
		held, ingErr := a.ingest(ctx, lipapi.Event{
			Kind: lipapi.EventToolCallArgsDelta, ToolCallID: id,
			ToolName: mandatoryToolName, Delta: fragment,
		}, meta)
		if ingErr != nil {
			t.Fatalf("args delta: %v", ingErr)
		}
		if !held {
			released.WriteString(fragment)
		}
	}
	_, err := a.ingest(ctx, lipapi.Event{
		Kind: lipapi.EventToolCallFinished, ToolCallID: id, ToolName: mandatoryToolName,
	}, meta)
	for {
		ev, ok := a.popDrain()
		if !ok {
			return released.String(), err
		}
		if ev.Kind == lipapi.EventToolCallArgsDelta {
			released.WriteString(ev.Delta)
		}
	}
}

func TestRED_ToolCallAssembler_MandatoryFinalizerBypassedAboveDefaultCap(t *testing.T) {
	t.Parallel()

	catalog := []lipapi.ToolDef{{Name: mandatoryToolName, Parameters: []byte(`{"type":"object"}`)}}

	// Control: the same finalizer is wired correctly below the shared cap, so a
	// failure in the subtest below is attributable to the cap and not to
	// finalizer registration, catalog lookup, or fragment buffering.
	t.Run("under_shared_cap_finalizer_sees_complete_args", func(t *testing.T) {
		t.Parallel()
		fin := newMandatoryExpansionFin()
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared cap: got %d want %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		const smallArgsBytes = 8 * 1024
		args := mandatoryArgsJSON(smallArgsBytes)
		if len(args) > a.maxArgsBytes {
			t.Fatalf("control fixture must stay under the shared cap: %d > %d", len(args), a.maxArgsBytes)
		}

		released, err := streamMandatoryToolCall(t, a, "control-1", args)
		if err != nil {
			t.Fatalf("finished: %v", err)
		}
		if fin.calls != 1 {
			t.Fatalf("control: mandatory finalizer invocations=%d want 1", fin.calls)
		}
		if string(fin.seen) != args {
			t.Fatalf("control: finalizer saw %d args bytes, want the complete %d-byte document", len(fin.seen), len(args))
		}
		if released != mandatoryExpandedArgsJSON(smallArgsBytes) {
			t.Fatalf("control: released arguments must be the expanded document; reserved alias present=%t",
				strings.Contains(released, mandatoryVirtualRoot))
		}
	})

	// RED: a completed call larger than the shared cap is released as the
	// original fragments, and the finalizer that declares mandatory expansion is
	// never invoked.
	t.Run("above_shared_cap_mandatory_expansion_must_not_be_bypassed", func(t *testing.T) {
		t.Parallel()
		fin := newMandatoryExpansionFin()
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared cap: got %d want %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		oversizedArgsBytes := a.maxArgsBytes + 4096
		args := mandatoryArgsJSON(oversizedArgsBytes)
		if len(args) <= a.maxArgsBytes {
			t.Fatalf("fixture must exceed the shared cap: %d <= %d", len(args), a.maxArgsBytes)
		}

		released, err := streamMandatoryToolCall(t, a, "oversize-1", args)
		aliasReleased := strings.Contains(released, mandatoryVirtualRoot)
		if fin.calls != 1 || string(fin.seen) != args {
			t.Fatalf("RED: requirements.md 4.6/4.5/8.4 - a completed tool call of %d args bytes above the shared %d-byte finalization cap must still reach a finalizer that declares mandatory expansion; finalizer invocations=%d complete_args_seen=%d/%d reserved_alias_released=%t error=%v",
				len(args), a.maxArgsBytes, fin.calls, len(fin.seen), len(args), aliasReleased, err)
		}
		if aliasReleased {
			t.Fatalf("RED: requirements.md 4.5 - the reserved virtual alias was released to the client without expansion")
		}
		if released != mandatoryExpandedArgsJSON(oversizedArgsBytes) {
			t.Fatalf("RED: requirements.md 4.5 - released arguments must be the expanded document, got %d bytes with reserved alias present=%t",
				len(released), strings.Contains(released, mandatoryVirtualRoot))
		}
		if err != nil {
			t.Fatalf("finished: %v", err)
		}
	})
}
