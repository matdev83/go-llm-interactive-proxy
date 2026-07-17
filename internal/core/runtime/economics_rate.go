package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/plane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// rateMonetaryExposure invokes the injected EconomicsRater. When a rater is
// configured, catalog EstimateCost must not silently substitute (requirements
// 6.1–6.4, 12.1).
func (e *Executor) rateMonetaryExposure(ctx context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	if e == nil || e.EconomicsRater == nil {
		return economics.RatingResult{}, fmt.Errorf("runtime: economics rater not configured")
	}
	if req.At.IsZero() {
		req.At = e.now()
	}
	res, err := e.EconomicsRater.Rate(ctx, req)
	if err != nil {
		return economics.RatingResult{}, err
	}
	if vErr := res.ValidateFor(req); vErr != nil {
		return economics.RatingResult{}, fmt.Errorf("runtime: economics rater result: %w", vErr)
	}
	return res, nil
}

func ratingSnapshotRef(res economics.RatingResult) economics.RatingSnapshotRef {
	ref := economics.RatingSnapshotRef{
		VersionRef: res.Version,
		RaterID:    strings.TrimSpace(res.RaterID),
	}
	if ref.RaterID == "" {
		ref.RaterID = strings.TrimSpace(res.Source)
	}
	if ref.ID == "" {
		ref.ID = ref.RaterID
	}
	return ref
}

func ratingResultToSpend(res economics.RatingResult) domain.Amount {
	return domain.Amount{
		Unit:     domain.AmountUnitMoneyNano,
		Value:    res.Money.NanoUnits,
		Currency: strings.TrimSpace(res.Money.Currency),
	}
}

func attemptRatingQuantities(decision accountingpreflight.Decision) []metering.Quantity {
	qs := []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     int64(decision.Count.InputTokens),
		Present:   true,
	}}
	if out, ok := explicitOutputQuantity(decision); ok {
		qs = append(qs, out)
	}
	if decision.Count.CacheReadTokens > 0 {
		qs = append(qs, metering.Quantity{
			Component: metering.ComponentCacheReadInputToken,
			Unit:      metering.UnitToken,
			Value:     int64(decision.Count.CacheReadTokens),
			Present:   true,
		})
	}
	if decision.Count.CacheWriteTokens > 0 {
		qs = append(qs, metering.Quantity{
			Component: metering.ComponentCacheWriteInputToken,
			Unit:      metering.UnitToken,
			Value:     int64(decision.Count.CacheWriteTokens),
			Present:   true,
		})
	}
	if decision.Count.ReasoningTokens > 0 {
		qs = append(qs, metering.Quantity{
			Component: metering.ComponentReasoningOutputToken,
			Unit:      metering.UnitToken,
			Value:     int64(decision.Count.ReasoningTokens),
			Present:   true,
		})
	}
	return qs
}

// explicitOutputQuantity returns Present output only when an authoritative bound
// exists (AdjustedMax or positive counted output). Unknown output is omitted
// (requirement 2.7); never Present:true Value:0 without an explicit bound.
func explicitOutputQuantity(decision accountingpreflight.Decision) (metering.Quantity, bool) {
	if decision.AdjustedMaxOutputTokens != nil {
		v := int64(*decision.AdjustedMaxOutputTokens)
		if v < 0 {
			return metering.Quantity{}, false
		}
		return metering.Quantity{
			Component: metering.ComponentOutputToken,
			Unit:      metering.UnitToken,
			Value:     v,
			Present:   true,
		}, true
	}
	if decision.Count.OutputTokens > 0 {
		return metering.Quantity{
			Component: metering.ComponentOutputToken,
			Unit:      metering.UnitToken,
			Value:     int64(decision.Count.OutputTokens),
			Present:   true,
		}, true
	}
	return metering.Quantity{}, false
}

// finalOperatorAttemptQuantities prefers frozen backend-ingress checkpoint
// quantities over a stale preflight Decision (requirements 2.1–2.2, design D3).
// When BE ingress lacks an output component, the conservative output from
// decision/AdjustedMax is retained.
func finalOperatorAttemptQuantities(ctx context.Context, blegID string, decision accountingpreflight.Decision) []metering.Quantity {
	fallback := attemptRatingQuantities(decision)
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		return fallback
	}
	be := holder.BackendIngressFor(blegID)
	if be == nil || len(be.Public.Quantities) == 0 {
		return fallback
	}
	merged := append([]metering.Quantity(nil), be.Public.Quantities...)
	if !quantityComponentPresent(merged, metering.ComponentOutputToken) {
		for _, q := range fallback {
			if q.Component == metering.ComponentOutputToken {
				merged = append(merged, q)
				break
			}
		}
	}
	return merged
}

