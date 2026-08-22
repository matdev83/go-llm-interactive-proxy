package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type requestFacts struct {
	recvTurnFacts
	bus                 *hooks.Bus
	aScope              *leglifecycle.ALeg
	suppressThinker     bool
	suppressVisibleMemo bool
}
type openMode int

const (
	openModeInitial openMode = iota
	openModeRetry
)

type candidatePlan struct {
	cand            routing.AttemptCandidate
	nextCycle       *interleavedstate.CycleState
	stickyBackendID string
	stickyBinding   bool
}

type rejectionKind int

const (
	rejectNone rejectionKind = iota
	rejectExclude
	rejectAdmission
	rejectContextLimit
	rejectParallel
)

type candidateRejection struct {
	kind   rejectionKind
	detail any
}

type candidateEvaluationOutcome struct {
	accepted          bool
	rejection         candidateRejection
	shapeRes          interleavedthinking.ShapeResult
	facts             modelcatalog.EffectiveFacts
	preflightDecision accountingpreflight.Decision
	admitOut          candidateAdmissionOutcome
}
type openedAttempt struct {
	ready       *readyAttempt
	interleaved interleavedstate.State
	memoUpdate  *interleavedthinking.PendingMemoUpdate
}
type attemptTx struct {
	e                *Executor
	reqFacts         requestFacts
	routeFacts       routeFacts
	cand             routing.AttemptCandidate
	bleg             b2bua.BLegRecord
	authState        attemptAuthorityState
	authLifecycle    authorityLifecycle
	stream           lipapi.ManagedEventStream
	registered       bool
	openInvoked      bool
	openStartedAt    time.Time
	budget           *attemptBudget
	budgetAcquired   bool
	backendAttempted bool
	completed        bool
	failures         *candidateFailureHistory

	// attempt-local resources
	accounting            attemptAccountingTracker
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
	recordAttemptLoggedFn func(context.Context, recordAttemptParams, diag.AttrOpts)
}

func rollbackCommandToIntent(cmd sdkterminal.Command) attemptTerminalIntent {
	switch cmd {
	case sdkterminal.CommandParallelLoser:
		return IntentParallelLoser
	case sdkterminal.CommandBackendOpenFailure:
		return IntentOpenReadinessFailure
	case sdkterminal.CommandPreBackendDenial:
		return IntentOpenReadinessFailure
	case sdkterminal.CommandCancel, sdkterminal.CommandClose:
		return IntentCancellation
	case sdkterminal.CommandTimeout:
		return IntentTimeout
	case sdkterminal.CommandSwallowedAttempt:
		return IntentSwallowedFailure
	default:
		return IntentSurfacedFailure
	}
}

func (e *Executor) newAttemptSession(in attemptSessionInput) *attemptSession {
	if e != nil {
		in.emitBackendEgressFn = e.emitBackendEgressMeteringFact
		in.appendBillingLegFn = func(cctx context.Context, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time, outcome billing.LegOutcome) {
			e.appendIndependentTerminalLeg(cctx, in.billingCallState, bleg.ALegID, bleg, primary, started, finished, outcome)
		}
		in.now = e.now
		in.finalizeBilling = e.callFinalizeBilling
		in.billingEnabled = e.billingEnabled
		in.operatorRateRef = e.operatorRateRef
		in.billingWorkload = e.billingWorkloadIdentityForALeg
		in.observeBillingLeg = e.observeBillingLeg
		in.appendBillingLeg = e.appendIndependentCallLeg
	}
	return newAttemptSession(in)
}

func (tx *attemptTx) createSession() *attemptSession {
	if tx == nil {
		return nil
	}
	return tx.e.newAttemptSession(attemptSessionInput{
		inner:                 tx.stream,
		bleg:                  tx.bleg,
		cand:                  tx.cand,
		authority:             tx.authLifecycle,
		aScope:                tx.reqFacts.aScope,
		traceID:               tx.reqFacts.traceID,
		billingCallID:         tx.reqFacts.billingCallID,
		billingCallState:      tx.reqFacts.billingCallState,
		accounting:            tx.accounting,
		toolFinal:             tx.toolFinal,
		promptCacheSource:     tx.promptCacheSource,
		promptCacheController: tx.promptCacheController,
		finalStreamObs:        tx.finalStreamObs,
		recordAttemptLoggedFn: tx.recordAttemptLoggedFn,
	})
}

func (e *Executor) createSessionForParallelLeg(leg *parallelLeg, aScope *leglifecycle.ALeg) *attemptSession {
	if leg == nil {
		return nil
	}
	if leg.tx != nil {
		return leg.tx.createSession()
	}
	return e.newAttemptSession(attemptSessionInput{
		inner:            leg.stream,
		bleg:             leg.bleg,
		cand:             leg.cand,
		authority:        leg.authority,
		aScope:           aScope,
		billingCallState: leg.billingCallState,
		recordAttemptLoggedFn: func(cctx context.Context, p recordAttemptParams, attrs diag.AttrOpts) {
			if e != nil {
				e.recordAttemptLogged(cctx, p, attrs)
			}
		},
	})
}

