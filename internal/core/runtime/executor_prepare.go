package runtime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func (e *Executor) prepareSubmitAndALeg(ctx context.Context, bus *hooks.Bus, call *lipapi.Call) (traceID string, baseline lipapi.Call, aLeg b2bua.ALegRecord, routeAuth routeAuthoritySnapshot, outCtx context.Context, err error) {
	ibt, workingCall, outCtx, err := e.prepareIdentity(ctx, bus, call)
	if err == nil {
		traceID, baseline, aLeg, routeAuth = ibt.traceID, *workingCall, ibt.aLeg, ibt.routeAuth
	}
	return
}

func (e *Executor) prepareIdentity(ctx context.Context, bus *hooks.Bus, call *lipapi.Call) (ibt *identityBoundTurn, workingCall *lipapi.Call, outCtx context.Context, err error) {
	if e != nil {
		if mode, marked := execctx.SessionModeFromContext(ctx); marked && mode == execctx.SessionModeDetached {
			return e.prepareSubmitAndALegDetached(ctx, bus, call)
		}
	}
	if e == nil || e.SecureSession == nil {
		return nil, nil, ctx, fmt.Errorf("executor: secure session manager is required")
	}
	return e.prepareSubmitAndALegSecure(ctx, bus, call)
}
