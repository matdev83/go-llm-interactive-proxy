package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair/repair"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// Spec: b-leg-path-virtualization Task 7.3, the feature-integration half of the
// tool-call-repair <-> path-expansion composition. Requirements 8.4 and 8.5 and
// design.md sections "Existing Architecture and Placement" (step 11 response
// tool-call assembly/finalization, before step 12 tool policies/reactors),
// "6. Mandatory Buffering / Completeness Contract" ("do not globally raise
// tool-call-repair's own repair budget"), "7. Path Expansion Finalizer" (step 4
// parse completed ArgsJSON; step 9 validate rewritten JSON; step 10 assembler
// synthesizes the canonical rewritten lifecycle), and "Testing Strategy".
//
// WHAT THIS FILE OWNS. requirements.md 8.4 has two clauses: mandatory path
// expansion "shall receive valid completed JSON" and "shall not be bypassed by
// tool-call-repair size policy". The first is an ORDERING contract, the second a
// BOUNDING contract.
//
// The ordering contract is realized entirely by Finalizer.Order(). There is no
// core rule that reorders finalizers, and adding one was explicitly rejected in
// review of Task 7.2: it overrode the authoritative numeric Order() contract in
// pkg/lipsdk/toolcall/sort.go, had no present value, and closed nothing, because
// finalizeCall's err/invalid-envelope/unknown-action fallbacks all replay
// buf.originals. finalizeCall therefore iterates a.finalizers exactly as
// toolcall.MaterializeSorted produced it. This file is the missing evidence for
// that hand-off: it proves the composition is reachable and correct using only
// order numbers, so Task 8.1 knows precisely what its finalizer must declare.
//
// The expansion finalizer Task 8.1 will ship does not exist yet, so it is
// simulated by the existing test stand-in mandatoryExpansionFin, which already
// publishes the REAL shipped toolcall.BufferingRequirement capability. Every
// other participant is the real shipped tool-call-repair finalizer from
// internal/plugins/features/toolcallrepair, at its production order and with its
// production 64 KiB repair budget. No production file changes here.
//
// NOT RE-COVERED HERE. Task 7.2 already owns the assembler-level repair-budget
// behavior with test doubles: TestToolCallAssembler_RaisedMandatoryBoundDoesNotRaiseRepairBudget
// (both subtests) and TestToolCallAssembler_UnrelatedFinalizerFailureCannotSkipMandatoryExpansion.
// The tests below are the composition angles those cannot reach: repair is a real
// shipped finalizer whose decisions are observed, and expansion is declared
// ABOVE repair rather than at its zero-value natural order.

// composedRepairProbe wraps the REAL shipped tool-call-repair finalizer so a
// composition test can observe exactly what repair decided without changing any
// of its behavior: ID() and Order() are promoted from the embedded production
// finalizer, so toolcall.MaterializeSorted sorts on the real production order
// value and Finalize is the real production repair. MaxArgsBytes is left at zero
// so repair.NewFinalizer applies its own shipped default repair budget; nothing
// here widens it.
type composedRepairProbe struct {
	*repair.Finalizer

	calls   int
	seen    []byte
	results []toolcall.Result
	lastErr error
}

func (p *composedRepairProbe) Finalize(
	ctx context.Context,
	call toolcall.CompletedCall,
	tool lipapi.ToolDef,
	catalog []lipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	res, err := p.Finalizer.Finalize(ctx, call, tool, catalog, meta)
	p.calls++
	p.seen = append(p.seen[:0], call.ArgsJSON...)
	p.results = append(p.results, res)
	p.lastErr = err
	return res, err
}

var _ toolcall.Finalizer = (*composedRepairProbe)(nil)

