package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

// prepareSubmitAndALegDetached prepares one trusted internal auxiliary call.
// It shares the normal submit/request-authority seams, but intentionally does
// not enter secure-session BeginTurn: the child owns a private B2BUA A-leg,
// while the parent session and A-leg remain lineage-only metadata.
func (e *Executor) prepareSubmitAndALegDetached(
	ctx context.Context,
	bus *corehooks.Bus,
	call *lipapi.Call,
) (traceID string, baseline lipapi.Call, aLeg b2bua.ALegRecord, routeAuth routeAuthoritySnapshot, outCtx context.Context, err error) {
	if e == nil || e.Store == nil || call == nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, ctx, fmt.Errorf("executor: invalid detached arguments")
	}
	work := lipapi.CloneCall(*call)
	traceID = strings.TrimSpace(work.ID)
	if traceID == "" {
		traceID = diag.StableCallID(&work)
	}
	work.ID = traceID
	call.ID = traceID

	outCtx = ctx
	var principal execview.PrincipalView
	var reqScope scope.PrincipalScopeView
	if s, p, ok := e.resolveRequestScope(ctx); ok {
		reqScope, principal = s, p
		outCtx = execview.WithPrincipal(outCtx, p)
		outCtx = scope.WithScope(outCtx, s)
	}
	// Route-hint preferences are request-local advisory authority. Do not let
	// a parent primary snapshot reorder the explicitly selected extractor route.
	outCtx = execctx.WithoutRouteCandidatePreferences(outCtx)
	outCtx = diag.WithCallDiag(outCtx, traceID, "")

	// Create a fresh, unkeyed A-leg. A detached child must never replace or
	// resolve the parent continuity key, and route overrides are intentionally
	// not read for this private leg.
	aLeg, err = e.Store.CreateALeg(outCtx, "")
	if err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, fmt.Errorf("executor: create detached a-leg: %w", err)
	}
	// Parent session/client/resume values remain available only through the
	// trusted detached metadata. The canonical child carries only its private
	// execution A-leg, avoiding session affinity or client-header authority.
	work.Session.AuthoritativeSessionID = ""
	work.Session.ClientSessionID = ""
	work.Session.ALegID = aLeg.ALegID
	work.Session.ContinuityKey = ""
	work.Session.ResumeToken = ""
	work.Session.Metadata = nil

	preSession := session.SessionView{
		ALegID:         aLeg.ALegID,
		IsNew:          false,
		ResumeEligible: false,
	}
	if e.Log != nil {
		outCtx = corehooks.WithDiagnosticsLogger(outCtx, e.Log)
	}

	// Keep the normal request-ingress and authority boundaries. The detached
	// marker only changes session lifecycle/routing authority; it is not a
	// bypass around billing, usage, hooks, or B-leg execution.
	if outCtx, _, err = captureFrontendIngressBeforeSubmit(outCtx, work, reqScope, e.now()); err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, err
	}
	if outCtx, err = e.admitRequestAuthorityOnce(outCtx, work.ID, aLeg.ALegID, traceID, reqScope); err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, err
	}
	admitted := true
	failAfterAdmission := func(in error) (string, lipapi.Call, b2bua.ALegRecord, routeAuthoritySnapshot, context.Context, error) {
		if admitted {
			_ = e.releaseRequestAuthority(outCtx)
		}
		return "", lipapi.Call{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, outCtx, in
	}

	submitMeta := &sdkhooks.SubmitMeta{TraceID: traceID, Annotations: map[string]string{}}
	if err := bus.RunSubmit(outCtx, &work, submitMeta); err != nil {
		return failAfterAdmission(err)
	}
	// Submit hooks may enrich canonical calls, but cannot turn detached
	// lineage hints back into session or continuity authority.
	work.Session.AuthoritativeSessionID = ""
	work.Session.ClientSessionID = ""
	work.Session.ALegID = aLeg.ALegID
	work.Session.ContinuityKey = ""
	work.Session.ResumeToken = ""
	work.Session.Metadata = nil

	baseline = lipapi.CloneCall(work)
	call.Session = work.Session
	outCtx = diag.EnsureCallDiag(outCtx, traceID, aLeg.ALegID)
	outCtx = execctx.WithViews(outCtx, execctx.Views{
		Principal: principal,
		Scope:     reqScope,
		Session:   preSession,
		Attempt:   execview.AttemptView{TraceID: traceID},
		Annotations: map[string]string{
			"execution_mode": "detached",
		},
	})
	admitted = false // preparedRequest owns request-authority cleanup after return.
	return traceID, baseline, aLeg, routeAuth, outCtx, nil
}
