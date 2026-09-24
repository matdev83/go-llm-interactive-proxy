package billing

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func operatorCostTestAmount(t *testing.T, unit, raw string) *MonetaryExactAmount {
	t.Helper()
	amount := toleranceAmount(t, unit, raw)
	return &amount
}

func operatorCostTestRef(id string) metering.ObservationRef {
	return metering.ObservationRef{StoreID: reconciliationSubject().StoreID, ObservationID: id, Revision: 1, PayloadHash: strings.Repeat("a", 64)}
}

func operatorCostTestCandidate(t *testing.T, basis OperatorCostSelectionBasis, id, amount string, completeness economics.Completeness, payer metering.PaymentParty) OperatorCostCandidate {
	t.Helper()
	return OperatorCostCandidate{
		Basis: basis, ValuationID: "valuation-" + id, ValuationVersion: 1,
		Currency: "USD", Amount: operatorCostTestAmount(t, "USD", amount), Completeness: completeness,
		Payer: payer, Scope: "call:operator-selection",
		SourceRefs: []metering.ObservationRef{operatorCostTestRef(id + "-observation")},
	}
}

func operatorCostTestRule(id string, basis OperatorCostSelectionBasis, status OperatorCostSelectionStatus, requireOperatorPayer, allowPartialEvidence, requireComparableComparison bool) OperatorCostSelectionRule {
	return OperatorCostSelectionRule{
		ID: id, Basis: basis, Status: status,
		RequireOperatorPayer: requireOperatorPayer, AllowPartialEvidence: allowPartialEvidence,
		RequireComparableComparison: requireComparableComparison,
	}
}

func operatorCostTestPolicy(rules ...OperatorCostSelectionRule) OperatorCostSelectionPolicy {
	return OperatorCostSelectionPolicy{
		Version: OperatorCostSelectionPolicyV1,
		Ref:     VersionRef{ID: "operator-selection-policy", Version: "v1"},
		Rules:   rules,
	}
}

func operatorCostTestInput(t *testing.T, candidates ...OperatorCostCandidate) OperatorCostSelectionInput {
	t.Helper()
	return OperatorCostSelectionInput{
		Subject: reconciliationSubject(), Scope: "call:operator-selection",
		Currency: "USD", PayerClass: OperatorCostPayerOperator,
		Provenance: OperatorCostProvenanceAttempted,
		Reconciliation: OperatorCostReconciliationState{
			Ref:    OperatorCostReconciliationRef{ID: "reconciliation-1", Version: 1, Fingerprint: strings.Repeat("f", 64)},
			Status: ReconciliationStatusDiscrepant, Complete: true,
		},
		Candidates: candidates,
		AsOf:       time.Unix(1_700_003_000, 0).UTC(),
	}
}

// TestOperatorCostSelectionChoosesPWithoutErasingAlternatives locks C4: a
// selected P basis never deletes the competing E/Q/S evidence and selection is
// not reconciliation.
func TestOperatorCostSelectionChoosesPWithoutErasingAlternatives(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(
		operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false),
		operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, true, false, false),
	)
	input := operatorCostTestInput(t,
		operatorCostTestCandidate(t, OperatorCostBasisE, "e", "1.00", economics.CompletenessComplete, metering.PaymentParty{}),
		operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, metering.PaymentParty{}),
		operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		operatorCostTestCandidate(t, OperatorCostBasisS, "s", "1.30", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
	)
	result, err := SelectOperatorCost(policy, input)
	if err != nil {
		t.Fatalf("SelectOperatorCost: %v", err)
	}
	if result.Status != OperatorCostSelectionStatusFinal || result.Basis != OperatorCostBasisP {
		t.Fatalf("selection = %s/%s, want final/p", result.Status, result.Basis)
	}
	if result.Amount == nil || result.Amount.Decimal == nil || result.Amount.Decimal.CanonicalString() != "132/2" {
		t.Fatalf("selected amount = %+v, want exact P amount 1.32", result.Amount)
	}
	if len(result.Candidates) != 4 {
		t.Fatalf("candidates = %d, want all four alternatives preserved", len(result.Candidates))
	}
	for _, basis := range []OperatorCostSelectionBasis{OperatorCostBasisE, OperatorCostBasisQ, OperatorCostBasisP, OperatorCostBasisS} {
		found := false
		for _, candidate := range result.Candidates {
			if candidate.Basis == basis && candidate.Amount != nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("candidate %s was erased: %+v", basis, result.Candidates)
		}
	}
	if result.ComparisonStatus != ReconciliationStatusDiscrepant || !result.ComparisonComplete {
		t.Fatalf("comparison state lost: %+v", result)
	}
	if result.Reconciliation.ID != "reconciliation-1" || result.Reconciliation.Version != 1 || result.Reconciliation.Fingerprint == "" {
		t.Fatalf("reconciliation ref lost: %+v", result.Reconciliation)
	}
	if result.PostingState != OperatorCostPostingUnposted {
		t.Fatalf("posting state = %q, want unposted", result.PostingState)
	}
	if result.Policy.ID != "operator-selection-policy" || result.Policy.Version != "v1" {
		t.Fatalf("policy ref lost: %+v", result.Policy)
	}
}

