package economics

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.2A public contract tests: scope/query validation, page bounds,
// independent status vocabularies and operator-DTO leakage discipline. These
// DTOs carry no SQL, persistence handles, provider SDKs or raw content, and
// must never appear in frontend responses.

func TestOperatorScopeValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, OperatorScope{StoreID: "store", TenantID: "tenant"}.Validate())
	require.NoError(t, OperatorScope{StoreID: "store", AccountID: "account"}.Validate())

	for _, tc := range []struct {
		name  string
		scope OperatorScope
	}{
		{name: "missing store", scope: OperatorScope{TenantID: "tenant"}},
		{name: "store only without tenant or account", scope: OperatorScope{StoreID: "store"}},
		{name: "blank store", scope: OperatorScope{StoreID: "  ", TenantID: "tenant"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, tc.scope.Validate(), ErrOperatorQueryInvalid)
		})
	}
}

func TestOperatorQueryNormalize(t *testing.T) {
	t.Parallel()

	t.Run("discrepancy defaults limit", func(t *testing.T) {
		t.Parallel()
		got, err := DiscrepancyQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}}.Normalize()
		require.NoError(t, err)
		require.Equal(t, OperatorPageDefaultLimit, got.Limit)
	})

	t.Run("allowance defaults limit and rejects account scope", func(t *testing.T) {
		t.Parallel()
		got, err := AllowanceQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}, ProviderAccountKey: "provider"}.Normalize()
		require.NoError(t, err)
		require.Equal(t, OperatorPageDefaultLimit, got.Limit)

		_, err = AllowanceQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t", AccountID: "a"}, ProviderAccountKey: "provider"}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)

		_, err = AllowanceQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)
	})

	t.Run("statement requires narrow filter and tenant authority", func(t *testing.T) {
		t.Parallel()
		_, err := StatementLineQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)

		_, err = StatementLineQuery{Scope: OperatorScope{StoreID: "s", AccountID: "a"}, ProviderAccountKey: "p"}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)

		got, err := StatementLineQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}, ProviderAccountKey: "p"}.Normalize()
		require.NoError(t, err)
		require.Equal(t, OperatorPageDefaultLimit, got.Limit)
	})

	t.Run("statement line id is a narrow filter", func(t *testing.T) {
		t.Parallel()
		got, err := StatementLineQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}, LineID: "line-1"}.Normalize()
		require.NoError(t, err)
		require.Equal(t, "line-1", got.LineID)
		require.Equal(t, OperatorPageDefaultLimit, got.Limit)
	})

	t.Run("adjustment requires account scope", func(t *testing.T) {
		t.Parallel()
		_, err := AdjustmentQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)

		got, err := AdjustmentQuery{Scope: OperatorScope{StoreID: "s", AccountID: "a"}}.Normalize()
		require.NoError(t, err)
		require.Equal(t, OperatorPageDefaultLimit, got.Limit)
	})

	t.Run("subject kind and id travel together", func(t *testing.T) {
		t.Parallel()
		_, err := DiscrepancyQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}, SubjectKind: metering.SubjectBLeg}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)
	})

	t.Run("over-bound limits fail closed", func(t *testing.T) {
		t.Parallel()
		_, err := DiscrepancyQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}, Limit: OperatorPageMaxLimit + 1}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)
		_, err = AllowanceQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}, ProviderAccountKey: "p", Limit: -1}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)
		_, err = StatementLineQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}, ProviderAccountKey: "p", Limit: OperatorPageMaxLimit + 1}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)
		_, err = AdjustmentQuery{Scope: OperatorScope{StoreID: "s", AccountID: "a"}, Limit: OperatorPageMaxLimit + 1}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)
	})

	t.Run("unknown outcome fails closed", func(t *testing.T) {
		t.Parallel()
		_, err := StatementLineQuery{Scope: OperatorScope{StoreID: "s", TenantID: "t"}, ProviderAccountKey: "p", Outcome: "guessed"}.Normalize()
		require.ErrorIs(t, err, ErrOperatorQueryInvalid)
	})
}

