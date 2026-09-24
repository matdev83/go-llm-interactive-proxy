package billing

import (
	"fmt"
	"math/big"
)

// Task 13.3A pure selected-cost head transition planner. It is deterministic
// and side-effect free: it reads only its input, performs exact arithmetic and
// returns a plan. Persistence, SQL, migrations, workers, statement matching and
// runtime wiring stay outside this file.

// PlanSelectedCostHeadTransition plans one compare-and-swap transition of the
// current selected/posted valuation head. Monetary delta is computed as new
// selected minus previously posted only when both selected valuations share the
// same native currency or the exact same explicit frozen FX conversion basis.
// Otherwise the correction stays pending with no valuation link, journal delta
// or head transition.
//
// A durable head that already carries the exact new selected valuation is an
// idempotent replay. A stale caller version and a same-version identity or
// revision conflict return typed zero-effect results.
func PlanSelectedCostHeadTransition(input SelectedCostHeadTransitionInput) (SelectedCostHeadTransition, error) {
	normalized, err := normalizedSelectedCostHeadTransitionInput(input)
	if err != nil {
		return SelectedCostHeadTransition{}, err
	}
	current, expected, selected := normalized.Current, normalized.Expected, normalized.Selected
	identity := selectedCostChargeIdentity{AccountID: current.AccountID, CallID: current.CallID, HeadKey: current.HeadKey, Subject: current.Subject}
	result := SelectedCostHeadTransition{
		AccountID: current.AccountID, CallID: current.CallID, HeadKey: current.HeadKey, Subject: current.Subject,
		SelectionStatus: selected.Status, SelectionReason: selected.Reason,
		Comparison: SelectedCostComparisonNotEvaluated, Posting: SelectedCostPostingUnposted,
		Selected: selectedCostPointer(selected),
	}
	if current.Selected != nil {
		result.Previous = selectedCostPointer(current.Selected.Clone())
	}

	// Idempotent replay: the durable head already carries exactly this frozen
	// selected valuation. No link insert, journal delta or head version change
	// is planned; the stable operation identity is reproduced for the caller.
	if current.Selected != nil && current.Selected.IdentityEqual(selected) {
		return selectedCostReplayTransition(result, identity, expected, selected)
	}
	// CAS fence: a caller that read an older head version never overwrites a
	// newer selected-cost head, regardless of the requested correction.
	if current.Version != expected.Version {
		result.Status = SelectedCostTransitionStale
		result.Reason = SelectedCostReasonStaleHeadVersion
		return result, nil
	}
	// A same-version expectation whose previous valuation does not match the
	// durable head is a conflict, not a silent replacement.
	if (current.Selected == nil) != (expected.Previous == nil) ||
		(expected.Previous != nil && !current.Selected.IdentityEqual(*expected.Previous)) {
		result.Status = SelectedCostTransitionConflict
		result.Reason = SelectedCostReasonHeadIdentityConflict
		return result, nil
	}
	// Selection plane: only a final or explicitly authorized known-zero
	// selected cost can post or advance the head. Unknown, provisional,
	// missing-attempt and non-operator selections stay pending.
	if !selectedCostSelectionPostable(selected) {
		result.Status = SelectedCostTransitionPending
		result.Reason = selectedCostPendingSelectionReason(selected)
		result.Comparison = SelectedCostComparisonPending
		result.Posting = SelectedCostPostingPending
		return result, nil
	}
	comparison, reason, deltaCurrency := compareSelectedCostValuations(current.Selected, selected)
	if comparison != SelectedCostComparisonComparable {
		result.Status = SelectedCostTransitionPending
		result.Reason = reason
		result.Comparison = comparison
		result.Posting = SelectedCostPostingPending
		return result, nil
	}
	// Revision ordering: late evidence is stale; the same revision with a
	// different immutable payload is an identity conflict.
	if current.Selected != nil {
		switch {
		case selected.Ref.Revision < current.Selected.Ref.Revision:
			result.Status = SelectedCostTransitionStale
			result.Reason = SelectedCostReasonStaleRevision
			return result, nil
		case selected.Ref.Revision == current.Selected.Ref.Revision:
			result.Status = SelectedCostTransitionConflict
			result.Reason = SelectedCostReasonRevisionConflict
			return result, nil
		}
	}
	return selectedCostApplyTransition(result, identity, current, expected, selected, comparison, reason, deltaCurrency)
}

