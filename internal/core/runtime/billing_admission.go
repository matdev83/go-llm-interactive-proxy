package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
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

// billingWorkloadIdentity projects only trusted detached auxiliary lineage
// into the durable billing identity. Primary calls retain the legacy zero
// workload projection; an auxiliary role is accepted only after the detached
// policy has bounded it and the core billing mapper allowlists it.
func billingWorkloadIdentity(ctx context.Context) (billing.WorkloadIdentity, error) {
	return billingWorkloadIdentityForALeg(ctx, diag.ALegID(ctx))
}

func billingWorkloadIdentityForALeg(ctx context.Context, aLegID string) (billing.WorkloadIdentity, error) {
	if ctx == nil {
		return billing.WorkloadIdentity{}, nil
	}
	meta, detached := execctx.DetachedSessionFromContext(ctx)
	sc, _ := scope.ScopeFromContext(ctx)
	if detached && strings.TrimSpace(meta.AuxiliaryRole) != "" {
		fact := metering.Fact{
			Lifecycle: metering.LifecycleAuxiliaryRequest,
			Scope:     sc,
		}
		return coremetering.ProjectWorkloadIdentity(fact, meta.AuxiliaryRole)
	}
	if st := requestAuthorityFrom(ctx); st != nil {
		if workload, ok := st.workloadForALeg(aLegID); ok {
			return workload, nil
		}
		if !st.Workload.IsZero() {
			return st.Workload, nil
		}
	}
	return billing.WorkloadIdentity{}, nil
}

// (e *Executor).billingWorkloadIdentity is the terminal-path accessor. The
// request-authority carrier survives bare Recv contexts and takes precedence;
// direct context extraction remains useful for focused producer tests.
func (e *Executor) billingWorkloadIdentity(ctx context.Context) billing.WorkloadIdentity {
	return e.billingWorkloadIdentityForALeg(ctx, diag.ALegID(ctx))
}

func (e *Executor) billingWorkloadIdentityForALeg(ctx context.Context, aLegID string) billing.WorkloadIdentity {
	identity, err := billingWorkloadIdentityForALeg(ctx, aLegID)
	if err != nil {
		if e != nil && e.Log != nil {
			e.Log.DebugContext(ctx, "billing workload identity unavailable", "error", err)
		}
		return billing.WorkloadIdentity{}
	}
	return identity
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
	accountID := strings.TrimSpace(e.BillingIdentity.AccountID(ctx, *prep.call))
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
		accountID = strings.TrimSpace(e.BillingIdentity.AccountID(ctx, *prep.call))
	}
	pricing := exposure.PricingRef
	policy := exposure.ChargePolicyRef
	if pricing == (billing.VersionRef{}) && e.BillingIdentity.CustomerPricingRef != nil {
		pricing = e.BillingIdentity.CustomerPricingRef(ctx, *prep.call)
	}
	if policy == (billing.VersionRef{}) && e.BillingIdentity.ChargePolicyRef != nil {
		policy = e.BillingIdentity.ChargePolicyRef(ctx, *prep.call)
	}
	if accountID == "" {
		return
	}
	prep.billingExposure = billing.CallExposure{
		AccountID:       accountID,
		PricingRef:      pricing,
		ChargePolicyRef: policy,
	}
	prep.billingIdentityStamped = true
	prep.recvTurnFacts.billingAccountID = accountID
	prep.recvTurnFacts.billingCustomerPricing = pricing
	prep.recvTurnFacts.billingChargePolicy = policy
}

func (e *Executor) billingRoutePlanInput(ctx context.Context, prep *preparedRequest, plan *routePlanState) BillingRoutePlanInput {
	if prep == nil || plan == nil {
		return BillingRoutePlanInput{}
	}
	return BillingRoutePlanInput{
		Call: lipapi.CloneCall(*prep.call), TraceID: prep.identity.traceID, ALegID: prep.identity.aLeg.ALegID, BillingCallID: prep.billingCallID.String(),
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
	est := e.RequestTokenEstimator.EstimateRequestTokens(ctx, *prep.call)
	return routing.RequestSizeEstimate{Available: est.Available, Tokens: est.Input, Basis: est.Basis}
}

func (e *Executor) appendExposureAbortAfterAdmission(ctx context.Context, prep *preparedRequest, _ *routePlanState) {
	if e == nil || prep == nil || e.BillingExposureAdmission == nil {
		return
	}
	e.appendExposureAbortClosure(ctx, prep, strings.TrimSpace(prep.identity.aLeg.ALegID))
}

func (e *Executor) appendExposureAbortClosure(ctx context.Context, prep *preparedRequest, aLegID string) {
	if e == nil || prep == nil || !e.hasTerminalCallSink() || !prep.billingIdentityStamped {
		return
	}
	if err := prep.billingCallID.Validate(); err != nil {
		return
	}
	accountID := strings.TrimSpace(prep.billingExposure.AccountID)
	if accountID == "" {
		return
	}
	now := e.now()
	record := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             prep.billingCallID,
		AccountID:          accountID,
		ALegID:             aLegID,
		SessionID:          strings.TrimSpace(prep.call.Session.AuthoritativeSessionID),
		StartedAt:          now,
		FinishedAt:         now,
		Outcome:            billing.TurnOutcomeFailed,
		CustomerPricingRef: prep.billingExposure.PricingRef,
		ChargePolicyRef:    prep.billingExposure.ChargePolicyRef,
		ExpectedBLegIDs:    prep.billingCallState.freezeAllocatedBLegs(),
		Workload:           e.billingWorkloadIdentityForALeg(ctx, aLegID),
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
