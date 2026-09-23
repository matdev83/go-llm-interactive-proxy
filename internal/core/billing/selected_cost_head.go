package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.3A pure selected-cost head domain contracts. The planner consumes a
// durable selected/posted valuation head, a caller compare-and-swap
// expectation and one new frozen selected valuation; it produces a typed
// correction outcome with an immutable valuation link and a balanced two-sided
// journal intent. It performs no SQL, no store transaction, no migration, no
// worker behavior and no provider parsing.
//
// This file owns the value contracts; selected_cost_head_transition.go owns the
// pure transition planner.

var (
	// ErrSelectedCostHeadInvalid identifies malformed or inconsistent selected
	// cost head transition input. Invalid input fails closed and produces no
	// plan.
	ErrSelectedCostHeadInvalid = errors.New("billing: invalid selected cost head transition")
	// ErrSelectedCostHeadLedgerPrecision identifies an exact monetary delta that
	// cannot be represented as checked integer ledger nanos. The planner never
	// rounds; callers need an explicit rounding policy before posting.
	ErrSelectedCostHeadLedgerPrecision = errors.New("billing: selected cost delta is not ledger-exact")
)

const (
	selectedCostAdjustmentOperationKind = "provider_call_cogs"
	selectedCostCogsLedgerAccount       = "inference_provider_cogs"
	selectedCostPayableLedgerAccount    = "provider_payable_clearing"
)

// SelectedCostHeadTransitionStatus is the correction outcome. Only Applied,
// NoOp and Replay may carry effects; Pending, Stale and Conflict are always
// zero-effect results.
type SelectedCostHeadTransitionStatus string

const (
	SelectedCostTransitionApplied  SelectedCostHeadTransitionStatus = "applied"
	SelectedCostTransitionNoOp     SelectedCostHeadTransitionStatus = "no_op"
	SelectedCostTransitionReplay   SelectedCostHeadTransitionStatus = "replay"
	SelectedCostTransitionPending  SelectedCostHeadTransitionStatus = "pending"
	SelectedCostTransitionStale    SelectedCostHeadTransitionStatus = "stale"
	SelectedCostTransitionConflict SelectedCostHeadTransitionStatus = "conflict"
)

// IsKnown reports whether s is a documented transition status.
func (s SelectedCostHeadTransitionStatus) IsKnown() bool {
	switch s {
	case SelectedCostTransitionApplied, SelectedCostTransitionNoOp, SelectedCostTransitionReplay,
		SelectedCostTransitionPending, SelectedCostTransitionStale, SelectedCostTransitionConflict:
		return true
	default:
		return false
	}
}

// SelectedCostHeadTransitionReason is the typed explanation of a transition
// outcome. It never erases the separate selection, comparison and posting
// planes carried by the result.
type SelectedCostHeadTransitionReason string

const (
	SelectedCostReasonNone                   SelectedCostHeadTransitionReason = ""
	SelectedCostReasonInitialPosting         SelectedCostHeadTransitionReason = "initial_posting"
	SelectedCostReasonSameNativeCurrency     SelectedCostHeadTransitionReason = "same_native_currency"
	SelectedCostReasonFrozenFXBasis          SelectedCostHeadTransitionReason = "frozen_fx_basis"
	SelectedCostReasonZeroDelta              SelectedCostHeadTransitionReason = "zero_delta"
	SelectedCostReasonAlreadyApplied         SelectedCostHeadTransitionReason = "already_applied"
	SelectedCostReasonProvisionalSelection   SelectedCostHeadTransitionReason = "provisional_selection"
	SelectedCostReasonUnknownSelection       SelectedCostHeadTransitionReason = "unknown_selection"
	SelectedCostReasonMissingAttemptedUsage  SelectedCostHeadTransitionReason = "missing_attempted_usage"
	SelectedCostReasonNotOperatorPayable     SelectedCostHeadTransitionReason = "not_operator_payable"
	SelectedCostReasonIncomparableSelection  SelectedCostHeadTransitionReason = "incomparable_selection"
	SelectedCostReasonSelectionConflict      SelectedCostHeadTransitionReason = "selection_conflict"
	SelectedCostReasonPostedCurrencyMismatch SelectedCostHeadTransitionReason = "posted_currency_mismatch"
	SelectedCostReasonFrozenFXBasisMismatch  SelectedCostHeadTransitionReason = "frozen_fx_basis_mismatch"
	SelectedCostReasonStaleHeadVersion       SelectedCostHeadTransitionReason = "stale_head_version"
	SelectedCostReasonStaleRevision          SelectedCostHeadTransitionReason = "stale_revision"
	SelectedCostReasonHeadIdentityConflict   SelectedCostHeadTransitionReason = "head_identity_conflict"
	SelectedCostReasonRevisionConflict       SelectedCostHeadTransitionReason = "revision_conflict"
)

