package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// Admit evaluates the configured rules and reserves strict exposure before any
// protected backend work starts.
//
// Rule-level availability scenarios — authority-unavailable outcomes
// (authoritative-only rules under estimated evidence), unavailable reservation
// infrastructure, best-effort evidence
// projection failures, and evaluation timeouts — are resolved through the
// matched rules' effective failure behavior and returned as AdmissionResult
// values (not Go errors) so the runtime maps them through the normal outcome
// path (requirement 4.6, 5.5, 6.3, 6.6, 8.3, 10.1-10.3). Go errors are still
// returned for system-wide disabled/degraded/unavailable status where no
// matched rules could be enforced; the runtimebundle startup-posture fallback
// handles those. Deterministic capacity and reservation conflicts always deny.
func (s *Service) Admit(ctx context.Context, in AdmissionInput) (res AdmissionResult, err error) {
	start := time.Now()
	defer func() {
		s.observeStage(StageAdmit, admitOutcome(res, err), time.Since(start).Seconds())
	}()
	// Cancellation: stop accounting work without converting it into an unrelated
	// denial (requirement 10.4). A canceled context returns the raw error so the
	// runtime can distinguish cancellation from enforcement. A deadline-exceeded
	// context is not short-circuited here; it is resolved through the matched
	// rules' failure behavior once the snapshot is available.
	if err := ctx.Err(); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			return AdmissionResult{}, err
		}
	}
	evaluationCtx, cancel := s.evaluationContext(ctx)
	defer cancel()

	snap, err := s.snapshot(evaluationCtx)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return AdmissionResult{}, ctx.Err()
		}
		if errors.Is(evaluationCtx.Err(), context.DeadlineExceeded) {
			if cached := s.cachedSnapshot(); len(cached.Rules) > 0 {
				now := s.now()
				evaluation := domain.EvaluateRules(cached.Rules, evaluationContextFromAdmission(in, now))
				behavior := effectiveFailureBehavior(evaluation.Matches, cached.Rules, cached.Status)
				return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, cached.Status, cached.Rules, now, behavior, policydecision.AccountingReasonUnavailable)
			}
			return s.resolveAdmissionTimeoutWithoutSnapshot(evaluationCtx, in)
		}
		return AdmissionResult{}, err
	}
	in = s.normalizeAdmissionInput(snap.UnknownAttribution, in, snap.Rules)

	status, err := s.admissionStatus(evaluationCtx, snap)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return AdmissionResult{}, ctx.Err()
		}
		if errors.Is(evaluationCtx.Err(), context.DeadlineExceeded) {
			evaluation := domain.EvaluateRules(snap.Rules, evaluationContextFromAdmission(in, s.now()))
			behavior := effectiveFailureBehavior(evaluation.Matches, snap.Rules, snap.Status)
			return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, snap.Status, snap.Rules, s.now(), behavior, policydecision.AccountingReasonUnavailable)
		}
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
	evaluation := domain.EvaluateRules(snap.Rules, evaluationContextFromAdmission(in, now))

	failBehavior := effectiveFailureBehavior(evaluation.Matches, snap.Rules, status)
	strictUnavailableIDs := domain.StrictUnavailableRuleIDs(evaluation.Matches)
	admissionReason := policydecision.AccountingReasonCode("")
	if len(strictUnavailableIDs) > 0 {
		unavailableBehavior := effectiveFailureBehaviorForRuleIDs(strictUnavailableIDs, evaluation.Matches, snap.Rules, status)
		if unavailableBehavior == domain.FailureBehaviorFailClosed {
			return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, status, snap.Rules, now, unavailableBehavior, policydecision.AccountingReasonUnavailable)
		}
		excluded := make(map[string]struct{}, len(strictUnavailableIDs))
		for _, id := range strictUnavailableIDs {
			excluded[id] = struct{}{}
		}
		evaluation.Selected = domain.SelectRuleOutcome(evaluation.Matches, excluded)
		// A strict unavailable rule must remain visible in the admission
		// posture even when another advisory rule has a higher raw outcome
		// severity. Fail-open means the request may proceed, but only as an
		// advisory decision; otherwise the unavailable rule would be silently
		// bypassed by the selected advisory/clamp result.
		if evaluation.Selected.Outcome != domain.DecisionOutcomeDeny {
			evaluation.Selected.Outcome = domain.DecisionOutcomeAdvisory
			admissionReason = policydecision.AccountingReasonUnavailable
		}
		evaluation.Selected.RuleIDs = matchedRuleIDs(evaluation.Matches)
	}

	// Evaluation timeout reached after rules are known: resolve via the matched
	// rules' failure behavior (requirement 10.3). When no strict rule matched,
	// there is nothing to enforce, so the deadline does not change the outcome.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(evaluationCtx.Err(), context.DeadlineExceeded) {
		if hasMatchedStrictRules(evaluation.Matches) {
			return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, status, snap.Rules, now, failBehavior, policydecision.AccountingReasonUnavailable)
		}
	}

	result := AdmissionResult{
		Allowed:         isAdmissionAllowed(evaluation.Selected.Outcome),
		Outcome:         evaluation.Selected.Outcome,
		RuleIDs:         matchedRuleIDs(evaluation.Matches),
		AdvisoryRuleIDs: advisoryRuleIDs(evaluation.Matches, snap.Rules),
		SelectedRuleID:  evaluation.Selected.RuleID,
		RuleKind:        evaluation.Selected.Kind,
		BoundVersion:    snap.PolicyRef(),
	}

	// Authority-unavailable outcome (authoritative-only rule under estimated
	// evidence, currency/unit mismatch): resolve via the rule's failure behavior
	// instead of denying outright (requirement 8.3, 5.5, 6.3).
	if evaluation.Selected.Outcome == domain.DecisionOutcomeUnavailable {
		return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, status, snap.Rules, now, failBehavior, policydecision.AccountingReasonUnavailable)
	}
	result.UnreservedRuleIDs = unreservedRuleIDs(evaluation, snap.Rules)

	if !result.Allowed {
		projection, err := s.projectAdmissionEvidence(evaluationCtx, in, result, status, snap.Rules, now, admissionReason)
		if err != nil {
			return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, status, snap.Rules, now, failBehavior, policydecision.AccountingReasonError)
		}
		result.PolicyRecord = projection.Policy
		result.AccountingEvent = projection.Event
		return result, nil
	}

	if requiresReservation(evaluation, snap.Rules) && !in.EstimateOnly {
		for requiresReservation(evaluation, snap.Rules) {
			reserve, reservations, err := s.storeReserve(evaluationCtx, in, now, evaluation, snap.Rules)
			if err == nil {
				result.Reserved = reserve.Applied || len(reservations) > 0
				result.ReservationID = reserve.ReservationID
				if reserve.Applied {
					result.ReservedAmount = reserve.ReservedAmount
				}
				result.Reservations = reservations
				break
			}
			reason := policydecision.AccountingReasonReservationFailed
			if errors.Is(err, ErrEvaluationTimeout) {
				reason = policydecision.AccountingReasonUnavailable
			}
			failedRuleID := ReservationFailureRuleID(err)
			if failedRuleID == "" {
				if errors.Is(err, ErrCapacityExceeded) || errors.Is(err, ErrReservationConflict) {
					return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, status, snap.Rules, now, domain.FailureBehaviorFailClosed, reasonForCapacityError(err))
				}
				return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, status, snap.Rules, now, failBehavior, reason)
			}
			failed, ok := ruleMatchByID(evaluation.Matches, failedRuleID)
			if !ok {
				return s.resolveAdmissionFailure(evaluationCtx, in, evaluation, status, snap.Rules, now, failBehavior, reason)
			}
			failedBehavior := effectiveFailureBehaviorForRuleIDs([]string{failedRuleID}, evaluation.Matches, snap.Rules, status)
			failedEvaluation := evaluation
			failedEvaluation.Selected = failed
			if errors.Is(err, ErrCapacityExceeded) || errors.Is(err, ErrReservationConflict) {
				return s.resolveAdmissionFailure(evaluationCtx, in, failedEvaluation, status, snap.Rules, now, domain.FailureBehaviorFailClosed, reasonForCapacityError(err))
			}
			if failedBehavior != domain.FailureBehaviorFailOpen {
				return s.resolveAdmissionFailure(evaluationCtx, in, failedEvaluation, status, snap.Rules, now, failedBehavior, reason)
			}
			markRuleReservationFailOpen(&evaluation, failedRuleID)
			result.Allowed = true
			result.Outcome = domain.DecisionOutcomeAdvisory
			result.SelectedRuleID = failedRuleID
			result.RuleKind = failed.Kind
			result.UnreservedRuleIDs = unreservedRuleIDs(evaluation, snap.Rules)
			admissionReason = reason
		}
	}

	projection, err := s.projectAdmissionEvidence(evaluationCtx, in, result, status, snap.Rules, now, admissionReason)
	if err != nil {
		return s.resolveAdmissionEvidenceFailure(evaluationCtx, in, result, failBehavior, err)
	}
	result.PolicyRecord = projection.Policy
	result.AccountingEvent = projection.Event
	return result, nil
}