// newComposedRepairProbe builds the production repair finalizer at the given
// order and asserts the probe is observationally transparent: it must not
// accidentally publish a buffering requirement, because the real repair feature
// keeps its own size policy and never declares a mandatory completeness bound
// (design.md "6. Mandatory Buffering / Completeness Contract": do not globally
// raise tool-call-repair's own repair budget).
func newComposedRepairProbe(t *testing.T, order int) *composedRepairProbe {
	t.Helper()
	probe := &composedRepairProbe{Finalizer: repair.NewFinalizer(repair.FinalizerPolicy{
		ID:    repair.DefaultFinalizerID,
		Order: order,
	})}
	if _, ok := toolcall.Finalizer(probe.Finalizer).(toolcall.BufferingRequirement); ok {
		t.Fatal("the shipped tool-call-repair finalizer must not declare a mandatory buffering requirement")
	}
	if _, ok := toolcall.Finalizer(probe).(toolcall.BufferingRequirement); ok {
		t.Fatal("the probe must not change whether tool-call-repair declares a buffering requirement")
	}
	return probe
}

// onlyResult returns the single decision the real repair finalizer made.
func (p *composedRepairProbe) onlyResult(t *testing.T) toolcall.Result {
	t.Helper()
	if len(p.results) != 1 {
		t.Fatalf("tool-call-repair decisions=%d want exactly 1", len(p.results))
	}
	return p.results[0]
}

// newExpansionAboveRepairFin is the stand-in for the path-expansion finalizer
// Task 8.1 will ship: the same mandatory-declaring finalizer the assembler tests
// already use, declared at the first order strictly above the shipped
// tool-call-repair order. That single number is the whole mechanism.
func newExpansionAboveRepairFin() *mandatoryExpansionFin {
	fin := newMandatoryExpansionFin()
	fin.order = repair.DefaultFinalizerOrder + 1
	return fin
}

