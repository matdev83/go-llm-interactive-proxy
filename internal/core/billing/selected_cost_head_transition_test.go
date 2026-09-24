package billing

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.3A selected-cost head transition fixtures. The planner is pure: it
// compares the previously posted selected valuation with the new selected
// valuation, computes new-minus-posted only for the same native currency or the
// same explicit frozen FX basis, and otherwise leaves the correction pending
// with no valuation link, journal delta or cost-head transition. Downward
// corrections reverse debit/credit sides instead of posting an invalid
// negative gross amount, and CAS mismatch/racing revisions return typed
// stale/conflict results with zero effects.
//
// Every price below is a synthetic test fixture, not a provider tariff.

const (
	selectedCostTestAccount        = "account-selected-cost"
	selectedCostTestStore          = "store-selected-cost"
	selectedCostTestHeadKey        = "provider-cogs-head-13-3"
	selectedCostTestCallRaw        = "bc_0123456789abcdef0123456789abcdef"
	selectedCostTestCogsLedger     = "inference_provider_cogs"
	selectedCostTestClearingLedger = "provider_payable_clearing"
	selectedCostTestOperationKind  = "provider_call_cogs"
)

func selectedCostTestCallID(t *testing.T) BillingCallID {
	t.Helper()
	id, err := ParseBillingCallID(selectedCostTestCallRaw)
	require.NoError(t, err)
	return id
}

func selectedCostTestSubject(t *testing.T) metering.SubjectRef {
	t.Helper()
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: selectedCostTestStore,
		AccountID: selectedCostTestAccount, ALegID: "a-leg-13-3",
		BillingCallID: selectedCostTestCallRaw, BLegID: "b-leg-13-3",
	}
}

func selectedCostTestRef(t *testing.T, valuation string, revision uint64) SelectedCostValuationRef {
	t.Helper()
	return SelectedCostValuationRef{
		ValuationID: valuation, Revision: revision,
		InputSetHash: fmt.Sprintf("input-set-%s-%d", valuation, revision),
	}
}

func selectedCostTestSelection(t *testing.T, status OperatorCostSelectionStatus, provenance OperatorCostProvenance, currency, amountRaw string, fx *OperatorCostFXBasis, nativeRaw string) OperatorCostSelectionResult {
	t.Helper()
	selection := OperatorCostSelectionResult{Status: status, Provenance: provenance, Currency: currency}
	if amountRaw != "" {
		selection.Amount = operatorCostTestAmount(t, currency, amountRaw)
	}
	if fx != nil {
		selection.FX = fx
		selection.NativeAmount = operatorCostTestAmount(t, fx.FromCurrency, nativeRaw)
	}
	return selection
}

func selectedCostTestValuation(t *testing.T, ref SelectedCostValuationRef, status OperatorCostSelectionStatus, provenance OperatorCostProvenance, currency, amountRaw string, fx *OperatorCostFXBasis, nativeRaw string) SelectedCostValuation {
	t.Helper()
	valuation, err := NewSelectedCostValuation(ref, selectedCostTestSelection(t, status, provenance, currency, amountRaw, fx, nativeRaw))
	require.NoError(t, err)
	return valuation
}

