package billing

import (
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// monetaryTestValuation builds a minimal valid complete valuation with exact
// totals. E/Q carry the frozen local tariff/rater snapshot; P is a provider
// monetary claim without a local tariff.
func monetaryTestValuation(t *testing.T, id string, basis economics.ValuationBasis, totals ...economics.CurrencyTotal) economics.Valuation {
	t.Helper()
	subject := reconciliationSubject()
	valuation := economics.Valuation{
		ID: id, Version: economics.ValuationVersionV2,
		Perspective: metering.PerspectiveOperator,
		Basis:       basis, Subject: subject,
		InputObservations: []metering.ObservationRef{{
			StoreID: subject.StoreID, ObservationID: id + "-observation", Revision: 1, PayloadHash: strings.Repeat("c", 64),
		}},
		InputSetHash: strings.Repeat("d", 64),
		// All roles share the frozen effective measurement context so P-Q/P-E
		// comparability is proved by identity rather than by omission.
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://monetary/qualifiers/v1", ContentHash: strings.Repeat("a", 64)},
		Completeness:         economics.CompletenessComplete,
		CreatedAt:            time.Unix(1_700_001_200, 0).UTC(),
		Totals:               totals,
	}
	if basis != economics.BasisProviderReported {
		valuation.Tariff = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-monetary", Version: "v1"}}
		valuation.TariffContent = &economics.SnapshotContentRef{ContentRef: "catalog://monetary/tariff/v1", ContentHash: strings.Repeat("f", 64)}
		valuation.Rater = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-monetary", Version: "v1"}, RaterID: "reference-rater"}
		valuation.RaterContent = &economics.SnapshotContentRef{ContentRef: "catalog://monetary/rater/v1", ContentHash: strings.Repeat("e", 64)}
	}
	if err := valuation.Validate(); err != nil {
		t.Fatalf("valuation %q invalid: %v", id, err)
	}
	return valuation
}

func monetaryPartialTestValuation(t *testing.T, id string, basis economics.ValuationBasis, totals ...economics.CurrencyTotal) economics.Valuation {
	t.Helper()
	valuation := monetaryTestValuation(t, id, basis, totals...)
	valuation.Completeness = economics.CompletenessPartial
	valuation.Lines = []economics.LineItem{{
		ID: id + "-line", RuleID: "rule-monetary", ItemID: "item-monetary", Unit: metering.UnitToken,
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Status: economics.RatingLineRateMissing,
	}}
	if err := valuation.Validate(); err != nil {
		t.Fatalf("partial valuation %q invalid: %v", id, err)
	}
	return valuation
}

func monetaryDecimalTotal(t *testing.T, currency, amount string) economics.CurrencyTotal {
	t.Helper()
	value, err := metering.ParseDecimal(amount)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", amount, err)
	}
	nanos, err := value.ToNanoUnits()
	if err != nil {
		t.Fatalf("ToNanoUnits(%q): %v", amount, err)
	}
	return economics.CurrencyTotal{
		Currency: currency, Amount: &value,
		RoundedAmount: economics.Money{NanoUnits: nanos, Currency: currency, Present: true},
	}
}

func monetaryRationalTotal(t *testing.T, currency, numerator, denominator string) economics.CurrencyTotal {
	t.Helper()
	n, ok := new(big.Int).SetString(numerator, 10)
	if !ok {
		t.Fatalf("bad numerator %q", numerator)
	}
	d, ok := new(big.Int).SetString(denominator, 10)
	if !ok || d.Sign() <= 0 {
		t.Fatalf("bad denominator %q", denominator)
	}
	rat := new(big.Rat).SetFrac(n, d)
	rounded, err := roundMoney(rat, currency, economics.RoundingHalfEven)
	if err != nil {
		t.Fatalf("roundMoney(%s): %v", rat.RatString(), err)
	}
	return economics.CurrencyTotal{
		Currency: currency, AmountNumerator: rat.Num().String(), AmountDenominator: rat.Denom().String(),
		RoundedAmount: rounded,
	}
}