// TestToolCallRepairRunsBeforeMandatoryExpansionByOrderAlone is the hand-off
// evidence Task 8.1 needs. It proves, with zero core change, that declaring a
// FinalizerOrder above toolcallrepair.DefaultFinalizerOrder puts syntax repair
// ahead of mandatory path expansion, and that the expansion finalizer therefore
// observes completed VALID JSON (requirements.md 8.4 first clause, design.md
// section 7 step 4).
func TestToolCallRepairRunsBeforeMandatoryExpansionByOrderAlone(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	t.Run("materialize_sorted_orders_repair_before_expansion_from_order_alone", func(t *testing.T) {
		t.Parallel()
		expansion := newExpansionAboveRepairFin()
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)

		if repairFin.Order() != repair.DefaultFinalizerOrder {
			t.Fatalf("shipped repair order changed: got %d want %d", repairFin.Order(), repair.DefaultFinalizerOrder)
		}
		if expansion.Order() <= repairFin.Order() {
			t.Fatalf("the expansion declaration must sit strictly above the repair order: expansion=%d repair=%d",
				expansion.Order(), repairFin.Order())
		}

		// Registered in the OPPOSITE order on purpose, so only the Order()
		// contract can produce the composition.
		sorted := toolcall.MaterializeSorted([]toolcall.Finalizer{expansion, repairFin})
		if len(sorted) != 2 {
			t.Fatalf("materialized finalizers=%d want 2", len(sorted))
		}
		if sorted[0].ID() != repair.DefaultFinalizerID {
			t.Fatalf("sorted[0]=%q want the repair finalizer", sorted[0].ID())
		}
		if sorted[1].ID() != expansion.ID() {
			t.Fatalf("sorted[1]=%q want the expansion finalizer", sorted[1].ID())
		}

		// The same must hold through the assembler's own construction path,
		// which is the only ordering decision the runtime makes.
		a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if len(a.finalizers) != 2 {
			t.Fatalf("assembler finalizers=%d want 2", len(a.finalizers))
		}
		if a.finalizers[0].ID() != repair.DefaultFinalizerID || a.finalizers[1].ID() != expansion.ID() {
			t.Fatalf("assembler iteration order changed: [%s %s]",
				a.finalizers[0].ID(), a.finalizers[1].ID())
		}
	})

	t.Run("malformed_json_is_repaired_before_expansion_observes_it", func(t *testing.T) {
		t.Parallel()
		expansion := newExpansionAboveRepairFin()
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}

		// A legacy PartJSON tool call: the model emitted malformed JSON whose
		// closing quote and brace never arrived, delivered as stream deltas cut
		// through the reserved alias so no single fragment is valid JSON.
		args := mandatoryMalformedArgsJSON(4 * 1024)
		repairedDocument := mandatoryArgsJSON(4 * 1024)
		if json.Valid([]byte(args)) {
			t.Fatal("fixture must be malformed JSON")
		}
		if len(args) >= repair.DefaultMaxArgsBytes {
			t.Fatalf("fixture must stay inside the repair budget: %d >= %d", len(args), repair.DefaultMaxArgsBytes)
		}
		if len(args) > a.mandatory.assemblyMaxArgsBytes {
			t.Fatalf("fixture must stay inside the declared mandatory bound: %d > %d",
				len(args), a.mandatory.assemblyMaxArgsBytes)
		}

		released, err := streamMandatoryToolCall(t, a, "repair-then-expand", args)
		if err != nil {
			t.Fatalf("the composition must not fail the call: %v", err)
		}

		// The shipped repair finalizer really ran first and really decided.
		if repairFin.calls != 1 {
			t.Fatalf("tool-call-repair invocations=%d want 1", repairFin.calls)
		}
		if repairFin.lastErr != nil {
			t.Fatalf("the shipped repair finalizer must not surface a Go error to the assembler")
		}
		if !bytes.Equal(repairFin.seen, []byte(args)) {
			t.Fatalf("repair must observe the malformed document: saw %d bytes want %d",
				len(repairFin.seen), len(args))
		}
		decision := repairFin.onlyResult(t)
		if decision.Action != toolcall.ActionRewrite || decision.ReasonCode != toolcall.ReasonSyntaxRepaired {
			t.Fatalf("repair decision action=%v reason=%q want rewrite / syntax_repaired",
				decision.Action, decision.ReasonCode)
		}
		if !json.Valid(decision.ArgsJSON) {
			t.Fatalf("repair must emit valid JSON: action=%v reason=%q", decision.Action, decision.ReasonCode)
		}

		// Requirement 8.4 first clause: the expansion finalizer receives the
		// REPAIRED document, not the malformed one.
		if expansion.calls != 1 {
			t.Fatalf("expansion invocations=%d want 1", expansion.calls)
		}
		if len(expansion.seen) != len(repairedDocument) {
			t.Fatalf("expansion must receive the completed repaired document: saw %d bytes want %d",
				len(expansion.seen), len(repairedDocument))
		}
		if !json.Valid(expansion.seen) {
			t.Fatal("expansion received invalid JSON: repair did not run first")
		}
		if !bytes.Equal(expansion.seen, []byte(repairedDocument)) {
			t.Fatalf("expansion must observe the repaired bytes, not the original fragments: saw %d valid bytes",
				len(expansion.seen))
		}

		// Requirement 8.5 plus design.md section 7 steps 9/10: the released
		// lifecycle is the assembler's synthesized rewrite of the validated
		// expanded document, with no reserved alias left in it.
		want := mandatoryExpandedArgsJSON(4 * 1024)
		if len(released) != len(want) {
			t.Fatalf("released %d bytes want %d", len(released), len(want))
		}
		if !json.Valid([]byte(released)) {
			t.Fatal("the released argument document must be valid JSON")
		}
		if strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("the reserved virtual alias reached the client unexpanded")
		}
		if !bytes.Equal([]byte(released), []byte(want)) {
			t.Fatal("released arguments are not the expanded repaired document")
		}
	})

	t.Run("expansion_declared_below_the_repair_order_sees_the_unrepaired_document", func(t *testing.T) {
		t.Parallel()
		// The negative control: the identical fixture and the identical two
		// shipped/stand-in participants, differing ONLY in the declared order.
		// It proves the observation above is genuinely order-driven, so it would
		// fail if finalizeCall stopped applying a repair rewrite or if the sort
		// contract were inverted.
		expansion := newMandatoryExpansionFin()
		expansion.order = repair.DefaultFinalizerOrder - 1
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}

		args := mandatoryMalformedArgsJSON(4 * 1024)
		released, err := streamMandatoryToolCall(t, a, "expand-then-repair", args)
		if err != nil {
			t.Fatalf("the composition must not fail the call: %v", err)
		}
		if expansion.calls != 1 {
			t.Fatalf("expansion invocations=%d want 1", expansion.calls)
		}
		if json.Valid(expansion.seen) {
			t.Fatal("with expansion ordered first it must observe the malformed document, not a repaired one")
		}
		if !bytes.Equal(expansion.seen, []byte(args)) {
			t.Fatalf("expansion must observe the original fragments: saw %d bytes want %d",
				len(expansion.seen), len(args))
		}
		// Repair still ran and still produced valid JSON, so the document the
		// client receives is syntactically repaired but never expanded: the
		// reserved alias is still there. That is the composition difference the
		// order declaration buys, stated in the negative.
		if repairFin.calls != 1 {
			t.Fatalf("tool-call-repair invocations=%d want 1", repairFin.calls)
		}
		if !json.Valid([]byte(released)) {
			t.Fatal("repair must still repair the document when it runs second")
		}
		if !strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("negative control: with expansion ordered first the alias must remain unexpanded")
		}
		if strings.Contains(released, mandatoryRealRoot) {
			t.Fatal("negative control: expansion must not have run at all in this ordering")
		}
	})
}

