package runtime

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// Mandatory completeness requirements at the tool-call assembly chokepoint.
//
// A finalizer may publish the optional [toolcall.BufferingRequirement]
// capability to declare that it must receive the complete assembled tool-call
// arguments before the call may be released. The shared assembly limit cannot
// express that on its own: past the shared limit the assembler historically
// gave up, replayed the original stream fragments, and never ran any finalizer,
// which silently bypassed a completeness requirement.
//
// This file holds the assembler-side projection of that capability. It is
// feature-neutral: it names argument size and overflow behavior only, never a
// concrete feature, payload domain, or target namespace.

// Bounded classification codes for a completed tool call the assembler refuses
// closed because a declared mandatory completeness requirement could not be
// honored. They are the whole classification payload of the typed error: no
// path, alias, workspace tag, argument content, tool-call ID, or leg identity
// ever appears in it.
const (
	// ReasonMandatoryBufferingOverflow marks a call whose assembled arguments
	// exceed a bound a finalizer declared together with
	// [toolcall.OverflowReject].
	ReasonMandatoryBufferingOverflow = "mandatory_buffering_overflow"

	// ReasonMandatoryBufferingDeclarationInvalid marks a call refused because a
	// finalizer published a present-but-unusable [toolcall.BufferingSpec].
	ReasonMandatoryBufferingDeclarationInvalid = "mandatory_buffering_declaration_invalid"

	// ReasonMandatoryBufferingIncomplete marks a call refused because an
	// unrelated finalizer failed or produced an unusable result before a
	// finalizer with a declared mandatory completeness requirement could decide
	// on the complete arguments.
	ReasonMandatoryBufferingIncomplete = "mandatory_buffering_incomplete"
)

// ErrMandatoryBuffering is the stable root error for a completed tool call the
// assembler refused closed, so that no possibly alias-bearing argument reaches
// the client without the finalizer that must decide on it having decided.
var ErrMandatoryBuffering = errors.New("tool call assembler: mandatory buffering requirement not honored")

// MandatoryBufferingError reports that the tool-call assembler refused exactly
// one completed tool call closed at the assembly chokepoint. It is the typed
// counterpart of the [toolcall.RejectError] a finalizer returns for its own
// decision, covering the three cases the assembler itself decides: an assembled
// call past a declared mandatory bound with [toolcall.OverflowReject], a
// present-but-unusable mandatory declaration, and an unrelated finalizer
// failing before a declared requirement could be decided
// ([ReasonMandatoryBufferingIncomplete]).
//
// It is content-free by construction. FinalizerID and ToolCallID are carried
// for programmatic classification only and are deliberately absent from
// Error(), exactly as in [toolcall.RejectError].
type MandatoryBufferingError struct {
	// Reason is one of [ReasonMandatoryBufferingOverflow],
	// [ReasonMandatoryBufferingDeclarationInvalid], or
	// [ReasonMandatoryBufferingIncomplete].
	Reason string

	// FinalizerID identifies the finalizer whose declaration decided the
	// refusal, for diagnostics only.
	FinalizerID string

	// ToolCallID is the refused call's identifier, for classification only.
	ToolCallID string

	// MaxArgsBytes is the effective assembly bound that was exceeded. It is
	// zero for a declaration that was unusable at any bound.
	MaxArgsBytes int
}

func (e *MandatoryBufferingError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrMandatoryBuffering.Error()
	}
	return fmt.Sprintf("%s (%s)", ErrMandatoryBuffering, e.Reason)
}

func (e *MandatoryBufferingError) Is(target error) bool { return target == ErrMandatoryBuffering }

// IsMandatoryBufferingError reports whether err is or wraps a
// [MandatoryBufferingError].
func IsMandatoryBufferingError(err error) bool {
	var mbe *MandatoryBufferingError
	return errors.As(err, &mbe)
}

// mandatoryBuffering is the per-attempt projection of every finalizer that
// publishes [toolcall.BufferingRequirement], computed once when the assembler
// is built from the already sorted finalizer list.
type mandatoryBuffering struct {
	// assemblyMaxArgsBytes is the effective assembly bound: the largest
	// completed call this assembler buffers so a mandatory finalizer can be
	// invoked on it. It is the shared legacy bound raised to the widest
	// applicable declared bound, never lowered, and never above
	// [lipapi.MaxEventDeltaBytes].
	assemblyMaxArgsBytes int

	// rejectPastBound records that at least one applicable declaration requires
	// [toolcall.OverflowReject], so an assembled call past the effective bound
	// must be refused instead of replayed as its original fragments.
	rejectPastBound bool

	// rejectFinalizerID is the deterministic first declaring finalizer that set
	// rejectPastBound, for diagnostics only.
	rejectFinalizerID string

	// declaredCount is how many finalizers satisfy the capability assertion at
	// all. Zero means no finalizer opted in, so nothing about the pre-existing
	// assembly behavior changes.
	declaredCount int

	// mandatoryBoundDeclared records that at least one declaration is a
	// well-formed mandatory completeness requirement. It is the only thing that
	// turns on the requirement 4.6 failure clause: a call may not fall back to
	// replaying its original fragments while such a requirement is still
	// undecided. A zero-spec opt-in and a present-but-malformed declaration
	// never set it.
	mandatoryBoundDeclared bool

	// invalidDeclaration records that a finalizer published the capability with
	// a [toolcall.BufferingSpec] that does not validate.
	invalidDeclaration bool

	// invalidFinalizerID is the deterministic first finalizer with an unusable
	// declaration, for diagnostics only.
	invalidFinalizerID string
}