func selectedCostTestUSDValuation(t *testing.T, ref SelectedCostValuationRef, amount string) SelectedCostValuation {
	t.Helper()
	return selectedCostTestValuation(t, ref, OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", amount, nil, "")
}

func selectedCostTestEURBasis(t *testing.T, rate string) *OperatorCostFXBasis {
	t.Helper()
	return &OperatorCostFXBasis{ID: "fx-eur-usd", Version: "v1", FromCurrency: "EUR", ToCurrency: "USD", Rate: toleranceLimit(t, rate)}
}

func selectedCostTestHead(t *testing.T, version uint64, selected *SelectedCostValuation) SelectedCostHead {
	t.Helper()
	return SelectedCostHead{
		AccountID: selectedCostTestAccount, CallID: selectedCostTestCallID(t),
		HeadKey: selectedCostTestHeadKey, Subject: selectedCostTestSubject(t),
		Version: version, Selected: selected,
	}
}

func selectedCostTestInput(t *testing.T, current SelectedCostHead, expected SelectedCostHeadExpectation, selected SelectedCostValuation) SelectedCostHeadTransitionInput {
	t.Helper()
	return SelectedCostHeadTransitionInput{Current: current, Expected: expected, Selected: selected}
}

func selectedCostTestAssertNoEffects(t *testing.T, result SelectedCostHeadTransition) {
	t.Helper()
	assert.Nil(t, result.Delta, "pending/stale/conflict results retain no monetary delta")
	assert.Nil(t, result.Link, "pending/stale/conflict results insert no valuation link")
	assert.Nil(t, result.Journal, "pending/stale/conflict results emit no journal intent")
	assert.Nil(t, result.NextHead, "pending/stale/conflict results transition no cost head")
	assert.False(t, result.HasEffects(), "pending/stale/conflict results must have zero effects")
}

func selectedCostTestAssertJournalEntry(t *testing.T, entry JournalEntry, account string, side JournalSide, nano int64) {
	t.Helper()
	assert.Equal(t, account, entry.LedgerAccount)
	assert.Equal(t, side, entry.Side)
	assert.Equal(t, nano, entry.Amount.Nano)
	assert.Equal(t, "USD", entry.Amount.Currency)
	assert.Positive(t, entry.Amount.Nano, "journal entries must carry a positive gross amount")
}

// TestSelectedCostHeadInitialPostingPostsExactSelectedAmount locks the clean
// initial posting: no previous head, one balanced two-sided journal intent in
// the selected native currency and a version-1 CAS successor.
func TestSelectedCostHeadInitialPostingPostsExactSelectedAmount(t *testing.T) {
	t.Parallel()

	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t,
		selectedCostTestHead(t, 0, nil), SelectedCostHeadExpectation{}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionApplied, result.Status)
	assert.Equal(t, SelectedCostReasonInitialPosting, result.Reason)
	assert.Equal(t, SelectedCostComparisonNotEvaluated, result.Comparison)
	assert.Equal(t, SelectedCostPostingApplied, result.Posting)
	assert.Equal(t, OperatorCostSelectionStatusFinal, result.SelectionStatus)
	assert.Nil(t, result.Previous)
	require.NotNil(t, result.Selected)
	assert.True(t, selected.IdentityEqual(*result.Selected))
	assertToleranceAmount(t, "delta", result.Delta, "USD", "10/0")

	require.NotNil(t, result.Link)
	assert.Nil(t, result.Link.Previous, "an initial posting has no previous selected valuation")
	assert.Equal(t, selected.Ref, result.Link.Current)
	assert.Equal(t, "USD", result.Link.Currency)
	assert.Nil(t, result.Link.FX)
	assert.Equal(t, uint64(1), result.Link.AdjustmentRevision)
	linkKey, err := result.Link.Key()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(linkKey, "selected-cost-link:v1:"))

	require.NotNil(t, result.Journal)
	require.NoError(t, result.Journal.Validate())
	assert.Equal(t, result.OperationKey, result.Journal.OperationKey)
	assert.Equal(t, selectedCostTestOperationKind, result.Journal.OperationKind)
	assert.Equal(t, selectedCostTestHeadKey, result.Journal.CorrectionGroupID)
	assert.False(t, result.Journal.Replayed)
	require.Len(t, result.Journal.Entries, 2)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[0], selectedCostTestCogsLedger, JournalDebit, 10_000_000_000)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[1], selectedCostTestClearingLedger, JournalCredit, 10_000_000_000)

	require.NotNil(t, result.NextHead)
	assert.Equal(t, uint64(1), result.NextHead.Version)
	require.NotNil(t, result.NextHead.Selected)
	assert.True(t, selected.IdentityEqual(*result.NextHead.Selected))
	assert.Equal(t, result.OperationKey, result.NextHead.LastOperationKey)
	assert.True(t, result.HasEffects())
	assert.True(t, strings.HasPrefix(result.OperationKey, "selected-cost-adjustment:v1:"))
	assert.True(t, strings.HasPrefix(result.Fingerprint, "selected-cost-adjustment-fp:v1:"))
}

// TestSelectedCostHeadDownwardCorrectionUsesReversalSides locks the 10 USD to
// 8 USD acceptance vector: the exact delta is -2 USD and the balanced journal
// reverses the original debit/credit sides instead of recording a negative
// gross amount.
func TestSelectedCostHeadDownwardCorrectionUsesReversalSides(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)
	head.LastOperationKey = "selected-cost-adjustment:previous"
	head.OriginalTransactionID = "tx-original"
	head.LastTransactionID = "tx-previous"
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "8")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionApplied, result.Status)
	assert.Equal(t, SelectedCostReasonSameNativeCurrency, result.Reason)
	assert.Equal(t, SelectedCostComparisonComparable, result.Comparison)
	assert.Equal(t, SelectedCostPostingApplied, result.Posting)
	require.NotNil(t, result.Previous)
	assert.True(t, previous.IdentityEqual(*result.Previous))
	assertToleranceAmount(t, "delta", result.Delta, "USD", "-2/0")

	require.NotNil(t, result.Link)
	require.NotNil(t, result.Link.Previous)
	assert.Equal(t, previous.Ref, *result.Link.Previous)
	assert.Equal(t, selected.Ref, result.Link.Current)
	assert.Equal(t, "USD", result.Link.Currency)
	assert.Nil(t, result.Link.FX)
	assert.Equal(t, uint64(2), result.Link.AdjustmentRevision)

	require.NotNil(t, result.Journal)
	require.NoError(t, result.Journal.Validate())
	assert.Equal(t, "tx-previous", result.Journal.ReversalOf)
	assert.Equal(t, "tx-previous", result.Journal.CorrectsTransactionID)
	require.Len(t, result.Journal.Entries, 2)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[0], selectedCostTestClearingLedger, JournalDebit, 2_000_000_000)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[1], selectedCostTestCogsLedger, JournalCredit, 2_000_000_000)

	require.NotNil(t, result.NextHead)
	assert.Equal(t, uint64(2), result.NextHead.Version)
	require.NotNil(t, result.NextHead.Selected)
	assert.True(t, selected.IdentityEqual(*result.NextHead.Selected))
	assert.Equal(t, result.OperationKey, result.NextHead.LastOperationKey)
}