// TestOperatorCostSelectionChoosesQProvisional proves Q can be a provisional
// operator view when P/S are absent.
func TestOperatorCostSelectionChoosesQProvisional(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(
		operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false),
		operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, true, false, false),
	)
	result, err := SelectOperatorCost(policy, operatorCostTestInput(t,
		operatorCostTestCandidate(t, OperatorCostBasisE, "e", "1.00", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
	))
	if err != nil {
		t.Fatalf("SelectOperatorCost: %v", err)
	}
	if result.Status != OperatorCostSelectionStatusProvisional || result.Basis != OperatorCostBasisQ {
		t.Fatalf("selection = %s/%s, want provisional/q", result.Status, result.Basis)
	}
	if result.PostingState != OperatorCostPostingPending {
		t.Fatalf("posting state = %q, want pending for provisional selection", result.PostingState)
	}
	if result.Reason != OperatorCostReasonNone {
		t.Fatalf("reason = %q, want none", result.Reason)
	}
}

// TestOperatorCostSelectionAttemptedMissingStaysUnknown proves attempted or
// unknown work without payable evidence is never a reconciled zero.
func TestOperatorCostSelectionAttemptedMissingStaysUnknown(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))

	t.Run("no candidates", func(t *testing.T) {
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusUnknown || result.Reason != OperatorCostReasonNoPayableEvidence {
			t.Fatalf("selection = %s/%s, want unknown/no_payable_evidence", result.Status, result.Reason)
		}
		if result.Amount != nil {
			t.Fatalf("unknown selection must not carry an amount: %+v", result.Amount)
		}
		if result.KnownZeroBasis != "" {
			t.Fatalf("unknown selection must not claim a known-zero basis: %q", result.KnownZeroBasis)
		}
	})

	t.Run("only partial evidence without provisional rule", func(t *testing.T) {
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t,
			operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessPartial, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusUnknown {
			t.Fatalf("selection = %s, want unknown for incomplete evidence", result.Status)
		}
		if result.Amount != nil {
			t.Fatalf("incomplete selection must not carry an amount: %+v", result.Amount)
		}
	})
}

