package app

import (
	"context"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Service owns concurrency-lease orchestration for admit, renew, release,
// query, and readiness. Persistence dialects implement LeaseStore (task 8.2).
type Service struct {
	rules RuleSource
	store LeaseStore
	clock Clock
}

// NewService constructs a service with explicit dependencies. A nil clock uses
// the system wall clock.
func NewService(rules RuleSource, store LeaseStore, clock Clock) *Service {
	return &Service{rules: rules, store: store, clock: clock}
}

func (s *Service) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

// Admit acquires or replays one lease per matching rule for a logical request.
// On multi-rule allow, scalar fields reflect the lastAllow primary; Leases lists all.
// On strict deny, prior acquires from this Admit are released before return.
func (s *Service) Admit(ctx context.Context, in AdmitInput) (AdmitResult, error) {
	if s == nil || s.store == nil || s.rules == nil {
		return AdmitResult{}, WrapError("admit", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return AdmitResult{}, err
	}
	requestID := strings.TrimSpace(in.RequestID)
	if requestID == "" {
		return AdmitResult{}, WrapError("admit", ErrInvalidInput)
	}

	snap, err := s.rules.Snapshot(ctx)
	if err != nil {
		return AdmitResult{}, WrapError("admit", err)
	}
	ready := mergeReadiness(snap.Readiness, nil)
	if storeReady, storeErr := s.store.CheckReadiness(ctx); storeErr == nil {
		ready = mergeReadiness(ready, &storeReady)
	}

	// Auxiliary requests inherit the parent lease by default (requirement 10.10).
	if in.Lifecycle == metering.LifecycleAuxiliaryRequest && in.AuxPolicy.InheritsParent() {
		return AdmitResult{
			Kind:      domain.DecisionAllow,
			LeaseID:   strings.TrimSpace(in.ParentLeaseID),
			Readiness: ready,
			Acquired:  false,
		}, nil
	}

	dims := domain.DimensionsFromScope(in.Scope)
	namespace := strings.TrimSpace(in.Namespace)
	now := s.now()

	matched := matchRules(snap.Rules, dims, namespace, in.RuleID)
	if len(matched) == 0 {
		return AdmitResult{Kind: domain.DecisionAllow, Readiness: ready}, nil
	}

	var (
		best           AdmitResult
		haveBest       bool
		sawDeny        bool
		sawAdvise      bool
		lastAllow      AdmitResult
		haveAllow      bool
		rollbackIDs    []string
		acquiredLeases []AdmittedLease
	)
	best = AdmitResult{Kind: domain.DecisionAllow, Readiness: ready}

	for _, rule := range matched {
		ttl := rule.EffectiveTTL()
		if in.TTL > 0 {
			ttl = in.TTL
		}
		ns := rule.Namespace
		if ns == "" {
			ns = namespace
		}
		leaseID := domain.StableLeaseID(ns, rule.ID, rule.Version, requestID, dims)
		proposed := domain.NewLease(domain.NewLeaseParams{
			LeaseID:     leaseID,
			RuleID:      rule.ID,
			RuleVersion: rule.Version,
			LogicalID:   requestID,
			Namespace:   ns,
			Dimensions:  dims,
			Now:         now,
			TTL:         ttl,
		})
		acq, acqErr := s.store.Acquire(ctx, AcquireCommand{
			Lease:      proposed,
			RuleID:     rule.ID,
			Dimensions: dims,
			Limit:      rule.Limit,
			Mode:       rule.Mode,
			Now:        now,
		})
		if acqErr != nil {
			s.rollbackAcquired(ctx, rollbackIDs, requestID, now)
			return AdmitResult{}, WrapError("admit", acqErr)
		}

		if acq.CapacityExceeded {
			if rule.Mode == domain.RuleModeAdvisory {
				sawAdvise = true
				best = AdmitResult{
					Kind:           domain.DecisionAdvisory,
					RemainingSlots: 0,
					Readiness:      ready,
					Evidence:       domain.DenialEvidence(rule.ID, 0),
					RuleID:         rule.ID,
					BoundVersion:   in.BoundVersion,
				}
				haveBest = true
				continue
			}
			sawDeny = true
			best = AdmitResult{
				Kind:           domain.DecisionDeny,
				RemainingSlots: 0,
				Readiness:      ready,
				Evidence:       domain.DenialEvidence(rule.ID, 0),
				RuleID:         rule.ID,
				BoundVersion:   in.BoundVersion,
			}
			haveBest = true
			// Stop matching further rules; prior acquires are rolled back below.
			break
		}
		if acq.Rejected {
			continue
		}

		occ := AdmittedLease{
			LeaseID:         acq.Lease.LeaseID,
			RuleID:          rule.ID,
			Generation:      acq.Lease.Generation,
			ExpiresAt:       acq.Lease.ExpiresAt,
			RenewBefore:     rule.EffectiveRenewBefore(),
			TTL:             ttl,
			FailureBehavior: rule.FailureBehavior,
			Acquired:        !acq.Replayed,
			Replayed:        acq.Replayed,
		}
		if occ.Acquired {
			rollbackIDs = append(rollbackIDs, occ.LeaseID)
		}
		acquiredLeases = append(acquiredLeases, occ)

		out := AdmitResult{
			Kind:            domain.DecisionAllow,
			LeaseID:         acq.Lease.LeaseID,
			Generation:      acq.Lease.Generation,
			ExpiresAt:       acq.Lease.ExpiresAt,
			RemainingSlots:  acq.RemainingSlots,
			Readiness:       ready,
			BoundVersion:    snap.PolicyRef(),
			Acquired:        !acq.Replayed,
			Replayed:        acq.Replayed,
			RuleID:          rule.ID,
			RenewBefore:     rule.EffectiveRenewBefore(),
			TTL:             ttl,
			FailureBehavior: rule.FailureBehavior,
			Leases:          append([]AdmittedLease(nil), acquiredLeases...),
		}
		if in.BoundVersion.Version != "" {
			out.BoundVersion = in.BoundVersion
		}
		lastAllow = out
		haveAllow = true
		if !haveBest || best.Kind == domain.DecisionAllow {
			best = out
			haveBest = true
		}
	}

	if sawDeny {
		// Strict capacity deny: release every occupancy acquired in this Admit
		// so prior rules do not leave orphans (multi-rule mid-loop deny).
		s.rollbackAcquired(ctx, rollbackIDs, requestID, now)
		best.Leases = nil
		best.LeaseID = ""
		best.Generation = 0
		best.ExpiresAt = time.Time{}
		best.Acquired = false
		best.Replayed = false
		return best, nil
	}
	if sawAdvise && !haveAllow {
		return best, nil
	}
	if haveAllow {
		lastAllow.Leases = append([]AdmittedLease(nil), acquiredLeases...)
		return lastAllow, nil
	}
	if haveBest {
		return best, nil
	}
	return AdmitResult{Kind: domain.DecisionAllow, Readiness: ready}, nil
}

// rollbackAcquired idempotently releases leases acquired earlier in the same Admit.
func (s *Service) rollbackAcquired(ctx context.Context, leaseIDs []string, requestID string, now time.Time) {
	if s == nil || s.store == nil {
		return
	}
	for _, id := range leaseIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		_, _ = s.store.Release(ctx, ReleaseCommand{
			LeaseID:   id,
			RequestID: requestID,
			Reason:    "admit_deny_rollback",
			Now:       now,
		})
	}
}

func matchRules(rules []domain.Rule, dims domain.Dimensions, namespace, ruleID string) []domain.Rule {
	out := make([]domain.Rule, 0, len(rules))
	wantRule := strings.TrimSpace(ruleID)
	for _, rule := range rules {
		if wantRule != "" && rule.ID != wantRule {
			continue
		}
		if namespace != "" && rule.Namespace != "" && rule.Namespace != namespace {
			continue
		}
		if rule.Limit <= 0 {
			continue
		}
		if !rule.Mode.IsKnown() {
			continue
		}
		if !rule.Matches(dims) {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func mergeReadiness(a domain.Readiness, b *domain.Readiness) domain.Readiness {
	if b == nil {
		if a.State == "" {
			return domain.Readiness{State: domain.ReadinessStateReady}
		}
		return a
	}
	return worseReadiness(a, *b)
}

func worseReadiness(a, b domain.Readiness) domain.Readiness {
	rank := func(s domain.ReadinessState) int {
		switch s {
		case domain.ReadinessStateReady:
			return 0
		case domain.ReadinessStateDegraded:
			return 1
		case domain.ReadinessStateUnavailable:
			return 2
		case domain.ReadinessStateDisabled:
			return 3
		default:
			return 0
		}
	}
	if rank(b.State) > rank(a.State) {
		return b
	}
	if a.State == "" {
		return b
	}
	return a
}

// Renew extends an active lease with generation CAS.
func (s *Service) Renew(ctx context.Context, in RenewInput) (AdmitResult, error) {
	if s == nil || s.store == nil {
		return AdmitResult{}, WrapError("renew", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return AdmitResult{}, err
	}
	if strings.TrimSpace(in.LeaseID) == "" {
		return AdmitResult{}, WrapError("renew", ErrInvalidInput)
	}
	ready, _ := s.ReadinessDomain(ctx)
	res, err := s.store.Renew(ctx, RenewCommand{
		LeaseID:            in.LeaseID,
		RequestID:          in.RequestID,
		ExpectedGeneration: in.ExpectedGeneration,
		TTL:                in.TTL,
		Now:                s.now(),
	})
	if err != nil {
		return AdmitResult{}, WrapError("renew", err)
	}
	return AdmitResult{
		Kind:       domain.DecisionAllow,
		LeaseID:    res.Lease.LeaseID,
		Generation: res.Lease.Generation,
		ExpiresAt:  res.Lease.ExpiresAt,
		Readiness:  ready,
	}, nil
}

// Release releases a lease idempotently.
func (s *Service) Release(ctx context.Context, in ReleaseInput) error {
	if s == nil || s.store == nil {
		return WrapError("release", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(in.LeaseID) == "" {
		return WrapError("release", ErrInvalidInput)
	}
	_, err := s.store.Release(ctx, ReleaseCommand{
		LeaseID:   in.LeaseID,
		RequestID: in.RequestID,
		Reason:    in.Reason,
		Now:       s.now(),
	})
	if err != nil {
		return WrapError("release", err)
	}
	return nil
}

// RulesSnapshot returns the current immutable rule set for capacity queries.
func (s *Service) RulesSnapshot(ctx context.Context) (RuleSnapshot, error) {
	if s == nil || s.rules == nil {
		return RuleSnapshot{}, WrapError("rules", ErrUnavailable)
	}
	return s.rules.Snapshot(ctx)
}

// Query returns a bounded lease page.
func (s *Service) Query(ctx context.Context, q QueryCommand) (QueryResult, error) {
	if s == nil || s.store == nil {
		return QueryResult{}, WrapError("query", ErrUnavailable)
	}
	if q.Now.IsZero() {
		q.Now = s.now()
	}
	res, err := s.store.Query(ctx, q)
	if err != nil {
		return QueryResult{}, WrapError("query", err)
	}
	return res, nil
}

// ReadinessDomain returns domain readiness from store (and optional snapshot).
func (s *Service) ReadinessDomain(ctx context.Context) (domain.Readiness, error) {
	if s == nil || s.store == nil {
		return domain.Readiness{State: domain.ReadinessStateUnavailable}, WrapError("readiness", ErrUnavailable)
	}
	ready, err := s.store.CheckReadiness(ctx)
	if err != nil {
		return domain.Readiness{State: domain.ReadinessStateUnavailable}, WrapError("readiness", err)
	}
	if s.rules != nil {
		if snap, snapErr := s.rules.Snapshot(ctx); snapErr == nil {
			ready = mergeReadiness(snap.Readiness, &ready)
		}
	}
	if ready.State == "" {
		ready.State = domain.ReadinessStateReady
	}
	return ready, nil
}

// Readiness maps domain readiness onto the public authority readiness enum.
func (s *Service) Readiness(ctx context.Context) (authority.Readiness, error) {
	ready, err := s.ReadinessDomain(ctx)
	if err != nil {
		return authority.ReadinessUnavailable, err
	}
	return mapReadiness(ready), nil
}

func mapReadiness(r domain.Readiness) authority.Readiness {
	switch r.State {
	case domain.ReadinessStateReady:
		return authority.ReadinessReady
	case domain.ReadinessStateDegraded:
		return authority.ReadinessDegraded
	case domain.ReadinessStateUnavailable:
		return authority.ReadinessUnavailable
	case domain.ReadinessStateDisabled:
		return authority.ReadinessDisabled
	default:
		return authority.ReadinessReady
	}
}
