package runtime

import (
	"context"
	"errors"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type attemptAuthorityState struct {
	admissionInput  authorityapp.AdmissionInput
	admissionResult authorityapp.AdmissionResult
}

func (e *Executor) authorityService() UsageAuthorityService {
	if e == nil {
		return nil
	}
	return e.UsageAuthority
}

func (e *Executor) admitAttemptAuthority(
	ctx context.Context,
	traceID string,
	aLegID string,
	bleg b2bua.BLegRecord,
	call lipapi.Call,
	c routing.AttemptCandidate,
	decision accountingpreflight.Decision,
	estimateOnly bool,
) (attemptAuthorityState, error) {
	svc := e.authorityService()
	if svc == nil {
		return attemptAuthorityState{}, nil
	}
	admissionInput := authorityapp.AdmissionInput{
		Correlation:    attemptAuthorityCorrelation(traceID, call.ID, aLegID, bleg, c),
		Scope:          scopeFromCtx(ctx),
		Dimensions:     attemptAuthorityDimensions(ctx, call, c),
		Request:        attemptAuthorityRequestAmount(decision),
		RequestCount:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		PreflightUsage: attemptAuthorityPreflightUsage(decision),
		Spend:          attemptAuthoritySpendAmount(e.AccountingPriceCatalog, c, decision),
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: attemptAuthorityReservationKey(call.ID, traceID, aLegID, bleg, c),
		EstimateOnly:   estimateOnly,
	}
	result, err := svc.Admit(ctx, admissionInput)
	if err != nil {
		outcome := domain.DecisionOutcomeUnavailable
		if errors.Is(err, authorityapp.ErrReservationConflict) {
			outcome = domain.DecisionOutcomeDeny
		}
		return attemptAuthorityState{}, attemptAuthorityAdmissionError(authorityapp.AdmissionResult{Outcome: outcome}, err)
	}
	if result.ReservedAmount.Unit != "" {
		admissionInput.Request = result.ReservedAmount
	}
	state := attemptAuthorityState{
		admissionInput:  admissionInput,
		admissionResult: result,
	}
	if authErr := attemptAuthorityAdmissionError(result, nil); authErr != nil {
		return state, authErr
	}
	return state, nil
}

func attemptAuthorityCorrelation(traceID, requestID, aLegID string, bleg b2bua.BLegRecord, c routing.AttemptCandidate) controlplane.Correlation {
	reqID := strings.TrimSpace(requestID)
	if reqID == "" {
		reqID = strings.TrimSpace(traceID)
	}
	return controlplane.Correlation{
		TraceID:    strings.TrimSpace(traceID),
		RequestID:  reqID,
		ALegID:     strings.TrimSpace(aLegID),
		BLegID:     strings.TrimSpace(bleg.BLegID),
		AttemptSeq: bleg.Seq,
		BackendID:  strings.TrimSpace(c.Primary.Backend),
		Model:      strings.TrimSpace(c.Primary.Model),
	}
}

func attemptAuthorityDimensions(ctx context.Context, call lipapi.Call, c routing.AttemptCandidate) domain.Dimensions {
	sc := scopeFromCtx(ctx)
	return domain.Dimensions{
		Principal:    sc.PrincipalID,
		Tenant:       sc.TenantID,
		Organization: sc.OrganizationID,
		Workspace:    sc.WorkspaceID,
		Project:      sc.ProjectID,
		Department:   sc.DepartmentID,
		CostCenter:   sc.CostCenterID,
		Backend:      scope.Known(strings.TrimSpace(c.Primary.Backend)),
		Model:        scope.Known(strings.TrimSpace(c.Primary.Model)),
		Route:        scope.Known(strings.TrimSpace(call.Route.Selector)),
	}
}

func attemptAuthorityRequestAmount(decision accountingpreflight.Decision) domain.Amount {
	return domain.Amount{Unit: domain.AmountUnitInputTokens, Value: int64(decision.Count.InputTokens)}
}

func attemptAuthorityPreflightUsage(decision accountingpreflight.Decision) domain.PreflightUsage {
	count := decision.Count
	output := int64(count.OutputTokens)
	if output < 0 {
		output = 0
	}
	return domain.PreflightUsage{
		InputTokens:      int64(count.InputTokens),
		OutputTokens:     output,
		CacheReadTokens:  int64(count.CacheReadTokens),
		CacheWriteTokens: int64(count.CacheWriteTokens),
		ReasoningTokens:  int64(count.ReasoningTokens),
		TotalTokens:      int64(count.TotalTokens),
	}
}

func attemptAuthoritySpendAmount(catalog accounting.PriceCatalog, c routing.AttemptCandidate, decision accountingpreflight.Decision) domain.Amount {
	outputTokens := decision.Count.OutputTokens
	if outputTokens < 0 {
		outputTokens = 0
	}
	usage := accounting.TokenUsage{
		InputTokens:  int64(decision.Count.InputTokens),
		OutputTokens: int64(outputTokens),
	}
	cost := accounting.EstimateCost(accounting.CostInput{
		Backend: strings.TrimSpace(c.Primary.Backend),
		Model:   strings.TrimSpace(c.Primary.Model),
		Usage:   usage,
	}, catalog)
	if cost.Unavailable {
		return domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 0, Currency: "unknown"}
	}
	return domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: cost.NanoUnits, Currency: cost.Currency}
}

