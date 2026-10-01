// Private, attempt-local control-call interception for
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Response
// Interception / Placement, Handler Semantics, and Concurrency and Lifecycle;
// requirements 3.5, 4.7, 5.1-5.7, 8.2-8.7, 10.3, 11.1-11.3, 12.3-12.5).
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
// The capture and its normalized outcome are attempt-local mutable state, owned
// by the attempt session under one short-held control lock (attemptSession.controlMu,
// documented at its declaration). The single backend Recv loop observes and hands
// off; attempt lifecycle cleanup — cancellation, Close, attempt loss, and
// replacement — disposes that same state from whichever goroutine owns the
// transition. The lock therefore covers capture observation, handoff bookkeeping,
// outcome storage, and disposal, and nothing else: no Provider.Handle call, no
// backend Recv/Cancel/Close, and no terminal effect ever runs while it is held, so
// cleanup can never wait on an in-flight handler.
//
// Disposal is one-way. A released attempt keeps its immutable activation and a
// visibly released capture, so a late event fails closed instead of reopening the
// capture or falling through to ordinary client tool execution. This file adds no
// timer, no goroutine, no global map, and no public extension metadata.
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

// errControlAttemptReleased is the bounded failure for a control event, or a
// provider result, that reaches an attempt whose private control state was
// already released by cancellation, Close, attempt loss, or replacement. The
// attempt no longer owns a classifiable protocol, so the response fails closed
// rather than reopening the capture or executing the call as ordinary client
// tool traffic.
var errControlAttemptReleased = errors.New("runtime: control tool state released for this attempt")

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
	obs, owned := attempt.observeControlCall(ev)
	if !owned {
		// A released attempt keeps its capture visible but unusable. A control
		// event that reaches this point cannot be classified, so it fails closed
		// instead of reopening the capture or continuing as ordinary client
		// tool traffic.
		return true, errControlAttemptReleased
	}
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

// observeControlCall runs the capture's per-event decision under the attempt's
// control lock.
//
// The bool reports whether this attempt still owns its control state. A released
// attempt answers false without touching the capture, so cleanup and observation
// are ordered rather than racy, and a late event cannot observe or revive state
// that cleanup already released. The control lock is held only for this bounded
// classification: no provider, backend, or terminal call happens inside it.
func (a *attemptSession) observeControlCall(ev lipapi.Event) (controlCallObservation, bool) {
	if a == nil || a.controlCapture == nil {
		return controlCallObservation{}, false
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased {
		return controlCallObservation{}, false
	}
	return a.controlCapture.observe(ev), true
}

// handleControlCall invokes the pinned generation control provider for the one
// captured, bounded completion and stores only the validated outcome on this
// attempt.
//
// The handoff is claim-then-call-then-store, and the claim and the store are the
// only locked regions. The provider call runs unlocked, so cancellation, Close,
// or attempt loss can dispose this attempt's state while the handler is still
// in flight; the store then re-checks live ownership and drops the result
// instead of resurrecting state the attempt no longer owns.
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
	// The call arrives already owned: the capture hands off its argument buffer and
	// then only nils its own fields, so concurrent cleanup drops capture references
	// without ever mutating the bytes this value points at.
	activation, ok := a.claimControlHandoff()
	if !ok {
		return errControlHandlerUnavailable
	}
	outcome, err := extensions.HandleControlTool(ctx, extensions.ControlToolHandleRequest{
		ProviderID:   activation.providerID,
		Provider:     activation.provider,
		Call:         call,
		Meta:         controlHandleMeta(activation.meta),
		MaxArgsBytes: activation.projection.MaxArgsBytes(),
	})
	if err != nil {
		a.clearControlOutcome()
		return err
	}
	if !a.storeControlOutcome(outcome) {
		// The attempt was released while the provider was in flight, so this
		// result belongs to state the attempt no longer owns and is discarded with
		// it. The release was itself a legitimate lifecycle transition — the owner
		// that released it already decides the response outcome — so the handoff
		// stays a private protocol event and does not raise a second, competing
		// terminal cause over that owner's decision. A *later* control event on a
		// released attempt still fails closed, because then nothing can classify it.
		return nil
	}
	return nil
}

// claimControlHandoff records the one permitted completion handoff and returns the
// pinned immutable activation the provider call addresses. The activation is
// request/attempt provenance frozen at open, so the provider reads no live
// snapshot, identity, or spec; the returned pointer names that frozen value and
// nothing about it changes for the rest of the attempt. It reports false when this
// attempt may not hand off at all: it was already handed off, its activation is not
// active, or its control state was released.
func (a *attemptSession) claimControlHandoff() (*controlToolActivation, bool) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased || !a.controlTool.active() {
		return nil, false
	}
	if a.controlHandled {
		// One completion handoff at most. The capture already guarantees a single
		// Completed observation per response, so a second one is a wiring defect,
		// not a protocol state: revoke any outcome rather than re-invoke.
		a.controlOutcome = nil
		return nil, false
	}
	a.controlHandled = true
	return a.controlTool, true
}

// storeControlOutcome records a validated outcome only while this attempt still
// owns its control state, and reports whether it did. A handler that returns
// after cancellation, Close, or attempt loss therefore cannot restore a result.
func (a *attemptSession) storeControlOutcome(outcome controltool.Outcome) bool {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased {
		return false
	}
	a.controlOutcome = &outcome
	return true
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
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased {
		return
	}
	if a.controlCapture.closeResponse().Invalid() {
		a.controlOutcome = nil
	}
}

// clearControlOutcome drops the private pending outcome. A malformed, duplicate,
// or multiple control sequence after a valid completion revokes that completion
// without ever invoking the provider a second time.
func (a *attemptSession) clearControlOutcome() {
	if a == nil {
		return
	}
	a.controlMu.Lock()
	a.controlOutcome = nil
	a.controlMu.Unlock()
}

// discardControlState releases every private control buffer, raw ID, correlation
// digest, and pending result this attempt retained, and marks the state released
// so a late event or a returning handler cannot revive it.
//
// It is the cancellation, Close, attempt-loss, and replacement cleanup path. The
// immutable activation is deliberately left in place: it is request/attempt
// provenance, and erasing it would only hide who owned the released state. The
// capture object itself is kept for the same reason — a released attempt stays
// visible to late callers, which then fail closed instead of quietly behaving
// like an attempt that never had a control protocol.
//
// The operation is idempotent, holds the control lock for a bounded field release
// only, and never runs provider, backend, or terminal work while holding it.
func (a *attemptSession) discardControlState() {
	if a == nil {
		return
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased {
		return
	}
	a.controlReleased = true
	if a.controlCapture != nil {
		a.controlCapture.discard()
	}
	a.controlOutcome = nil
}
