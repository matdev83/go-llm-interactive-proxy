package billing

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func amountDecimal(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	return &value
}

// TestMonetaryExactAmountCanonicalContract locks the authoritative exact-money
// contract used at every Phase12 ingress.
func TestMonetaryExactAmountCanonicalContract(t *testing.T) {
	t.Parallel()

	huge := new(big.Int).Exp(big.NewInt(10), big.NewInt(150), nil).String()
	cases := map[string]MonetaryExactAmount{
		"decimal and rational both":          {Currency: "USD", Decimal: amountDecimal(t, "1"), Numerator: "1", Denominator: "2"},
		"neither representation":             {Currency: "USD"},
		"noncanonical decimal leading zero":  {Currency: "USD", Decimal: &metering.Decimal{Coefficient: "01"}},
		"noncanonical decimal negative zero": {Currency: "USD", Decimal: &metering.Decimal{Coefficient: "-0"}},
		"noncanonical decimal zero scale":    {Currency: "USD", Decimal: &metering.Decimal{Coefficient: "0", Scale: 2}},
		"unreduced rational":                 {Currency: "USD", Numerator: "2", Denominator: "2"},
		"huge cancel to small":               {Currency: "USD", Numerator: huge, Denominator: huge},
		"leading plus numerator":             {Currency: "USD", Numerator: "+1", Denominator: "2"},
		"leading zero numerator":             {Currency: "USD", Numerator: "01", Denominator: "2"},
		"negative zero numerator":            {Currency: "USD", Numerator: "-0", Denominator: "1"},
		"zero rational":                      {Currency: "USD", Numerator: "0", Denominator: "1"},
		"negative denominator":               {Currency: "USD", Numerator: "1", Denominator: "-2"},
		"zero denominator":                   {Currency: "USD", Numerator: "1", Denominator: "0"},
		"huge numerator":                     {Currency: "USD", Numerator: huge, Denominator: "1"},
		"huge denominator":                   {Currency: "USD", Numerator: "1", Denominator: huge},
		"mixed case currency":                {Currency: "Usd", Decimal: amountDecimal(t, "1")},
		"empty currency":                     {Currency: "", Decimal: amountDecimal(t, "1")},
	}
	for name, amount := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := amount.Validate(); !errors.Is(err, ErrInvalidMonetaryExactAmount) {
				t.Fatalf("Validate error = %v, want ErrInvalidMonetaryExactAmount", err)
			}
			if _, err := amount.Rat(); !errors.Is(err, ErrInvalidMonetaryExactAmount) {
				t.Fatalf("Rat error = %v, want ErrInvalidMonetaryExactAmount", err)
			}
			if _, err := amount.NormalizeCanonical(); !errors.Is(err, ErrInvalidMonetaryExactAmount) {
				t.Fatalf("NormalizeCanonical error = %v, want ErrInvalidMonetaryExactAmount", err)
			}
		})
	}

	t.Run("canonical decimal round trip", func(t *testing.T) {
		t.Parallel()
		amount := toleranceAmount(t, "USD", "1.32")
		if err := amount.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		normalized, err := amount.NormalizeCanonical()
		if err != nil {
			t.Fatalf("NormalizeCanonical: %v", err)
		}
		if normalized.Decimal == nil || normalized.Decimal.CanonicalString() != "132/2" {
			t.Fatalf("normalized = %+v, want canonical 132/2", normalized)
		}
		rat, err := amount.Rat()
		if err != nil {
			t.Fatalf("Rat: %v", err)
		}
		if rat.RatString() != "33/25" {
			t.Fatalf("rat = %s, want 33/25", rat.RatString())
		}
	})

	t.Run("canonical reduced rational", func(t *testing.T) {
		t.Parallel()
		amount := MonetaryExactAmount{Currency: "USD", Numerator: "-1", Denominator: "12"}
		if err := amount.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		rat, err := amount.Rat()
		if err != nil {
			t.Fatalf("Rat: %v", err)
		}
		if rat.RatString() != "-1/12" {
			t.Fatalf("rat = %s, want -1/12", rat.RatString())
		}
	})

	t.Run("lowercase measurement unit keys stay canonical", func(t *testing.T) {
		t.Parallel()
		for _, unit := range []string{"token", "second", "count", "byte_second"} {
			amount := toleranceAmount(t, unit, "1")
			if err := amount.Validate(); err != nil {
				t.Fatalf("unit %q: %v", unit, err)
			}
		}
	})

	t.Run("uppercase currency code is canonical", func(t *testing.T) {
		t.Parallel()
		amount := toleranceAmount(t, "USD", "1")
		if err := amount.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
}

// TestMonetaryExactAmountValidationAtIngress proves every Phase12 entry point
// rejects noncanonical exact amounts and unbounded FX rates.
func TestMonetaryExactAmountValidationAtIngress(t *testing.T) {
	t.Parallel()

	noncanonical := MonetaryExactAmount{Currency: "USD", Numerator: "2", Denominator: "2"}

	t.Run("tolerance", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), nil))
		if _, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, noncanonical, toleranceAmount(t, "USD", "1")); !errors.Is(err, ErrInvalidMonetaryExactAmount) {
			t.Fatalf("error = %v, want ErrInvalidMonetaryExactAmount", err)
		}
	})

	t.Run("aggregation finding", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), nil))
		finding := reconciliationTestFinding(t, "amount-ingress", "call-amount", "USD", "", "1", "1", metering.QualityObserved, ReconciliationStatusDiscrepant)
		finding.Expected = &noncanonical
		if _, err := AggregateReconciliationFindings(policy, []ReconciliationFinding{finding}); !errors.Is(err, ErrInvalidMonetaryExactAmount) {
			t.Fatalf("error = %v, want ErrInvalidMonetaryExactAmount", err)
		}
	})

	t.Run("selection candidate", func(t *testing.T) {
		t.Parallel()
		policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Amount = &noncanonical
		if _, err := SelectOperatorCost(policy, operatorCostTestInput(t, candidate)); !errors.Is(err, ErrInvalidMonetaryExactAmount) {
			t.Fatalf("error = %v, want ErrInvalidMonetaryExactAmount", err)
		}
	})

	t.Run("selection fx rate", func(t *testing.T) {
		t.Parallel()
		fx := &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD", Rate: &metering.Decimal{Coefficient: "092", Scale: 2}}
		if err := fx.Validate(); !errors.Is(err, ErrOperatorCostSelectionInput) {
			t.Fatalf("error = %v, want ErrOperatorCostSelectionInput", err)
		}
	})

	t.Run("tolerance limit precision", func(t *testing.T) {
		t.Parallel()
		limit := metering.Decimal{Coefficient: strings.Repeat("1", 40)}
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, &limit, nil))
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})
}
