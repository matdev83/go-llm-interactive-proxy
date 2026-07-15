package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
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
	if !res.Money.Present {
		return res, fmt.Errorf("runtime: economics rater returned absent money (distinct from authoritative zero)")
	}
	if res.Perspective == "" {
		res.Perspective = req.Perspective
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
	outputTokens := max(int64(decision.Count.OutputTokens), 0)
	if outputTokens == 0 && decision.AdjustedMaxOutputTokens != nil && *decision.AdjustedMaxOutputTokens > 0 {
		outputTokens = int64(*decision.AdjustedMaxOutputTokens)
	}
	qs := []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     int64(decision.Count.InputTokens),
		Present:   true,
	}, {
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     outputTokens,
		Present:   true,
	}}
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

func usageEventRatingQuantities(ev lipapi.Event) []metering.Quantity {
	return []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: int64(ev.InputTokens), Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: int64(ev.OutputTokens), Present: true},
		{Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, Value: int64(ev.CacheReadTokens), Present: ev.CacheReadTokens > 0},
		{Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken, Value: int64(ev.CacheWriteTokens), Present: ev.CacheWriteTokens > 0},
		{Component: metering.ComponentReasoningOutputToken, Unit: metering.UnitToken, Value: int64(ev.ReasoningTokens), Present: ev.ReasoningTokens > 0},
	}
}

func (e *Executor) rateOperatorAttemptSpend(ctx context.Context, c routing.AttemptCandidate, decision accountingpreflight.Decision) (domain.Amount, economics.RatingResult, error) {
	if e == nil || e.EconomicsRater == nil {
		catalog := accounting.PriceCatalog{}
		if e != nil {
			catalog = e.AccountingPriceCatalog
		}
		return attemptAuthoritySpendAmount(catalog, c, decision), economics.RatingResult{}, nil
	}
	res, err := e.rateMonetaryExposure(ctx, economics.RatingRequest{
		Perspective: metering.PerspectiveOperator,
		BackendID:   strings.TrimSpace(c.Primary.Backend),
		Model:       strings.TrimSpace(c.Primary.Model),
		Quantities:  attemptRatingQuantities(decision),
		At:          e.now(),
	})
	if err != nil {
		return domain.Amount{}, res, err
	}
	return ratingResultToSpend(res), res, nil
}

func (e *Executor) rateCustomerRequestExposure(ctx context.Context, quantities []metering.Quantity, at time.Time) (economics.Money, economics.RatingResult, error) {
	if e == nil || e.EconomicsRater == nil {
		return economics.Money{}, economics.RatingResult{}, nil
	}
	// Empty quantities are still legal (fixed fees / request-count offers; req 6.9).
	res, err := e.rateMonetaryExposure(ctx, economics.RatingRequest{
		Perspective: metering.PerspectiveCustomer,
		Quantities:  append([]metering.Quantity(nil), quantities...),
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
