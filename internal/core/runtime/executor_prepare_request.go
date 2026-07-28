package runtime

import (
	"context"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// preparedRequest holds the result of [Executor.prepareRequest] — all state assembled
// during Execute phases 1-9 (validation, snapshot binding, secure-session, tracing,
// submit/A-leg, lifecycle, exec views, secure turn, route prefs).
type preparedRequest struct {
	bus            *hooks.Bus
	traceID        string
	baseline       lipapi.Call
	aLeg           b2bua.ALegRecord
	aScope         *leglifecycle.ALeg
	recvViews      execctx.Views
	recvViewsOK    bool
	secureTurn     execctx.SecureSessionTurn
	secureTurnOK   bool
	routePrefs     []string
	streamReturned bool
	execSpan       trace.Span
	metering       *checkpoint.RequestHolder
}

// prepareRequest executes phases 1-9 of the former inline [Executor.Execute]:
// validation, runtime snapshot binding, secure-session readiness, tracing span,
// submit/A-leg preparation, A-leg lifecycle start, exec-view extraction, secure-turn
// extraction, and route-preference clone. It returns a [preparedRequest] holding all
// state the downstream phases need, plus a cleanup function the caller must defer.
//
// Error wrapping is identical to the former inline code. The returned context is
// the prepared request context (execute span + submit enrichments) the caller
// threads into downstream phases.
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
	e.secureSessionMu.Lock()
	if e.SecureSession == nil {
		secureSessionTestPrepare(e)
	}
	secureSessionReady := e.SecureSession != nil
	e.secureSessionMu.Unlock()
	if !secureSessionReady {
		return nil, nil, noop, fmt.Errorf("executor: secure session manager is required")
	}

	prep := &preparedRequest{bus: bus}
	var err error
	prepCtx, execSpan := otel.Tracer(otelScopeExecutor).Start(ctx, "lip.executor.execute")
	prep.execSpan = execSpan

	prep.traceID, prep.baseline, prep.aLeg, prepCtx, err = e.prepareSubmitAndALeg(prepCtx, bus, call)
	if err != nil {
		// Route through finalize so the lip.executor.execute span records the
		// prepare-submit failure (RecordError + SetStatus) before ending, matching
		// the former inline Execute defer. Execute does not call finalize when
		// prepareRequest returns an error (prep is nil), so the span ends here.
		prep.finalize(err)
		return nil, nil, noop, fmt.Errorf("executor: prepare submit: %w", err)
	}
	prep.metering = meteringHolderFrom(prepCtx)

	lifecycle := e.lifecycleCoordinator()
	prep.aScope = lifecycle.StartALeg(prep.aLeg.ALegID)

	cleanup := func() {
		if prep.streamReturned {
			return
		}
		// Release logical-request concurrency occupancy on post-admit prepare/
		// route/open failures before a stream is returned (requirement 10.5).
		_ = e.releaseRequestAuthority(prepCtx)
		if prep.aScope == nil {
			return
		}
		cleanupCtx, cleanupCancel := detachedCleanupContext(prepCtx, cancelLosersTimeout)
		defer cleanupCancel()
		_ = prep.aScope.Cancel(cleanupCtx, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
		prep.aScope.End()
	}

	if v, ok := execctx.FromContext(prepCtx); ok {
		prep.recvViews = v
		prep.recvViewsOK = true
	}
	if st, ok := execctx.SecureSessionTurnFromContext(prepCtx); ok {
		prep.secureTurn = st
		prep.secureTurnOK = true
	}
	prep.routePrefs = slices.Clone(execctx.RouteCandidatePreferences(prepCtx))

	return prep, prepCtx, cleanup, nil
}

// finalize ends the tracing span and records any error. The caller's
// deferred func in Execute calls this.
func (p *preparedRequest) finalize(err error) {
	if err != nil {
		p.execSpan.RecordError(err)
		p.execSpan.SetStatus(codes.Error, err.Error())
	}
	p.execSpan.End()
}