// hasMatchedStrictRules reports whether any matched rule is strict. Strict
// rules require enforceable reservation or denial; advisory rules only record
// evidence, so a deadline that prevents enforcement only matters when at least
// one strict rule matched.
func hasMatchedStrictRules(matches []domain.RuleMatch) bool {
	for _, m := range matches {
		if m.Mode == domain.RuleModeStrict {
			return true
		}
	}
	return false
}

func (s *Service) resolveAdmissionTimeoutWithoutSnapshot(ctx context.Context, in AdmissionInput) (AdmissionResult, error) {
	evaluation := domain.EvaluationResult{
		Selected: domain.RuleMatch{Outcome: domain.DecisionOutcomeUnavailable},
	}
	status := domain.AuthorityStatus{
		State:  domain.AuthorityStateUnavailable,
		Reason: domain.StatusReasonBackingUnavailable,
	}
	behavior := domain.FailureBehaviorFailClosed
	if s != nil && s.defaultFailureBehavior != "" {
		behavior = s.defaultFailureBehavior
	}
	return s.resolveAdmissionFailure(ctx, in, evaluation, status, nil, s.now(), behavior, policydecision.AccountingReasonUnavailable)
}

// resolveAdmissionFailure builds an AdmissionResult from the effective failure
// behavior and projects evidence with the scenario's stable reason code.
// Fail-open continues with an advisory outcome; fail-closed denies before
// protected work (requirement 10.1, 10.2). The result is returned (not a Go
// error) so the runtime maps it through the normal outcome path.
func (s *Service) resolveAdmissionFailure(ctx context.Context, in AdmissionInput, evaluation domain.EvaluationResult, status domain.AuthorityStatus, rules []domain.Rule, now time.Time, behavior domain.FailureBehavior, reason policydecision.AccountingReasonCode) (AdmissionResult, error) {
	allowed, outcome := resolveFailureOutcome(behavior)
	result := AdmissionResult{
		Allowed:           allowed,
		Outcome:           outcome,
		RuleIDs:           matchedRuleIDs(evaluation.Matches),
		AdvisoryRuleIDs:   advisoryRuleIDs(evaluation.Matches, rules),
		UnreservedRuleIDs: unreservedRuleIDs(evaluation, rules),
		SelectedRuleID:    evaluation.Selected.RuleID,
		RuleKind:          evaluation.Selected.Kind,
		BoundVersion:      s.cachedSnapshot().PolicyRef(),
	}
	projection, err := s.projectAdmissionEvidence(ctx, in, result, status, rules, now, reason)
	if err != nil {
		// Required pre-work evidence is itself an admission prerequisite. A
		// recorder failure must not let a fail-open rule continue protected work.
		if errors.Is(err, ErrRequiredEvidence) {
			result.Allowed = false
			result.Outcome = domain.DecisionOutcomeDeny
		}
		return result, nil
	}
	result.PolicyRecord = projection.Policy
	result.AccountingEvent = projection.Event
	return result, nil
}