func monetaryRow(t *testing.T, result MonetaryDiscrepancyComparison, currency string) MonetaryDiscrepancyRow {
	t.Helper()
	for _, row := range result.Rows {
		if row.Currency == currency {
			return row
		}
	}
	t.Fatalf("result has no %s row: %+v", currency, result.Rows)
	return MonetaryDiscrepancyRow{}
}

func assertMonetaryTerm(t *testing.T, label string, term MonetaryDiscrepancyTerm, status MonetaryTermStatus, reason MonetaryDiscrepancyReason) {
	t.Helper()
	if term.Status != status {
		t.Fatalf("%s status = %q, want %q (term=%+v)", label, term.Status, status, term)
	}
	if term.Reason != reason {
		t.Fatalf("%s reason = %q, want %q", label, term.Reason, reason)
	}
}

func assertMonetaryDecimalAmount(t *testing.T, label string, term MonetaryDiscrepancyTerm, currency, canonical string) {
	t.Helper()
	if term.Amount == nil {
		t.Fatalf("%s has no amount: %+v", label, term)
	}
	if term.Amount.Currency != currency {
		t.Fatalf("%s currency = %q, want %q", label, term.Amount.Currency, currency)
	}
	if term.Amount.Decimal == nil {
		t.Fatalf("%s is not a terminating decimal: %+v", label, term.Amount)
	}
	if got := term.Amount.Decimal.CanonicalString(); got != canonical {
		t.Fatalf("%s amount = %s, want %s", label, got, canonical)
	}
	if term.Amount.Numerator != "" || term.Amount.Denominator != "" {
		t.Fatalf("%s mixes decimal and rational representations: %+v", label, term.Amount)
	}
}

func assertMonetaryNoAmount(t *testing.T, label string, term MonetaryDiscrepancyTerm) {
	t.Helper()
	if term.Amount != nil {
		t.Fatalf("%s unexpectedly carries an amount: %+v", label, term.Amount)
	}
	if term.Status == MonetaryTermComplete {
		t.Fatalf("%s was declared complete without a comparable amount", label)
	}
}

func assertMonetaryRationalAmount(t *testing.T, label string, term MonetaryDiscrepancyTerm, numerator, denominator string) {
	t.Helper()
	if term.Amount == nil {
		t.Fatalf("%s has no amount: %+v", label, term)
	}
	if term.Amount.Decimal != nil {
		t.Fatalf("%s unexpectedly has a terminating decimal: %+v", label, term.Amount)
	}
	if term.Amount.Numerator != numerator || term.Amount.Denominator != denominator {
		t.Fatalf("%s rational = %s/%s, want %s/%s", label, term.Amount.Numerator, term.Amount.Denominator, numerator, denominator)
	}
	rat, err := term.Amount.Rat()
	if err != nil {
		t.Fatalf("%s Rat(): %v", label, err)
	}
	if rat.Num().String() != numerator || rat.Denom().String() != denominator {
		t.Fatalf("%s Rat = %s, want %s/%s", label, rat.RatString(), numerator, denominator)
	}
}