// TestOperatorCostSelectionKnownZeroRequiresExplicitPolicy proves known_zero
// is only produced for explicit never-started/not-billable provenance that the
// policy authorizes.
func TestOperatorCostSelectionKnownZeroRequiresExplicitPolicy(t *testing.T) {
	t.Parallel()

	authorized := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
	authorized.KnownZeroProvenance = []OperatorCostProvenance{OperatorCostProvenanceNeverStarted, OperatorCostProvenanceNotBillable}

	t.Run("authorized never started", func(t *testing.T) {
		input := operatorCostTestInput(t)
		input.Provenance = OperatorCostProvenanceNeverStarted
		result, err := SelectOperatorCost(authorized, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusKnownZero || result.Reason != OperatorCostReasonKnownZeroAuthorized {
			t.Fatalf("selection = %s/%s, want known_zero/known_zero_authorized", result.Status, result.Reason)
		}
		if result.KnownZeroBasis != OperatorCostProvenanceNeverStarted {
			t.Fatalf("known zero basis = %q, want never_started", result.KnownZeroBasis)
		}
		if result.Amount == nil || result.Amount.Decimal == nil || result.Amount.Decimal.CanonicalString() != "0/0" {
			t.Fatalf("known zero amount = %+v, want exact zero", result.Amount)
		}
	})

	t.Run("unauthorized never started stays unknown", func(t *testing.T) {
		policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
		input := operatorCostTestInput(t)
		input.Provenance = OperatorCostProvenanceNeverStarted
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusUnknown || result.Reason != OperatorCostReasonKnownZeroNotAuthorized {
			t.Fatalf("selection = %s/%s, want unknown/known_zero_not_authorized", result.Status, result.Reason)
		}
		if result.Amount != nil {
			t.Fatalf("unauthorized known zero must not carry an amount: %+v", result.Amount)
		}
	})
}

// TestOperatorCostSelectionPayerTreatment proves BYOK and unallocated payers
// never become operator payables.
func TestOperatorCostSelectionPayerTreatment(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
	providerCandidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})

	t.Run("customer byok is not operator payable", func(t *testing.T) {
		input := operatorCostTestInput(t, providerCandidate)
		input.PayerClass = OperatorCostPayerCustomerBYOK
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusNotOperatorPayable || result.Reason != OperatorCostReasonPayerCustomerBYOK {
			t.Fatalf("selection = %s/%s, want not_operator_payable/payer_customer_byok", result.Status, result.Reason)
		}
		if result.Amount != nil || len(result.Candidates) != 1 {
			t.Fatalf("BYOK selection must keep alternatives and no amount: %+v", result)
		}
	})

	t.Run("customer-paid provider charge is not operator payable", func(t *testing.T) {
		customer := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-1"})
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t, customer))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusUnknown || result.Amount != nil {
			t.Fatalf("customer-paid charge must not become operator payable: %+v", result)
		}
	})

	t.Run("unallocated payer stays unknown", func(t *testing.T) {
		input := operatorCostTestInput(t, providerCandidate)
		input.PayerClass = OperatorCostPayerUnallocated
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusUnknown || result.Reason != OperatorCostReasonPayerUnallocated {
			t.Fatalf("selection = %s/%s, want unknown/payer_unallocated", result.Status, result.Reason)
		}
		if result.Amount != nil {
			t.Fatalf("unallocated payer must not produce an amount: %+v", result.Amount)
		}
	})
}