// TestSelectedCostHeadUpwardCorrectionPostsPositiveDelta covers the inverse
// 10 USD to 12 USD vector with the original debit/credit orientation.
func TestSelectedCostHeadUpwardCorrectionPostsPositiveDelta(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)
	head.LastTransactionID = "tx-previous"
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "12")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionApplied, result.Status)
	assert.Equal(t, SelectedCostReasonSameNativeCurrency, result.Reason)
	assertToleranceAmount(t, "delta", result.Delta, "USD", "2/0")
	require.NotNil(t, result.Journal)
	require.Len(t, result.Journal.Entries, 2)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[0], selectedCostTestCogsLedger, JournalDebit, 2_000_000_000)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[1], selectedCostTestClearingLedger, JournalCredit, 2_000_000_000)
}

// TestSelectedCostHeadZeroDeltaAdvancesHeadWithoutJournal locks the no-op
// correction: a new selected revision with the same amount advances the
// selected-cost head and emits a balanced intent with no entries.
func TestSelectedCostHeadZeroDeltaAdvancesHeadWithoutJournal(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)
	head.LastTransactionID = "tx-previous"
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "10")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionNoOp, result.Status)
	assert.Equal(t, SelectedCostReasonZeroDelta, result.Reason)
	assert.Equal(t, SelectedCostComparisonComparable, result.Comparison)
	assert.Equal(t, SelectedCostPostingApplied, result.Posting)
	assertToleranceAmount(t, "delta", result.Delta, "USD", "0/0")
	require.NotNil(t, result.Link)
	require.NotNil(t, result.Journal)
	assert.True(t, result.Journal.IsNoOp())
	assert.Empty(t, result.Journal.Entries)
	require.NoError(t, result.Journal.Validate())
	require.NotNil(t, result.NextHead)
	require.NotNil(t, result.NextHead.Selected)
	assert.True(t, selected.IdentityEqual(*result.NextHead.Selected))
	assert.Equal(t, uint64(2), result.NextHead.Version)
}

// TestSelectedCostHeadCurrencyMismatchWithoutFrozenFXStaysPending locks the
// 10 USD to 8 EUR vector: without an explicit frozen FX basis shared by both
// valuations there is no valuation link, journal delta or cost-head transition.
func TestSelectedCostHeadCurrencyMismatchWithoutFrozenFXStaysPending(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)
	selected := selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v2", 2),
		OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "EUR", "8", nil, "")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionPending, result.Status)
	assert.Equal(t, SelectedCostReasonPostedCurrencyMismatch, result.Reason)
	assert.Equal(t, SelectedCostComparisonIncomparable, result.Comparison)
	assert.Equal(t, SelectedCostPostingPending, result.Posting)
	assert.Empty(t, result.OperationKey)
	assert.Empty(t, result.Fingerprint)
	selectedCostTestAssertNoEffects(t, result)
}

// TestSelectedCostHeadSharedFrozenFXComputesExactDelta locks the frozen FX
// acceptance path: both selected valuations are native EUR mapped through the
// exact same frozen conversion basis, so the delta is posted in the basis
// target currency while native amounts stay retained.
func TestSelectedCostHeadSharedFrozenFXComputesExactDelta(t *testing.T) {
	t.Parallel()

	basis := selectedCostTestEURBasis(t, "1.25")
	previous := selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v1", 1),
		OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "10", basis, "8")
	head := selectedCostTestHead(t, 1, &previous)
	head.LastTransactionID = "tx-previous"
	selected := selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v2", 2),
		OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "8", basis, "6.4")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionApplied, result.Status)
	assert.Equal(t, SelectedCostReasonFrozenFXBasis, result.Reason)
	assert.Equal(t, SelectedCostComparisonComparable, result.Comparison)
	assertToleranceAmount(t, "delta", result.Delta, "USD", "-2/0")
	require.NotNil(t, result.Link)
	assert.True(t, sameOperatorCostFX(result.Link.FX, basis))
	assert.Equal(t, "USD", result.Link.Currency)
	require.NotNil(t, result.Selected)
	assertToleranceAmount(t, "selected native amount", result.Selected.NativeAmount, "EUR", "64/1")
	require.NotNil(t, result.Previous)
	assertToleranceAmount(t, "previous native amount", result.Previous.NativeAmount, "EUR", "8/0")
	require.NotNil(t, result.Journal)
	require.Len(t, result.Journal.Entries, 2)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[0], selectedCostTestClearingLedger, JournalDebit, 2_000_000_000)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[1], selectedCostTestCogsLedger, JournalCredit, 2_000_000_000)
}

