package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type attemptAuthorityState struct {
	admissionInput  authorityapp.AdmissionInput
	admissionResult authorityapp.AdmissionResult
	cleanupTimeout  time.Duration
	// viaCoordinator is set when admit ran through AttemptCoordinator so settle/
	// release must preserve stack provider identity (not flatten to UA-only handles).
	viaCoordinator bool
	stack          authoritycoord.CompensationStack
	boundVersions  []economics.PolicySnapshotRef
	admitClamps    []authority.Clamp
	requestID      string
	attemptID      string
	bLegID         string
	// settledProviders tracks AttemptCoordinator / built-in UA providers that
	// already completed Settle successfully so retries skip them and Release
	// never runs after a successful Settle (requirement 15.5).
	settledProviders map[string]struct{}
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
	// External AttemptProviders must run even when built-in UsageAuthority is disabled.
	if !estimateOnly && e != nil && e.AttemptCoordinator != nil {
		return e.admitAttemptViaCoordinator(ctx, traceID, aLegID, bleg, call, c, decision)
	}
	svc := e.authorityService()
	if svc == nil {
		return attemptAuthorityState{}, nil
	}
	quantities := attemptRatingQuantities(decision)
	factIDs := []string(nil)
	if !estimateOnly {
		quantities = finalOperatorAttemptQuantities(ctx, bleg.BLegID, decision)
		if holder := meteringHolderFrom(ctx); holder != nil {
			if id := strings.TrimSpace(holder.BackendIngressFactID(bleg.BLegID)); id != "" {
				factIDs = []string{id}
			}
		}
	}
	spend, rated, rateErr := e.rateOperatorAttemptSpend(ctx, c, decision, quantities, factIDs...)
	if rateErr != nil {
		if errors.Is(rateErr, context.Canceled) {
			return attemptAuthorityState{}, rateErr
		}
		return attemptAuthorityState{}, attemptAuthorityAdmissionError(
			authorityapp.AdmissionResult{Outcome: domain.DecisionOutcomeUnavailable},
			rateErr,
		)
	}
	admissionInput := authorityapp.AdmissionInput{
		Correlation:    attemptAuthorityCorrelation(traceID, call.ID, aLegID, call, bleg, c),
		Scope:          scopeFromCtx(ctx),
		Dimensions:     attemptAuthorityDimensions(ctx, call, c),
		Request:        attemptAuthorityRequestAmount(decision),
		RequestCount:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		PreflightUsage: attemptAuthorityPreflightUsage(decision),
		Spend:          spend,
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: attemptAuthorityReservationKey(call.ID, traceID, aLegID, bleg, c),
		EstimateOnly:   estimateOnly,
	}
	if rated.Money.Present || len(quantities) > 0 {
		admissionInput.Exposure = economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Quantities:  quantities,
			Money:       rated.Money,
			Output:      conservativeOutputAssumption(decision, quantities),
		}
	}
	// When request coordinator already reserved request-count, avoid double-charging
	// customer request quotas on each B-leg (requirements 4.5, 8.3).
	if requestAuthorityFrom(ctx) != nil {
		admissionInput.RequestCount = domain.Amount{Unit: domain.AmountUnitRequests, Value: 0}
	}
	if !estimateOnly {
		admissionInput.LifecycleScope = metering.LifecycleBackendAttempt
	}
	result, err := svc.Admit(ctx, admissionInput)
	if err != nil {
		// Cancellation must not be converted into an unrelated accounting denial
		// (requirement 10.4): propagate the canceled error verbatim so the
		// runtime can distinguish it from enforcement outcomes.
		if errors.Is(err, context.Canceled) {
			return attemptAuthorityState{}, err
		}
		outcome := domain.DecisionOutcomeUnavailable
		if errors.Is(err, authorityapp.ErrReservationConflict) {
			outcome = domain.DecisionOutcomeDeny
		}
		return attemptAuthorityState{}, attemptAuthorityAdmissionError(authorityapp.AdmissionResult{Outcome: outcome}, err)
	}
	if result.ReservedAmount.Unit != "" {
		admissionInput.Request = result.ReservedAmount
	}
	e.applyGenerationBoundVersion(&result)
	bindAdmissionRatingVersion(&result, rated)
	state := attemptAuthorityState{
		admissionInput:  admissionInput,
		admissionResult: result,
		cleanupTimeout:  e.UsageAuthorityCleanupTimeout,
	}
	if authErr := attemptAuthorityAdmissionError(result, nil); authErr != nil {
		return state, authErr
	}
	return state, nil
}