// TestMonetaryDiscrepancyDecomposesAcceptanceVector locks the C4 acceptance
// vector: E=1.00, Q=1.10, P=1.32 produces exactly 0.10, 0.22 and 0.32.
func TestMonetaryDiscrepancyDecomposesAcceptanceVector(t *testing.T) {
	t.Parallel()

	result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
		monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
		monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
	}})
	if err != nil {
		t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
	}
	if result.Status != MonetaryDiscrepancyComplete {
		t.Fatalf("status = %q, want complete (result=%+v)", result.Status, result)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %+v, want one currency row", result.Rows)
	}
	row := monetaryRow(t, result, "USD")
	assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
	assertMonetaryDecimalAmount(t, "metering cost effect", row.MeteringCostEffect, "USD", "1/1")
	assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermComplete, MonetaryReasonNone)
	assertMonetaryDecimalAmount(t, "reported price residual", row.ReportedPriceResidual, "USD", "22/2")
	assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermComplete, MonetaryReasonNone)
	assertMonetaryDecimalAmount(t, "end-to-end cost delta", row.EndToEndCostDelta, "USD", "32/2")
	if row.MeteringCostEffect.Cause != MonetaryCauseQuantityDifference {
		t.Fatalf("metering cost effect cause = %q, want quantity_difference", row.MeteringCostEffect.Cause)
	}
	if row.ReportedPriceResidual.Cause != MonetaryCauseSuspectedPricingDifference {
		t.Fatalf("reported price residual cause = %q, want suspected_pricing_difference", row.ReportedPriceResidual.Cause)
	}
	if row.EndToEndCostDelta.Cause != MonetaryCauseNone {
		t.Fatalf("end-to-end cost delta cause = %q, want none", row.EndToEndCostDelta.Cause)
	}
	for _, want := range []struct {
		role   MonetaryDiscrepancyRole
		id     string
		amount string
		num    string
		den    string
	}{
		{MonetaryRoleE, "valuation-e", "1/0", "1", "1"},
		{MonetaryRoleQ, "valuation-q", "11/1", "11", "10"},
		{MonetaryRoleP, "valuation-p", "132/2", "33", "25"},
	} {
		found := false
		for _, evidence := range result.Valuations {
			if evidence.Role != want.role {
				continue
			}
			found = true
			if evidence.Valuation.ID != want.id {
				t.Fatalf("preserved %s valuation id = %q, want %q", want.role, evidence.Valuation.ID, want.id)
			}
			total := evidence.Valuation.Totals[0]
			if total.Amount == nil || total.Amount.CanonicalString() != want.amount {
				t.Fatalf("preserved %s total = %+v, want amount %s", want.role, total, want.amount)
			}
		}
		if !found {
			t.Fatalf("valuation role %q was not preserved", want.role)
		}
	}
}

// TestMonetaryDiscrepancyKeepsMissingTermsAbsent proves that absent E/Q/P
// terms stay absent with a typed reason instead of being zero-filled.
func TestMonetaryDiscrepancyKeepsMissingTermsAbsent(t *testing.T) {
	t.Parallel()

	t.Run("E and Q only", func(t *testing.T) {
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		if result.Status != MonetaryDiscrepancyPartial || result.Reason != MonetaryReasonMissingP {
			t.Fatalf("result = %+v, want partial missing_p", result)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryDecimalAmount(t, "metering cost effect", row.MeteringCostEffect, "USD", "1/1")
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermMissing, MonetaryReasonMissingP)
		assertMonetaryNoAmount(t, "reported price residual", row.ReportedPriceResidual)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermMissing, MonetaryReasonMissingP)
		assertMonetaryNoAmount(t, "end-to-end cost delta", row.EndToEndCostDelta)
	})

	t.Run("provider quantity without money", func(t *testing.T) {
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermMissing, MonetaryReasonMissingE)
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermMissing, MonetaryReasonMissingP)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermMissing, MonetaryReasonMissingP)
		for label, term := range map[string]MonetaryDiscrepancyTerm{
			"metering cost effect": row.MeteringCostEffect, "reported price residual": row.ReportedPriceResidual, "end-to-end cost delta": row.EndToEndCostDelta,
		} {
			assertMonetaryNoAmount(t, label, term)
		}
	})

	t.Run("provider money only", func(t *testing.T) {
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermMissing, MonetaryReasonMissingQ)
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermMissing, MonetaryReasonMissingQ)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermMissing, MonetaryReasonMissingE)
	})

	t.Run("explicit zero is not missing", func(t *testing.T) {
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "0")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "0")),
			monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "0")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		if result.Status != MonetaryDiscrepancyComplete {
			t.Fatalf("status = %q, want complete", result.Status)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryDecimalAmount(t, "metering cost effect", row.MeteringCostEffect, "USD", "0/0")
		assertMonetaryDecimalAmount(t, "reported price residual", row.ReportedPriceResidual, "USD", "0/0")
		assertMonetaryDecimalAmount(t, "end-to-end cost delta", row.EndToEndCostDelta, "USD", "0/0")
		if row.MeteringCostEffect.Cause != MonetaryCauseNone || row.ReportedPriceResidual.Cause != MonetaryCauseNone {
			t.Fatalf("zero deltas must not carry a cause: %+v", row)
		}
	})
}