func attemptAuthorityUsageAmount(ev lipapi.Event, estimate domain.Amount) domain.Amount {
	var amount domain.Amount
	switch estimate.Unit {
	case domain.AmountUnitRequests:
		return domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}
	case domain.AmountUnitInputTokens:
		amount = domain.Amount{Unit: domain.AmountUnitInputTokens, Value: int64(ev.InputTokens)}
	case domain.AmountUnitOutputTokens:
		amount = domain.Amount{Unit: domain.AmountUnitOutputTokens, Value: int64(ev.OutputTokens)}
	case domain.AmountUnitCacheReadTokens:
		amount = domain.Amount{Unit: domain.AmountUnitCacheReadTokens, Value: int64(ev.CacheReadTokens)}
	case domain.AmountUnitCacheWriteTokens:
		amount = domain.Amount{Unit: domain.AmountUnitCacheWriteTokens, Value: int64(ev.CacheWriteTokens)}
	case domain.AmountUnitReasoningTokens:
		amount = domain.Amount{Unit: domain.AmountUnitReasoningTokens, Value: int64(ev.ReasoningTokens)}
	case domain.AmountUnitMoneyNano:
		currency := strings.TrimSpace(ev.Currency)
		if currency == "" {
			currency = strings.TrimSpace(estimate.Currency)
		}
		amount = domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: ev.CostNanoUnits, Currency: currency}
	case domain.AmountUnitTotalTokens:
		value := int64(ev.TotalTokens)
		if value == 0 {
			value = int64(ev.InputTokens + ev.OutputTokens + ev.CacheReadTokens + ev.CacheWriteTokens + ev.ReasoningTokens)
		}
		amount = domain.Amount{Unit: domain.AmountUnitTotalTokens, Value: value}
	default:
		value := int64(ev.TotalTokens)
		if value == 0 {
			value = int64(ev.InputTokens + ev.OutputTokens + ev.CacheReadTokens + ev.CacheWriteTokens + ev.ReasoningTokens)
		}
		if estimate.Unit == "" {
			amount = domain.Amount{Unit: domain.AmountUnitTotalTokens, Value: value}
		} else {
			amount = domain.Amount{Unit: estimate.Unit, Value: value}
		}
	}
	if amount.Value == 0 && !attemptAuthorityEventHasUsageForUnit(ev, estimate.Unit) {
		return domain.Amount{
			Unit:     estimate.Unit,
			Value:    estimate.Value,
			Currency: estimate.Currency,
		}
	}
	return amount
}

