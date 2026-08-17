package domain

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// AmountSelectionSource carries quantity evidence for admission and settlement.
// Monetary exposure/facts may be present for telemetry, but this selector never
// reads them and therefore cannot turn them into authority state.
type AmountSelectionSource struct {
	Amount         Amount
	RequestCount   Amount
	PreflightUsage PreflightUsage
	Exposure       economics.ExposureBasis
	Facts          []metering.Fact
	FinalUsage     Amount
	ForSettlement  bool
}

// AppliesToLifecycle reports whether the rule should evaluate at the given
// admission/settlement lifecycle stage. Empty stage disables filtering.
func (r Rule) AppliesToLifecycle(stage metering.LifecycleScope) bool {
	if stage == "" {
		return true
	}
	if r.Basis.IsLegacyCompatibility() || strings.TrimSpace(string(r.LifecycleScope)) == "" {
		unit := r.Unit
		if unit == "" {
			unit = r.Limit.Unit
		}
		if unit == AmountUnitRequests {
			return stage == metering.LifecycleLogicalRequest
		}
		return stage == metering.LifecycleBackendAttempt
	}
	return r.LifecycleScope == stage
}

// SelectAmount resolves the quantity amount for one rule.
func (r Rule) SelectAmount(src AmountSelectionSource) (Amount, bool) {
	unit := r.Unit
	if unit == "" {
		unit = r.Limit.Unit
	}
	if r.Basis.IsLegacyCompatibility() || strings.TrimSpace(string(r.Basis)) == "" {
		return selectLegacyAmount(r, unit, src)
	}
	return selectFactOrExposureAmount(r, unit, src)
}

func selectLegacyAmount(r Rule, unit AmountUnit, src AmountSelectionSource) (Amount, bool) {
	if src.ForSettlement {
		if src.FinalUsage.Unit != "" {
			return src.FinalUsage, true
		}
		return Amount{}, false
	}
	basis := src.Amount
	if unit == AmountUnitRequests && src.RequestCount.Unit != "" {
		return src.RequestCount, true
	}
	if unit != "" && basis.Unit != unit {
		if amount, ok := src.PreflightUsage.AmountForUnit(unit); ok {
			return amount, true
		}
		return Amount{}, false
	}
	if unit != "" && basis.Unit == unit {
		return basis, true
	}
	if basis.Unit != "" {
		return basis, true
	}
	return Amount{}, false
}

func selectFactOrExposureAmount(r Rule, unit AmountUnit, src AmountSelectionSource) (Amount, bool) {
	wantBoundary := metering.Boundary(r.Basis)
	wantPersp := r.Perspective

	if amt, ok := amountFromFacts(r, unit, src.Facts, wantPersp, wantBoundary, false); ok {
		return amt, true
	}
	if src.ForSettlement {
		if amt, ok := amountFromFacts(r, unit, src.Facts, wantPersp, wantBoundary, true); ok {
			return amt, true
		}
	}

	exp := src.Exposure
	if !src.ForSettlement {
		if exp.Boundary != "" && wantBoundary != "" && exp.Boundary != wantBoundary {
			return Amount{}, false
		}
	} else if exp.Boundary != "" && wantBoundary != "" && exp.Boundary != wantBoundary && !isEgressBoundary(exp.Boundary) {
		return Amount{}, false
	}
	if exp.Perspective != "" && wantPersp != "" && exp.Perspective != wantPersp {
		return Amount{}, false
	}
	if amt, ok := quantityAmountForUnit(exp.Quantities, unit); ok {
		return amt, true
	}
	if !src.ForSettlement && unit == AmountUnitRequests && src.RequestCount.Unit == AmountUnitRequests {
		return src.RequestCount, true
	}
	if !src.ForSettlement && src.Amount.Unit == unit {
		return src.Amount, true
	}
	return Amount{}, false
}

func amountFromFacts(r Rule, unit AmountUnit, facts []metering.Fact, wantPersp metering.EconomicPerspective, wantBoundary metering.Boundary, egressFallback bool) (Amount, bool) {
	for _, f := range facts {
		if wantPersp != "" && f.Perspective != wantPersp {
			continue
		}
		if !egressFallback {
			if wantBoundary != "" && f.Boundary != wantBoundary {
				continue
			}
		} else if !isEgressBoundary(f.Boundary) {
			continue
		}
		if amt, ok := quantityAmountForUnit(f.Quantities, unit); ok {
			return amt, true
		}
	}
	return Amount{}, false
}

func isEgressBoundary(b metering.Boundary) bool {
	return b == metering.BoundaryBackendEgress || b == metering.BoundaryFrontendEgress
}

func quantityAmountForUnit(qs []metering.Quantity, unit AmountUnit) (Amount, bool) {
	switch unit {
	case AmountUnitRequests:
		for _, q := range qs {
			if q.Present && q.Component == metering.ComponentRequest && (q.Unit == metering.UnitCount || q.Unit == "") {
				return Amount{Unit: unit, Value: q.Value}, true
			}
		}
		return Amount{}, false
	case AmountUnitInputTokens:
		return firstQuantity(qs, metering.ComponentInputToken, unit)
	case AmountUnitOutputTokens:
		return firstQuantity(qs, metering.ComponentOutputToken, unit)
	case AmountUnitCacheReadTokens:
		return firstQuantity(qs, metering.ComponentCacheReadInputToken, unit)
	case AmountUnitCacheWriteTokens:
		return firstQuantity(qs, metering.ComponentCacheWriteInputToken, unit)
	case AmountUnitReasoningTokens:
		return firstQuantity(qs, metering.ComponentReasoningOutputToken, unit)
	case AmountUnitTotalTokens:
		in, inOK := firstQuantity(qs, metering.ComponentInputToken, AmountUnitInputTokens)
		out, outOK := firstQuantity(qs, metering.ComponentOutputToken, AmountUnitOutputTokens)
		if !inOK && !outOK {
			return Amount{}, false
		}
		return Amount{Unit: AmountUnitTotalTokens, Value: in.Value + out.Value}, true
	default:
		return Amount{}, false
	}
}

func firstQuantity(qs []metering.Quantity, component string, unit AmountUnit) (Amount, bool) {
	for _, q := range qs {
		if !q.Present || q.Component != component {
			continue
		}
		return Amount{Unit: unit, Value: q.Value}, true
	}
	return Amount{}, false
}