// TestMonetaryDiscrepancyTypedIncomparability proves that frozen context,
// coverage, payer, subject and currency mismatches are typed incomparable
// outcomes, never matched or zero.
func TestMonetaryDiscrepancyTypedIncomparability(t *testing.T) {
	t.Parallel()

	usd := func(t *testing.T, amount string) economics.CurrencyTotal {
		return monetaryDecimalTotal(t, "USD", amount)
	}

	t.Run("tariff mismatch isolates the E/Q cost effect", func(t *testing.T) {
		e := monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, usd(t, "1.00"))
		e.Tariff.ID = "tariff-other"
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			e,
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, usd(t, "1.10")),
			monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermIncomparable, MonetaryReasonTariffMismatch)
		assertMonetaryNoAmount(t, "metering cost effect", row.MeteringCostEffect)
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermComplete, MonetaryReasonNone)
		if result.Status != MonetaryDiscrepancyIncomparable || result.Reason != MonetaryReasonTariffMismatch {
			t.Fatalf("result = %+v, want incomparable tariff_mismatch", result)
		}
	})

	t.Run("coverage mismatch isolates P terms", func(t *testing.T) {
		provider := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32"))
		provider.CoverageRefs = []metering.ChargeCoverageRef{{
			Ref: metering.ChargeRef{StoreID: reconciliationSubject().StoreID, ObservationID: "charge-observation", Revision: 1, ChargeItemID: "charge-item"}, Relation: metering.CoverageAdditive,
		}}
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, usd(t, "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, usd(t, "1.10")),
			provider,
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermIncomparable, MonetaryReasonCoverageMismatch)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermIncomparable, MonetaryReasonCoverageMismatch)
	})

	t.Run("payer mismatch isolates P terms", func(t *testing.T) {
		provider := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32"))
		provider.Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator}
		e := monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, usd(t, "1.00"))
		e.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-1"}
		q := monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, usd(t, "1.10"))
		q.Payer = e.Payer
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{e, q, provider}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermIncomparable, MonetaryReasonPayerMismatch)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermIncomparable, MonetaryReasonPayerMismatch)
	})

	t.Run("subject mismatch isolates P terms", func(t *testing.T) {
		provider := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32"))
		provider.Subject.BLegID = "b-leg-other"
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, usd(t, "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, usd(t, "1.10")),
			provider,
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermIncomparable, MonetaryReasonSubjectMismatch)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermIncomparable, MonetaryReasonSubjectMismatch)
		if result.Status != MonetaryDiscrepancyIncomparable || result.Reason != MonetaryReasonSubjectMismatch {
			t.Fatalf("result = %+v, want incomparable subject_mismatch", result)
		}
	})

	t.Run("disjoint currencies are incomparable", func(t *testing.T) {
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "EUR", "1.10")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		if len(result.Rows) != 2 || result.Rows[0].Currency != "EUR" || result.Rows[1].Currency != "USD" {
			t.Fatalf("rows = %+v, want sorted EUR and USD", result.Rows)
		}
		for _, currency := range []string{"EUR", "USD"} {
			row := monetaryRow(t, result, currency)
			assertMonetaryTerm(t, currency+" metering cost effect", row.MeteringCostEffect, MonetaryTermIncomparable, MonetaryReasonCurrencyMismatch)
			assertMonetaryNoAmount(t, currency+" metering cost effect", row.MeteringCostEffect)
		}
		if result.Status != MonetaryDiscrepancyIncomparable || result.Reason != MonetaryReasonCurrencyMismatch {
			t.Fatalf("result = %+v, want incomparable currency_mismatch", result)
		}
	})
}

