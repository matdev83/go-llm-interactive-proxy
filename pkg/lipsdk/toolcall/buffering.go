package toolcall

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Mandatory-buffer bound contract for declared tool-call completeness
// requirements.
//
// The requested default bound is 1 MiB and the configurable range is
// [64 KiB, lipapi.MaxEventDeltaBytes]: the floor is the smallest bound that can
// still request more than the pre-existing shared assembly default guarantees,
// and the ceiling is the hard canonical limit on a single streamed argument
// delta, above which no bound could ever be honored.
//
// A declared bound is a completeness requirement, not a repair budget: it never
// widens or narrows any other finalizer's own size policy.

// DefaultMandatoryMaxArgsBytes is the requested mandatory bound when a
// declaration does not name one.
const DefaultMandatoryMaxArgsBytes = 1 << 20

// MinMandatoryMaxArgsBytes is the lower bound of the configurable range.
const MinMandatoryMaxArgsBytes = 64 << 10

// MaxMandatoryMaxArgsBytes is the upper bound of the configurable range and is
// the hard canonical ceiling: a single streamed argument delta can never exceed
// lipapi.MaxEventDeltaBytes, so a larger declared bound is unsatisfiable.
const MaxMandatoryMaxArgsBytes = lipapi.MaxEventDeltaBytes

// OverflowPolicy selects what must happen to a completed tool call whose
// assembled arguments exceed a declared mandatory bound.
//
// A policy is a declaration of intent. Honoring it is the assembler chokepoint's
// responsibility; a finalizer that only records the declaration changes nothing
// by itself.
type OverflowPolicy string

const (
	// OverflowPassThrough keeps the pre-existing behavior past the declared
	// bound: the call is released as the original argument fragments without the
	// declaring finalizer being invoked. This is the safe default for a
	// declaration whose policy is left unspecified.
	OverflowPassThrough OverflowPolicy = "pass_through"

	// OverflowReject fails the tool call closed past the declared bound instead
	// of releasing it, so a finalizer that cannot decide on incomplete
	// arguments never lets them reach the client.
	OverflowReject OverflowPolicy = "reject"
)

// BufferingRequirement is the optional capability a finalizer implements when it
// must receive the complete assembled tool-call arguments before the call may be
// released.
//
// It is deliberately NOT part of [Finalizer]: adding a required method would
// break every already-shipped finalizer. Consumers read it with a plain type
// assertion and fall back to pre-existing behavior when it is absent:
//
//	if req, ok := f.(toolcall.BufferingRequirement); ok {
//		spec := req.ToolCallBufferingRequirement()
//		if spec.DeclaresMandatoryBound() {
//			// a declared completeness requirement applies to this finalizer
//		}
//	}
//
// A finalizer that does not implement it keeps exactly its pre-existing
// buffering behavior. A finalizer that implements it but returns the zero
// [BufferingSpec] also declares no requirement, so opting in and returning the
// zero spec is observationally identical to not implementing the capability at
// all.
//
// The contract is feature-neutral: it names argument size and overflow
// behavior only, never any concrete feature, payload domain, or target
// namespace.
type BufferingRequirement interface {
	// ToolCallBufferingRequirement returns the declared completeness
	// requirement. It must be deterministic and side-effect free: it is read
	// during composition and must not depend on request state.
	ToolCallBufferingRequirement() BufferingSpec
}

// BufferingSpec is a declared completeness requirement: the largest completed
// tool call, in argument bytes, whose complete arguments this finalizer requires
// in order to decide, plus what must happen beyond that bound.
type BufferingSpec struct {
	// MaxArgsBytes is the declared mandatory bound. Zero declares no
	// requirement, which is the same as not implementing
	// [BufferingRequirement] at all.
	MaxArgsBytes int

	// Overflow selects the required behavior past MaxArgsBytes. The empty value
	// is unspecified and means [OverflowPassThrough], which is the
	// pre-existing behavior; it is not valid without a declared bound.
	Overflow OverflowPolicy
}

// DeclaresMandatoryBound reports whether this spec is a well-formed declaration
// of a mandatory completeness requirement.
//
// A malformed declaration deliberately reports false, so an unusable bound can
// never silently change behavior: consumers fall back to their pre-existing
// handling exactly as they do for a finalizer that never opted in.
//
// Consumers must not treat that fallback as sufficient on its own. A
// declaration that is PRESENT but malformed is a publisher error, not an
// absent capability: treating it as "did not opt in" would downgrade a
// mandatory completeness requirement into optional pass-through handling.
// A consumer that enforces a mandatory bound must therefore distinguish the
// two by branching on the capability assertion itself, and when the assertion
// succeeds while [BufferingSpec.Validate] reports an error it must fail the
// affected call closed rather than degrade.
func (s BufferingSpec) DeclaresMandatoryBound() bool {
	return s.MaxArgsBytes > 0 && s.Validate() == nil
}

// Validate reports whether this spec is a well-formed declaration.
//
// The zero spec is valid and declares nothing. Otherwise the bound must lie
// inside [MinMandatoryMaxArgsBytes, MaxMandatoryMaxArgsBytes] and the policy
// must be either unspecified, [OverflowPassThrough], or [OverflowReject]; a
// policy without a declared bound is rejected because it cannot bound anything.
//
// Both a feature's configuration validation and a consumer reading a
// declaration should call this rather than re-deriving the range, so the
// configurable range has exactly one definition.
func (s BufferingSpec) Validate() error {
	if s.MaxArgsBytes == 0 {
		if s.Overflow != "" {
			return fmt.Errorf("toolcall: buffering overflow policy %q declared without a mandatory max args bound", s.Overflow)
		}
		return nil
	}
	if s.MaxArgsBytes < MinMandatoryMaxArgsBytes || s.MaxArgsBytes > MaxMandatoryMaxArgsBytes {
		return fmt.Errorf("toolcall: mandatory max args bound %d outside the configurable range [%d, %d]",
			s.MaxArgsBytes, MinMandatoryMaxArgsBytes, MaxMandatoryMaxArgsBytes)
	}
	switch s.Overflow {
	case "", OverflowPassThrough, OverflowReject:
		return nil
	default:
		return fmt.Errorf("toolcall: unknown mandatory buffering overflow policy %q", s.Overflow)
	}
}
