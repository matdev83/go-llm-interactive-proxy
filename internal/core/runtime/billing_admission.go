package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var (
	ErrBillingAdmissionDenied         = errors.New("executor: billing admission denied")
	ErrBillingCreditScreenDenied      = errors.New("executor: cheap credit screen denied")
	ErrBillingCreditScreenUnavailable = errors.New("executor: cheap credit screen unavailable")
)

type BillingCreditGate interface {
	Check(context.Context, string) error
}
type BillingRoutePlanInput struct {
	Call          lipapi.Call
	TraceID       string
	ALegID        string
	BillingCallID string
	Route         *routing.Selector
	RequestSize   routing.RequestSizeEstimate
}
type (
	BillingAdmissionInput    = BillingRoutePlanInput
	BillingExposureAdmission interface {
		Admit(context.Context, BillingExposureAdmissionInput) (billing.CallExposure, error)
	}
)

type BillingExposureAdmissionInput struct {
	BillingAdmissionInput
	CallID string
}

func (e *Executor) checkCheapCredit(ctx context.Context, prep *preparedRequest) error {
	if e == nil {
		return nil
	}
	if e.BillingCreditGate == nil {
		return nil
	}
	if prep == nil || e.BillingIdentity.AccountID == nil {
		return fmt.Errorf("%w: %w: account identity resolver is required", ErrBillingAdmissionDenied, ErrBillingCreditScreenDenied)
	}
	accountID := strings.TrimSpace(e.BillingIdentity.AccountID(ctx, prep.baseline))
	if accountID == "" {
		return fmt.Errorf("%w: %w: account identity is empty", ErrBillingAdmissionDenied, ErrBillingCreditScreenDenied)
	}
	if err := e.BillingCreditGate.Check(ctx, accountID); err != nil {
		class := ErrBillingCreditScreenUnavailable
		if errors.Is(err, billing.ErrCreditScreenDenied) {
			class = ErrBillingCreditScreenDenied
		}
		return fmt.Errorf("%w: %w: %w", ErrBillingAdmissionDenied, class, err)
	}
	return nil
}

func (e *Executor) authorizeBillingOnce(ctx context.Context, prep *preparedRequest, plan *routePlanState) error {
	if e == nil || e.BillingExposureAdmission == nil {
		return nil
	}
	if prep == nil || plan == nil {
		return fmt.Errorf("%w: missing prepared route plan", ErrBillingAdmissionDenied)
	}
	if err := prep.billingCallID.Validate(); err != nil {
		return fmt.Errorf("%w: billing call identity: %v", ErrBillingAdmissionDenied, err)
	}
	exposure, err := e.BillingExposureAdmission.Admit(ctx, BillingExposureAdmissionInput{
		BillingAdmissionInput: e.billingRoutePlanInput(ctx, prep, plan), CallID: prep.billingCallID.String(),
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBillingAdmissionDenied, err)
	}
	e.stampExposureIdentity(ctx, prep, exposure)
	return nil
}

func (e *Executor) stampExposureIdentity(ctx context.Context, prep *preparedRequest, exposure billing.CallExposure) {
	if e == nil || prep == nil {
		return
	}
	accountID := strings.TrimSpace(exposure.AccountID)
	if accountID == "" && e.BillingIdentity.AccountID != nil {
		accountID = strings.TrimSpace(e.BillingIdentity.AccountID(ctx, prep.baseline))
	}
	pricing := exposure.PricingRef
	policy := exposure.ChargePolicyRef
	if pricing == (billing.VersionRef{}) && e.BillingIdentity.CustomerPricingRef != nil {
		pricing = e.BillingIdentity.CustomerPricingRef(ctx, prep.baseline)
	}
	if policy == (billing.VersionRef{}) && e.BillingIdentity.ChargePolicyRef != nil {
		policy = e.BillingIdentity.ChargePolicyRef(ctx, prep.baseline)
	}
	if accountID == "" {
		return
	}
	prep.billingAccountID = accountID
	prep.billingCustomerPricing = pricing
	prep.billingChargePolicy = policy
	prep.billingIdentityStamped = true
}

func (e *Executor) billingRoutePlanInput(ctx context.Context, prep *preparedRequest, plan *routePlanState) BillingRoutePlanInput {
	if prep == nil || plan == nil {
		return BillingRoutePlanInput{}
	}
	return BillingRoutePlanInput{
		Call: lipapi.CloneCall(prep.baseline), TraceID: prep.traceID, ALegID: prep.aLeg.ALegID, BillingCallID: prep.billingCallID.String(),
		Route: plan.sel, RequestSize: e.billingRequestSize(ctx, prep, plan),
	}
}

func (e *Executor) billingRequestSize(ctx context.Context, prep *preparedRequest, plan *routePlanState) routing.RequestSizeEstimate {
	if plan != nil && plan.requestSize.Available {
		return plan.requestSize
	}
	if e == nil || prep == nil || e.RequestTokenEstimator == nil {
		return routing.RequestSizeEstimate{}
	}
	est := e.RequestTokenEstimator.EstimateRequestTokens(ctx, prep.baseline)
	return routing.RequestSizeEstimate{Available: est.Available, Tokens: est.Input, Basis: est.Basis}
}

func (e *Executor) appendExposureAbortAfterAdmission(ctx context.Context, prep *preparedRequest, _ *routePlanState) {
	if e == nil || prep == nil || e.BillingExposureAdmission == nil {
		return
	}
	e.appendExposureAbortClosure(ctx, prep, strings.TrimSpace(prep.aLeg.ALegID))
}

func (e *Executor) appendExposureAbortClosure(ctx context.Context, prep *preparedRequest, aLegID string) {
	if e == nil || prep == nil || !e.hasTerminalCallSink() || !prep.billingIdentityStamped {
		return
	}
	if err := prep.billingCallID.Validate(); err != nil {
		return
	}
	accountID := strings.TrimSpace(prep.billingAccountID)
	if accountID == "" {
		return
	}
	now := e.now()
	record := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             prep.billingCallID,
		AccountID:          accountID,
		ALegID:             aLegID,
		SessionID:          strings.TrimSpace(prep.baseline.Session.AuthoritativeSessionID),
		StartedAt:          now,
		FinishedAt:         now,
		Outcome:            billing.TurnOutcomeFailed,
		CustomerPricingRef: prep.billingCustomerPricing,
		ChargePolicyRef:    prep.billingChargePolicy,
		ExpectedBLegIDs:    prep.billingCallState.freezeAllocatedBLegs(),
	}
	sealed, err := record.Seal()
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "billing exposure abort closure seal failed", "error", err)
		}
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	if err := safety.Call(safety.BoundaryStream, "billing_exposure_abort_closure", func() error {
		return e.TerminalUsageSink.AppendCall(persistCtx, sealed)
	}); err != nil {
		e.logBillingUsageAppendFailure(persistCtx, "billing_call_closure_append_critical", "billing exposure abort closure append failed", err)
	}
}