func quantityComponentPresent(qs []metering.Quantity, component string) bool {
	for _, q := range qs {
		if q.Component == component && q.Present {
			return true
		}
	}
	return false
}

func conservativeOutputAssumption(decision accountingpreflight.Decision, quantities []metering.Quantity) economics.ConservativeOutputAssumption {
	for _, q := range quantities {
		if q.Component == metering.ComponentOutputToken && q.Present {
			kind := economics.OutputBoundClientProvided
			if decision.AdjustedMaxOutputTokens != nil && int64(*decision.AdjustedMaxOutputTokens) == q.Value {
				kind = economics.OutputBoundClamp
			}
			return economics.ConservativeOutputAssumption{
				BoundKind: kind, TokenCount: q.Value, Present: true,
			}
		}
	}
	if decision.AdjustedMaxOutputTokens != nil {
		v := int64(*decision.AdjustedMaxOutputTokens)
		if v < 0 {
			return economics.ConservativeOutputAssumption{}
		}
		return economics.ConservativeOutputAssumption{
			BoundKind:  economics.OutputBoundClamp,
			TokenCount: v,
			Present:    true,
		}
	}
	return economics.ConservativeOutputAssumption{}
}

func usageEventRatingQuantities(ev lipapi.Event) []metering.Quantity {
	return plane.QuantitiesFromUsageEvent(ev)
}

func (e *Executor) rateOperatorAttemptSpend(
	ctx context.Context,
	c routing.AttemptCandidate,
	decision accountingpreflight.Decision,
	quantities []metering.Quantity,
	factIDs []string,
	factRefs []metering.FactRef,
) (domain.Amount, economics.RatingResult, error) {
	qs := quantities
	if len(qs) == 0 {
		qs = attemptRatingQuantities(decision)
	}
	if e == nil || e.EconomicsRater == nil {
		catalog := accounting.PriceCatalog{}
		if e != nil {
			catalog = e.AccountingPriceCatalog
		}
		return attemptAuthoritySpendAmountFromQuantities(catalog, c, qs), economics.RatingResult{}, nil
	}
	res, err := e.rateMonetaryExposure(ctx, economics.RatingRequest{
		Perspective: metering.PerspectiveOperator,
		BackendID:   strings.TrimSpace(c.Primary.Backend),
		Model:       strings.TrimSpace(c.Primary.Model),
		Quantities:  qs,
		Output:      conservativeOutputAssumption(decision, qs),
		FactIDs:     append([]string(nil), factIDs...),
		FactRefs:    append([]metering.FactRef(nil), factRefs...),
		At:          e.now(),
	})
	if err != nil {
		return domain.Amount{}, res, err
	}
	return ratingResultToSpend(res), res, nil
}

func attemptAuthoritySpendAmountFromQuantities(catalog accounting.PriceCatalog, c routing.AttemptCandidate, quantities []metering.Quantity) domain.Amount {
	var usage accounting.TokenUsage
	for _, q := range quantities {
		if !q.Present {
			continue
		}
		switch q.Component {
		case metering.ComponentInputToken:
			usage.InputTokens = q.Value
		case metering.ComponentOutputToken:
			usage.OutputTokens = q.Value
		case metering.ComponentCacheReadInputToken:
			usage.CacheReadTokens = q.Value
		case metering.ComponentCacheWriteInputToken:
			usage.CacheWriteTokens = q.Value
		case metering.ComponentReasoningOutputToken:
			usage.ReasoningTokens = q.Value
		}
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

func (e *Executor) rateCustomerRequestExposure(
	ctx context.Context,
	quantities []metering.Quantity,
	at time.Time,
	factIDs []string,
	factRefs []metering.FactRef,
) (economics.Money, economics.RatingResult, error) {
	if e == nil || e.EconomicsRater == nil {
		return economics.Money{}, economics.RatingResult{}, nil
	}
	// Empty quantities are still legal (fixed fees / request-count offers; req 6.9).
	res, err := e.rateMonetaryExposure(ctx, economics.RatingRequest{
		Perspective: metering.PerspectiveCustomer,
		Quantities:  append([]metering.Quantity(nil), quantities...),
		FactIDs:     append([]string(nil), factIDs...),
		FactRefs:    append([]metering.FactRef(nil), factRefs...),
		At:          at,
	})
	if err != nil {
		return economics.Money{}, res, err
	}
	return res.Money, res, nil
}

func bindAdmissionRatingVersion(res *authorityapp.AdmissionResult, rated economics.RatingResult) {
	if res == nil {
		return
	}
	ref := ratingSnapshotRef(rated)
	if ref.Version == "" && ref.RaterID == "" {
		return
	}
	res.BoundRatingVersion = ref
}
