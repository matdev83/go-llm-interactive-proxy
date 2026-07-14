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
		}
		ld, err := c.Concurrency.AdmitLease(ctx, leaseIn)
		if err != nil {
			return CompositeDecision{}, &ErrUnavailable{ProviderID: "concurrency", Err: err}
		}
		out.Readiness = AggregateReadiness(out.Readiness, ld.Readiness)
		switch ld.Kind {
		case authority.LeaseDeny:
			out.Kind = authority.DecisionDeny
			out.DeniedBy = "concurrency"
			return out, &ErrDenied{ProviderID: "concurrency"}
		case authority.LeaseAllow, authority.LeaseAdvisory, "":
			if strings.TrimSpace(ld.LeaseID) != "" {
				leaseID := ld.LeaseID
				reqID := in.RequestID
				out.Stack.Push(StackEntry{
					ProviderID: "concurrency",
					Handle:     leaseID,
					Compensate: func(cctx context.Context) error {
						return c.Concurrency.ReleaseLease(cctx, authority.LeaseRelease{LeaseID: leaseID, RequestID: reqID})
					},
				})
			}
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

		d, err := slot.Provider.AdmitRequest(ctx, in)
		if err != nil {
			if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
				out.Readiness = AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			// Capacity-style denials wrapped as ErrDenied must not fail-open (15.4).
			fails := out.Stack.ReverseCompensate(ctx, timeout)
			out.CompensateFailures = fails
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, &ErrUnavailable{ProviderID: id, Err: err}
		}
		out.ProviderDecisions = append(out.ProviderDecisions, d)
		out.Readiness = AggregateReadiness(out.Readiness, d.Readiness)
		out.Clamps = mergeClampsNonWidening(out.Clamps, d.Clamps)

		switch d.Kind {
		case authority.DecisionDeny:
			fails := out.Stack.ReverseCompensate(ctx, timeout)
			out.CompensateFailures = fails
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, &ErrDenied{ProviderID: id, Decision: d}
		case authority.DecisionAllow, authority.DecisionAdvisory, "":
			for _, r := range d.Reservations {
				handle := strings.TrimSpace(r.Handle)
				if handle == "" {
					handle = strings.TrimSpace(d.CompensationHandle)
				}
				if handle == "" {
					continue
				}
				h := handle
				prov := slot.Provider
				reqID := in.RequestID
				out.Stack.Push(StackEntry{
					ProviderID: id,
					Handle:     h,
					Evidence:   d.Evidence,
					Compensate: func(cctx context.Context) error {
						return prov.ReleaseRequest(cctx, authority.RequestRelease{
							RequestID:          reqID,
							Handles:            []string{h},
							CompensationHandle: h,
						})
					},
				})
			}
			if d.Kind == authority.DecisionAdvisory && out.Kind == authority.DecisionAllow {
				// advisory does not downgrade an allow
			}
		}
	}
	return out, nil
}

// Settle settles all request providers that contributed handles (once per logical request).
func (c *RequestCoordinator) Settle(ctx context.Context, in authority.RequestSettlement) error {
	if c == nil {
		return nil
	}
	var first error
	for _, slot := range c.Slots {
		if slot.Provider == nil {
			continue
		}
		if _, err := slot.Provider.SettleRequest(ctx, in); err != nil && first == nil {
			first = err
		}
	}
	return first
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