// TestMonetaryDiscrepancyIntegratesQuantityComparison proves that the 12.1
// quantity comparison status downgrades the monetary decomposition when its
// evidence is partial, incomparable or conflicting, without rewriting the
// already exact E/Q/P terms.
// TestMonetaryDiscrepancyPProviderMeasurementContext proves every P-Q/P-E pair
// requires compatible economic scope and effective measurement context before a
// residual or end-to-end delta may be calculated.
func TestMonetaryDiscrepancyPProviderMeasurementContext(t *testing.T) {
	t.Parallel()

	usd := func(t *testing.T, amount string) economics.CurrencyTotal {
		return monetaryDecimalTotal(t, "USD", amount)
	}
	e := func(t *testing.T) economics.Valuation {
		return monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, usd(t, "1.00"))
	}
	q := func(t *testing.T) economics.Valuation {
		return monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, usd(t, "1.10"))
	}

	t.Run("provider scope mismatch isolates P terms", func(t *testing.T) {
		provider := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32"))
		provider.Scope = "call:other"
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{e(t), q(t), provider}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryDecimalAmount(t, "metering cost effect", row.MeteringCostEffect, "USD", "1/1")
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermIncomparable, MonetaryReasonContextMismatch)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermIncomparable, MonetaryReasonContextMismatch)
		assertMonetaryNoAmount(t, "reported price residual", row.ReportedPriceResidual)
		assertMonetaryNoAmount(t, "end-to-end cost delta", row.EndToEndCostDelta)
		if result.Status != MonetaryDiscrepancyIncomparable || result.Reason != MonetaryReasonContextMismatch {
			t.Fatalf("result = %+v, want incomparable context_mismatch", result)
		}
	})

	t.Run("provider effective qualifier mismatch isolates P terms", func(t *testing.T) {
		provider := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32"))
		provider.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "eu"}}
		provider.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "catalog://monetary/qualifiers/eu", ContentHash: strings.Repeat("b", 64)}
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{e(t), q(t), provider}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermIncomparable, MonetaryReasonContextMismatch)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermIncomparable, MonetaryReasonContextMismatch)
		assertMonetaryNoAmount(t, "reported price residual", row.ReportedPriceResidual)
		assertMonetaryNoAmount(t, "end-to-end cost delta", row.EndToEndCostDelta)
	})

	t.Run("provider qualifier snapshot mismatch isolates P terms", func(t *testing.T) {
		provider := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32"))
		provider.QualifierSnapshot = strings.Repeat("d", 64)
		provider.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "catalog://monetary/qualifiers/alt", ContentHash: strings.Repeat("d", 64)}
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{e(t), q(t), provider}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermIncomparable, MonetaryReasonContextMismatch)
		assertMonetaryTerm(t, "end-to-end cost delta", row.EndToEndCostDelta, MonetaryTermIncomparable, MonetaryReasonContextMismatch)
	})

	t.Run("matching context keeps the provider price difference suspected only", func(t *testing.T) {
		provider := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32"))
		evaluation, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{e(t), q(t), provider}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, evaluation, "USD")
		assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryDecimalAmount(t, "reported price residual", row.ReportedPriceResidual, "USD", "22/2")
		if row.ReportedPriceResidual.Cause != MonetaryCauseSuspectedPricingDifference {
			t.Fatalf("residual cause = %q, want suspected_pricing_difference", row.ReportedPriceResidual.Cause)
		}
	})
}