// resolveAdmissionEvidenceFailure keeps a successful reservation tied to the
// admission result. Fail-open continues with the reservation owned by the
// runtime lifecycle; fail-closed compensates the complete set before denying.
func (s *Service) resolveAdmissionEvidenceFailure(ctx context.Context, in AdmissionInput, result AdmissionResult, behavior domain.FailureBehavior, evidenceErr error) (AdmissionResult, error) {
	mandatory := errors.Is(evidenceErr, ErrRequiredEvidence)
	if behavior == domain.FailureBehaviorFailOpen && !mandatory {
		result.Allowed = true
		result.Outcome = domain.DecisionOutcomeAdvisory
		return result, nil
	}
	if result.Reserved {
		cleanupCtx, cancel := s.cleanupContext(ctx)
		defer cancel()
		if err := s.compensateAdmissionReservation(cleanupCtx, in, result); err == nil {
			result.Reserved = false
			result.ReservationID = ""
			result.ReservedAmount = domain.Amount{}
			result.Reservations = nil
		}
	}
	result.Allowed = false
	result.Outcome = domain.DecisionOutcomeDeny
	return result, nil
}

func reasonForCapacityError(err error) policydecision.AccountingReasonCode {
	if errors.Is(err, ErrCapacityExceeded) {
		// resolveAdmissionFailure uses the selected rule kind to project the
		// final quota/rate/budget reason; this value only prevents the generic
		// reservation-failed override.
		return ""
	}
	return policydecision.AccountingReasonReservationFailed
}