func normalizedSelectedCostHeadTransitionInput(input SelectedCostHeadTransitionInput) (SelectedCostHeadTransitionInput, error) {
	out := SelectedCostHeadTransitionInput{Current: input.Current.Clone(), Selected: input.Selected.Clone()}
	if input.Expected.Previous != nil {
		previous := input.Expected.Previous.Clone()
		out.Expected = SelectedCostHeadExpectation{Version: input.Expected.Version, Previous: &previous}
	} else {
		out.Expected = SelectedCostHeadExpectation{Version: input.Expected.Version}
	}
	if err := out.Current.Validate(); err != nil {
		return SelectedCostHeadTransitionInput{}, err
	}
	if err := out.Expected.Validate(); err != nil {
		return SelectedCostHeadTransitionInput{}, err
	}
	if err := out.Selected.Validate(); err != nil {
		return SelectedCostHeadTransitionInput{}, err
	}
	return out, nil
}

func selectedCostSelectionPostable(selected SelectedCostValuation) bool {
	return selected.Status == OperatorCostSelectionStatusFinal || selected.Status == OperatorCostSelectionStatusKnownZero
}

func selectedCostPendingSelectionReason(selected SelectedCostValuation) SelectedCostHeadTransitionReason {
	switch selected.Status {
	case OperatorCostSelectionStatusProvisional:
		return SelectedCostReasonProvisionalSelection
	case OperatorCostSelectionStatusNotOperatorPayable:
		return SelectedCostReasonNotOperatorPayable
	case OperatorCostSelectionStatusIncomparable:
		return SelectedCostReasonIncomparableSelection
	case OperatorCostSelectionStatusConflict:
		return SelectedCostReasonSelectionConflict
	case OperatorCostSelectionStatusUnknown:
		if selected.Provenance == OperatorCostProvenanceAttempted {
			// Attempted work without accepted payable evidence stays unknown
			// rather than becoming a known zero.
			return SelectedCostReasonMissingAttemptedUsage
		}
		return SelectedCostReasonUnknownSelection
	default:
		return SelectedCostReasonUnknownSelection
	}
}

// compareSelectedCostValuations validates monetary comparability. The delta
// currency is the shared native currency when neither side used a conversion,
// or the frozen FX target currency when both sides used the exact same explicit
// frozen conversion basis and rate material.
func compareSelectedCostValuations(previous *SelectedCostValuation, selected SelectedCostValuation) (SelectedCostComparisonStatus, SelectedCostHeadTransitionReason, string) {
	if previous == nil {
		return SelectedCostComparisonComparable, SelectedCostReasonInitialPosting, selected.Currency
	}
	if previous.FX == nil && selected.FX == nil {
		if previous.Currency != selected.Currency {
			return SelectedCostComparisonIncomparable, SelectedCostReasonPostedCurrencyMismatch, ""
		}
		return SelectedCostComparisonComparable, SelectedCostReasonSameNativeCurrency, previous.Currency
	}
	if previous.FX != nil && selected.FX != nil && sameOperatorCostFX(previous.FX, selected.FX) {
		if previous.Currency != previous.FX.ToCurrency || selected.Currency != selected.FX.ToCurrency {
			return SelectedCostComparisonIncomparable, SelectedCostReasonPostedCurrencyMismatch, ""
		}
		return SelectedCostComparisonComparable, SelectedCostReasonFrozenFXBasis, previous.FX.ToCurrency
	}
	if previous.Currency != selected.Currency {
		return SelectedCostComparisonIncomparable, SelectedCostReasonPostedCurrencyMismatch, ""
	}
	return SelectedCostComparisonIncomparable, SelectedCostReasonFrozenFXBasisMismatch, ""
}

