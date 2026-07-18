package authoritycoord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// RequestSlot is one classified logical-request authority provider.
type RequestSlot struct {
	ID              string
	Class           PriorityClass
	Provider        authority.RequestProvider
	Strength        authority.Strength
	FailureBehavior authority.FailureBehavior
}

// RequestCoordinator evaluates concurrency (optional) then classified request
// providers in deterministic priority-class order (requirements 4.5, 8.1, 15.1–15.4).
type RequestCoordinator struct {
	Concurrency    authority.ConcurrencyProvider // nil skips lease admit (Phase 8)
	Slots          []RequestSlot
	CleanupTimeout time.Duration
}

// Admit runs request-stage admission and returns a composite decision.
// On later denial/failure, prior holds are reverse-compensated with fresh contexts.
func (c *RequestCoordinator) Admit(ctx context.Context, in authority.RequestAdmission) (CompositeDecision, error) {
	if err := in.Validate(); err != nil {
		return CompositeDecision{}, err
	}
	if c == nil {
		return CompositeDecision{Kind: authority.DecisionAllow, Readiness: authority.ReadinessDisabled}, nil
	}
	out := CompositeDecision{Kind: authority.DecisionAllow, Readiness: authority.ReadinessReady}
	timeout := c.CleanupTimeout
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}

	if c.Concurrency != nil {
		leaseIn := authority.LeaseAdmission{
			RequestID:      in.RequestID,
			Scope:          in.Scope,
			IdempotencyKey: in.IdempotencyKey,
			Lifecycle:      in.Lifecycle,
			ParentLeaseID:  in.ParentLeaseID,
			AuxPolicy:      in.AuxPolicy,
		}
		ld, err := invokeAdmitLease(ctx, c.Concurrency, leaseIn)
		if err != nil {
			var claimed CompensationStack
			pushLeaseDecisionHolds(&claimed, c.Concurrency, in.RequestID, ld)
			fails := claimed.ReverseCompensate(ctx, timeout)
			out.CompensateFailures = fails
			out.Kind = authority.DecisionDeny
			out.DeniedBy = "concurrency"
			return out, &ErrUnavailable{ProviderID: "concurrency", Err: err}
		}
		out.Lease = ld
		out.Readiness = AggregateReadiness(out.Readiness, ld.Readiness)
		if ld.BoundVersion.Version != "" {
			out.BoundVersions = append(out.BoundVersions, ld.BoundVersion)
		}
		switch ld.Kind {
		case authority.LeaseDeny:
			out.Kind = authority.DecisionDeny
			out.DeniedBy = "concurrency"
			return out, &ErrDenied{ProviderID: "concurrency"}
		case authority.LeaseAllow, authority.LeaseAdvisory, "":
			pushLeaseDecisionHolds(&out.Stack, c.Concurrency, in.RequestID, ld)
		}
	}

	slots := append([]RequestSlot(nil), c.Slots...)
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].Class != slots[j].Class {
			return slots[i].Class < slots[j].Class
		}
		return slots[i].ID < slots[j].ID
	})

	for _, slot := range slots {
		if slot.Provider == nil {
			continue
		}
		id := strings.TrimSpace(slot.ID)
		if id == "" {
			id = fmt.Sprintf("class-%d", slot.Class)
		}
		strength := slot.Strength
		if strength == "" {
			if slot.Class == PriorityAdvisory {
				strength = authority.StrengthAdvisory
			} else {
				strength = authority.StrengthRequired
			}
		}
		failBeh := slot.FailureBehavior
		if failBeh == "" {
			if strength == authority.StrengthAdvisory {
				failBeh = authority.FailureFailOpen
			} else {
				failBeh = authority.FailureFailClosed
			}
		}

		d, err := invokeAdmitRequest(ctx, slot.Provider, in)
		if err != nil {
			var claimed CompensationStack
			pushRequestDecisionHolds(&claimed, id, slot.Provider, in.RequestID, d)
			claimedFails := claimed.ReverseCompensate(ctx, timeout)
			if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
				out.CompensateFailures = append(out.CompensateFailures, claimedFails...)
				out.Readiness = AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			// Capacity-style denials wrapped as ErrDenied must not fail-open (15.4).
			fails := out.Stack.ReverseCompensate(ctx, timeout)
			out.CompensateFailures = append(claimedFails, fails...)
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, &ErrUnavailable{ProviderID: id, Err: err}
		}
		out.ProviderDecisions = append(out.ProviderDecisions, d)
		out.Readiness = AggregateReadiness(out.Readiness, d.Readiness)
		merged, merr := mergeClampsNonWidening(out.Clamps, d.Clamps)
		if merr != nil {
			// d is not on out.Stack yet; compensate its holds before unwinding prior slots.
			var claimed CompensationStack
			pushRequestDecisionHolds(&claimed, id, slot.Provider, in.RequestID, d)
			claimedFails := claimed.ReverseCompensate(ctx, timeout)
			fails := out.Stack.ReverseCompensate(ctx, timeout)
			out.CompensateFailures = append(claimedFails, fails...)
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, fmt.Errorf("authoritycoord: request %s: %w", id, merr)
		}
		out.Clamps = merged
		if len(d.BoundVersions) > 0 {
			out.BoundVersions = append(out.BoundVersions, d.BoundVersions...)
		}

		switch d.Kind {
		case authority.DecisionDeny:
			fails := out.Stack.ReverseCompensate(ctx, timeout)
			out.CompensateFailures = fails
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, &ErrDenied{ProviderID: id, Decision: d}
		case authority.DecisionAllow, authority.DecisionAdvisory, "":
			pushRequestDecisionHolds(&out.Stack, id, slot.Provider, in.RequestID, d)
		}
	}
	return out, nil
}

