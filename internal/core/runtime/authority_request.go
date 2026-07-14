package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type requestAuthorityKey struct{}

// requestAuthorityState holds the once-per-request coordinator result for settle/release.
type requestAuthorityState struct {
	Decision  authoritycoord.CompositeDecision
	RequestID string
	Settled   bool
	Released  bool
}

func withRequestAuthority(ctx context.Context, st *requestAuthorityState) context.Context {
	if st == nil {
		return ctx
	}
	return context.WithValue(ctx, requestAuthorityKey{}, st)
}

func requestAuthorityFrom(ctx context.Context) *requestAuthorityState {
	if ctx == nil {
		return nil
	}
	st, _ := ctx.Value(requestAuthorityKey{}).(*requestAuthorityState)
	return st
}

// admitRequestAuthorityOnce runs the logical-request coordinator after FE ingress
// (requirements 4.5, 8.1, 9.3). Nil coordinator is a no-op.
func (e *Executor) admitRequestAuthorityOnce(ctx context.Context, requestID, aLegID, traceID string, sc scope.PrincipalScopeView) (context.Context, error) {
	if e == nil || e.RequestCoordinator == nil {
		return ctx, nil
	}
	if requestAuthorityFrom(ctx) != nil {
		return ctx, nil // already admitted
	}
	in := authority.RequestAdmission{
		RequestID:      strings.TrimSpace(requestID),
		ALegID:         strings.TrimSpace(aLegID),
		TraceID:        strings.TrimSpace(traceID),
		Perspective:    metering.PerspectiveCustomer,
		Lifecycle:      metering.LifecycleLogicalRequest,
		Scope:          sc,
		IdempotencyKey: "req:" + strings.TrimSpace(requestID),
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   metering.LifecycleLogicalRequest,
		},
	}
	if holder := meteringHolderFrom(ctx); holder != nil && holder.FrontendIngress != nil {
		in.Exposure.Quantities = append([]metering.Quantity(nil), holder.FrontendIngress.Public.Quantities...)
	}
	d, err := e.RequestCoordinator.Admit(ctx, in)
	if err != nil {
		return ctx, fmt.Errorf("executor: request authority: %w", err)
	}
	st := &requestAuthorityState{Decision: d, RequestID: in.RequestID}
	return withRequestAuthority(ctx, st), nil
}

func (e *Executor) settleRequestAuthority(ctx context.Context, facts []metering.Fact) {
	if e == nil || e.RequestCoordinator == nil {
		return
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Settled || st.Released {
		return
	}
	_ = e.RequestCoordinator.Settle(ctx, authority.RequestSettlement{
		RequestID: st.RequestID,
		Handles:   st.Decision.Stack.Handles(),
		Facts:     facts,
	})
	st.Settled = true
}

func (e *Executor) releaseRequestAuthority(ctx context.Context) {
	if e == nil || e.RequestCoordinator == nil {
		return
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Settled || st.Released {
		return
	}
	_ = e.RequestCoordinator.Release(ctx, st.Decision.Stack, st.RequestID)
	st.Released = true
}