func TestMonetaryDiscrepancyIntegratesQuantityComparison(t *testing.T) {
	t.Parallel()

	acceptanceValuations := func(t *testing.T) []economics.Valuation {
		t.Helper()
		return []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
			monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
		}
	}
	inputKey := reconciliationKey(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	cacheKey := reconciliationKey(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken, metering.DefaultInclusionSchemaID)
	quantityComparison := func(t *testing.T, local, provider ReconciliationEvidenceSet) *ComponentQuantityComparison {
		t.Helper()
		comparison, err := CompareComponentQuantities(local, provider)
		if err != nil {
			t.Fatalf("CompareComponentQuantities: %v", err)
		}
		return &comparison
	}

	t.Run("partial quantity evidence downgrades without rewriting terms", func(t *testing.T) {
		comparison := quantityComparison(t,
			reconciliationSide(reconciliationObservation(t, "monetary-qty-local", metering.OriginLocal, reconciliationMeasure(t, cacheKey, metering.QualityObserved, "tok", "7"))),
			reconciliationSide(reconciliationObservation(t, "monetary-qty-provider", metering.OriginProvider, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100"))),
		)
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: acceptanceValuations(t), QuantityComparison: comparison})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		if result.Status != MonetaryDiscrepancyPartial || result.Reason != MonetaryReasonQuantityEvidencePartial {
			t.Fatalf("result = %+v, want partial quantity_evidence_partial", result)
		}
		if result.Quantity == nil || result.Quantity.Complete {
			t.Fatalf("quantity integration = %+v, want incomplete comparison preserved", result.Quantity)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryDecimalAmount(t, "metering cost effect", row.MeteringCostEffect, "USD", "1/1")
		assertMonetaryDecimalAmount(t, "reported price residual", row.ReportedPriceResidual, "USD", "22/2")
		assertMonetaryDecimalAmount(t, "end-to-end cost delta", row.EndToEndCostDelta, "USD", "32/2")
	})

	t.Run("incomparable quantity evidence downgrades", func(t *testing.T) {
		local := reconciliationSide(reconciliationObservation(t, "monetary-qty-local", metering.OriginLocal, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100")))
		local.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer}
		provider := reconciliationSide(reconciliationObservation(t, "monetary-qty-provider", metering.OriginProvider, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100")))
		provider.Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator}
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: acceptanceValuations(t), QuantityComparison: quantityComparison(t, local, provider)})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		if result.Status != MonetaryDiscrepancyIncomparable || result.Reason != MonetaryReasonQuantityEvidenceIncomparable {
			t.Fatalf("result = %+v, want incomparable quantity_evidence_incomparable", result)
		}
	})

	t.Run("conflicting quantity evidence downgrades to conflict", func(t *testing.T) {
		local := reconciliationSide(
			reconciliationObservation(t, "monetary-qty-dup-1", metering.OriginLocal, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "5")),
			reconciliationObservation(t, "monetary-qty-dup-2", metering.OriginLocal, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "6")),
		)
		provider := reconciliationSide(reconciliationObservation(t, "monetary-qty-provider", metering.OriginProvider, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "5")))
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: acceptanceValuations(t), QuantityComparison: quantityComparison(t, local, provider)})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		if result.Status != MonetaryDiscrepancyConflict || result.Reason != MonetaryReasonQuantityEvidenceConflict {
			t.Fatalf("result = %+v, want conflict quantity_evidence_conflict", result)
		}
	})

	t.Run("compatible quantity discrepancy stays complete", func(t *testing.T) {
		local := reconciliationSide(reconciliationObservation(t, "monetary-qty-local", metering.OriginLocal, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "100")))
		provider := reconciliationSide(reconciliationObservation(t, "monetary-qty-provider", metering.OriginProvider, reconciliationMeasure(t, inputKey, metering.QualityObserved, "tok", "110")))
		comparison := quantityComparison(t, local, provider)
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: acceptanceValuations(t), QuantityComparison: comparison})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		if result.Status != MonetaryDiscrepancyComplete {
			t.Fatalf("result = %+v, want complete", result)
		}
		if result.Quantity == nil || !result.Quantity.Complete || len(result.Quantity.Items) != 1 {
			t.Fatalf("quantity integration = %+v, want one complete discrepant item", result.Quantity)
		}
		if result.Quantity.Items[0].SignedDelta == nil || result.Quantity.Items[0].SignedDelta.CanonicalString() != "10/0" {
			t.Fatalf("quantity item = %+v, want signed delta 10", result.Quantity.Items[0])
		}
	})
}