// TestOperatorCostSelectionCurrencyAndFrozenFX proves currency mismatches are
// incomparable unless both sides share one explicit frozen FX basis identity
// with direction and exact rate, and that a converted selection separates the
// native and view-currency amounts.
func TestOperatorCostSelectionCurrencyAndFrozenFX(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
	eurBasis := func(rate string) *OperatorCostFXBasis {
		return &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD", Rate: toleranceLimit(t, rate)}
	}

	t.Run("mismatch without fx is incomparable", func(t *testing.T) {
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Currency = "EUR"
		candidate.Amount = operatorCostTestAmount(t, "EUR", "1.32")
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t, candidate))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusIncomparable || result.Reason != OperatorCostReasonCurrencyMismatch {
			t.Fatalf("selection = %s/%s, want incomparable/currency_mismatch", result.Status, result.Reason)
		}
		if result.Amount != nil {
			t.Fatalf("incomparable currency must not select an amount: %+v", result.Amount)
		}
	})

	t.Run("shared frozen fx basis permits exact converted selection", func(t *testing.T) {
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Currency = "EUR"
		candidate.Amount = operatorCostTestAmount(t, "EUR", "1.32")
		candidate.FX = eurBasis("0.92")
		input := operatorCostTestInput(t, candidate)
		input.FX = eurBasis("0.92")
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusFinal || result.Basis != OperatorCostBasisP {
			t.Fatalf("selection = %s/%s, want final/p with shared FX", result.Status, result.Basis)
		}
		if result.FX == nil || result.FX.ID != "fx-eur-usd" || result.FX.Version != "v1" ||
			result.FX.FromCurrency != "EUR" || result.FX.ToCurrency != "USD" || result.FX.Rate == nil {
			t.Fatalf("selected FX basis not retained with direction: %+v", result.FX)
		}
		if result.Amount == nil || result.Amount.Currency != result.Currency {
			t.Fatalf("view amount currency = %+v, want %s and never an unconverted foreign amount", result.Amount, result.Currency)
		}
		if result.Amount.Decimal == nil || result.Amount.Decimal.CanonicalString() != "12144/4" {
			t.Fatalf("converted amount = %+v, want exact 1.2144 USD", result.Amount)
		}
		if result.NativeAmount == nil || result.NativeAmount.Currency != "EUR" ||
			result.NativeAmount.Decimal == nil || result.NativeAmount.Decimal.CanonicalString() != "132/2" {
			t.Fatalf("native amount = %+v, want exact 1.32 EUR preserved", result.NativeAmount)
		}

		// Mutating caller FX or candidate memory after selection must not change
		// or re-derive the result.
		candidate.FX.Rate = toleranceLimit(t, "9.99")
		input.FX.Rate = toleranceLimit(t, "9.99")
		if result.FX.Rate == nil || result.FX.Rate.CanonicalString() != "92/2" {
			t.Fatalf("result aliased caller FX memory: %+v", result.FX)
		}
		if result.Amount.Decimal.CanonicalString() != "12144/4" || result.NativeAmount.Decimal.CanonicalString() != "132/2" {
			t.Fatalf("result aliased caller amount memory after FX mutation: %+v", result)
		}
	})

	t.Run("same identity different exact rate is incomparable", func(t *testing.T) {
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Currency = "EUR"
		candidate.Amount = operatorCostTestAmount(t, "EUR", "1.32")
		candidate.FX = eurBasis("0.92")
		input := operatorCostTestInput(t, candidate)
		input.FX = eurBasis("0.93")
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusIncomparable || result.Reason != OperatorCostReasonCurrencyMismatch {
			t.Fatalf("selection = %s/%s, want incomparable/currency_mismatch", result.Status, result.Reason)
		}
		if result.Amount != nil || result.NativeAmount != nil {
			t.Fatalf("rate mismatch must not select an amount: %+v", result)
		}
	})

	t.Run("reversed direction is incomparable", func(t *testing.T) {
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Currency = "EUR"
		candidate.Amount = operatorCostTestAmount(t, "EUR", "1.32")
		reversed := &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "USD", ToCurrency: "EUR", Rate: toleranceLimit(t, "1.08")}
		input := operatorCostTestInput(t, candidate)
		input.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "USD", ToCurrency: "EUR", Rate: toleranceLimit(t, "1.08")}
		// Candidate direction is bound to its native currency, so this input is
		// rejected before it can select a mismatched amount.
		_, err := SelectOperatorCost(policy, input)
		if !errors.Is(err, ErrOperatorCostSelectionInput) {
			t.Fatalf("candidate with reversed FX direction error = %v, want ErrOperatorCostSelectionInput", err)
		}
		_ = reversed
	})

	t.Run("destination mismatch is incomparable", func(t *testing.T) {
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Currency = "EUR"
		candidate.Amount = operatorCostTestAmount(t, "EUR", "1.32")
		candidate.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "GBP", Rate: toleranceLimit(t, "0.85")}
		input := operatorCostTestInput(t, candidate)
		input.FX = eurBasis("0.92")
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusIncomparable || result.Reason != OperatorCostReasonCurrencyMismatch {
			t.Fatalf("selection = %s/%s, want incomparable/currency_mismatch", result.Status, result.Reason)
		}
	})

	t.Run("input fx must convert into the view currency", func(t *testing.T) {
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		input := operatorCostTestInput(t, candidate)
		input.FX = &OperatorCostFXBasis{ID: "fx-eur-gbp", Version: "v1", FromCurrency: "EUR", ToCurrency: "GBP", Rate: toleranceLimit(t, "0.85")}
		if _, err := SelectOperatorCost(policy, input); !errors.Is(err, ErrOperatorCostSelectionInput) {
			t.Fatalf("error = %v, want ErrOperatorCostSelectionInput", err)
		}
	})
}

