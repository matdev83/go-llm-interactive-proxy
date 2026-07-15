package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"math/bits"
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
	if !estimateOnly && e != nil && e.AttemptCoordinator != nil && e.authorityService() != nil {
		return e.admitAttemptViaCoordinator(ctx, traceID, aLegID, bleg, call, c, decision)
	}
	svc := e.authorityService()
	if svc == nil {
		return attemptAuthorityState{}, nil
	}
	admissionInput := authorityapp.AdmissionInput{
		Correlation:    attemptAuthorityCorrelation(traceID, call.ID, aLegID, call, bleg, c),
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
			Quantities: []metering.Quantity{{
				Component: metering.ComponentInputToken,
				Unit:      metering.UnitToken,
				Value:     int64(decision.Count.InputTokens),
				Present:   true,
			}},
		},
	}
	if holder := meteringHolderFrom(ctx); holder != nil {
		if be := holder.BackendIngressFor(bleg.BLegID); be != nil {
			in.Exposure.Quantities = append([]metering.Quantity(nil), be.Public.Quantities...)
		}
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
	handles := d.Stack.Handles()
	res := authorityapp.AdmissionResult{Allowed: true, Outcome: domain.DecisionOutcomeAllow}
	if len(handles) > 0 {
		res.Reserved = true
		res.ReservationID = handles[0]
		for _, h := range handles {
			res.Reservations = append(res.Reservations, authorityapp.AdmissionReservation{ReservationID: h})
		}
	}
	admissionInput := authorityapp.AdmissionInput{
		Correlation:    attemptAuthorityCorrelation(traceID, call.ID, aLegID, call, bleg, c),
		Scope:          scopeFromCtx(ctx),
		Dimensions:     attemptAuthorityDimensions(ctx, call, c),
		Request:        attemptAuthorityRequestAmount(decision),
		RequestCount:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		PreflightUsage: attemptAuthorityPreflightUsage(decision),
		Spend:          attemptAuthoritySpendAmount(e.AccountingPriceCatalog, c, decision),
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: attemptAuthorityReservationKey(call.ID, traceID, aLegID, bleg, c),
	}
	return attemptAuthorityState{
		admissionInput:  admissionInput,
		admissionResult: res,
		cleanupTimeout:  e.UsageAuthorityCleanupTimeout,
	}, nil
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
	// Use a 128-bit intermediate so a large money cap cannot overflow before
	// conversion to output tokens. The quotient is capped before conversion
	// to int because call options use the platform's native int width.
	productHigh, productLow := bits.Mul64(uint64(remainingNano), 1_000_000)
	if productHigh >= uint64(sample.NanoUnits) {
		return int64(^uint64(0) >> 1), authorityClampApplied
	}
	quotient, _ := bits.Div64(productHigh, productLow, uint64(sample.NanoUnits))
	maxInt64 := uint64(^uint64(0) >> 1)
	if quotient > maxInt64 {
		quotient = maxInt64
	}
	maxInt := uint64(^uint(0) >> 1)
	if quotient > maxInt {
		quotient = maxInt
	}
	return int64(quotient), authorityClampApplied
}

// applyAuthorityClamp mutates the call's requested max output so the backend
// receives the clamped exposure (requirement 6.5). When the price is
// unavailable, the rule's cost-unavailable behavior applies: fail-open
// proceeds without clamping (the clamp intent was already recorded in
// evidence), fail-closed denies before protected work (requirement 5.5).
func (e *Executor) applyAuthorityClamp(call *lipapi.Call, c routing.AttemptCandidate, clamp *authorityapp.AdmissionClamp, inputTokens ...int64) error {
	if clamp == nil {
		return nil
	}
	maxOutput, outcome := authorityClampMaxOutputTokens(e.AccountingPriceCatalog, c, clamp.EffectiveMax.Value, inputTokens...)
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