func (tx *attemptTx) HandoffReady(pending pendingSelectionEffects) *readyAttempt {
	if tx == nil {
		panic("nil attemptTx handoff")
	}
	if tx.completed {
		panic("double handoff")
	}
	if tx.e == nil {
		panic("nil executor in handoff")
	}
	tx.completed = true
	return newReadyAttempt(tx.createSession(), pending)
}

func (tx *attemptTx) rollback(ctx context.Context, cmd sdkterminal.Command, evidence attemptEvidence) attemptTerminalResult {
	if tx == nil || tx.completed {
		return attemptTerminalResult{}
	}
	if tx.budgetAcquired && tx.budget != nil && !tx.backendAttempted {
		tx.budget.release()
		tx.budgetAcquired = false
	}
	tx.completed = true
	session := tx.createSession()
	intent := rollbackCommandToIntent(cmd)
	return session.TerminalizeAttempt(ctx, intent, evidence)
}

func (tx *attemptTx) rollbackSimple(ctx context.Context, cmd sdkterminal.Command, rel authorityapp.ReleaseKind, outcome billing.LegOutcome, err error, reason string) {
	if tx == nil || tx.completed {
		return
	}
	var rErr error
	if err != nil {
		rErr = err
	} else if ctx != nil {
		rErr = ctx.Err()
	}
	tx.rollback(ctx, cmd, attemptEvidence{
		Command:      cmd,
		ReleaseKind:  rel,
		LegOutcome:   outcome,
		Usage:        emptyOperatorUsageShell(),
		Err:          rErr,
		TraceID:      tx.reqFacts.traceID,
		ALegID:       tx.reqFacts.aLegID,
		StartedAt:    tx.openStartedAt,
		RecordReason: reason,
	})
}

func (tx *attemptTx) recordFailure(ctx context.Context, outcome lipapi.AttemptOutcome, reason string, err error) {
	tx.e.recordAttemptLogged(ctx, recordAttemptParams{
		ALegID:    tx.reqFacts.aLegID,
		BLeg:      tx.bleg,
		Cand:      tx.cand,
		Outcome:   outcome,
		Reason:    reason,
		DetailErr: err,
	}, diag.AttrOpts{CallID: tx.reqFacts.traceID, BLegID: tx.bleg.BLegID})
}

func (e *Executor) startAttemptTx(ctx context.Context, rf requestFacts, route routeFacts, cand routing.AttemptCandidate, budget *attemptBudget, failures *candidateFailureHistory) (*attemptTx, error) {
	bleg, err := e.Store.NextBLeg(ctx, rf.aLegID)
	if err != nil {
		return nil, fmt.Errorf("executor: next b-leg: %w", err)
	}

	if rf.billingCallState != nil {
		rf.billingCallState.noteAllocatedBLeg(bleg.BLegID, bleg.Seq)
	}
	if failures == nil && budget != nil {
		failures = budget.getFailures()
	}
	return &attemptTx{
		e:          e,
		reqFacts:   rf,
		routeFacts: route,
		cand:       cand,
		bleg:       bleg,
		budget:     budget,
		failures:   failures,
	}, nil
}

