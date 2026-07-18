package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type attemptOpenParams struct {
	ctx                 context.Context
	bus                 *hooks.Bus
	traceID             string
	aLegID              string
	aScope              *leglifecycle.ALeg
	baseline            lipapi.Call
	sel                 *routing.Selector
	requestSize         routing.RequestSizeEstimate
	session             *routing.SessionRoutingState
	excluded            map[string]struct{}
	rng                 routing.Rng
	budget              *attemptBudget
	ttft                *ttftBudget
	isRetryPath         bool
	lastReject          *lipapi.NegotiationResult
	lastTransportReject *lipapi.TransportNegotiationResult
	// lastParallelFailure carries aggregated parallel-arm failure details across failover iterations
	// so an eventual ErrNoEligibleCandidate can surface contextual root causes.
	lastParallelFailure *error
	affinityKey         affinity.Key
	affinitySet         bool
	// isContextLimitExhaustion, when non-nil, is set true when excluding a candidate for context-limit
	// eligibility so a subsequent ErrNoEligibleCandidate maps to [lipapi.ErrAllCandidatesContextLimitExceeded].
	isContextLimitExhaustion *bool
	// transformExcludes aggregates attempt-transform exclusions for stable all-excluded errors.
	transformExcludes *transformExcludeTracker
	// interleaved is the loaded interleaved-thinking state (cycle cursor + memo reference) for the
	// A-leg. It is the zero-value when interleaved thinking is disabled or no state has been stored.
	interleaved interleavedstate.State
	// suppressThinker skips thinker branches during planning (interleaved executor continuation).
	suppressThinker bool
	// suppressVisibleMemo skips visible memo injection during call shaping only.
	suppressVisibleMemo bool
	// deferMemoInjectionCommit leaves executor memo-store updates pending in the open result.
	// Parallel races use this so only the winning leg consumes memo budget.
	deferMemoInjectionCommit bool
}

type attemptOpenResult struct {
	opened     bool
	registered bool
	stream     lipapi.ManagedEventStream
	bleg       b2bua.BLegRecord
	cand       routing.AttemptCandidate
	authority  attemptAuthorityState
	// interleaved is the interleaved-thinking state after this attempt, with the cycle cursor
	// advanced and memo reference updated when shaping persisted them. Callers thread it back
	// into the next attempt-open iteration so retry/failover continues from the current state.
	interleaved interleavedstate.State
	memoUpdate  *interleavedthinking.PendingMemoUpdate
}

func (e *Executor) tryPlanOpenOnce(p attemptOpenParams) (attemptOpenResult, error) {
	var zero attemptOpenResult
	stickyBackendID, stickyBinding, err := e.lookupAffinityBinding(p.ctx, p.traceID, p.sel, p.affinityKey, p.affinitySet)
	if err != nil {
		return zero, err
	}
	groups, err := routing.ExpandFailoverGroups(p.sel, routing.PlanOptions{
		Excluded:               p.excluded,
		Unhealthy:              e.mergePlannerHealth(),
		RequestSize:            p.requestSize,
		Session:                p.session,
		PreferredCandidateKeys: execctx.RouteCandidatePreferences(p.ctx),
		StickyBackendID:        stickyBackendID,
		Rand:                   p.rng,
		IsRetryPath:            p.isRetryPath,
		ThinkerCycle:           p.interleaved.Cycle,
		SuppressThinker:        p.suppressThinker,
	})
	if stickyBinding && stickyBackendID != "" &&
		(err != nil || len(groups) == 0 || len(groups[0].Candidates) == 0 || groups[0].Candidates[0].Primary.Backend != stickyBackendID) {
		e.clearAffinityBinding(p.ctx, p.traceID, p.affinityKey, p.affinitySet, "ineligible")
		stickyBackendID = ""
		stickyBinding = false
		groups, err = routing.ExpandFailoverGroups(p.sel, routing.PlanOptions{
			Excluded:               p.excluded,
			Unhealthy:              e.mergePlannerHealth(),
			RequestSize:            p.requestSize,
			Session:                p.session,
			PreferredCandidateKeys: execctx.RouteCandidatePreferences(p.ctx),
			Rand:                   p.rng,
			IsRetryPath:            p.isRetryPath,
			ThinkerCycle:           p.interleaved.Cycle,
			SuppressThinker:        p.suppressThinker,
		})
	}
	if err != nil {
		noEligible := errors.Is(err, routing.ErrNoEligibleCandidate)
		lastNegotiationReject := p.lastReject != nil && p.lastReject.Kind == lipapi.NegotiationReject
		lastTransportReject := p.lastTransportReject != nil && p.lastTransportReject.Kind == lipapi.NegotiationReject
		if noEligible && lastTransportReject {
			return zero, p.lastTransportReject.Err()
		}
		if noEligible && lastNegotiationReject {
			return zero, p.lastReject.Err()
		}
		if noEligible && p.isContextLimitExhaustion != nil && *p.isContextLimitExhaustion {
			return zero, lipapi.ErrAllCandidatesContextLimitExceeded
		}
		if noEligible {
			if aggErr := p.transformExcludes.allExcludedError(); aggErr != nil {
				return zero, aggErr
			}
		}
		if noEligible && p.lastParallelFailure != nil && *p.lastParallelFailure != nil {
			return zero, *p.lastParallelFailure
		}
		return zero, fmt.Errorf("executor: expand failover: %w", err)
	}
	var lastNoOpen attemptOpenResult
	for gi, group := range groups {
		candidates := group.Candidates
		if len(candidates) == 0 {
			continue
		}
		if candidates[0].IsParallel {
			out, err := e.tryOpenParallelGroup(p, candidates, group.NextThinkerCycle, stickyBackendID, stickyBinding)
			if err != nil {
				return zero, err
			}
			p.interleaved = out.interleaved
			if out.opened {
				if p.lastParallelFailure != nil {
					*p.lastParallelFailure = nil
				}
				return out, nil
			}
			lastNoOpen = out
			if gi+1 < len(groups) {
				continue
			}
			return lastNoOpen, nil
		}
		c := candidates[0]
		out, err := e.openPlannedCandidate(p, c, group.NextThinkerCycle, stickyBackendID, stickyBinding)
		if err == nil && out.opened && p.lastParallelFailure != nil {
			*p.lastParallelFailure = nil
		}
		return out, err
	}
	return lastNoOpen, nil
}

