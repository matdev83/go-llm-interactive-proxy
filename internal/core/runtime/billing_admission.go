package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
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
	Call            lipapi.Call
	TraceID         string
	ALegID          string
	BillingCallID   string
	Route           *routing.Selector
	RequestSize     routing.RequestSizeEstimate
	Scope           scope.PrincipalScopeView
	SessionID       string
	MaxOutputTokens *int
	AccountID       string
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

// WireBillingCreditArgs carries bounded facts for the cheap pre-route credit screen (Req 15.6).
type WireBillingCreditArgs struct {
	Scope     scope.PrincipalScopeView
	AccountID string // optional explicit account ID override
}

// WireBillingExposureArgs carries bounded facts for post-quote atomic exposure admission (Req 15.6, 19).
type WireBillingExposureArgs struct {
	BillingCallID   billing.BillingCallID
	TraceID         string
	ALegID          string
	SessionID       string
	Scope           scope.PrincipalScopeView
	AccountID       string
	Route           *routing.Selector
	RequestSize     routing.RequestSizeEstimate
	MaxOutputTokens *int
}

// WireExposureAbortArgs carries bounded facts for recording an exposure abort closure (Req 15.6, 19).
type WireExposureAbortArgs struct {
	BillingCallID   billing.BillingCallID
	Exposure        billing.CallExposure
	ALegID          string
	SessionID       string
	ExpectedBLegIDs []string
	Now             time.Time
}

