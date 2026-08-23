package runtime

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type preparedRequest struct {
	recvTurnFacts
	bus                *hooks.Bus
	identity           *identityBoundTurn
	call               *lipapi.Call
	guard              *preStreamGuard
	aScope             *leglifecycle.ALeg
	billingCallID      billing.BillingCallID
	billingCallState   *billingCallState
	billingExposure    billing.CallExposure
	execSpan           trace.Span
	compactionOpenMeta compaction.PreservationMeta
}

func (prep *preparedRequest) ensureRecvTurnFacts(ctx context.Context) {
	if prep == nil {
		return
	}
	if prep.recvTurnFacts.billingCallID == "" && prep.billingCallID != "" {
		prep.recvTurnFacts.billingCallID = prep.billingCallID
		prep.recvTurnFacts.billingCallState = prep.billingCallState
	}
	if prep.aLegID == "" && prep.identity != nil {
		prep.recvTurnFacts = newRecvTurnFacts(ctx, recvTurnFactsInput{
			baseline:         *prep.call,
			traceID:          prep.identity.traceID,
			aLegID:           prep.identity.aLeg.ALegID,
			secureTurn:       prep.identity.secureTurn,
			secureTurnOK:     prep.identity.secureTurnOK,
			billingCallID:    prep.billingCallID,
			billingCallState: prep.billingCallState,
		})
	}
}

func (e *Executor) prepareRequest(ctx context.Context, call *lipapi.Call) (*preparedRequest, context.Context, func(), error) {
	noop := func() {}
	if e == nil || e.Store == nil || call == nil {
		return nil, nil, noop, fmt.Errorf("executor: invalid arguments")
	}
	if e.Bus == nil {
		return nil, nil, noop, fmt.Errorf("executor: nil hook bus")
	}
	bus := e.Bus
	if err := call.Validate(); err != nil {
		return nil, nil, noop, fmt.Errorf("executor: validate call: %w", err)
	}
	if ctx == nil {
		return nil, nil, noop, lipapi.ErrNilContext
	}
	if e.RuntimeSnapshot != nil {
		ctx = extensions.WithRequestRuntimeSnapshot(ctx, e.RuntimeSnapshot)
	}
	detached := false
	if mode, marked := execctx.SessionModeFromContext(ctx); marked && mode == execctx.SessionModeDetached {
		detached = true
	}
	if !detached {
		e.secureSessionMu.Lock()
		if e.SecureSession == nil {
			secureSessionTestPrepare(e)
		}
		secureSessionReady := e.SecureSession != nil
		e.secureSessionMu.Unlock()
		if !secureSessionReady {
			return nil, nil, noop, fmt.Errorf("executor: secure session manager is required")
		}
	}
	pr := &preparedRequest{bus: bus}
	var err error
	prepCtx, execSpan := otel.Tracer(otelScopeExecutor).Start(ctx, "lip.executor.execute")
	pr.execSpan = execSpan
	ibt, workingCall, prepCtx, err := e.prepareIdentity(prepCtx, bus, call)
	if err != nil {
		pr.finalize(err)
		return nil, nil, noop, fmt.Errorf("executor: prepare submit: %w", err)
	}
	prepCtx = ibt.projectContext(prepCtx)
	pr.identity = ibt
	pr.call = workingCall
	// Task 3.1 seam: ingress view is preserved in ibt.ingressCall (deep
	// clone taken after secure A-leg + secret/submit/CTP boundaries and
	// before inference-specific transforms). See conversation_view_seam.go.
	// No snapshot/project yet (3.2). Ensure isolation fallback if ingress
	// was not set (detached legacy or snap==nil edge).
	if pr.identity.ingressCall == nil {
		ing := lipapi.CloneCall(*workingCall)
		pr.identity.ingressCall = &ing
		bk := lipapi.CloneCall(*workingCall)
		pr.call = &bk
	} else if pr.identity.ingressCall == pr.call {
		bk := lipapi.CloneCall(*pr.call)
		pr.call = &bk
	}

	guard := &preStreamGuard{
		executor:                 e,
		ctx:                      prepCtx,
		requestAuthorityAdmitted: true,
	}
	pr.guard = guard

	if ctx.Value(testInjectBillingErrorKey{}) != nil {
		pr.billingCallID = "invalid"
	}

	if e.Keepwarm != nil {
		e.Keepwarm.BeginRealTurn(pr.identity.aLeg.ALegID)
	}
	if err := stampBillingCallID(pr); err != nil {
		guard.Close()
		pr.finalize(err)
		return nil, nil, noop, fmt.Errorf("executor: allocate billing call id: %w", err)
	}
	lifecycle := e.lifecycleCoordinator()
	pr.aScope = lifecycle.StartALeg(pr.identity.aLeg.ALegID)
	guard.aScope = pr.aScope

	var recvViews execctx.Views
	var recvViewsOK bool
	if v, ok := execctx.FromContext(prepCtx); ok {
		recvViews = v
		recvViewsOK = true
	}
	boundReg, boundRegOK := modelregistry.BoundViewFromContext(prepCtx)
	boundCat, boundCatOK := modelcatalog.BoundViewFromContext(prepCtx)
	nativeResolver, _ := routing.NativeModelResolverFromContext(prepCtx)
	modelViewID, modelViewIDOK := modelview.FromContext(prepCtx)
	pr.recvTurnFacts = newRecvTurnFacts(prepCtx, recvTurnFactsInput{
		baseline:               *workingCall,
		traceID:                ibt.traceID,
		aLegID:                 ibt.aLeg.ALegID,
		recvViews:              recvViews,
		recvViewsOK:            recvViewsOK,
		routePrefs:             slices.Clone(execctx.RouteCandidatePreferences(prepCtx)),
		secureTurn:             ibt.secureTurn,
		secureTurnOK:           ibt.secureTurnOK,
		boundRegistry:          boundReg,
		boundRegistryOK:        boundRegOK,
		boundCatalog:           boundCat,
		boundCatalogOK:         boundCatOK,
		nativeResolver:         nativeResolver,
		modelViewID:            modelViewID,
		modelViewIDOK:          modelViewIDOK,
		metering:               meteringHolderFrom(prepCtx),
		requestAuth:            requestAuthorityFrom(prepCtx),
		billingAccountID:       pr.billingExposure.AccountID,
		billingCustomerPricing: pr.billingExposure.PricingRef,
		billingChargePolicy:    pr.billingExposure.ChargePolicyRef,
		billingIdentityStamped: pr.billingIdentityStamped,
		billingCallID:          pr.billingCallID,
		billingCallState:       pr.billingCallState,
	})
	return pr, prepCtx, guard.Close, nil
}

