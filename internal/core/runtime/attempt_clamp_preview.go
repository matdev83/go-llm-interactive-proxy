package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const maxClampPreviewIterations = 4

// previewAndApplyAttemptClamps runs side-effect-free bounded strictly-narrowing
// clamp preview before backend-ingress freeze (design Final Attempt Sequence /
// Clamp Preview). Each iteration rebuilds exposure from the current call,
// previews, applies known clamps to a clone, and verifies non-widening.
func (e *Executor) previewAndApplyAttemptClamps(
	ctx context.Context,
	call *lipapi.Call,
	c routing.AttemptCandidate,
	aLegID string,
	blegID string,
) (previewed []authority.Clamp, previewRan bool, err error) {
	if e == nil || e.AttemptCoordinator == nil || call == nil {
		return nil, false, nil
	}
	for _, slot := range e.AttemptCoordinator.Slots {
		if _, ok := slot.Provider.(authority.AttemptClampPreviewer); ok {
			previewRan = true
			break
		}
	}
	if !previewRan {
		return nil, false, nil
	}

	var converged []authority.Clamp
	working := lipapi.CloneCall(*call)
	for range maxClampPreviewIterations {
		qs := e.previewExposureQuantities(ctx, working)
		in := authority.AttemptAdmission{
			RequestID:   strings.TrimSpace(working.ID),
			AttemptID:   strings.TrimSpace(blegID),
			BLegID:      strings.TrimSpace(blegID),
			ALegID:      strings.TrimSpace(aLegID),
			BackendID:   strings.TrimSpace(c.Primary.Backend),
			Model:       strings.TrimSpace(c.Primary.Model),
			Perspective: metering.PerspectiveOperator,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Scope:       scopeFromCtx(ctx),
			Exposure: economics.ExposureBasis{
				Perspective: metering.PerspectiveOperator,
				Boundary:    metering.BoundaryBackendIngress,
				Lifecycle:   metering.LifecycleBackendAttempt,
				Quantities:  qs,
				Output:      previewOutputAssumption(working),
			},
		}
		next, perr := e.AttemptCoordinator.PreviewClamps(ctx, in)
		if perr != nil {
			return nil, true, fmt.Errorf("executor: attempt clamp preview: %w", perr)
		}
		candidate := lipapi.CloneCall(working)
		if aerr := e.applyPreviewClamps(ctx, &candidate, c, next, inputTokensFromQuantities(qs)); aerr != nil {
			return nil, true, aerr
		}
		if werr := checkpoint.AssertNotWidened(working, candidate); werr != nil {
			return nil, true, fmt.Errorf("executor: preview clamp widened exposure: %w", werr)
		}
		if clampSetsEqualExact(converged, next) && maxOutputEqual(working, candidate) {
			*call = working
			return converged, true, nil
		}
		converged = cloneAuthorityClamps(next)
		working = candidate
	}
	return nil, true, fmt.Errorf("executor: attempt clamp preview did not converge within %d iterations", maxClampPreviewIterations)
}

func (e *Executor) previewExposureQuantities(ctx context.Context, call lipapi.Call) []metering.Quantity {
	qs := checkpoint.QuantitiesFromCall(call)
	if e == nil || e.AdminCountService == nil {
		return qs
	}
	count, err := e.AdminCountService.CountCall(ctx, accountingapp.CountCallInput{
		CallID: strings.TrimSpace(call.ID),
		Call:   call,
	})
	if err != nil {
		return qs
	}
	return checkpoint.MergeQuantities(qs, countedInputQuantities(count))
}

func previewOutputAssumption(call lipapi.Call) economics.ConservativeOutputAssumption {
	if call.Options.MaxOutputTokens == nil {
		return economics.ConservativeOutputAssumption{}
	}
	return economics.ConservativeOutputAssumption{
		BoundKind:  economics.OutputBoundClientProvided,
		TokenCount: int64(*call.Options.MaxOutputTokens),
		Present:    true,
	}
}

func inputTokensFromQuantities(qs []metering.Quantity) int64 {
	for _, q := range qs {
		if q.Component == metering.ComponentInputToken && q.Present {
			return q.Value
		}
	}
	return 0
}

func (e *Executor) applyPreviewClamps(ctx context.Context, call *lipapi.Call, c routing.AttemptCandidate, clamps []authority.Clamp, inputTokens int64) error {
	if call == nil {
		return nil
	}
	for _, clamp := range clamps {
		switch clamp.Kind {
		case authority.ClampMaxOutputTokens:
			if clamp.Value < 0 {
				return fmt.Errorf("executor: preview max_output_tokens clamp negative")
			}
			maxOut := int(clamp.Value)
			if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens >= 0 && *call.Options.MaxOutputTokens < maxOut {
				continue
			}
			call.Options.MaxOutputTokens = &maxOut
		case authority.ClampMaxSpend:
			adm := &authorityapp.AdmissionClamp{
				RuleID: clamp.RuleID,
				EffectiveMax: domain.Amount{
					Unit:     domain.AmountUnitMoneyNano,
					Value:    clamp.Money.NanoUnits,
					Currency: clamp.Money.Currency,
				},
				FailureBehavior: domain.FailureBehaviorFailClosed,
			}
			if err := e.applyAuthorityClamp(ctx, call, c, adm, inputTokens); err != nil {
				return fmt.Errorf("executor: preview max_spend quote: %w", err)
			}
		default:
			return fmt.Errorf("executor: preview unknown clamp kind %q", clamp.Kind)
		}
	}
	return nil
}

