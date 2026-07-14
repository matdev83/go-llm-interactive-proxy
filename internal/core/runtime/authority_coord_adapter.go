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
	return mapAdmissionDecision(res, "request-ua", authority.StageRequestAdmit), nil
}

func (a *usageAuthorityProviderAdapter) SettleRequest(ctx context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	if a == nil || a.svc == nil {
		return authority.Settlement{Kind: authority.SettlementFinal}, nil
	}
	for _, h := range in.Handles {
		_, err := a.svc.Settle(ctx, authorityapp.SettleInput{
			ReservationID: strings.TrimSpace(h),
			ReservationKey: domain.ReservationKey{
				LogicalRequestID: strings.TrimSpace(in.RequestID),
				Sequence:         1,
			},
			Correlation: controlplane.Correlation{RequestID: strings.TrimSpace(in.RequestID)},
			Kind:        authorityapp.SettlementKindFinal,
			Stage:       string(authority.StageRequestSettle),
		})
		if err != nil {
			return authority.Settlement{}, err
		}
	}
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
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

func (a *usageAuthorityProviderAdapter) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if a == nil || a.svc == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
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
	}
	if be := strings.TrimSpace(in.BackendID); be != "" {
		input.Dimensions.Backend = scope.Known(be)
	}
	if m := strings.TrimSpace(in.Model); m != "" {
		input.Dimensions.Model = scope.Known(m)
	}
	res, err := a.svc.Admit(ctx, input)
	if err != nil {
		return authority.Decision{}, err
	}
	return mapAdmissionDecision(res, "attempt-ua", authority.StageAttemptAdmit), nil
}

func (a *usageAuthorityProviderAdapter) SettleAttempt(ctx context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	if a == nil || a.svc == nil {
		return authority.Settlement{Kind: authority.SettlementFinal}, nil
	}
	for _, h := range in.Handles {
		_, err := a.svc.Settle(ctx, authorityapp.SettleInput{
			ReservationID: strings.TrimSpace(h),
			ReservationKey: domain.ReservationKey{
				LogicalRequestID: strings.TrimSpace(in.RequestID),
				BLegID:           strings.TrimSpace(in.BLegID),
				AttemptID:        strings.TrimSpace(in.AttemptID),
				Sequence:         1,
			},
			Correlation:      controlplane.Correlation{RequestID: in.RequestID, BLegID: in.BLegID},
			Kind:             authorityapp.SettlementKindFinal,
			Stage:            string(authority.StageAttemptSettle),
			BackendAttempted: true,
		})
		if err != nil {
			return authority.Settlement{}, err
		}
	}
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
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
		return d
	}
	d.Kind = authority.DecisionAllow
	if res.Reserved && strings.TrimSpace(res.ReservationID) != "" {
		d.Reservations = append(d.Reservations, authority.Reservation{
			Handle: res.ReservationID,
			Kind:   authority.ReservationQuota,
			RuleID: res.SelectedRuleID,
		})
		d.CompensationHandle = res.ReservationID
	}
	for _, r := range res.Reservations {
		if strings.TrimSpace(r.ReservationID) == "" {
			continue
		}
		d.Reservations = append(d.Reservations, authority.Reservation{
			Handle: r.ReservationID,
			Kind:   authority.ReservationQuota,
			RuleID: r.RuleID,
		})
	}
	if res.Clamp != nil && res.Clamp.EffectiveMax.Unit == domain.AmountUnitMoneyNano {
		d.Clamps = append(d.Clamps, authority.Clamp{
			Kind:   authority.ClampMaxSpend,
			RuleID: res.Clamp.RuleID,
		})
	}
	return d
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
// when a usage-authority service is present (Phase 6). Concurrency remains nil until Phase 8.
func BuildAuthorityCoordinators(svc UsageAuthorityService) (*authoritycoord.RequestCoordinator, *authoritycoord.AttemptCoordinator) {
	return buildDefaultCoordinators(svc)
}

// buildDefaultCoordinators wires thin adapters into request/attempt coordinators.
func buildDefaultCoordinators(svc UsageAuthorityService) (*authoritycoord.RequestCoordinator, *authoritycoord.AttemptCoordinator) {
	adapter := newUsageAuthorityProviderAdapter(svc)
	if adapter == nil {
		return nil, nil
	}
	req := &authoritycoord.RequestCoordinator{
		Concurrency: nil, // Phase 8
		Slots: []authoritycoord.RequestSlot{{
			ID:       "usage-authority-request",
			Class:    authoritycoord.PriorityQuotaBudgetRate,
			Provider: adapter,
			Strength: authority.StrengthRequired,
		}},
	}
	att := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID:       "usage-authority-attempt",
			Class:    authoritycoord.AttemptPriorityHardSpend,
			Provider: adapter,
			Strength: authority.StrengthRequired,
		}},
	}
	return req, att
}
