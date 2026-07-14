package domain

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// AmountSelectionSource carries optional metering facts and exposure plus the
// legacy Request/Spend/RequestCount fields used by compatibility-basis rules.
type AmountSelectionSource struct {
	Amount         Amount
	Spend          Amount
	RequestCount   Amount
	PreflightUsage PreflightUsage
	Exposure       economics.ExposureBasis
	Facts          []metering.Fact
	FinalUsage     Amount
	FinalCost      Amount
	ForSettlement  bool
}

// AppliesToLifecycle reports whether the rule should evaluate at the given
// admission/settlement lifecycle stage. Empty stage disables filtering (legacy
// callers/tests). Legacy-compat rules infer stage from unit: requests →
// logical_request, otherwise → backend_attempt (Phase 6 split).
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

// SelectAmount resolves the evaluation/settlement amount for one rule.
// Compatibility basis uses undifferentiated Request/Spend/Final* inputs.
// Dual-plane bases require matching exposure/facts; missing basis is not zero.
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
		if unit == AmountUnitMoneyNano || r.Kind == RuleKindBudget || r.Kind == RuleKindSpendCap {
			if src.FinalCost.Unit != "" {
				return src.FinalCost, true
			}
			if src.FinalCost.Value == 0 && src.FinalCost.Unit == AmountUnitMoneyNano {
				return src.FinalCost, true
			}
		}
		if src.FinalUsage.Unit != "" {
			return src.FinalUsage, true
		}
		return Amount{}, false
	}
	basis := src.Amount
	if unit == AmountUnitRequests && src.RequestCount.Unit != "" {
		return src.RequestCount, true
	}
	if r.Kind == RuleKindBudget || r.Kind == RuleKindSpendCap {
		if src.Spend.Unit == "" {
			return Amount{}, false
		}
		return src.Spend, true
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

	if len(src.Facts) > 0 {
		for _, f := range src.Facts {
			if wantPersp != "" && f.Perspective != wantPersp {
				continue
			}
			if wantBoundary != "" && f.Boundary != wantBoundary {
				continue
			}
			if amt, ok := quantityAmountForUnit(f.Quantities, unit, r.Currency); ok {
				return amt, true
			}
		}
	}

	exp := src.Exposure
	if exp.Boundary != "" && wantBoundary != "" && exp.Boundary != wantBoundary {
		return Amount{}, false
	}
	if exp.Perspective != "" && wantPersp != "" && exp.Perspective != wantPersp {
		return Amount{}, false
	}
	if unit == AmountUnitMoneyNano || r.Kind == RuleKindBudget || r.Kind == RuleKindSpendCap {
		if exp.Money.Present {
			cur := strings.TrimSpace(exp.Money.Currency)
			if cur == "" {
				cur = strings.TrimSpace(r.Currency)
			}
			return Amount{Unit: AmountUnitMoneyNano, Value: exp.Money.NanoUnits, Currency: cur}, true
		}
		return Amount{}, false
	}
	if amt, ok := quantityAmountForUnit(exp.Quantities, unit, r.Currency); ok {
		return amt, true
	}
	// Allow request-count dual-plane rules to use explicit RequestCount when the
	// exposure does not yet carry a request quantity component.
	if unit == AmountUnitRequests && src.RequestCount.Unit == AmountUnitRequests {
		return src.RequestCount, true
	}
	if unit != AmountUnitMoneyNano && src.Amount.Unit == unit {
		return src.Amount, true
	}
	return Amount{}, false
}

func quantityAmountForUnit(qs []metering.Quantity, unit AmountUnit, currency string) (Amount, bool) {
	_ = currency
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
	case AmountUnitTotalTokens:
		in, inOK := firstQuantity(qs, metering.ComponentInputToken, AmountUnitInputTokens)
		out, outOK := firstQuantity(qs, metering.ComponentOutputToken, AmountUnitOutputTokens)
		if !inOK && !outOK {
			return Amount{}, false
		}
		return Amount{Unit: AmountUnitTotalTokens, Value: in.Value + out.Value}, true
	case AmountUnitMoneyNano:
		return Amount{}, false
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