// WireBillingAssessmentArgs carries facts needed to assess billing compatibility under held permit (Req 15.6, 19).
type WireBillingAssessmentArgs struct {
	Scope scope.PrincipalScopeView
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

func (e *Executor) checkCreditScreen(ctx context.Context, accountID string) error {
	if e == nil || e.BillingCreditGate == nil {
		return nil
	}
	accountID = strings.TrimSpace(accountID)
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

func (e *Executor) checkCheapCredit(ctx context.Context, prep *preparedRequest) error {
	if e == nil || e.BillingCreditGate == nil {
		return nil
	}
	if prep == nil || e.BillingIdentity.AccountID == nil {
		return fmt.Errorf("%w: %w: account identity resolver is required", ErrBillingAdmissionDenied, ErrBillingCreditScreenDenied)
	}
	accountID := strings.TrimSpace(e.BillingIdentity.AccountID(ctx, *prep.call))
	return e.checkCreditScreen(ctx, accountID)
}

// wireBillingAccountID resolves the billing account from bounded wire facts using
// the exact fallback chain shared by the wire credit screen and exposure admission:
// explicit override, WireAccountID contract, WireBounded canonical callback on a zero
// Call under scoped context, then Scope.PrincipalID (Requirements 15.6, 19).
func (e *Executor) wireBillingAccountID(ctx context.Context, explicit string, sc scope.PrincipalScopeView) string {
	if e == nil {
		return strings.TrimSpace(explicit)
	}
	accountID := strings.TrimSpace(explicit)
	if accountID == "" && e.BillingIdentity.WireAccountID != nil {
		accountID = strings.TrimSpace(e.BillingIdentity.WireAccountID(ctx, sc))
	}
	if accountID == "" && e.BillingIdentity.WireBounded && e.BillingIdentity.AccountID != nil {
		scCtx := scope.WithScope(ctx, sc)
		accountID = strings.TrimSpace(e.BillingIdentity.AccountID(scCtx, lipapi.Call{}))
	}
	if accountID == "" && sc.PrincipalID.IsKnown() {
		accountID = strings.TrimSpace(sc.PrincipalID.String())
	}
	return accountID
}

// CheckWireCheapCredit performs the cheap settled-credit screen on bounded wire facts
// without materializing or inspecting a lipapi.Call (Requirements 15.6, 19).
func (e *Executor) CheckWireCheapCredit(ctx context.Context, args WireBillingCreditArgs) error {
	if e == nil || e.BillingCreditGate == nil {
		return nil
	}
	if e.BillingIdentity.HasCustomCallCallbacks() && strings.TrimSpace(args.AccountID) == "" {
		return fmt.Errorf("%w: %w: custom BillingIdentity Call callbacks cannot run on wire path", ErrBillingAdmissionDenied, ErrBillingCreditScreenDenied)
	}
	accountID := e.wireBillingAccountID(ctx, args.AccountID, args.Scope)
	return e.checkCreditScreen(ctx, accountID)
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

func resolveExposureIdentity(exposure billing.CallExposure, fallbackAccountID string, fallbackPricing billing.VersionRef, fallbackPolicy billing.VersionRef) (billing.CallExposure, bool) {
	accountID := strings.TrimSpace(exposure.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(fallbackAccountID)
	}
	pricing := exposure.PricingRef
	if pricing == (billing.VersionRef{}) {
		pricing = fallbackPricing
	}
	policy := exposure.ChargePolicyRef
	if policy == (billing.VersionRef{}) {
		policy = fallbackPolicy
	}
	if accountID == "" {
		return billing.CallExposure{}, false
	}
	return billing.CallExposure{
		AccountID:       accountID,
		PricingRef:      pricing,
		ChargePolicyRef: policy,
	}, true
}

func (e *Executor) stampExposureIdentity(ctx context.Context, prep *preparedRequest, exposure billing.CallExposure) {
	if e == nil || prep == nil {
		return
	}
	fallbackAccount := ""
	if e.BillingIdentity.AccountID != nil {
		fallbackAccount = e.BillingIdentity.AccountID(ctx, *prep.call)
	}
	fallbackPricing := billing.VersionRef{}
	if e.BillingIdentity.CustomerPricingRef != nil {
		fallbackPricing = e.BillingIdentity.CustomerPricingRef(ctx, *prep.call)
	}
	fallbackPolicy := billing.VersionRef{}
	if e.BillingIdentity.ChargePolicyRef != nil {
		fallbackPolicy = e.BillingIdentity.ChargePolicyRef(ctx, *prep.call)
	}
	resolved, ok := resolveExposureIdentity(exposure, fallbackAccount, fallbackPricing, fallbackPolicy)
	if !ok {
		return
	}
	prep.billingExposure = resolved
	prep.billingIdentityStamped = true
	prep.billingAccountID = resolved.AccountID
	prep.billingCustomerPricing = resolved.PricingRef
	prep.billingChargePolicy = resolved.ChargePolicyRef
}

// AuthorizeWireBilling admits operational exposure from bounded wire facts post-quote
// without constructing or inspecting a lipapi.Call (Requirements 15.6, 19).
func (e *Executor) AuthorizeWireBilling(ctx context.Context, args WireBillingExposureArgs) (billing.CallExposure, error) {
	if e == nil {
		return billing.CallExposure{}, nil
	}
	if err := args.BillingCallID.Validate(); err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: billing call identity: %v", ErrBillingAdmissionDenied, err)
	}
	if e.BillingIdentity.HasCustomCallCallbacks() {
		return billing.CallExposure{}, fmt.Errorf("%w: custom BillingIdentity Call callbacks cannot run on wire path", ErrBillingAdmissionDenied)
	}
	if e.BillingExposureAdmission == nil {
		return billing.CallExposure{}, nil
	}

	accountID := e.wireBillingAccountID(ctx, args.AccountID, args.Scope)

	callIDStr := args.BillingCallID.String()
	admitInput := BillingExposureAdmissionInput{
		BillingAdmissionInput: BillingAdmissionInput{
			TraceID:         args.TraceID,
			ALegID:          args.ALegID,
			BillingCallID:   callIDStr,
			Route:           args.Route,
			RequestSize:     args.RequestSize,
			Scope:           args.Scope,
			SessionID:       args.SessionID,
			MaxOutputTokens: args.MaxOutputTokens,
			AccountID:       accountID,
		},
		CallID: callIDStr,
	}

	exposure, err := e.BillingExposureAdmission.Admit(ctx, admitInput)
	if err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: %w", ErrBillingAdmissionDenied, err)
	}

	fallbackPricing := billing.VersionRef{}
	if e.BillingIdentity.WireCustomerPricingRef != nil {
		fallbackPricing = e.BillingIdentity.WireCustomerPricingRef(ctx)
	} else if e.BillingIdentity.WireBounded && e.BillingIdentity.CustomerPricingRef != nil {
		fallbackPricing = e.BillingIdentity.CustomerPricingRef(ctx, lipapi.Call{})
	}

	fallbackPolicy := billing.VersionRef{}
	if e.BillingIdentity.WireChargePolicyRef != nil {
		fallbackPolicy = e.BillingIdentity.WireChargePolicyRef(ctx)
	} else if e.BillingIdentity.WireBounded && e.BillingIdentity.ChargePolicyRef != nil {
		fallbackPolicy = e.BillingIdentity.ChargePolicyRef(ctx, lipapi.Call{})
	}

	stamped, _ := resolveExposureIdentity(exposure, accountID, fallbackPricing, fallbackPolicy)
	return stamped, nil
}

func (e *Executor) billingRoutePlanInput(ctx context.Context, prep *preparedRequest, plan *routePlanState) BillingRoutePlanInput {
	if prep == nil || plan == nil {
		return BillingRoutePlanInput{}
	}
	sessID := ""
	if prep.call != nil {
		sessID = prep.call.Session.AuthoritativeSessionID
	}
	var maxOut *int
	if prep.call != nil && prep.call.Options.MaxOutputTokens != nil {
		v := *prep.call.Options.MaxOutputTokens
		maxOut = &v
	}
	return BillingRoutePlanInput{
		Call:            lipapi.CloneCall(*prep.call),
		TraceID:         prep.identity.traceID,
		ALegID:          prep.identity.aLeg.ALegID,
		BillingCallID:   prep.billingCallID.String(),
		Route:           plan.sel,
		RequestSize:     e.billingRequestSize(ctx, prep, plan),
		SessionID:       sessID,
		MaxOutputTokens: maxOut,
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

// appendAbortClosureRecord is the exact shared helper for recording an exposure abort
// closure to the terminal usage sink, shared between canonical and wire execution (Requirements 15.6, 19).
func (e *Executor) appendAbortClosureRecord(ctx context.Context, args WireExposureAbortArgs) error {
	if e == nil || !e.hasTerminalCallSink() {
		return nil
	}
	if err := args.BillingCallID.Validate(); err != nil {
		return err
	}
	accountID := strings.TrimSpace(args.Exposure.AccountID)
	if accountID == "" {
		return nil
	}
	now := args.Now
	if now.IsZero() {
		now = e.now()
	}
	record := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             args.BillingCallID,
		AccountID:          accountID,
		ALegID:             strings.TrimSpace(args.ALegID),
		SessionID:          strings.TrimSpace(args.SessionID),
		StartedAt:          now,
		FinishedAt:         now,
		Outcome:            billing.TurnOutcomeFailed,
		CustomerPricingRef: args.Exposure.PricingRef,
		ChargePolicyRef:    args.Exposure.ChargePolicyRef,
		ExpectedBLegIDs:    args.ExpectedBLegIDs,
		Workload:           e.billingWorkloadIdentityForALeg(ctx, args.ALegID),
	}
	sealed, err := record.Seal()
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "billing exposure abort closure seal failed", "error", err)
		}
		return err
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	if err := safety.Call(safety.BoundaryStream, "billing_exposure_abort_closure", func() error {
		return e.TerminalUsageSink.AppendCall(persistCtx, sealed)
	}); err != nil {
		e.logBillingUsageAppendFailure(persistCtx, "billing_call_closure_append_critical", "billing exposure abort closure append failed", err)
		return err
	}
	return nil
}