func (e *Executor) openPlannedCandidate(
	p attemptOpenParams,
	c routing.AttemptCandidate,
	nextCycle *interleavedstate.CycleState,
	stickyBackendID string,
	stickyBinding bool,
) (attemptOpenResult, error) {
	var zero attemptOpenResult
	if p.isContextLimitExhaustion != nil {
		*p.isContextLimitExhaustion = false
	}
	attempt := lipapi.CloneCall(p.baseline)
	if e != nil && e.MaxPendingWireEvents > 0 {
		attempt.MaxPendingWireEvents = e.MaxPendingWireEvents
	}
	// Apply interleaved call shaping after route selection and before capability negotiation.
	// Thinker candidates get instructions prepended and tools suppressed; executor candidates
	// get the latest memo injected. The shaped call is the one used for negotiation and open.
	interleaved := p.interleaved
	shapeRes, err := e.shapeAttemptCall(p.ctx, attempt, c, p.aLegID, interleaved, p.suppressVisibleMemo)
	if err != nil {
		return zero, fmt.Errorf("executor: interleaved shape: %w", err)
	}
	attempt = shapeRes.Call
	e.logInterleavedMemoShape(p.ctx, p.traceID, "", c, shapeRes)
	noOpen := attemptOpenResult{interleaved: interleaved}
	be, ok := e.Backends[c.Primary.Backend]
	if !ok {
		return zero, fmt.Errorf("executor: unknown backend %q", c.Primary.Backend)
	}
	var transforms []request.AttemptTransform
	var atSvc request.Services
	if e.RuntimeSnapshot != nil {
		transforms = e.RuntimeSnapshot.AttemptTransforms()
		atSvc = request.Services{State: e.RuntimeSnapshot.State(), Aux: e.RuntimeSnapshot.Aux()}
	}
	atMeta := e.candidateAttemptMeta(p, attempt, c, be)
	xformRes, xformErr := extensions.RunCandidateAttemptTransformStage(
		p.ctx, e.Log, e.ExtensionMetrics, transforms, &attempt, atMeta, atSvc,
	)
	if xformErr != nil {
		return zero, fmt.Errorf("executor: candidate attempt transform: %w", xformErr)
	}
	if xformRes.Excluded {
		e.noteAttemptTransformExclude(p, c, xformRes)
		p.excluded[c.Key] = struct{}{}
		return noOpen, nil
	}
	pinCandidateRouteIdentity(&attempt, p.baseline)
	req := lipapi.RequiredCapabilities(attempt)
	var facts modelcatalog.EffectiveFacts
	res, negotiatePanicErr := safety.CallValue(
		safety.BoundaryBackend,
		"backend_capability_negotiate",
		func() (lipapi.NegotiationResult, error) {
			facts = e.effectiveFactsForAttempt(p.ctx, be, attempt, c)
			return lipapi.Negotiate(req, facts.EffectiveCaps), nil
		},
	)
	if negotiatePanicErr != nil {
		var pe *safety.PanicError
		if errors.As(negotiatePanicErr, &pe) {
			if e != nil && e.Log != nil {
				attrs := diag.IsolatedCrashAttrs(p.ctx, pe, diag.CrashAttrOpts{AttrOpts: diag.AttrOpts{CallID: p.traceID}})
				attrs = diag.AppendIsolatedCrashStack(attrs, pe)
				e.Log.LogAttrs(p.ctx, slog.LevelError, "isolated_panic_capability_negotiate", attrs...)
			}
			diag.LogDecision(
				p.ctx, e.Log, "capability_negotiate_panic_exclude", diag.AttrOpts{CallID: p.traceID},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			if p.transformExcludes != nil {
				p.transformExcludes.noteOther()
			}
			p.excluded[c.Key] = struct{}{}
			return noOpen, nil
		}
		return zero, negotiatePanicErr
	}
	if res.Kind == lipapi.NegotiationReject {
		if stickyBinding && c.Primary.Backend == stickyBackendID {
			e.clearAffinityBinding(p.ctx, p.traceID, p.affinityKey, p.affinitySet, "capability_reject")
		}
		if p.lastReject != nil {
			*p.lastReject = res
		}
		diag.LogDecision(
			p.ctx, e.Log, "capability_reject", diag.AttrOpts{CallID: p.traceID},
			slog.String("decision", "exclude_candidate"),
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		// Req 9.3 / task 6.2: same route-trace surface as context exclusions (negotiation outcome + catalog metadata).
		cat := catalogRouteTraceIfEnabled(e, facts, res, nil, false)
		e.notePlanCandidate(p.ctx, p.traceID, c.Key, cat)
		if p.transformExcludes != nil {
			p.transformExcludes.noteOther()
		}
		p.excluded[c.Key] = struct{}{}
		return noOpen, nil
	}
	if p.lastReject != nil {
		*p.lastReject = lipapi.NegotiationResult{}
	}
	transportCtx, transportSpan := otel.Tracer(otelScopeExecutor).Start(
		p.ctx, "lip.executor.transport_negotiate",
		trace.WithAttributes(
			attribute.String("lip.backend", c.Primary.Backend),
			attribute.String("lip.operation", string(attempt.Invocation.Operation)),
			attribute.String("lip.client_delivery_mode", string(attempt.Invocation.DeliveryMode)),
		),
	)
	defer transportSpan.End()
	transportCaps := e.transportCapsForAttempt(transportCtx, be, attempt, c)
	transportRes := lipapi.NegotiateTransport(attempt.Invocation, transportCaps, e.effectiveTransportFallbackPolicy())
	transportMode := transportRes.Selected
	if transportMode == "" {
		// Rejections may not select a concrete mode; fall back to the negotiated mode for diagnostics.
		transportMode = transportRes.Mode
	}
	transportSpan.SetAttributes(
		attribute.String("lip.transport_mode", string(transportMode)),
		attribute.String("lip.transport_negotiation_kind", string(transportRes.Kind)),
	)
	if transportRes.Kind == lipapi.NegotiationReject {
		transportSpan.RecordError(transportRes.Err())
		transportSpan.SetStatus(codes.Error, "transport negotiation rejected")
		e.recordTransportNegotiation(attempt.Invocation.Operation, transportRes.Mode, "reject")
		if stickyBinding && c.Primary.Backend == stickyBackendID {
			e.clearAffinityBinding(p.ctx, p.traceID, p.affinityKey, p.affinitySet, "transport_reject")
		}
		if p.lastTransportReject != nil {
			*p.lastTransportReject = transportRes
		}
		diag.LogDecision(
			p.ctx, e.Log, "transport_reject", diag.AttrOpts{CallID: p.traceID},
			slog.String("decision", "exclude_candidate"),
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		cat := catalogRouteTraceIfEnabled(e, facts, res, nil, false)
		e.notePlanCandidate(p.ctx, p.traceID, c.Key, cat)
		if p.transformExcludes != nil {
			p.transformExcludes.noteOther()
		}
		p.excluded[c.Key] = struct{}{}
		return noOpen, nil
	}
	e.recordTransportNegotiation(attempt.Invocation.Operation, transportRes.Selected, "accept")
	if p.lastTransportReject != nil {
		*p.lastTransportReject = lipapi.TransportNegotiationResult{}
	}
	attempt.Invocation.TransportMode = transportRes.Selected
	if res.Kind == lipapi.NegotiationDowngrade {
		diag.LogDecision(
			p.ctx, e.Log, "capability_downgrade", diag.AttrOpts{CallID: p.traceID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		lipapi.ApplyNegotiatedDowngrades(&attempt, res)
	}
	var elig *modelcatalog.EligibilityDecision
	eligRan := e != nil && e.EligibilityResolver != nil
	if eligRan {
		facts = e.effectiveFactsForAttempt(p.ctx, be, attempt, c)
		d := e.EligibilityResolver.Check(p.ctx, c, attempt, facts)
		elig = &d
		if !d.IsEligible {
			if stickyBinding && c.Primary.Backend == stickyBackendID {
				e.clearAffinityBinding(p.ctx, p.traceID, p.affinityKey, p.affinitySet, string(d.Reason))
			}
			if p.isContextLimitExhaustion != nil && d.Reason == modelcatalog.EligibilityContextLimitExceeded {
				*p.isContextLimitExhaustion = true
			}
			diag.LogDecision(
				p.ctx, e.Log, "context_limit_exclude", diag.AttrOpts{CallID: p.traceID},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			cat := catalogRouteTraceIfEnabled(e, facts, res, elig, true)
			e.notePlanCandidate(p.ctx, p.traceID, c.Key, cat)
			if p.transformExcludes != nil {
				p.transformExcludes.noteOther()
			}
			p.excluded[c.Key] = struct{}{}
			return noOpen, nil
		}
	}
	if res.Kind == lipapi.NegotiationDowngrade && !eligRan && e != nil {
		facts = e.effectiveFactsForAttempt(p.ctx, be, attempt, c)
	}
	cat := catalogRouteTraceIfEnabled(e, facts, res, elig, eligRan)
	e.notePlanCandidate(p.ctx, p.traceID, c.Key, cat)
	var preflightDecision accountingpreflight.Decision
	if decision, ok := e.runPreflight(p.ctx, p.traceID, attempt, c, facts.Facts); ok {
		preflightDecision = decision
		if !decision.Allowed {
			return zero, fmt.Errorf("executor: token accounting preflight: %w", decision.Err)
		}
		if decision.AdjustedMaxOutputTokens != nil {
			adjusted := *decision.AdjustedMaxOutputTokens
			attempt.Options.MaxOutputTokens = &adjusted
		}
	}
	precheckState, err := e.admitAttemptAuthority(p.ctx, p.traceID, p.aLegID, b2bua.BLegRecord{}, attempt, c, preflightDecision, true)
	if err != nil {
		return zero, err
	}
	_ = precheckState // precheck is estimate-only; state is not carried forward
	if !p.budget.tryAcquire() {
		return zero, fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
	}
	// NextBLeg allocates a B-leg seq before the authoritative admit; on a subsequent admit
	// failure that seq is intentionally NOT restored. Orphaned seqToBLeg entries are
	// functionally invisible (only RecordAttempt reads them, never for a rolled-back b-leg;
	// LoadAttempts needs no contiguous seqs) and reclaimed on A-leg eviction. Rollback was
	// rejected: it breaks the stable continuity.Store contract (contract test pins the method
	// set), nextSeq-- is ABA-unsafe; a delete-only variant-B touches ~9-11 files for cosmetic tidiness.
	bleg, err := e.Store.NextBLeg(p.ctx, p.aLegID)
	if err != nil {
		// tryAcquire already consumed a routing attempt slot; refund it so a
		// failed B-leg allocation does not permanently consume an attempt.
		p.budget.release()
		return zero, fmt.Errorf("executor: next b-leg: %w", err)
	}

	// Assemble the final provider-neutral attempt call before attempt authorization
	// (design Backend Ingress: transforms/hooks/route → freeze → count → admit → open).
	hookCtx := p.ctx
	if e != nil && e.Log != nil {
		hookCtx = hooks.WithDiagnosticsLogger(p.ctx, e.Log)
	}
	if err := p.bus.RunRequestPartHooks(hookCtx, &attempt, sdk.PartMeta{
		TraceID:    p.traceID,
		ALegID:     p.aLegID,
		BLegID:     bleg.BLegID,
		AttemptSeq: bleg.Seq,
		BackendID:  strings.TrimSpace(c.Primary.Backend),
	}); err != nil {
		p.budget.release()
		return zero, fmt.Errorf("executor: request hooks: %w", err)
	}
	postHook, postErr := e.rederiveAfterRequestHooks(p, &attempt, c, be, stickyBackendID, stickyBinding)
	if postErr != nil {
		p.budget.release()
		return zero, postErr
	}
	if postHook.excluded {
		p.budget.release()
		p.excluded[c.Key] = struct{}{}
		return noOpen, nil
	}
	if postHook.preflightOK {
		preflightDecision = postHook.preflight
	}
	facts = postHook.facts
	openCall, err := backendCallWithRouteParams(attempt, c)
	if err != nil {
		p.budget.release()
		return zero, fmt.Errorf("executor: %w", err)
	}

	previewedClamps, previewRan, perr := e.previewAndApplyAttemptClamps(p.ctx, &openCall, c, p.aLegID, bleg.BLegID)
	if perr != nil {
		p.budget.release()
		return zero, perr
	}

	// Freeze/store BE ingress before authorization. Authority clamps may narrow
	// MaxOutputTokens afterward; AssertNotWidened treats that as non-widening (7.5).
	authorizedFreeze := lipapi.CloneCall(openCall)
	admitDecision := preflightDecision
	if holder := meteringHolderFrom(p.ctx); holder != nil {
		if _, cerr := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
			Call:         authorizedFreeze,
			Scope:        scopeFromCtx(p.ctx),
			AttemptID:    bleg.BLegID,
			BLegID:       bleg.BLegID,
			ALegID:       p.aLegID,
			BackendID:    strings.TrimSpace(c.Primary.Backend),
			Model:        strings.TrimSpace(c.Primary.Model),
			CheckpointID: "operator-attempt:" + bleg.BLegID,
			StreamID:     "operator-attempt:" + bleg.BLegID,
			TraceID:      strings.TrimSpace(p.traceID),
			Now:          e.now(),
		}); cerr != nil {
			p.budget.release()
			return zero, fmt.Errorf("executor: metering backend ingress: %w", cerr)
		}
		if beDecision, ok := e.runPreflight(p.ctx, p.traceID, openCall, c, facts.Facts); ok {
			if !beDecision.Allowed {
				p.budget.release()
				return zero, fmt.Errorf("executor: token accounting preflight: %w", beDecision.Err)
			}
			admitDecision = beDecision
			e.enrichBackendIngressQuantitiesWithDecision(holder, bleg.BLegID, beDecision)
			if beDecision.AdjustedMaxOutputTokens != nil {
				adjusted := *beDecision.AdjustedMaxOutputTokens
				openCall.Options.MaxOutputTokens = &adjusted
			}
		} else {
			e.enrichBackendIngressQuantitiesWithDecision(holder, bleg.BLegID, admitDecision)
		}
		if _, ferr := e.persistBackendIngressFact(p.ctx, holder, bleg.BLegID); ferr != nil {
			p.budget.release()
			return zero, fmt.Errorf("executor: metering backend ingress fact: %w", ferr)
		}
	}

	authState, err := e.admitAttemptAuthority(p.ctx, p.traceID, p.aLegID, bleg, openCall, c, admitDecision, false)
	if err != nil {
		if authState.admissionResult.Reserved {
			cleanup := e.newAttemptAuthorityLifecycle(authState, c)
			cleanup.backendAttempted.Store(false)
			_ = terminalizeAttemptEphemeral(p.ctx, sdkterminal.CommandPreBackendDenial, false, func(cctx context.Context) error {
				cleanup.Release(cctx, authorityapp.ReleaseKindAdmissionFailure)
				return nil
			})
		}
		// The estimate-only precheck passed and consumed a routing attempt slot, but
		// the authoritative admit failed (e.g. strict store ErrReservationConflict when
		// the live window is full). Refund the budget slot so a backend that never opens
		// does not permanently consume an attempt. The b2bua Store exposes no B-leg
		// sequence rollback API, so the seq allocated by NextBLeg is not restored here.
		p.budget.release()
		return zero, err
	}
	releaseKind := authorityapp.ReleaseKindLosing
	opened := false
	cleanupAuthority := e.newAttemptAuthorityLifecycle(authState, c)
	// Admission happens before the backend open. Keep the cleanup evidence
	// accurate until the actual Open call begins; the constructor's default is
	// post-open because the other lifecycle owners are created from opened
	// attempts.
	cleanupAuthority.backendAttempted.Store(false)
	defer func() {
		if !opened {
			cmd := sdkterminal.CommandBackendOpenFailure
			if cleanupAuthority.backendAttempted == nil || !cleanupAuthority.backendAttempted.Load() {
				cmd = sdkterminal.CommandPreBackendDenial
			}
			_ = terminalizeAttemptEphemeral(p.ctx, cmd, false, func(cctx context.Context) error {
				cleanupAuthority.finalizeIncurredOrRelease(cctx, releaseKind, emptyOperatorUsageShell())
				return nil
			})
		}
	}()
	if err := e.enforcePostAdmitClamps(p.ctx, &openCall, authorizedFreeze, previewedClamps, previewRan, authState, c, int64(admitDecision.Count.InputTokens)); err != nil {
		releaseKind = authorityapp.ReleaseKindAdmissionFailure
		p.budget.release()
		return zero, err
	}
	if len(previewedClamps) > 0 && !backendCanEnforceAuthorityClamp(be, &openCall) {
		diag.LogDecision(
			p.ctx, e.Log, "authority_clamp_unenforceable_exclude", diag.AttrOpts{CallID: p.traceID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		releaseKind = authorityapp.ReleaseKindAdmissionFailure
		p.budget.release()
		p.excluded[c.Key] = struct{}{}
		return noOpen, nil
	}
	// Preflight-applied max-output clamps must be enforceable on the wire (7.4).
	if admitDecision.RequireMaxOutputEnforcement && !backendCanEnforceAuthorityClamp(be, &openCall) {
		diag.LogDecision(
			p.ctx, e.Log, "unknown_output_clamp_unenforceable_exclude", diag.AttrOpts{CallID: p.traceID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		releaseKind = authorityapp.ReleaseKindAdmissionFailure
		p.budget.release()
		p.excluded[c.Key] = struct{}{}
		return noOpen, nil
	}
	if werr := checkpoint.AssertNotWidened(authorizedFreeze, openCall); werr != nil {
		p.budget.release()
		return zero, fmt.Errorf("executor: %w", werr)
	}
	// Wire payload is the post-admit (possibly clamp-narrowed) call, cloned so
	// later in-place mutation of openCall cannot widen what Open observes (7.5).
	wireCall := lipapi.CloneCall(openCall)

	if e.RuntimeSnapshot != nil {
		if rawPayload, jerr := json.Marshal(wireCall); jerr == nil {
			sc := scopeFromCtx(p.ctx)
			meta := sdktraffic.CaptureMeta{
				TraceID:     p.traceID,
				ALegID:      p.aLegID,
				BLegID:      bleg.BLegID,
				AttemptSeq:  bleg.Seq,
				BackendID:   strings.TrimSpace(c.Primary.Backend),
				PrincipalID: strings.TrimSpace(sc.PrincipalID.String()),
				Scope:       sc,
			}
			coretraffic.PortBundleFromSnapshot(e.RuntimeSnapshot).Emit(
				p.ctx,
				sdktraffic.LegPTB,
				meta,
				"lip/canonical+json",
				"application/json",
				rawPayload,
			)
		}
	}
	baseOpenCtx := p.ctx
	var cancelOpen context.CancelFunc = func() {}
	ttftDeadline := ttftContextDeadline{}
	if p.ttft != nil {
		baseOpenCtx, cancelOpen, ttftDeadline = p.ttft.scopedContext(p.ctx, e.now(), c.Key, c.Primary.TTFTTimeout)
	}
	defer cancelOpen()

	openCtx, openSpan := otel.Tracer(otelScopeExecutor).Start(
		baseOpenCtx, "lip.executor.backend_open",
		trace.WithAttributes(
			attribute.String("lip.backend", c.Primary.Backend),
			attribute.Int("lip.b_leg_seq", int(bleg.Seq)),
		),
	)
	defer openSpan.End()
	openStart := time.Now()
	if aerr := e.assertSecureSessionActiveBeforeOpen(openCtx); aerr != nil {
		return zero, aerr
	}
	cleanupAuthority.backendAttempted.Store(true)
	// Mark call-path identity for approved B-leg httpidentity transports (passthrough).
	openCtx = identity.WithClientUserAgent(openCtx, wireCall.Invocation.ClientUserAgent)
	stream, err := safety.CallValue(safety.BoundaryBackend, "backend_open", func() (lipapi.ManagedEventStream, error) {
		return be.Open(openCtx, wireCall, c)
	})
	openDur := time.Since(openStart).Seconds()
	if err != nil {
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			err = mapBackendPanic(pe, false, c.Key)
		}
	}
	if e != nil && e.Metrics != nil {
		e.Metrics.OnBackendOpenDuration(c.Primary.Backend, openDur)
	}
	if err != nil {
		if ttftDeadline.expired(openCtx, err) {
			ttftScope := ttftDeadline.scope
			tf := ttftFailure(ttftScope, c.Key)
			if ttftScope == ttftTimeoutLeaf {
				e.recordAttemptLogged(p.ctx, recordAttemptParams{
					ALegID:    p.aLegID,
					BLeg:      bleg,
					Cand:      c,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    ttftAttemptReason(ttftScope),
					DetailErr: tf,
				}, diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID})
				e.emitBackendEgressMeteringFact(p.ctx, bleg.BLegID, metering.AttemptOutcomeFailed, metering.SurfacedNo, lipapi.Event{Kind: lipapi.EventUsageDelta})
				releaseKind = authorityapp.ReleaseKindSwallowed
				p.excluded[c.Key] = struct{}{}
				return noOpen, nil
			}
			e.recordAttemptLogged(p.ctx, recordAttemptParams{
				ALegID:    p.aLegID,
				BLeg:      bleg,
				Cand:      c,
				Outcome:   lipapi.AttemptSurfacedFailure,
				Reason:    ttftAttemptReason(ttftScope),
				DetailErr: tf,
			}, diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID})
			e.emitBackendEgressMeteringFact(p.ctx, bleg.BLegID, metering.AttemptOutcomeFailed, metering.SurfacedYes, lipapi.Event{Kind: lipapi.EventUsageDelta})
			return zero, fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, lipapi.ErrTTFTTimeout)
		}
		openSpan.RecordError(err)
		openSpan.SetStatus(codes.Error, "backend open failed")
		if lipapi.IsRecoverablePreOutput(err) {
			if stickyBinding && c.Primary.Backend == stickyBackendID {
				e.clearAffinityBinding(p.ctx, p.traceID, p.affinityKey, p.affinitySet, "recoverable_pre_output_open")
			}
			e.recordAttemptLogged(p.ctx, recordAttemptParams{
				ALegID:    p.aLegID,
				BLeg:      bleg,
				Cand:      c,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    "recoverable pre-output (open)",
				DetailErr: err,
			}, diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID})
			diag.LogDecision(
				p.ctx, e.Log, "recoverable_pre_output_swallowed",
				diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID},
				slog.String("candidate_key", c.Key),
				slog.String("phase", "open"),
			)
			e.emitBackendEgressMeteringFact(p.ctx, bleg.BLegID, metering.AttemptOutcomeFailed, metering.SurfacedNo, lipapi.Event{Kind: lipapi.EventUsageDelta})
			releaseKind = authorityapp.ReleaseKindSwallowed
			p.excluded[c.Key] = struct{}{}
			return noOpen, nil
		}
		e.recordAttemptLogged(p.ctx, recordAttemptParams{
			ALegID:    p.aLegID,
			BLeg:      bleg,
			Cand:      c,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    attemptReasonDetail(err),
			DetailErr: err,
		}, diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID})
		e.emitBackendEgressMeteringFact(p.ctx, bleg.BLegID, metering.AttemptOutcomeFailed, metering.SurfacedYes, lipapi.Event{Kind: lipapi.EventUsageDelta})
		return zero, fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, err)
	}
	if nextCycle != nil {
		interleaved.Cycle = *nextCycle
	}
	var memoUpdate *interleavedthinking.PendingMemoUpdate
	if p.deferMemoInjectionCommit {
		if nextCycle != nil {
			if perr := e.persistInterleavedState(p.ctx, p.aLegID, interleaved); perr != nil {
				if stream != nil {
					_ = stream.Close()
				}
				return zero, fmt.Errorf("executor: persist interleaved cycle: %w", perr)
			}
		}
		memoUpdate = shapeRes.MemoUpdate
	} else {
		if shapeRes.MemoUpdate != nil {
			interleaved, err = e.commitMemoInjection(p.ctx, p.aLegID, interleaved, shapeRes.MemoUpdate)
			if err != nil {
				if stream != nil {
					_ = stream.Close()
				}
				return zero, err
			}
		} else if nextCycle != nil {
			if perr := e.persistInterleavedState(p.ctx, p.aLegID, interleaved); perr != nil {
				if stream != nil {
					_ = stream.Close()
				}
				return zero, fmt.Errorf("executor: persist interleaved cycle: %w", perr)
			}
		}
	}
	if m := e.secureSessionForAttempt(); m != nil {
		if st, ok := execctx.SecureSessionTurnFromContext(openCtx); ok {
			tr := buildAttemptTrace(st, p.aLegID, bleg, c, openCall, openStart)
			persistCtx := context.WithoutCancel(openCtx)
			if rerr := m.RecordAttemptOpened(persistCtx, tr); rerr != nil && e.Log != nil {
				e.Log.DebugContext(persistCtx, "secure_session_attempt_trace_failed", "error", rerr)
			}
		}
	}
	diag.LogDecision(
		p.ctx, e.Log, "backend_attempt_opened", diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("model", c.Primary.Model),
		slog.String("operation", string(openCall.Invocation.Operation)),
		slog.String("client_delivery_mode", string(openCall.Invocation.DeliveryMode)),
		slog.String("upstream_transport_mode", string(openCall.Invocation.TransportMode)),
		slog.String("reasoning_effort", openCall.Options.ReasoningEffort),
		slog.String("verbosity", string(openCall.Options.Verbosity)),
		slog.Int64("open_duration_ms", time.Since(openStart).Milliseconds()),
	)
	e.logInterleavedRouteSelected(p.ctx, p.traceID, bleg.BLegID, c)
	if c.MarkedFirst {
		if err := e.Store.SetWeightedFirstConsumed(p.ctx, p.aLegID, true); err != nil {
			if stream != nil {
				_ = stream.Close()
			}
			return zero, fmt.Errorf("executor: set weighted first consumed: %w", err)
		}
		if p.session != nil {
			p.session.FirstRequestConsumed = true
		}
	}
	opened = true
	return attemptOpenResult{opened: true, registered: false, stream: stream, bleg: bleg, cand: c, authority: authState, interleaved: interleaved, memoUpdate: memoUpdate}, nil
}

