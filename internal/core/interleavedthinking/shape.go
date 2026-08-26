package interleavedthinking

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ErrThinkerInstructionsMissing is returned when a thinker candidate is shaped
// without resolved thinker instructions. Requirement 3.3: thinker instructions
// that are configured but cannot be loaded or resolved must fail before upstream
// execution. The runtime resolves the instructions text before calling ShapeCall;
// an empty Instructions here means resolution failed or produced nothing.
var ErrThinkerInstructionsMissing = errors.New("interleavedthinking: thinker instructions required but missing")

// SessionSteeringGuidanceHeader is the plain-text header that introduces the
// steering block. It is used by the runtime when persisting the thinker memo
// as a conversation-view steering overlay; no XML and no special markers:
// models treat it as ordinary markdown.
const SessionSteeringGuidanceHeader = "[Session Steering Guidance]"

// MemoInjectionModeConversationView identifies the memo injection mode in
// diagnostics. Memos are persisted as backend-only steering overlays in the
// conversation-view store and injected by the runtime projection as standalone
// synthetic user messages at a fixed activation anchor (#391).
const MemoInjectionModeConversationView = "conversation_view_overlay"

const (
	// MemoInjectionModeTailAnchored is retained for source compatibility.
	// Deprecated: conversation-view projection no longer emits this mode.
	MemoInjectionModeTailAnchored = "tail_anchored"
	// MetadataSourceInterleavedThinking is retained for source compatibility.
	// Deprecated: conversation-view projection does not emit tail metadata.
	MetadataSourceInterleavedThinking = "interleaved_thinking"
	// MetadataKindThinkerMemoTail is retained for source compatibility.
	// Deprecated: conversation-view projection does not emit tail metadata.
	MetadataKindThinkerMemoTail = "thinker_memo_tail"
)

// ShapeConfig carries the resolved interleaved-thinking settings needed to shape
// a candidate call. Instructions is the resolved thinker planning text (loaded
// from the configured instructions source by the runtime). Non-thinker
// candidates do not require Instructions.
type ShapeConfig struct {
	Instructions string
	// StreamToClient is the visibility mode: "hidden" (default) or "visible".
	StreamToClient string
	// MaxMemoBytes bounds captured memo content. Zero uses the config default.
	MaxMemoBytes int
	// RegularTurnsRemaining is the memo injection budget for captured memos. Zero uses the default.
	RegularTurnsRemaining int
}

// ShapeInput is the input to ShapeCall.
//
// MemoStore, Scope, and MemoRef drive executor memo classification. They are
// optional: when MemoStore is nil or MemoRef is empty, executor candidates are
// returned unchanged (no memo available). Thinker candidates ignore them.
type ShapeInput struct {
	Call      lipapi.Call
	Candidate routing.AttemptCandidate
	Config    ShapeConfig

	// MemoStore is the bounded memo store used to load memo state for executor
	// candidates. May be nil when no memo state is available.
	MemoStore MemoStore
	// Scope identifies the authoritative session or A-leg that owns the memo.
	// Required when MemoStore is non-nil and MemoRef is set.
	Scope Scope
	// MemoRef points to the current memo for the session/A-leg. May be nil when
	// no memo has been captured yet.
	MemoRef *interleavedstate.MemoRef
	// SuppressVisibleMemo skips memo authorization when the memo was already
	// visible to the client on the immediate continuation executor open. Later
	// executor turns leave this false so previously visible memos can be
	// presented again.
	SuppressVisibleMemo bool
}

// MemoOutcome classifies executor memo shaping for bounded runtime diagnostics.
type MemoOutcome string

const (
	MemoOutcomeNone           MemoOutcome = ""
	MemoOutcomeInjected       MemoOutcome = "injected"
	MemoOutcomeExpired        MemoOutcome = "expired"
	MemoOutcomeSkippedVisible MemoOutcome = "skipped_visible"
	MemoOutcomeSkippedMissing MemoOutcome = "skipped_missing"
	MemoOutcomeSkippedEmpty   MemoOutcome = "skipped_empty"
	// MemoOutcomeSkippedDuplicate is retained for source compatibility.
	// Deprecated: the conversation-view store owns overlay de-duplication.
	MemoOutcomeSkippedDuplicate MemoOutcome = "skipped_duplicate"
)

// PendingMemoUpdate is the memo-store mutation that should be committed once a
// shaped executor attempt becomes authoritative.
type PendingMemoUpdate struct {
	Ref   interleavedstate.MemoRef
	State MemoState
}

// ShapeResult is the output of ShapeCall.
//
// MemoInjected is true when an executor candidate was authorized to receive the
// latest valid memo through conversation-view steering projection. MemoUpdate
// carries the budget decrement and injection count bump; callers commit it only
// after the shaped attempt actually opens or wins.
type ShapeResult struct {
	Call         lipapi.Call
	MemoInjected bool
	MemoUpdate   *PendingMemoUpdate
	MemoOutcome  MemoOutcome
}