func (e *Executor) appendExposureAbortClosure(ctx context.Context, prep *preparedRequest, aLegID string) {
	if e == nil || prep == nil || !e.hasTerminalCallSink() || !prep.billingIdentityStamped {
		return
	}
	sessID := ""
	if prep.call != nil {
		sessID = prep.call.Session.AuthoritativeSessionID
	}
	var bLegs []string
	if prep.billingCallState != nil {
		bLegs = prep.billingCallState.freezeAllocatedBLegs()
	}
	_ = e.appendAbortClosureRecord(ctx, WireExposureAbortArgs{
		BillingCallID:   prep.billingCallID,
		Exposure:        prep.billingExposure,
		ALegID:          aLegID,
		SessionID:       sessID,
		ExpectedBLegIDs: bLegs,
		Now:             e.now(),
	})
}

// AppendWireExposureAbort records an exposure abort closure for a failed wire attempt
// sharing exact record sealing and sink handoff logic with canonical aborts (Requirements 15.6, 19).
func (e *Executor) AppendWireExposureAbort(ctx context.Context, args WireExposureAbortArgs) error {
	return e.appendAbortClosureRecord(ctx, args)
}

// AssessWireBilling evaluates billing eligibility under the held decode permit (Requirements 15.6, 19).
// It accepts whenever no custom BillingIdentity Call callbacks block the wire path,
// regardless of whether credit/exposure gates are configured (unconfigured gates
// accept the same way on the canonical path).
// If custom BillingIdentity Call callbacks exist without a wire-bounded contract,
// it declines with (AssessmentDecisionDecline, DeclineReasonAuthorityBlocker).
func (e *Executor) AssessWireBilling(
	ctx context.Context,
	args WireBillingAssessmentArgs,
) (largebody.AssessmentDecision, largebody.DeclineReason, error) {
	if e == nil {
		return largebody.AssessmentDecisionAccept, largebody.DeclineReasonNone, nil
	}

	// Custom Call-shaped callbacks are blockers unless certified as wire-bounded (Req 15.6).
	if e.BillingIdentity.HasCustomCallCallbacks() {
		return largebody.AssessmentDecisionDecline, largebody.DeclineReasonAuthorityBlocker,
			fmt.Errorf("%w: custom BillingIdentity Call callbacks cannot run on wire path", ErrBillingAdmissionDenied)
	}

	return largebody.AssessmentDecisionAccept, largebody.DeclineReasonNone, nil
}
