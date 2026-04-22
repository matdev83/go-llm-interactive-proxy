package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// prepareSubmitAndALeg runs submit hooks, assigns trace id, clones baseline, and resolves the A-leg row.
func (e *Executor) prepareSubmitAndALeg(ctx context.Context, bus *hooks.Bus, call *lipapi.Call) (traceID string, baseline lipapi.Call, aLeg b2bua.ALegRecord, outCtx context.Context, err error) {
	work := *call
	traceID = strings.TrimSpace(work.ID)
	if traceID == "" {
		traceID = diag.StableCallID(&work)
	}
	work.ID = traceID
	call.ID = traceID
	outCtx = diag.WithCallDiag(ctx, traceID, "")
	if err := bus.RunSubmit(outCtx, &work, &sdk.SubmitMeta{TraceID: traceID}); err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, ctx, err
	}
	baseline = lipapi.CloneCall(work)
	aLeg, err = continuity.ResolveALegRecord(outCtx, e.Store, baseline.Session)
	if err != nil {
		return "", lipapi.Call{}, b2bua.ALegRecord{}, ctx, err
	}
	// Expose resolved A-leg on the live call so frontends can attach [diag.EnsureCallDiag] once
	// to the request context and avoid per-Recv context.Value allocation on the streaming path.
	call.Session.ALegID = aLeg.ALegID
	outCtx = diag.EnsureCallDiag(outCtx, traceID, aLeg.ALegID)
	return traceID, baseline, aLeg, outCtx, nil
}
