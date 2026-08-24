package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// prepareSubmitAndALegDetached prepares one trusted internal auxiliary call.
// It shares the normal submit/request-authority seams, but intentionally does
// not enter secure-session BeginTurn: the child owns a private B2BUA A-leg,
// while the parent session and A-leg remain lineage-only metadata.
func (e *Executor) prepareSubmitAndALegDetached(
	ctx context.Context,
	bus *corehooks.Bus,
	call *lipapi.Call,
) (ibt *identityBoundTurn, workingCall *lipapi.Call, outCtx context.Context, err error) {
	if e == nil || e.Store == nil || call == nil {
		return nil, nil, ctx, fmt.Errorf("executor: invalid detached arguments")
	}
	work := lipapi.CloneCall(*call)
	traceID := strings.TrimSpace(work.ID)
	if traceID == "" {
		traceID = diag.StableCallID(&work)
	}
	work.ID, call.ID, outCtx = traceID, traceID, ctx

	var principal execview.PrincipalView
	var reqScope scope.PrincipalScopeView
	hasPrincipal := false
	if s, p, ok := e.resolveRequestScope(ctx); ok {
		reqScope, principal, hasPrincipal, outCtx = s, p, true, scope.WithScope(execview.WithPrincipal(outCtx, p), s)
	}
	// Route-hint preferences are request-local advisory authority. Do not let
	// a parent primary snapshot reorder the explicitly selected extractor route.
	outCtx = execctx.WithoutRouteCandidatePreferences(outCtx)
	outCtx = diag.WithCallDiag(outCtx, traceID, "")

	// Create a fresh, unkeyed A-leg for detached children inheriting parent A-leg,
	// or reuse existing ALeg if explicitly targeted.
	// A detached child must never replace or resolve the parent continuity key,
	// and route overrides are intentionally not read for this private leg.
	parentMeta, _ := execctx.DetachedSessionFromContext(outCtx)
	var aLeg b2bua.ALegRecord
	if work.Session.ALegID != "" && work.Session.ALegID != parentMeta.ParentALegID {
		var ferr error
		aLeg, ferr = e.Store.FetchALeg(outCtx, work.Session.ALegID)
		if ferr != nil {
			aLeg, err = e.Store.CreateALeg(outCtx, "")
			if err != nil {
				return nil, nil, outCtx, fmt.Errorf("executor: create detached a-leg: %w", err)
			}
		}
	} else {
		aLeg, err = e.Store.CreateALeg(outCtx, "")
		if err != nil {
			return nil, nil, outCtx, fmt.Errorf("executor: create detached a-leg: %w", err)
		}
	}
	work.Session = lipapi.SessionRef{ALegID: aLeg.ALegID}

	preSession := session.SessionView{
		ALegID:         aLeg.ALegID,
		IsNew:          false,
		ResumeEligible: false,
	}

	ibt, err = newIdentityBoundTurn(traceID, &work, principal, reqScope, hasPrincipal, lipworkspace.WorkspaceView{}, aLeg, routeAuthoritySnapshot{}, execctx.SecureSessionTurn{}, false, preSession)
	if err != nil {
		return nil, nil, outCtx, fmt.Errorf("executor: create identity bound turn: %w", err)
	}
	workingCall = &work

	if e.Log != nil {
		outCtx = corehooks.WithDiagnosticsLogger(outCtx, e.Log)
	}

	// Keep the normal request-ingress and authority boundaries. The detached
	// marker only changes session lifecycle/routing authority; it is not a
	// bypass around billing, usage, hooks, or B-leg execution.
	if outCtx, _, err = captureFrontendIngressBeforeSubmit(outCtx, *workingCall, ibt.scope, e.now()); err != nil {
		return nil, nil, outCtx, err
	}
	if outCtx, err = e.admitRequestAuthorityOnce(outCtx, workingCall.ID, ibt.aLeg.ALegID, ibt.traceID, ibt.scope); err != nil {
		return nil, nil, outCtx, err
	}
	submitMeta := &sdkhooks.SubmitMeta{TraceID: ibt.traceID, Annotations: map[string]string{}}
	if err := bus.RunSubmit(outCtx, workingCall, submitMeta); err != nil {
		_ = e.releaseRequestAuthority(outCtx)
		return nil, nil, outCtx, err
	}
	// --- Task 3.2 seam: snapshot once after A-leg resolution ---
	// Preserve ingress then project exclusion+steering before backend work;
	// fail closed on snapshot/projection errors.
	ingressClone := lipapi.CloneCall(*workingCall)
	ibt.ingressCall = &ingressClone
	backendClone := lipapi.CloneCall(*workingCall)
	originalForFilter := lipapi.CloneCall(backendClone)
	snapView, projEv, projected, perr := e.snapshotAndProject(outCtx, ibt.aLeg.ALegID, backendClone)
	if perr != nil {
		_ = e.releaseRequestAuthority(outCtx)
		return nil, nil, outCtx, perr
	}
	ibt.conversationSnapshot = snapView
	ibt.conversationEvidence = projEv
	ibt.conversationSummary = newConversationProjectionSummary(snapView, projEv)
	ibt.convSnapshotSet = true
	if filtered, ferr := conversationview.FilterNeverBackend(originalForFilter, snapView); ferr == nil {
		ibt.conversationFilteredBaseline = &filtered
	} else {
		_ = e.releaseRequestAuthority(outCtx)
		return nil, nil, outCtx, ferr
	}
	backendClone = projected
	workingCall = &backendClone
	// Submit hooks may enrich canonical calls, but cannot turn detached
	workingCall.Session.AuthoritativeSessionID, workingCall.Session.ClientSessionID = "", ""
	workingCall.Session.ALegID = ibt.aLeg.ALegID
	workingCall.Session.ContinuityKey, workingCall.Session.ResumeToken, workingCall.Session.Metadata = "", "", nil

	*workingCall = lipapi.CloneCall(*workingCall)
	call.Session = workingCall.Session
	// Ensure backend is distinct from preserved ingress (deep clone isolation).
	if ibt.ingressCall != nil && ibt.ingressCall == workingCall {
		bk := lipapi.CloneCall(*ibt.ingressCall)
		workingCall = &bk
	}
	outCtx = diag.EnsureCallDiag(outCtx, ibt.traceID, ibt.aLeg.ALegID)
	outCtx = execctx.WithViews(outCtx, execctx.Views{
		Principal: ibt.principal,
		Scope:     ibt.scope,
		Session:   ibt.preSession,
		Attempt:   execview.AttemptView{TraceID: ibt.traceID},
		Annotations: map[string]string{
			"execution_mode": "detached",
		},
	})
	return ibt, workingCall, outCtx, nil
}
