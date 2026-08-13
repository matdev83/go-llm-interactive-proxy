package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	coreauth "github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

const (
	syntheticLocalPrincipalID     = "local-dev"
	syntheticLocalPrincipalIssuer = "lip-localhost"
)

// prepareSubmitAndALegSecure resolves principal and workspace, authorizes via secure-session BeginTurn
// before submit hooks and extension stages, so hooks and CTP traffic see proxy-validated session
// continuity (not client-forged ALegID / resume authority).
func (e *Executor) prepareSubmitAndALegSecure(
	ctx context.Context,
	bus *hooks.Bus,
	call *lipapi.Call,
) (
	traceID string,
	baseline lipapi.Call,
	aLeg b2bua.ALegRecord,
	routeAuth routeAuthoritySnapshot,
	outCtx context.Context,
	err error,
) {
	snap := e.RuntimeSnapshot
	work := *call
	traceID = strings.TrimSpace(work.ID)
	if traceID == "" {
		traceID = diag.StableCallID(&work)
	}
	work.ID = traceID
	call.ID = traceID

	outCtx = ctx
	var principal execview.PrincipalView
	hasPrincipal := false
	var reqScope scope.PrincipalScopeView
	if s, p, ok := e.resolveRequestScope(ctx); ok {
		reqScope = s
		principal = p
		hasPrincipal = true
		outCtx = execview.WithPrincipal(outCtx, p)
		outCtx = scope.WithScope(outCtx, s)
	}
	outCtx = diag.WithCallDiag(outCtx, traceID, "")

	preSession := session.SessionView{
		AuthoritativeSessionID: strings.TrimSpace(work.Session.AuthoritativeSessionID),
		ClientSessionHint:      strings.TrimSpace(work.Session.ClientSessionID),
		ALegID:                 "",
		IsNew:                  false,
		ResumeEligible:         false,
	}
	if snap != nil {
		openIn := session.OpenInput{TraceID: traceID, Principal: principal, Session: preSession}
		openRes := extensions.RunSessionOpenStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.SessionOpeners(),
			openIn,
		)
		for k, v := range openRes.SessionLabelUpserts {
			if preSession.Labels == nil {
				preSession.Labels = make(map[string]string, len(openRes.SessionLabelUpserts))
			}
			preSession.Labels[k] = v
		}
	}

	var wsView lipworkspace.WorkspaceView
	if snap != nil {
		wsStart := time.Now()
		wsCtx, wsSpan := otel.Tracer(otelScopeExecutor).Start(outCtx, "lip.executor.workspace_resolve")
		var werr error
		wsView, werr = snap.Workspace().Resolve(wsCtx)
		outcome := "ok"
		if werr != nil {
			if e != nil && e.SecureSessionWorkspaceResolveFailClosed {
				outcome = "fail_closed"
				wsSpan.RecordError(werr)
				wsSpan.SetStatus(codes.Error, "workspace resolve failed")
				wsSpan.End()
				outCtx = wsCtx
				if e.ExtensionMetrics != nil {
					e.ExtensionMetrics.ObserveStage(extensions.MetricsStageWorkspaceResolve, outcome, time.Since(wsStart).Seconds())
				}
				mapped := domain.ErrWorkspaceUnresolved
				if e.SessionDenialMapper != nil {
					mapped = e.SessionDenialMapper(domain.ErrWorkspaceUnresolved)
				}
				if e.SecureSessionMetrics != nil {
					code := lipapi.SessionDenialPublicCode(mapped)
					if code == "" {
						code = "unknown"
					}
					e.SecureSessionMetrics.ObserveBeginTurnDenied(code)
				}
				if e.Log != nil {
					e.Log.InfoContext(
						outCtx, "secure_session: workspace resolve denied",
						"code", lipapi.SessionDenialPublicCode(mapped),
						"trace_id", strings.TrimSpace(traceID),
						"error", werr,
					)
				}
				return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, fmt.Errorf("executor: secure session: %w", mapped)
			}
			outcome = "fail_open"
			if e.Log != nil {
				e.Log.DebugContext(wsCtx, "workspace: resolve error (fail-open)", "error", werr)
			}
			wsSpan.RecordError(werr)
			wsSpan.SetStatus(codes.Error, "workspace resolve failed")
		} else {
			wsSpan.SetStatus(codes.Ok, "")
		}
		wsSpan.End()
		outCtx = wsCtx
		if e.ExtensionMetrics != nil {
			e.ExtensionMetrics.ObserveStage(extensions.MetricsStageWorkspaceResolve, outcome, time.Since(wsStart).Seconds())
		}
	}

	beginIn := app.BeginInput{
		Now:                    e.now(),
		TraceID:                traceID,
		Session:                secureSessionWireFromLipAPI(work.Session),
		Principal:              principalRefFromScope(principal, reqScope),
		Workspace:              domain.WorkspaceRef{ID: strings.TrimSpace(wsView.ID)},
		GlobalPolicy:           app.DefaultGlobalPolicy(),
		ClientHints:            domain.ClientHints{ClientSessionID: strings.TrimSpace(work.Session.ClientSessionID)},
		FirstMessageDigest:     "",
		WorkspaceMatchRequired: e != nil && e.SecureSessionRequireWorkspaceID,
	}
	br, err := e.SecureSession.BeginTurn(outCtx, beginIn)
	if err != nil {
		mapped := err
		if e != nil && e.SessionDenialMapper != nil {
			mapped = e.SessionDenialMapper(err)
		}
		if e != nil && e.SecureSessionMetrics != nil {
			if errors.Is(err, domain.ErrStorageUnavailable) {
				e.SecureSessionMetrics.ObserveStorageUnavailable()
			}
			code := lipapi.SessionDenialPublicCode(mapped)
			if code == "" {
				code = "unknown"
			}
			e.SecureSessionMetrics.ObserveBeginTurnDenied(code)
		}
		if e != nil && e.Log != nil {
			logCode := lipapi.SessionDenialPublicCode(mapped)
			if logCode == "" {
				logCode = "unknown"
			}
			e.Log.InfoContext(
				outCtx, "secure_session: begin turn denied",
				"code", logCode,
				"trace_id", strings.TrimSpace(traceID),
				"client_session_id", HashOpaqueIDForLog(work.Session.ClientSessionID),
			)
		}
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, fmt.Errorf("executor: secure session: %w", mapped)
	}
	if e != nil && e.SecureSessionMetrics != nil {
		if br.IsNew {
			e.SecureSessionMetrics.ObserveBeginTurnNew()
		} else {
			e.SecureSessionMetrics.ObserveBeginTurnResume()
		}
	}

	work.Session.AuthoritativeSessionID = string(br.Record.SessionID)
	work.Session.ALegID = strings.TrimSpace(br.Record.ALegID)
	work.Session.ResumeToken = ""

	aLeg, err = e.Store.FetchALeg(outCtx, br.Record.ALegID)
	if err != nil {
		return "",
			lipapi.Call{},
			b2bua.ALegRecord{},
			routeAuthoritySnapshot{},
			outCtx,
			fmt.Errorf("executor: fetch a-leg after secure session: %w", err)
	}
	work.Session.ContinuityKey = strings.TrimSpace(aLeg.ContinuityKey)
	work.Session.ALegID = aLeg.ALegID

	routeAuth, err = e.snapshotRouteOverride(outCtx, aLeg.ALegID)
	if err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, err
	}

	// Deterministic test cut after the override snapshot and before
	// submit/request stages and route-plan construction (design D3 snapshot point).
	if err := waitRouteAuthoritySnapshotBarrier(outCtx, aLeg.ALegID); err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, fmt.Errorf("executor: route authority snapshot barrier: %w", err)
	}

	preSession.ALegID = aLeg.ALegID
	preSession.AuthoritativeSessionID = string(br.Record.SessionID)
	preSession.IsNew = br.IsNew
	preSession.ResumeEligible = br.Record.ResumeEligible
	preSession.TurnID = string(br.TurnID)
	preSession.WorkspaceID = strings.TrimSpace(wsView.ID)

	// One base policy decision evidence seam carries the shared Emitter and
	// TimeoutBudget for the whole prepare phase (secret_guard through submit and
	// pre-backend stages). Views are refreshed per phase via WithViews.
	// Emitter stays nil for the no-op observer default so no evidence/logs are
	// produced (requirements 7.6, 10.5). ALegID is known post-BeginTurn;
	// BLegID/AttemptSeq are unknown pre-backend, correct for no-backend-attempt
	// evidence.
	var baseEvidence *extensions.DecisionEvidence
	if snap != nil {
		guardViews := execctx.Views{
			Principal: principal,
			Scope:     reqScope,
			Session:   preSession,
			Attempt:   execview.AttemptView{TraceID: traceID},
			Workspace: wsView,
		}
		baseEvidence = &extensions.DecisionEvidence{
			Emitter:       e.policyEvidenceEmitter(snap),
			Views:         guardViews,
			TimeoutBudget: snap.TimeoutBudgetSource(),
			TimeoutGuard:  snap.ProviderTimeoutGuard(),
		}
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence)
		if err := e.runSecretGuardStage(outCtx, &work, secretGuardStageInput{
			TraceID:   traceID,
			Principal: principal,
			Scope:     reqScope,
			Session:   preSession,
			Workspace: wsView,
			SessionID: br.Record.SessionID,
			TurnID:    br.TurnID,
		}); err != nil {
			return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, err
		}
	}

	var meteringHolder *checkpoint.RequestHolder
	outCtx, meteringHolder, err = captureFrontendIngressBeforeSubmit(outCtx, work, reqScope, e.now())
	if err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, err
	}
	_ = meteringHolder
	outCtx, err = e.admitRequestAuthorityOnce(outCtx, work.ID, aLeg.ALegID, traceID, reqScope)
	if err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, err
	}
	// Release request-stage holds if prepare fails after a successful admit
	// (settlement only runs once output is committed).
	failAfterRequestAdmit := func(err error) (string, lipapi.Call, b2bua.ALegRecord, routeAuthoritySnapshot, context.Context, error) {
		_ = e.releaseRequestAuthority(outCtx)
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, err
	}

	submitMeta := &sdk.SubmitMeta{TraceID: traceID, Annotations: map[string]string{}}
	if e.Log != nil {
		outCtx = hooks.WithDiagnosticsLogger(outCtx, e.Log)
	}
	if snap != nil {
		submitViews := execctx.Views{
			Principal:   principal,
			Scope:       reqScope,
			Session:     preSession,
			Attempt:     execview.AttemptView{TraceID: traceID},
			Workspace:   wsView,
			Annotations: submitMeta.Annotations,
		}
		baseEvidence = baseEvidence.WithViews(submitViews)
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence)
		outCtx = hooks.WithSubmitEvidence(outCtx, extensions.NewSubmitEvidenceFunc(baseEvidence))
	}
	if err := bus.RunSubmit(outCtx, &work, submitMeta); err != nil {
		return failAfterRequestAdmit(err)
	}
	if snap != nil {
		ctpCall := work
		ctpSess := work.Session
		ctpSess.ResumeToken = ""
		ctpCall.Session = ctpSess
		if rawPayload, jerr := json.Marshal(&ctpCall); jerr == nil {
			meta := sdktraffic.CaptureMeta{
				TraceID:     traceID,
				ALegID:      strings.TrimSpace(aLeg.ALegID),
				SessionID:   ctpCall.Session.CorrelationID(),
				PrincipalID: strings.TrimSpace(principal.ID),
				Scope:       reqScope.Clone(),
			}
			coretraffic.PortBundleFromSnapshot(snap).Emit(
				outCtx,
				sdktraffic.LegCTP,
				meta,
				"lip/canonical+json",
				"application/json",
				rawPayload,
			)
		} else if e.Log != nil {
			e.Log.DebugContext(outCtx, "submit traffic marshal skipped", "leg", sdktraffic.LegCTP, "error", jerr)
		}
		ann := maps.Clone(submitMeta.Annotations)
		if ann == nil {
			ann = make(map[string]string, len(submitMeta.Annotations))
		}
		reqMeta := request.RequestMeta{
			TraceID:     traceID,
			Annotations: ann,
			Principal:   principal,
			Scope:       reqScope,
			Session:     preSession,
			Workspace:   wsView,
		}
		if reqMeta.Annotations == nil {
			reqMeta.Annotations = make(map[string]string, len(submitMeta.Annotations))
		}
		// Decision-context views for pre-backend stages. ALegID comes from preSession
		// (set after BeginTurn authority); BLegID/AttemptSeq are unknown pre-backend, which is
		// correct for no-backend-attempt evidence (req 8.1, 8.6). The evidence seam is attached
		// to the context so the stage runners project per-provider decisions themselves; runtime
		// only establishes context and does not emit aggregate runtime evidence.
		pdViews := decisionViewsFromRequestMeta(reqMeta, execview.AttemptView{TraceID: traceID})
		// Refresh the base seam's views for post-submit pre-backend stages before
		// the first such stage runs. The base seam is re-attached whenever a
		// snapshot is present so non-default timeout budgets are enforced even
		// without a policy observer. Emitter stays nil for the no-op observer
		// default so no evidence/logs are produced (requirements 7.6, 10.5);
		// emitDecisionRecord and the tool reactor evidence func are no-ops on a
		// nil emitter. The default zero-budget source keeps the
		// no-timeout/no-observer path cheap and silent (legacy synchronous
		// behavior). The submit evidence func captured the original baseEvidence
		// pointer, so its submit-time views remain unchanged for the already-
		// completed RunSubmit call.
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence.WithViews(pdViews))
		catalogMeta := toolcatalog.CatalogMeta{
			TraceID:     traceID,
			Annotations: ann,
			Principal:   principal,
			Session:     preSession,
			Workspace:   wsView,
		}
		catSvc := toolcatalog.Services{State: snap.State(), Aux: snap.Aux()}
		if err := extensions.RunToolCatalogFilterStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.ToolCatalogFilters(),
			&work,
			catalogMeta,
			catSvc,
		); err != nil {
			return failAfterRequestAdmit(err)
		}
		reqSvc := request.Services{State: snap.State(), Aux: snap.Aux()}
		if err := extensions.RunRequestTransformStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.RequestTransforms(),
			&work,
			reqMeta,
			reqSvc,
		); err != nil {
			return failAfterRequestAdmit(err)
		}
		preMeta := prerequest.Meta{
			TraceID:        traceID,
			Annotations:    ann,
			Principal:      principal,
			Scope:          reqScope,
			Session:        preSession,
			Workspace:      wsView,
			AuxiliaryDepth: execctx.AuxiliaryDepth(outCtx),
		}
		preSvc := prerequest.Services{State: snap.State(), Aux: snap.Aux()}
		if err := extensions.RunPreRequestStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.PreRequestHandlers(),
			&work,
			preMeta,
			preSvc,
		); err != nil {
			return failAfterRequestAdmit(err)
		}
	}

	effective := lipapi.CloneCall(work)
	if routeAuth.active() {
		effective.Route.Selector = routeAuth.State.Selector
	}
	if snap != nil {
		hintIn := routehint.Input{
			TraceID:   traceID,
			Call:      &effective,
			Principal: principal,
			Session:   preSession,
			Workspace: wsView,
		}
		prefs, err := extensions.RunRouteHintStage(
			outCtx,
			e.Log,
			snap.RouteHintProviders(),
			&effective,
			hintIn,
		)
		if err != nil {
			return failAfterRequestAdmit(err)
		}
		outCtx = execctx.WithRouteCandidatePreferences(outCtx, prefs)
	}

	baseline = lipapi.CloneCall(effective)
	call.Session = work.Session
	if br.IsNew && len(br.Response.ResumeToken) > 0 {
		// Client-visible bearer for resume; never included on baseline (backend attempts).
		call.Session.ResumeToken = string(br.Response.ResumeToken)
	}
	outCtx = diag.EnsureCallDiag(outCtx, traceID, aLeg.ALegID)

	policyLabels := policyLabelsFromMetadata(br.EffectivePolicy)
	views := execctx.ViewsFromSecureSubmit(execctx.SecureSubmitViewsInput{
		TraceID:                traceID,
		ALeg:                   aLeg,
		Call:                   baseline,
		HookAnnotations:        submitMeta.Annotations,
		AuthoritativeSessionID: string(br.Record.SessionID),
		TurnID:                 string(br.TurnID),
		ResumeEligible:         br.Record.ResumeEligible,
		PolicyLabels:           policyLabels,
	})
	if hasPrincipal {
		views.Principal = principal
		views.Scope = reqScope
	}
	views.Workspace = wsView
	views.Session.WorkspaceID = strings.TrimSpace(wsView.ID)
	for k, v := range preSession.Labels {
		if views.Session.Labels == nil {
			views.Session.Labels = make(map[string]string, len(preSession.Labels))
		}
		views.Session.Labels[k] = v
	}
	outCtx = execctx.WithSecureSessionTurn(outCtx, execctx.SecureSessionTurn{
		SessionID: br.Record.SessionID,
		TurnID:    br.TurnID,
		Policy:    br.EffectivePolicy,
	})
	if e.SecureSessionRecorder != nil {
		in := buildClientTurnRecordInput(e.now(), traceID, br, &work)
		if err := e.SecureSessionRecorder.RecordClientTurnAfterGate(outCtx, in); err != nil {
			if e.SecureSessionMetrics != nil {
				e.SecureSessionMetrics.ObserveRecorderClientTurnFailed(e.SecureSessionRecordingMandatory)
			}
			if e.SecureSessionRecordingMandatory {
				return failAfterRequestAdmit(fmt.Errorf("executor: secure session recording: %w", err))
			}
			if e.Log != nil {
				e.Log.DebugContext(outCtx, "secure_session recorder client turn", "error", err)
			}
		}
	}
	outCtx = execctx.WithViews(outCtx, views)
	if err := e.emitSessionStartIfNeeded(
		outCtx,
		traceID,
		principalSnapshotForSessionAudit(principal),
		br,
		work,
		aLeg,
	); err != nil {
		return failAfterRequestAdmit(err)
	}
	return traceID, baseline, aLeg, routeAuth, outCtx, nil
}

