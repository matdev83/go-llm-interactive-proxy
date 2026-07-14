package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
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
	snap := s.snapshotForSettle(ctx, in.BoundVersion)
	in = s.normalizeSettleInput(snap.UnknownAttribution, in, snap.Rules)
	settle, err := s.storeSettle(ctx, in, now, snap.Rules)
	if err != nil {
		s.emitSettlementFailureEvidence(ctx, in, now, snap)
		return SettleResult{}, err
	}

	result := SettleResult{
		Applied:         settle.Applied,
		ReservationID:   settle.ReservationID,
		ReleasedDelta:   settle.ReleasedDelta,
		OverageDelta:    settle.OverageDelta,
		AdjustmentDelta: settle.AdjustmentDelta,
		Mutations:       append([]SettlementMutation(nil), settle.Mutations...),
	}
	if result.ReservationID == "" {
		result.ReservationID = in.ReservationID
	}

	status := s.readinessForEvidence(ctx, snap.Status)
	projection, err := s.emitSettlementEvidence(ctx, in, result, now, snap.Rules, status)
	if err != nil {
		// The store mutation already committed. Preserve its result so lifecycle
		// owners can close the reservation even when evidence recording fails.
		return result, err
	}
	result.PolicyRecord = projection.Policy
	result.AccountingEvent = projection.Event
	result.PolicyRecords = append([]policydecision.Record(nil), projection.PolicyRecords...)
	result.AccountingEvents = append([]controlplane.Event(nil), projection.AccountingEvents...)
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
	in = s.normalizeReleaseInput(snap.UnknownAttribution, in, snap.Rules)
	release, err := s.storeRelease(ctx, in, now)
	if err != nil {
		s.emitReleaseFailureEvidence(ctx, in, now, snap)
		return ReleaseResult{}, err
	}

	result := ReleaseResult{
		Applied:       release.Applied,
		ReservationID: release.ReservationID,
		ReleasedDelta: release.ReleasedDelta,
		Mutations:     append([]ReleaseMutation(nil), release.Mutations...),
	}
	if result.ReservationID == "" {
		result.ReservationID = in.ReservationID
	}

	status := s.readinessForEvidence(ctx, snap.Status)
	projection, err := s.emitReleaseEvidence(ctx, in, result, now, snap.Rules, status)
	if err != nil {
		// The release mutation already committed. Preserve its result so callers
		// do not retry an operation that is already durable.
		return result, err
	}
	result.PolicyRecord = projection.Policy
	result.AccountingEvent = projection.Event
	result.PolicyRecords = append([]policydecision.Record(nil), projection.PolicyRecords...)
	result.AccountingEvents = append([]controlplane.Event(nil), projection.AccountingEvents...)
	return result, nil
}

// emitSettlementFailureEvidence records an unavailable lifecycle outcome when
// the atomic settlement did not commit. It is best-effort: the store error is
// still returned to the lifecycle owner, but evidence failure must not turn a
// post-output accounting problem into a client denial or failover decision.
func (s *Service) emitSettlementFailureEvidence(ctx context.Context, in SettleInput, now time.Time, snap RuleSnapshot) {
	if s == nil || s.evidence == nil {
		return
	}
	ruleKind := selectedRuleKind([]string{in.RuleID}, snap.Rules)
	projection, err := projectAuthorityEvidence(s.readinessForEvidence(ctx, snap.Status), true, Evidence{
		At:               now,
		Correlation:      in.Correlation,
		Scope:            in.Scope,
		RuleID:           in.RuleID,
		RuleType:         string(ruleKind),
		Outcome:          controlplane.AccountingOutcomeUnavailable,
		ReasonCode:       policydecision.AccountingReasonUnavailable,
		ReservationID:    in.ReservationID,
		SettlementState:  controlplane.AccountingSettlementUnavailable,
		Unit:             string(in.FinalUsage.Unit),
		Currency:         in.FinalUsage.Currency,
		Authority:        domain.AuthorityLevelUnavailable,
		Stage:            in.Stage,
		BackendAttempted: in.BackendAttempted,
		OutputCommitted:  in.OutputCommitted,
	})
	if err != nil {
		return
	}
	if err := s.evidence.RecordPolicyDecision(ctx, projection.Policy); err != nil {
		return
	}
	_ = s.evidence.RecordAccountingAuthority(ctx, projection.Event)
}