// TestOperatorCostSelectionRejectsUnboundedFX proves FX material must be
// explicit, directed and bounded.
func TestOperatorCostSelectionRejectsUnboundedFX(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
	baseCandidate := func() OperatorCostCandidate {
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Currency = "EUR"
		candidate.Amount = operatorCostTestAmount(t, "EUR", "1.32")
		return candidate
	}

	cases := map[string]func(*OperatorCostCandidate, *OperatorCostSelectionInput){
		"nil rate": func(c *OperatorCostCandidate, in *OperatorCostSelectionInput) {
			c.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD"}
			in.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD"}
		},
		"zero rate": func(c *OperatorCostCandidate, in *OperatorCostSelectionInput) {
			c.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD", Rate: toleranceLimit(t, "0")}
			in.FX = c.FX
		},
		"negative rate": func(c *OperatorCostCandidate, in *OperatorCostSelectionInput) {
			c.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD", Rate: toleranceLimit(t, "-0.92")}
			in.FX = c.FX
		},
		"unbounded rate": func(c *OperatorCostCandidate, in *OperatorCostSelectionInput) {
			rate := metering.Decimal{Coefficient: strings.Repeat("9", 40)}
			c.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD", Rate: &rate}
			in.FX = c.FX
		},
		"missing direction": func(c *OperatorCostCandidate, in *OperatorCostSelectionInput) {
			c.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", Rate: toleranceLimit(t, "0.92")}
			in.FX = c.FX
		},
		"same from and to": func(c *OperatorCostCandidate, in *OperatorCostSelectionInput) {
			c.FX = &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "EUR", Rate: toleranceLimit(t, "0.92")}
			in.FX = c.FX
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := baseCandidate()
			input := operatorCostTestInput(t, candidate)
			mutate(&input.Candidates[0], &input)
			if _, err := SelectOperatorCost(policy, input); !errors.Is(err, ErrOperatorCostSelectionInput) {
				t.Fatalf("error = %v, want ErrOperatorCostSelectionInput", err)
			}
		})
	}
}

// TestOperatorCostSelectionRequiresExplicitOperatorPayer proves empty,
// unknown, unallocated and customer payer classifications never satisfy an
// operator-payable rule for either provider (P/S) or locally rated (E/Q)
// candidates.
func TestOperatorCostSelectionRequiresExplicitOperatorPayer(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(
		operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false),
		operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, true, false, false),
		operatorCostTestRule("e-provisional", OperatorCostBasisE, OperatorCostSelectionStatusProvisional, true, false, false),
	)
	payerCases := []struct {
		name  string
		payer metering.PaymentParty
	}{
		{name: "empty", payer: metering.PaymentParty{}},
		{name: "explicit unknown", payer: metering.PaymentParty{Kind: metering.PaymentPartyUnknown}},
		{name: "unallocated", payer: metering.PaymentParty{Kind: metering.PaymentPartyUnallocated}},
		{name: "customer", payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-1"}},
	}
	for _, tc := range payerCases {
		t.Run("P with "+tc.name+" payer stays unknown", func(t *testing.T) {
			result, err := SelectOperatorCost(policy, operatorCostTestInput(t,
				operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, tc.payer),
			))
			if err != nil {
				t.Fatalf("SelectOperatorCost: %v", err)
			}
			if result.Status != OperatorCostSelectionStatusUnknown || result.Reason != OperatorCostReasonPayerNotOperator {
				t.Fatalf("selection = %s/%s, want unknown/payer_not_operator", result.Status, result.Reason)
			}
			if result.Amount != nil {
				t.Fatalf("%s payer must not produce an operator-payable amount: %+v", tc.name, result.Amount)
			}
		})
		t.Run("Q with "+tc.name+" payer stays unknown", func(t *testing.T) {
			result, err := SelectOperatorCost(policy, operatorCostTestInput(t,
				operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, tc.payer),
			))
			if err != nil {
				t.Fatalf("SelectOperatorCost: %v", err)
			}
			if result.Basis != OperatorCostBasisNone {
				t.Fatalf("selection basis = %q, want none for %s payer", result.Basis, tc.name)
			}
			if result.Amount != nil {
				t.Fatalf("%s payer must not produce an operator-payable amount: %+v", tc.name, result.Amount)
			}
		})
		t.Run("E with "+tc.name+" payer stays unknown", func(t *testing.T) {
			result, err := SelectOperatorCost(policy, operatorCostTestInput(t,
				operatorCostTestCandidate(t, OperatorCostBasisE, "e", "1.00", economics.CompletenessComplete, tc.payer),
			))
			if err != nil {
				t.Fatalf("SelectOperatorCost: %v", err)
			}
			if result.Amount != nil {
				t.Fatalf("%s payer must not produce an operator-payable amount: %+v", tc.name, result.Amount)
			}
		})
	}

	t.Run("explicit operator payer passes", func(t *testing.T) {
		operatorPayer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
		for _, basis := range []OperatorCostSelectionBasis{OperatorCostBasisP, OperatorCostBasisQ, OperatorCostBasisE} {
			candidate := operatorCostTestCandidate(t, basis, string(basis), "1.10", economics.CompletenessComplete, operatorPayer)
			result, err := SelectOperatorCost(policy, operatorCostTestInput(t, candidate))
			if err != nil {
				t.Fatalf("SelectOperatorCost(%s): %v", basis, err)
			}
			if result.Basis != basis || result.Amount == nil {
				t.Fatalf("basis %s = %s/%+v, want selected with amount", basis, result.Basis, result.Amount)
			}
		}
	})

	t.Run("rule without payer requirement may use an absent payer", func(t *testing.T) {
		policy := operatorCostTestPolicy(
			operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, false, false, false),
		)
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t,
			operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, metering.PaymentParty{}),
		))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Basis != OperatorCostBasisQ || result.Amount == nil {
			t.Fatalf("explicit no-payer policy must still select locally rated Q: %+v", result)
		}
	})

	t.Run("customer payer never passes even without a payer rule", func(t *testing.T) {
		policy := operatorCostTestPolicy(
			operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, false, false, false),
		)
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t,
			operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-1"}),
		))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Amount != nil || result.Status != OperatorCostSelectionStatusUnknown {
			t.Fatalf("customer payer became operator payable: %+v", result)
		}
	})
}