// TestMonetaryDiscrepancyPreservesEvidenceAndPartialLabels proves that all
// alternative valuations, exact source refs and quality/completeness labels
// survive and that the result does not alias caller-owned input.
func TestMonetaryDiscrepancyPreservesEvidenceAndPartialLabels(t *testing.T) {
	t.Parallel()

	input := []economics.Valuation{
		monetaryPartialTestValuation(t, "valuation-e-partial", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00")),
		monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10")),
		monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32")),
	}
	result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: input})
	if err != nil {
		t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
	}
	row := monetaryRow(t, result, "USD")
	assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermPartial, MonetaryReasonValuationIncomplete)
	assertMonetaryDecimalAmount(t, "metering cost effect", row.MeteringCostEffect, "USD", "1/1")
	assertMonetaryTerm(t, "reported price residual", row.ReportedPriceResidual, MonetaryTermComplete, MonetaryReasonNone)

	var partial *economics.Valuation
	for i := range result.Valuations {
		if result.Valuations[i].Role == MonetaryRoleE {
			partial = &result.Valuations[i].Valuation
		}
	}
	if partial == nil {
		t.Fatal("E valuation was not preserved")
	}
	if partial.Completeness != economics.CompletenessPartial {
		t.Fatalf("E completeness = %q, want partial", partial.Completeness)
	}
	if len(partial.Lines) != 1 || partial.Lines[0].Status != economics.RatingLineRateMissing {
		t.Fatalf("E line quality label lost: %+v", partial.Lines)
	}
	if len(partial.InputObservations) != 1 || partial.InputObservations[0].ObservationID != "valuation-e-partial-observation" {
		t.Fatalf("E source observation refs lost: %+v", partial.InputObservations)
	}

	input[0].ID = "mutated-after-call"
	input[1].Totals[0].Amount = nil
	preserved := false
	for i := range result.Valuations {
		if result.Valuations[i].Role == MonetaryRoleQ && result.Valuations[i].Valuation.Totals[0].Amount != nil {
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("result aliased caller-owned valuation totals")
	}
}

// TestMonetaryDiscrepancyFailsClosedOnRoleConflictsAndBounds covers duplicate
// or unsupported valuation roles, invalid valuations and bounded input.
func TestMonetaryDiscrepancyFailsClosedOnRoleConflictsAndBounds(t *testing.T) {
	t.Parallel()

	usd := func(t *testing.T, amount string) economics.CurrencyTotal {
		return monetaryDecimalTotal(t, "USD", amount)
	}

	t.Run("duplicate role conflicts", func(t *testing.T) {
		_, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e-1", economics.BasisLocalExpected, usd(t, "1.00")),
			monetaryTestValuation(t, "valuation-e-2", economics.BasisLocalExpected, usd(t, "1.00")),
		}})
		if !errors.Is(err, ErrMonetaryDiscrepancyConflict) {
			t.Fatalf("error = %v, want ErrMonetaryDiscrepancyConflict", err)
		}
	})

	t.Run("conflicting role content conflicts", func(t *testing.T) {
		_, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e-1", economics.BasisLocalExpected, usd(t, "1.00")),
			monetaryTestValuation(t, "valuation-e-2", economics.BasisLocalExpected, usd(t, "2.00")),
		}})
		if !errors.Is(err, ErrMonetaryDiscrepancyConflict) {
			t.Fatalf("error = %v, want ErrMonetaryDiscrepancyConflict", err)
		}
	})

	t.Run("unsupported basis fails closed", func(t *testing.T) {
		statement := monetaryTestValuation(t, "valuation-s", economics.BasisStatementReported, usd(t, "1.00"))
		_, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{statement}})
		if !errors.Is(err, ErrMonetaryDiscrepancyRole) {
			t.Fatalf("error = %v, want ErrMonetaryDiscrepancyRole", err)
		}
	})

	t.Run("valuation bound fails closed", func(t *testing.T) {
		_, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, usd(t, "1.00")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, usd(t, "1.10")),
			monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, usd(t, "1.32")),
			monetaryTestValuation(t, "valuation-p-2", economics.BasisProviderReported, usd(t, "1.33")),
		}})
		if !errors.Is(err, ErrMonetaryDiscrepancyInput) {
			t.Fatalf("error = %v, want ErrMonetaryDiscrepancyInput", err)
		}
	})

	t.Run("empty input fails closed", func(t *testing.T) {
		if _, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{}); !errors.Is(err, ErrMonetaryDiscrepancyInput) {
			t.Fatalf("error = %v, want ErrMonetaryDiscrepancyInput", err)
		}
	})

	t.Run("invalid valuation fails closed", func(t *testing.T) {
		invalid := monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, usd(t, "1.00"))
		invalid.ID = ""
		if _, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{invalid}}); !errors.Is(err, ErrMonetaryDiscrepancyInput) {
			t.Fatalf("error = %v, want ErrMonetaryDiscrepancyInput", err)
		}
	})
}

