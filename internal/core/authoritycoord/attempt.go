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

		d, err := invokeAdmitAttempt(ctx, slot.Provider, in)
		if err != nil {
			var claimed CompensationStack
			pushAttemptDecisionHolds(&claimed, id, slot.Provider, in.RequestID, in.AttemptID, in.BLegID, d)
			claimedFails := claimed.ReverseCompensate(ctx, timeout)
			if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
				out.CompensateFailures = append(out.CompensateFailures, claimedFails...)
				out.Readiness = AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
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
			fails := out.Stack.ReverseCompensate(ctx, timeout)
			out.CompensateFailures = fails
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, fmt.Errorf("authoritycoord: attempt %s: %w", id, merr)
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
			pushAttemptDecisionHolds(&out.Stack, id, slot.Provider, in.RequestID, in.AttemptID, in.BLegID, d)
		}
	}
	return out, nil
}

// Settle settles attempt reservations through their owning providers using the
// compensation stack from Admit (requirements 5.3, 5.4, 15.9). Handles are never
// broadcast across providers: each slot receives only its own reservation handles.
// An empty stack is a no-op. Each provider callback runs on a fresh bounded
// cleanup context independent of client cancellation (requirements 8.7, 15.3).
func (c *AttemptCoordinator) Settle(parent context.Context, stack CompensationStack, in authority.AttemptSettlement) error {
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
		if id == "" || h == "" {
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
			id = fmt.Sprintf("attempt-class-%d", slot.Class)
		}
		handles := handlesByProvider[id]
		if len(handles) == 0 {
			continue
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
		settlement := in
		settlement.Handles = append([]string(nil), handles...)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
		_, err := invokeSettleAttempt(ctx, slot.Provider, settlement)
		cancel()
		if err == nil {
			continue
		}
		if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
			continue
		}
		if first == nil {
			first = &ErrUnavailable{ProviderID: id, Err: err}
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