func (e *Executor) admitAttemptViaCoordinator(
	ctx context.Context,
	traceID string,
	aLegID string,
	bleg b2bua.BLegRecord,
	call lipapi.Call,
	c routing.AttemptCandidate,
	decision accountingpreflight.Decision,
) (attemptAuthorityState, error) {
	quantities := finalOperatorAttemptQuantities(ctx, bleg.BLegID, decision)
	factIDs := []string(nil)
	if holder := meteringHolderFrom(ctx); holder != nil {
		if id := strings.TrimSpace(holder.BackendIngressFactID(bleg.BLegID)); id != "" {
			factIDs = []string{id}
		}
	}
	spend, rated, rateErr := e.rateOperatorAttemptSpend(ctx, c, decision, quantities, factIDs...)
	if rateErr != nil {
		if errors.Is(rateErr, context.Canceled) {
			return attemptAuthorityState{}, rateErr
		}
		return attemptAuthorityState{}, attemptAuthorityAdmissionError(
			authorityapp.AdmissionResult{Outcome: domain.DecisionOutcomeUnavailable},
			rateErr,
		)
	}
	in := authority.AttemptAdmission{
		RequestID:      strings.TrimSpace(call.ID),
		AttemptID:      strings.TrimSpace(bleg.BLegID),
		BLegID:         strings.TrimSpace(bleg.BLegID),
		ALegID:         strings.TrimSpace(aLegID),
		BackendID:      strings.TrimSpace(c.Primary.Backend),
		Model:          strings.TrimSpace(c.Primary.Model),
		Perspective:    metering.PerspectiveOperator,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Scope:          scopeFromCtx(ctx),
		IdempotencyKey: attemptAuthorityReservationKey(call.ID, traceID, aLegID, bleg, c).String(),
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Quantities:  quantities,
			Money:       rated.Money,
			Output:      conservativeOutputAssumption(decision, quantities),
		},
	}
	if rated.Money.Present {
		in.RatingVersions = []economics.RatingSnapshotRef{ratingSnapshotRef(rated)}
	}
	d, err := e.AttemptCoordinator.Admit(ctx, in)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return attemptAuthorityState{}, err
		}
		outcome := domain.DecisionOutcomeDeny
		if !authoritycoord.IsDenied(err) {
			outcome = domain.DecisionOutcomeUnavailable
		}
		return attemptAuthorityState{}, attemptAuthorityAdmissionError(authorityapp.AdmissionResult{Outcome: outcome}, err)
	}
	entries := d.Stack.Entries()
	handles := d.Stack.Handles()
	res := authorityapp.AdmissionResult{Allowed: true, Outcome: domain.DecisionOutcomeAllow}
	if len(handles) > 0 {
		res.Reserved = true
		res.ReservationID = handles[0]
		for _, entry := range entries {
			if strings.TrimSpace(entry.ProviderID) != usageAuthorityAttemptProviderID {
				continue
			}
			reservation := authorityapp.AdmissionReservation{
				ReservationID:  strings.TrimSpace(entry.Handle),
				RuleID:         strings.TrimSpace(entry.Reservation.RuleID),
				ReservedAmount: authorityAmountFromReservation(entry.Reservation),
			}
			res.Reservations = append(res.Reservations, reservation)
			if len(res.Reservations) == 1 {
				res.ReservationID = reservation.ReservationID
				res.ReservedAmount = reservation.ReservedAmount
				res.SelectedRuleID = reservation.RuleID
			}
			if reservation.RuleID != "" {
				res.RuleIDs = append(res.RuleIDs, reservation.RuleID)
			}
		}
	}
	if len(d.BoundVersions) > 0 {
		res.BoundVersion = d.BoundVersions[0]
	}
	e.applyGenerationBoundVersion(&res)
	bindAdmissionRatingVersion(&res, rated)
	admissionInput := authorityapp.AdmissionInput{
		Correlation:    attemptAuthorityCorrelation(traceID, call.ID, aLegID, call, bleg, c),
		Scope:          scopeFromCtx(ctx),
		Dimensions:     attemptAuthorityDimensions(ctx, call, c),
		Request:        attemptAuthorityRequestAmount(decision),
		RequestCount:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		PreflightUsage: attemptAuthorityPreflightUsage(decision),
		Spend:          spend,
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: attemptAuthorityReservationKey(call.ID, traceID, aLegID, bleg, c),
		Exposure:       in.Exposure,
		Perspective:    metering.PerspectiveOperator,
		LifecycleScope: metering.LifecycleBackendAttempt,
	}
	return attemptAuthorityState{
		admissionInput:  admissionInput,
		admissionResult: res,
		cleanupTimeout:  e.UsageAuthorityCleanupTimeout,
		viaCoordinator:  true,
		stack:           d.Stack,
		boundVersions:   append([]economics.PolicySnapshotRef(nil), d.BoundVersions...),
		admitClamps:     append([]authority.Clamp(nil), d.Clamps...),
		requestID:       in.RequestID,
		attemptID:       in.AttemptID,
		bLegID:          in.BLegID,
	}, nil
}

