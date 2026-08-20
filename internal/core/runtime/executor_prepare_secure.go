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

func (e *Executor) prepareSubmitAndALegSecure(
	ctx context.Context,
	bus *hooks.Bus,
	call *lipapi.Call,
) (
	ibt *identityBoundTurn,
	workingCall *lipapi.Call,
	outCtx context.Context,
	err error,
) {
	snap := e.RuntimeSnapshot
	work := *call
	traceID := strings.TrimSpace(work.ID)
	if traceID == "" {
		traceID = diag.StableCallID(&work)
	}
	work.ID, call.ID, outCtx = traceID, traceID, ctx
	var principal execview.PrincipalView
	hasPrincipal := false
	var reqScope scope.PrincipalScopeView
	if s, p, ok := e.resolveRequestScope(ctx); ok {
		reqScope, principal, hasPrincipal, outCtx = s, p, true, scope.WithScope(execview.WithPrincipal(outCtx, p), s)
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
				preSession.Labels = make(map[string]string)
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
					e.Log.InfoContext(outCtx, "secure_session: workspace resolve denied", "code", lipapi.SessionDenialPublicCode(mapped), "trace_id", strings.TrimSpace(traceID), "error", werr)
				}
				return nil, nil, outCtx, fmt.Errorf("executor: secure session: %w", mapped)
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
			e.Log.InfoContext(outCtx, "secure_session: begin turn denied", "code", logCode, "trace_id", strings.TrimSpace(traceID), "client_session_id", HashOpaqueIDForLog(work.Session.ClientSessionID))
		}
		return nil, nil, outCtx, fmt.Errorf("executor: secure session: %w", mapped)
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
	aLeg, err := e.Store.FetchALeg(outCtx, br.Record.ALegID)
	if err != nil {
		return nil, nil,
			outCtx,
			fmt.Errorf("executor: fetch a-leg after secure session: %w", err)
	}
	work.Session.ContinuityKey = strings.TrimSpace(aLeg.ContinuityKey)
	work.Session.ALegID = aLeg.ALegID
	routeAuth, err := e.snapshotRouteOverride(outCtx, aLeg.ALegID)
	if err != nil {
		return nil, nil, outCtx, err
	}
	if err := waitRouteAuthoritySnapshotBarrier(outCtx, aLeg.ALegID); err != nil {
		return nil, nil, outCtx, fmt.Errorf("executor: route authority snapshot barrier: %w", err)
	}
	preSession.ALegID = aLeg.ALegID
	preSession.AuthoritativeSessionID = string(br.Record.SessionID)
	preSession.IsNew = br.IsNew
	preSession.ResumeEligible = br.Record.ResumeEligible
	preSession.TurnID = string(br.TurnID)
	preSession.WorkspaceID = strings.TrimSpace(wsView.ID)
	secureTurn := execctx.SecureSessionTurn{
		SessionID: br.Record.SessionID,
		TurnID:    br.TurnID,
		Policy:    br.EffectivePolicy,
	}
	secureTurnOK := true
	ibt, err = newIdentityBoundTurn(traceID, &work, principal, reqScope, hasPrincipal, wsView, aLeg, routeAuth, secureTurn, secureTurnOK, preSession)
	if err != nil {
		return nil, nil, outCtx, fmt.Errorf("executor: create identity bound turn: %w", err)
	}
	var baseEvidence *extensions.DecisionEvidence
	workingCall = &work
	if snap != nil {
		guardViews := execctx.Views{
			Principal: ibt.principal,
			Scope:     ibt.scope,
			Session:   ibt.preSession,
			Attempt:   execview.AttemptView{TraceID: ibt.traceID},
			Workspace: ibt.workspace,
		}
		baseEvidence = &extensions.DecisionEvidence{
			Emitter:       e.policyEvidenceEmitter(snap),
			Views:         guardViews,
			TimeoutBudget: snap.TimeoutBudgetSource(),
			TimeoutGuard:  snap.ProviderTimeoutGuard(),
		}
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence)
		if err := e.runSecretGuardStage(outCtx, workingCall, secretGuardStageInput{
			TraceID:   ibt.traceID,
			Principal: ibt.principal,
			Scope:     ibt.scope,
			Session:   ibt.preSession,
			Workspace: ibt.workspace,
			SessionID: ibt.secureTurn.SessionID,
			TurnID:    ibt.secureTurn.TurnID,
		}); err != nil {
			return nil, nil, outCtx, err
		}
	}
	var meteringHolder *checkpoint.RequestHolder
	outCtx, meteringHolder, err = captureFrontendIngressBeforeSubmit(outCtx, *workingCall, ibt.scope, e.now())
	if err != nil {
		return nil, nil, outCtx, err
	}
	_ = meteringHolder
	outCtx, err = e.admitRequestAuthorityOnce(outCtx, workingCall.ID, ibt.aLeg.ALegID, ibt.traceID, ibt.scope)
	if err != nil {
		return nil, nil, outCtx, err
	}
	failAfterRequestAdmit := func(err error) (*identityBoundTurn, *lipapi.Call, context.Context, error) {
		_ = e.releaseRequestAuthority(outCtx)
		return nil, nil, outCtx, err
	}
	submitMeta := &sdk.SubmitMeta{TraceID: ibt.traceID, Annotations: map[string]string{}}
	if e.Log != nil {
		outCtx = hooks.WithDiagnosticsLogger(outCtx, e.Log)
	}
	if snap != nil {
		submitViews := execctx.Views{
			Principal:   ibt.principal,
			Scope:       ibt.scope,
			Session:     ibt.preSession,
			Attempt:     execview.AttemptView{TraceID: ibt.traceID},
			Workspace:   ibt.workspace,
			Annotations: submitMeta.Annotations,
		}
		baseEvidence = baseEvidence.WithViews(submitViews)
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence)
		outCtx = hooks.WithSubmitEvidence(outCtx, extensions.NewSubmitEvidenceFunc(baseEvidence))
	}
	if err := bus.RunSubmit(outCtx, workingCall, submitMeta); err != nil {
		return failAfterRequestAdmit(err)
	}
	if snap != nil {
		ctpCall := *workingCall
		ctpSess := workingCall.Session
		ctpSess.ResumeToken = ""
		ctpCall.Session = ctpSess
		if rawPayload, jerr := json.Marshal(&ctpCall); jerr == nil {
			meta := sdktraffic.CaptureMeta{
				TraceID:     ibt.traceID,
				ALegID:      strings.TrimSpace(ibt.aLeg.ALegID),
				SessionID:   ctpCall.Session.CorrelationID(),
				PrincipalID: strings.TrimSpace(ibt.principal.ID),
				Scope:       ibt.scope.Clone(),
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
			TraceID:     ibt.traceID,
			Annotations: ann,
			Principal:   ibt.principal,
			Scope:       ibt.scope,
			Session:     ibt.preSession,
			Workspace:   ibt.workspace,
		}
		if reqMeta.Annotations == nil {
			reqMeta.Annotations = make(map[string]string, len(submitMeta.Annotations))
		}
		pdViews := decisionViewsFromRequestMeta(reqMeta, execview.AttemptView{TraceID: ibt.traceID})
		outCtx = extensions.WithDecisionEvidence(outCtx, baseEvidence.WithViews(pdViews))
		catalogMeta := toolcatalog.CatalogMeta{
			TraceID:     ibt.traceID,
			Annotations: ann,
			Principal:   ibt.principal,
			Session:     ibt.preSession,
			Workspace:   ibt.workspace,
		}
		catSvc := toolcatalog.Services{State: snap.State(), Aux: snap.Aux()}
		if err := extensions.RunToolCatalogFilterStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.ToolCatalogFilters(),
			workingCall,
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
			workingCall,
			reqMeta,
			reqSvc,
		); err != nil {
			return failAfterRequestAdmit(err)
		}
		preMeta := prerequest.Meta{
			TraceID:        ibt.traceID,
			Annotations:    ann,
			Principal:      ibt.principal,
			Scope:          ibt.scope,
			Session:        ibt.preSession,
			Workspace:      ibt.workspace,
			AuxiliaryDepth: execctx.AuxiliaryDepth(outCtx),
		}
		preSvc := prerequest.Services{State: snap.State(), Aux: snap.Aux()}
		if err := extensions.RunPreRequestStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			snap.PreRequestHandlers(),
			workingCall,
			preMeta,
			preSvc,
		); err != nil {
			return failAfterRequestAdmit(err)
		}
	}
	effective := lipapi.CloneCall(*workingCall)
	if ibt.routeAuth.active() {
		effective.Route.Selector = ibt.routeAuth.State.Selector
	}
	if snap != nil {
		hintIn := routehint.Input{
			TraceID:   ibt.traceID,
			Call:      &effective,
			Principal: ibt.principal,
			Session:   ibt.preSession,
			Workspace: ibt.workspace,
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
	*workingCall = lipapi.CloneCall(effective)
	call.Session = workingCall.Session
	if br.IsNew && len(br.Response.ResumeToken) > 0 {
		call.Session.ResumeToken = string(br.Response.ResumeToken)
	}
	outCtx = diag.EnsureCallDiag(outCtx, ibt.traceID, ibt.aLeg.ALegID)
	policyLabels := policyLabelsFromMetadata(br.EffectivePolicy)
	views := execctx.ViewsFromSecureSubmit(execctx.SecureSubmitViewsInput{
		TraceID:                ibt.traceID,
		ALeg:                   ibt.aLeg,
		Call:                   *workingCall,
		HookAnnotations:        submitMeta.Annotations,
		AuthoritativeSessionID: string(br.Record.SessionID),
		TurnID:                 string(br.TurnID),
		ResumeEligible:         br.Record.ResumeEligible,
		PolicyLabels:           policyLabels,
	})
	if ibt.hasPrincipal {
		views.Principal = ibt.principal
		views.Scope = ibt.scope
	}
	views.Workspace = ibt.workspace
	views.Session.WorkspaceID = strings.TrimSpace(ibt.workspace.ID)
	if len(ibt.preSession.Labels) > 0 {
		if views.Session.Labels == nil {
			views.Session.Labels = make(map[string]string)
		}
		maps.Copy(views.Session.Labels, ibt.preSession.Labels)
	}
	outCtx = execctx.WithSecureSessionTurn(outCtx, ibt.secureTurn)
	if e.SecureSessionRecorder != nil {
		in := buildClientTurnRecordInput(e.now(), ibt.traceID, br, workingCall)
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
		ibt.traceID,
		principalSnapshotForSessionAudit(ibt.principal),
		br,
		*workingCall,
		ibt.aLeg,
	); err != nil {
		return failAfterRequestAdmit(err)
	}
	return ibt, workingCall, outCtx, nil
}

func secureSessionWireFromLipAPI(s lipapi.SessionRef) app.SessionWire {
	return app.SessionWire{ClientSessionID: s.ClientSessionID, ContinuityKey: s.ContinuityKey, ALegID: s.ALegID, SessionID: s.AuthoritativeSessionID, ResumeToken: s.ResumeToken}
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
	out["transcript_enabled"] = "false"
	if p.TranscriptEnabled {
		out["transcript_enabled"] = "true"
	}
	return out
}

func principalSnapshotForSessionAudit(p execview.PrincipalView) coreauth.PrincipalSnapshot {
	return coreauth.NewPrincipalSnapshot(p.ID, p.DisplayName)
}