// IsKnown reports whether r is a documented transition reason.
func (r SelectedCostHeadTransitionReason) IsKnown() bool {
	if r == SelectedCostReasonNone {
		return true
	}
	switch r {
	case SelectedCostReasonInitialPosting, SelectedCostReasonSameNativeCurrency,
		SelectedCostReasonFrozenFXBasis, SelectedCostReasonZeroDelta,
		SelectedCostReasonAlreadyApplied, SelectedCostReasonProvisionalSelection,
		SelectedCostReasonUnknownSelection, SelectedCostReasonMissingAttemptedUsage,
		SelectedCostReasonNotOperatorPayable, SelectedCostReasonIncomparableSelection,
		SelectedCostReasonSelectionConflict, SelectedCostReasonPostedCurrencyMismatch,
		SelectedCostReasonFrozenFXBasisMismatch, SelectedCostReasonStaleHeadVersion,
		SelectedCostReasonStaleRevision, SelectedCostReasonHeadIdentityConflict,
		SelectedCostReasonRevisionConflict:
		return true
	default:
		return false
	}
}

// SelectedCostComparisonStatus is the monetary comparability of the previously
// posted selected valuation and the new selected valuation.
type SelectedCostComparisonStatus string

const (
	SelectedCostComparisonNotEvaluated SelectedCostComparisonStatus = "not_evaluated"
	SelectedCostComparisonComparable   SelectedCostComparisonStatus = "comparable"
	SelectedCostComparisonPending      SelectedCostComparisonStatus = "pending"
	SelectedCostComparisonIncomparable SelectedCostComparisonStatus = "incomparable"
)

// IsKnown reports whether c is a documented comparison status.
func (c SelectedCostComparisonStatus) IsKnown() bool {
	switch c {
	case SelectedCostComparisonNotEvaluated, SelectedCostComparisonComparable,
		SelectedCostComparisonPending, SelectedCostComparisonIncomparable:
		return true
	default:
		return false
	}
}

// SelectedCostPostingStatus is the financial posting state of the planned
// transition, separate from the selection and comparison planes.
type SelectedCostPostingStatus string

const (
	SelectedCostPostingUnposted SelectedCostPostingStatus = "unposted"
	SelectedCostPostingPending  SelectedCostPostingStatus = "pending"
	SelectedCostPostingApplied  SelectedCostPostingStatus = "applied"
	SelectedCostPostingReplayed SelectedCostPostingStatus = "replayed"
)

// IsKnown reports whether s is a documented posting status.
func (s SelectedCostPostingStatus) IsKnown() bool {
	switch s {
	case SelectedCostPostingUnposted, SelectedCostPostingPending,
		SelectedCostPostingApplied, SelectedCostPostingReplayed:
		return true
	default:
		return false
	}
}

// SelectedCostValuationRef is the immutable identity of one frozen selected
// valuation revision. InputSetHash is the canonical evidence input identity, so
// a replay can be recognized without re-reading the rater.
type SelectedCostValuationRef struct {
	ValuationID  string `json:"valuation_id"`
	Revision     uint64 `json:"revision"`
	InputSetHash string `json:"input_set_hash"`
}

// Validate checks the immutable valuation identity.
func (r SelectedCostValuationRef) Validate() error {
	if !validEconomicIdentity(r.ValuationID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: selected valuation id is required", ErrSelectedCostHeadInvalid)
	}
	if r.Revision == 0 {
		return fmt.Errorf("%w: selected valuation revision is required", ErrSelectedCostHeadInvalid)
	}
	if !validEconomicIdentity(r.InputSetHash, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: selected valuation input set hash is required", ErrSelectedCostHeadInvalid)
	}
	return nil
}