func (e *Executor) evaluateCandidate(
	ctx context.Context,
	rf requestFacts,
	routeFacts routeFacts,
	plan candidatePlan,
	interleaved interleavedstate.State,
) (candidateEvaluationOutcome, error) {
	var zero candidateEvaluationOutcome
	attempt := lipapi.CloneCall(rf.baseline)
	if e.MaxPendingWireEvents > 0 {
		attempt.MaxPendingWireEvents = e.MaxPendingWireEvents
	}
	shapeRes, err := e.shapeAttemptCall(ctx, attempt, plan.cand, rf.aLegID, interleaved, rf.suppressVisibleMemo)
	if err != nil {
		return zero, fmt.Errorf("executor: interleaved shape: %w", err)
	}
	attempt = shapeRes.Call
	e.logInterleavedMemoShape(ctx, rf.traceID, "", plan.cand, shapeRes)
	be, ok := e.Backends[plan.cand.Primary.Backend]
	if !ok {
		return zero, fmt.Errorf("executor: unknown backend %q", plan.cand.Primary.Backend)
	}
	var transforms []request.AttemptTransform
	var atSvc request.Services
	if e.RuntimeSnapshot != nil {
		transforms = e.RuntimeSnapshot.AttemptTransforms()
		atSvc = request.Services{State: e.RuntimeSnapshot.State(), Aux: e.RuntimeSnapshot.Aux()}
	}
	atMeta := e.candidateAttemptMeta(ctx, rf, attempt, plan.cand, be)
	xformRes, xformErr := extensions.RunCandidateAttemptTransformStage(
		ctx, e.Log, e.ExtensionMetrics, transforms, &attempt, atMeta, atSvc,
	)
	if xformErr != nil {
		return zero, fmt.Errorf("executor: candidate attempt transform: %w", xformErr)
	}
	shapeRes.Call = attempt
	if xformRes.Excluded {
		return candidateEvaluationOutcome{
			accepted: false,
			rejection: candidateRejection{
				kind:   rejectExclude,
				detail: xformRes.ReasonCode,
			},
		}, nil
	}
	pinCandidateRouteIdentity(&attempt, rf.baseline)
	transportCtx, transportSpan := otel.Tracer(otelScopeExecutor).Start(
		ctx, "lip.executor.candidate_admission",
		trace.WithAttributes(
			attribute.String("lip.backend", plan.cand.Primary.Backend),
			attribute.String("lip.operation", string(attempt.Invocation.Operation)),
			attribute.String("lip.client_delivery_mode", string(attempt.Invocation.DeliveryMode)),
		),
	)
	defer transportSpan.End()
	var facts modelcatalog.EffectiveFacts
	var res lipapi.NegotiationResult
	admitOut, admitPanicErr := safety.CallValue(
		safety.BoundaryBackend,
		"backend_candidate_admission",
		func() (candidateAdmissionOutcome, error) {
			return e.evaluateCandidateAdmission(transportCtx, rf.traceID, attempt, plan.cand, be, routeFacts.failoverReq), nil
		},
	)
	if admitPanicErr != nil {
		var pe *safety.PanicError
		if errors.As(admitPanicErr, &pe) {
			if e != nil && e.Log != nil {
				attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{AttrOpts: diag.AttrOpts{CallID: rf.traceID}})
				attrs = diag.AppendIsolatedCrashStack(attrs, pe)
				e.Log.LogAttrs(ctx, slog.LevelError, "isolated_panic_candidate_admission", attrs...)
			}
			diag.LogDecision(
				ctx, e.Log, "candidate_admission_panic_exclude", diag.AttrOpts{CallID: rf.traceID},
				slog.String("candidate_key", plan.cand.Key),
				slog.String("backend", plan.cand.Primary.Backend),
			)
			return candidateEvaluationOutcome{
				accepted: false,
				rejection: candidateRejection{
					kind:   rejectExclude,
					detail: "panic",
				},
			}, nil
		}
		return zero, admitPanicErr
	}
	facts = admitOut.facts
	res = admitOut.admitRes.Capability
	transportRes := admitOut.admitRes.Transport
	transportMode := admitOut.transportMode
	transportSpan.SetAttributes(
		attribute.String("lip.transport_mode", string(transportMode)),
		attribute.String("lip.transport_negotiation_kind", string(transportRes.Kind)),
		attribute.String("lip.admission_kind", string(admitOut.admitRes.Kind)),
	)
	if admitOut.admitRes.Kind == lipapi.NegotiationReject {
		if transportRes.Kind == lipapi.NegotiationReject {
			transportSpan.RecordError(transportRes.Err())
			transportSpan.SetStatus(codes.Error, "transport negotiation rejected")
		}
		return candidateEvaluationOutcome{
			accepted: false,
			rejection: candidateRejection{
				kind:   rejectAdmission,
				detail: admitOut.admitRes,
			},
			admitOut: admitOut,
		}, nil
	}
	e.recordTransportNegotiation(attempt.Invocation.Operation, transportRes.Selected, "accept")
	attempt.Invocation.TransportMode = transportRes.Selected
	if res.Kind == lipapi.NegotiationDowngrade {
		diag.LogDecision(
			ctx, e.Log, "capability_downgrade", diag.AttrOpts{CallID: rf.traceID},
			slog.String("candidate_key", plan.cand.Key),
			slog.String("backend", plan.cand.Primary.Backend),
		)
		lipapi.ApplyNegotiatedDowngrades(&attempt, res)
	}
	preflightDecision, _ := e.runPreflight(ctx, rf.traceID, attempt, plan.cand, facts.Facts)
	eligRan := e.EligibilityResolver != nil
	if eligRan {
		facts = e.effectiveFactsForAttempt(ctx, be, attempt, plan.cand)
		d := e.EligibilityResolver.Check(ctx, plan.cand, attempt, facts)
		if !d.IsEligible {
			return candidateEvaluationOutcome{
				accepted: false,
				rejection: candidateRejection{
					kind: rejectContextLimit,
				},
				facts:             facts,
				preflightDecision: preflightDecision,
				admitOut:          admitOut,
			}, nil
		}
	}
	if res.Kind == lipapi.NegotiationDowngrade && !eligRan {
		facts = e.effectiveFactsForAttempt(ctx, be, attempt, plan.cand)
	}
	precheckState, err := e.admitAttemptAuthority(ctx, rf.traceID, rf.aLegID, b2bua.BLegRecord{}, attempt, plan.cand, preflightDecision, true)
	if err != nil {
		return zero, err
	}
	_ = precheckState
	return candidateEvaluationOutcome{
		accepted:          true,
		shapeRes:          shapeRes,
		facts:             facts,
		preflightDecision: preflightDecision,
		admitOut:          admitOut,
	}, nil
}