func (e *Executor) lookupAffinityBinding(ctx context.Context, traceID string, sel *routing.Selector, key affinity.Key, keyOK bool) (string, bool, error) {
	if e == nil || e.AffinityStore == nil || sel == nil || sel.Affinity == routing.AffinityNone || !keyOK {
		return "", false, nil
	}
	b, ok, err := e.AffinityStore.Get(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("executor: affinity lookup: %w", err)
	}
	backend := strings.TrimSpace(b.BackendID)
	if !ok || backend == "" {
		return "", false, nil
	}
	e.noteRouteDecision(ctx, traceID, "affinity_hit", backend)
	return backend, true, nil
}

func (e *Executor) clearAffinityBinding(ctx context.Context, traceID string, key affinity.Key, keyOK bool, reason string) {
	if e == nil || e.AffinityStore == nil || !keyOK {
		return
	}
	if err := e.AffinityStore.Delete(ctx, key); err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "affinity binding delete failed", "error", err)
		}
		return
	}
	e.noteRouteDecision(ctx, traceID, "affinity_reset", strings.TrimSpace(reason))
}

func (e *Executor) requestSizeEstimateForRouting(ctx context.Context, sel *routing.Selector, call lipapi.Call) routing.RequestSizeEstimate {
	if e.Preflight != nil && routing.SelectorHasRequestSizeConstraints(sel) {
		model := ""
		backend := ""
		if primary := firstSelectorPrimary(sel); primary != nil {
			model = primary.Model
			backend = primary.Backend
		}
		decision := e.Preflight.Check(ctx, accountingpreflight.Input{Backend: backend, Model: model, CallID: call.ID, Call: call})
		if decision.Err == nil && decision.Reason != accountingpreflight.ReasonDisabled {
			return routing.RequestSizeEstimate{Available: true, Tokens: int64(decision.Count.InputTokens) + 1, Basis: "token_accounting_preflight"}
		}
	}
	if !routing.SelectorHasRequestSizeConstraints(sel) || e.RequestTokenEstimator == nil {
		return routing.RequestSizeEstimate{}
	}
	est := e.RequestTokenEstimator.EstimateRequestTokens(ctx, call)
	return routing.RequestSizeEstimate{Available: est.Available, Tokens: est.Input, Basis: est.Basis}
}

