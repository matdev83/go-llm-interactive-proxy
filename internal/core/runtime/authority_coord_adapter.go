package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// usageAuthorityProviderAdapter bridges lipsdk RequestProvider / AttemptProvider
// onto usageauthority.Service with Phase 7 lifecycle/exposure forwarding.
type usageAuthorityProviderAdapter struct {
	svc UsageAuthorityService
}

func newUsageAuthorityProviderAdapter(svc UsageAuthorityService) *usageAuthorityProviderAdapter {
	if svc == nil {
		return nil
	}
	return &usageAuthorityProviderAdapter{svc: svc}
}

func (a *usageAuthorityProviderAdapter) AdmitRequest(ctx context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	if a == nil || a.svc == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	key := domain.ReservationKey{
		LogicalRequestID: strings.TrimSpace(in.RequestID),
		ALegID:           strings.TrimSpace(in.ALegID),
		Sequence:         1,
	}
	input := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			RequestID: strings.TrimSpace(in.RequestID),
			TraceID:   strings.TrimSpace(in.TraceID),
			ALegID:    strings.TrimSpace(in.ALegID),
		},
		Scope:          in.Scope,
		Dimensions:     scopeToDimensions(in.Scope),
		Request:        exposureInputTokens(in.Exposure),
		RequestCount:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: key,
		LifecycleScope: metering.LifecycleLogicalRequest,
		Perspective:    in.Perspective,
		Exposure:       in.Exposure,
	}
	res, err := a.svc.Admit(ctx, input)
	if err != nil {
		return authority.Decision{}, err
	}
	return mapAdmissionDecision(res, usageAuthorityRequestProviderID, authority.StageRequestAdmit), nil
}

