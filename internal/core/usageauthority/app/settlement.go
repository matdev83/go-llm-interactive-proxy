package app

import (
	"context"
	"errors"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// Settle reconciles surfaced-attempt usage against a matching reservation.
func (s *Service) Settle(ctx context.Context, in SettleInput) (SettleResult, error) {
	if err := ctx.Err(); err != nil && !in.ClientCanceled {
		if errors.Is(err, context.DeadlineExceeded) {
			return SettleResult{}, WrapError(ErrEvaluationTimeout, "settle", err)
		}
		return SettleResult{}, err
	}

	now := s.now()
	snap := s.snapshotTolerant(ctx)
	in = s.normalizeSettleInput(snap.UnknownAttribution, in)
	settle, err := s.storeSettle(ctx, in, now)
	if err != nil {
		return SettleResult{}, err
	}

	result := SettleResult{
		Applied:         settle.Applied,
		ReservationID:   settle.ReservationID,
		ReleasedDelta:   settle.ReleasedDelta,
		OverageDelta:    settle.OverageDelta,
		AdjustmentDelta: settle.AdjustmentDelta,
	}
	if result.ReservationID == "" {
		result.ReservationID = in.ReservationID
	}

	ruleKind := selectedRuleKind([]string{in.RuleID}, snap.Rules)
	status := s.readinessForEvidence(ctx, snap.Status)
	projection, err := s.emitSettlementEvidence(ctx, in, result, now, ruleKind, status)
	if err != nil {
		return SettleResult{}, err
	}
	result.PolicyRecord = projection.Policy
	result.AccountingEvent = projection.Event
	return result, nil
}

// Release marks reservations for swallowed and losing attempts without
// attributing surfaced usage to the released attempt.
func (s *Service) Release(ctx context.Context, in ReleaseInput) (ReleaseResult, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ReleaseResult{}, WrapError(ErrEvaluationTimeout, "release", err)
		}
		return ReleaseResult{}, err
	}

	now := s.now()
	snap := s.snapshotTolerant(ctx)
	in = s.normalizeReleaseInput(snap.UnknownAttribution, in)
	release, err := s.storeRelease(ctx, in, now)
	if err != nil {
		return ReleaseResult{}, err
	}

	result := ReleaseResult{
		Applied:       release.Applied,
		ReservationID: release.ReservationID,
		ReleasedDelta: release.ReleasedDelta,
	}
	if result.ReservationID == "" {
		result.ReservationID = in.ReservationID
	}

	ruleKind := selectedRuleKind([]string{in.RuleID}, snap.Rules)
	status := s.readinessForEvidence(ctx, snap.Status)
	projection, err := s.emitReleaseEvidence(ctx, in, result, now, ruleKind, status)
	if err != nil {
		return ReleaseResult{}, err
	}
	result.PolicyRecord = projection.Policy
	result.AccountingEvent = projection.Event
	return result, nil
}