func secureSessionWireFromLipAPI(s lipapi.SessionRef) app.SessionWire {
	return app.SessionWire{
		ClientSessionID: s.ClientSessionID,
		ContinuityKey:   s.ContinuityKey,
		ALegID:          s.ALegID,
		SessionID:       s.AuthoritativeSessionID,
		ResumeToken:     s.ResumeToken,
	}
}

func policyLabelsFromMetadata(p domain.PolicyMetadata) map[string]string {
	out := make(map[string]string)
	if s := strings.TrimSpace(p.PolicyVersion); s != "" {
		out["policy_version"] = s
	}
	if s := strings.TrimSpace(p.EffectiveTreatment); s != "" {
		out["effective_treatment"] = s
	}
	if s := strings.TrimSpace(p.AuditMode); s != "" {
		out["audit_mode"] = s
	}
	if s := strings.TrimSpace(p.RedactionProfile); s != "" {
		out["redaction_profile"] = s
	}
	if p.TranscriptEnabled {
		out["transcript_enabled"] = "true"
	} else {
		out["transcript_enabled"] = "false"
	}
	return out
}

func principalSnapshotForSessionAudit(p execview.PrincipalView) coreauth.PrincipalSnapshot {
	return coreauth.NewPrincipalSnapshot(p.ID, p.DisplayName)
}