// SelectedCostValuation is one frozen selected valuation in the operator
// postings plane: the Phase 12 selection outcome plus its valuation identity.
// Currency/Amount are the posted/view-currency values; FX and NativeAmount are
// present only when an explicit frozen conversion produced them.
type SelectedCostValuation struct {
	Ref          SelectedCostValuationRef
	Status       OperatorCostSelectionStatus
	Reason       OperatorCostSelectionReason
	Basis        OperatorCostSelectionBasis
	Provenance   OperatorCostProvenance
	Currency     string
	Amount       *MonetaryExactAmount
	NativeAmount *MonetaryExactAmount
	FX           *OperatorCostFXBasis
}

// NewSelectedCostValuation freezes one Phase 12 operator cost selection result
// onto a valuation identity. It never re-rates and never repairs an
// inconsistent selection.
func NewSelectedCostValuation(ref SelectedCostValuationRef, selection OperatorCostSelectionResult) (SelectedCostValuation, error) {
	out := SelectedCostValuation{
		Ref: ref, Status: selection.Status, Reason: selection.Reason, Basis: selection.Basis,
		Provenance: selection.Provenance, Currency: strings.TrimSpace(selection.Currency),
		Amount: cloneExactAmount(selection.Amount), NativeAmount: cloneExactAmount(selection.NativeAmount),
		FX: cloneOperatorCostFX(selection.FX),
	}
	normalized, err := economics.NormalizeCurrency(out.Currency)
	if err != nil {
		return SelectedCostValuation{}, fmt.Errorf("%w: selection currency: %v", ErrSelectedCostHeadInvalid, err)
	}
	out.Currency = normalized
	if err := out.Validate(); err != nil {
		return SelectedCostValuation{}, err
	}
	return out, nil
}

// Validate checks the frozen valuation contract: known selection state,
// canonical currency, nonnegative selected amount, and a frozen FX basis only
// with matching native amount and target currency.
func (v SelectedCostValuation) Validate() error {
	if err := v.Ref.Validate(); err != nil {
		return err
	}
	if !selectedCostSelectionStatusKnown(v.Status) {
		return fmt.Errorf("%w: unknown selection status %q", ErrSelectedCostHeadInvalid, v.Status)
	}
	if v.Basis != OperatorCostBasisNone && !v.Basis.IsKnown() {
		return fmt.Errorf("%w: unknown selection basis %q", ErrSelectedCostHeadInvalid, v.Basis)
	}
	if !v.Provenance.IsKnown() {
		return fmt.Errorf("%w: unknown selection provenance %q", ErrSelectedCostHeadInvalid, v.Provenance)
	}
	if err := selectedCostValidateCanonicalCurrency(v.Currency); err != nil {
		return err
	}
	if v.FX == nil {
		if v.NativeAmount != nil {
			return fmt.Errorf("%w: native amount requires an explicit frozen FX basis", ErrSelectedCostHeadInvalid)
		}
	} else {
		if err := v.FX.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrSelectedCostHeadInvalid, err)
		}
		if err := selectedCostValidateCanonicalCurrency(v.FX.FromCurrency); err != nil {
			return err
		}
		if err := selectedCostValidateCanonicalCurrency(v.FX.ToCurrency); err != nil {
			return err
		}
		if v.FX.ToCurrency != v.Currency {
			return fmt.Errorf("%w: frozen FX target %q does not match posted currency %q", ErrSelectedCostHeadInvalid, v.FX.ToCurrency, v.Currency)
		}
		if v.NativeAmount == nil {
			return fmt.Errorf("%w: frozen FX basis requires the native amount", ErrSelectedCostHeadInvalid)
		}
		if v.NativeAmount.Currency != v.FX.FromCurrency {
			return fmt.Errorf("%w: native amount currency %q does not match frozen FX source %q", ErrSelectedCostHeadInvalid, v.NativeAmount.Currency, v.FX.FromCurrency)
		}
	}
	switch v.Status {
	case OperatorCostSelectionStatusFinal, OperatorCostSelectionStatusKnownZero:
		if v.Amount == nil {
			return fmt.Errorf("%w: %s selection requires a selected amount", ErrSelectedCostHeadInvalid, v.Status)
		}
	default:
		// Provisional and unknown selections may retain an unposted amount.
	}
	if v.Amount != nil {
		if v.Amount.Currency != v.Currency {
			return fmt.Errorf("%w: amount currency %q does not match posted currency %q", ErrSelectedCostHeadInvalid, v.Amount.Currency, v.Currency)
		}
		value, err := v.Amount.Rat()
		if err != nil {
			return fmt.Errorf("%w: amount: %v", ErrSelectedCostHeadInvalid, err)
		}
		if value.Sign() < 0 {
			return fmt.Errorf("%w: selected amount cannot be negative", ErrSelectedCostHeadInvalid)
		}
		if v.Status == OperatorCostSelectionStatusKnownZero && value.Sign() != 0 {
			return fmt.Errorf("%w: known-zero selection must select exactly zero", ErrSelectedCostHeadInvalid)
		}
	}
	if v.NativeAmount != nil {
		if v.NativeAmount.Currency != v.FX.FromCurrency {
			return fmt.Errorf("%w: native amount currency mismatch", ErrSelectedCostHeadInvalid)
		}
		if _, err := v.NativeAmount.Rat(); err != nil {
			return fmt.Errorf("%w: native amount: %v", ErrSelectedCostHeadInvalid, err)
		}
	}
	return nil
}