func (s *Service) compensateAdmissionReservation(ctx context.Context, in AdmissionInput, result AdmissionResult) error {
	if s == nil || !result.Reserved {
		return nil
	}
	reservations := append([]AdmissionReservation(nil), result.Reservations...)
	if len(reservations) == 0 && result.ReservationID != "" {
		amount := result.ReservedAmount
		if amount.Unit == "" {
			amount = in.Request
		}
		reservations = append(reservations, AdmissionReservation{ReservationID: result.ReservationID, RuleID: firstRuleID(result.RuleIDs, in.ReservationKey.RuleID), ReservedAmount: amount})
	}
	if len(reservations) == 0 {
		return nil
	}
	descriptors := make([]ReleaseDescriptor, 0, len(reservations))
	for _, reservation := range reservations {
		key := in.ReservationKey
		key.RuleID = reservation.RuleID
		descriptors = append(descriptors, ReleaseDescriptor{Reservation: ReservationDescriptor{
			RuleID:         reservation.RuleID,
			Unit:           reservation.ReservedAmount.Unit,
			ReservationKey: key,
			Dimensions:     in.Dimensions,
			ReservationID:  reservation.ReservationID,
			Amount:         reservation.ReservedAmount,
		}})
	}
	_, err := s.storeRelease(ctx, ReleaseInput{
		Reservations:   descriptors,
		Correlation:    in.Correlation,
		Scope:          in.Scope,
		ReservationKey: descriptors[0].Reservation.ReservationKey,
		ReservationID:  descriptors[0].Reservation.ReservationID,
		RuleID:         descriptors[0].Reservation.RuleID,
		Kind:           ReleaseKindAdmissionFailure,
		Authority:      domain.AuthorityLevelEstimated,
		Stage:          feature.StageIDPreRequest,
	}, s.now())
	return err
}