// TestOperatorCostSelectionRequiresDurableReconciliationRef proves a durable
// id/version/fingerprint reference is mandatory and retained for final
// choices, with no implicit ephemeral mode.
func TestOperatorCostSelectionRequiresDurableReconciliationRef(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
	candidate := func() OperatorCostCandidate {
		return operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
	}

	t.Run("empty ref is rejected", func(t *testing.T) {
		input := operatorCostTestInput(t, candidate())
		input.Reconciliation.Ref = OperatorCostReconciliationRef{}
		if _, err := SelectOperatorCost(policy, input); !errors.Is(err, ErrOperatorCostSelectionInput) {
			t.Fatalf("error = %v, want ErrOperatorCostSelectionInput", err)
		}
	})

	t.Run("partial refs are rejected", func(t *testing.T) {
		for name, ref := range map[string]OperatorCostReconciliationRef{
			"id only":           {ID: "reconciliation-1"},
			"version only":      {Version: 1},
			"fingerprint":       {Fingerprint: strings.Repeat("f", 64)},
			"bad fingerprint":   {ID: "reconciliation-1", Version: 1, Fingerprint: "not-a-fingerprint"},
			"short fingerprint": {ID: "reconciliation-1", Version: 1, Fingerprint: strings.Repeat("f", 32)},
		} {
			input := operatorCostTestInput(t, candidate())
			input.Reconciliation.Ref = ref
			if _, err := SelectOperatorCost(policy, input); !errors.Is(err, ErrOperatorCostSelectionInput) {
				t.Fatalf("%s: error = %v, want ErrOperatorCostSelectionInput", name, err)
			}
		}
	})

	t.Run("final selection retains the durable ref", func(t *testing.T) {
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t, candidate()))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusFinal {
			t.Fatalf("status = %q, want final", result.Status)
		}
		if result.Reconciliation.ID == "" || result.Reconciliation.Version == 0 ||
			len(result.Reconciliation.Fingerprint) != 64 {
			t.Fatalf("final selection lost its durable ref: %+v", result.Reconciliation)
		}
	})
}