// TestSelectedCostHeadChangedFrozenFXBasisIsRejected locks that only the exact
// same frozen FX identity and rate material is comparable: a changed rate,
// changed basis id and a missing basis all stay pending with zero effects.
func TestSelectedCostHeadChangedFrozenFXBasisIsRejected(t *testing.T) {
	t.Parallel()

	previousBasis := selectedCostTestEURBasis(t, "1.25")
	previous := selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v1", 1),
		OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "10", previousBasis, "8")
	head := selectedCostTestHead(t, 1, &previous)

	cases := []struct {
		name     string
		selected SelectedCostValuation
	}{
		{
			name: "same identity changed exact rate",
			selected: selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v2", 2),
				OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "8", selectedCostTestEURBasis(t, "1.30"), "8"),
		},
		{
			name: "different frozen basis identity",
			selected: selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v3", 3),
				OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "8",
				&OperatorCostFXBasis{ID: "fx-eur-usd-other", Version: "v2", FromCurrency: "EUR", ToCurrency: "USD", Rate: toleranceLimit(t, "1.25")}, "6.4"),
		},
		{
			name:     "new selection without frozen basis",
			selected: selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v4", 4), "8"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
				SelectedCostHeadExpectation{Version: 1, Previous: &previous}, tc.selected))
			require.NoError(t, err)
			assert.Equal(t, SelectedCostTransitionPending, result.Status)
			assert.Equal(t, SelectedCostReasonFrozenFXBasisMismatch, result.Reason)
			assert.Equal(t, SelectedCostComparisonIncomparable, result.Comparison)
			selectedCostTestAssertNoEffects(t, result)
		})
	}
}

// TestSelectedCostHeadStaleExpectedVersionHasZeroEffects locks CAS mismatch:
// a caller that read an older head version never overwrites the newer
// selected-cost head and emits no journal intent.
func TestSelectedCostHeadStaleExpectedVersionHasZeroEffects(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	stale := previous
	current := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v3", 3), "7")
	head := selectedCostTestHead(t, 2, &current)
	head.LastTransactionID = "tx-newer"
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v4", 4), "9")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &stale}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionStale, result.Status)
	assert.Equal(t, SelectedCostReasonStaleHeadVersion, result.Reason)
	assert.Equal(t, SelectedCostPostingUnposted, result.Posting)
	selectedCostTestAssertNoEffects(t, result)
}

// TestSelectedCostHeadStaleRevisionHasZeroEffects locks late evidence: a new
// selected valuation below the current revision is stale even when the CAS
// version matches.
func TestSelectedCostHeadStaleRevisionHasZeroEffects(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v3", 3), "7")
	head := selectedCostTestHead(t, 3, &previous)
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "5")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 3, Previous: &previous}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionStale, result.Status)
	assert.Equal(t, SelectedCostReasonStaleRevision, result.Reason)
	selectedCostTestAssertNoEffects(t, result)
}

// TestSelectedCostHeadIdentityConflictHasZeroEffects locks a same-version CAS
// whose caller read a different previously posted valuation.
func TestSelectedCostHeadIdentityConflictHasZeroEffects(t *testing.T) {
	t.Parallel()

	current := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &current)
	other := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-other", 1), "7")
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "8")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &other}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionConflict, result.Status)
	assert.Equal(t, SelectedCostReasonHeadIdentityConflict, result.Reason)
	selectedCostTestAssertNoEffects(t, result)
}

// TestSelectedCostHeadRevisionConflictHasZeroEffects locks 10.1 identity
// conflict semantics: the same selected revision with a different semantic
// payload is rejected rather than silently replacing the posted head.
func TestSelectedCostHeadRevisionConflictHasZeroEffects(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "8")
	head := selectedCostTestHead(t, 2, &previous)
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2-altered", 2), "9")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 2, Previous: &previous}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionConflict, result.Status)
	assert.Equal(t, SelectedCostReasonRevisionConflict, result.Reason)
	selectedCostTestAssertNoEffects(t, result)
}

// TestSelectedCostHeadExactReplayReturnsNoNewIntent locks idempotent replay:
// re-planning the same transition against the already-advanced durable head
// returns the identical operation identity with an empty replayed intent.
func TestSelectedCostHeadExactReplayReturnsNoNewIntent(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)
	head.LastTransactionID = "tx-previous"
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "8")
	input := selectedCostTestInput(t, head, SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected)

	first, err := PlanSelectedCostHeadTransition(input)
	require.NoError(t, err)
	require.Equal(t, SelectedCostTransitionApplied, first.Status)
	firstLinkKey, err := first.Link.Key()
	require.NoError(t, err)

	replayInput := input
	replayInput.Current = *first.NextHead
	replayInput.Current.LastOperationKey = first.OperationKey
	replayed, err := PlanSelectedCostHeadTransition(replayInput)
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionReplay, replayed.Status)
	assert.Equal(t, SelectedCostReasonAlreadyApplied, replayed.Reason)
	assert.Equal(t, SelectedCostPostingReplayed, replayed.Posting)
	assert.Equal(t, first.OperationKey, replayed.OperationKey)
	assert.Equal(t, first.Fingerprint, replayed.Fingerprint)
	require.NotNil(t, replayed.Journal)
	assert.True(t, replayed.Journal.Replayed)
	assert.Empty(t, replayed.Journal.Entries)
	assert.Nil(t, replayed.NextHead)
	assert.False(t, replayed.HasEffects())
	require.NotNil(t, replayed.Link)
	require.NotNil(t, replayed.Previous)
	assert.True(t, previous.IdentityEqual(*replayed.Previous))
	replayedLinkKey, err := replayed.Link.Key()
	require.NoError(t, err)
	assert.Equal(t, firstLinkKey, replayedLinkKey)
}