// resolveMandatoryBuffering projects the optional capability declarations onto
// one effective assembly bound and one refusal policy.
//
// It branches on the capability assertion itself, never on
// [toolcall.BufferingSpec.DeclaresMandatoryBound] alone, because a declaration
// that is PRESENT but malformed is a publisher error rather than an absent
// capability: degrading it to the legacy pass-through would downgrade a
// mandatory completeness requirement into optional handling, the single
// direction requirements 4.5, 4.6, and 8.3 forbid. Such a declaration is
// therefore recorded as invalid and its unusable bound is never used to raise
// the assembly bound, and the affected tool call is refused closed.
//
// legacyMaxArgsBytes is the already clamped shared assembly bound. It is the
// floor of the effective bound, so raising the bound for a mandatory finalizer
// can never narrow what an ordinary finalizer already receives, and no
// finalizer's own size policy is read or widened here.
func resolveMandatoryBuffering(finalizers []toolcall.Finalizer, legacyMaxArgsBytes int) mandatoryBuffering {
	mb := mandatoryBuffering{assemblyMaxArgsBytes: legacyMaxArgsBytes}
	for _, fin := range finalizers {
		if fin == nil {
			continue
		}
		req, ok := fin.(toolcall.BufferingRequirement)
		if !ok {
			// Case 1: the finalizer never opted in. Pre-existing behavior.
			continue
		}
		mb.declaredCount++
		spec := req.ToolCallBufferingRequirement()
		if err := spec.Validate(); err != nil {
			// Case 3: opted in, but the declaration cannot bound anything.
			if !mb.invalidDeclaration {
				mb.invalidDeclaration = true
				mb.invalidFinalizerID = fin.ID()
			}
			continue
		}
		if !spec.DeclaresMandatoryBound() {
			// Opted in with the zero spec: observationally identical to case 1.
			continue
		}
		// Case 2: a well-formed mandatory bound applies.
		mb.mandatoryBoundDeclared = true
		if spec.MaxArgsBytes > mb.assemblyMaxArgsBytes {
			mb.assemblyMaxArgsBytes = spec.MaxArgsBytes
		}
		if spec.Overflow == toolcall.OverflowReject && !mb.rejectPastBound {
			mb.rejectPastBound = true
			mb.rejectFinalizerID = fin.ID()
		}
	}
	if mb.assemblyMaxArgsBytes > lipapi.MaxEventDeltaBytes {
		mb.assemblyMaxArgsBytes = lipapi.MaxEventDeltaBytes
	}
	return mb
}

// failClosedPastBound reports whether an assembled call past the effective
// bound must be refused rather than replayed as its original fragments. An
// unusable declaration qualifies: no bound could ever satisfy it, so releasing
// the fragments would be the very bypass the capability exists to prevent.
func (m mandatoryBuffering) failClosedPastBound() bool {
	return m.rejectPastBound || m.invalidDeclaration
}

// declaresMandatoryBound reports whether fin published a well-formed mandatory
// completeness requirement. It is the per-call counterpart of case 2 in
// [resolveMandatoryBuffering], used to learn that the declaring finalizer has
// now been invoked.
//
// A present-but-malformed declaration answers false here because
// finalizeCall already refuses such a call closed before the loop runs, so it
// can never reach this point.
func declaresMandatoryBound(fin toolcall.Finalizer) bool {
	req, ok := fin.(toolcall.BufferingRequirement)
	if !ok {
		return false
	}
	return req.ToolCallBufferingRequirement().DeclaresMandatoryBound()
}

// rejection builds the typed, content-free refusal for one tool call.
func (m mandatoryBuffering) rejection(toolCallID string) error {
	if m.invalidDeclaration {
		return &MandatoryBufferingError{
			Reason:      ReasonMandatoryBufferingDeclarationInvalid,
			FinalizerID: m.invalidFinalizerID,
			ToolCallID:  toolCallID,
		}
	}
	return &MandatoryBufferingError{
		Reason:       ReasonMandatoryBufferingOverflow,
		FinalizerID:  m.rejectFinalizerID,
		ToolCallID:   toolCallID,
		MaxArgsBytes: m.assemblyMaxArgsBytes,
	}
}

// rejectionIncomplete builds the typed, content-free refusal for a call whose
// declared mandatory completeness requirement could not be decided because an
// unrelated finalizer failed first (requirement 4.6). No bound was exceeded, so
// MaxArgsBytes stays zero and FinalizerID names the declaring finalizer.
func (m mandatoryBuffering) rejectionIncomplete(toolCallID string) error {
	finalizerID := m.rejectFinalizerID
	if finalizerID == "" {
		finalizerID = m.invalidFinalizerID
	}
	return &MandatoryBufferingError{
		Reason:      ReasonMandatoryBufferingIncomplete,
		FinalizerID: finalizerID,
		ToolCallID:  toolCallID,
	}
}

// refusal wraps a typed refusal with the chokepoint that produced it, so a
// caller can classify it with errors.Is/errors.As without parsing text.
func (a *toolCallAssembler) refusal(toolCallID string) error {
	if a == nil {
		return nil
	}
	return fmt.Errorf("tool call finalization: %w", a.mandatory.rejection(toolCallID))
}

// refusalIncomplete is refusal for an undecided mandatory requirement, with the
// same classification surface.
func (a *toolCallAssembler) refusalIncomplete(toolCallID string) error {
	if a == nil {
		return nil
	}
	return fmt.Errorf("tool call finalization: %w", a.mandatory.rejectionIncomplete(toolCallID))
}