func (s *Service) emitReleaseFailureEvidence(ctx context.Context, in ReleaseInput, now time.Time, snap RuleSnapshot) {
	if s == nil || s.evidence == nil {
		return
	}
	ruleKind := selectedRuleKind([]string{in.RuleID}, snap.Rules)
	projection, err := projectAuthorityEvidence(s.readinessForEvidence(ctx, snap.Status), true, Evidence{
		At:               now,
		Correlation:      in.Correlation,
		Scope:            in.Scope,
		RuleID:           in.RuleID,
		RuleType:         string(ruleKind),
		Outcome:          controlplane.AccountingOutcomeUnavailable,
		ReasonCode:       policydecision.AccountingReasonUnavailable,
		ReservationID:    in.ReservationID,
		SettlementState:  controlplane.AccountingSettlementUnavailable,
		Unit:             string(in.Amount.Unit),
		Currency:         in.Amount.Currency,
		Authority:        domain.AuthorityLevelUnavailable,
		Stage:            in.Stage,
		BackendAttempted: in.BackendAttempted,
		OutputCommitted:  in.OutputCommitted,
	})
	if err != nil {
		return
	}
	if err := s.evidence.RecordPolicyDecision(ctx, projection.Policy); err != nil {
		return
	}
	_ = s.evidence.RecordAccountingAuthority(ctx, projection.Event)
}

