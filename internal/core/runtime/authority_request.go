package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
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
	FailureBehavior authority.FailureBehavior
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
	lifecycle := metering.LifecycleLogicalRequest
	parentLeaseID := ""
	auxPolicy := ""
	if parent := requestAuthorityFrom(ctx); parent != nil {
		policy := strings.ToLower(strings.TrimSpace(e.ConcurrencyAuxiliaryLeasePolicy))
		if policy == "" || policy == "inherit" || execctx.AuxiliaryDepth(ctx) == 0 {
			// Default: auxiliary Execute reuses parent occupancy (requirement 10.10).
			return ctx, nil
		}
		// acquire_own: continue into Admit with auxiliary lifecycle below.
		lifecycle = metering.LifecycleAuxiliaryRequest
		parentLeaseID = parent.LeaseID
		auxPolicy = "acquire_own"
	} else if execctx.AuxiliaryDepth(ctx) > 0 {
		lifecycle = metering.LifecycleAuxiliaryRequest
		policy := strings.ToLower(strings.TrimSpace(e.ConcurrencyAuxiliaryLeasePolicy))
		if policy == "acquire_own" {
			auxPolicy = "acquire_own"
		}
	}
	admitRequestID := strings.TrimSpace(requestID)
	idempotencyKey := "req:" + admitRequestID
	if auxPolicy == "acquire_own" {
		// Distinct logical identity so acquire_own does not replay the parent lease.
		suffix := strings.TrimSpace(aLegID)
		if suffix == "" {
			suffix = "aux"
		}
		admitRequestID = admitRequestID + ":aux:" + suffix
		idempotencyKey = "req-aux:" + admitRequestID
	}
	in := authority.RequestAdmission{
		RequestID:      admitRequestID,
		ALegID:         strings.TrimSpace(aLegID),
		TraceID:        strings.TrimSpace(traceID),
		Perspective:    metering.PerspectiveCustomer,
		Lifecycle:      lifecycle,
		Scope:          sc,
		IdempotencyKey: idempotencyKey,
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
		return ctx, mapRequestAuthorityError(err)
	}
	e.mergeGenerationBoundVersions(&d)
	st := &requestAuthorityState{
		Decision:        d,
		RequestID:       in.RequestID,
		LeaseID:         d.Lease.LeaseID,
		LeaseGeneration: d.Lease.Generation,
		LeaseExpiresAt:  d.Lease.ExpiresAt,
		RenewBefore:     d.Lease.RenewBefore,
		LeaseTTL:        d.Lease.TTL,
		FailureBehavior: d.Lease.FailureBehavior,
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
		RequestID:     st.RequestID,
		Handles:       st.Decision.Stack.Handles(),
		Facts:         facts,
		BoundVersions: st.Decision.BoundVersions,
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

// mapRequestAuthorityError converts coordinator denials into client-safe policy
// errors. Concurrency denials use the stable concurrency_limit category and must
// not include internal lease IDs (requirements 10.11, 14.3).
func mapRequestAuthorityError(err error) error {
	var denied *authoritycoord.ErrDenied
	if errors.As(err, &denied) && denied != nil && denied.ProviderID == "concurrency" {
		return lipapi.NewPolicyDeniedError(
			"request_authority",
			"",
			"concurrency_limit",
			"concurrency_limit",
			"active request limit reached",
			nil,
		)
	}
	var unavail *authoritycoord.ErrUnavailable
	if errors.As(err, &unavail) && unavail != nil && unavail.ProviderID == "concurrency" {
		return lipapi.NewPolicyFailureError(
			"request_authority",
			"",
			"concurrency_unavailable",
			"concurrency_limit",
			"concurrency authority unavailable",
			nil,
		)
	}
	return fmt.Errorf("executor: request authority: %w", err)
}