func (e *Executor) applyRejection(failures *candidateFailureHistory, c routing.AttemptCandidate, rejection candidateRejection) {
	if failures == nil {
		return
	}
	switch rejection.kind {
	case rejectExclude:
		if failures.TransformExcludes != nil {
			if reason, ok := rejection.detail.(string); ok && reason != "" && reason != "panic" {
				failures.TransformExcludes.noteTransform(reason)
			} else {
				failures.TransformExcludes.noteOther()
			}
		}
	case rejectAdmission:
		if admit, ok := rejection.detail.(lipapi.CandidateAdmissionResult); ok {
			if admit.Kind == lipapi.NegotiationReject {
				failures.CapabilityReject = admit.Capability
			}
			if admit.Transport.Kind == lipapi.NegotiationReject {
				failures.TransportReject = admit.Transport
			}
		}
	case rejectContextLimit:
		failures.ContextLimit = true
		if failures.TransformExcludes != nil {
			failures.TransformExcludes.noteOther()
		}
	case rejectParallel:
		if err, ok := rejection.detail.(error); ok {
			failures.ParallelFailure = err
		}
	}
}

func (e *Executor) openAttemptTx(
	ctx context.Context,
	tx *attemptTx,
	evalOutcome candidateEvaluationOutcome,
	plan candidatePlan,
) error {
	c := plan.cand
	attempt := evalOutcome.shapeRes.Call
	be := e.Backends[c.Primary.Backend]
	if tx.budget != nil {
		if !tx.budget.tryAcquire() {
			return fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
		}
		tx.budgetAcquired = true
	}
	hookCtx := tx.reqFacts.projectContext(ctx, e.Log)
	if err := tx.reqFacts.bus.RunRequestPartHooks(hookCtx, &attempt, sdk.PartMeta{
		TraceID:    tx.reqFacts.traceID,
		ALegID:     tx.reqFacts.aLegID,
		BLegID:     tx.bleg.BLegID,
		AttemptSeq: tx.bleg.Seq,
		BackendID:  strings.TrimSpace(c.Primary.Backend),
	}); err != nil {
		return fmt.Errorf("executor: request hooks: %w", err)
	}
	postHook, postErr := e.rederiveAfterRequestHooks(ctx, tx.reqFacts, tx.routeFacts, &attempt, c, be, plan.stickyBackendID, plan.stickyBinding, tx.failures)
	if postErr != nil {
		return postErr
	}
	if postHook.excluded {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted, nil, "")
		return nil
	}
	preflightDecision := evalOutcome.preflightDecision
	if postHook.preflightOK {
		preflightDecision = postHook.preflight
	}
	facts := postHook.facts
	openCall, err := backendCallWithRouteParams(attempt, c)
	if err != nil {
		return fmt.Errorf("executor: %w", err)
	}
	previewedClamps, previewRan, perr := e.previewAndApplyAttemptClamps(ctx, &openCall, c, tx.reqFacts.aLegID, tx.bleg.BLegID)
	if perr != nil {
		return perr
	}
	authorizedFreeze := lipapi.CloneCall(openCall)
	admitDecision := preflightDecision
	holder := tx.reqFacts.metering
	scopeVal := tx.reqFacts.recvViews.Scope
	if holder != nil {
		if _, cerr := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
			Call:         authorizedFreeze,
			Scope:        scopeVal,
			AttemptID:    tx.bleg.BLegID,
			BLegID:       tx.bleg.BLegID,
			ALegID:       tx.reqFacts.aLegID,
			BackendID:    strings.TrimSpace(c.Primary.Backend),
			Model:        strings.TrimSpace(c.Primary.Model),
			CheckpointID: "operator-attempt:" + tx.bleg.BLegID,
			StreamID:     "operator-attempt:" + tx.bleg.BLegID,
			TraceID:      strings.TrimSpace(tx.reqFacts.traceID),
			Now:          e.now(),
		}); cerr != nil {
			return fmt.Errorf("executor: metering backend ingress: %w", cerr)
		}
		if beDecision, ok := e.runPreflight(ctx, tx.reqFacts.traceID, openCall, c, facts.Facts); ok {
			if !beDecision.Allowed {
				return fmt.Errorf("executor: token accounting preflight: %w", beDecision.Err)
			}
			admitDecision = beDecision
			e.enrichBackendIngressQuantitiesWithDecision(holder, tx.bleg.BLegID, beDecision)
			if beDecision.AdjustedMaxOutputTokens != nil {
				adjusted := *beDecision.AdjustedMaxOutputTokens
				openCall.Options.MaxOutputTokens = &adjusted
			}
		} else {
			e.enrichBackendIngressQuantitiesWithDecision(holder, tx.bleg.BLegID, admitDecision)
		}
		if _, ferr := e.persistBackendIngressFact(ctx, holder, tx.bleg.BLegID); ferr != nil {
			return fmt.Errorf("executor: metering backend ingress fact: %w", ferr)
		}
	}
	authState, err := e.admitAttemptAuthority(ctx, tx.reqFacts.traceID, tx.reqFacts.aLegID, tx.bleg, openCall, c, admitDecision, false)
	if err != nil {
		if authState.admissionResult.Reserved {
			cleanup := e.newAttemptAuthorityLifecycle(authState, c)
			_ = terminalizeAttemptEphemeral(ctx, sdkterminal.CommandPreBackendDenial, false, func(cctx context.Context) error {
				cleanup.Release(cctx, authorityapp.ReleaseKindAdmissionFailure)
				return nil
			})
		}
		return err
	}
	tx.authState = authState
	tx.authLifecycle = e.newAttemptAuthorityLifecycle(authState, c)
	if err := e.enforcePostAdmitClamps(ctx, &openCall, authorizedFreeze, previewedClamps, previewRan, authState, c, int64(admitDecision.Count.InputTokens)); err != nil {
		return err
	}
	if len(previewedClamps) > 0 && !backendCanEnforceAuthorityClamp(be, &openCall) {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted, nil, "")
		return nil
	}
	if admitDecision.RequireMaxOutputEnforcement && !backendCanEnforceAuthorityClamp(be, &openCall) {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted, nil, "")
		return nil
	}
	if werr := checkpoint.AssertNotWidened(authorizedFreeze, openCall); werr != nil {
		return fmt.Errorf("executor: %w", werr)
	}
	replay := execbackend.EffectiveReplaySupport(ctx, be, openCall, c)
	projTarget := lipapi.LegacyProjectionTargetFromCaps(facts.EffectiveCaps, replay)
	projTarget.SupportedExtensions = append([]lipapi.ExtensionRequirement(nil), execbackend.EffectiveDialectSupport(ctx, be, openCall, c).ExtensionTypes...)
	adaptedCall, adaptErr := lipapi.AdaptCallForCandidate(lipapi.CloneCall(openCall), projTarget)
	if adaptErr != nil {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted, nil, "")
		return adaptErr
	}
	wireCall := adaptedCall
	wireCall.Session.ClientSessionID = ""
	wireCall.Session.ContinuityKey = ""
	wireCall.Session.AuthoritativeSessionID = ""
	wireCall.Session.ResumeToken = ""
	if e.RuntimeSnapshot != nil {
		if rawPayload, jerr := json.Marshal(wireCall); jerr == nil {
			sc := tx.reqFacts.recvViews.Scope
			meta := sdktraffic.CaptureMeta{
				TraceID:     tx.reqFacts.traceID,
				ALegID:      tx.reqFacts.aLegID,
				BLegID:      tx.bleg.BLegID,
				AttemptSeq:  tx.bleg.Seq,
				BackendID:   strings.TrimSpace(c.Primary.Backend),
				PrincipalID: strings.TrimSpace(sc.PrincipalID.String()),
				Scope:       sc,
			}
			coretraffic.PortBundleFromSnapshot(e.RuntimeSnapshot).Emit(
				ctx,
				sdktraffic.LegPTB,
				meta,
				"lip/canonical+json",
				"application/json",
				rawPayload,
			)
		}
	}
	baseOpenCtx := ctx
	var cancelOpen context.CancelFunc = func() {}
	ttftDeadline := ttftContextDeadline{}
	if tx.failures != nil && tx.failures.progress != nil && tx.failures.progress.ttft != nil {
		baseOpenCtx, cancelOpen, ttftDeadline = tx.failures.progress.ttft.scopedContext(ctx, e.now(), c.Key, c.Primary.TTFTTimeout)
	}
	defer cancelOpen()
	openCtx, openSpan := otel.Tracer(otelScopeExecutor).Start(
		baseOpenCtx, "lip.executor.backend_open",
		trace.WithAttributes(
			attribute.String("lip.backend", c.Primary.Backend),
			attribute.Int("lip.b_leg_seq", int(tx.bleg.Seq)),
		),
	)
	defer openSpan.End()
	openStart := e.now()
	if aerr := e.assertSecureSessionActiveBeforeOpen(openCtx); aerr != nil {
		return aerr
	}
	tx.openStartedAt = openStart
	tx.openInvoked = true
	tx.backendAttempted = true
	tx.authLifecycle.backendAttempted.Store(true)
	openCtx = identity.WithClientUserAgent(openCtx, wireCall.Invocation.ClientUserAgent)
	openCtx = promptcache.WithObservationLineage(openCtx, promptcache.ObservationLineage{
		ALegID: tx.reqFacts.aLegID, BLegID: tx.bleg.BLegID,
		BackendInstanceID: c.Primary.Backend, CanonicalModelID: c.Primary.Model,
	})
	stream, err := safety.CallValue(safety.BoundaryBackend, "backend_open", func() (lipapi.ManagedEventStream, error) {
		return be.Open(openCtx, wireCall, routing.BackendFacingCandidate(c))
	})
	tx.stream = stream
	openDur := time.Since(openStart).Seconds()
	if err != nil {
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			err = mapBackendPanic(pe, false, c.Key)
		}
	}
	if e.Metrics != nil {
		e.Metrics.OnBackendOpenDuration(c.Primary.Backend, openDur)
	}
	if err != nil {
		if ttftDeadline.expired(openCtx, err) {
			tf := ttftFailure(ttftDeadline.scope, c.Key)
			if ttftDeadline.scope == ttftTimeoutLeaf {
				tx.recordFailure(ctx, lipapi.AttemptSwallowedFailure, ttftAttemptReason(ttftDeadline.scope), tf)
				tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindSwallowed, billing.LegOutcomeNeverStarted, nil, "")
				return nil
			}
			tx.recordFailure(ctx, lipapi.AttemptSurfacedFailure, ttftAttemptReason(ttftDeadline.scope), tf)
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindLosing, billing.LegOutcomeNeverStarted, nil, "")
			return fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, lipapi.ErrTTFTTimeout)
		}
		openSpan.RecordError(err)
		openSpan.SetStatus(codes.Error, "backend open failed")
		if lipapi.IsRecoverablePreOutput(err) {
			if plan.stickyBinding && c.Primary.Backend == plan.stickyBackendID {
				e.clearAffinityBinding(ctx, tx.reqFacts.traceID, tx.routeFacts.affinityKey, tx.routeFacts.affinitySet, "recoverable_pre_output_open")
			}
			tx.recordFailure(ctx, lipapi.AttemptSwallowedFailure, "recoverable pre-output (open)", err)
			diag.LogDecision(
				ctx, e.Log, "recoverable_pre_output_swallowed",
				diag.AttrOpts{CallID: tx.reqFacts.traceID, BLegID: tx.bleg.BLegID},
				slog.String("candidate_key", c.Key),
				slog.String("phase", "open"),
			)
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindSwallowed, billing.LegOutcomeNeverStarted, err, "recoverable pre-output (open)")
			return nil
		}
		tx.recordFailure(ctx, lipapi.AttemptSurfacedFailure, attemptReasonDetail(err), err)
		releaseKind := authorityapp.ReleaseKindLosing
		if errors.Is(err, context.Canceled) && openCtx.Err() == nil {
			releaseKind = authorityapp.ReleaseKindAdmissionFailure
		}
		tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, releaseKind, billing.LegOutcomeNeverStarted, nil, "")
		return fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, err)
	}
	if m := e.secureSessionForAttempt(); m != nil {
		if tx.reqFacts.secureTurnOK {
			tr := buildAttemptTrace(tx.reqFacts.secureTurn, tx.reqFacts.aLegID, tx.bleg, c, openCall, openStart)
			persistCtx := context.WithoutCancel(openCtx)
			if rerr := m.RecordAttemptOpened(persistCtx, tr); rerr != nil && e.Log != nil {
				e.Log.DebugContext(persistCtx, "secure_session_attempt_trace_failed", "error", rerr)
			}
		}
	}
	diag.LogDecision(
		ctx, e.Log, "backend_attempt_opened", diag.AttrOpts{CallID: tx.reqFacts.traceID, BLegID: tx.bleg.BLegID},
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
	// Initialize attempt-local resources on the acquisition owner
	fs, maxArgs := e.resolveToolCallFinalizers()
	tx.accounting = newAttemptAccountingTracker(e.now())
	tx.toolFinal = newToolCallAssembler(fs, maxArgs, tx.reqFacts.baseline.Tools)
	tx.promptCacheSource = promptCacheObservationSource(stream)
	tx.promptCacheController = promptCacheControllerFor(e.Backends[c.Primary.Backend])
	tx.finalStreamObs = &extensions.FinalStreamObservationSession{Log: e.Log, Metrics: e.ExtensionMetrics}
	tx.recordAttemptLoggedFn = e.recordAttemptLogged

	if c.MarkedFirst {
		if err := e.Store.SetWeightedFirstConsumed(ctx, tx.reqFacts.aLegID, true); err != nil {
			return fmt.Errorf("executor: set weighted first consumed: %w", err)
		}
	}
	return nil
}

