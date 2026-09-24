package billing

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.3B durable selected-cost adjustment contract fixtures. The store
// boundary consumes one frozen selected valuation plus a caller CAS read; the
// domain planner remains pure and the SQL adapter owns the transaction.

func TestSelectedCostAdjustmentInputNormalize(t *testing.T) {
	t.Parallel()

	subject := selectedCostTestSubject(t)
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-adjustment", 1), "10")

	t.Run("valid input trims and keeps identity", func(t *testing.T) {
		input := SelectedCostAdjustmentInput{
			AccountID: "  " + selectedCostTestAccount + "  ",
			CallID:    selectedCostTestCallID(t),
			HeadKey:   " " + selectedCostTestHeadKey,
			Subject:   subject,
			Expected:  SelectedCostHeadExpectation{},
			Selected:  selected,
		}
		normalized, err := input.Normalize()
		require.NoError(t, err)
		require.Equal(t, selectedCostTestAccount, normalized.AccountID)
		require.Equal(t, selectedCostTestHeadKey, normalized.HeadKey)
		require.Equal(t, subject, normalized.Subject)
		require.True(t, selected.IdentityEqual(normalized.Selected))
	})

	t.Run("malformed identity and CAS reads fail closed", func(t *testing.T) {
		previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-previous", 1), "10")
		cases := map[string]func(*SelectedCostAdjustmentInput){
			"missing account": func(in *SelectedCostAdjustmentInput) { in.AccountID = " " },
			"missing call":    func(in *SelectedCostAdjustmentInput) { in.CallID = BillingCallID("") },
			"missing head":    func(in *SelectedCostAdjustmentInput) { in.HeadKey = "" },
			"blank head":      func(in *SelectedCostAdjustmentInput) { in.HeadKey = "   " },
			"foreign subject kind": func(in *SelectedCostAdjustmentInput) {
				in.Subject = metering.SubjectRef{Kind: metering.SubjectALeg, StoreID: selectedCostTestStore, ALegID: "a-leg-13-3"}
			},
			"subject account mismatch": func(in *SelectedCostAdjustmentInput) {
				in.Subject.AccountID = "other-account"
			},
			"subject call mismatch": func(in *SelectedCostAdjustmentInput) {
				in.Subject.BillingCallID = "bc_ffffffffffffffffffffffffffffffff"
			},
			"expected version without previous": func(in *SelectedCostAdjustmentInput) {
				in.Expected = SelectedCostHeadExpectation{Version: 3}
			},
			"expected previous without version": func(in *SelectedCostAdjustmentInput) {
				in.Expected = SelectedCostHeadExpectation{Previous: &previous}
			},
			"invalid selected valuation": func(in *SelectedCostAdjustmentInput) {
				in.Selected = SelectedCostValuation{Ref: selectedCostTestRef(t, "valuation-adjustment", 1), Currency: "USD"}
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				input := SelectedCostAdjustmentInput{
					AccountID: selectedCostTestAccount, CallID: selectedCostTestCallID(t),
					HeadKey: selectedCostTestHeadKey, Subject: subject,
					Expected: SelectedCostHeadExpectation{Version: 1, Previous: &previous},
					Selected: selected,
				}
				mutate(&input)
				_, err := input.Normalize()
				require.ErrorIs(t, err, ErrSelectedCostAdjustmentInvalid)
			})
		}
	})
}

func TestSelectedCostAdjustmentResultVocabularyIsKnown(t *testing.T) {
	t.Parallel()

	// The durable adapter returns planner statuses; only applied, no_op and
	// replay may carry effects.
	result := SelectedCostAdjustmentResult{
		Status:  SelectedCostTransitionPending,
		Reason:  SelectedCostReasonPostedCurrencyMismatch,
		Posting: SelectedCostPostingPending,
	}
	require.True(t, result.Status.IsKnown())
	require.True(t, result.Reason.IsKnown())
	require.True(t, result.Posting.IsKnown())
	require.False(t, SelectedCostHeadTransitionStatus("forged").IsKnown())
	require.True(t, errors.Is(ErrSelectedCostAdjustmentConflict, ErrSelectedCostAdjustmentConflict))
}