func (s *Service) storeSettle(ctx context.Context, in SettleInput, now time.Time, rules []domain.Rule) (SettleResult, error) {
	if s == nil || s.store == nil {
		return SettleResult{}, WrapError(ErrUnavailable, "settle", errors.New("store not configured"))
	}
	if in.Sequence <= 0 {
		in.Sequence = SettlementSequence(in.Kind, in.Authority)
	}
	descriptors := settlementDescriptors(in, now)
	if len(descriptors) == 0 {
		return SettleResult{}, WrapError(ErrReservationConflict, "settle", errors.New("settlement set is empty"))
	}
	var err error
	descriptors, in, err = applySelectedSettlementAmounts(in, descriptors, rules)
	if err != nil {
		return SettleResult{}, WrapError(ErrUnavailable, "settle", err)
	}
	cmd := SettleCommand{
		Reservations: descriptors,
		Correlation:  in.Correlation,
		Scope:        in.Scope,
		SettlementKey: domain.SettlementKey{
			ReservationKey: in.ReservationKey,
			Sequence:       in.Sequence,
		},
		ReservationKey:       in.ReservationKey,
		ReservationID:        in.ReservationID,
		RuleID:               in.RuleID,
		Kind:                 in.Kind,
		FinalUsage:           in.FinalUsage,
		FinalCost:            in.FinalCost,
		ReservedUsage:        in.ReservedUsage,
		EstimatedUsage:       in.EstimatedUsage,
		EstimatedCost:        in.EstimatedCost,
		Authority:            in.Authority,
		MeasurementAuthority: in.MeasurementAuthority,
		FinalUsagePresent:    in.FinalUsagePresent,
		FinalCostPresent:     in.FinalCostPresent,
		Stage:                in.Stage,
		BackendAttempted:     in.BackendAttempted,
		OutputCommitted:      in.OutputCommitted,
		ClientCanceled:       in.ClientCanceled,
		At:                   now,
		SourceKey:            descriptors[0].SourceKey,
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

// applySelectedSettlementAmounts resolves per-rule settle amounts before store
// mutation (requirement 9.5). Dual-plane rules use Facts/Exposure; compatibility
// basis keeps FinalUsage/FinalCost.
func applySelectedSettlementAmounts(in SettleInput, descriptors []SettlementDescriptor, rules []domain.Rule) ([]SettlementDescriptor, SettleInput, error) {
	if len(descriptors) == 0 {
		return descriptors, in, nil
	}
	out := append([]SettlementDescriptor(nil), descriptors...)
	for i := range out {
		ruleID := strings.TrimSpace(out[i].Reservation.RuleID)
		if ruleID == "" {
			ruleID = strings.TrimSpace(in.RuleID)
		}
		rule, ok := ruleByID(rules, ruleID)
		if !ok {
			continue
		}
		src := domain.AmountSelectionSource{
			FinalUsage:    out[i].FinalUsage,
			FinalCost:     out[i].FinalCost,
			Exposure:      in.Exposure,
			Facts:         in.Facts,
			ForSettlement: true,
		}
		if src.FinalUsage.Unit == "" {
			src.FinalUsage = in.FinalUsage
		}
		if src.FinalCost.Unit == "" {
			src.FinalCost = in.FinalCost
		}
		amt, selected := rule.SelectAmount(src)
		if !selected {
			if rule.Basis.IsLegacyCompatibility() || strings.TrimSpace(string(rule.Basis)) == "" {
				continue
			}
			return nil, in, fmt.Errorf("settlement amount unavailable for rule %q basis %q (dual-plane requires matching Facts/Exposure)", rule.ID, rule.Basis)
		}
		unit := rule.Unit
		if unit == "" {
			unit = rule.Limit.Unit
		}
		if unit == domain.AmountUnitMoneyNano || rule.Kind == domain.RuleKindBudget || rule.Kind == domain.RuleKindSpendCap {
			out[i].FinalCost = amt
			in.FinalCost = amt
			in.FinalCostPresent = true
		} else {
			out[i].FinalUsage = amt
			in.FinalUsage = amt
			in.FinalUsagePresent = true
		}
	}
	return out, in, nil
}

func settlementDescriptors(in SettleInput, now time.Time) []SettlementDescriptor {
	if len(in.Reservations) > 0 {
		out := append([]SettlementDescriptor(nil), in.Reservations...)
		for i := range out {
			out[i] = completeSettlementDescriptor(in, out[i], now)
		}
		return out
	}
	descriptor := SettlementDescriptor{
		Reservation: ReservationDescriptor{
			RuleID:         in.RuleID,
			Unit:           in.ReservedUsage.Unit,
			Currency:       in.ReservedUsage.Currency,
			Dimensions:     dimensionsFromScope(in.Scope),
			ReservationKey: in.ReservationKey,
			ReservationID:  in.ReservationID,
			Amount:         in.ReservedUsage,
		},
		FinalUsage:     in.FinalUsage,
		FinalCost:      in.FinalCost,
		EstimatedUsage: in.EstimatedUsage,
		EstimatedCost:  in.EstimatedCost,
	}
	return []SettlementDescriptor{completeSettlementDescriptor(in, descriptor, now)}
}

func completeSettlementDescriptor(in SettleInput, descriptor SettlementDescriptor, now time.Time) SettlementDescriptor {
	if descriptor.Reservation.ReservationKey.RuleID == "" {
		descriptor.Reservation.ReservationKey = in.ReservationKey
	}
	if descriptor.Reservation.RuleID == "" {
		descriptor.Reservation.RuleID = in.RuleID
	}
	if descriptor.Reservation.ReservationID == "" {
		descriptor.Reservation.ReservationID = in.ReservationID
	}
	if descriptor.Reservation.Amount.Unit == "" {
		descriptor.Reservation.Amount = in.ReservedUsage
	}
	if dimensionsEmpty(descriptor.Reservation.Dimensions) {
		descriptor.Reservation.Dimensions = dimensionsFromScope(in.Scope)
	}
	if descriptor.FinalUsage.Unit == "" {
		descriptor.FinalUsage = in.FinalUsage
	}
	if descriptor.FinalCost.Unit == "" {
		descriptor.FinalCost = in.FinalCost
	}
	if descriptor.EstimatedUsage.Unit == "" {
		descriptor.EstimatedUsage = in.EstimatedUsage
	}
	if descriptor.EstimatedCost.Unit == "" {
		descriptor.EstimatedCost = in.EstimatedCost
	}
	if descriptor.MeasurementAuthority.Usage == "" && descriptor.MeasurementAuthority.Cost == "" {
		descriptor.MeasurementAuthority = in.MeasurementAuthority
		if descriptor.MeasurementAuthority.Usage == "" && descriptor.MeasurementAuthority.Cost == "" {
			descriptor.MeasurementAuthority = MeasurementAuthority{Usage: in.Authority, Cost: in.Authority}
		}
	}
	if descriptor.Authority == "" {
		descriptor.Authority = descriptor.MeasurementAuthority.ForUnit(descriptor.Reservation.Amount.Unit)
	}
	if descriptor.Reservation.Authority == "" {
		descriptor.Reservation.Authority = descriptor.Authority
	}
	if descriptor.SourceKey == "" {
		sequence := in.Sequence
		if sequence <= 0 {
			sequence = SettlementSequence(in.Kind, descriptor.MeasurementAuthority.ForUnit(descriptor.Reservation.Amount.Unit))
		}
		descriptor.Sequence = sequence
		reservationID := descriptor.Reservation.ReservationID
		descriptor.SourceKey = sourceEventKey(Evidence{
			At:              now,
			Correlation:     in.Correlation,
			Scope:           in.Scope,
			RuleID:          descriptor.Reservation.RuleID,
			Outcome:         controlplane.AccountingOutcomeReconcile,
			ReasonCode:      policydecision.AccountingReasonReconciled,
			ReservationID:   reservationID,
			SettlementState: controlplane.AccountingSettlementSettled,
			SourceKind:      string(in.Kind),
			SourceSequence:  sequence,
		})
	}
	if descriptor.Sequence <= 0 {
		descriptor.Sequence = in.Sequence
		if descriptor.Sequence <= 0 {
			descriptor.Sequence = SettlementSequence(in.Kind, descriptor.MeasurementAuthority.ForUnit(descriptor.Reservation.Amount.Unit))
		}
	}
	if descriptor.Reservation.SourceKey == "" {
		descriptor.Reservation.SourceKey = descriptor.SourceKey
	}
	return descriptor
}

func dimensionsEmpty(d domain.Dimensions) bool {
	return d.Principal.IsUnknown() && d.Credential.IsUnknown() && d.Tenant.IsUnknown() &&
		d.Organization.IsUnknown() && d.Workspace.IsUnknown() && d.Project.IsUnknown() &&
		d.Department.IsUnknown() && d.CostCenter.IsUnknown() && d.Backend.IsUnknown() &&
		d.Model.IsUnknown() && d.Route.IsUnknown() && len(d.PolicyLabels) == 0
}

func dimensionsFromScope(view scope.PrincipalScopeView) domain.Dimensions {
	dims := domain.Dimensions{
		Principal:    view.PrincipalID,
		Credential:   view.CredentialID,
		Tenant:       view.TenantID,
		Organization: view.OrganizationID,
		Workspace:    view.WorkspaceID,
		Project:      view.ProjectID,
		Department:   view.DepartmentID,
		CostCenter:   view.CostCenterID,
	}
	if len(view.PolicyLabels) > 0 {
		dims.PolicyLabels = make(map[string]scope.Value, len(view.PolicyLabels))
		for key, value := range view.PolicyLabels {
			if domain.IsSafeLabelKey(key) {
				dims.PolicyLabels[key] = scope.Known(value)
			}
		}
	}
	return dims
}

func (s *Service) storeRelease(ctx context.Context, in ReleaseInput, now time.Time) (ReleaseResult, error) {
	if s == nil || s.store == nil {
		return ReleaseResult{}, WrapError(ErrUnavailable, "release", errors.New("store not configured"))
	}
	if in.Sequence <= 0 {
		in.Sequence = ReleaseSequence(in.Kind)
	}
	descriptors := releaseInputDescriptors(in, now)
	if len(descriptors) == 0 {
		return ReleaseResult{}, WrapError(ErrReservationConflict, "release", errors.New("release set is empty"))
	}
	cmd := ReleaseCommand{
		Reservations: descriptors,
		Correlation:  in.Correlation,
		Scope:        in.Scope,
		ReleaseKey: domain.ReleaseKey{
			ReservationKey: in.ReservationKey,
			Sequence:       in.Sequence,
		},
		ReservationKey:   in.ReservationKey,
		ReservationID:    in.ReservationID,
		RuleID:           in.RuleID,
		Kind:             in.Kind,
		Amount:           in.Amount,
		Authority:        in.Authority,
		Stage:            in.Stage,
		BackendAttempted: in.BackendAttempted,
		OutputCommitted:  in.OutputCommitted,
		At:               now,
		SourceKey:        descriptors[0].SourceKey,
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

func releaseInputDescriptors(in ReleaseInput, now time.Time) []ReleaseDescriptor {
	if len(in.Reservations) > 0 {
		out := append([]ReleaseDescriptor(nil), in.Reservations...)
		for i := range out {
			out[i] = completeReleaseDescriptor(in, out[i], now)
		}
		return out
	}
	descriptor := ReleaseDescriptor{Reservation: ReservationDescriptor{
		RuleID:         in.RuleID,
		Unit:           in.Amount.Unit,
		Currency:       in.Amount.Currency,
		Dimensions:     dimensionsFromScope(in.Scope),
		ReservationKey: in.ReservationKey,
		ReservationID:  in.ReservationID,
		Amount:         in.Amount,
	}}
	return []ReleaseDescriptor{completeReleaseDescriptor(in, descriptor, now)}
}

func completeReleaseDescriptor(in ReleaseInput, descriptor ReleaseDescriptor, now time.Time) ReleaseDescriptor {
	if descriptor.Reservation.RuleID == "" {
		descriptor.Reservation.RuleID = in.RuleID
	}
	if descriptor.Reservation.ReservationKey.RuleID == "" {
		descriptor.Reservation.ReservationKey = in.ReservationKey
	}
	if descriptor.Reservation.ReservationID == "" {
		descriptor.Reservation.ReservationID = in.ReservationID
	}
	if descriptor.Reservation.Amount.Unit == "" {
		descriptor.Reservation.Amount = in.Amount
	}
	if descriptor.Reservation.Authority == "" {
		descriptor.Reservation.Authority = in.Authority
	}
	if dimensionsEmpty(descriptor.Reservation.Dimensions) {
		descriptor.Reservation.Dimensions = dimensionsFromScope(in.Scope)
	}
	if descriptor.SourceKey == "" {
		sequence := in.Sequence
		if sequence <= 0 {
			sequence = ReleaseSequence(in.Kind)
		}
		descriptor.Sequence = sequence
		descriptor.SourceKey = sourceEventKey(Evidence{
			At:              now,
			Correlation:     in.Correlation,
			Scope:           in.Scope,
			RuleID:          descriptor.Reservation.RuleID,
			Outcome:         controlplane.AccountingOutcomeReconcile,
			ReasonCode:      policydecision.AccountingReasonReserved,
			ReservationID:   descriptor.Reservation.ReservationID,
			SettlementState: controlplane.AccountingSettlementReleased,
			SourceKind:      string(in.Kind),
			SourceSequence:  sequence,
		})
	}
	if descriptor.Sequence <= 0 {
		descriptor.Sequence = in.Sequence
		if descriptor.Sequence <= 0 {
			descriptor.Sequence = ReleaseSequence(in.Kind)
		}
	}
	if descriptor.Reservation.SourceKey == "" {
		descriptor.Reservation.SourceKey = descriptor.SourceKey
	}
	return descriptor
}

// ApplyUsage applies final usage/cost to matched advisory (or otherwise
// unreserved) accounting windows without creating a reservation (requirement
// 7.7). It is independent of the reservation lifecycle: it runs whether or not
// the request was reserved, updating advisory windows so they accumulate actual
// usage. Idempotent via the store source key. The store records an advisory
// decision row; this method intentionally skips policydecision/control-plane
// projection to keep the advisory path minimal and best-effort.
func (s *Service) ApplyUsage(ctx context.Context, cmd ApplyUsageCommand) (ApplyUsageResult, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ApplyUsageResult{}, WrapError(ErrEvaluationTimeout, "apply_usage", err)
		}
		return ApplyUsageResult{}, err
	}
	if s == nil || s.store == nil {
		return ApplyUsageResult{}, WrapError(ErrUnavailable, "apply_usage", errors.New("store not configured"))
	}
	now := s.now()
	if cmd.At.IsZero() {
		cmd.At = now
	}
	if cmd.Authority == domain.AuthorityLevelUnavailable {
		return ApplyUsageResult{}, nil
	}
	snap := s.snapshotTolerant(ctx)
	if len(snap.Rules) > 0 {
		filtered := make([]string, 0, len(cmd.RuleIDs))
		for _, ruleID := range cmd.RuleIDs {
			rule, ok := ruleByID(snap.Rules, ruleID)
			if !ok || rule.SupportsAuthority(cmd.Authority) || s.allowsEstimatedUnreservedUsage(rule, cmd.Authority) {
				filtered = append(filtered, ruleID)
			}
		}
		cmd.RuleIDs = filtered
	}
	if len(cmd.RuleIDs) == 0 {
		return ApplyUsageResult{}, nil
	}
	// ApplyUsage must use the same attribution normalization as admission and
	// reservation. Otherwise known-empty attribution can miss the reserved row
	// and make the final unreserved fact disappear from accounting.
	cmd.Scope = snap.UnknownAttribution.NormalizeScope(cmd.Scope)
	cmd.Dimensions = snap.UnknownAttribution.NormalizeDimensions(cmd.Dimensions)
	cmd.Scope, cmd.Dimensions = normalizeConfiguredPolicyLabels(snap.UnknownAttribution, cmd.Scope, cmd.Dimensions, snap.Rules)
	result, err := s.store.ApplyUsage(ctx, cmd)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ApplyUsageResult{}, WrapError(ErrEvaluationTimeout, "apply_usage", err)
		}
		return ApplyUsageResult{}, WrapError(ErrUnavailable, "apply_usage", err)
	}
	return result, nil
}