// ShapeCall returns a per-candidate canonical call shaped for interleaved
// thinking.
//
// For thinker candidates it prepends the configured instructions to the
// system-style Instructions list and suppresses tools and tool-choice
// directives before capability checks and backend open.
//
// For executor candidates it classifies the latest valid memo and returns the
// pending budget mutation (decrement per presented turn, Requirement 5.3),
// reports expired memos (Requirement 5.4), and suppresses presentation on the
// immediate continuation executor when the memo was already visible to the
// client (Requirement 5.2). Later executor turns present the memo again.
// Shaping never mutates the conversation: since #391 the memo reaches executor
// context exclusively as a persistent backend-only steering overlay in the
// conversation-view store, projected by the runtime as a standalone synthetic
// user message at its fixed activation anchor.
//
// For RoleNone candidates it returns a deep clone of the input call unchanged.
// The returned call for a thinker or executor candidate always validates with
// [lipapi.Call.Validate].
//
// ShapeCall does not mutate the input Call.
func ShapeCall(ctx context.Context, in ShapeInput) (ShapeResult, error) {
	out := lipapi.CloneCall(in.Call)
	switch in.Candidate.InterleavedRole {
	case interleavedstate.RoleThinker:
		return shapeThinker(out, in)
	case interleavedstate.RoleExecutor:
		return shapeExecutor(ctx, out, in)
	default:
		return ShapeResult{Call: out}, nil
	}
}

func shapeThinker(out lipapi.Call, in ShapeInput) (ShapeResult, error) {
	instructions := strings.TrimSpace(in.Config.Instructions)
	if instructions == "" {
		return ShapeResult{}, ErrThinkerInstructionsMissing
	}
	prepend := lipapi.Message{
		Role:  lipapi.RoleSystem,
		Parts: []lipapi.Part{lipapi.TextPart(instructions)},
	}
	out.Instructions = append([]lipapi.Message{prepend}, out.Instructions...)
	out.Tools = nil
	out.ToolChoice = lipapi.ToolChoice{}
	if err := out.Validate(); err != nil {
		return ShapeResult{}, fmt.Errorf("interleavedthinking: shaped thinker call invalid: %w", err)
	}
	return ShapeResult{Call: out}, nil
}

func shapeExecutor(ctx context.Context, out lipapi.Call, in ShapeInput) (ShapeResult, error) {
	if in.MemoStore == nil || in.MemoRef == nil || in.MemoRef.Key == "" {
		return ShapeResult{Call: out, MemoOutcome: MemoOutcomeSkippedMissing}, nil
	}
	if err := ctx.Err(); err != nil {
		return ShapeResult{}, err
	}
	if in.Scope == "" {
		return ShapeResult{}, ErrEmptyScope
	}
	state, ok, err := in.MemoStore.Get(ctx, in.Scope, *in.MemoRef)
	if err != nil {
		return ShapeResult{}, fmt.Errorf("interleavedthinking: memo lookup: %w", err)
	}
	if !ok {
		return ShapeResult{Call: out, MemoOutcome: MemoOutcomeSkippedMissing}, nil
	}
	memo := strings.TrimSpace(state.Memo)
	if memo == "" {
		return ShapeResult{Call: out, MemoOutcome: MemoOutcomeSkippedEmpty}, nil
	}
	// Requirement 5.4: no remaining budget means the memo is expired.
	if state.RegularTurnsRemaining <= 0 {
		return ShapeResult{Call: out, MemoOutcome: MemoOutcomeExpired}, nil
	}
	// Requirement 5.2: suppress only the immediate continuation executor when
	// the memo was already visible to the client on this turn.
	if state.VisibleToClient && in.SuppressVisibleMemo {
		return ShapeResult{Call: out, MemoOutcome: MemoOutcomeSkippedVisible}, nil
	}
	state.InjectedCount++
	state.RegularTurnsRemaining--
	if state.RegularTurnsRemaining < 0 {
		state.RegularTurnsRemaining = 0
	}
	pending := &PendingMemoUpdate{Ref: *in.MemoRef, State: state}
	if err := out.Validate(); err != nil {
		return ShapeResult{}, fmt.Errorf("interleavedthinking: shaped executor call invalid: %w", err)
	}
	// No conversation mutation here (#391): the memo reaches executor context
	// exclusively as a persistent conversation-view steering overlay projected
	// by the runtime. Shaping only classifies the memo and authorizes the
	// budget decrement committed after the shaped attempt opens or wins.
	return ShapeResult{
		Call:         out,
		MemoInjected: true,
		MemoUpdate:   pending,
		MemoOutcome:  MemoOutcomeInjected,
	}, nil
}
