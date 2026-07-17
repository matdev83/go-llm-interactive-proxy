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
	LeaseID         string // primary lease (backward compat / aux parent)
	LeaseIDs        []string
	LeaseTargets    []leaseRenewTarget
	LeaseGeneration int64
	LeaseExpiresAt  time.Time
	RenewBefore     time.Duration
	LeaseTTL        time.Duration
	FailureBehavior authority.FailureBehavior
	heartbeat       *leaseHeartbeat
}

// leaseRenewTarget is one occupancy the heartbeat renews until settle/release.
type leaseRenewTarget struct {
	LeaseID         string
	Generation      int64
	ExpiresAt       time.Time
	RenewBefore     time.Duration
	TTL             time.Duration
	RuleID          string
	FailureBehavior authority.FailureBehavior
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
	if err := e.enrichFrontendIngressQuantities(ctx); err != nil {
		return ctx, fmt.Errorf("executor: frontend ingress counting: %w", err)
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
	if money, rated, rateErr := e.rateCustomerRequestExposure(ctx, in.Exposure.Quantities, e.now()); rateErr != nil {
		return ctx, fmt.Errorf("executor: request authority rating: %w", rateErr)
	} else if money.Present {
		in.Exposure.Money = money
		in.RatingVersions = []economics.RatingSnapshotRef{ratingSnapshotRef(rated)}
	}
	d, err := e.RequestCoordinator.Admit(ctx, in)
	if err != nil {
		return ctx, mapRequestAuthorityError(err)
	}
	e.mergeGenerationBoundVersions(&d)
	targets := leaseTargetsFromDecision(d.Lease)
	leaseIDs := make([]string, 0, len(targets))
	for _, t := range targets {
		leaseIDs = append(leaseIDs, t.LeaseID)
	}
	primaryID := d.Lease.LeaseID
	primaryGen := d.Lease.Generation
	primaryExp := d.Lease.ExpiresAt
	if primaryID == "" && len(targets) > 0 {
		primaryID = targets[0].LeaseID
		primaryGen = targets[0].Generation
		primaryExp = targets[0].ExpiresAt
	}
	st := &requestAuthorityState{
		Decision:        d,
		RequestID:       in.RequestID,
		LeaseID:         primaryID,
		LeaseIDs:        leaseIDs,
		LeaseTargets:    targets,
		LeaseGeneration: primaryGen,
		LeaseExpiresAt:  primaryExp,
		RenewBefore:     d.Lease.RenewBefore,
		LeaseTTL:        d.Lease.TTL,
		FailureBehavior: d.Lease.FailureBehavior,
	}
	outCtx := withRequestAuthority(ctx, st)
	e.startLeaseHeartbeat(outCtx, st)
	return outCtx, nil
}

func leaseTargetsFromDecision(ld authority.LeaseDecision) []leaseRenewTarget {
	if len(ld.Leases) > 0 {
		out := make([]leaseRenewTarget, 0, len(ld.Leases))
		seen := make(map[string]struct{}, len(ld.Leases))
		for _, occ := range ld.Leases {
			id := strings.TrimSpace(occ.LeaseID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			rb := occ.RenewBefore
			if rb <= 0 {
				rb = ld.RenewBefore
			}
			ttl := occ.TTL
			if ttl <= 0 {
				ttl = ld.TTL
			}
			fb := occ.FailureBehavior
			if fb == "" {
				fb = ld.FailureBehavior
			}
			exp := occ.ExpiresAt
			if exp.IsZero() {
				exp = ld.ExpiresAt
			}
			out = append(out, leaseRenewTarget{
				LeaseID:         id,
				Generation:      occ.Generation,
				ExpiresAt:       exp,
				RenewBefore:     rb,
				TTL:             ttl,
				RuleID:          strings.TrimSpace(occ.RuleID),
				FailureBehavior: fb,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	if id := strings.TrimSpace(ld.LeaseID); id != "" {
		return []leaseRenewTarget{{
			LeaseID:         id,
			Generation:      ld.Generation,
			ExpiresAt:       ld.ExpiresAt,
			RenewBefore:     ld.RenewBefore,
			TTL:             ld.TTL,
			FailureBehavior: ld.FailureBehavior,
		}}
	}
	return nil
}

func (e *Executor) settleRequestAuthority(ctx context.Context, facts []metering.Fact, rated ...economics.RatingResult) {
	if e == nil || e.RequestCoordinator == nil {
		return
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Settled || st.Released {
		return
	}
	err := e.RequestCoordinator.Settle(ctx, st.Decision.Stack, authority.RequestSettlement{
		RequestID:     st.RequestID,
		Handles:       st.Decision.Stack.Handles(),
		Facts:         facts,
		Rated:         append([]economics.RatingResult(nil), rated...),
		BoundVersions: st.Decision.BoundVersions,
	})
	if err != nil {
		// Post-output settlement failure must retain reservation evidence and stay
		// retryable; do not mark settled/released or release the lease (15.5).
		return
	}
	// RequestCoordinator.Settle does not release concurrency occupancy (10.5).
	e.stopLeaseHeartbeat(st)
	ids := st.LeaseIDs
	if len(ids) == 0 && st.LeaseID != "" {
		ids = []string{st.LeaseID}
	}
	_ = e.RequestCoordinator.ReleaseLeases(ctx, ids, st.RequestID, "settled")
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