func (e *Executor) openNext(ctx context.Context, req openNextRequest) (openedAttempt, error) {
	ctx = req.reqFacts.projectContext(ctx, nil)
	p := req.progress
	failures := p.getFailures()
	p.interleaved = req.interleaved
	stickyBackendID, stickyBinding, err := e.lookupAffinityBinding(ctx, req.reqFacts.traceID, req.routeFacts.sel, req.routeFacts.affinityKey, req.routeFacts.affinitySet)
	if err != nil {
		return openedAttempt{interleaved: req.interleaved}, err
	}
	modeRetry := req.mode == openModeRetry
	opts := routing.PlanOptions{
		Excluded:               p.excluded,
		Unhealthy:              e.mergePlannerHealth(),
		RequestSize:            req.routeFacts.requestSize,
		Session:                p.session,
		PreferredCandidateKeys: slices.Clone(req.reqFacts.routePrefs),
		StickyBackendID:        stickyBackendID,
		Rand:                   req.routeFacts.rng,
		IsRetryPath:            modeRetry,
		ThinkerCycle:           p.interleaved.Cycle,
		SuppressThinker:        req.reqFacts.suppressThinker,
	}
	groups, err := routing.ExpandFailoverGroups(req.routeFacts.sel, opts)
	if stickyBinding && stickyBackendID != "" &&
		(err != nil || len(groups) == 0 || len(groups[0].Candidates) == 0 || groups[0].Candidates[0].Primary.Backend != stickyBackendID) {
		e.clearAffinityBinding(ctx, req.reqFacts.traceID, req.routeFacts.affinityKey, req.routeFacts.affinitySet, "ineligible")
		stickyBackendID = ""
		stickyBinding = false
		opts.StickyBackendID = ""
		groups, err = routing.ExpandFailoverGroups(req.routeFacts.sel, opts)
	}
	if err != nil {
		if errors.Is(err, routing.ErrNoEligibleCandidate) {
			if finalErr := failures.FinalError(err); finalErr != err {
				return openedAttempt{interleaved: req.interleaved}, finalErr
			}
		}
		return openedAttempt{interleaved: req.interleaved}, fmt.Errorf("executor: expand failover: %w", err)
	}
	var lastNoOpen openedAttempt
	lastNoOpen.interleaved = req.interleaved
	for gi, group := range groups {
		candidates := group.Candidates
		if len(candidates) == 0 {
			continue
		}
		if candidates[0].IsParallel {
			out, err := e.tryOpenParallelGroup(ctx, req, candidates, group.NextThinkerCycle, stickyBackendID, stickyBinding)
			if err != nil {
				return openedAttempt{interleaved: req.interleaved}, err
			}
			p.interleaved = out.interleaved
			if out.ready != nil {
				failures.ParallelFailure = nil
				return out, nil
			}
			lastNoOpen = out
			if gi+1 < len(groups) {
				continue
			}
			return lastNoOpen, nil
		}
		c := candidates[0]
		plan := candidatePlan{
			cand:            c,
			nextCycle:       group.NextThinkerCycle,
			stickyBackendID: stickyBackendID,
			stickyBinding:   stickyBinding,
		}
		out, err := e.evaluateAndOpenCandidate(ctx, req, plan)
		if err == nil && out.ready != nil {
			failures.ParallelFailure = nil
		}
		return out, err
	}
	return lastNoOpen, nil
}