func authorityAmountFromReservation(in authority.Reservation) domain.Amount {
	if in.Money != nil && in.Money.Present {
		return domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: in.Money.NanoUnits, Currency: strings.TrimSpace(in.Money.Currency)}
	}
	if in.Quantity == nil || !in.Quantity.Present {
		return domain.Amount{}
	}
	var unit domain.AmountUnit
	switch in.Quantity.Component {
	case metering.ComponentRequest:
		unit = domain.AmountUnitRequests
	case metering.ComponentInputToken:
		unit = domain.AmountUnitInputTokens
	case metering.ComponentOutputToken:
		unit = domain.AmountUnitOutputTokens
	case metering.ComponentCacheReadInputToken:
		unit = domain.AmountUnitCacheReadTokens
	case metering.ComponentCacheWriteInputToken:
		unit = domain.AmountUnitCacheWriteTokens
	case metering.ComponentReasoningOutputToken:
		unit = domain.AmountUnitReasoningTokens
	case metering.ComponentTotalToken:
		unit = domain.AmountUnitTotalTokens
	default:
		return domain.Amount{}
	}
	return domain.Amount{Unit: unit, Value: in.Quantity.Value}
}

func attemptAuthorityCorrelation(traceID, requestID, aLegID string, call lipapi.Call, bleg b2bua.BLegRecord, c routing.AttemptCandidate) controlplane.Correlation {
	reqID := strings.TrimSpace(requestID)
	if reqID == "" {
		reqID = strings.TrimSpace(traceID)
	}
	return controlplane.Correlation{
		TraceID:    strings.TrimSpace(traceID),
		RequestID:  reqID,
		SessionID:  strings.TrimSpace(call.Session.CorrelationID()),
		ALegID:     strings.TrimSpace(aLegID),
		BLegID:     strings.TrimSpace(bleg.BLegID),
		AttemptSeq: bleg.Seq,
		BackendID:  strings.TrimSpace(c.Primary.Backend),
		Model:      strings.TrimSpace(c.Primary.Model),
	}
}