func selectedCostReplayTransition(result SelectedCostHeadTransition, identity selectedCostChargeIdentity, expected SelectedCostHeadExpectation, selected SelectedCostValuation) (SelectedCostHeadTransition, error) {
	result.Previous = nil
	if expected.Previous != nil {
		previous := expected.Previous.Clone()
		result.Previous = &previous
	}
	comparison, _, deltaCurrency := compareSelectedCostValuations(expected.Previous, selected)
	result.Status = SelectedCostTransitionReplay
	result.Reason = SelectedCostReasonAlreadyApplied
	result.Posting = SelectedCostPostingReplayed
	result.Comparison = comparison
	if comparison != SelectedCostComparisonComparable {
		return result, nil
	}
	deltaRat, err := selectedCostDeltaRat(expected.Previous, selected)
	if err != nil {
		return SelectedCostHeadTransition{}, err
	}
	delta, err := newMonetaryExactAmount(deltaCurrency, deltaRat)
	if err != nil {
		return SelectedCostHeadTransition{}, fmt.Errorf("%w: replayed delta: %v", ErrSelectedCostHeadInvalid, err)
	}
	operationKey, err := selectedCostOperationKey(identity, selectedCostRefOf(expected.Previous), selected, deltaCurrency)
	if err != nil {
		return SelectedCostHeadTransition{}, err
	}
	fingerprint, err := selectedCostFingerprint(identity, expected, selected, deltaCurrency, &delta)
	if err != nil {
		return SelectedCostHeadTransition{}, err
	}
	result.OperationKey = operationKey
	result.Fingerprint = fingerprint
	link := selectedCostValuationLink(identity, expected.Previous, selected, deltaCurrency, operationKey)
	result.Link = &link
	result.Journal = &BalancedJournalIntent{
		OperationKey: operationKey, AccountID: identity.AccountID, TurnID: identity.CallID.String(),
		ALegID: identity.Subject.ALegID, BLegID: identity.Subject.BLegID, HeadKey: identity.HeadKey,
		OperationKind: selectedCostAdjustmentOperationKind, Currency: deltaCurrency, Replayed: true,
	}
	return result, nil
}

func selectedCostApplyTransition(result SelectedCostHeadTransition, identity selectedCostChargeIdentity, current SelectedCostHead, expected SelectedCostHeadExpectation, selected SelectedCostValuation, comparison SelectedCostComparisonStatus, reason SelectedCostHeadTransitionReason, deltaCurrency string) (SelectedCostHeadTransition, error) {
	deltaRat, err := selectedCostDeltaRat(current.Selected, selected)
	if err != nil {
		return SelectedCostHeadTransition{}, err
	}
	delta, err := newMonetaryExactAmount(deltaCurrency, deltaRat)
	if err != nil {
		return SelectedCostHeadTransition{}, fmt.Errorf("%w: delta: %v", ErrSelectedCostHeadInvalid, err)
	}
	operationKey, err := selectedCostOperationKey(identity, selectedCostRefOf(expected.Previous), selected, deltaCurrency)
	if err != nil {
		return SelectedCostHeadTransition{}, err
	}
	fingerprint, err := selectedCostFingerprint(identity, expected, selected, deltaCurrency, &delta)
	if err != nil {
		return SelectedCostHeadTransition{}, err
	}
	result.OperationKey = operationKey
	result.Fingerprint = fingerprint
	result.Comparison = comparison
	if current.Selected == nil {
		// An initial posting has no previously posted valuation to compare.
		result.Comparison = SelectedCostComparisonNotEvaluated
	}
	result.Delta = &delta
	link := selectedCostValuationLink(identity, expected.Previous, selected, deltaCurrency, operationKey)
	result.Link = &link

	journal := BalancedJournalIntent{
		OperationKey: operationKey, AccountID: identity.AccountID, TurnID: identity.CallID.String(),
		ALegID: identity.Subject.ALegID, BLegID: identity.Subject.BLegID, HeadKey: identity.HeadKey,
		OperationKind: selectedCostAdjustmentOperationKind, Currency: deltaCurrency,
	}
	switch deltaRat.Sign() {
	case 0:
		result.Status = SelectedCostTransitionNoOp
		result.Reason = SelectedCostReasonZeroDelta
	default:
		magnitude := new(big.Rat).Abs(deltaRat)
		amount, err := selectedCostLedgerMoney(deltaCurrency, magnitude)
		if err != nil {
			return SelectedCostHeadTransition{}, err
		}
		debit, credit := selectedCostCogsLedgerAccount, selectedCostPayableLedgerAccount
		if deltaRat.Sign() < 0 {
			// A downward correction reverses the applicable COGS/payable
			// entries with a positive gross amount instead of inserting an
			// invalid negative gross charge.
			debit, credit = credit, debit
		}
		journal.Entries = []JournalEntry{
			{LedgerAccount: debit, Side: JournalDebit, Amount: amount},
			{LedgerAccount: credit, Side: JournalCredit, Amount: amount},
		}
		if current.LastTransactionID != "" {
			// Chained adjustment referencing the original posting chain.
			journal.ReversalOf = current.LastTransactionID
			journal.CorrectsTransactionID = current.LastTransactionID
		} else {
			journal.CorrectionGroupID = current.HeadKey
		}
		result.Status = SelectedCostTransitionApplied
		result.Reason = reason
	}
	result.Journal = &journal
	result.Posting = SelectedCostPostingApplied

	next := current.Clone()
	next.Version = current.Version + 1
	next.Selected = selectedCostPointer(selected.Clone())
	next.LastOperationKey = operationKey
	result.NextHead = &next
	return result, nil
}