// IdentityEqual reports whether two selected valuations are the same immutable
// semantic payload: identity, selection plane, posted/native amounts and frozen
// FX basis. It is stricter than Ref equality so a changed amount under the same
// revision is an identity conflict rather than a replay.
func (v SelectedCostValuation) IdentityEqual(other SelectedCostValuation) bool {
	if v.Ref != other.Ref || v.Status != other.Status || v.Reason != other.Reason ||
		v.Basis != other.Basis || v.Provenance != other.Provenance || v.Currency != other.Currency {
		return false
	}
	if !monetaryExactAmountsEqual(v.Amount, other.Amount) {
		return false
	}
	if !monetaryExactAmountsEqual(v.NativeAmount, other.NativeAmount) {
		return false
	}
	switch {
	case v.FX == nil && other.FX == nil:
		return true
	case v.FX != nil && other.FX != nil:
		return sameOperatorCostFX(v.FX, other.FX)
	default:
		return false
	}
}

// Clone returns a detached copy of the frozen valuation.
func (v SelectedCostValuation) Clone() SelectedCostValuation {
	out := v
	out.Amount = cloneExactAmount(v.Amount)
	out.NativeAmount = cloneExactAmount(v.NativeAmount)
	out.FX = cloneOperatorCostFX(v.FX)
	return out
}

// SelectedCostHead is the current selected/posted valuation pointer for one
// economic charge. Version is the compare-and-swap token a writer must fence
// against; the immutable operation link and journal rows remain the audit
// history.
type SelectedCostHead struct {
	AccountID string
	CallID    BillingCallID
	HeadKey   string
	Subject   metering.SubjectRef
	Version   uint64
	Selected  *SelectedCostValuation
	// LastOperationKey is the transition operation that most recently advanced
	// this head. OriginalTransactionID/LastTransactionID reference the durable
	// correction journal chain; both are assigned by the persistence adapter.
	LastOperationKey      string
	OriginalTransactionID string
	LastTransactionID     string
	// PostingState is the exact persisted financial posting state of this head,
	// separate from the selection and comparison planes. It is populated from
	// the durable head row by the persistence adapter; the empty value is only
	// valid for a caller-built in-memory head that was never read from durable
	// storage.
	PostingState SelectedCostPostingStatus
}