func attemptAuthorityDimensions(ctx context.Context, call lipapi.Call, c routing.AttemptCandidate) domain.Dimensions {
	sc := scopeFromCtx(ctx)
	dims := domain.Dimensions{
		Principal:    sc.PrincipalID,
		Credential:   sc.CredentialID,
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
	for k, v := range sc.PolicyLabels {
		if !domain.IsSafeLabelKey(k) {
			continue
		}
		if dims.PolicyLabels == nil {
			dims.PolicyLabels = make(map[string]scope.Value, len(sc.PolicyLabels))
		}
		dims.PolicyLabels[k] = scope.Known(v)
	}
	return dims
}

func attemptAuthorityRequestAmount(decision accountingpreflight.Decision) domain.Amount {
	return domain.Amount{Unit: domain.AmountUnitInputTokens, Value: int64(decision.Count.InputTokens)}
}

func attemptAuthorityPreflightUsage(decision accountingpreflight.Decision) domain.PreflightUsage {
	count := decision.Count
	output := max(int64(count.OutputTokens), 0)
	return domain.PreflightUsage{
		InputTokens:        int64(count.InputTokens),
		OutputTokens:       output,
		CacheReadTokens:    int64(count.CacheReadTokens),
		CacheWriteTokens:   int64(count.CacheWriteTokens),
		ReasoningTokens:    int64(count.ReasoningTokens),
		TotalTokens:        int64(count.TotalTokens),
		TotalTokensPresent: count.TotalTokensPresent,
	}
}

func attemptAuthoritySpendAmount(catalog accounting.PriceCatalog, c routing.AttemptCandidate, decision accountingpreflight.Decision) domain.Amount {
	outputTokens := max(int64(decision.Count.OutputTokens), 0)
	if outputTokens == 0 && decision.AdjustedMaxOutputTokens != nil && *decision.AdjustedMaxOutputTokens > 0 {
		outputTokens = int64(*decision.AdjustedMaxOutputTokens)
	}
	usage := accounting.TokenUsage{
		InputTokens:  int64(decision.Count.InputTokens),
		OutputTokens: outputTokens,
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

// authorityClampMaxOutputTokens converts a clamp EffectiveMax (money nano) into
// the maximum output token count the backend should receive. The optional input
// token count is charged first, using the same catalog as admission spend
// estimation; only the remaining money may be allocated to output tokens. A
// missing price is unavailable, while input cost above the cap is deterministic
// exhaustion and must never be converted to fail-open behavior.
type authorityClampOutcome uint8

const (
	authorityClampApplied authorityClampOutcome = iota
	authorityClampPricingUnavailable
	authorityClampCapacityExhausted
)

func authorityClampMaxOutputTokens(catalog accounting.PriceCatalog, c routing.AttemptCandidate, effectiveMaxNano int64, inputTokens ...int64) (int64, authorityClampOutcome) {
	if effectiveMaxNano < 0 {
		effectiveMaxNano = 0
	}
	input := int64(0)
	if len(inputTokens) > 0 && inputTokens[0] > 0 {
		input = inputTokens[0]
	}
	fixed := accounting.EstimateCost(accounting.CostInput{
		Backend: strings.TrimSpace(c.Primary.Backend),
		Model:   strings.TrimSpace(c.Primary.Model),
		Usage:   accounting.TokenUsage{InputTokens: input},
	}, catalog)
	if fixed.Unavailable {
		return 0, authorityClampPricingUnavailable
	}
	if fixed.NanoUnits > effectiveMaxNano {
		return 0, authorityClampCapacityExhausted
	}
	remainingNano := effectiveMaxNano - fixed.NanoUnits
	if remainingNano == 0 {
		// No remaining spend for any output tokens: treat as deterministic
		// exhaustion. A zero MaxOutputTokens clamp is omitted by several
		// backends and would otherwise open with the provider default allowance.
		return 0, authorityClampCapacityExhausted
	}
	sample := accounting.EstimateCost(accounting.CostInput{
		Backend: strings.TrimSpace(c.Primary.Backend),
		Model:   strings.TrimSpace(c.Primary.Model),
		Usage:   accounting.TokenUsage{OutputTokens: 1_000_000},
	}, catalog)
	if sample.Unavailable || sample.NanoUnits <= 0 {
		return 0, authorityClampPricingUnavailable
	}
	tokens, err := economics.TokensFromMoneyPer1M(remainingNano, sample.NanoUnits, economics.RoundingTowardZero)
	if err != nil {
		return 0, authorityClampPricingUnavailable
	}
	maxInt := int64(^uint(0) >> 1)
	if tokens > maxInt {
		tokens = maxInt
	}
	return tokens, authorityClampApplied
}

// applyAuthorityClamp mutates the call's requested max output so the backend
// receives the clamped exposure (requirement 6.5). When the price is
// unavailable, the rule's cost-unavailable behavior applies: fail-open
// proceeds without clamping (the clamp intent was already recorded in
// evidence), fail-closed denies before protected work (requirement 5.5).
//
// When EconomicsRater is injected, money→token conversion uses the public
// OutputLimitQuoter contract exclusively — AccountingPriceCatalog must not
// silently substitute (requirements 6.1–6.5, 12.1).
func (e *Executor) applyAuthorityClamp(ctx context.Context, call *lipapi.Call, c routing.AttemptCandidate, clamp *authorityapp.AdmissionClamp, inputTokens ...int64) error {
	if clamp == nil {
		return nil
	}
	var maxOutput int64
	var outcome authorityClampOutcome
	if e != nil && e.EconomicsRater != nil {
		maxOutput, outcome = e.authorityClampViaEconomics(ctx, c, clamp, inputTokens...)
	} else {
		catalog := accounting.PriceCatalog{}
		if e != nil {
			catalog = e.AccountingPriceCatalog
		}
		maxOutput, outcome = authorityClampMaxOutputTokens(catalog, c, clamp.EffectiveMax.Value, inputTokens...)
	}
	switch outcome {
	case authorityClampCapacityExhausted:
		return lipapi.NewPolicyDeniedError("usage_authority_admission", "", "budget_exceeded", "accounting_authority", "spend cap exhausted by fixed input cost", nil)
	case authorityClampPricingUnavailable:
		if clamp.FailureBehavior == domain.FailureBehaviorFailOpen {
			return nil
		}
		return lipapi.NewPolicyDeniedError("usage_authority_admission", "", "unavailable", "accounting_authority", "spend cap clamp unavailable: price missing", nil)
	}
	if maxOutput < 0 {
		maxOutput = 0
	}
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens >= 0 && int64(*call.Options.MaxOutputTokens) < maxOutput {
		maxOutput = int64(*call.Options.MaxOutputTokens)
	}
	adjusted := int(maxOutput)
	call.Options.MaxOutputTokens = &adjusted
	return nil
}

func (e *Executor) authorityClampViaEconomics(ctx context.Context, c routing.AttemptCandidate, clamp *authorityapp.AdmissionClamp, inputTokens ...int64) (int64, authorityClampOutcome) {
	if e == nil || e.EconomicsRater == nil || clamp == nil {
		return 0, authorityClampPricingUnavailable
	}
	quoter, ok := e.EconomicsRater.(economics.OutputLimitQuoter)
	if !ok {
		return 0, authorityClampPricingUnavailable
	}
	input := int64(0)
	if len(inputTokens) > 0 && inputTokens[0] > 0 {
		input = inputTokens[0]
	}
	at := e.now()
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := quoter.QuoteOutputLimit(ctx, economics.OutputLimitRequest{
		Perspective: metering.PerspectiveOperator,
		BackendID:   strings.TrimSpace(c.Primary.Backend),
		Model:       strings.TrimSpace(c.Primary.Model),
		FixedQuantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     input,
			Present:   true,
		}},
		MaxMoney: economics.Money{
			NanoUnits: clamp.EffectiveMax.Value,
			Currency:  strings.TrimSpace(clamp.EffectiveMax.Currency),
			Present:   true,
		},
		At: at,
	})
	if err != nil {
		return 0, authorityClampPricingUnavailable
	}
	switch res.Status {
	case economics.OutputLimitOK:
		if res.MaxOutputTokens < 0 {
			return 0, authorityClampPricingUnavailable
		}
		if res.MaxOutputTokens == 0 {
			// Zero remaining output budget must deny; several backends omit a
			// zero MaxOutputTokens and would otherwise open with defaults.
			return 0, authorityClampCapacityExhausted
		}
		return res.MaxOutputTokens, authorityClampApplied
	case economics.OutputLimitCapacityExhausted:
		return 0, authorityClampCapacityExhausted
	case economics.OutputLimitUnsupported, economics.OutputLimitOverflow:
		return 0, authorityClampPricingUnavailable
	default:
		return 0, authorityClampPricingUnavailable
	}
}

// authorityClampIgnoreUnsupportedGenParamsExt is the extension key used by
// codex-client-compat to drop unsupported generation parameters. Core must not
// import the Codex plugin; the string is the stable wire contract.
const authorityClampIgnoreUnsupportedGenParamsExt = "openai_codex.ignore_unsupported_gen_params"

// backendCanEnforceAuthorityClamp reports whether the selected backend can
// represent a MaxOutputTokens authority clamp on the wire. Enforcement is an
// explicit execbackend.Backend port contract (EnforcesMaxOutputTokens); the
// zero value is fail-closed so unknown adapters never accept a spend clamp
// they cannot bind. The codex-client-compat ignore extension also drops the
// limit, so an otherwise-capable backend still fails closed when it is set.
func backendCanEnforceAuthorityClamp(be execbackend.Backend, call *lipapi.Call) bool {
	if call == nil || call.Options.MaxOutputTokens == nil {
		return true
	}
	if !be.EnforcesMaxOutputTokens {
		return false
	}
	if ignore, ok := callExtensionBool(call, authorityClampIgnoreUnsupportedGenParamsExt); ok && ignore {
		return false
	}
	return true
}

func callExtensionBool(call *lipapi.Call, key string) (bool, bool) {
	if call == nil || len(call.Extensions) == 0 {
		return false, false
	}
	raw, ok := call.Extensions[key]
	if !ok {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
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
		if value == 0 && !attemptAuthorityEventHasUsageForUnit(ev, domain.AmountUnitTotalTokens) {
			// Default inclusion schema: cache ⊂ input, reasoning ⊂ output.
			value = int64(ev.InputTokens + ev.OutputTokens)
		}
		amount = domain.Amount{Unit: domain.AmountUnitTotalTokens, Value: value}
	default:
		value := int64(ev.TotalTokens)
		if value == 0 && !attemptAuthorityEventHasUsageForUnit(ev, domain.AmountUnitTotalTokens) {
			value = int64(ev.InputTokens + ev.OutputTokens)
		}
		if estimate.Unit == "" {
			amount = domain.Amount{Unit: domain.AmountUnitTotalTokens, Value: value}
		} else {
			amount = domain.Amount{Unit: estimate.Unit, Value: value}
		}
	}
	// A zero amount is only reconciled to the preflight estimate when usage is
	// genuinely absent (no scoped usage delta was reported) or when usage was
	// reported for other units but not this one (partial reporting). A present
	// usage delta whose scoped readings are all zero is a legitimate zero-usage
	// or zero-cost completion and must settle at zero, not the reserved estimate.
	if amount.Value == 0 && !attemptAuthorityEventHasUsageForUnit(ev, estimate.Unit) {
		switch estimate.Unit {
		case domain.AmountUnitMoneyNano:
			if len(ev.UsageScopes) == 0 && !ev.CostPresent {
				return domain.Amount{
					Unit:     estimate.Unit,
					Value:    estimate.Value,
					Currency: estimate.Currency,
				}
			}
		default:
			if len(ev.UsageScopes) == 0 || attemptAuthorityEventHasAnyUsage(ev) {
				return domain.Amount{
					Unit:     estimate.Unit,
					Value:    estimate.Value,
					Currency: estimate.Currency,
				}
			}
		}
	}
	return amount
}

func attemptAuthorityEventHasUsageForUnit(ev lipapi.Event, unit domain.AmountUnit) bool {
	if present, known := explicitUsagePresenceForUnit(ev, unit); known {
		return present
	}
	// Preserve legacy all-zero authoritative events when presence is unmarked.
	if ev.Kind == lipapi.EventUsageDelta && !attemptAuthorityEventHasAnyUsage(ev) {
		if authoritativeProviderAccounting(ev.Accounting) {
			return unit != domain.AmountUnitMoneyNano
		}
		for _, usageScope := range ev.UsageScopes {
			if authoritativeProviderAccounting(usageScope.Accounting) {
				return unit != domain.AmountUnitMoneyNano
			}
		}
	}
	if unit == domain.AmountUnitRequests {
		return true
	}
	if usageCounterValue(unit, int64(ev.InputTokens), int64(ev.OutputTokens), int64(ev.CacheReadTokens), int64(ev.CacheWriteTokens), int64(ev.ReasoningTokens), int64(ev.TotalTokens), ev.CostNanoUnits) > 0 {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if usageCounterValue(unit, int64(scope.InputTokens), int64(scope.OutputTokens), int64(scope.CacheReadTokens), int64(scope.CacheWriteTokens), int64(scope.ReasoningTokens), int64(scope.TotalTokens), 0) > 0 {
			return true
		}
	}
	return false
}

func usageCounterValue(unit domain.AmountUnit, input, output, cacheRead, cacheWrite, reasoning, total, cost int64) int64 {
	switch unit {
	case domain.AmountUnitInputTokens:
		return input
	case domain.AmountUnitOutputTokens:
		return output
	case domain.AmountUnitCacheReadTokens:
		return cacheRead
	case domain.AmountUnitCacheWriteTokens:
		return cacheWrite
	case domain.AmountUnitReasoningTokens:
		return reasoning
	case domain.AmountUnitMoneyNano:
		return cost
	case domain.AmountUnitTotalTokens:
		return total
	default:
		return total
	}
}

func explicitUsagePresenceForUnit(ev lipapi.Event, unit domain.AmountUnit) (bool, bool) {
	if ev.Kind != lipapi.EventUsageDelta {
		return false, false
	}
	if unit == domain.AmountUnitMoneyNano {
		// Monetary presence is event-level and independent of token UsagePresence.
		return ev.CostPresent, true
	}
	presence := ev.UsagePresence
	for _, scope := range ev.UsageScopes {
		presence = presence.Union(scope.UsagePresence)
	}
	if !presence.Any() {
		return false, false
	}
	switch unit {
	case domain.AmountUnitInputTokens:
		return presence.InputTokens, true
	case domain.AmountUnitOutputTokens:
		return presence.OutputTokens, true
	case domain.AmountUnitCacheReadTokens:
		return presence.CacheReadTokens, true
	case domain.AmountUnitCacheWriteTokens:
		return presence.CacheWriteTokens, true
	case domain.AmountUnitReasoningTokens:
		return presence.ReasoningTokens, true
	case domain.AmountUnitTotalTokens:
		return presence.TotalTokens, true
	default:
		return false, false
	}
}

// attemptAuthorityEventHasAnyUsage reports whether any legacy counter is non-zero.
func attemptAuthorityEventHasAnyUsage(ev lipapi.Event) bool {
	if ev.InputTokens > 0 || ev.OutputTokens > 0 || ev.CacheReadTokens > 0 ||
		ev.CacheWriteTokens > 0 || ev.ReasoningTokens > 0 || ev.TotalTokens > 0 ||
		ev.CostNanoUnits > 0 {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if scope.InputTokens > 0 || scope.OutputTokens > 0 || scope.CacheReadTokens > 0 ||
			scope.CacheWriteTokens > 0 || scope.ReasoningTokens > 0 || scope.TotalTokens > 0 {
			return true
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

// applyGenerationBoundVersion prefers the published runtime generation's usage
// snapshot identity when SnapshotGeneration is wired (design: Publication).
func (e *Executor) applyGenerationBoundVersion(res *authorityapp.AdmissionResult) {
	if e == nil || res == nil || e.SnapshotGeneration == nil {
		return
	}
	gen := e.SnapshotGeneration.Current()
	if gen == nil {
		return
	}
	if strings.TrimSpace(gen.Usage.Version) != "" {
		policyID := strings.TrimSpace(gen.Usage.ID)
		if policyID == "" {
			policyID = string(economics.PolicyKindUsageAuthority)
		}
		res.BoundVersion = gen.Usage.PolicyRef(policyID)
	}
	if strings.TrimSpace(gen.Rating.Version) != "" {
		raterID := strings.TrimSpace(gen.Rating.ID)
		if raterID == "" {
			raterID = "rating"
		}
		res.BoundRatingVersion = gen.Rating.RatingRef(raterID)
	}
}

// mergeGenerationBoundVersions appends Current() usage/concurrency refs onto a
// request-stage composite decision when SnapshotGeneration is wired.
func (e *Executor) mergeGenerationBoundVersions(d *authoritycoord.CompositeDecision) {
	if e == nil || d == nil || e.SnapshotGeneration == nil {
		return
	}
	gen := e.SnapshotGeneration.Current()
	if gen == nil {
		return
	}
	if strings.TrimSpace(gen.Usage.Version) != "" {
		policyID := strings.TrimSpace(gen.Usage.ID)
		if policyID == "" {
			policyID = string(economics.PolicyKindUsageAuthority)
		}
		ref := gen.Usage.PolicyRef(policyID)
		d.BoundVersions = prependPolicyRef(d.BoundVersions, ref)
	}
	if strings.TrimSpace(gen.Concurrency.Version) != "" {
		policyID := strings.TrimSpace(gen.Concurrency.ID)
		if policyID == "" {
			policyID = string(economics.PolicyKindConcurrency)
		}
		ref := gen.Concurrency.PolicyRef(policyID)
		d.BoundVersions = prependPolicyRef(d.BoundVersions, ref)
	}
}

func prependPolicyRef(existing []economics.PolicySnapshotRef, ref economics.PolicySnapshotRef) []economics.PolicySnapshotRef {
	out := make([]economics.PolicySnapshotRef, 0, len(existing)+1)
	out = append(out, ref)
	for _, cur := range existing {
		if cur.Version == ref.Version && cur.ID == ref.ID && cur.PolicyID == ref.PolicyID {
			continue
		}
		out = append(out, cur)
	}
	return out
}
