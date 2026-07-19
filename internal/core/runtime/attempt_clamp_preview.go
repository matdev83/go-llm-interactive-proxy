package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// maxAttemptClampPreviewIterations bounds the strictly-narrowing clamp preview
// loop (adapter PreviewAttempt comment; design Clamp Preview / V-15).
const maxAttemptClampPreviewIterations = 4

// previewAndApplyAttemptClamps runs side-effect-free clamp preview after final
// openCall assembly and before freeze/backend ingress. When AttemptCoordinator
// is nil but UsageAuthority is present, preview still runs via the adapter.
func (e *Executor) previewAndApplyAttemptClamps(
	ctx context.Context,
	traceID string,
	aLegID string,
	bleg b2bua.BLegRecord,
	openCall *lipapi.Call,
	c routing.AttemptCandidate,
	decision accountingpreflight.Decision,
) error {
	if e == nil || openCall == nil {
		return nil
	}
	if e.AttemptCoordinator == nil && e.authorityService() == nil {
		return nil
	}

	inputTokens := int64(decision.Count.InputTokens)
	for range maxAttemptClampPreviewIterations {
		in, err := e.buildAttemptAdmissionForPreview(ctx, traceID, aLegID, bleg, *openCall, c, decision)
		if err != nil {
			return err
		}
		preview, err := e.invokeAttemptClampPreview(ctx, in)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			outcome := domain.DecisionOutcomeDeny
			if !authoritycoord.IsDenied(err) {
				outcome = domain.DecisionOutcomeUnavailable
			}
			return attemptAuthorityAdmissionError(authorityapp.AdmissionResult{Outcome: outcome}, err)
		}
		if len(preview.Clamps) == 0 {
			return nil
		}
		before := callMaxOutputTokens(*openCall)
		if err := e.applyPreviewClamps(ctx, openCall, c, preview.Clamps, inputTokens); err != nil {
			return err
		}
		after := callMaxOutputTokens(*openCall)
		if after == before {
			return nil
		}
		// Feed narrowed max-output into the next preview exposure estimate.
		if after >= 0 {
			adjusted := int(after)
			decision.AdjustedMaxOutputTokens = &adjusted
		}
	}
	return nil
}

func (e *Executor) invokeAttemptClampPreview(ctx context.Context, in authority.AttemptAdmission) (authoritycoord.CompositeDecision, error) {
	if e != nil && e.AttemptCoordinator != nil {
		return e.AttemptCoordinator.Preview(ctx, in)
	}
	adapter := newUsageAuthorityProviderAdapter(e.authorityService())
	if adapter == nil {
		return authoritycoord.CompositeDecision{Kind: authority.DecisionAllow, Readiness: authority.ReadinessDisabled}, nil
	}
	d, err := adapter.PreviewAttempt(ctx, in)
	if err != nil {
		return authoritycoord.CompositeDecision{}, err
	}
	out := authoritycoord.CompositeDecision{
		Kind:              d.Kind,
		Clamps:            append([]authority.Clamp(nil), d.Clamps...),
		ProviderDecisions: []authority.Decision{d},
		Readiness:         d.Readiness,
		BoundVersions:     append([]economics.PolicySnapshotRef(nil), d.BoundVersions...),
	}
	if out.Kind == "" {
		out.Kind = authority.DecisionAllow
	}
	if out.Readiness == "" {
		out.Readiness = authority.ReadinessReady
	}
	if d.Kind == authority.DecisionDeny {
		return out, &authoritycoord.ErrDenied{ProviderID: strings.TrimSpace(d.ProviderID), Decision: d}
	}
	return out, nil
}

func (e *Executor) buildAttemptAdmissionForPreview(
	ctx context.Context,
	traceID string,
	aLegID string,
	bleg b2bua.BLegRecord,
	call lipapi.Call,
	c routing.AttemptCandidate,
	decision accountingpreflight.Decision,
) (authority.AttemptAdmission, error) {
	_, rated, rateErr := e.rateOperatorAttemptSpend(ctx, c, decision)
	if rateErr != nil {
		if errors.Is(rateErr, context.Canceled) {
			return authority.AttemptAdmission{}, rateErr
		}
		return authority.AttemptAdmission{}, attemptAuthorityAdmissionError(
			authorityapp.AdmissionResult{Outcome: domain.DecisionOutcomeUnavailable},
			rateErr,
		)
	}
	quantities := attemptRatingQuantities(decision)
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens >= 0 {
		// Prefer the converged call's max-output when building preview exposure.
		quantities = upsertOutputTokenQuantity(quantities, int64(*call.Options.MaxOutputTokens))
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
		IdempotencyKey: attemptAuthorityReservationKey(call.ID, traceID, aLegID, bleg, c).String() + ":preview",
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Quantities:  quantities,
			Money:       rated.Money,
		},
	}
	if rated.Money.Present {
		in.RatingVersions = []economics.RatingSnapshotRef{ratingSnapshotRef(rated)}
	}
	return in, nil
}

func (e *Executor) applyPreviewClamps(
	ctx context.Context,
	call *lipapi.Call,
	c routing.AttemptCandidate,
	clamps []authority.Clamp,
	inputTokens int64,
) error {
	for _, clamp := range clamps {
		switch clamp.Kind {
		case authority.ClampMaxOutputTokens:
			if clamp.Value <= 0 {
				continue
			}
			if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens >= 0 &&
				int64(*call.Options.MaxOutputTokens) <= clamp.Value {
				continue
			}
			adjusted := int(clamp.Value)
			call.Options.MaxOutputTokens = &adjusted
		case authority.ClampMaxSpend:
			if !clamp.Money.Present {
				return fmt.Errorf("executor: preview max_spend clamp missing money (rule %q)", clamp.RuleID)
			}
			ac := &authorityapp.AdmissionClamp{
				RuleID: clamp.RuleID,
				EffectiveMax: domain.Amount{
					Unit:     domain.AmountUnitMoneyNano,
					Value:    clamp.Money.NanoUnits,
					Currency: strings.TrimSpace(clamp.Money.Currency),
				},
			}
			if err := e.applyAuthorityClamp(ctx, call, c, ac, inputTokens); err != nil {
				return err
			}
		}
	}
	return nil
}

func callMaxOutputTokens(call lipapi.Call) int64 {
	if call.Options.MaxOutputTokens == nil {
		return -1
	}
	return int64(*call.Options.MaxOutputTokens)
}

func upsertOutputTokenQuantity(qs []metering.Quantity, value int64) []metering.Quantity {
	out := append([]metering.Quantity(nil), qs...)
	for i := range out {
		if out[i].Component == metering.ComponentOutputToken {
			out[i].Value = value
			out[i].Present = true
			out[i].Unit = metering.UnitToken
			return out
		}
	}
	return append(out, metering.Quantity{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     value,
		Present:   true,
	})
}