func (e *Executor) runPreflight(
	ctx context.Context,
	traceID string,
	call lipapi.Call,
	c routing.AttemptCandidate,
	facts modelcatalog.ModelFacts,
) (accountingpreflight.Decision, bool) {
	if e == nil || e.Preflight == nil {
		return accountingpreflight.Decision{}, false
	}
	decision := e.Preflight.Check(ctx, accountingpreflight.Input{
		Backend:                  c.Primary.Backend,
		Model:                    c.Primary.Model,
		CallID:                   traceID,
		Call:                     call,
		RequestedMaxOutputTokens: call.Options.MaxOutputTokens,
		Facts:                    facts,
	})
	return decision, true
}

func firstSelectorPrimary(sel *routing.Selector) *routing.Primary {
	if sel == nil || len(sel.Alternatives) == 0 {
		return nil
	}
	alt := sel.Alternatives[0]
	if alt.Primary != nil {
		return alt.Primary
	}
	if alt.Weighted != nil {
		for i := range alt.Weighted.Branches {
			b := &alt.Weighted.Branches[i]
			if b.Parallel != nil {
				for j := range b.Parallel.Branches {
					if b.Parallel.Branches[j].Target.Model != "" {
						return &b.Parallel.Branches[j].Target
					}
				}
				continue
			}
			if b.Target.Model != "" {
				return &b.Target
			}
		}
	}
	if alt.Parallel != nil {
		for i := range alt.Parallel.Branches {
			if alt.Parallel.Branches[i].Target.Model != "" {
				return &alt.Parallel.Branches[i].Target
			}
		}
	}
	return nil
}
