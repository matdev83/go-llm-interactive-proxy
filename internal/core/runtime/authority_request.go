package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type requestAuthorityKey struct{}

// requestAuthorityState holds the once-per-request coordinator result for settle/release.
type requestAuthorityState struct {
	Decision        authoritycoord.CompositeDecision
	RequestID       string
	Settled         bool
	Released        bool
	LeaseID         string
	LeaseGeneration int64
	LeaseExpiresAt  time.Time
	RenewBefore     time.Duration
	LeaseTTL        time.Duration
	heartbeat       *leaseHeartbeat
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
// (requirements 4.5, 8.1, 9.3, 10.4). Nil coordinator is a no-op.
func (e *Executor) admitRequestAuthorityOnce(ctx context.Context, requestID, aLegID, traceID string, sc scope.PrincipalScopeView) (context.Context, error) {
	if e == nil || e.RequestCoordinator == nil {
		return ctx, nil
	}
	if requestAuthorityFrom(ctx) != nil {
		// Auxiliary Execute reuses the parent context value: inherit occupancy
		// without consuming an additional top-level lease (requirement 10.10).
		return ctx, nil
	}
	lifecycle := metering.LifecycleLogicalRequest
	parentLeaseID := ""
	auxPolicy := ""
	if execctx.AuxiliaryDepth(ctx) > 0 {
		lifecycle = metering.LifecycleAuxiliaryRequest
		// No parent lease in context: AdmitInput default AuxPolicy inherits and
		// returns allow without a new slot when ParentLeaseID is empty.
	}
	in := authority.RequestAdmission{
		RequestID:      strings.TrimSpace(requestID),
		ALegID:         strings.TrimSpace(aLegID),
		TraceID:        strings.TrimSpace(traceID),
		Perspective:    metering.PerspectiveCustomer,
		Lifecycle:      lifecycle,
		Scope:          sc,
		IdempotencyKey: "req:" + strings.TrimSpace(requestID),
		ParentLeaseID:  parentLeaseID,
		AuxPolicy:      auxPolicy,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   lifecycle,
		},
	}
	if holder := meteringHolderFrom(ctx); holder != nil && holder.FrontendIngress != nil {
		in.Exposure.Quantities = append([]metering.Quantity(nil), holder.FrontendIngress.Public.Quantities...)
	}
	d, err := e.RequestCoordinator.Admit(ctx, in)
	if err != nil {
		return ctx, fmt.Errorf("executor: request authority: %w", err)
	}
	st := &requestAuthorityState{
		Decision:        d,
		RequestID:       in.RequestID,
		LeaseID:         d.Lease.LeaseID,
		LeaseGeneration: d.Lease.Generation,
		LeaseExpiresAt:  d.Lease.ExpiresAt,
		RenewBefore:     d.Lease.RenewBefore,
		LeaseTTL:        d.Lease.TTL,
	}
	outCtx := withRequestAuthority(ctx, st)
	e.startLeaseHeartbeat(outCtx, st)
	return outCtx, nil
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
	// RequestCoordinator.Settle does not release concurrency occupancy (10.5).
	e.stopLeaseHeartbeat(st)
	_ = e.RequestCoordinator.ReleaseLease(ctx, st.LeaseID, st.RequestID, "settled")
	st.Settled = true
	st.Released = true
}

func (e *Executor) releaseRequestAuthority(ctx context.Context) {
	if e == nil || e.RequestCoordinator == nil {
		return
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Settled || st.Released {
		return
	}
	e.stopLeaseHeartbeat(st)
	_ = e.RequestCoordinator.Release(ctx, st.Decision.Stack, st.RequestID)
	st.Released = true
}
