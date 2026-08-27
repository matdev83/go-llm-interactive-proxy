package billing

import "context"

// TerminalUsageSink is the single runtime terminal handoff. Implementations
// durably append immutable current call/leg records locally; they do not rate,
// authorize, or write financial journals.
//
// The context passed to AppendLeg/AppendCall carries the persistence deadline
// for the append and must not be derived from the caller's request context.
// Implementations must return success only after the record is durably
// committed (or, for replay-safe stores, provably already present with an
// identical fingerprint); a returned error means the record must be retried
// and never silently dropped. The same key may be appended more than once:
// replay must be idempotent and conflicting fingerprints must surface as a
// typed conflict rather than overwriting prior evidence.
type TerminalUsageSink interface {
	AppendLeg(context.Context, CallLegUsageRecord) error
	AppendCall(context.Context, CallUsageRecord) error
}

type ExposureAdmissionStore interface {
	AdmitExposure(context.Context, AdmitExposureInput) (CallExposure, error)
}

// ExposureStore is an alias for ExposureAdmissionStore to align with canonical store naming.
type ExposureStore = ExposureAdmissionStore
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