func TestOperatorStatusVocabularies(t *testing.T) {
	t.Parallel()
	for _, status := range []DiscrepancyStatus{
		DiscrepancyMatched, DiscrepancyWithinTolerance, DiscrepancyDiscrepant,
		DiscrepancyPartial, DiscrepancyIncomparable, DiscrepancyMissingLocal,
		DiscrepancyMissingProvider, DiscrepancyPendingStatement, DiscrepancyConflict,
	} {
		require.True(t, status.IsKnown(), "status %q must be known", status)
	}
	require.False(t, DiscrepancyStatus("reconciled").IsKnown())
	require.False(t, DiscrepancyStatus("").IsKnown())

	for _, state := range []MonetaryState{MonetaryComplete, MonetaryPartial, MonetaryIncomparable, MonetaryConflict} {
		require.True(t, state.IsKnown(), "monetary state %q must be known", state)
	}
	require.False(t, MonetaryState("matched").IsKnown(), "quantity and monetary planes must not share a status type")

	for _, reason := range []DiscrepancyReason{
		DiscrepancyReasonNone, DiscrepancyReasonSubjectMismatch, DiscrepancyReasonCurrencyMismatch,
		DiscrepancyReasonTokenizerMismatch, DiscrepancyReasonMissingE, DiscrepancyReasonMissingQ,
		DiscrepancyReasonMissingP, DiscrepancyReasonAmountUnavailable, DiscrepancyReasonTariffMismatch,
		DiscrepancyReasonZeroDenominator, DiscrepancyReasonUnitMismatch,
		DiscrepancyReasonQuantityEvidencePartial, DiscrepancyReasonValuationIncomplete,
	} {
		require.True(t, reason.IsKnown(), "reason %q must be known", reason)
	}
	require.False(t, DiscrepancyReason("guessed").IsKnown())

	for _, status := range []AdjustmentTransitionStatus{
		AdjustmentApplied, AdjustmentNoOp, AdjustmentReplay,
		AdjustmentPending, AdjustmentStale, AdjustmentConflict,
	} {
		require.True(t, status.IsKnown(), "transition status %q must be known", status)
	}
	for _, comparison := range []AdjustmentComparison{
		AdjustmentComparisonNotEvaluated, AdjustmentComparisonComparable,
		AdjustmentComparisonPending, AdjustmentComparisonIncomparable,
	} {
		require.True(t, comparison.IsKnown(), "comparison %q must be known", comparison)
	}
	for _, posting := range []AdjustmentPosting{
		AdjustmentPostingUnposted, AdjustmentPostingPending,
		AdjustmentPostingApplied, AdjustmentPostingReplayed,
	} {
		require.True(t, posting.IsKnown(), "posting %q must be known", posting)
	}
	for _, selection := range []AdjustmentSelection{
		AdjustmentSelectionFinal, AdjustmentSelectionProvisional, AdjustmentSelectionKnownZero,
		AdjustmentSelectionUnknown, AdjustmentSelectionIncomparable, AdjustmentSelectionConflict,
		AdjustmentSelectionNotOperatorPayable,
	} {
		require.True(t, selection.IsKnown(), "selection %q must be known", selection)
	}
}

func operatorTestSubject() metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "store", TenantID: "tenant",
		ALegID: "a-1", BillingCallID: "call-1", BLegID: "b-1",
	}
}

