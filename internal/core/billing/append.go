package billing

import "context"

type CallLegUsageAppender interface {
	AppendCallLegUsage(context.Context, CallLegUsageRecord) error
}
type CallUsageAppender interface {
	AppendCallUsage(context.Context, CallUsageRecord) error
}
type ExposureAdmissionStore interface {
	AdmitExposure(context.Context, AdmitExposureInput) (CallExposure, error)
}
type CallUsageStore interface {
	CompleteCallClaimer
	ClaimCompleteCalls(context.Context, int) ([]CompleteCall, error)
	GetCallExposure(context.Context, BillingCallID) (CallExposure, error)
	RetryCompleteCall(context.Context, BillingCallID, string) error
}
type CallUsageReader interface {
	ListCallUsage(context.Context, string) ([]CallUsageRecord, error)
}
type CallLegUsageReader interface {
	ListCallLegUsage(context.Context, BillingCallID) ([]CallLegUsageRecord, error)
}
type ProviderCostResolver interface {
	ResolveProviderCost(context.Context, CallLegUsageRecord) (OperatorCostResult, error)
}
type CallSettlementStore interface {
	ApplyCallBillingResult(context.Context, ApplyCallBillingInput) (CallSettlement, error)
}
type CompleteCallClaimer interface {
	ClaimCompleteCall(context.Context, BillingCallID) (CompleteCall, error)
}
type CallLegUsageAppenderFunc func(context.Context, CallLegUsageRecord) error

func (f CallLegUsageAppenderFunc) AppendCallLegUsage(ctx context.Context, record CallLegUsageRecord) error {
	if f == nil {
		return nil
	}
	return f(ctx, record)
}

type CallUsageAppenderFunc func(context.Context, CallUsageRecord) error

func (f CallUsageAppenderFunc) AppendCallUsage(ctx context.Context, record CallUsageRecord) error {
	if f == nil {
		return nil
	}
	return f(ctx, record)
}