// TestOperatorCostSelectionKeepsStatesSeparate proves comparison status,
// evidence completeness, selection status/basis and posting state remain
// distinct fields.
func TestOperatorCostSelectionKeepsStatesSeparate(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(
		operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false),
		operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, true, true, false),
	)

	t.Run("partial evidence needs a provisional rule", func(t *testing.T) {
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t,
			operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessPartial, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
			operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessPartial, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusProvisional || result.Basis != OperatorCostBasisQ {
			t.Fatalf("selection = %s/%s, want provisional/q", result.Status, result.Basis)
		}
		if result.ComparisonStatus != ReconciliationStatusDiscrepant || !result.ComparisonComplete {
			t.Fatalf("comparison state was merged into selection: %+v", result)
		}
		if result.PostingState != OperatorCostPostingPending {
			t.Fatalf("posting state = %q, want pending", result.PostingState)
		}
	})

	t.Run("comparison conflict fails closed", func(t *testing.T) {
		input := operatorCostTestInput(t,
			operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		)
		input.Reconciliation.Status = ReconciliationStatusConflict
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusConflict || result.Reason != OperatorCostReasonComparisonConflict {
			t.Fatalf("selection = %s/%s, want conflict/comparison_conflict", result.Status, result.Reason)
		}
		if result.Amount != nil || len(result.Candidates) != 1 {
			t.Fatalf("conflict must preserve alternatives and select nothing: %+v", result)
		}
	})

	t.Run("incomparable comparison stays visible when Q is provisional", func(t *testing.T) {
		policy := operatorCostTestPolicy(
			operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, true, false, false),
		)
		input := operatorCostTestInput(t,
			operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		)
		input.Reconciliation.Status = ReconciliationStatusIncomparable
		input.Reconciliation.Complete = false
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusProvisional || result.Basis != OperatorCostBasisQ {
			t.Fatalf("selection = %s/%s, want provisional/q", result.Status, result.Basis)
		}
		if result.ComparisonStatus != ReconciliationStatusIncomparable || result.ComparisonComplete {
			t.Fatalf("comparison state lost: %+v", result)
		}
	})

	t.Run("comparable comparison required by rule", func(t *testing.T) {
		policy := operatorCostTestPolicy(
			operatorCostTestRule("q-final", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, true, false, true),
		)
		input := operatorCostTestInput(t,
			operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		)
		input.Reconciliation.Status = ReconciliationStatusIncomparable
		input.Reconciliation.Complete = false
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Basis != OperatorCostBasisNone || result.Amount != nil {
			t.Fatalf("rule requiring comparable comparison selected without one: %+v", result)
		}
		if result.Status != OperatorCostSelectionStatusUnknown || result.Reason != OperatorCostReasonIncomparableComparison {
			t.Fatalf("selection = %s/%s, want unknown/incomparable_comparison", result.Status, result.Reason)
		}
	})
}

// TestOperatorCostSelectionCompatibility proves scope, coverage and context
// must match before P or S can be selected.
func TestOperatorCostSelectionCompatibility(t *testing.T) {
	t.Parallel()

	policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))

	t.Run("scope mismatch is skipped", func(t *testing.T) {
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Scope = "call:other"
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t, candidate))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusIncomparable || result.Reason != OperatorCostReasonCandidateIncompatible {
			t.Fatalf("selection = %s/%s, want incomparable/candidate_incompatible", result.Status, result.Reason)
		}
	})

	t.Run("compatible provisional fallback is still selectable", func(t *testing.T) {
		policy := operatorCostTestPolicy(
			operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false),
			operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, true, false, false),
		)
		provider := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		provider.CoverageKey = "coverage:other"
		quantity := operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		result, err := SelectOperatorCost(policy, operatorCostTestInput(t, provider, quantity))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if result.Status != OperatorCostSelectionStatusProvisional || result.Basis != OperatorCostBasisQ {
			t.Fatalf("selection = %s/%s, want provisional/q fallback", result.Status, result.Basis)
		}
	})
}