// TestSelectedCostHeadMissingAndProvisionalSelectionsFailClosed locks that
// unknown, provisional, missing-attempt and non-operator selections never post
// or advance the selected-cost head.
func TestSelectedCostHeadMissingAndProvisionalSelectionsFailClosed(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)

	cases := []struct {
		name       string
		status     OperatorCostSelectionStatus
		reason     OperatorCostSelectionReason
		provenance OperatorCostProvenance
		amount     string
		wantReason SelectedCostHeadTransitionReason
	}{
		{
			name: "missing attempted usage", status: OperatorCostSelectionStatusUnknown,
			reason: OperatorCostReasonNoPayableEvidence, provenance: OperatorCostProvenanceAttempted,
			wantReason: SelectedCostReasonMissingAttemptedUsage,
		},
		{
			name: "unknown non-attempted selection", status: OperatorCostSelectionStatusUnknown,
			reason: OperatorCostReasonKnownZeroNotAuthorized, provenance: OperatorCostProvenanceNeverStarted,
			wantReason: SelectedCostReasonUnknownSelection,
		},
		{
			name: "provisional selection", status: OperatorCostSelectionStatusProvisional,
			reason: OperatorCostReasonNone, provenance: OperatorCostProvenanceAttempted, amount: "9",
			wantReason: SelectedCostReasonProvisionalSelection,
		},
		{
			name: "not operator payable", status: OperatorCostSelectionStatusNotOperatorPayable,
			reason: OperatorCostReasonPayerCustomerBYOK, provenance: OperatorCostProvenanceAttempted,
			wantReason: SelectedCostReasonNotOperatorPayable,
		},
		{
			name: "incomparable selection", status: OperatorCostSelectionStatusIncomparable,
			reason: OperatorCostReasonCurrencyMismatch, provenance: OperatorCostProvenanceAttempted,
			wantReason: SelectedCostReasonIncomparableSelection,
		},
		{
			name: "selection conflict", status: OperatorCostSelectionStatusConflict,
			reason: OperatorCostReasonComparisonConflict, provenance: OperatorCostProvenanceAttempted,
			wantReason: SelectedCostReasonSelectionConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			selection := selectedCostTestSelection(t, tc.status, tc.provenance, "USD", tc.amount, nil, "")
			selection.Reason = tc.reason
			selected, err := NewSelectedCostValuation(selectedCostTestRef(t, "valuation-"+strings.ReplaceAll(tc.name, " ", "-"), 2), selection)
			require.NoError(t, err)

			result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
				SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected))
			require.NoError(t, err)
			assert.Equal(t, SelectedCostTransitionPending, result.Status)
			assert.Equal(t, tc.wantReason, result.Reason)
			assert.Equal(t, tc.status, result.SelectionStatus)
			assert.Equal(t, tc.reason, result.SelectionReason)
			assert.Equal(t, SelectedCostComparisonPending, result.Comparison)
			assert.Equal(t, SelectedCostPostingPending, result.Posting)
			selectedCostTestAssertNoEffects(t, result)
		})
	}
}

// TestSelectedCostHeadKnownZeroCorrectionReversesPostedCOGS locks payer
// correction: an authorized known-zero selection reverses the full previously
// posted amount with positive reversal entries.
func TestSelectedCostHeadKnownZeroCorrectionReversesPostedCOGS(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)
	head.LastTransactionID = "tx-previous"
	selected := selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v2", 2),
		OperatorCostSelectionStatusKnownZero, OperatorCostProvenanceNotBillable, "USD", "0", nil, "")

	result, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected))
	require.NoError(t, err)

	assert.Equal(t, SelectedCostTransitionApplied, result.Status)
	assert.Equal(t, SelectedCostReasonSameNativeCurrency, result.Reason)
	assertToleranceAmount(t, "delta", result.Delta, "USD", "-10/0")
	require.NotNil(t, result.Journal)
	require.Len(t, result.Journal.Entries, 2)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[0], selectedCostTestClearingLedger, JournalDebit, 10_000_000_000)
	selectedCostTestAssertJournalEntry(t, result.Journal.Entries[1], selectedCostTestCogsLedger, JournalCredit, 10_000_000_000)
	require.NotNil(t, result.NextHead)
	require.NotNil(t, result.NextHead.Selected)
	assertToleranceAmount(t, "next selected amount", result.NextHead.Selected.Amount, "USD", "0/0")
}