func (e *Executor) evaluateAndOpenCandidate(ctx context.Context, req openNextRequest, plan candidatePlan) (openedAttempt, error) {
	evalOutcome, err := e.evaluateCandidate(ctx, req.reqFacts, req.routeFacts, plan, req.interleaved)
	if err != nil {
		return openedAttempt{interleaved: req.interleaved}, err
	}
	if !evalOutcome.accepted {
		failures := req.progress.getFailures()
		e.applyRejection(failures, plan.cand, evalOutcome.rejection)
		switch evalOutcome.rejection.kind {
		case rejectAdmission:
			e.noteCandidateAdmissionReject(ctx, req.reqFacts.traceID, req.routeFacts.affinityKey, req.routeFacts.affinitySet, plan.cand, plan.stickyBackendID, plan.stickyBinding, evalOutcome.admitOut, "pre_open", failures)
		case rejectContextLimit:
			diag.LogDecision(
				ctx, e.Log, "context_limit_exclude", diag.AttrOpts{CallID: req.reqFacts.traceID},
				slog.String("candidate_key", plan.cand.Key),
				slog.String("backend", plan.cand.Primary.Backend),
			)
			cat := catalogRouteTraceIfEnabled(e, evalOutcome.facts, evalOutcome.admitOut.admitRes.Capability, nil, true)
			e.notePlanCandidate(ctx, req.reqFacts.traceID, plan.cand.Key, cat)
		case rejectExclude:
			reason, _ := evalOutcome.rejection.detail.(string)
			diag.LogDecision(ctx, e.Log, "attempt_transform_exclude", diag.AttrOpts{CallID: req.reqFacts.traceID},
				slog.String("decision", "exclude_candidate"), slog.String("candidate_key", plan.cand.Key),
				slog.String("backend", plan.cand.Primary.Backend), slog.String("reason_code", reason))
			e.notePlanCandidate(ctx, req.reqFacts.traceID, plan.cand.Key, nil)
		}
		req.progress.excluded[plan.cand.Key] = struct{}{}
		return openedAttempt{ready: nil, interleaved: req.interleaved}, nil
	}
	ranContextEligibility := e.EligibilityResolver != nil
	cat := catalogRouteTraceIfEnabled(e, evalOutcome.facts, evalOutcome.admitOut.admitRes.Capability, &modelcatalog.EligibilityDecision{IsEligible: true, Facts: evalOutcome.facts}, ranContextEligibility)
	e.notePlanCandidate(ctx, req.reqFacts.traceID, plan.cand.Key, cat)
	tx, err := e.startAttemptTx(ctx, req.reqFacts, req.routeFacts, plan.cand, req.progress.budget, req.progress.getFailures())
	if err != nil {
		return openedAttempt{interleaved: req.interleaved}, err
	}
	defer func() {
		if !tx.completed {
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted, nil, "")
		}
	}()
	err = e.openAttemptTx(ctx, tx, evalOutcome, plan)
	if err != nil {
		return openedAttempt{interleaved: req.interleaved}, err
	}
	if tx.completed {
		req.progress.excluded[plan.cand.Key] = struct{}{}
		return openedAttempt{ready: nil, interleaved: req.interleaved}, nil
	}
	interleaved := req.interleaved
	if plan.nextCycle != nil {
		interleaved.Cycle = *plan.nextCycle
	}
	var memoUpdate *interleavedthinking.PendingMemoUpdate
	if req.reqFacts.suppressThinker {
		if plan.nextCycle != nil {
			if perr := e.persistInterleavedState(ctx, req.reqFacts.aLegID, interleaved); perr != nil {
				outcome := billing.LegOutcomeFailed
				if errors.Is(perr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
					outcome = billing.LegOutcomeCanceled
				}
				tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindAdmissionFailure, outcome, nil, "")
				return openedAttempt{interleaved: req.interleaved}, fmt.Errorf("executor: persist interleaved cycle: %w", perr)
			}
		}
		memoUpdate = evalOutcome.shapeRes.MemoUpdate
	} else {
		if evalOutcome.shapeRes.MemoUpdate != nil {
			interleaved, err = e.commitMemoInjection(ctx, req.reqFacts.aLegID, interleaved, evalOutcome.shapeRes.MemoUpdate)
			if err != nil {
				outcome := billing.LegOutcomeFailed
				if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
					outcome = billing.LegOutcomeCanceled
				}
				tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindAdmissionFailure, outcome, nil, "")
				return openedAttempt{interleaved: req.interleaved}, err
			}
		} else if plan.nextCycle != nil {
			if perr := e.persistInterleavedState(ctx, req.reqFacts.aLegID, interleaved); perr != nil {
				outcome := billing.LegOutcomeFailed
				if errors.Is(perr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
					outcome = billing.LegOutcomeCanceled
				}
				tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindAdmissionFailure, outcome, nil, "")
				return openedAttempt{interleaved: req.interleaved}, fmt.Errorf("executor: persist interleaved cycle: %w", perr)
			}
		}
	}
	if req.reqFacts.aScope != nil {
		if err := req.reqFacts.aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
			ID:      tx.bleg.BLegID,
			Attempt: lifecycleAttempt(tx.stream),
		}); err != nil {
			tx.stream = nil
			outcome := billing.LegOutcomeFailed
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				outcome = billing.LegOutcomeCanceled
			}
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindSwallowed, outcome, nil, "")
			return openedAttempt{interleaved: req.interleaved}, err
		}
		tx.registered = true
	}
	e.logInterleavedRouteSelected(ctx, req.reqFacts.traceID, tx.bleg.BLegID, plan.cand, req.interleaved.Cycle, interleaved.Cycle)
	ready := tx.HandoffReady(pendingSelectionEffects{
		interleaved: interleaved,
		memoUpdate:  memoUpdate,
	})
	return openedAttempt{
		ready:       ready,
		interleaved: interleaved,
		memoUpdate:  memoUpdate,
	}, nil
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
		var model, backend string
		if primary := firstSelectorPrimary(sel); primary != nil {
			model, backend = primary.Model, primary.Backend
		}
		if decision := e.Preflight.Check(ctx, accountingpreflight.Input{Backend: backend, Model: model, CallID: call.ID, Call: call}); decision.Err == nil && decision.Reason != accountingpreflight.ReasonDisabled {
			return routing.RequestSizeEstimate{Available: true, Tokens: int64(decision.Count.InputTokens) + 1, Basis: "token_accounting_preflight"}
		}
	}
	if !routing.SelectorHasRequestSizeConstraints(sel) || e.RequestTokenEstimator == nil {
		return routing.RequestSizeEstimate{}
	}
	est := e.RequestTokenEstimator.EstimateRequestTokens(ctx, call)
	return routing.RequestSizeEstimate{Available: est.Available, Tokens: est.Input, Basis: est.Basis}
}

func (e *Executor) runPreflight(ctx context.Context, traceID string, call lipapi.Call, c routing.AttemptCandidate, facts modelcatalog.ModelFacts) (accountingpreflight.Decision, bool) {
	if e.Preflight == nil {
		return accountingpreflight.Decision{}, false
	}
	return e.Preflight.Check(ctx, accountingpreflight.Input{
		Backend:                  c.Primary.Backend,
		Model:                    c.Primary.Model,
		CallID:                   traceID,
		Call:                     call,
		RequestedMaxOutputTokens: call.Options.MaxOutputTokens,
		Facts:                    facts,
	}), true
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