// TestOperatorCostSelectionDeterminismAndFailClosed covers policy validation,
// deterministic ordering, duplicate roles, bounds and clone independence.
func TestOperatorCostSelectionDeterminismAndFailClosed(t *testing.T) {
	t.Parallel()

	t.Run("policy validation", func(t *testing.T) {
		valid := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
		if err := valid.Validate(); err != nil {
			t.Fatalf("valid policy rejected: %v", err)
		}
		cases := map[string]func(*OperatorCostSelectionPolicy){
			"version":            func(p *OperatorCostSelectionPolicy) { p.Version = 99 },
			"missing ref":        func(p *OperatorCostSelectionPolicy) { p.Ref.ID = "" },
			"no rules":           func(p *OperatorCostSelectionPolicy) { p.Rules = nil },
			"bad basis":          func(p *OperatorCostSelectionPolicy) { p.Rules[0].Basis = "x" },
			"bad status":         func(p *OperatorCostSelectionPolicy) { p.Rules[0].Status = OperatorCostSelectionStatusKnownZero },
			"final partial rule": func(p *OperatorCostSelectionPolicy) { p.Rules[0].AllowPartialEvidence = true },
			"bad known zero":     func(p *OperatorCostSelectionPolicy) { p.KnownZeroProvenance = []OperatorCostProvenance{"maybe"} },
			"duplicate known zero": func(p *OperatorCostSelectionPolicy) {
				p.KnownZeroProvenance = []OperatorCostProvenance{OperatorCostProvenanceNeverStarted, OperatorCostProvenanceNeverStarted}
			},
		}
		for name, mutate := range cases {
			policy := valid
			policy.Rules = append([]OperatorCostSelectionRule(nil), valid.Rules...)
			mutate(&policy)
			if err := policy.Validate(); !errors.Is(err, ErrOperatorCostSelectionInput) {
				t.Fatalf("%s: error = %v, want ErrOperatorCostSelectionInput", name, err)
			}
		}
		duplicate := valid
		duplicate.Rules = append(duplicate.Rules, duplicate.Rules[0])
		if err := duplicate.Validate(); !errors.Is(err, ErrOperatorCostSelectionInput) {
			t.Fatalf("duplicate rule id error = %v, want ErrOperatorCostSelectionInput", err)
		}
	})

	t.Run("deterministic regardless of candidate order", func(t *testing.T) {
		policy := operatorCostTestPolicy(
			operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false),
			operatorCostTestRule("q-provisional", OperatorCostBasisQ, OperatorCostSelectionStatusProvisional, true, false, false),
		)
		e := operatorCostTestCandidate(t, OperatorCostBasisE, "e", "1.00", economics.CompletenessComplete, metering.PaymentParty{})
		q := operatorCostTestCandidate(t, OperatorCostBasisQ, "q", "1.10", economics.CompletenessComplete, metering.PaymentParty{})
		p := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		first, err := SelectOperatorCost(policy, operatorCostTestInput(t, e, q, p))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		second, err := SelectOperatorCost(policy, operatorCostTestInput(t, p, e, q))
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("selection depends on candidate order:\nfirst=%+v\nsecond=%+v", first, second)
		}
	})

	t.Run("duplicate roles fail closed", func(t *testing.T) {
		policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
		_, err := SelectOperatorCost(policy, operatorCostTestInput(t,
			operatorCostTestCandidate(t, OperatorCostBasisP, "p-one", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
			operatorCostTestCandidate(t, OperatorCostBasisP, "p-two", "1.33", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		))
		if !errors.Is(err, ErrOperatorCostSelectionConflict) {
			t.Fatalf("error = %v, want ErrOperatorCostSelectionConflict", err)
		}
	})

	t.Run("candidate bound fails closed", func(t *testing.T) {
		policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
		candidates := make([]OperatorCostCandidate, 0, MaxOperatorCostCandidates+1)
		for i := 0; i <= MaxOperatorCostCandidates; i++ {
			candidates = append(candidates, operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}))
		}
		if _, err := SelectOperatorCost(policy, operatorCostTestInput(t, candidates...)); !errors.Is(err, ErrOperatorCostSelectionInput) {
			t.Fatalf("error = %v, want ErrOperatorCostSelectionInput", err)
		}
	})

	t.Run("malformed candidate fails closed", func(t *testing.T) {
		policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
		candidate := operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator})
		candidate.Amount = nil
		if _, err := SelectOperatorCost(policy, operatorCostTestInput(t, candidate)); !errors.Is(err, ErrOperatorCostSelectionInput) {
			t.Fatalf("error = %v, want ErrOperatorCostSelectionInput", err)
		}
	})

	t.Run("result does not alias caller input", func(t *testing.T) {
		policy := operatorCostTestPolicy(operatorCostTestRule("p-final", OperatorCostBasisP, OperatorCostSelectionStatusFinal, true, false, false))
		candidates := []OperatorCostCandidate{
			operatorCostTestCandidate(t, OperatorCostBasisP, "p", "1.32", economics.CompletenessComplete, metering.PaymentParty{Kind: metering.PaymentPartyOperator}),
		}
		input := operatorCostTestInput(t, candidates...)
		result, err := SelectOperatorCost(policy, input)
		if err != nil {
			t.Fatalf("SelectOperatorCost: %v", err)
		}
		candidates[0].Amount = operatorCostTestAmount(t, "USD", "9.99")
		input.Candidates = nil
		if result.Candidates[0].Amount == nil || result.Candidates[0].Amount.Decimal.CanonicalString() != "132/2" {
			t.Fatalf("result aliased caller candidate memory: %+v", result.Candidates[0])
		}
		if result.Amount == nil || result.Amount.Decimal.CanonicalString() != "132/2" {
			t.Fatalf("result aliased caller amount memory: %+v", result.Amount)
		}
	})
}
