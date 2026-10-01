// Private, attempt-local control-call interception for task 4.2 of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Response
// Interception / Placement and Handler Semantics; requirements 3.5, 5.1-5.7,
// 11.1-11.3, 12.3-12.4).
//
// This file owns the interception seam only. It runs on the backend-event
// boundary after BTP and provider usage observation and before the ordinary
// tool-call assembler, so a proxy-owned control call never reaches ordinary
// tool-call finalizers, tool policy, tool reactors, response hooks, the client
// recorder, or client release, while operator BTP capture and provider
// accounting keep seeing the legitimate upstream control traffic. Ordinary events
// are returned unchanged, undelayed, and unbuffered.
//
// Ownership comes exclusively from the trusted activation the request path
// already froze. Nothing here re-reads a runtime snapshot, a provider ID() or
// Spec(), capability eligibility, request Extensions, or a mutable context value
// to decide response ownership.
//
// The capture and its normalized outcome live on the attempt session and are
// driven by the single backend Recv loop, exactly like the ordinary tool-call
// assembler this seam runs ahead of. This file therefore adds no lock, no timer,
// no goroutine, no global map, and no public extension metadata. Task 4.3 owns
// cancellation/loss cleanup and cross-attempt isolation; nothing here mutates the
// capture from a lifecycle callback, so no additional synchronization is needed
// for this wiring.
package runtime

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
)

// errControlHandlerUnavailable is the bounded failure for an attempt whose
// trusted activation cannot address a handler. It is a wiring defect rather than
// a protocol state: a live capture exists only for an active activation.
var errControlHandlerUnavailable = errors.New("runtime: control tool handler unavailable for this attempt")

// divertControlCall runs the private control-call interception for one backend
// event.
//
// swallowed=true means the event belongs to the private protocol path and must
// not continue on the ordinary tool path or reach the client. err is the bounded
// protocol failure the existing receive error ownership already handles. An
// absent capture — the ordinary no-provider, no-activation, or no-eligible-tool
// case — returns immediately with neither, so that path performs no hashing, no
// allocation, and no comparison per event.
func (p *responsePipeline) divertControlCall(ctx context.Context, attempt *attemptSession, ev lipapi.Event) (swallowed bool, err error) {
	if p == nil || attempt == nil || attempt.controlCapture == nil {
		return false, nil
	}
	if ev.Kind == lipapi.EventResponseFinished {
		attempt.closeControlCapture()
		return false, nil
	}
	obs := attempt.controlCapture.observe(ev)
	if !obs.Claimed() {
		return false, nil
	}
	if obs.Fatal() != nil {
		attempt.clearControlOutcome()
		return true, obs.Fatal()
	}
	if obs.Completed() {
		return true, attempt.handleControlCall(ctx, obs.Call())
	}
	if obs.Invalid() {
		attempt.clearControlOutcome()
	}
	return true, nil
}

// handleControlCall invokes the pinned generation control provider for the one
// captured, bounded completion and stores only the validated outcome on this
// attempt.
//
// The invocation never falls through to ordinary tool execution and never
// publishes client output: a validated completion stays private on this attempt
// until the later terminal owner reads it, and a provider failure clears any
// pending outcome before returning a bounded error to existing receive error
// ownership.
func (a *attemptSession) handleControlCall(ctx context.Context, call controltool.CompletedCall) error {
	if a == nil {
		return errControlHandlerUnavailable
	}
	if a.controlHandled {
		// One completion handoff at most. The capture already guarantees a single
		// Completed observation per response, so a second one is a wiring defect,
		// not a protocol state: revoke any outcome rather than re-invoke.
		a.clearControlOutcome()
		return errControlHandlerUnavailable
	}
	a.controlHandled = true
	if !a.controlTool.active() {
		a.clearControlOutcome()
		return errControlHandlerUnavailable
	}
	outcome, err := extensions.HandleControlTool(ctx, extensions.ControlToolHandleRequest{
		ProviderID:   a.controlTool.providerID,
		Provider:     a.controlTool.provider,
		Call:         call,
		Meta:         controlHandleMeta(a.controlTool.meta),
		MaxArgsBytes: a.controlTool.projection.MaxArgsBytes(),
	})
	if err != nil {
		a.clearControlOutcome()
		return err
	}
	a.controlOutcome = &outcome
	return nil
}

// controlHandleMeta hands the provider its own deep-owned copy of the frozen
// provenance.
//
// controltool.Meta carries views, and those views own backing slices and maps.
// Passing the activation's value straight to the provider would let a provider
// that writes through the view it was handed corrupt the activation's own
// provenance for the rest of the attempt. The existing request-path clone helpers
// are reused here rather than a second cloning convention, so the wire-level
// facts stay owned exactly as they were when the request path froze them.
func controlHandleMeta(meta controltool.Meta) controltool.Meta {
	meta.Scope = meta.Scope.Clone()
	meta.Session = cloneSessionView(meta.Session)
	meta.Workspace = cloneWorkspaceView(meta.Workspace)
	return meta
}

// closeControlCapture is the normal end of one response stream. A started control
// call that never finished is invalid, so any pending outcome is dropped; a call
// that already completed stays valid so the later terminal owner can still read
// it. The terminal event itself is ordinary and keeps streaming to the client.
func (a *attemptSession) closeControlCapture() {
	if a == nil || a.controlCapture == nil {
		return
	}
	if a.controlCapture.closeResponse().Invalid() {
		a.clearControlOutcome()
	}
}

// clearControlOutcome drops the private pending outcome. A malformed, duplicate,
// or multiple control sequence after a valid completion revokes that completion
// without ever invoking the provider a second time.
func (a *attemptSession) clearControlOutcome() {
	if a == nil {
		return
	}
	a.controlOutcome = nil
}
