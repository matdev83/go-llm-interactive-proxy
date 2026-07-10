package app

import (
	"context"
	"errors"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// Admit evaluates the configured rules and reserves strict exposure before any
// protected backend work starts.
func (s *Service) Admit(ctx context.Context, in AdmissionInput) (AdmissionResult, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return AdmissionResult{}, WrapError(ErrEvaluationTimeout, "admit", err)
		}
		return AdmissionResult{}, err
	}

	snap, err := s.snapshot(ctx)
	if err != nil {
		return AdmissionResult{}, err
	}
	in = s.normalizeAdmissionInput(snap.UnknownAttribution, in)

	status, err := s.admissionStatus(ctx, snap)
	if err != nil {
		return AdmissionResult{}, err
	}
	switch status.State {
	case domain.AuthorityStateDisabled:
		return AdmissionResult{}, WrapError(ErrDisabled, "admit", errors.New(string(status.Reason)))
	case domain.AuthorityStateDegraded:
		return AdmissionResult{}, WrapError(ErrDegraded, "admit", errors.New(string(status.Reason)))
	case domain.AuthorityStateUnavailable:
		return AdmissionResult{}, WrapError(ErrUnavailable, "admit", errors.New(string(status.Reason)))
	}

	now := s.now()
	evaluation := domain.EvaluateRules(snap.Rules, domain.EvaluationContext{
		Dimensions:     in.Dimensions,
		Amount:         in.Request,
		Spend:          in.Spend,
		RequestCount:   in.RequestCount,
		PreflightUsage: in.PreflightUsage,
		Authority:      in.Authority,
		At:             now,
	})

	result := AdmissionResult{
		Allowed:  isAdmissionAllowed(evaluation.Selected.Outcome),
		Outcome:  evaluation.Selected.Outcome,
		RuleIDs:  append([]string(nil), evaluation.Selected.RuleIDs...),
		RuleKind: selectedRuleKind(evaluation.Selected.RuleIDs, snap.Rules),
	}

	if !result.Allowed {
		projection, err := s.projectAdmissionEvidence(ctx, in, result, status, snap.Rules, now)
		if err != nil {
			return AdmissionResult{}, err
		}
		result.PolicyRecord = projection.Policy
		result.AccountingEvent = projection.Event
		return result, nil
	}

	if requiresReservation(evaluation, snap.Rules) && !in.EstimateOnly {
		reserve, err := s.storeReserve(ctx, in, now, evaluation, snap.Rules)
		if err != nil {
			return AdmissionResult{}, err
		}
		result.Reserved = reserve.Applied
		result.ReservationID = reserve.ReservationID
		if reserve.Applied {
			result.ReservedAmount = reserve.ReservedAmount
		}
	}

	projection, err := s.projectAdmissionEvidence(ctx, in, result, status, snap.Rules, now)
	if err != nil {
		return AdmissionResult{}, err
	}
	result.PolicyRecord = projection.Policy
	result.AccountingEvent = projection.Event
	return result, nil
}

func requiresReservation(result domain.EvaluationResult, rules []domain.Rule) bool {
	for _, match := range result.Matches {
		rule, ok := ruleByID(rules, match.RuleID)
		if !ok {
			continue
		}
		if rule.Mode != domain.RuleModeStrict {
			continue
		}
		if match.Outcome == domain.DecisionOutcomeAllow || match.Outcome == domain.DecisionOutcomeClamp {
			switch rule.Kind {
			case domain.RuleKindQuota, domain.RuleKindRate, domain.RuleKindBudget, domain.RuleKindSpendCap:
				return true
			}
		}
	}
	return false
}

func ruleByID(rules []domain.Rule, id string) (domain.Rule, bool) {
	for _, rule := range rules {
		if rule.ID == id {
			return rule, true
		}
	}
	return domain.Rule{}, false
}

func selectedRuleKind(ruleIDs []string, rules []domain.Rule) domain.RuleKind {
	for _, id := range ruleIDs {
		if rule, ok := ruleByID(rules, id); ok {
			return rule.Kind
		}
	}
	return ""
}

func isAdmissionAllowed(outcome domain.DecisionOutcome) bool {
	switch outcome {
	case domain.DecisionOutcomeAllow, domain.DecisionOutcomeAdvisory, domain.DecisionOutcomeClamp:
		return true
	default:
		return false
	}
}

func (s *Service) storeReserve(ctx context.Context, in AdmissionInput, now time.Time, evaluation domain.EvaluationResult, rules []domain.Rule) (ReserveResult, error) {
	if s == nil || s.store == nil {
		return ReserveResult{}, WrapError(ErrUnavailable, "reserve", errors.New("store not configured"))
	}
	reservationID := in.ReservationKey.String()
	ruleID := firstRuleID(evaluation.Selected.RuleIDs, in.ReservationKey.RuleID)
	selectedRule, _ := ruleByID(rules, ruleID)
	unit := selectedRule.Unit
	if unit == "" {
		unit = selectedRule.Limit.Unit
	}
	requestAmount := in.Request
	if unit == domain.AmountUnitRequests && in.RequestCount.Unit != "" {
		requestAmount = in.RequestCount
	} else if amount, ok := in.PreflightUsage.AmountForUnit(unit); ok {
		requestAmount = amount
	}
	ruleType := string(selectedRule.Kind)
	if ruleType == "" {
		ruleType = "quota"
	}
	cmd := ReserveCommand{
		ReservationKey: in.ReservationKey,
		RuleID:         ruleID,
		RuleType:       ruleType,
		Dimensions:     in.Dimensions,
		Request:        requestAmount,
		Spend:          in.Spend,
		Authority:      in.Authority,
		EstimateOnly:   in.EstimateOnly,
		At:             now,
		SourceKey: sourceEventKey(Evidence{
			At:            now,
			Correlation:   in.Correlation,
			Scope:         in.Scope,
			RuleID:        in.ReservationKey.RuleID,
			Outcome:       controlplane.AccountingOutcomeReserve,
			ReasonCode:    policydecision.AccountingReasonReserved,
			ReservationID: reservationID,
		}),
	}
	reserve, err := s.store.Reserve(ctx, cmd)
	if err != nil {
		if errors.Is(err, ErrReservationConflict) {
			return ReserveResult{}, WrapError(ErrReservationConflict, "reserve", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ReserveResult{}, WrapError(ErrEvaluationTimeout, "reserve", err)
		}
		return ReserveResult{}, WrapError(ErrUnavailable, "reserve", err)
	}
	if reserve.Applied && reserve.ReservedAmount.Unit == "" {
		reserve.ReservedAmount = requestAmount
	}
	return reserve, nil
}