// TestSelectedCostHeadJournalIntentRejectsInvalidGrossAmounts locks the ledger
// contract: tampered intents with negative or unbalanced entries fail closed,
// while the planner always produces balanced, positive entries.
func TestSelectedCostHeadJournalIntentRejectsInvalidGrossAmounts(t *testing.T) {
	t.Parallel()

	money := func(nano int64) Money { return Money{Nano: nano, Currency: "USD"} }
	cases := []struct {
		name    string
		intent  BalancedJournalIntent
		wantErr bool
	}{
		{
			name: "planner-shaped balanced intent",
			intent: BalancedJournalIntent{
				OperationKey: "op", AccountID: "account", HeadKey: "head", Currency: "USD",
				Entries: []JournalEntry{
					{LedgerAccount: selectedCostTestCogsLedger, Side: JournalDebit, Amount: money(2)},
					{LedgerAccount: selectedCostTestClearingLedger, Side: JournalCredit, Amount: money(2)},
				},
			},
		},
		{
			name:   "no-op intent",
			intent: BalancedJournalIntent{OperationKey: "op", AccountID: "account", HeadKey: "head", Currency: "USD"},
		},
		{
			name: "negative gross amount",
			intent: BalancedJournalIntent{
				OperationKey: "op", AccountID: "account", HeadKey: "head", Currency: "USD",
				Entries: []JournalEntry{
					{LedgerAccount: selectedCostTestCogsLedger, Side: JournalDebit, Amount: money(-2)},
					{LedgerAccount: selectedCostTestClearingLedger, Side: JournalCredit, Amount: money(2)},
				},
			},
			wantErr: true,
		},
		{
			name: "unbalanced entries",
			intent: BalancedJournalIntent{
				OperationKey: "op", AccountID: "account", HeadKey: "head", Currency: "USD",
				Entries: []JournalEntry{
					{LedgerAccount: selectedCostTestCogsLedger, Side: JournalDebit, Amount: money(2)},
					{LedgerAccount: selectedCostTestClearingLedger, Side: JournalCredit, Amount: money(1)},
				},
			},
			wantErr: true,
		},
		{
			name: "mismatched currency",
			intent: BalancedJournalIntent{
				OperationKey: "op", AccountID: "account", HeadKey: "head", Currency: "USD",
				Entries: []JournalEntry{
					{LedgerAccount: selectedCostTestCogsLedger, Side: JournalDebit, Amount: Money{Nano: 2, Currency: "EUR"}},
					{LedgerAccount: selectedCostTestClearingLedger, Side: JournalCredit, Amount: money(2)},
				},
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.intent.Validate()
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrSelectedCostHeadInvalid)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestSelectedCostHeadIdentityAndOrderAreStable locks immutable fingerprints:
// repeated planning of equivalent input yields byte-identical identities, the
// input is detached from caller memory, and candidate ordering from the Phase
// 12 selection plane cannot change the derived valuation identity.
func TestSelectedCostHeadIdentityAndOrderAreStable(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)
	head.LastTransactionID = "tx-previous"
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "8")
	input := selectedCostTestInput(t, head, SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected)

	first, err := PlanSelectedCostHeadTransition(input)
	require.NoError(t, err)
	second, err := PlanSelectedCostHeadTransition(input)
	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(first, second), "repeated planning must be deterministic")

	firstLinkKey, err := first.Link.Key()
	require.NoError(t, err)
	secondLinkKey, err := second.Link.Key()
	require.NoError(t, err)
	assert.Equal(t, firstLinkKey, secondLinkKey)

	// Caller memory mutation after planning must not rewrite the already
	// planned result: the plan is a detached immutable intent.
	selected.Amount.Decimal = toleranceLimit(t, "999")
	previous.Amount.Decimal = toleranceLimit(t, "999")
	assertToleranceAmount(t, "delta after caller mutation", first.Delta, "USD", "-2/0")
	require.NotNil(t, first.NextHead)
	require.NotNil(t, first.NextHead.Selected)
	assertToleranceAmount(t, "next head amount after caller mutation", first.NextHead.Selected.Amount, "USD", "8/0")
	require.NotNil(t, first.Previous)
	assertToleranceAmount(t, "previous amount after caller mutation", first.Previous.Amount, "USD", "10/0")

	// Candidate ordering is a selection-plane concern and must not change the
	// frozen valuation identity used by head transitions.
	ref := selectedCostTestRef(t, "valuation-order", 5)
	left := selectedCostTestSelection(t, OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "8", nil, "")
	left.Candidates = []OperatorCostCandidate{{Basis: OperatorCostBasisP}, {Basis: OperatorCostBasisQ}}
	right := selectedCostTestSelection(t, OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "8", nil, "")
	right.Candidates = []OperatorCostCandidate{{Basis: OperatorCostBasisQ}, {Basis: OperatorCostBasisP}}
	leftValuation, err := NewSelectedCostValuation(ref, left)
	require.NoError(t, err)
	rightValuation, err := NewSelectedCostValuation(ref, right)
	require.NoError(t, err)
	assert.True(t, leftValuation.IdentityEqual(rightValuation))
}