// Validate checks the request-scoped selected-cost head identity.
func (h SelectedCostHead) Validate() error {
	if !validEconomicIdentity(h.AccountID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: head account id is required", ErrSelectedCostHeadInvalid)
	}
	if err := h.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: head call id: %v", ErrSelectedCostHeadInvalid, err)
	}
	if !validEconomicIdentity(h.HeadKey, metering.MaxSchemaIDBytes) || strings.TrimSpace(h.HeadKey) != h.HeadKey {
		return fmt.Errorf("%w: head key is required and must not carry surrounding whitespace", ErrSelectedCostHeadInvalid)
	}
	if err := h.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: head subject: %v", ErrSelectedCostHeadInvalid, err)
	}
	if h.Subject.Kind != metering.SubjectBLeg && h.Subject.Kind != metering.SubjectProviderCharge {
		return fmt.Errorf("%w: request-scoped B-leg/provider-charge subject required", ErrSelectedCostHeadInvalid)
	}
	if h.Subject.BillingCallID != "" && h.Subject.BillingCallID != h.CallID.String() {
		return fmt.Errorf("%w: head subject billing call differs from call id", ErrSelectedCostHeadInvalid)
	}
	if h.Subject.CallID != "" && h.Subject.CallID != h.CallID.String() {
		return fmt.Errorf("%w: head subject call differs from call id", ErrSelectedCostHeadInvalid)
	}
	if h.Subject.AccountID != "" && h.Subject.AccountID != h.AccountID {
		return fmt.Errorf("%w: head subject account differs from account id", ErrSelectedCostHeadInvalid)
	}
	if (h.Version == 0) != (h.Selected == nil) {
		return fmt.Errorf("%w: head version %d and selected valuation must be present together", ErrSelectedCostHeadInvalid, h.Version)
	}
	if h.Version == math.MaxUint64 {
		return fmt.Errorf("%w: head version cannot advance", ErrSelectedCostHeadInvalid)
	}
	if h.Selected != nil {
		if err := h.Selected.Validate(); err != nil {
			return err
		}
	}
	if h.PostingState != "" && !h.PostingState.IsKnown() {
		return fmt.Errorf("%w: unknown head posting state %q", ErrSelectedCostHeadInvalid, h.PostingState)
	}
	return nil
}

// Clone returns a detached copy of the head.
func (h SelectedCostHead) Clone() SelectedCostHead {
	out := h
	if h.Selected != nil {
		selected := h.Selected.Clone()
		out.Selected = &selected
	}
	return out
}

// SelectedCostHeadExpectation is the caller's compare-and-swap read: the head
// version and the previously posted selected valuation the caller planned
// against. Version zero and a nil previous valuation are required together.
type SelectedCostHeadExpectation struct {
	Version  uint64
	Previous *SelectedCostValuation
}