// allowsEstimatedUnreservedUsage keeps fail-open strict rules observable when
// their required authoritative fact is unavailable. Such a rule is already in
// the unreserved set only after admission resolved its unavailable outcome via
// fail-open, so recording the estimated fact cannot create an enforceable
// reservation or silently turn it into authoritative usage.
func (s *Service) allowsEstimatedUnreservedUsage(rule domain.Rule, authority domain.AuthorityLevel) bool {
	if authority == domain.AuthorityLevelUnavailable {
		return false
	}
	if rule.Mode == domain.RuleModeAdvisory {
		return true
	}
	if rule.Mode != domain.RuleModeStrict {
		return false
	}
	if rule.FailureBehavior == domain.FailureBehaviorFailOpen {
		return true
	}
	return rule.FailureBehavior == domain.FailureBehaviorDefault && s != nil && s.defaultFailureBehavior == domain.FailureBehaviorFailOpen
}

func (s *Service) emitSettlementEvidence(ctx context.Context, in SettleInput, result SettleResult, now time.Time, rules []domain.Rule, status domain.AuthorityStatus) (policyAndControlPlane, error) {
	if in.Sequence <= 0 {
		in.Sequence = SettlementSequence(in.Kind, in.Authority)
	}
	outcome, reason, state := settlementProjection(in.Kind, result)
	descriptors := settlementDescriptors(in, now)
	mutations := append([]SettlementMutation(nil), result.Mutations...)
	if len(mutations) == 0 {
		mutations = []SettlementMutation{{
			RuleID:          in.RuleID,
			ReservationID:   result.ReservationID,
			ReleasedDelta:   result.ReleasedDelta,
			OverageDelta:    result.OverageDelta,
			AdjustmentDelta: result.AdjustmentDelta,
		}}
	}
	out := policyAndControlPlane{}
	for _, mutation := range mutations {
		descriptor := settlementDescriptorForMutation(descriptors, mutation)
		usage := descriptor.FinalUsage
		if usage.Unit == "" {
			usage = in.FinalUsage
		}
		reserved := descriptor.Reservation.Amount
		if reserved.Unit == "" {
			reserved = in.ReservedUsage
		}
		authority := descriptor.Authority
		if authority == "" {
			authority = in.Authority
		}
		ruleID := mutation.RuleID
		if ruleID == "" {
			ruleID = descriptor.Reservation.RuleID
		}
		reservationID := mutation.ReservationID
		if reservationID == "" {
			reservationID = descriptor.Reservation.ReservationID
		}
		projection, err := projectAuthorityEvidence(status, true, Evidence{
			At:               now,
			Correlation:      in.Correlation,
			Scope:            in.Scope,
			RuleID:           ruleID,
			MatchedRuleIDs:   settlementRuleIDs(descriptors, ruleID),
			RuleType:         string(selectedRuleKind([]string{ruleID}, rules)),
			Outcome:          outcome,
			ReasonCode:       reason,
			ReservationID:    reservationID,
			SettlementState:  state,
			SourceKind:       string(in.Kind),
			SourceSequence:   in.Sequence,
			Unit:             string(usage.Unit),
			Currency:         usage.Currency,
			Consumed:         usage.Value,
			Reserved:         reserved.Value,
			Adjustment:       mutation.AdjustmentDelta.Value,
			Authority:        authority,
			Stage:            in.Stage,
			BackendAttempted: in.BackendAttempted,
			OutputCommitted:  in.OutputCommitted,
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
		out.PolicyRecords = append(out.PolicyRecords, projection.Policy)
		out.AccountingEvents = append(out.AccountingEvents, projection.Event)
	}
	if len(out.PolicyRecords) > 0 {
		out.Policy = out.PolicyRecords[0]
		out.Event = out.AccountingEvents[0]
	}
	return out, nil
}

func (s *Service) emitReleaseEvidence(ctx context.Context, in ReleaseInput, result ReleaseResult, now time.Time, rules []domain.Rule, status domain.AuthorityStatus) (policyAndControlPlane, error) {
	if in.Sequence <= 0 {
		in.Sequence = ReleaseSequence(in.Kind)
	}
	descriptors := releaseInputDescriptors(in, now)
	mutations := append([]ReleaseMutation(nil), result.Mutations...)
	if len(mutations) == 0 {
		mutations = []ReleaseMutation{{
			RuleID:        in.RuleID,
			ReservationID: result.ReservationID,
			ReleasedDelta: result.ReleasedDelta,
		}}
	}
	out := policyAndControlPlane{}
	for _, mutation := range mutations {
		descriptor := releaseDescriptorForMutation(descriptors, mutation)
		reservation := descriptor.Reservation
		if reservation.Amount.Unit == "" {
			reservation.Amount = in.Amount
		}
		authority := reservation.Authority
		if authority == "" {
			authority = in.Authority
		}
		ruleID := mutation.RuleID
		if ruleID == "" {
			ruleID = reservation.RuleID
		}
		reservationID := mutation.ReservationID
		if reservationID == "" {
			reservationID = reservation.ReservationID
		}
		projection, err := projectAuthorityEvidence(status, true, Evidence{
			At:               now,
			Correlation:      in.Correlation,
			Scope:            in.Scope,
			RuleID:           ruleID,
			MatchedRuleIDs:   releaseRuleIDs(descriptors, ruleID),
			RuleType:         string(selectedRuleKind([]string{ruleID}, rules)),
			Outcome:          controlplane.AccountingOutcomeReconcile,
			ReasonCode:       policydecision.AccountingReasonReserved,
			ReservationID:    reservationID,
			SettlementState:  controlplane.AccountingSettlementReleased,
			SourceKind:       string(in.Kind),
			SourceSequence:   in.Sequence,
			Unit:             string(reservation.Amount.Unit),
			Currency:         reservation.Amount.Currency,
			Reserved:         reservation.Amount.Value,
			Adjustment:       mutation.ReleasedDelta.Value,
			Authority:        authority,
			Stage:            in.Stage,
			BackendAttempted: in.BackendAttempted,
			OutputCommitted:  in.OutputCommitted,
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
		out.PolicyRecords = append(out.PolicyRecords, projection.Policy)
		out.AccountingEvents = append(out.AccountingEvents, projection.Event)
	}
	if len(out.PolicyRecords) > 0 {
		out.Policy = out.PolicyRecords[0]
		out.Event = out.AccountingEvents[0]
	}
	return out, nil
}

func settlementDescriptorForMutation(descriptors []SettlementDescriptor, mutation SettlementMutation) SettlementDescriptor {
	for _, descriptor := range descriptors {
		if descriptor.Reservation.RuleID == mutation.RuleID && (mutation.ReservationID == "" || descriptor.Reservation.ReservationID == mutation.ReservationID) {
			return descriptor
		}
	}
	// Authoritative re-settlement uses a suffixed reservation identity for the
	// store source key, while the mutation result intentionally reports the
	// stable original reservation ID. Rule identity is the safe fallback when
	// those two representations differ.
	for _, descriptor := range descriptors {
		if descriptor.Reservation.RuleID == mutation.RuleID {
			return descriptor
		}
	}
	return SettlementDescriptor{}
}

func releaseDescriptorForMutation(descriptors []ReleaseDescriptor, mutation ReleaseMutation) ReleaseDescriptor {
	for _, descriptor := range descriptors {
		if descriptor.Reservation.RuleID == mutation.RuleID && (mutation.ReservationID == "" || descriptor.Reservation.ReservationID == mutation.ReservationID) {
			return descriptor
		}
	}
	return ReleaseDescriptor{}
}

func settlementRuleIDs(descriptors []SettlementDescriptor, fallback string) []string {
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.Reservation.RuleID != "" {
			ids = append(ids, descriptor.Reservation.RuleID)
		}
	}
	if len(ids) == 0 && fallback != "" {
		return []string{fallback}
	}
	return ids
}

func releaseRuleIDs(descriptors []ReleaseDescriptor, fallback string) []string {
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.Reservation.RuleID != "" {
			ids = append(ids, descriptor.Reservation.RuleID)
		}
	}
	if len(ids) == 0 && fallback != "" {
		return []string{fallback}
	}
	return ids
}