// TestSelectedCostHeadFingerprintIsPayloadSensitive locks that the stable
// operation key is independent of the selected amount while the fingerprint
// changes, so a different payload under the same operation identity conflicts.
func TestSelectedCostHeadFingerprintIsPayloadSensitive(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	head := selectedCostTestHead(t, 1, &previous)
	head.LastTransactionID = "tx-previous"
	eight := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "8")
	seven := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "7")

	first, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, eight))
	require.NoError(t, err)
	second, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, seven))
	require.NoError(t, err)

	assert.Equal(t, first.OperationKey, second.OperationKey)
	assert.NotEqual(t, first.Fingerprint, second.Fingerprint)
}

// TestSelectedCostHeadRejectsMalformedInput locks fail-closed ingress: malformed
// head, expectation, valuation and FX identities are rejected before any
// transition planning.
func TestSelectedCostHeadRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "10")
	selected := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v2", 2), "8")
	head := selectedCostTestHead(t, 1, &previous)
	expectation := SelectedCostHeadExpectation{Version: 1, Previous: &previous}

	cases := []struct {
		name  string
		input SelectedCostHeadTransitionInput
	}{
		{
			name: "head version without selected valuation",
			input: func() SelectedCostHeadTransitionInput {
				broken := selectedCostTestHead(t, 1, nil)
				return selectedCostTestInput(t, broken, expectation, selected)
			}(),
		},
		{
			name: "head selected valuation without version",
			input: func() SelectedCostHeadTransitionInput {
				broken := selectedCostTestHead(t, 0, &previous)
				return selectedCostTestInput(t, broken, SelectedCostHeadExpectation{}, selected)
			}(),
		},
		{
			name:  "expectation version without previous valuation",
			input: selectedCostTestInput(t, head, SelectedCostHeadExpectation{Version: 1}, selected),
		},
		{
			name:  "expectation previous valuation without version",
			input: selectedCostTestInput(t, head, SelectedCostHeadExpectation{Previous: &previous}, selected),
		},
		{
			name: "missing account",
			input: func() SelectedCostHeadTransitionInput {
				broken := head
				broken.AccountID = ""
				return selectedCostTestInput(t, broken, expectation, selected)
			}(),
		},
		{
			name: "unsafe head key",
			input: func() SelectedCostHeadTransitionInput {
				broken := head
				broken.HeadKey = " head "
				return selectedCostTestInput(t, broken, expectation, selected)
			}(),
		},
		{
			name: "non request-scoped subject",
			input: func() SelectedCostHeadTransitionInput {
				broken := head
				broken.Subject.Kind = metering.SubjectALeg
				return selectedCostTestInput(t, broken, expectation, selected)
			}(),
		},
		{
			name: "zero selected valuation ref",
			input: func() SelectedCostHeadTransitionInput {
				broken := selected
				broken.Ref = SelectedCostValuationRef{}
				return selectedCostTestInput(t, head, expectation, broken)
			}(),
		},
		{
			name: "final selection without amount",
			input: func() SelectedCostHeadTransitionInput {
				broken := selected
				broken.Amount = nil
				return selectedCostTestInput(t, head, expectation, broken)
			}(),
		},
		{
			name: "frozen fx without native amount",
			input: func() SelectedCostHeadTransitionInput {
				broken := selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v5", 5),
					OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "8", selectedCostTestEURBasis(t, "1.25"), "6.4")
				broken.NativeAmount = nil
				return selectedCostTestInput(t, head, expectation, broken)
			}(),
		},
		{
			name: "frozen fx target currency mismatch",
			input: func() SelectedCostHeadTransitionInput {
				broken := selectedCostTestValuation(t, selectedCostTestRef(t, "valuation-v6", 6),
					OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "8", selectedCostTestEURBasis(t, "1.25"), "6.4")
				broken.FX = &OperatorCostFXBasis{ID: "fx-eur-gbp", Version: "v1", FromCurrency: "EUR", ToCurrency: "GBP", Rate: toleranceLimit(t, "1.25")}
				return selectedCostTestInput(t, head, expectation, broken)
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := PlanSelectedCostHeadTransition(tc.input)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSelectedCostHeadInvalid)
		})
	}
}