// TestMonetaryDiscrepancyPreservesExactRationalAndChecksOverflow proves that a
// non-terminating exact difference is retained as a reduced rational and that
// an unbounded exact difference fails closed.
func TestMonetaryDiscrepancyPreservesExactRationalAndChecksOverflow(t *testing.T) {
	t.Parallel()

	t.Run("non-terminating difference stays exact", func(t *testing.T) {
		result, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryRationalTotal(t, "USD", "1", "3")),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryRationalTotal(t, "USD", "1", "4")),
		}})
		if err != nil {
			t.Fatalf("DecomposeMonetaryDiscrepancies: %v", err)
		}
		row := monetaryRow(t, result, "USD")
		assertMonetaryTerm(t, "metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
		assertMonetaryRationalAmount(t, "metering cost effect", row.MeteringCostEffect, "-1", "12")
		if row.MeteringCostEffect.Cause != MonetaryCauseQuantityDifference {
			t.Fatalf("metering cost effect cause = %q, want quantity_difference", row.MeteringCostEffect.Cause)
		}
	})

	t.Run("unbounded exact difference fails closed", func(t *testing.T) {
		first := new(big.Int).Exp(big.NewInt(2), big.NewInt(425), nil)
		second := new(big.Int).Exp(big.NewInt(3), big.NewInt(267), nil)
		_, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
			monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryRationalTotal(t, "USD", "1", first.String())),
			monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryRationalTotal(t, "USD", "1", second.String())),
		}})
		if !errors.Is(err, ErrMonetaryDiscrepancyOverflow) {
			t.Fatalf("error = %v, want ErrMonetaryDiscrepancyOverflow", err)
		}
	})
}

// TestMonetaryDiscrepancyDeterministicCurrencyRows proves deterministic role,
// row and term ordering independent of input order and multiple currencies.
func TestMonetaryDiscrepancyDeterministicCurrencyRows(t *testing.T) {
	t.Parallel()

	e := monetaryTestValuation(t, "valuation-e", economics.BasisLocalExpected, monetaryDecimalTotal(t, "USD", "1.00"), monetaryDecimalTotal(t, "EUR", "2.00"))
	q := monetaryTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, monetaryDecimalTotal(t, "USD", "1.10"), monetaryDecimalTotal(t, "EUR", "2.20"))
	p := monetaryTestValuation(t, "valuation-p", economics.BasisProviderReported, monetaryDecimalTotal(t, "USD", "1.32"), monetaryDecimalTotal(t, "EUR", "2.40"))

	first, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{e, q, p}})
	if err != nil {
		t.Fatalf("DecomposeMonetaryDiscrepancies(first): %v", err)
	}
	second, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{p, e, q}})
	if err != nil {
		t.Fatalf("DecomposeMonetaryDiscrepancies(second): %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("decomposition depends on input order:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if len(first.Rows) != 2 || first.Rows[0].Currency != "EUR" || first.Rows[1].Currency != "USD" {
		t.Fatalf("rows = %+v, want sorted EUR then USD", first.Rows)
	}
	if len(first.Rows) > MaxMonetaryDiscrepancyRows {
		t.Fatalf("rows = %d exceed bound %d", len(first.Rows), MaxMonetaryDiscrepancyRows)
	}
	for _, row := range first.Rows {
		assertMonetaryTerm(t, row.Currency+" metering cost effect", row.MeteringCostEffect, MonetaryTermComplete, MonetaryReasonNone)
	}
}