// reservationRuleIDs returns matched strict quantity rules in evaluation order.
// Admission must reserve against every such rule so each matched window reflects
// the admitted exposure; reserving only the first would leave the remaining
// matched windows unreserved and let later admissions over-commit against them.
func reservationRuleIDs(result domain.EvaluationResult, rules []domain.Rule) []string {
	var ids []string
	for _, match := range result.Matches {
		if match.Outcome != domain.DecisionOutcomeAllow && match.Outcome != domain.DecisionOutcomeClamp {
			continue
		}
		rule, ok := ruleByID(rules, match.RuleID)
		if !ok || rule.Mode != domain.RuleModeStrict {
			continue
		}
		switch rule.Kind {
		case domain.RuleKindQuota, domain.RuleKindRate:
			ids = append(ids, match.RuleID)
		}
	}
	return ids
}

func requiresReservation(result domain.EvaluationResult, rules []domain.Rule) bool {
	return len(reservationRuleIDs(result, rules)) > 0
}

// advisoryRuleIDs returns the IDs of matched rules configured with
// RuleModeAdvisory, in evaluation (snapshot) order. Advisory rules never
// reserve, but their windows must still accumulate final usage via ApplyUsage
// (requirement 7.7), so Admit surfaces them separately from the strict
// reservation RuleIDs.
func advisoryRuleIDs(matches []domain.RuleMatch, rules []domain.Rule) []string {
	var ids []string
	for _, match := range matches {
		if match.Mode != domain.RuleModeAdvisory {
			continue
		}
		if _, ok := ruleByID(rules, match.RuleID); !ok {
			continue
		}
		ids = append(ids, match.RuleID)
	}
	return ids
}

func matchedRuleIDs(matches []domain.RuleMatch) []string {
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		if strings.TrimSpace(match.RuleID) != "" {
			ids = append(ids, match.RuleID)
		}
	}
	return ids
}

func ruleMatchByID(matches []domain.RuleMatch, ruleID string) (domain.RuleMatch, bool) {
	for _, match := range matches {
		if match.RuleID == ruleID {
			return match, true
		}
	}
	return domain.RuleMatch{}, false
}

func markRuleReservationFailOpen(evaluation *domain.EvaluationResult, ruleID string) {
	if evaluation == nil {
		return
	}
	for i := range evaluation.Matches {
		if evaluation.Matches[i].RuleID != ruleID {
			continue
		}
		evaluation.Matches[i].Outcome = domain.DecisionOutcomeAdvisory
		evaluation.Matches[i].Exceeded = true
		return
	}
}