func TestDiscrepancyViewValidate(t *testing.T) {
	t.Parallel()
	view := DiscrepancyView{
		ID: "recon-1", Revision: 1, Subject: operatorTestSubject(),
		PolicyID: "policy", PolicyVersion: "v1",
		QuantityStatus: DiscrepancyDiscrepant, QuantityComplete: true,
		MonetaryState: MonetaryComplete, MonetaryReason: DiscrepancyReasonNone,
		CreatedAt: time.Unix(1_700_030_000, 0).UTC(),
	}
	require.NoError(t, view.Validate())

	empty := DiscrepancyView{}
	require.ErrorIs(t, empty.Validate(), ErrOperatorQueryInvalid)

	neither := view
	neither.QuantityStatus = ""
	neither.MonetaryState = ""
	require.ErrorIs(t, neither.Validate(), ErrOperatorQueryInvalid)

	confused := view
	confused.QuantityStatus = "reconciled"
	require.ErrorIs(t, confused.Validate(), ErrOperatorQueryInvalid)
}

// Phase 16 second-pass Finding 5 public contract: an aggregate-only retained
// reconciliation is a valid independent plane. It must validate, preserve its
// bounded classification outcome and missing-evidence identity, and never be
// coerced into the quantity or monetary planes.
func TestDiscrepancyViewValidateAggregateOnly(t *testing.T) {
	t.Parallel()
	base := DiscrepancyView{
		ID: "recon-agg-1", Revision: 1, Subject: operatorTestSubject(),
		PolicyID: "policy", PolicyVersion: "v1",
		CreatedAt: time.Unix(1_700_030_000, 0).UTC(),
	}

	aggregateOnly := base
	aggregateOnly.Aggregate = &DiscrepancyAggregate{
		Status: DiscrepancyMissingProvider, Complete: false,
		MissingIDs: []string{"end-to-end:USD"},
	}
	require.NoError(t, aggregateOnly.Validate(), "an aggregate-only plane must validate")

	cloned := aggregateOnly.Clone()
	require.NotNil(t, cloned.Aggregate)
	require.Equal(t, []string{"end-to-end:USD"}, cloned.Aggregate.MissingIDs)
	cloned.Aggregate.MissingIDs[0] = "mutated"
	require.Equal(t, "end-to-end:USD", aggregateOnly.Aggregate.MissingIDs[0], "Clone must deep-copy aggregate evidence")

	complete := base
	complete.Aggregate = &DiscrepancyAggregate{Status: DiscrepancyMatched, Complete: true}
	require.NoError(t, complete.Validate(), "a complete aggregate-only plane must validate")

	empty := base
	empty.Aggregate = &DiscrepancyAggregate{}
	require.NoError(t, empty.Validate(), "an empty aggregate plane is still a declared plane")

	unknown := base
	unknown.Aggregate = &DiscrepancyAggregate{Status: "reconciled"}
	require.ErrorIs(t, unknown.Validate(), ErrOperatorQueryInvalid)

	contradictory := base
	contradictory.Aggregate = &DiscrepancyAggregate{Status: DiscrepancyConflict, Complete: true, ConflictIDs: []string{"finding-1"}}
	require.ErrorIs(t, contradictory.Validate(), ErrOperatorQueryInvalid, "a complete aggregate cannot carry a conflict outcome or conflict evidence")

	neither := base
	require.ErrorIs(t, neither.Validate(), ErrOperatorQueryInvalid, "no plane at all stays invalid")
}

func TestStatementLineViewValidate(t *testing.T) {
	t.Parallel()
	matched := StatementLineView{
		StatementID: "stmt-1", LineID: "line-1", Revision: 1, PeriodID: "period-1",
		ProviderAccountKey: "provider", Outcome: StatementLineMatched, ChargeItemID: "charge-1",
		Observation: metering.ObservationRef{StoreID: "store", ObservationID: "obs-1", Revision: 1, PayloadHash: strings.Repeat("c", 64)},
	}
	require.NoError(t, matched.Validate())

	unmatched := StatementLineView{
		StatementID: "stmt-1", LineID: "line-2", Revision: 1, PeriodID: "period-1",
		ProviderAccountKey: "provider", Outcome: StatementLineUnmatched, UnmatchedReason: "account-period aggregate",
	}
	require.NoError(t, unmatched.Validate())

	linkedUnmatched := unmatched
	linkedUnmatched.ChargeItemID = "charge-1"
	require.ErrorIs(t, linkedUnmatched.Validate(), ErrOperatorQueryInvalid)

	reasonedMatched := matched
	reasonedMatched.UnmatchedReason = "aggregate"
	require.ErrorIs(t, reasonedMatched.Validate(), ErrOperatorQueryInvalid)
}