func (a *usageAuthorityProviderAdapter) SettleRequest(ctx context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	if a == nil || a.svc == nil {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	for _, h := range in.Handles {
		settleIn := authorityapp.SettleInput{
			ReservationID: strings.TrimSpace(h),
			ReservationKey: domain.ReservationKey{
				LogicalRequestID: strings.TrimSpace(in.RequestID),
				Sequence:         1,
			},
			Correlation: controlplane.Correlation{RequestID: strings.TrimSpace(in.RequestID)},
			Kind:        authorityapp.SettlementKindFinal,
			Stage:       string(authority.StageRequestSettle),
			Facts:       in.Facts,
		}
		if len(in.BoundVersions) > 0 {
			settleIn.BoundVersion = in.BoundVersions[0]
		}
		_, err := a.svc.Settle(ctx, settleIn)
		if err != nil {
			return authority.Settlement{}, err
		}
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (a *usageAuthorityProviderAdapter) ReleaseRequest(ctx context.Context, in authority.RequestRelease) error {
	if a == nil || a.svc == nil {
		return nil
	}
	handles := in.Handles
	if len(handles) == 0 && strings.TrimSpace(in.CompensationHandle) != "" {
		handles = []string{in.CompensationHandle}
	}
	for _, h := range handles {
		_, err := a.svc.Release(ctx, authorityapp.ReleaseInput{
			ReservationID: strings.TrimSpace(h),
			ReservationKey: domain.ReservationKey{
				LogicalRequestID: strings.TrimSpace(in.RequestID),
				Sequence:         1,
			},
			Correlation: controlplane.Correlation{RequestID: strings.TrimSpace(in.RequestID)},
			Kind:        authorityapp.ReleaseKindAdmissionFailure,
			Stage:       string(authority.StageRequestRelease),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func attemptAdmissionInput(in authority.AttemptAdmission, estimateOnly bool) authorityapp.AdmissionInput {
	key := domain.ReservationKey{
		LogicalRequestID: strings.TrimSpace(in.RequestID),
		ALegID:           strings.TrimSpace(in.ALegID),
		BLegID:           strings.TrimSpace(in.BLegID),
		AttemptID:        strings.TrimSpace(in.AttemptID),
		Sequence:         1,
	}
	input := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			RequestID: strings.TrimSpace(in.RequestID),
			ALegID:    strings.TrimSpace(in.ALegID),
			BLegID:    strings.TrimSpace(in.BLegID),
		},
		Scope:          in.Scope,
		Dimensions:     scopeToDimensions(in.Scope),
		Request:        exposureInputTokens(in.Exposure),
		RequestCount:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 0}, // request-count reserved at request stage
		Spend:          exposureSpend(in.Exposure),
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: key,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Perspective:    in.Perspective,
		Exposure:       in.Exposure,
		EstimateOnly:   estimateOnly,
	}
	if be := strings.TrimSpace(in.BackendID); be != "" {
		input.Dimensions.Backend = scope.Known(be)
	}
	if m := strings.TrimSpace(in.Model); m != "" {
		input.Dimensions.Model = scope.Known(m)
	}
	return input
}

func (a *usageAuthorityProviderAdapter) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if a == nil || a.svc == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	res, err := a.svc.Admit(ctx, attemptAdmissionInput(in, false))
	if err != nil {
		return authority.Decision{}, err
	}
	return mapAdmissionDecision(res, usageAuthorityAttemptProviderID, authority.StageAttemptAdmit), nil
}

func (a *usageAuthorityProviderAdapter) PreviewAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if a == nil || a.svc == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	// Clamp preview is side-effect free: EstimateOnly skips holds; SkipEvidence
	// skips durable policy/accounting projection (bounded loop may call this
	// up to four times per backend open).
	admitIn := attemptAdmissionInput(in, true)
	admitIn.SkipEvidence = true
	res, err := a.svc.Admit(ctx, admitIn)
	if err != nil {
		return authority.Decision{}, err
	}
	d := mapAdmissionDecision(res, usageAuthorityAttemptProviderID, authority.StageAttemptAdmit)
	d.Reservations = nil
	d.CompensationHandle = ""
	return d, nil
}

func (a *usageAuthorityProviderAdapter) SettleAttempt(ctx context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	if a == nil || a.svc == nil {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	settleIn := attemptSettleInput(in)
	if len(settleIn.Reservations) == 0 && strings.TrimSpace(settleIn.ReservationID) == "" {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	result, err := a.svc.Settle(ctx, authorityapp.DeriveSettleScalars(settleIn))
	if result.Applied {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	if err != nil {
		return authority.Settlement{}, err
	}
	return authority.Settlement{}, nil
}

func attemptSettleInput(in authority.AttemptSettlement) authorityapp.SettleInput {
	kind := authorityapp.SettlementKind(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = settlementKindFromOutcome(in.Outcome, in.OutputCommitted, in.ClientCanceled)
	}
	dims := scopeToDimensions(in.Scope)
	if be := strings.TrimSpace(in.BackendID); be != "" {
		dims.Backend = scope.Known(be)
	}
	if m := strings.TrimSpace(in.Model); m != "" {
		dims.Model = scope.Known(m)
	}
	key := domain.ReservationKey{
		LogicalRequestID: strings.TrimSpace(in.RequestID),
		ALegID:           strings.TrimSpace(in.ALegID),
		BLegID:           strings.TrimSpace(in.BLegID),
		AttemptID:        strings.TrimSpace(in.AttemptID),
		Sequence:         1,
	}
	settleIn := authorityapp.SettleInput{
		Correlation: controlplane.Correlation{
			RequestID: strings.TrimSpace(in.RequestID),
			ALegID:    strings.TrimSpace(in.ALegID),
			BLegID:    strings.TrimSpace(in.BLegID),
		},
		Scope:            in.Scope,
		Kind:             kind,
		Stage:            feature.StageIDAttemptLifecycle,
		BackendAttempted: in.BackendAttempted,
		OutputCommitted:  in.OutputCommitted,
		ClientCanceled:   in.ClientCanceled,
		Facts:            in.Facts,
		ReservationKey:   key,
	}
	if len(in.Facts) > 0 {
		settleIn.FinalUsagePresent = true
		settleIn.Authority = authorityFromFacts(in.Facts)
	}
	if len(in.BoundVersions) > 0 {
		settleIn.BoundVersion = in.BoundVersions[0]
	}
	if len(in.Reservations) > 0 {
		settleIn.Reservations = make([]authorityapp.SettlementDescriptor, 0, len(in.Reservations))
		for _, r := range in.Reservations {
			reserved := authorityAmountFromReservation(r)
			resKey := key
			resKey.RuleID = strings.TrimSpace(r.RuleID)
			finalUsage := finalUsageFromFacts(in.Facts, reserved)
			settleIn.Reservations = append(settleIn.Reservations, authorityapp.SettlementDescriptor{
				Reservation: authorityapp.ReservationDescriptor{
					RuleID:         strings.TrimSpace(r.RuleID),
					Unit:           reserved.Unit,
					Currency:       reserved.Currency,
					Dimensions:     dims,
					ReservationKey: resKey,
					ReservationID:  strings.TrimSpace(r.Handle),
					Amount:         reserved,
				},
				FinalUsage:           finalUsage,
				EstimatedUsage:       reserved,
				Authority:            authorityFromFacts(in.Facts),
				MeasurementAuthority: authorityapp.MeasurementAuthority{Usage: authorityFromFacts(in.Facts)},
			})
		}
		return settleIn
	}
	for _, h := range in.Handles {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if settleIn.ReservationID == "" {
			settleIn.ReservationID = h
		}
		settleIn.Reservations = append(settleIn.Reservations, authorityapp.SettlementDescriptor{
			Reservation: authorityapp.ReservationDescriptor{
				ReservationID:  h,
				ReservationKey: settleIn.ReservationKey,
				Dimensions:     dims,
			},
			FinalUsage: finalUsageFromFacts(in.Facts, domain.Amount{}),
			Authority:  authorityFromFacts(in.Facts),
		})
	}
	return settleIn
}

func settlementKindFromOutcome(outcome metering.AttemptOutcome, outputCommitted, clientCanceled bool) authorityapp.SettlementKind {
	switch outcome {
	case metering.AttemptOutcomeCanceled:
		return authorityapp.SettlementKindCancellation
	case metering.AttemptOutcomeLoser:
		return authorityapp.SettlementKindLosing
	case metering.AttemptOutcomeFailed:
		return authorityapp.SettlementKindUnavailable
	case metering.AttemptOutcomeWinner:
		return authorityapp.SettlementKindFinal
	default:
		if clientCanceled {
			return authorityapp.SettlementKindCancellation
		}
		if outputCommitted {
			return authorityapp.SettlementKindFinal
		}
		return authorityapp.SettlementKindFinal
	}
}

func finalUsageFromFacts(facts []metering.Fact, reserved domain.Amount) domain.Amount {
	unit := reserved.Unit
	for _, fact := range facts {
		for _, q := range fact.Quantities {
			if !q.Present {
				continue
			}
			got := amountFromMeteringQuantity(q)
			if unit == "" || got.Unit == unit {
				return got
			}
		}
	}
	if reserved.Unit != "" {
		return domain.Amount{Unit: reserved.Unit, Currency: reserved.Currency}
	}
	return domain.Amount{}
}

func authorityFromFacts(facts []metering.Fact) domain.AuthorityLevel {
	for _, fact := range facts {
		switch fact.Authority {
		case metering.AuthorityAuthoritative:
			return domain.AuthorityLevelAuthoritative
		case metering.AuthorityUnavailable:
			return domain.AuthorityLevelUnavailable
		}
	}
	return domain.AuthorityLevelEstimated
}

func amountFromMeteringQuantity(q metering.Quantity) domain.Amount {
	switch q.Component {
	case metering.ComponentRequest:
		return domain.Amount{Unit: domain.AmountUnitRequests, Value: q.Value}
	case metering.ComponentInputToken:
		return domain.Amount{Unit: domain.AmountUnitInputTokens, Value: q.Value}
	case metering.ComponentOutputToken:
		return domain.Amount{Unit: domain.AmountUnitOutputTokens, Value: q.Value}
	case metering.ComponentCacheReadInputToken:
		return domain.Amount{Unit: domain.AmountUnitCacheReadTokens, Value: q.Value}
	case metering.ComponentCacheWriteInputToken:
		return domain.Amount{Unit: domain.AmountUnitCacheWriteTokens, Value: q.Value}
	case metering.ComponentReasoningOutputToken:
		return domain.Amount{Unit: domain.AmountUnitReasoningTokens, Value: q.Value}
	case metering.ComponentTotalToken:
		return domain.Amount{Unit: domain.AmountUnitTotalTokens, Value: q.Value}
	default:
		return domain.Amount{Value: q.Value}
	}
}

func (a *usageAuthorityProviderAdapter) ReleaseAttempt(ctx context.Context, in authority.AttemptRelease) error {
	if a == nil || a.svc == nil {
		return nil
	}
	handles := in.Handles
	if len(handles) == 0 && strings.TrimSpace(in.CompensationHandle) != "" {
		handles = []string{in.CompensationHandle}
	}
	for _, h := range handles {
		_, err := a.svc.Release(ctx, authorityapp.ReleaseInput{
			ReservationID: strings.TrimSpace(h),
			ReservationKey: domain.ReservationKey{
				LogicalRequestID: strings.TrimSpace(in.RequestID),
				BLegID:           strings.TrimSpace(in.BLegID),
				AttemptID:        strings.TrimSpace(in.AttemptID),
				Sequence:         1,
			},
			Correlation: controlplane.Correlation{RequestID: in.RequestID, BLegID: in.BLegID},
			Kind:        authorityapp.ReleaseKindAdmissionFailure,
			Stage:       string(authority.StageAttemptRelease),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func mapAdmissionDecision(res authorityapp.AdmissionResult, providerID string, stage authority.Stage) authority.Decision {
	d := authority.Decision{ProviderID: providerID, Stage: stage}
	if !res.Allowed {
		d.Kind = authority.DecisionDeny
		if res.BoundVersion.Version != "" {
			d.BoundVersions = []economics.PolicySnapshotRef{res.BoundVersion}
		}
		return d
	}
	d.Kind = authority.DecisionAllow
	if res.BoundVersion.Version != "" {
		d.BoundVersions = []economics.PolicySnapshotRef{res.BoundVersion}
	}
	reservations := append([]authorityapp.AdmissionReservation(nil), res.Reservations...)
	if len(reservations) == 0 && res.Reserved && strings.TrimSpace(res.ReservationID) != "" {
		reservations = append(reservations, authorityapp.AdmissionReservation{
			ReservationID:  res.ReservationID,
			RuleID:         res.SelectedRuleID,
			ReservedAmount: res.ReservedAmount,
		})
	}
	seen := make(map[string]struct{}, len(reservations))
	for _, r := range reservations {
		if strings.TrimSpace(r.ReservationID) == "" {
			continue
		}
		if _, ok := seen[r.ReservationID]; ok {
			continue
		}
		seen[r.ReservationID] = struct{}{}
		d.Reservations = append(d.Reservations, mapAdmissionReservation(r))
		if d.CompensationHandle == "" {
			d.CompensationHandle = r.ReservationID
		}
	}
	// Monetary admission clamps are retired from the runtime authority adapter;
	// BillingAdmission owns the customer max-charge authorization boundary.
	return d
}

func mapAdmissionReservation(in authorityapp.AdmissionReservation) authority.Reservation {
	out := authority.Reservation{
		Handle: strings.TrimSpace(in.ReservationID),
		Kind:   authority.ReservationQuota,
		RuleID: strings.TrimSpace(in.RuleID),
	}
	amount := in.ReservedAmount
	if amount.Unit == domain.AmountUnitMoneyNano {
		out.Kind = authority.ReservationBudget
		out.Money = &economics.Money{NanoUnits: amount.Value, Currency: strings.TrimSpace(amount.Currency), Present: true}
		return out
	}
	component, unit := meteringComponentForAuthorityAmount(amount.Unit)
	if component != "" {
		out.Quantity = &metering.Quantity{Component: component, Unit: unit, Value: amount.Value, Present: true}
	}
	return out
}

func meteringComponentForAuthorityAmount(unit domain.AmountUnit) (string, string) {
	switch unit {
	case domain.AmountUnitRequests:
		return metering.ComponentRequest, metering.UnitCount
	case domain.AmountUnitInputTokens:
		return metering.ComponentInputToken, metering.UnitToken
	case domain.AmountUnitOutputTokens:
		return metering.ComponentOutputToken, metering.UnitToken
	case domain.AmountUnitCacheReadTokens:
		return metering.ComponentCacheReadInputToken, metering.UnitToken
	case domain.AmountUnitCacheWriteTokens:
		return metering.ComponentCacheWriteInputToken, metering.UnitToken
	case domain.AmountUnitReasoningTokens:
		return metering.ComponentReasoningOutputToken, metering.UnitToken
	case domain.AmountUnitTotalTokens:
		return metering.ComponentTotalToken, metering.UnitToken
	default:
		return "", ""
	}
}

func scopeToDimensions(sc scope.PrincipalScopeView) domain.Dimensions {
	return domain.Dimensions{
		Principal:    sc.PrincipalID,
		Credential:   sc.CredentialID,
		Tenant:       sc.TenantID,
		Organization: sc.OrganizationID,
		Workspace:    sc.WorkspaceID,
		Project:      sc.ProjectID,
		Department:   sc.DepartmentID,
		CostCenter:   sc.CostCenterID,
	}
}

func exposureInputTokens(exp economics.ExposureBasis) domain.Amount {
	for _, q := range exp.Quantities {
		if q.Component == metering.ComponentInputToken && q.Present {
			return domain.Amount{Unit: domain.AmountUnitInputTokens, Value: q.Value}
		}
	}
	return domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 0}
}

func exposureSpend(exp economics.ExposureBasis) domain.Amount {
	if exp.Money.Present {
		return domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: exp.Money.NanoUnits}
	}
	return domain.Amount{}
}

// BuildAuthorityCoordinators wires thin adapters into request/attempt coordinators
// when a usage-authority service and/or concurrency provider is present.
func BuildAuthorityCoordinators(svc UsageAuthorityService, concurrency authority.ConcurrencyProvider) (*authoritycoord.RequestCoordinator, *authoritycoord.AttemptCoordinator) {
	adapter := newUsageAuthorityProviderAdapter(svc)
	if adapter == nil && concurrency == nil {
		return nil, nil
	}
	req := &authoritycoord.RequestCoordinator{
		Concurrency: concurrency,
	}
	if adapter != nil {
		req.Slots = []authoritycoord.RequestSlot{{
			ID:       usageAuthorityRequestProviderID,
			Class:    authoritycoord.PriorityQuotaBudgetRate,
			Provider: adapter,
			Strength: authority.StrengthRequired,
		}}
	}
	var att *authoritycoord.AttemptCoordinator
	if adapter != nil {
		att = &authoritycoord.AttemptCoordinator{
			Slots: []authoritycoord.AttemptSlot{{
				ID:       usageAuthorityAttemptProviderID,
				Class:    authoritycoord.AttemptPriorityHardSpend,
				Provider: adapter,
				Strength: authority.StrengthRequired,
			}},
		}
	}
	return req, att
}
