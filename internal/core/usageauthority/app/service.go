package app

import (
	"context"
	"errors"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// Service owns usage-authority orchestration for admission, settlement,
// release, status, and bounded query flows.
type Service struct {
	rules    RuleSource
	store    StateStore
	evidence EvidenceSink
	clock    Clock
}

// NewService constructs a service with explicit dependencies. A nil clock
// means "use system wall-clock time" (see Service.now).
func NewService(rules RuleSource, store StateStore, evidence EvidenceSink, clock Clock) *Service {
	return &Service{
		rules:    rules,
		store:    store,
		evidence: evidence,
		clock:    clock,
	}
}

func (s *Service) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now().UTC()
	}
	return time.Now().UTC()
}

// snapshotTolerant fetches the rule snapshot for settlement/release without
// hard-failing when the rule source is unavailable. A nil rule source or a
// snapshot error yields an empty RuleSnapshot so selectedRuleKind returns "" and
// normalization uses Preserve (an empty UnknownAttribution normalizes identically
// to UnknownAttributionPreserve). Settlement/release stay error-tolerant, so
// this never returns an error; fetch the snapshot once and reuse it for both
// normalization and rule-kind derivation.
func (s *Service) snapshotTolerant(ctx context.Context) RuleSnapshot {
	if s == nil || s.rules == nil {
		return RuleSnapshot{}
	}
	snap, err := s.rules.Snapshot(ctx)
	if err != nil {
		return RuleSnapshot{}
	}
	return snap
}

func (s *Service) normalizeAdmissionInput(mode domain.UnknownAttribution, in AdmissionInput) AdmissionInput {
	in.Scope = mode.NormalizeScope(in.Scope)
	in.Dimensions = mode.NormalizeDimensions(in.Dimensions)
	return in
}

func (s *Service) normalizeSettleInput(mode domain.UnknownAttribution, in SettleInput) SettleInput {
	in.Scope = mode.NormalizeScope(in.Scope)
	return in
}

func (s *Service) normalizeReleaseInput(mode domain.UnknownAttribution, in ReleaseInput) ReleaseInput {
	in.Scope = mode.NormalizeScope(in.Scope)
	return in
}

func (s *Service) snapshot(ctx context.Context) (RuleSnapshot, error) {
	if s == nil || s.rules == nil {
		return RuleSnapshot{}, WrapError(ErrUnavailable, "snapshot", errors.New("rule source not configured"))
	}
	snap, err := s.rules.Snapshot(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return RuleSnapshot{}, WrapError(ErrEvaluationTimeout, "snapshot", err)
		}
		return RuleSnapshot{}, WrapError(ErrUnavailable, "snapshot", err)
	}
	if snap.FetchedAt.IsZero() {
		snap.FetchedAt = s.now()
	}
	return snap, nil
}

func (s *Service) readiness(ctx context.Context, fallback domain.AuthorityStatus) (domain.AuthorityStatus, error) {
	if s != nil && s.store != nil {
		status, err := s.store.CheckReadiness(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return domain.AuthorityStatus{}, WrapError(ErrEvaluationTimeout, "readiness", err)
			}
			return domain.AuthorityStatus{}, WrapError(ErrUnavailable, "readiness", err)
		}
		if status.State == "" {
			if fallback.State != "" {
				return fallback, nil
			}
			return domain.AuthorityStatus{
				State:  domain.AuthorityStateUnavailable,
				Reason: domain.StatusReasonBackingUnavailable,
			}, nil
		}
		return status, nil
	}
	return fallback, nil
}

// readinessForEvidence fetches the live readiness status for settlement/release
// evidence without hard-failing when the backing store is unavailable. On error
// it falls back to the snapshot's status (or an empty AuthorityStatus when the
// snapshot carried none). Settlement/release stay error-tolerant, so this never
// returns an error.
func (s *Service) readinessForEvidence(ctx context.Context, fallback domain.AuthorityStatus) domain.AuthorityStatus {
	status, err := s.readiness(ctx, fallback)
	if err != nil {
		return fallback
	}
	return status
}

func (s *Service) admissionStatus(ctx context.Context, snap RuleSnapshot) (domain.AuthorityStatus, error) {
	status, err := s.readiness(ctx, snap.Status)
	if err != nil {
		return domain.AuthorityStatus{}, err
	}
	return status, nil
}

func (s *Service) projectAdmissionEvidence(ctx context.Context, in AdmissionInput, res AdmissionResult, status domain.AuthorityStatus, rules []domain.Rule, now time.Time) (policyAndControlPlane, error) {
	ruleKind := res.RuleKind
	if ruleKind == "" {
		ruleKind = selectedRuleKind(res.RuleIDs, rules)
	}
	if ruleKind == "" {
		ruleKind = domain.RuleKindQuota
	}
	reserved := res.Reserved && res.ReservationID != ""
	evidence, err := projectAuthorityEvidence(status, reserved, Evidence{
		At:              now,
		Correlation:     in.Correlation,
		Scope:           in.Scope,
		RuleID:          firstRuleID(res.RuleIDs, in.ReservationKey.RuleID),
		RuleType:        string(ruleKind),
		Outcome:         sdkOutcomeFromAdmission(res.Outcome),
		ReasonCode:      reasonForAdmission(res.Outcome, status, reserved, res.ReservationID),
		ReservationID:   res.ReservationID,
		SettlementState: settlementStateForAdmission(res.Reserved),
		Unit:            string(in.Request.Unit),
		Currency:        in.Request.Currency,
		Reserved:        res.ReservedAmount.Value,
	})
	if err != nil {
		return policyAndControlPlane{}, err
	}
	if s != nil && s.evidence != nil {
		if err := s.evidence.RecordPolicyDecision(ctx, evidence.Policy); err != nil {
			return policyAndControlPlane{}, WrapError(ErrUnavailable, "admission evidence", err)
		}
		if err := s.evidence.RecordAccountingAuthority(ctx, evidence.Event); err != nil {
			return policyAndControlPlane{}, WrapError(ErrUnavailable, "admission evidence", err)
		}
	}
	return evidence, nil
}

type policyAndControlPlane struct {
	Policy policydecision.Record
	Event  controlplane.Event
}
