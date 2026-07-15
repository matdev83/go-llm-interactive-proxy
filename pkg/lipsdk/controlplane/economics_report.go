package controlplane

import (
	"fmt"
	"strings"
)

// TokenQuantityInput is a safe quantity observation without raw content
// (requirements 4.7, 14.6, 17.5).
type TokenQuantityInput struct {
	Component string `json:"component,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Value     int64  `json:"value,omitempty"`
	Present   bool   `json:"present"`
}

// MoneyAmountInput is a currency-tagged amount with explicit presence (6.2).
type MoneyAmountInput struct {
	NanoUnits int64  `json:"nano_units,omitempty"`
	Currency  string `json:"currency,omitempty"`
	Present   bool   `json:"present"`
}

// CustomerChargeInput holds independently queryable customer charge facts (5.7).
type CustomerChargeInput struct {
	Money                 MoneyAmountInput   `json:"money,omitzero"`
	FrontendIngressTokens TokenQuantityInput `json:"frontend_ingress_tokens,omitzero"`
	FrontendEgressTokens  TokenQuantityInput `json:"frontend_egress_tokens,omitzero"`
	RatingVersion         VersionRef         `json:"rating_version,omitzero"`
}

// OperatorCostInput holds independently queryable operator cost facts (5.7).
type OperatorCostInput struct {
	Money                MoneyAmountInput   `json:"money,omitzero"`
	BackendIngressTokens TokenQuantityInput `json:"backend_ingress_tokens,omitzero"`
	BackendEgressTokens  TokenQuantityInput `json:"backend_egress_tokens,omitzero"`
	ProviderReportedCost MoneyAmountInput   `json:"provider_reported_cost,omitempty"`
	RatingVersion        VersionRef         `json:"rating_version,omitzero"`
}

// CompressionReportInput exposes transformation lineage without raw content
// (requirements 4.7, 14.6).
type CompressionReportInput struct {
	FrontendInput   TokenQuantityInput `json:"frontend_input,omitempty"`
	BackendInput    TokenQuantityInput `json:"backend_input,omitempty"`
	DeliveredOutput TokenQuantityInput `json:"delivered_output,omitempty"`
	ProviderOutput  TokenQuantityInput `json:"provider_output,omitempty"`
	ProviderCost    MoneyAmountInput   `json:"provider_cost,omitempty"`
}

// RoutingOverheadInput holds routing overhead separately from direct attempt
// costs (5.7).
type RoutingOverheadInput struct {
	AttemptCount        TokenQuantityInput `json:"attempt_count,omitempty"`
	NonSurfacedAttempts TokenQuantityInput `json:"non_surfaced_attempts,omitempty"`
	OverheadCost        MoneyAmountInput   `json:"overhead_cost,omitempty"`
}

// ReportCalculationType names an explicit cross-plane calculation. Customer
// charge and operator cost must never merge without one of these types (14.7).
type ReportCalculationType string

const (
	ReportCalculationGrossMargin        ReportCalculationType = "gross_margin"
	ReportCalculationNetExposure        ReportCalculationType = "net_exposure"
	ReportCalculationCompressionSavings ReportCalculationType = "compression_token_savings"
	ReportCalculationRoutingOverhead    ReportCalculationType = "routing_overhead_total"
)

// IsKnown reports whether t is a documented report calculation type.
func (t ReportCalculationType) IsKnown() bool {
	switch t {
	case ReportCalculationGrossMargin, ReportCalculationNetExposure,
		ReportCalculationCompressionSavings, ReportCalculationRoutingOverhead:
		return true
	}
	return false
}

// CalculatedAmount is the only DTO that may combine customer and operator
// economics. It always carries an explicit ReportCalculationType (14.7).
type CalculatedAmount struct {
	Calculation ReportCalculationType `json:"calculation"`
	Money       MoneyAmountInput      `json:"money,omitempty"`
	Quantity    TokenQuantityInput    `json:"quantity,omitempty"`
}

// DualPlaneReportInputs groups independent source inputs for query/report
// consumers. It intentionally omits any merged total field (14.7).
type DualPlaneReportInputs struct {
	Customer        CustomerChargeInput    `json:"customer"`
	Operator        OperatorCostInput      `json:"operator"`
	Compression     CompressionReportInput `json:"compression,omitzero"`
	RoutingOverhead RoutingOverheadInput   `json:"routing_overhead,omitzero"`
}

// ValidateCalculatedAmount rejects amounts that omit an explicit calculation
// type (14.7).
func ValidateCalculatedAmount(a CalculatedAmount) error {
	if !a.Calculation.IsKnown() {
		return fmt.Errorf("controlplane: calculated amount requires explicit report calculation type")
	}
	if !a.Money.Present && !a.Quantity.Present {
		return fmt.Errorf("controlplane: calculated amount requires present money or quantity")
	}
	return nil
}

// CalculateGrossMargin returns customer charge minus operator cost using an
// explicit gross_margin calculation (14.7).
func CalculateGrossMargin(customer CustomerChargeInput, operator OperatorCostInput) (CalculatedAmount, error) {
	if !customer.Money.Present || !operator.Money.Present {
		return CalculatedAmount{}, fmt.Errorf("controlplane: gross margin requires present customer charge and operator cost")
	}
	if err := requireSameCurrency(customer.Money, operator.Money); err != nil {
		return CalculatedAmount{}, err
	}
	diff, ok := subMoneyChecked(customer.Money.NanoUnits, operator.Money.NanoUnits)
	if !ok {
		return CalculatedAmount{}, fmt.Errorf("controlplane: gross margin underflow")
	}
	out := CalculatedAmount{
		Calculation: ReportCalculationGrossMargin,
		Money: MoneyAmountInput{
			NanoUnits: diff,
			Currency:  customer.Money.Currency,
			Present:   true,
		},
	}
	return out, ValidateCalculatedAmount(out)
}

// CalculateCompressionTokenSavings returns frontend minus backend input tokens
// via an explicit compression_token_savings calculation (4.7, 14.6).
func CalculateCompressionTokenSavings(in CompressionReportInput) (CalculatedAmount, error) {
	if !in.FrontendInput.Present || !in.BackendInput.Present {
		return CalculatedAmount{}, fmt.Errorf("controlplane: compression savings requires present frontend and backend input quantities")
	}
	if in.FrontendInput.Value < in.BackendInput.Value {
		return CalculatedAmount{}, fmt.Errorf("controlplane: compression savings requires frontend input >= backend input")
	}
	out := CalculatedAmount{
		Calculation: ReportCalculationCompressionSavings,
		Quantity: TokenQuantityInput{
			Component: in.FrontendInput.Component,
			Unit:      in.FrontendInput.Unit,
			Value:     in.FrontendInput.Value - in.BackendInput.Value,
			Present:   true,
		},
	}
	return out, ValidateCalculatedAmount(out)
}

func requireSameCurrency(a, b MoneyAmountInput) error {
	ac := strings.TrimSpace(a.Currency)
	bc := strings.TrimSpace(b.Currency)
	if ac == "" || bc == "" {
		return fmt.Errorf("controlplane: currency required")
	}
	if ac != bc {
		return fmt.Errorf("controlplane: currency mismatch %q vs %q", ac, bc)
	}
	return nil
}

func subMoneyChecked(a, b int64) (int64, bool) {
	if b > a {
		return 0, false
	}
	return a - b, true
}