func (s *Service) storeSettle(ctx context.Context, in SettleInput, now time.Time) (SettleResult, error) {
	if s == nil || s.store == nil {
		return SettleResult{}, WrapError(ErrUnavailable, "settle", errors.New("store not configured"))
	}
	cmd := SettleCommand{
		SettlementKey: domain.SettlementKey{
			ReservationKey: in.ReservationKey,
			Sequence:       1,
		},
		ReservationKey: in.ReservationKey,
		ReservationID:  in.ReservationID,
		RuleID:         in.RuleID,
		Kind:           in.Kind,
		FinalUsage:     in.FinalUsage,
		FinalCost:      in.FinalCost,
		ReservedUsage:  in.ReservedUsage,
		EstimatedUsage: in.EstimatedUsage,
		EstimatedCost:  in.EstimatedCost,
		Authority:      in.Authority,
		ClientCanceled: in.ClientCanceled,
		At:             now,
		SourceKey: sourceEventKey(Evidence{
			At:              now,
			Correlation:     in.Correlation,
			Scope:           in.Scope,
			RuleID:          in.RuleID,
			Outcome:         controlplane.AccountingOutcomeReconcile,
			ReasonCode:      policydecision.AccountingReasonReconciled,
			ReservationID:   in.ReservationID,
			SettlementState: controlplane.AccountingSettlementSettled,
		}),
	}
	settle, err := s.store.Settle(ctx, cmd)
	if err != nil {
		if errors.Is(err, ErrDuplicateSettlement) {
			return SettleResult{}, WrapError(ErrDuplicateSettlement, "settle", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return SettleResult{}, WrapError(ErrEvaluationTimeout, "settle", err)
		}
		return SettleResult{}, WrapError(ErrUnavailable, "settle", err)
	}
	if settle.ReservationID == "" {
		settle.ReservationID = in.ReservationID
	}
	return settle, nil
}

func (s *Service) storeRelease(ctx context.Context, in ReleaseInput, now time.Time) (ReleaseResult, error) {
	if s == nil || s.store == nil {
		return ReleaseResult{}, WrapError(ErrUnavailable, "release", errors.New("store not configured"))
	}
	cmd := ReleaseCommand{
		ReleaseKey: domain.ReleaseKey{
			ReservationKey: in.ReservationKey,
			Sequence:       1,
		},
		ReservationKey: in.ReservationKey,
		ReservationID:  in.ReservationID,
		RuleID:         in.RuleID,
		Kind:           in.Kind,
		Amount:         in.Amount,
		At:             now,
		SourceKey: sourceEventKey(Evidence{
			At:              now,
			Correlation:     in.Correlation,
			Scope:           in.Scope,
			RuleID:          in.RuleID,
			Outcome:         controlplane.AccountingOutcomeReconcile,
			ReasonCode:      policydecision.AccountingReasonReserved,
			ReservationID:   in.ReservationID,
			SettlementState: controlplane.AccountingSettlementReleased,
		}),
	}
	release, err := s.store.Release(ctx, cmd)
	if err != nil {
		if errors.Is(err, ErrDuplicateSettlement) {
			return ReleaseResult{}, WrapError(ErrDuplicateSettlement, "release", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ReleaseResult{}, WrapError(ErrEvaluationTimeout, "release", err)
		}
		return ReleaseResult{}, WrapError(ErrUnavailable, "release", err)
	}
	if release.ReservationID == "" {
		release.ReservationID = in.ReservationID
	}
	return release, nil
}

func (s *Service) emitSettlementEvidence(ctx context.Context, in SettleInput, result SettleResult, now time.Time, ruleKind domain.RuleKind, status domain.AuthorityStatus) (policyAndControlPlane, error) {
	outcome, reason, state := settlementProjection(in.Kind, result)
	projection, err := projectAuthorityEvidence(status, true, Evidence{
		At:              now,
		Correlation:     in.Correlation,
		Scope:           in.Scope,
		RuleID:          in.RuleID,
		RuleType:        string(ruleKind),
		Outcome:         outcome,
		ReasonCode:      reason,
		ReservationID:   result.ReservationID,
		SettlementState: state,
		Unit:            string(in.FinalUsage.Unit),
		Currency:        in.FinalUsage.Currency,
		Consumed:        in.FinalUsage.Value,
		Reserved:        in.ReservedUsage.Value,
		Adjustment:      result.AdjustmentDelta.Value,
	})
	if err != nil {
		return policyAndControlPlane{}, err
	}
	if s != nil && s.evidence != nil {
		if err := s.evidence.RecordPolicyDecision(ctx, projection.Policy); err != nil {
			return policyAndControlPlane{}, WrapError(ErrUnavailable, "settlement evidence", err)
		}
		if err := s.evidence.RecordAccountingAuthority(ctx, projection.Event); err != nil {
			return policyAndControlPlane{}, WrapError(ErrUnavailable, "settlement evidence", err)
		}
	}
	return projection, nil
}

func (s *Service) emitReleaseEvidence(ctx context.Context, in ReleaseInput, result ReleaseResult, now time.Time, ruleKind domain.RuleKind, status domain.AuthorityStatus) (policyAndControlPlane, error) {
	projection, err := projectAuthorityEvidence(status, true, Evidence{
		At:              now,
		Correlation:     in.Correlation,
		Scope:           in.Scope,
		RuleID:          in.RuleID,
		RuleType:        string(ruleKind),
		Outcome:         controlplane.AccountingOutcomeReconcile,
		ReasonCode:      policydecision.AccountingReasonReserved,
		ReservationID:   result.ReservationID,
		SettlementState: controlplane.AccountingSettlementReleased,
		Unit:            string(in.Amount.Unit),
		Currency:        in.Amount.Currency,
		Reserved:        in.Amount.Value,
	})
	if err != nil {
		return policyAndControlPlane{}, err
	}
	if s != nil && s.evidence != nil {
		if err := s.evidence.RecordPolicyDecision(ctx, projection.Policy); err != nil {
			return policyAndControlPlane{}, WrapError(ErrUnavailable, "release evidence", err)
		}
		if err := s.evidence.RecordAccountingAuthority(ctx, projection.Event); err != nil {
			return policyAndControlPlane{}, WrapError(ErrUnavailable, "release evidence", err)
		}
	}
	return projection, nil
}