// TestToolCallRepairSizePolicyCannotSkipMandatoryExpansion is requirements.md
// 8.4's second clause: tool-call-repair's own size policy may decline a large
// call, but that decline must never skip mandatory expansion. The repair budget
// is a repair budget, not the assembler's assembly bound
// (design.md "6. Mandatory Buffering / Completeness Contract").
//
// The boundary probe below is what pins the budget as unchanged BEHAVIOR: at the
// exact repair budget the shipped finalizer still decides, one byte past it the
// shipped finalizer declines with args_too_large, and both sit far below the
// effective assembly bound the mandatory declaration raised. That is stronger
// than re-asserting the constant, and it complements rather than repeats Task
// 7.2's assembler-level constant equality subtest.
func TestToolCallRepairSizePolicyCannotSkipMandatoryExpansion(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	newComposition := func(t *testing.T) (*toolCallAssembler, *composedRepairProbe, *mandatoryExpansionFin) {
		t.Helper()
		expansion := newExpansionAboveRepairFin()
		repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
		a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		// The declaration raises the effective assembly bound only; the shared
		// legacy bound keeps its pre-existing value and no finalizer's own size
		// policy is read or widened here.
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared legacy bound changed: got %d want %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
			t.Fatalf("effective assembly bound: got %d want the declared %d",
				a.mandatory.assemblyMaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes <= repair.DefaultMaxArgsBytes {
			t.Fatalf("the effective assembly bound must stay above the repair budget: %d <= %d",
				a.mandatory.assemblyMaxArgsBytes, repair.DefaultMaxArgsBytes)
		}
		return a, repairFin, expansion
	}

	t.Run("repair_declines_past_its_own_budget_and_expansion_still_runs", func(t *testing.T) {
		t.Parallel()
		a, repairFin, expansion := newComposition(t)

		// Malformed and past the shipped repair budget, so the shipped repair
		// finalizer declines: this is the one shape where syntax repair does NOT
		// happen, and mandatory expansion must still be reached.
		args := mandatoryMalformedArgsJSON(repair.DefaultMaxArgsBytes + 8*1024)
		if len(args) <= repair.DefaultMaxArgsBytes {
			t.Fatalf("fixture must exceed the repair budget: %d <= %d", len(args), repair.DefaultMaxArgsBytes)
		}
		if len(args) > a.mandatory.assemblyMaxArgsBytes {
			t.Fatalf("fixture must stay inside the declared mandatory bound: %d > %d",
				len(args), a.mandatory.assemblyMaxArgsBytes)
		}

		released, err := streamMandatoryToolCall(t, a, "repair-declines", args)
		if err != nil {
			t.Fatalf("a repair size-policy decline must not fail the call: %v", err)
		}

		// The decline is observable and is a decline, not a silent pass-through
		// of an uninspected document.
		if repairFin.calls != 1 {
			t.Fatalf("tool-call-repair invocations=%d want 1", repairFin.calls)
		}
		decision := repairFin.onlyResult(t)
		if decision.Action != toolcall.ActionPass || decision.ReasonCode != toolcall.ReasonArgsTooLarge {
			t.Fatalf("repair must decline past its own budget: action=%v reason=%q",
				decision.Action, decision.ReasonCode)
		}

		// Requirement 8.4 second clause: expansion was NOT skipped. The
		// declaring finalizer ran exactly once on the COMPLETE document, which
		// is only reachable because the assembler buffers past the repair
		// budget up to the declared mandatory bound.
		if expansion.calls != 1 {
			t.Fatalf("requirements.md 8.4 - repair's size policy bypassed mandatory expansion: invocations=%d",
				expansion.calls)
		}
		if len(expansion.seen) != len(args) {
			t.Fatalf("requirements.md 8.4 - expansion must receive the complete document: saw %d bytes want %d",
				len(expansion.seen), len(args))
		}
		if !bytes.Equal(expansion.seen, []byte(args)) {
			t.Fatal("requirements.md 8.4 - expansion received a different document than the model emitted")
		}

		// The stand-in expansion finalizer declines to act on incomplete JSON,
		// so the pre-existing replay applies and no assembler refusal is raised:
		// the repair budget is neither the assembly bound nor a completeness
		// requirement. Whether a malformed document still carrying a reserved
		// alias may be released is the real feature finalizer's Task 8.1/8.2
		// decision, not this composition's; no alias content is asserted here.
		if released != args {
			t.Fatalf("released %d bytes want the %d replayed originals", len(released), len(args))
		}
	})

	t.Run("the_effective_assembly_bound_is_not_the_repair_budget", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name       string
			argsBytes  int
			wantReason string
		}{
			{
				name:       "at_the_repair_budget_the_shipped_finalizer_still_decides",
				argsBytes:  repair.DefaultMaxArgsBytes,
				wantReason: toolcall.ReasonValidPassThrough,
			},
			{
				name:       "one_byte_past_the_repair_budget_it_declines",
				argsBytes:  repair.DefaultMaxArgsBytes + 1,
				wantReason: toolcall.ReasonArgsTooLarge,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				a, repairFin, expansion := newComposition(t)

				// A syntactically valid, alias-bearing document sized so the ONLY
				// thing that can decide the call is repair's own size policy.
				args := mandatoryArgsJSON(tc.argsBytes)
				if len(args) != tc.argsBytes {
					t.Fatalf("fixture sizing: got %d bytes want %d", len(args), tc.argsBytes)
				}
				if len(args) > a.mandatory.assemblyMaxArgsBytes {
					t.Fatalf("the probe must never hit the assembler bound: %d > %d",
						len(args), a.mandatory.assemblyMaxArgsBytes)
				}

				released, err := streamMandatoryToolCall(t, a, "repair-budget-"+tc.name, args)
				if err != nil {
					t.Fatalf("a repair size-policy decline must not fail the call: %v", err)
				}
				decision := repairFin.onlyResult(t)
				if decision.ReasonCode != tc.wantReason {
					t.Fatalf("at %d bytes repair reason=%q want %q",
						len(args), decision.ReasonCode, tc.wantReason)
				}
				if decision.Action != toolcall.ActionPass {
					t.Fatalf("at %d bytes repair action=%v want pass", len(args), decision.Action)
				}

				// Either way the declaring finalizer decided on the complete
				// document and its expansion is what reaches the client, so the
				// repair budget governs only whether repair did any work.
				if expansion.calls != 1 || len(expansion.seen) != len(args) {
					t.Fatalf("requirements.md 8.4 - at %d bytes expansion must still run on the complete document: invocations=%d seen=%d",
						len(args), expansion.calls, len(expansion.seen))
				}
				if strings.Contains(released, mandatoryVirtualRoot) {
					t.Fatalf("requirements.md 4.5 - at %d bytes the reserved alias reached the client unexpanded", len(args))
				}
				if !json.Valid([]byte(released)) {
					t.Fatalf("requirements.md 8.5 - at %d bytes the released document is not valid JSON", len(args))
				}
			})
		}
	})
}

