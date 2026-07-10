package runtime

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// authorityLifecycle owns a single usage-authority reservation's settle/release
// lifecycle and the settled idempotency guard. It is a consumption-site wrapper:
// open paths keep handing off attemptAuthorityState values, so it introduces no
// ripple into attemptOpenResult/attemptOpenParams signatures.
//
// Settle folds the losing-fallback release (previously hand-written at the
// finalizeResponseFinishedAuthority and settleCancellationAuthority call sites)
// into the owner, so every settle failure is followed by exactly one
// ReleaseKindLosing release and a settled mark. Phase 4b/4c will migrate the
// scattered settle/release call sites onto this owner; until then it is
// exercised only in isolation and the existing Executor methods stay intact.
type authorityLifecycle struct {
	svc     UsageAuthorityService
	log     *slog.Logger
	state   attemptAuthorityState
	cand    routing.AttemptCandidate
	settled atomic.Bool
}

// newAuthorityLifecycle builds an owner over the supplied authority service,
// logger, reservation state, and attempt candidate. The owner is inactive until
// IsActive reports true (correlated + reserved admission result).
func newAuthorityLifecycle(svc UsageAuthorityService, log *slog.Logger, state attemptAuthorityState, cand routing.AttemptCandidate) authorityLifecycle {
	return authorityLifecycle{svc: svc, log: log, state: state, cand: cand}
}

// IsActive reports whether this lifecycle owns a live reservation worth
// settling/releasing: a non-nil owner with a correlated admission input that
// was reserved by the authority service.
func (l *authorityLifecycle) IsActive() bool {
	return l != nil &&
		l.state.admissionInput.Correlation.TraceID != "" &&
		l.state.admissionResult.Reserved
}

// Settled reports whether the reservation has already been finalized (settled
// or released) so no further Settle/Release should run. It is the consumption
// site's read view of the settled idempotency guard owned by the lifecycle,
// replacing the old retryRecvStream.authoritySettled atomic.
func (l *authorityLifecycle) Settled() bool {
	return l != nil && l.settled.Load()
}

// Settle calls the authority service Settle once for the owned reservation. On
// success it marks the lifecycle settled and returns true. On settle failure
// (service error or not-applied result) it releases the reservation with
// ReleaseKindLosing — the losing-fallback previously hand-written at the
// finalizeResponseFinishedAuthority and settleCancellationAuthority sites —
// marks settled, and returns false. Idempotent via settled: a second Settle is
// a no-op. No-op when inactive or when svc is nil. Returns true only when the
// settle applied.
//
// The SettleInput construction is moved verbatim from Executor.settleAttemptAuthority
// (helpers attemptAuthorityRuleID, attemptAuthorityUsageAmount, attemptAuthorityCostAmount)
// so behavior is preserved exactly; only the losing-fallback and settled guard
// are new.
func (l *authorityLifecycle) Settle(ctx context.Context, kind authorityapp.SettlementKind, usageEv lipapi.Event, clientCanceled bool) bool {
	if l == nil || !l.IsActive() || l.settled.Load() {
		return false
	}
	if l.svc == nil {
		return false
	}
	settleInput := authorityapp.SettleInput{
		Correlation:    l.state.admissionInput.Correlation,
		Scope:          l.state.admissionInput.Scope,
		ReservationKey: l.state.admissionInput.ReservationKey,
		ReservationID:  l.state.admissionResult.ReservationID,
		RuleID:         attemptAuthorityRuleID(l.state),
		Kind:           kind,
		FinalUsage:     attemptAuthorityUsageAmount(usageEv, l.state.admissionInput.Request),
		FinalCost:      attemptAuthorityCostAmount(usageEv, l.state.admissionInput.Spend.Currency),
		ReservedUsage:  l.state.admissionResult.ReservedAmount,
		EstimatedUsage: l.state.admissionInput.Request,
		EstimatedCost:  l.state.admissionInput.Spend,
		Authority:      domain.AuthorityLevelAuthoritative,
		ClientCanceled: clientCanceled,
	}
	result, err := l.svc.Settle(ctx, settleInput)
	if err != nil {
		if l.log != nil {
			l.log.DebugContext(ctx, "usage authority settle failed", "error", err, "candidate_key", l.cand.Key)
		}
		l.Release(ctx, authorityapp.ReleaseKindLosing)
		l.settled.Store(true)
		return false
	}
	if !result.Applied {
		l.Release(ctx, authorityapp.ReleaseKindLosing)
		l.settled.Store(true)
		return false
	}
	l.settled.Store(true)
	return true
}

// Release releases the owned reservation once with the supplied kind; idempotent
// via settled. It marks settled after the attempt so a later Settle/Release is
// a no-op, matching the existing fallback sites that set authoritySettled after
// releasing. No-op when inactive or when svc is nil.
//
// The ReleaseInput construction is moved verbatim from Executor.releaseAttemptAuthority
// (helper attemptAuthorityRuleID) so behavior is preserved exactly.
func (l *authorityLifecycle) Release(ctx context.Context, kind authorityapp.ReleaseKind) {
	if l == nil || !l.IsActive() || l.settled.Load() {
		return
	}
	if l.svc == nil {
		return
	}
	releaseInput := authorityapp.ReleaseInput{
		Correlation:    l.state.admissionInput.Correlation,
		Scope:          l.state.admissionInput.Scope,
		ReservationKey: l.state.admissionInput.ReservationKey,
		ReservationID:  l.state.admissionResult.ReservationID,
		RuleID:         attemptAuthorityRuleID(l.state),
		Kind:           kind,
		Amount:         l.state.admissionResult.ReservedAmount,
	}
	if _, err := l.svc.Release(ctx, releaseInput); err != nil && l.log != nil {
		l.log.DebugContext(ctx, "usage authority release failed", "error", err, "candidate_key", l.cand.Key)
	}
	l.settled.Store(true)
}

// Reset replaces the reservation state for a replacement iteration (recv-phase
// failover or parallel winner handoff) and clears the settled guard so the new
// reservation can be settled/released independently.
func (l *authorityLifecycle) Reset(state attemptAuthorityState, cand routing.AttemptCandidate) {
	if l == nil {
		return
	}
	l.state = state
	l.cand = cand
	l.settled.Store(false)
}