// Settle settles request reservations through their owning providers using the
// compensation stack from Admit. Concurrency lease IDs on the stack are never
// forwarded to RequestProvider.SettleRequest (requirement 10.5). Each provider
// callback runs on a fresh bounded cleanup context independent of client
// cancellation (requirements 8.7, 15.3). Handles are never broadcast: each slot
// receives only its own stack handles. Settlement failures remain observable for
// retry regardless of admission-time fail-open/advisory posture (requirement 15.5).
// An empty stack is a no-op.
func (c *RequestCoordinator) Settle(parent context.Context, stack CompensationStack, in authority.RequestSettlement) error {
	if c == nil {
		return nil
	}
	timeout := defaultCleanupTimeout
	if c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	handlesByProvider := make(map[string][]string)
	for _, e := range stack.Entries() {
		id := strings.TrimSpace(e.ProviderID)
		h := strings.TrimSpace(e.Handle)
		if id == "" || h == "" || id == "concurrency" {
			continue
		}
		handlesByProvider[id] = append(handlesByProvider[id], h)
	}
	var first error
	for _, slot := range c.Slots {
		if slot.Provider == nil {
			continue
		}
		id := strings.TrimSpace(slot.ID)
		if id == "" {
			id = fmt.Sprintf("class-%d", slot.Class)
		}
		handles := handlesByProvider[id]
		if len(handles) == 0 {
			continue
		}
		settlement := in
		settlement.Handles = append([]string(nil), handles...)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
		_, err := invokeSettleRequest(ctx, slot.Provider, settlement)
		cancel()
		if err == nil {
			continue
		}
		if first == nil {
			first = &ErrUnavailable{ProviderID: id, Err: err}
		}
	}
	return first
}

// ReleaseLease releases a concurrency occupancy using a fresh cleanup context (15.3).
func (c *RequestCoordinator) ReleaseLease(parent context.Context, leaseID, requestID, reason string) error {
	if c == nil || c.Concurrency == nil || strings.TrimSpace(leaseID) == "" {
		return nil
	}
	timeout := defaultCleanupTimeout
	if c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	return c.Concurrency.ReleaseLease(ctx, authority.LeaseRelease{
		LeaseID:   leaseID,
		RequestID: requestID,
		Reason:    reason,
	})
}

// ReleaseLeases releases each lease ID with a fresh cleanup context (idempotent per ID).
func (c *RequestCoordinator) ReleaseLeases(parent context.Context, leaseIDs []string, requestID, reason string) error {
	var first error
	for _, id := range leaseIDs {
		if err := c.ReleaseLease(parent, id, requestID, reason); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// leaseIDsFromDecision returns all occupancy IDs from a lease decision.
// Prefers Leases when non-empty; otherwise falls back to the primary LeaseID.
func leaseIDsFromDecision(ld authority.LeaseDecision) []string {
	if len(ld.Leases) > 0 {
		out := make([]string, 0, len(ld.Leases))
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
			out = append(out, id)
		}
		if len(out) > 0 {
			return out
		}
	}
	if id := strings.TrimSpace(ld.LeaseID); id != "" {
		return []string{id}
	}
	return nil
}

// Release compensates request-stage holds using a fresh cleanup context.
func (c *RequestCoordinator) Release(ctx context.Context, stack CompensationStack, reqID string) []CompensateFailed {
	_ = reqID
	timeout := defaultCleanupTimeout
	if c != nil && c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	return stack.ReverseCompensate(ctx, timeout)
}

// IsDenied reports whether err is a capacity/policy denial.
func IsDenied(err error) bool {
	var d *ErrDenied
	return errors.As(err, &d)
}