// Validate checks CAS read consistency.
func (e SelectedCostHeadExpectation) Validate() error {
	if (e.Version == 0) != (e.Previous == nil) {
		return fmt.Errorf("%w: expectation version and previous valuation must be present together", ErrSelectedCostHeadInvalid)
	}
	if e.Previous != nil {
		if err := e.Previous.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// SelectedCostValuationLink is the immutable linkage from the previously posted
// selected valuation to the newly selected valuation. It is inserted atomically
// with the journal delta and head transition.
type SelectedCostValuationLink struct {
	AccountID          string
	CallID             BillingCallID
	HeadKey            string
	Subject            metering.SubjectRef
	Previous           *SelectedCostValuationRef
	Current            SelectedCostValuationRef
	Currency           string
	FX                 *OperatorCostFXBasis
	AdjustmentRevision uint64
	OperationKey       string
}

// Key returns the stable link identity. Economic charge identity, both selected
// valuation identities, the delta currency or frozen FX basis, and the
// adjustment revision are the unique members.
func (l SelectedCostValuationLink) Key() (string, error) {
	payload, err := json.Marshal(struct {
		Version            string                    `json:"version"`
		AccountID          string                    `json:"account_id"`
		CallID             string                    `json:"call_id"`
		HeadKey            string                    `json:"head_key"`
		Subject            metering.SubjectRef       `json:"subject"`
		Previous           *SelectedCostValuationRef `json:"previous,omitempty"`
		Current            SelectedCostValuationRef  `json:"current"`
		Currency           string                    `json:"currency"`
		FX                 *selectedCostFXWire       `json:"fx,omitempty"`
		AdjustmentRevision uint64                    `json:"adjustment_revision"`
	}{
		"selected-cost-link:v1", l.AccountID, l.CallID.String(), l.HeadKey, l.Subject,
		l.Previous, l.Current, l.Currency, selectedCostFXWireOf(l.FX), l.AdjustmentRevision,
	})
	if err != nil {
		return "", fmt.Errorf("%w: valuation link identity: %v", ErrSelectedCostHeadInvalid, err)
	}
	digest := sha256.Sum256(payload)
	return "selected-cost-link:v1:" + hex.EncodeToString(digest[:]), nil
}

// BalancedJournalIntent is one pure two-sided journal posting plan. It uses the
// existing debit/credit and ledger-account contracts with strictly positive
// exact amounts; a downward correction reverses the applicable accounts instead
// of recording a negative gross amount. Entry amounts and transaction IDs are
// assigned by the durable journal writer.
type BalancedJournalIntent struct {
	OperationKey          string
	AccountID             string
	TurnID                string
	ALegID                string
	BLegID                string
	HeadKey               string
	OperationKind         string
	Currency              string
	CorrectionGroupID     string
	ReversalOf            string
	CorrectsTransactionID string
	Entries               []JournalEntry
	Replayed              bool
}

// IsNoOp reports a comparable head transition with no monetary delta; such an
// intent carries no journal entries and writes no journal row.
func (i BalancedJournalIntent) IsNoOp() bool { return len(i.Entries) == 0 }

// Validate checks the two-sided balanced contract with positive gross amounts.
func (i BalancedJournalIntent) Validate() error {
	if strings.TrimSpace(i.OperationKey) == "" || strings.TrimSpace(i.AccountID) == "" || strings.TrimSpace(i.HeadKey) == "" {
		return fmt.Errorf("%w: journal intent operation, account and head are required", ErrSelectedCostHeadInvalid)
	}
	if err := selectedCostValidateCanonicalCurrency(i.Currency); err != nil {
		return err
	}
	if i.ReversalOf != "" && i.ReversalOf == i.OperationKey {
		return fmt.Errorf("%w: journal intent cannot reverse itself", ErrSelectedCostHeadInvalid)
	}
	if i.CorrectsTransactionID != "" && i.CorrectsTransactionID == i.OperationKey {
		return fmt.Errorf("%w: journal intent cannot correct itself", ErrSelectedCostHeadInvalid)
	}
	if len(i.Entries) == 0 {
		return nil
	}
	if len(i.Entries) < 2 {
		return fmt.Errorf("%w: journal intent requires two sides", ErrSelectedCostHeadInvalid)
	}
	var debits, credits int64
	hasDebit, hasCredit := false, false
	for index, entry := range i.Entries {
		if strings.TrimSpace(entry.LedgerAccount) == "" {
			return fmt.Errorf("%w: journal intent entry %d account is required", ErrSelectedCostHeadInvalid, index)
		}
		if entry.Side != JournalDebit && entry.Side != JournalCredit {
			return fmt.Errorf("%w: journal intent entry %d side is required", ErrSelectedCostHeadInvalid, index)
		}
		if entry.Amount.Currency != i.Currency {
			return fmt.Errorf("%w: journal intent entry %d currency %q does not match %q", ErrSelectedCostHeadInvalid, index, entry.Amount.Currency, i.Currency)
		}
		if entry.Amount.Nano <= 0 {
			return fmt.Errorf("%w: journal intent entry %d requires a positive gross amount", ErrSelectedCostHeadInvalid, index)
		}
		var err error
		if entry.Side == JournalDebit {
			hasDebit = true
			debits, err = checkedAdd(debits, entry.Amount.Nano)
		} else {
			hasCredit = true
			credits, err = checkedAdd(credits, entry.Amount.Nano)
		}
		if err != nil {
			return fmt.Errorf("%w: journal intent entry %d: %v", ErrSelectedCostHeadInvalid, index, err)
		}
	}
	if !hasDebit || !hasCredit {
		return fmt.Errorf("%w: journal intent requires both debit and credit sides", ErrSelectedCostHeadInvalid)
	}
	if debits != credits {
		return fmt.Errorf("%w: journal intent debits=%d credits=%d", ErrSelectedCostHeadInvalid, debits, credits)
	}
	return nil
}

// SelectedCostHeadTransitionInput is the pure transition request: the durable
// current head, the caller compare-and-swap read and the new frozen selected
// valuation.
type SelectedCostHeadTransitionInput struct {
	Current  SelectedCostHead
	Expected SelectedCostHeadExpectation
	Selected SelectedCostValuation
}

// SelectedCostHeadTransition is the deterministic correction plan. Status,
// Reason and the separate selection/comparison/posting planes report the
// outcome; Previous/Selected/Delta/Link/Journal/NextHead are populated only
// when the corresponding effect is permitted.
type SelectedCostHeadTransition struct {
	AccountID string
	CallID    BillingCallID
	HeadKey   string
	Subject   metering.SubjectRef

	Status SelectedCostHeadTransitionStatus
	Reason SelectedCostHeadTransitionReason

	SelectionStatus OperatorCostSelectionStatus
	SelectionReason OperatorCostSelectionReason
	Comparison      SelectedCostComparisonStatus
	Posting         SelectedCostPostingStatus

	Previous *SelectedCostValuation
	Selected *SelectedCostValuation
	Delta    *MonetaryExactAmount
	Link     *SelectedCostValuationLink
	Journal  *BalancedJournalIntent
	NextHead *SelectedCostHead

	OperationKey string
	Fingerprint  string
}

// HasEffects reports whether the plan writes financial or selected-head state.
func (t SelectedCostHeadTransition) HasEffects() bool {
	return t.NextHead != nil || (t.Journal != nil && len(t.Journal.Entries) != 0)
}

// selectedCostChargeIdentity is the economic charge scope shared by the
// operation key and fingerprint preimages.
type selectedCostChargeIdentity struct {
	AccountID string
	CallID    BillingCallID
	HeadKey   string
	Subject   metering.SubjectRef
}

// selectedCostFXWire is the deterministic frozen FX identity and rate material
// retained in durable operation identities.
type selectedCostFXWire struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	FromCurrency string `json:"from_currency"`
	ToCurrency   string `json:"to_currency"`
	Rate         string `json:"rate"`
}

func selectedCostFXWireOf(fx *OperatorCostFXBasis) *selectedCostFXWire {
	if fx == nil {
		return nil
	}
	rate := ""
	if fx.Rate != nil {
		rate = fx.Rate.CanonicalString()
	}
	return &selectedCostFXWire{ID: fx.ID, Version: fx.Version, FromCurrency: fx.FromCurrency, ToCurrency: fx.ToCurrency, Rate: rate}
}

// selectedCostValuationWire is the deterministic semantic payload retained in
// the operation fingerprint.
type selectedCostValuationWire struct {
	ValuationID  string                      `json:"valuation_id"`
	Revision     uint64                      `json:"revision"`
	InputSetHash string                      `json:"input_set_hash"`
	Status       OperatorCostSelectionStatus `json:"status"`
	Reason       OperatorCostSelectionReason `json:"reason,omitempty"`
	Basis        OperatorCostSelectionBasis  `json:"basis,omitempty"`
	Provenance   OperatorCostProvenance      `json:"provenance"`
	Currency     string                      `json:"currency"`
	Amount       *MonetaryExactAmount        `json:"amount,omitempty"`
	NativeAmount *MonetaryExactAmount        `json:"native_amount,omitempty"`
	FX           *selectedCostFXWire         `json:"fx,omitempty"`
}

func selectedCostValuationWireOf(v SelectedCostValuation) selectedCostValuationWire {
	return selectedCostValuationWire{
		ValuationID: v.Ref.ValuationID, Revision: v.Ref.Revision, InputSetHash: v.Ref.InputSetHash,
		Status: v.Status, Reason: v.Reason, Basis: v.Basis, Provenance: v.Provenance, Currency: v.Currency,
		Amount: v.Amount, NativeAmount: v.NativeAmount, FX: selectedCostFXWireOf(v.FX),
	}
}

// selectedCostOperationKey derives the stable operation identity. It includes
// the economic charge identity, both selected valuation identities, the delta
// currency or frozen FX basis, and the adjustment revision, but never the
// monetary amount: a different payload under the same key is a conflict.
func selectedCostOperationKey(identity selectedCostChargeIdentity, previous *SelectedCostValuationRef, selected SelectedCostValuation, deltaCurrency string) (string, error) {
	payload, err := json.Marshal(struct {
		Version            string                    `json:"version"`
		AccountID          string                    `json:"account_id"`
		CallID             string                    `json:"call_id"`
		HeadKey            string                    `json:"head_key"`
		Subject            metering.SubjectRef       `json:"subject"`
		Previous           *SelectedCostValuationRef `json:"previous,omitempty"`
		Current            SelectedCostValuationRef  `json:"current"`
		Currency           string                    `json:"currency"`
		FX                 *selectedCostFXWire       `json:"fx,omitempty"`
		AdjustmentRevision uint64                    `json:"adjustment_revision"`
	}{
		"selected-cost-adjustment:v1", identity.AccountID, identity.CallID.String(), identity.HeadKey, identity.Subject,
		previous, selected.Ref, deltaCurrency, selectedCostFXWireOf(selected.FX), selected.Ref.Revision,
	})
	if err != nil {
		return "", fmt.Errorf("%w: operation key: %v", ErrSelectedCostHeadInvalid, err)
	}
	digest := sha256.Sum256(payload)
	return "selected-cost-adjustment:v1:" + hex.EncodeToString(digest[:]), nil
}

// selectedCostFingerprint derives the semantic payload identity. The delta is
// included, so replay detection can reject a changed amount under the same
// stable operation key.
func selectedCostFingerprint(identity selectedCostChargeIdentity, expected SelectedCostHeadExpectation, selected SelectedCostValuation, deltaCurrency string, delta *MonetaryExactAmount) (string, error) {
	var previous *selectedCostValuationWire
	if expected.Previous != nil {
		wire := selectedCostValuationWireOf(*expected.Previous)
		previous = &wire
	}
	payload, err := json.Marshal(struct {
		Version         string                     `json:"version"`
		AccountID       string                     `json:"account_id"`
		CallID          string                     `json:"call_id"`
		HeadKey         string                     `json:"head_key"`
		Subject         metering.SubjectRef        `json:"subject"`
		ExpectedVersion uint64                     `json:"expected_version"`
		Previous        *selectedCostValuationWire `json:"previous,omitempty"`
		Selected        selectedCostValuationWire  `json:"selected"`
		Currency        string                     `json:"currency"`
		Delta           *MonetaryExactAmount       `json:"delta,omitempty"`
	}{
		"selected-cost-adjustment-fp:v1", identity.AccountID, identity.CallID.String(), identity.HeadKey, identity.Subject,
		expected.Version, previous, selectedCostValuationWireOf(selected), deltaCurrency, delta,
	})
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint: %v", ErrSelectedCostHeadInvalid, err)
	}
	digest := sha256.Sum256(payload)
	return "selected-cost-adjustment-fp:v1:" + hex.EncodeToString(digest[:]), nil
}

// selectedCostSelectionStatusKnown reports a documented Phase 12 selection
// status.
func selectedCostSelectionStatusKnown(status OperatorCostSelectionStatus) bool {
	switch status {
	case OperatorCostSelectionStatusFinal, OperatorCostSelectionStatusProvisional,
		OperatorCostSelectionStatusKnownZero, OperatorCostSelectionStatusUnknown,
		OperatorCostSelectionStatusIncomparable, OperatorCostSelectionStatusConflict,
		OperatorCostSelectionStatusNotOperatorPayable:
		return true
	default:
		return false
	}
}

// selectedCostValidateCanonicalCurrency requires the exact normalized currency
// spelling so ledger and identity comparisons never depend on case folding.
func selectedCostValidateCanonicalCurrency(currency string) error {
	normalized, err := economics.NormalizeCurrency(currency)
	if err != nil {
		return fmt.Errorf("%w: currency: %v", ErrSelectedCostHeadInvalid, err)
	}
	if normalized != currency {
		return fmt.Errorf("%w: currency %q is not canonical", ErrSelectedCostHeadInvalid, currency)
	}
	return nil
}
