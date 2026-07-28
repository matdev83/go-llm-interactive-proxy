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
	// Descriptor, when set, is the registration descriptor for Decision.ValidateFor.
	// Nil uses a synthetic descriptor from ID/strength/failure and Stage.
	Descriptor *authority.ProviderDescriptor
	// Stage defaults to StageRequestAdmit when empty.
	Stage authority.Stage
}

// RequestCoordinator evaluates concurrency (optional) then classified request
// providers in deterministic priority-class order (requirements 4.5, 8.1, 15.1–15.4).
type RequestCoordinator struct {
	Concurrency authority.ConcurrencyProvider // nil skips lease admit (Phase 8)
	// ConcurrencyDescriptor, when set, is used for lease ValidateFor/ValidateRenewalFor.
	// When nil, concurrencyRegistration synthesizes a StageLeaseAdmit descriptor.
	ConcurrencyDescriptor *authority.ProviderDescriptor
	Slots                 []RequestSlot
	CleanupTimeout        time.Duration
	// Now, when set, supplies the single evaluation time for lease validation on Admit.
	// Nil defaults to time.Now (UTC) captured once per public call.
	Now func() time.Time
}

func (c *RequestCoordinator) nowUTC() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
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
	if err := validateRequestSlots(c.Slots); err != nil {
		return CompositeDecision{}, err
	}
	out := CompositeDecision{Kind: authority.DecisionAllow, Readiness: authority.ReadinessReady}
	timeout := c.CleanupTimeout
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	now := c.nowUTC()

	if c.Concurrency != nil {
		leaseIn := authority.LeaseAdmission{
			RequestID:      in.RequestID,
			Scope:          in.Scope,
			IdempotencyKey: in.IdempotencyKey,
			Lifecycle:      in.Lifecycle,
			ParentLeaseID:  in.ParentLeaseID,
			AuxPolicy:      in.AuxPolicy,
		}
		reg, regErr := c.concurrencyRegistration()
		if regErr != nil {
			return CompositeDecision{}, regErr
		}
		ld, err := invokeAdmitLease(ctx, c.Concurrency, leaseIn, now, reg)
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
		strength, failBeh := resolveRequestPosture(slot)

		d, err := invokeAdmitRequest(ctx, slot.Provider, in)
		if err != nil {
			fails := compensateCurrentThenPrior(ctx, timeout, &out.Stack, func(claimed *CompensationStack) {
				pushRequestDecisionHolds(claimed, id, slot.Provider, in.RequestID, d)
			})
			out.CompensateFailures = append(out.CompensateFailures, fails...)
			if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
				out.Readiness = AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, &ErrUnavailable{ProviderID: id, Err: err}
		}

		reg, stage := requestSlotRegistration(slot)
		if vErr := validateCoordinatorDecision(d, reg, stage); vErr != nil {
			ownsHolds := decisionHasHolds(d)
			if errors.Is(vErr, errNonAllowWithHolds) && d.Kind == authority.DecisionDeny && strength == authority.StrengthAdvisory {
				fails := compensateCurrentOnly(ctx, timeout, func(claimed *CompensationStack) {
					pushRequestDecisionHolds(claimed, id, slot.Provider, in.RequestID, d)
				})
				out.CompensateFailures = append(out.CompensateFailures, fails...)
				d.Kind = authority.DecisionAdvisory
				d.Evidence = advisoryEvidenceFrom(d)
				out.ProviderDecisions = append(out.ProviderDecisions, d)
				out.Evidence = mergeAdvisoryEvidence(out.Evidence, d.Evidence)
				out.Readiness = AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			// Advisory malformed degrades after compensating own holds; required malformed
			// always fails closed even when FailureFailOpen (req 3.6 / D5).
			if strength == authority.StrengthAdvisory {
				fails := compensateCurrentOnly(ctx, timeout, func(claimed *CompensationStack) {
					if ownsHolds {
						pushRequestDecisionHolds(claimed, id, slot.Provider, in.RequestID, d)
					}
				})
				out.CompensateFailures = append(out.CompensateFailures, fails...)
				out.Readiness = AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			fails := compensateCurrentThenPrior(ctx, timeout, &out.Stack, func(claimed *CompensationStack) {
				if ownsHolds {
					pushRequestDecisionHolds(claimed, id, slot.Provider, in.RequestID, d)
				}
			})
			out.CompensateFailures = append(out.CompensateFailures, fails...)
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			if errors.Is(vErr, errNonAllowWithHolds) && d.Kind == authority.DecisionDeny {
				return out, &ErrDenied{ProviderID: id, Decision: d}
			}
			return out, &ErrUnavailable{ProviderID: id, Err: vErr}
		}

		out.ProviderDecisions = append(out.ProviderDecisions, d)
		out.Readiness = AggregateReadiness(out.Readiness, d.Readiness)
		merged, merr := mergeClampsNonWidening(out.Clamps, d.Clamps)
		if merr != nil {
			fails := compensateCurrentThenPrior(ctx, timeout, &out.Stack, func(claimed *CompensationStack) {
				pushRequestDecisionHolds(claimed, id, slot.Provider, in.RequestID, d)
			})
			out.CompensateFailures = append(out.CompensateFailures, fails...)
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
			if strength == authority.StrengthAdvisory {
				d.Kind = authority.DecisionAdvisory
				d.Evidence = advisoryEvidenceFrom(d)
				out.ProviderDecisions[len(out.ProviderDecisions)-1] = d
				out.Evidence = mergeAdvisoryEvidence(out.Evidence, d.Evidence)
				out.Readiness = AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			// Required deterministic deny always fails closed, even fail-open (req 3.6).
			fails := compensateCurrentThenPrior(ctx, timeout, &out.Stack, func(claimed *CompensationStack) {
				if decisionHasHolds(d) {
					pushRequestDecisionHolds(claimed, id, slot.Provider, in.RequestID, d)
				}
			})
			out.CompensateFailures = append(out.CompensateFailures, fails...)
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			return out, &ErrDenied{ProviderID: id, Decision: d}
		case authority.DecisionAllow, authority.DecisionAdvisory, "":
			pushRequestDecisionHolds(&out.Stack, id, slot.Provider, in.RequestID, d)
			if d.Kind == authority.DecisionAdvisory {
				out.Evidence = mergeAdvisoryEvidence(out.Evidence, d.Evidence)
			}
		}
	}
	return out, nil
}

func resolveRequestPosture(slot RequestSlot) (authority.Strength, authority.FailureBehavior) {
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
	return strength, failBeh
}

func compensateCurrentOnly(ctx context.Context, timeout time.Duration, claim func(*CompensationStack)) []CompensateFailed {
	var claimed CompensationStack
	if claim != nil {
		claim(&claimed)
	}
	return claimed.ReverseCompensate(ctx, timeout)
}

func compensateCurrentThenPrior(ctx context.Context, timeout time.Duration, prior *CompensationStack, claim func(*CompensationStack)) []CompensateFailed {
	claimedFails := compensateCurrentOnly(ctx, timeout, claim)
	var priorFails []CompensateFailed
	if prior != nil {
		priorFails = prior.ReverseCompensate(ctx, timeout)
	}
	return append(claimedFails, priorFails...)
}

func mergeAdvisoryEvidence(dst, add authority.SafeEvidence) authority.SafeEvidence {
	if strings.TrimSpace(dst.Category) == "" {
		return add
	}
	if strings.TrimSpace(add.Category) == "" {
		return dst
	}
	return add
}

// Settle settles request reservations through their owning providers using the
// compensation stack from Admit. Concurrency lease IDs on the stack are never
// forwarded to RequestProvider.SettleRequest (requirement 10.5). Each provider
// callback runs on a fresh bounded cleanup context independent of client
// cancellation (requirements 8.7, 15.3). Handles are never broadcast: each slot
// receives only its own stack handles. Settlement failures remain observable for
// retry regardless of admission-time fail-open/advisory posture (requirement 15.5).
// Successful providers are not re-settled on retry (requirements 7.7, 8.6).
// An empty stack is a no-op.
func (c *RequestCoordinator) Settle(parent context.Context, stack CompensationStack, in authority.RequestSettlement) error {
	if c == nil {
		return nil
	}
	if err := validateRequestSlots(c.Slots); err != nil {
		return err
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
	tracker := stack.settlement()
	var first error
	for _, slot := range c.Slots {
		if slot.Provider == nil {
			continue
		}
		id := strings.TrimSpace(slot.ID)
		handles := handlesByProvider[id]
		if len(handles) == 0 {
			continue
		}
		skip, wait, finish := tracker.beginSettle(id)
		if skip {
			continue
		}
		if wait != nil {
			<-wait
			if err := tracker.waitResult(id); err != nil && first == nil {
				first = &ErrUnavailable{ProviderID: id, Err: err}
			}
			continue
		}
		settlement := in
		settlement.Handles = append([]string(nil), handles...)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
		result, err := invokeSettleRequest(ctx, slot.Provider, settlement)
		cancel()
		if err == nil {
			if vErr := validateSettlement(result, handles); vErr != nil {
				err = vErr
			}
		}
		finish(err)
		if err != nil && first == nil {
			first = &ErrUnavailable{ProviderID: id, Err: err}
		}
	}
	return first
}

// RenewLease validates a concurrency renew result using concurrencyRegistration
// and Now before returning it to callers (requirements 4.1, 10.2).
func (c *RequestCoordinator) RenewLease(ctx context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	if c == nil || c.Concurrency == nil {
		return authority.LeaseDecision{}, fmt.Errorf("authoritycoord: concurrency provider not configured")
	}
	reg, err := c.concurrencyRegistration()
	if err != nil {
		return authority.LeaseDecision{}, err
	}
	return invokeRenewLease(ctx, c.Concurrency, in, c.nowUTC(), reg)
}

// concurrencyRegistration returns the descriptor used for lease ValidateFor /
// ValidateRenewalFor. Explicit ConcurrencyDescriptor wins; otherwise a Describer
// or a synthetic StageLeaseAdmit registration with a non-empty ID is used.
func (c *RequestCoordinator) concurrencyRegistration() (authority.ProviderDescriptor, error) {
	if c == nil {
		return authority.ProviderDescriptor{}, fmt.Errorf("authoritycoord: nil coordinator")
	}
	if c.ConcurrencyDescriptor != nil {
		if strings.TrimSpace(c.ConcurrencyDescriptor.ID) == "" {
			return authority.ProviderDescriptor{}, fmt.Errorf("authoritycoord: concurrency descriptor id required")
		}
		return *c.ConcurrencyDescriptor, nil
	}
	if d, ok := c.Concurrency.(authority.Describer); ok {
		desc := d.Describe()
		if strings.TrimSpace(desc.ID) == "" {
			return authority.ProviderDescriptor{}, fmt.Errorf("authoritycoord: concurrency describer id required")
		}
		if _, ok := authority.AdmitPosture(desc, authority.StageLeaseAdmit); !ok {
			desc.Postures = append(append([]authority.StagePosture(nil), desc.Postures...), authority.StagePosture{
				Stage:           authority.StageLeaseAdmit,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			})
		}
		return desc, nil
	}
	return authority.ProviderDescriptor{
		ID: "concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}, nil
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

// ReleaseLeaseSet releases a complete atomic lease set using a fresh cleanup context.
func (c *RequestCoordinator) ReleaseLeaseSet(parent context.Context, setID, leaseID, requestID, reason string) error {
	if c == nil || c.Concurrency == nil || strings.TrimSpace(setID) == "" {
		return nil
	}
	timeout := defaultCleanupTimeout
	if c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	return c.Concurrency.ReleaseLease(ctx, authority.LeaseRelease{
		SetID:     setID,
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
func (c *RequestCoordinator) Release(ctx context.Context, stack CompensationStack) []CompensateFailed {
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