// TestSelectedCostHeadSubNanoDeltaFailsClosed locks exactness at the ledger
// boundary: a rational delta that cannot be represented as checked integer
// nanos is rejected rather than silently rounded.
func TestSelectedCostHeadSubNanoDeltaFailsClosed(t *testing.T) {
	t.Parallel()

	previous := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-v1", 1), "1")
	head := selectedCostTestHead(t, 1, &previous)
	selected := SelectedCostValuation{
		Ref: selectedCostTestRef(t, "valuation-v2", 2), Status: OperatorCostSelectionStatusFinal,
		Provenance: OperatorCostProvenanceAttempted, Currency: "USD",
		Amount: &MonetaryExactAmount{Currency: "USD", Numerator: "1", Denominator: "3"},
	}
	require.NoError(t, selected.Validate())

	_, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t, head,
		SelectedCostHeadExpectation{Version: 1, Previous: &previous}, selected))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSelectedCostHeadLedgerPrecision)
}

// TestSelectedCostValuationConstructorRejectsInconsistentSelection locks the
// Phase 12 bridge: an inconsistent selection result cannot become a frozen
// selected valuation.
func TestSelectedCostValuationConstructorRejectsInconsistentSelection(t *testing.T) {
	t.Parallel()

	ref := selectedCostTestRef(t, "valuation-constructor", 1)
	cases := []struct {
		name      string
		selection OperatorCostSelectionResult
	}{
		{name: "unknown selection status", selection: OperatorCostSelectionResult{Currency: "USD"}},
		{name: "empty currency", selection: OperatorCostSelectionResult{Status: OperatorCostSelectionStatusFinal, Amount: operatorCostTestAmount(t, "USD", "1")}},
		{name: "final without amount", selection: OperatorCostSelectionResult{Status: OperatorCostSelectionStatusFinal, Currency: "USD"}},
		{name: "negative final amount", selection: OperatorCostSelectionResult{Status: OperatorCostSelectionStatusFinal, Currency: "USD", Amount: operatorCostTestAmount(t, "USD", "-1")}},
		{
			name: "native amount without frozen basis",
			selection: OperatorCostSelectionResult{
				Status: OperatorCostSelectionStatusFinal, Currency: "USD", Amount: operatorCostTestAmount(t, "USD", "1"),
				NativeAmount: &MonetaryExactAmount{Currency: "EUR", Decimal: toleranceLimit(t, "1")},
			},
		},
		{name: "unknown provenance", selection: OperatorCostSelectionResult{Status: OperatorCostSelectionStatusFinal, Currency: "USD", Amount: operatorCostTestAmount(t, "USD", "1"), Provenance: OperatorCostProvenance("invented")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewSelectedCostValuation(ref, tc.selection)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrSelectedCostHeadInvalid))
		})
	}
}

// TestSelectedCostHeadVocabularyIsClosed locks the typed status/reason
// vocabulary: every documented value is known and invented values are not.
func TestSelectedCostHeadVocabularyIsClosed(t *testing.T) {
	t.Parallel()

	statuses := []SelectedCostHeadTransitionStatus{
		SelectedCostTransitionApplied, SelectedCostTransitionNoOp, SelectedCostTransitionReplay,
		SelectedCostTransitionPending, SelectedCostTransitionStale, SelectedCostTransitionConflict,
	}
	for _, status := range statuses {
		assert.True(t, status.IsKnown(), "status %q must be known", status)
	}
	assert.False(t, SelectedCostHeadTransitionStatus("invented").IsKnown())

	reasons := []SelectedCostHeadTransitionReason{
		SelectedCostReasonNone, SelectedCostReasonInitialPosting, SelectedCostReasonSameNativeCurrency,
		SelectedCostReasonFrozenFXBasis, SelectedCostReasonZeroDelta, SelectedCostReasonAlreadyApplied,
		SelectedCostReasonProvisionalSelection, SelectedCostReasonUnknownSelection,
		SelectedCostReasonMissingAttemptedUsage, SelectedCostReasonNotOperatorPayable,
		SelectedCostReasonIncomparableSelection, SelectedCostReasonSelectionConflict,
		SelectedCostReasonPostedCurrencyMismatch, SelectedCostReasonFrozenFXBasisMismatch,
		SelectedCostReasonStaleHeadVersion, SelectedCostReasonStaleRevision,
		SelectedCostReasonHeadIdentityConflict, SelectedCostReasonRevisionConflict,
	}
	for _, reason := range reasons {
		assert.True(t, reason.IsKnown(), "reason %q must be known", reason)
	}
	assert.False(t, SelectedCostHeadTransitionReason("invented").IsKnown())

	for _, comparison := range []SelectedCostComparisonStatus{
		SelectedCostComparisonNotEvaluated, SelectedCostComparisonComparable,
		SelectedCostComparisonPending, SelectedCostComparisonIncomparable,
	} {
		assert.True(t, comparison.IsKnown(), "comparison %q must be known", comparison)
	}
	assert.False(t, SelectedCostComparisonStatus("invented").IsKnown())

	for _, posting := range []SelectedCostPostingStatus{
		SelectedCostPostingUnposted, SelectedCostPostingPending,
		SelectedCostPostingApplied, SelectedCostPostingReplayed,
	} {
		assert.True(t, posting.IsKnown(), "posting %q must be known", posting)
	}
	assert.False(t, SelectedCostPostingStatus("invented").IsKnown())
}