// TestToolCallRepairComposesWithInvalidFinalizerRewriteSemantics characterizes
// requirements.md 8.5 for this composition: every mutation a finalizer performs
// is validated before the next finalizer sees it and before anything is
// released. The assembler has exactly three unusable-result fallbacks (a Go
// error, an invalid rewrite envelope, and an unknown action) and Task 7.2 added
// one chokepoint, undecidedMandatoryReplay, on top of the pre-existing
// behavior. Both boundaries are characterized here relative to the shipped
// repair finalizer, which is what makes them composition facts rather than
// assembler-internal ones.
func TestToolCallRepairComposesWithInvalidFinalizerRewriteSemantics(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	// unusableShapes are the four ways an ordinary, non-declaring finalizer can
	// return something the assembler cannot use. This finalizer never stands in
	// for a declaring one and never publishes a buffering requirement.
	unusableShapes := []struct {
		name string
		res  toolcall.Result
		err  error
	}{
		{
			name: "invalid_rewrite_envelope_json",
			res: toolcall.Result{
				Action:     toolcall.ActionRewrite,
				ToolName:   mandatoryToolName,
				ArgsJSON:   []byte(`{"path":`),
				ReasonCode: toolcall.ReasonValidPassThrough,
			},
		},
		{
			name: "invalid_rewrite_envelope_missing_tool_name",
			res: toolcall.Result{
				Action:     toolcall.ActionRewrite,
				ArgsJSON:   []byte(`{}`),
				ReasonCode: toolcall.ReasonValidPassThrough,
			},
		},
		{
			name: "invalid_rewrite_envelope_missing_args",
			res: toolcall.Result{
				Action:     toolcall.ActionRewrite,
				ToolName:   mandatoryToolName,
				ReasonCode: toolcall.ReasonValidPassThrough,
			},
		},
		{
			name: "unknown_action",
			res: toolcall.Result{
				Action:     toolcall.Action(999),
				ReasonCode: toolcall.ReasonValidPassThrough,
			},
		},
		{
			name: "finalizer_error",
			err:  context.Canceled,
		},
	}

	const (
		// BeforeRepairOrder is below the shipped tool-call-repair order, so an
		// unusable result there short-circuits the whole composition.
		BeforeRepairOrder = repair.DefaultFinalizerOrder - 30
		// AfterExpansionOrder is above both the repair order and the mandatory
		// declaration, so the declaring finalizer has already run by then.
		AfterExpansionOrder = repair.DefaultFinalizerOrder + 2
	)

	t.Run("before_the_declaring_finalizer_the_call_is_refused_closed", func(t *testing.T) {
		t.Parallel()
		wantMessage := "tool call finalization: tool call assembler: mandatory buffering requirement not honored (" +
			ReasonMandatoryBufferingIncomplete + ")"

		for _, shape := range unusableShapes {
			t.Run(shape.name, func(t *testing.T) {
				t.Parallel()
				var ordinary toolcall.Finalizer = &unusableOrdinaryFin{order: BeforeRepairOrder, res: shape.res}
				if shape.err != nil {
					ordinary = &failingOrdinaryFin{order: BeforeRepairOrder}
				}
				if _, ok := ordinary.(toolcall.BufferingRequirement); ok {
					t.Fatal("fixture must be a finalizer without a declaration")
				}
				expansion := newExpansionAboveRepairFin()
				repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
				a := newToolCallAssembler([]toolcall.Finalizer{ordinary, repairFin, expansion}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}
				if !a.mandatory.mandatoryBoundDeclared {
					t.Fatal("a declaring finalizer must arm the undecided-mandatory chokepoint")
				}

				released, err := streamMandatoryToolCall(t, a, "unusable-"+shape.name, mandatoryArgsJSON(8*1024))

				// Requirements 4.6/8.4/8.5: an unusable result before the
				// declaring finalizer decides refuses the call closed, so no
				// possibly alias-bearing argument is released and the shipped
				// repair finalizer never gets to run on a document another
				// finalizer mangled.
				if released != "" {
					t.Fatalf("%d original argument bytes were replayed", len(released))
				}
				if strings.Contains(released, mandatoryVirtualRoot) {
					t.Fatal("the reserved alias was released without expansion")
				}
				if repairFin.calls != 0 {
					t.Fatalf("tool-call-repair must not run after an unusable earlier result: invocations=%d", repairFin.calls)
				}
				if expansion.calls != 0 {
					t.Fatalf("the declaring finalizer must not have decided: invocations=%d", expansion.calls)
				}
				var mbe *MandatoryBufferingError
				if !errors.As(err, &mbe) || mbe == nil {
					t.Fatalf("want a typed MandatoryBufferingError, got %v", err)
				}
				if !errors.Is(err, ErrMandatoryBuffering) || !IsMandatoryBufferingError(err) {
					t.Fatalf("refusal must classify through the sentinel, got %v", err)
				}
				if mbe.Reason != ReasonMandatoryBufferingIncomplete {
					t.Fatalf("reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingIncomplete)
				}
				if mbe.FinalizerID != expansion.ID() {
					t.Fatalf("refusal must name the declaring finalizer, got %q", mbe.FinalizerID)
				}
				if mbe.MaxArgsBytes != 0 {
					t.Fatalf("no bound was exceeded, so MaxArgsBytes must stay zero: got %d", mbe.MaxArgsBytes)
				}
				if err.Error() != wantMessage {
					t.Fatalf("refusal message must be the bounded, content-free classification")
				}
			})
		}
	})

	t.Run("residual_after_the_declaring_finalizer_ran_the_replay_fallback_still_applies", func(t *testing.T) {
		t.Parallel()
		// ACCEPTED TASK 7.2 RESIDUAL, owned by Task 8.2 / 12.2 review. Once the
		// declaring finalizer has been invoked, an unusable result from a
		// LATER finalizer keeps the pre-existing assembler fallback: replay the
		// original fragments, error-free. That is not asserted here as fixed and
		// must not be "fixed" by reordering finalizers; it is characterized so
		// the residual is visible at composition level. This is also why an
		// expansion finalizer must not merely declare a high order and rely on
		// ordering alone.
		for _, shape := range unusableShapes {
			t.Run(shape.name, func(t *testing.T) {
				t.Parallel()
				var ordinary toolcall.Finalizer = &unusableOrdinaryFin{order: AfterExpansionOrder, res: shape.res}
				if shape.err != nil {
					ordinary = &failingOrdinaryFin{order: AfterExpansionOrder}
				}
				expansion := newExpansionAboveRepairFin()
				repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
				a := newToolCallAssembler([]toolcall.Finalizer{expansion, ordinary, repairFin}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}

				args := mandatoryArgsJSON(8 * 1024)
				released, err := streamMandatoryToolCall(t, a, "residual-"+shape.name, args)

				if err != nil {
					t.Fatalf("a post-declaration failure keeps the pre-existing error-free replay: %v", err)
				}
				if expansion.calls != 1 {
					t.Fatalf("the declaring finalizer must have been invoked first: invocations=%d", expansion.calls)
				}
				if repairFin.calls != 1 {
					t.Fatalf("the shipped repair finalizer must have been invoked first: invocations=%d", repairFin.calls)
				}
				// The expansion that already happened is discarded, so the
				// reserved alias reaches the client. This is the accepted
				// residual, named here on purpose.
				if len(released) != len(args) {
					t.Fatalf("released %d bytes want the %d replayed originals", len(released), len(args))
				}
				if !strings.Contains(released, mandatoryVirtualRoot) {
					t.Fatal("accepted Task 7.2 residual: the post-declaration replay releases the original fragments")
				}
			})
		}
	})

	t.Run("the_shipped_repair_finalizer_never_returns_an_unusable_result", func(t *testing.T) {
		t.Parallel()
		// Across this composition the shipped repair finalizer must never trip
		// any of the three unusable-result fallbacks: it returns no Go error, no
		// unknown action, and every rewrite it does return is a non-empty tool
		// name with valid JSON arguments. That is what makes requirements.md 8.5
		// ("preserve canonical call/event validation after every mutation") hold
		// for the repair feature rather than merely for the assembler.
		for _, tc := range []struct {
			name string
			args string
		}{
			{name: "valid_document", args: mandatoryArgsJSON(4 * 1024)},
			{name: "malformed_document_repairable", args: mandatoryMalformedArgsJSON(4 * 1024)},
			{name: "malformed_document_past_the_repair_budget", args: mandatoryMalformedArgsJSON(repair.DefaultMaxArgsBytes + 4096)},
			{name: "empty_arguments", args: ""},
			{name: "truncated_root", args: `{"path":"/`},
			{name: "trailing_garbage", args: mandatoryArgsJSON(1024) + `garbage`},
			{name: "duplicate_members", args: `{"path":"/a","path":"/b"}`},
			{name: "non_object_root", args: `["/a"]`},
			{name: "null_root", args: `null`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				expansion := newExpansionAboveRepairFin()
				repairFin := newComposedRepairProbe(t, repair.DefaultFinalizerOrder)
				a := newToolCallAssembler([]toolcall.Finalizer{expansion, repairFin}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}

				released, err := streamMandatoryToolCall(t, a, "envelope-"+tc.name, tc.args)
				if repairFin.calls != 1 {
					t.Fatalf("tool-call-repair invocations=%d want 1", repairFin.calls)
				}
				if repairFin.lastErr != nil {
					t.Fatal("the shipped repair finalizer surfaced a Go error to the assembler")
				}
				decision := repairFin.onlyResult(t)
				if decision.ReasonCode == "" {
					t.Fatal("the shipped repair finalizer must always give a bounded reason code")
				}
				switch decision.Action {
				case toolcall.ActionPass, toolcall.ActionRewrite, toolcall.ActionReject:
				default:
					t.Fatalf("the shipped repair finalizer returned an action the assembler cannot use: %d",
						int(decision.Action))
				}
				if decision.Action == toolcall.ActionRewrite {
					// Mirrors the assembler's rewrite-envelope validation
					// directly instead of calling it, so a relaxation of that
					// production rule cannot silently weaken this assertion.
					if strings.TrimSpace(decision.ToolName) == "" {
						t.Fatalf("requirements.md 8.5 - a repair rewrite carried an empty tool name (reason=%q)",
							decision.ReasonCode)
					}
					if decision.ArgsJSON == nil {
						t.Fatalf("requirements.md 8.5 - a repair rewrite carried nil arguments (reason=%q)",
							decision.ReasonCode)
					}
					if !json.Valid(decision.ArgsJSON) {
						t.Fatalf("requirements.md 8.5 - a repair rewrite carried invalid JSON (reason=%q)",
							decision.ReasonCode)
					}
					if len(decision.ArgsJSON) > repair.DefaultMaxArgsBytes {
						t.Fatalf("requirements.md 8.4 - a repair rewrite grew the document past its own budget: %d > %d",
							len(decision.ArgsJSON), repair.DefaultMaxArgsBytes)
					}
				}
				if decision.Action != toolcall.ActionRewrite && decision.ArgsJSON != nil {
					t.Fatalf("a non-rewrite decision must not carry a mutated document: action=%d args_bytes=%d",
						int(decision.Action), len(decision.ArgsJSON))
				}
				// The composition consequence, asserted only once repair's own
				// result contract holds, so a repair regression is reported as a
				// repair regression rather than as an assembler refusal.
				if err != nil {
					t.Fatalf("the shipped repair finalizer must not fail the call: %v", err)
				}
				// Whatever repair decided, the call still went through the
				// assembler: once the expansion finalizer has seen valid JSON,
				// whatever leaves the assembler must be valid JSON too, whether
				// expansion rewrote it or passed it through.
				if expansion.calls == 1 && json.Valid(expansion.seen) && !json.Valid([]byte(released)) {
					t.Fatalf("requirements.md 8.5 - invalid JSON released after expansion saw valid JSON (released=%d bytes)",
						len(released))
				}
			})
		}
	})
}