func unreservedRuleIDs(result domain.EvaluationResult, rules []domain.Rule) []string {
	reserved := make(map[string]struct{})
	for _, id := range reservationRuleIDs(result, rules) {
		reserved[id] = struct{}{}
	}
	ids := make([]string, 0)
	for _, match := range result.Matches {
		if _, ok := reserved[match.RuleID]; ok {
			continue
		}
		rule, ok := ruleByID(rules, match.RuleID)
		if !ok || strings.TrimSpace(match.RuleID) == "" {
			continue
		}
		if rule.Mode == domain.RuleModeAdvisory || match.Outcome == domain.DecisionOutcomeUnavailable || match.Outcome == domain.DecisionOutcomeAdvisory {
			ids = append(ids, match.RuleID)
		}
	}
	return ids
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

// effectiveFailureBehavior computes the enforcement posture for a matched-rule
// failure scenario (authority-unavailable, reservation failure, evidence
// failure, evaluation timeout). Among matched strict rules, an explicit
// fail-closed wins (most restrictive); otherwise an explicit fail-open wins;
// when every matched strict rule leaves the behavior default, the global
// startup posture fallback applies (fail-open when the backing is
// advisory-only, fail-closed otherwise). Default strict behavior is
// fail-closed (requirement 10.1-10.3).
func effectiveFailureBehavior(matches []domain.RuleMatch, rules []domain.Rule, snapStatus domain.AuthorityStatus) domain.FailureBehavior {
	var hasExplicitOpen bool
	for _, m := range matches {
		if m.Mode != domain.RuleModeStrict {
			continue
		}
		rule, ok := ruleByID(rules, m.RuleID)
		if !ok {
			continue
		}
		switch rule.FailureBehavior {
		case domain.FailureBehaviorFailClosed:
			return domain.FailureBehaviorFailClosed
		case domain.FailureBehaviorFailOpen:
			hasExplicitOpen = true
		}
	}
	if hasExplicitOpen {
		return domain.FailureBehaviorFailOpen
	}
	return globalFailureBehavior(snapStatus)
}

func effectiveFailureBehaviorForRuleIDs(ids []string, matches []domain.RuleMatch, rules []domain.Rule, snapStatus domain.AuthorityStatus) domain.FailureBehavior {
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	filtered := make([]domain.RuleMatch, 0, len(ids))
	for _, match := range matches {
		if _, ok := allowed[match.RuleID]; ok {
			filtered = append(filtered, match)
		}
	}
	return effectiveFailureBehavior(filtered, rules, snapStatus)
}

func globalFailureBehavior(status domain.AuthorityStatus) domain.FailureBehavior {
	if status.State == domain.AuthorityStateAdvisoryOnly {
		return domain.FailureBehaviorFailOpen
	}
	return domain.FailureBehaviorFailClosed
}

// resolveFailureOutcome returns the (allowed, outcome) pair a fail-open or
// fail-closed posture produces for a rule-level failure scenario. Fail-open
// continues with an advisory outcome; fail-closed denies before protected
// work (requirement 10.1, 10.2).
func resolveFailureOutcome(behavior domain.FailureBehavior) (bool, domain.DecisionOutcome) {
	if behavior == domain.FailureBehaviorFailOpen {
		return true, domain.DecisionOutcomeAdvisory
	}
	return false, domain.DecisionOutcomeDeny
}

func (s *Service) storeReserve(ctx context.Context, in AdmissionInput, now time.Time, evaluation domain.EvaluationResult, rules []domain.Rule) (ReserveResult, []AdmissionReservation, error) {
	if s == nil || s.store == nil {
		return ReserveResult{}, nil, WrapError(ErrUnavailable, "reserve", errors.New("store not configured"))
	}
	ruleIDs := reservationRuleIDs(evaluation, rules)
	if len(ruleIDs) == 0 {
		// storeReserve is only reached when requiresReservation is true, so this
		// fallback is defensive: preserve the historic single-rule selection.
		ruleIDs = []string{firstRuleID(evaluation.Selected.RuleIDs, in.ReservationKey.RuleID)}
	}
	descriptors := make(ReservationSet, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		descriptor, err := s.reservationDescriptorForRule(in, now, rules, ruleID)
		if err != nil {
			return ReserveResult{}, nil, &RuleReservationError{RuleID: ruleID, Err: err}
		}
		descriptors = append(descriptors, descriptor)
	}
	first := descriptors[0]
	reserve, err := s.store.Reserve(ctx, ReserveCommand{
		Reservations:   descriptors,
		Correlation:    in.Correlation,
		Scope:          in.Scope,
		ReservationKey: first.ReservationKey,
		RuleID:         first.RuleID,
		RuleType:       string(first.Kind),
		Dimensions:     first.Dimensions,
		Request:        first.Amount,
		Authority:      in.Authority,
		EstimateOnly:   in.EstimateOnly,
		At:             now,
		SourceKey:      first.SourceKey,
	})
	if err != nil {
		if errors.Is(err, ErrCapacityExceeded) {
			return ReserveResult{}, nil, WrapError(ErrCapacityExceeded, "reserve", err)
		}
		if errors.Is(err, ErrReservationConflict) {
			return ReserveResult{}, nil, WrapError(ErrReservationConflict, "reserve", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ReserveResult{}, nil, WrapError(ErrEvaluationTimeout, "reserve", err)
		}
		return ReserveResult{}, nil, WrapError(ErrUnavailable, "reserve", err)
	}
	if reserve.Applied && reserve.ReservedAmount.Unit == "" {
		reserve.ReservedAmount = first.Amount
	}
	reservations := append([]AdmissionReservation(nil), reserve.Reservations...)
	if len(reservations) == 0 && reserve.Applied {
		for i, descriptor := range descriptors {
			reservationID := descriptor.ReservationID
			if i == 0 && reserve.ReservationID != "" {
				reservationID = reserve.ReservationID
			}
			amount := descriptor.Amount
			if i == 0 && reserve.ReservedAmount.Unit != "" {
				amount = reserve.ReservedAmount
			}
			reservations = append(reservations, AdmissionReservation{
				ReservationID:  reservationID,
				RuleID:         descriptor.RuleID,
				ReservedAmount: amount,
			})
		}
	}
	return reserve, reservations, nil
}

// reservationDescriptorForRule builds one app-owned quantity descriptor for a
// matched strict rule and preserves the complete atomic reservation set.
func (s *Service) reservationDescriptorForRule(in AdmissionInput, now time.Time, rules []domain.Rule, ruleID string) (ReservationDescriptor, error) {
	selectedRule, _ := ruleByID(rules, ruleID)
	unit := selectedRule.Unit
	if unit == "" {
		unit = selectedRule.Limit.Unit
	}
	requestAmount, ok := selectedRule.SelectAmount(domain.AmountSelectionSource{
		Amount:         in.Request,
		RequestCount:   in.RequestCount,
		PreflightUsage: in.PreflightUsage,
		Exposure:       in.Exposure,
		Facts:          in.Facts,
	})
	if !ok {
		requestAmount = in.Request
		if unit == domain.AmountUnitRequests && in.RequestCount.Unit != "" {
			requestAmount = in.RequestCount
		} else if amount, ok := in.PreflightUsage.AmountForUnit(unit); ok {
			requestAmount = amount
		}
	} else if unit != "" && requestAmount.Unit == "" {
		requestAmount.Unit = unit
	}
	reservationKey := in.ReservationKey
	reservationKey.RuleID = ruleID
	if ns := strings.TrimSpace(selectedRule.Namespace); ns != "" {
		reservationKey.Namespace = ns
	} else if selectedRule.Basis.IsLegacyCompatibility() {
		// Keep legacy key format (empty namespace) for compatibility-basis rules.
		reservationKey.Namespace = ""
	} else if selectedRule.IsDualPlaneConfigured() {
		reservationKey.Namespace = domain.NamespaceDefault
	}
	reservationID := reservationKey.String()
	return ReservationDescriptor{
		RuleID:         ruleID,
		Kind:           selectedRule.Kind,
		Unit:           unit,
		Dimensions:     in.Dimensions,
		ReservationKey: reservationKey,
		ReservationID:  reservationID,
		Amount:         requestAmount,
		Authority:      in.Authority,
		SourceKey: sourceEventKey(Evidence{
			At:            now,
			Correlation:   in.Correlation,
			Scope:         in.Scope,
			RuleID:        ruleID,
			Outcome:       controlplane.AccountingOutcomeReserve,
			ReasonCode:    policydecision.AccountingReasonReserved,
			ReservationID: reservationID,
		}),
	}, nil
}

func evaluationContextFromAdmission(in AdmissionInput, at time.Time) domain.EvaluationContext {
	return domain.EvaluationContext{
		Dimensions:     in.Dimensions,
		Amount:         in.Request,
		RequestCount:   in.RequestCount,
		PreflightUsage: in.PreflightUsage,
		Authority:      in.Authority,
		At:             at,
		LifecycleScope: in.LifecycleScope,
		Exposure:       in.Exposure,
		Facts:          in.Facts,
	}
}

func admitOutcome(res AdmissionResult, err error) string {
	if err != nil {
		return stageErrOutcome(err)
	}
	if res.Outcome == domain.DecisionOutcomeDeny {
		return OutcomeDeny
	}
	return OutcomeOK
}

func stageErrOutcome(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return OutcomeCanceled
	case errors.Is(err, ErrEvaluationTimeout), errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimeout
	case errors.Is(err, ErrDisabled):
		return OutcomeDisabled
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrDegraded):
		return OutcomeUnavailable
	case errors.Is(err, ErrInvalidQuery):
		return OutcomeError
	default:
		return OutcomeError
	}
}
