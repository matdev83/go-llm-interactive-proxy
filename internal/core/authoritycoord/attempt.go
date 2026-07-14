package authoritycoord

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// AttemptPriorityClass orders attempt-stage providers (design attempt order).
type AttemptPriorityClass int

const (
	AttemptPriorityHardSpend AttemptPriorityClass = iota
	AttemptPriorityQuotaRate
	AttemptPriorityAdvisory
)

// AttemptSlot is one classified backend-attempt authority provider.
type AttemptSlot struct {
	ID              string
	Class           AttemptPriorityClass
	Provider        authority.AttemptProvider
	Strength        authority.Strength
	FailureBehavior authority.FailureBehavior
}

// AttemptCoordinator evaluates operator attempt providers per committed B-leg
// (requirements 5.1, 5.3, 5.5, 8.2, 9.3).
type AttemptCoordinator struct {
	Slots          []AttemptSlot
	CleanupTimeout time.Duration
}

// Admit runs attempt-stage admission for one B-leg.
func (c *AttemptCoordinator) Admit(ctx context.Context, in authority.AttemptAdmission) (CompositeDecision, error) {
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

	slots := append([]AttemptSlot(nil), c.Slots...)
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
			id = fmt.Sprintf("attempt-class-%d", slot.Class)
		}
		strength := slot.Strength
		if strength == "" {
			if slot.Class == AttemptPriorityAdvisory {
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

		d, err := invokeAdmitAttempt(slot.Provider, ctx, in)
		if err != nil {
			if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
				out.Readiness = AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			fails := out.Stack.ReverseCompensate(ctx, timeout)
			out.CompensateFailures = fails
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, &ErrUnavailable{ProviderID: id, Err: err}
		}
		out.ProviderDecisions = append(out.ProviderDecisions, d)
		out.Readiness = AggregateReadiness(out.Readiness, d.Readiness)
		out.Clamps = mergeClampsNonWidening(out.Clamps, d.Clamps)
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
				reqID, attID, bleg := in.RequestID, in.AttemptID, in.BLegID
				out.Stack.Push(StackEntry{
					ProviderID: id,
					Handle:     h,
					Evidence:   d.Evidence,
					Compensate: func(cctx context.Context) error {
						return prov.ReleaseAttempt(cctx, authority.AttemptRelease{
							RequestID:          reqID,
							AttemptID:          attID,
							BLegID:             bleg,
							Handles:            []string{h},
							CompensationHandle: h,
						})
					},
				})
			}
		}
	}
	return out, nil
}

// Settle settles attempt providers for one B-leg.
func (c *AttemptCoordinator) Settle(ctx context.Context, in authority.AttemptSettlement) error {
	if c == nil {
		return nil
	}
	var first error
	for _, slot := range c.Slots {
		if slot.Provider == nil {
			continue
		}
		if _, err := slot.Provider.SettleAttempt(ctx, in); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Release reverse-compensates attempt-stage holds with a fresh cleanup context.
func (c *AttemptCoordinator) Release(ctx context.Context, stack CompensationStack) []CompensateFailed {
	timeout := defaultCleanupTimeout
	if c != nil && c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	return stack.ReverseCompensate(ctx, timeout)
}