func clampSetsEqualExact(a, b []authority.Clamp) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !clampExactInSet(b, x) {
			return false
		}
	}
	for _, y := range b {
		if !clampExactInSet(a, y) {
			return false
		}
	}
	return true
}

func clampExactInSet(set []authority.Clamp, want authority.Clamp) bool {
	for _, c := range set {
		if clampsExactEqual(c, want) {
			return true
		}
	}
	return false
}

func clampsExactEqual(a, b authority.Clamp) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.RuleID != "" && b.RuleID != "" && a.RuleID != b.RuleID {
		return false
	}
	switch a.Kind {
	case authority.ClampMaxOutputTokens:
		return a.Value == b.Value
	case authority.ClampMaxSpend:
		return a.Money.Present == b.Money.Present &&
			a.Money.NanoUnits == b.Money.NanoUnits &&
			strings.EqualFold(strings.TrimSpace(a.Money.Currency), strings.TrimSpace(b.Money.Currency))
	default:
		return false
	}
}

func cloneAuthorityClamps(in []authority.Clamp) []authority.Clamp {
	if len(in) == 0 {
		return nil
	}
	out := make([]authority.Clamp, len(in))
	copy(out, in)
	return out
}

func maxOutputEqual(a, b lipapi.Call) bool {
	switch {
	case a.Options.MaxOutputTokens == nil && b.Options.MaxOutputTokens == nil:
		return true
	case a.Options.MaxOutputTokens == nil || b.Options.MaxOutputTokens == nil:
		return false
	default:
		return *a.Options.MaxOutputTokens == *b.Options.MaxOutputTokens
	}
}

func admissionClampAsAuthority(clamp *authorityapp.AdmissionClamp) (authority.Clamp, bool) {
	if clamp == nil {
		return authority.Clamp{}, false
	}
	if clamp.EffectiveMax.Unit != domain.AmountUnitMoneyNano {
		return authority.Clamp{}, false
	}
	return authority.Clamp{
		Kind:   authority.ClampMaxSpend,
		RuleID: clamp.RuleID,
		Money: economics.Money{
			NanoUnits: clamp.EffectiveMax.Value,
			Currency:  strings.TrimSpace(clamp.EffectiveMax.Currency),
			Present:   true,
		},
	}, true
}

// enforcePostAdmitClamps rejects exposure-changing admit clamps that were not
// exactly previewed. Matching clamps do not mutate the frozen call again.
func (e *Executor) enforcePostAdmitClamps(
	ctx context.Context,
	call *lipapi.Call,
	frozen lipapi.Call,
	previewed []authority.Clamp,
	previewRan bool,
	state attemptAuthorityState,
	c routing.AttemptCandidate,
	inputTokens int64,
) error {
	var admitClamps []authority.Clamp
	admitClamps = append(admitClamps, state.admitClamps...)
	if mapped, ok := admissionClampAsAuthority(state.admissionResult.Clamp); ok {
		if !clampExactInSet(admitClamps, mapped) {
			admitClamps = append(admitClamps, mapped)
		}
	}
	if len(admitClamps) == 0 {
		return nil
	}
	if previewRan {
		for _, clamp := range admitClamps {
			if !clampExactInSet(previewed, clamp) {
				return fmt.Errorf("executor: unpreviewed exposure clamp rejected (compensate and deny before Open)")
			}
		}
		return nil
	}
	for _, clamp := range admitClamps {
		probe := lipapi.CloneCall(frozen)
		switch clamp.Kind {
		case authority.ClampMaxOutputTokens:
			maxOut := int(clamp.Value)
			probe.Options.MaxOutputTokens = &maxOut
		case authority.ClampMaxSpend:
			adm, ok := clampToAdmissionClamp(clamp)
			if !ok {
				return fmt.Errorf("executor: legacy admit clamp unsupported")
			}
			if err := e.applyAuthorityClamp(ctx, &probe, c, adm, inputTokens); err != nil {
				return fmt.Errorf("executor: legacy clamp probe: %w", err)
			}
		default:
			return fmt.Errorf("executor: unknown admit clamp kind %q", clamp.Kind)
		}
		if !maxOutputEqual(frozen, probe) {
			return fmt.Errorf("executor: legacy exposure-changing admit clamp rejected (compensate and deny before Open)")
		}
	}
	_ = call
	return nil
}

func clampToAdmissionClamp(clamp authority.Clamp) (*authorityapp.AdmissionClamp, bool) {
	if clamp.Kind != authority.ClampMaxSpend || !clamp.Money.Present {
		return nil, false
	}
	return &authorityapp.AdmissionClamp{
		RuleID: clamp.RuleID,
		EffectiveMax: domain.Amount{
			Unit:     domain.AmountUnitMoneyNano,
			Value:    clamp.Money.NanoUnits,
			Currency: clamp.Money.Currency,
		},
		FailureBehavior: domain.FailureBehaviorFailClosed,
	}, true
}