func selectedCostDeltaRat(previous *SelectedCostValuation, selected SelectedCostValuation) (*big.Rat, error) {
	next, err := selected.Amount.Rat()
	if err != nil {
		return nil, fmt.Errorf("%w: selected amount: %v", ErrSelectedCostHeadInvalid, err)
	}
	if previous == nil {
		return next, nil
	}
	prior, err := previous.Amount.Rat()
	if err != nil {
		return nil, fmt.Errorf("%w: previous amount: %v", ErrSelectedCostHeadInvalid, err)
	}
	return new(big.Rat).Sub(next, prior), nil
}

// selectedCostLedgerMoney converts an exact nonnegative magnitude into checked
// ledger nanos. Sub-nano or out-of-range values fail closed; the planner never
// silently rounds.
func selectedCostLedgerMoney(currency string, value *big.Rat) (Money, error) {
	if value == nil || value.Sign() < 0 {
		return Money{}, fmt.Errorf("%w: ledger magnitude is required", ErrSelectedCostHeadLedgerPrecision)
	}
	nanos := new(big.Rat).Mul(value, new(big.Rat).SetInt64(1_000_000_000))
	if !nanos.IsInt() {
		return Money{}, fmt.Errorf("%w: %s delta %s has sub-nano precision", ErrSelectedCostHeadLedgerPrecision, currency, value.RatString())
	}
	if !nanos.Num().IsInt64() {
		return Money{}, fmt.Errorf("%w: %s delta %s exceeds ledger range", ErrSelectedCostHeadLedgerPrecision, currency, value.RatString())
	}
	return Money{Nano: nanos.Num().Int64(), Currency: currency}, nil
}

func selectedCostValuationLink(identity selectedCostChargeIdentity, previous *SelectedCostValuation, selected SelectedCostValuation, currency, operationKey string) SelectedCostValuationLink {
	return SelectedCostValuationLink{
		AccountID: identity.AccountID, CallID: identity.CallID, HeadKey: identity.HeadKey, Subject: identity.Subject,
		Previous: selectedCostRefOf(previous), Current: selected.Ref, Currency: currency,
		FX: cloneOperatorCostFX(selected.FX), AdjustmentRevision: selected.Ref.Revision, OperationKey: operationKey,
	}
}

func selectedCostRefOf(previous *SelectedCostValuation) *SelectedCostValuationRef {
	if previous == nil {
		return nil
	}
	ref := previous.Ref
	return &ref
}

func selectedCostPointer(selected SelectedCostValuation) *SelectedCostValuation {
	copied := selected
	return &copied
}