func attemptAuthorityEventHasUsageForUnit(ev lipapi.Event, unit domain.AmountUnit) bool {
	switch unit {
	case domain.AmountUnitRequests:
		return true
	case domain.AmountUnitInputTokens:
		if ev.InputTokens > 0 {
			return true
		}
	case domain.AmountUnitOutputTokens:
		if ev.OutputTokens > 0 {
			return true
		}
	case domain.AmountUnitCacheReadTokens:
		if ev.CacheReadTokens > 0 {
			return true
		}
	case domain.AmountUnitCacheWriteTokens:
		if ev.CacheWriteTokens > 0 {
			return true
		}
	case domain.AmountUnitReasoningTokens:
		if ev.ReasoningTokens > 0 {
			return true
		}
	case domain.AmountUnitMoneyNano:
		if ev.CostNanoUnits > 0 {
			return true
		}
	case domain.AmountUnitTotalTokens:
		if ev.TotalTokens > 0 {
			return true
		}
	default:
		if ev.TotalTokens > 0 {
			return true
		}
	}
	for _, scope := range ev.UsageScopes {
		switch unit {
		case domain.AmountUnitInputTokens:
			if scope.InputTokens > 0 {
				return true
			}
		case domain.AmountUnitOutputTokens:
			if scope.OutputTokens > 0 {
				return true
			}
		case domain.AmountUnitCacheReadTokens:
			if scope.CacheReadTokens > 0 {
				return true
			}
		case domain.AmountUnitCacheWriteTokens:
			if scope.CacheWriteTokens > 0 {
				return true
			}
		case domain.AmountUnitReasoningTokens:
			if scope.ReasoningTokens > 0 {
				return true
			}
		case domain.AmountUnitTotalTokens:
			if scope.TotalTokens > 0 {
				return true
			}
		default:
			if scope.TotalTokens > 0 {
				return true
			}
		}
	}
	return false
}

func attemptAuthorityCostAmount(ev lipapi.Event, fallbackCurrency string) domain.Amount {
	currency := strings.TrimSpace(ev.Currency)
	if currency == "" {
		currency = strings.TrimSpace(fallbackCurrency)
	}
	return domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: ev.CostNanoUnits, Currency: currency}
}

func attemptAuthorityReservationKey(requestID, traceID, aLegID string, bleg b2bua.BLegRecord, c routing.AttemptCandidate) domain.ReservationKey {
	reqID := strings.TrimSpace(requestID)
	if reqID == "" {
		reqID = strings.TrimSpace(traceID)
	}
	return domain.ReservationKey{
		LogicalRequestID: reqID,
		ALegID:           strings.TrimSpace(aLegID),
		BLegID:           strings.TrimSpace(bleg.BLegID),
		AttemptID:        strings.TrimSpace(bleg.BLegID),
		RuleID:           strings.TrimSpace(c.Key),
		Sequence:         1,
	}
}

func attemptAuthorityRuleID(state attemptAuthorityState) string {
	if len(state.admissionResult.RuleIDs) > 0 {
		return state.admissionResult.RuleIDs[0]
	}
	return state.admissionInput.ReservationKey.RuleID
}

func attemptAuthorityAdmissionError(result authorityapp.AdmissionResult, err error) error {
	reasonCode := "usage_authority_denied"
	if result.PolicyRecord.ReasonCode != "" {
		reasonCode = result.PolicyRecord.ReasonCode
	}
	switch result.Outcome {
	case domain.DecisionOutcomeUnavailable, domain.DecisionOutcomeError:
		return lipapi.NewPolicyFailureError("usage_authority_admission", "", reasonCode, "accounting_authority", "usage authority unavailable", err)
	case domain.DecisionOutcomeDeny:
		return lipapi.NewPolicyDeniedError("usage_authority_admission", "", reasonCode, "accounting_authority", "request denied by usage authority", err)
	default:
		if !result.Allowed {
			return lipapi.NewPolicyFailureError("usage_authority_admission", "", reasonCode, "accounting_authority", "usage authority unavailable", err)
		}
		return nil
	}
}