func TestAdjustmentViewValidate(t *testing.T) {
	t.Parallel()
	decimal, err := metering.ParseDecimal("1.32")
	require.NoError(t, err)
	view := AdjustmentView{
		OperationKey: "op-1", LinkKey: "link-1", Fingerprint: strings.Repeat("f", 64),
		AccountID: "acct", CallID: "bc_1", HeadKey: "head-1",
		Status: AdjustmentApplied, Comparison: AdjustmentComparisonComparable,
		Posting: AdjustmentPostingApplied, SelectionStatus: AdjustmentSelectionFinal,
		Current:  AdjustmentValuationRef{ValuationID: "val-1", Revision: 1, InputSetHash: strings.Repeat("d", 64)},
		Currency: "USD", Delta: &ExactAmount{Currency: "USD", Decimal: &decimal},
		CreatedAt: time.Unix(1_700_030_000, 0).UTC(),
	}
	require.NoError(t, view.Validate())

	negative := decimal
	_ = negative
	negDecimal, err := metering.ParseDecimal("-2")
	require.NoError(t, err)
	downward := view
	downward.Delta = &ExactAmount{Currency: "USD", Decimal: &negDecimal}
	require.NoError(t, downward.Validate(), "downward corrections carry signed deltas")

	confused := view
	confused.Status = "posted"
	require.ErrorIs(t, confused.Validate(), ErrOperatorQueryInvalid)
	confused = view
	confused.Posting = "final"
	require.ErrorIs(t, confused.Validate(), ErrOperatorQueryInvalid, "posting must not accept selection vocabulary")
}

func TestOperatorDTOLeakage(t *testing.T) {
	t.Parallel()
	forbidden := []string{"prompt", "response", "payload", "raw", "header", "cookie", "ciphertext", "output_text", "tool_arg", "secret", "authorization", "password"}
	allowlisted := map[string]bool{"PayloadHash": true}
	seen := map[string]bool{}
	var walk func(prefix string, typ reflect.Type)
	walk = func(prefix string, typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		key := typ.PkgPath() + "." + typ.Name()
		if seen[key] {
			return
		}
		seen[key] = true
		if strings.Contains(typ.PkgPath(), "/internal/") {
			t.Fatalf("operator DTO %s must not reference internal package %s", prefix+typ.Name(), typ.PkgPath())
		}
		if len(typ.PkgPath()) > 0 && !strings.HasPrefix(typ.PkgPath(), "github.com/matdev83/go-llm-interactive-proxy/pkg/") && !strings.HasPrefix(typ.PkgPath(), "time") && typ.PkgPath() != "" {
			t.Fatalf("operator DTO %s references unexpected package %s", prefix+typ.Name(), typ.PkgPath())
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if allowlisted[field.Name] {
				continue
			}
			lower := strings.ToLower(field.Name + " " + string(field.Tag))
			for _, word := range forbidden {
				if strings.Contains(lower, word) {
					t.Fatalf("operator DTO %s field %s must not expose raw content", prefix+typ.Name(), field.Name)
				}
			}
			walk(prefix+typ.Name()+".", field.Type)
		}
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(OperatorScope{}),
		reflect.TypeOf(DiscrepancyQuery{}),
		reflect.TypeOf(DiscrepancyPage{}),
		reflect.TypeOf(AllowanceQuery{}),
		reflect.TypeOf(AllowancePage{}),
		reflect.TypeOf(StatementLineQuery{}),
		reflect.TypeOf(StatementLinePage{}),
		reflect.TypeOf(AdjustmentQuery{}),
		reflect.TypeOf(AdjustmentPage{}),
	} {
		walk("", typ)
	}
}