func (p *preparedRequest) finalize(err error) {
	if err != nil {
		p.execSpan.RecordError(err)
		p.execSpan.SetStatus(codes.Error, err.Error())
	}
	p.execSpan.End()
}

type identityBoundTurn struct {
	traceID      string
	call         *lipapi.Call
	ingressCall  *lipapi.Call
	principal    execview.PrincipalView
	scope        scope.PrincipalScopeView
	hasPrincipal bool
	workspace    lipworkspace.WorkspaceView
	aLeg         b2bua.ALegRecord
	routeAuth    routeAuthoritySnapshot
	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool
	preSession   session.SessionView
}

func newIdentityBoundTurn(traceID string, call *lipapi.Call, principal execview.PrincipalView, scope scope.PrincipalScopeView, hasPrincipal bool, workspace lipworkspace.WorkspaceView, aLeg b2bua.ALegRecord, routeAuth routeAuthoritySnapshot, secureTurn execctx.SecureSessionTurn, secureTurnOK bool, preSession session.SessionView) (*identityBoundTurn, error) {
	if traceID == "" || call == nil || aLeg.ALegID == "" || call.Session.ALegID != aLeg.ALegID || preSession.ALegID != aLeg.ALegID {
		return nil, fmt.Errorf("invalid ibt args or A-leg ID mismatch")
	}
	if secureTurnOK {
		if secureTurn.SessionID == "" || secureTurn.TurnID == "" ||
			call.Session.AuthoritativeSessionID != string(secureTurn.SessionID) ||
			preSession.AuthoritativeSessionID != string(secureTurn.SessionID) ||
			preSession.TurnID != string(secureTurn.TurnID) ||
			preSession.WorkspaceID != workspace.ID {
			return nil, fmt.Errorf("invalid ibt args or secure turn mismatch")
		}
	} else {
		if secureTurn.SessionID != "" || secureTurn.TurnID != "" ||
			call.Session.AuthoritativeSessionID != "" ||
			preSession.AuthoritativeSessionID != "" ||
			preSession.TurnID != "" ||
			preSession.WorkspaceID != "" ||
			workspace.ID != "" {
			return nil, fmt.Errorf("invalid ibt args or non-empty detached session")
		}
	}
	cloned := lipapi.CloneCall(*call)
	return &identityBoundTurn{traceID: traceID, call: &cloned, principal: principal, scope: scope, hasPrincipal: hasPrincipal, workspace: workspace, aLeg: aLeg, routeAuth: routeAuth, secureTurn: secureTurn, secureTurnOK: secureTurnOK, preSession: preSession}, nil
}

func (t *identityBoundTurn) projectContext(ctx context.Context) context.Context {
	if t == nil {
		return ctx
	}
	if t.hasPrincipal {
		ctx = scope.WithScope(execview.WithPrincipal(ctx, t.principal), t.scope)
	}
	if t.secureTurnOK {
		ctx = execctx.WithSecureSessionTurn(ctx, t.secureTurn)
	}
	views, _ := execctx.FromContext(ctx)
	if t.hasPrincipal {
		views.Principal, views.Scope = t.principal, t.scope
	}
	views.Workspace = t.workspace
	labels := views.Session.Labels
	views.Session = t.preSession
	if len(t.preSession.Labels) > 0 {
		views.Session.Labels = maps.Clone(t.preSession.Labels)
	}
	if len(labels) > 0 {
		if views.Session.Labels == nil {
			views.Session.Labels = make(map[string]string)
		}
		maps.Copy(views.Session.Labels, labels)
	}
	return execctx.WithViews(ctx, views)
}

type preStreamGuard struct {
	mu                       sync.Mutex
	executor                 *Executor
	ctx                      context.Context
	aScope                   *leglifecycle.ALeg
	requestAuthorityAdmitted bool
	handedOver               bool
	closed                   bool
}

func (g *preStreamGuard) Handoff() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.handedOver = true
}

func (g *preStreamGuard) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closed || g.handedOver {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.mu.Unlock()

	if g.requestAuthorityAdmitted {
		_ = g.executor.releaseRequestAuthority(g.ctx)
	}
	if g.aScope != nil {
		cleanupCtx, cleanupCancel := detachedCleanupContext(g.ctx, cancelLosersTimeout)
		defer cleanupCancel()
		_ = g.aScope.Cancel(cleanupCtx, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
		g.aScope.End()
	}
}

type testInjectBillingErrorKey struct{}
