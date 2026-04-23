package runtime

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// prepareSubmitAndALeg assigns trace id, runs session_open and workspace resolution before submit hooks (R12),
// then runs submit hooks, resolves the A-leg row, and publishes [execctx.Views].
func (e *Executor) prepareSubmitAndALeg(ctx context.Context, bus *hooks.Bus, call *lipapi.Call) (traceID string, baseline lipapi.Call, aLeg b2bua.ALegRecord, outCtx context.Context, err error) {
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
	if p, ok := execview.PrincipalFromContext(ctx); ok {
		principal = p
		hasPrincipal = true
		outCtx = execview.WithPrincipal(outCtx, p)
	}
	outCtx = diag.WithCallDiag(outCtx, traceID, "")

	preALeg, err := continuity.ResolveALegRecord(outCtx, e.Store, work.Session)
	if err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, outCtx, err
	}
	preSession := session.SessionView{
		SessionID: strings.TrimSpace(work.Session.ClientSessionID),
		ALegID:    strings.TrimSpace(preALeg.ALegID),
		IsNew:     aLegRecordIsNew(preALeg),
	}
	if e.RuntimeSnapshot != nil {
		openIn := session.OpenInput{TraceID: traceID, Principal: principal, Session: preSession}
		openRes := extensions.RunSessionOpenStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			e.RuntimeSnapshot.SessionOpeners(),
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
	if e.RuntimeSnapshot != nil {
		wsStart := time.Now()
		wsCtx, wsSpan := otel.Tracer(otelScopeExecutor).Start(outCtx, "lip.executor.workspace_resolve")
		var werr error
		wsView, werr = e.RuntimeSnapshot.Workspace().Resolve(wsCtx)
		outcome := "ok"
		if werr != nil {
			outcome = "fail_open"
			if e.Log != nil {
				e.Log.DebugContext(wsCtx, "workspace: resolve error (fail-open)", "error", werr)
			}
			wsSpan.RecordError(werr)
			wsSpan.SetStatus(codes.Error, werr.Error())
		} else {
			wsSpan.SetStatus(codes.Ok, "")
		}
		wsSpan.End()
		outCtx = wsCtx
		if e.ExtensionMetrics != nil {
			e.ExtensionMetrics.ObserveStage(extensions.MetricsStageWorkspaceResolve, outcome, time.Since(wsStart).Seconds())
		}
	}

	submitMeta := &sdk.SubmitMeta{TraceID: traceID, Annotations: map[string]string{}}
	if err := bus.RunSubmit(outCtx, &work, submitMeta); err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, outCtx, err
	}
	if e.RuntimeSnapshot != nil {
		if rawPayload, jerr := json.Marshal(&work); jerr == nil {
			meta := sdktraffic.CaptureMeta{
				TraceID:     traceID,
				ALegID:      strings.TrimSpace(preSession.ALegID),
				SessionID:   strings.TrimSpace(work.Session.ClientSessionID),
				PrincipalID: strings.TrimSpace(principal.ID),
			}
			coretraffic.PortBundleFromSnapshot(e.RuntimeSnapshot).Emit(
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
		catalogMeta := toolcatalog.CatalogMeta{
			TraceID:     traceID,
			Annotations: ann,
			Principal:   principal,
			Session:     preSession,
			Workspace:   wsView,
		}
		catSvc := toolcatalog.Services{State: e.RuntimeSnapshot.State(), Aux: e.RuntimeSnapshot.Aux()}
		if err := extensions.RunToolCatalogFilterStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			e.RuntimeSnapshot.ToolCatalogFilters(),
			&work,
			catalogMeta,
			catSvc,
		); err != nil {
			return "", lipapi.Call{}, b2bua.ALegRecord{}, outCtx, err
		}
		reqMeta := request.RequestMeta{
			TraceID:     traceID,
			Annotations: maps.Clone(submitMeta.Annotations),
			Principal:   principal,
			Session:     preSession,
			Workspace:   wsView,
		}
		if reqMeta.Annotations == nil {
			reqMeta.Annotations = make(map[string]string, len(submitMeta.Annotations))
		}
		reqSvc := request.Services{State: e.RuntimeSnapshot.State(), Aux: e.RuntimeSnapshot.Aux()}
		if err := extensions.RunRequestTransformStage(
			outCtx,
			e.Log,
			e.ExtensionMetrics,
			e.RuntimeSnapshot.RequestTransforms(),
			&work,
			reqMeta,
			reqSvc,
		); err != nil {
			return "", lipapi.Call{}, b2bua.ALegRecord{}, outCtx, err
		}
		hintIn := routehint.Input{
			TraceID:   traceID,
			Call:      &work,
			Principal: principal,
			Session:   preSession,
			Workspace: wsView,
		}
		prefs, err := extensions.RunRouteHintStage(
			outCtx,
			e.Log,
			e.RuntimeSnapshot.RouteHintProviders(),
			&work,
			hintIn,
		)
		if err != nil {
			return "", lipapi.Call{}, b2bua.ALegRecord{}, outCtx, err
		}
		outCtx = execctx.WithRouteCandidatePreferences(outCtx, prefs)
	}
	baseline = lipapi.CloneCall(work)
	aLeg, err = continuity.ResolveALegRecord(outCtx, e.Store, baseline.Session)
	if err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, outCtx, err
	}
	call.Session.ALegID = aLeg.ALegID
	outCtx = diag.EnsureCallDiag(outCtx, traceID, aLeg.ALegID)
	views := execctx.ViewsFromSubmit(traceID, aLeg, baseline, submitMeta.Annotations)
	if hasPrincipal {
		views.Principal = principal
	}
	views.Workspace = wsView
	for k, v := range preSession.Labels {
		if views.Session.Labels == nil {
			views.Session.Labels = make(map[string]string, len(preSession.Labels))
		}
		views.Session.Labels[k] = v
	}
	outCtx = execctx.WithViews(outCtx, views)
	return traceID, baseline, aLeg, outCtx, nil
}

func aLegRecordIsNew(a b2bua.ALegRecord) bool {
	if a.CreatedAt.IsZero() || a.LastSeenAt.IsZero() {
		return false
	}
	return a.CreatedAt.Equal(a.LastSeenAt)
}
