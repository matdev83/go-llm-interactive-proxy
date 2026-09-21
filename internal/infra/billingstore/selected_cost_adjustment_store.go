package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.3B durable selected-cost adjustment adapter. One local transaction
// loads and locks the selected/posted head, invokes the pure 13.3A planner,
// persists the immutable adjustment operation (carrying the valuation link),
// appends the balanced journal delta and advances the head with a compare-and-
// swap on the expected version. Pending, stale, conflict and replay outcomes
// perform zero new financial, link or head effects.

var (
	_ billing.SelectedCostAdjustmentStore = (*DurableStore)(nil)
	_ billing.SelectedCostHeadReader      = (*DurableStore)(nil)
)

// ErrSelectedCostAdjustmentMismatch identifies a retained selected-cost head
// or adjustment row that is not the canonical self-validating record it claims
// to be.
var ErrSelectedCostAdjustmentMismatch = errors.New("billingstore: selected cost adjustment record mismatch")

const (
	selectedCostAdjustmentTxAttempts = 80
	selectedCostAdjustmentTxDelay    = 3 * time.Millisecond
)

type selectedCostAdjustmentRow struct {
	ID                   int64  `bun:"id"`
	OperationKey         string `bun:"operation_key"`
	LinkKey              string `bun:"link_key"`
	Fingerprint          string `bun:"fingerprint"`
	Status               string `bun:"status"`
	Comparison           string `bun:"comparison"`
	Posting              string `bun:"posting"`
	PreviousValuationID  string `bun:"previous_valuation_id"`
	PreviousRevision     int64  `bun:"previous_revision"`
	PreviousInputSetHash string `bun:"previous_input_set_hash"`
	CurrentValuationID   string `bun:"current_valuation_id"`
	CurrentRevision      int64  `bun:"current_revision"`
	CurrentInputSetHash  string `bun:"current_input_set_hash"`
	Currency             string `bun:"currency"`
	FXJSON               string `bun:"fx_json"`
	AdjustmentRevision   int64  `bun:"adjustment_revision"`
	DeltaJSON            string `bun:"delta_json"`
	JournalTransactionID string `bun:"journal_transaction_id"`
	CreatedAt            int64  `bun:"created_at_unix"`
}

const selectedCostAdjustmentSelect = `SELECT id, operation_key, link_key, fingerprint, status, comparison, posting, previous_valuation_id, previous_revision, previous_input_set_hash, current_valuation_id, current_revision, current_input_set_hash, currency, fx_json, adjustment_revision, delta_json, journal_transaction_id, created_at_unix FROM billing_selected_cost_adjustments`

// ApplySelectedCostAdjustment atomically applies one selected-cost head
// compare-and-swap. Pure provider COGS never locks or updates the customer
// account balance/version.
func (s *DurableStore) ApplySelectedCostAdjustment(ctx context.Context, input billing.SelectedCostAdjustmentInput) (billing.SelectedCostAdjustmentResult, error) {
	if s == nil || s.db == nil {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("billingstore: nil store")
	}
	if ctx == nil {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: nil context", billing.ErrSelectedCostAdjustmentInvalid)
	}
	if err := ctx.Err(); err != nil {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: %w", billing.ErrSelectedCostAdjustmentInvalid, err)
	}
	normalized, err := input.Normalize()
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	if normalized.Subject.StoreID != s.storeID {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: selected cost adjustment subject store", ErrEconomicsOutOfScope)
	}
	return withAccountTx(ctx, accountTxRetry{
		Attempts: selectedCostAdjustmentTxAttempts, Delay: selectedCostAdjustmentTxDelay,
		Exhausted: fmt.Errorf("%w: selected cost adjustment retry budget exhausted", billing.ErrBillingStoreUnavailable),
	}, func() (billing.SelectedCostAdjustmentResult, error) {
		return s.applySelectedCostAdjustmentAttempt(ctx, normalized)
	})
}

func (s *DurableStore) applySelectedCostAdjustmentAttempt(ctx context.Context, input billing.SelectedCostAdjustmentInput) (billing.SelectedCostAdjustmentResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("billingstore: begin selected cost adjustment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	account, err := getAccountTx(ctx, tx, input.AccountID)
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	subjectJSON, err := json.Marshal(input.Subject)
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("billingstore: encode selected cost subject: %w", err)
	}
	// PostgreSQL locks the head row so concurrent revisions serialize before
	// the CAS transition; SQLite relies on its writer transaction, while the
	// CAS still protects a stale snapshot if a competing writer wins first.
	row, found, err := s.loadProviderCostHead(ctx, tx, input.AccountID, input.CallID, input.HeadKey, true)
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	current := billing.SelectedCostHead{
		AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey, Subject: input.Subject,
	}
	if found {
		if row.SubjectJSON != string(subjectJSON) {
			return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: durable head subject differs from adjustment subject", billing.ErrSelectedCostAdjustmentConflict)
		}
		current, err = s.selectedCostHeadFromRow(row)
		if err != nil {
			return billing.SelectedCostAdjustmentResult{}, err
		}
	}
	plan, err := billing.PlanSelectedCostHeadTransition(billing.SelectedCostHeadTransitionInput{
		Current: current, Expected: input.Expected, Selected: input.Selected,
	})
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	switch plan.Status {
	case billing.SelectedCostTransitionApplied, billing.SelectedCostTransitionNoOp:
		return s.applySelectedCostAdjustmentEffects(ctx, tx, input, before, current, row, found, plan)
	case billing.SelectedCostTransitionReplay:
		return s.replaySelectedCostAdjustment(ctx, tx, input, current, plan)
	default:
		// Pending, stale and conflict outcomes plan no writes; the deferred
		// rollback discards the read-only transaction.
		return selectedCostAdjustmentResultFromPlan(input, current, plan), nil
	}
}

func (s *DurableStore) applySelectedCostAdjustmentEffects(ctx context.Context, tx bun.Tx, input billing.SelectedCostAdjustmentInput, before billing.AccountSnapshot, current billing.SelectedCostHead, row providerCostHeadRow, found bool, plan billing.SelectedCostHeadTransition) (billing.SelectedCostAdjustmentResult, error) {
	if plan.Link == nil || plan.NextHead == nil || plan.Delta == nil {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: selected cost head transition is missing its effects", billing.ErrSelectedCostAdjustmentInvalid)
	}
	linkKey, err := plan.Link.Key()
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	var transactionID string
	if plan.Journal != nil && !plan.Journal.IsNoOp() {
		posting, err := s.postSelectedCostAdjustmentJournalInTx(ctx, tx, input, before, *plan.Journal)
		if err != nil {
			return billing.SelectedCostAdjustmentResult{}, err
		}
		transactionID = posting.Transaction.ID
		if err := s.economicFault("after_selected_cost_adjustment_journal"); err != nil {
			return billing.SelectedCostAdjustmentResult{}, err
		}
	}
	if err := insertSelectedCostAdjustmentInTx(ctx, tx, s.storeID, input, plan, linkKey, transactionID); err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	if err := s.economicFault("after_selected_cost_adjustment_link"); err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	if err := s.persistSelectedCostHeadInTx(ctx, tx, input, row, found, plan, transactionID); err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	if err := s.economicFault("after_selected_cost_adjustment"); err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("billingstore: commit selected cost adjustment: %w", err)
	}
	result := selectedCostAdjustmentResultFromPlan(input, current, plan)
	result.LinkKey = linkKey
	result.TransactionID = transactionID
	result.HeadVersion = plan.NextHead.Version
	return result, nil
}

func (s *DurableStore) replaySelectedCostAdjustment(ctx context.Context, tx bun.Tx, input billing.SelectedCostAdjustmentInput, current billing.SelectedCostHead, plan billing.SelectedCostHeadTransition) (billing.SelectedCostAdjustmentResult, error) {
	result := selectedCostAdjustmentResultFromPlan(input, current, plan)
	result.TransactionID = current.LastTransactionID
	if plan.Link != nil {
		linkKey, err := plan.Link.Key()
		if err != nil {
			return billing.SelectedCostAdjustmentResult{}, err
		}
		result.LinkKey = linkKey
	}
	stored, found, err := loadLatestSelectedCostAdjustment(ctx, tx, s.storeID, input.AccountID, input.CallID, input.HeadKey)
	if err != nil {
		return billing.SelectedCostAdjustmentResult{}, err
	}
	if found && selectedCostAdjustmentStoredMatches(stored, input.Selected.Ref) {
		return selectedCostAdjustmentReplayResult(stored, input, current)
	}
	// No durable adjustment operation backs this head. A legacy provider-cost
	// head may already carry exactly the requested valuation; only a caller CAS
	// read that matches the durable head may be a benign replay no-op.
	previousMatches := (current.Selected == nil) == (input.Expected.Previous == nil)
	if previousMatches && input.Expected.Previous != nil {
		previousMatches = current.Selected != nil && current.Selected.IdentityEqual(*input.Expected.Previous)
	}
	if input.Expected.Version != current.Version || !previousMatches {
		return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: replay for head %s has no durable adjustment operation", billing.ErrSelectedCostAdjustmentConflict, input.HeadKey)
	}
	return result, nil
}

// GetSelectedCostHead returns the current selected/posted valuation pointer,
// including the exact frozen valuation identity and correction chain, and
// never waits on or mutates the customer account balance.
func (s *DurableStore) GetSelectedCostHead(ctx context.Context, accountID string, callID billing.BillingCallID, headKey string) (billing.SelectedCostHead, error) {
	if s == nil || s.db == nil {
		return billing.SelectedCostHead{}, fmt.Errorf("billingstore: nil store")
	}
	if ctx == nil {
		return billing.SelectedCostHead{}, fmt.Errorf("%w: nil context", billing.ErrSelectedCostAdjustmentInvalid)
	}
	if err := ctx.Err(); err != nil {
		return billing.SelectedCostHead{}, fmt.Errorf("%w: %w", billing.ErrSelectedCostAdjustmentInvalid, err)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || callID.Validate() != nil || strings.TrimSpace(headKey) != headKey || headKey == "" {
		return billing.SelectedCostHead{}, fmt.Errorf("%w: head identity", billing.ErrSelectedCostAdjustmentInvalid)
	}
	row, found, err := s.loadProviderCostHead(ctx, s.db, accountID, callID, headKey, false)
	if err != nil {
		return billing.SelectedCostHead{}, err
	}
	if !found {
		return billing.SelectedCostHead{}, billing.ErrProviderCostHeadNotFound
	}
	return s.selectedCostHeadFromRow(row)
}

// selectedCostHeadFromRow rebuilds the exact frozen valuation head. Legacy rows
// without the additive identity columns derive the posted exact amount from
// the retained integer nanos and default to a final attempted selection.
func (s *DurableStore) selectedCostHeadFromRow(row providerCostHeadRow) (billing.SelectedCostHead, error) {
	callID, err := billing.ParseBillingCallID(row.CallID)
	if err != nil {
		return billing.SelectedCostHead{}, fmt.Errorf("%w: head call id: %v", ErrSelectedCostAdjustmentMismatch, err)
	}
	var subject metering.SubjectRef
	if err := json.Unmarshal([]byte(row.SubjectJSON), &subject); err != nil {
		return billing.SelectedCostHead{}, fmt.Errorf("%w: head subject decode", ErrSelectedCostAdjustmentMismatch)
	}
	if row.HeadVersion <= 0 || row.EvidenceRevision <= 0 {
		return billing.SelectedCostHead{}, fmt.Errorf("%w: head version/revision", ErrSelectedCostAdjustmentMismatch)
	}
	revision := uint64(row.EvidenceRevision)
	if row.ValuationRevision != 0 {
		if row.ValuationRevision < 0 {
			return billing.SelectedCostHead{}, fmt.Errorf("%w: negative valuation revision", ErrSelectedCostAdjustmentMismatch)
		}
		revision = uint64(row.ValuationRevision)
	}
	amount, err := selectedCostAdjustmentPostedAmount(row)
	if err != nil {
		return billing.SelectedCostHead{}, err
	}
	status := billing.OperatorCostSelectionStatus(row.SelectionStatus)
	if status == "" {
		status = billing.OperatorCostSelectionStatusFinal
	}
	provenance := billing.OperatorCostProvenance(row.SelectionProvenance)
	if provenance == "" {
		provenance = billing.OperatorCostProvenanceAttempted
	}
	selected := billing.SelectedCostValuation{
		Ref:    billing.SelectedCostValuationRef{ValuationID: row.ValuationID, Revision: revision, InputSetHash: row.InputSetHash},
		Status: status, Reason: billing.OperatorCostSelectionReason(row.SelectionReason),
		Basis: billing.OperatorCostSelectionBasis(row.SelectionBasis), Provenance: provenance,
		Currency: row.Currency, Amount: amount,
	}
	if strings.TrimSpace(row.NativeAmountJSON) != "" {
		var native billing.MonetaryExactAmount
		if err := json.Unmarshal([]byte(row.NativeAmountJSON), &native); err != nil {
			return billing.SelectedCostHead{}, fmt.Errorf("%w: native exact amount decode", ErrSelectedCostAdjustmentMismatch)
		}
		if err := native.Validate(); err != nil {
			return billing.SelectedCostHead{}, fmt.Errorf("%w: native exact amount: %v", ErrSelectedCostAdjustmentMismatch, err)
		}
		selected.NativeAmount = &native
	}
	if strings.TrimSpace(row.FXJSON) != "" {
		var fx billing.OperatorCostFXBasis
		if err := json.Unmarshal([]byte(row.FXJSON), &fx); err != nil {
			return billing.SelectedCostHead{}, fmt.Errorf("%w: frozen FX decode", ErrSelectedCostAdjustmentMismatch)
		}
		if err := fx.Validate(); err != nil {
			return billing.SelectedCostHead{}, fmt.Errorf("%w: frozen FX: %v", ErrSelectedCostAdjustmentMismatch, err)
		}
		selected.FX = &fx
	}
	if err := selected.Validate(); err != nil {
		return billing.SelectedCostHead{}, fmt.Errorf("%w: %v", ErrSelectedCostAdjustmentMismatch, err)
	}
	// Posting state is read exactly from the durable column. The additive
	// migration is NOT NULL DEFAULT 'applied', so a legacy upgraded row reads
	// 'applied'; an unset value mirrors that documented schema default rather
	// than inferring anything from transaction IDs or the selection status. A
	// non-empty unknown state is corrupt and fails closed.
	postingState := billing.SelectedCostPostingStatus(row.PostingState)
	if postingState == "" {
		postingState = billing.SelectedCostPostingApplied
	}
	if !postingState.IsKnown() {
		return billing.SelectedCostHead{}, fmt.Errorf("%w: head posting state", ErrSelectedCostAdjustmentMismatch)
	}
	return billing.SelectedCostHead{
		AccountID: row.AccountID, CallID: callID, HeadKey: row.HeadKey, Subject: subject,
		Version: uint64(row.HeadVersion), Selected: &selected,
		LastOperationKey: row.LastOperationKey, LastTransactionID: row.LastTransactionID,
		OriginalTransactionID: providerCostOriginalTransactionID(row.OriginalTransactionID, row.LastTransactionID),
		PostingState:          postingState,
	}, nil
}

// selectedCostAdjustmentPostedAmount reads the exact posted amount and proves
// that it reconciles with the retained integer nanos before it is returned.
func selectedCostAdjustmentPostedAmount(row providerCostHeadRow) (*billing.MonetaryExactAmount, error) {
	if strings.TrimSpace(row.PostedAmountJSON) != "" {
		var amount billing.MonetaryExactAmount
		if err := json.Unmarshal([]byte(row.PostedAmountJSON), &amount); err != nil {
			return nil, fmt.Errorf("%w: posted exact amount decode", ErrSelectedCostAdjustmentMismatch)
		}
		if err := amount.Validate(); err != nil {
			return nil, fmt.Errorf("%w: posted exact amount: %v", ErrSelectedCostAdjustmentMismatch, err)
		}
		if amount.Currency != row.Currency {
			return nil, fmt.Errorf("%w: posted exact amount currency", ErrSelectedCostAdjustmentMismatch)
		}
		nanos, err := selectedCostAdjustmentExactNanos(&amount)
		if err != nil || nanos != row.AmountNano {
			return nil, fmt.Errorf("%w: posted exact amount does not match retained nanos", ErrSelectedCostAdjustmentMismatch)
		}
		return &amount, nil
	}
	decimal := metering.DecimalFromNanoUnits(row.AmountNano)
	return &billing.MonetaryExactAmount{Currency: row.Currency, Decimal: &decimal}, nil
}

// selectedCostAdjustmentExactNanos converts an exact amount into checked ledger
// nanos. Sub-nano or out-of-range values fail closed; the writer never rounds.
func selectedCostAdjustmentExactNanos(amount *billing.MonetaryExactAmount) (int64, error) {
	if amount == nil {
		return 0, fmt.Errorf("%w: selected exact amount is required", billing.ErrSelectedCostAdjustmentInvalid)
	}
	if amount.Decimal != nil {
		nanos, err := amount.Decimal.ToLedgerNanos()
		if err != nil {
			return 0, fmt.Errorf("%w: selected amount %s: %v", billing.ErrSelectedCostHeadLedgerPrecision, amount.Decimal.CanonicalString(), err)
		}
		return nanos, nil
	}
	value, err := amount.Rat()
	if err != nil {
		return 0, fmt.Errorf("%w: selected amount: %v", billing.ErrSelectedCostAdjustmentInvalid, err)
	}
	nanos := new(big.Rat).Mul(value, big.NewRat(1_000_000_000, 1))
	if !nanos.IsInt() {
		return 0, fmt.Errorf("%w: selected amount %s is not ledger-exact", billing.ErrSelectedCostHeadLedgerPrecision, value.RatString())
	}
	if !nanos.Num().IsInt64() {
		return 0, fmt.Errorf("%w: selected amount %s exceeds ledger range", billing.ErrSelectedCostHeadLedgerPrecision, value.RatString())
	}
	return nanos.Num().Int64(), nil
}

func (s *DurableStore) postSelectedCostAdjustmentJournalInTx(ctx context.Context, tx bun.Tx, input billing.SelectedCostAdjustmentInput, before billing.AccountSnapshot, intent billing.BalancedJournalIntent) (billing.Posting, error) {
	journal := billing.JournalTransaction{
		ID: intent.OperationKey, Book: billing.JournalBookFinancial, Currency: intent.Currency,
		SourceKey: intent.OperationKey, AccountID: input.AccountID, TurnID: input.CallID.String(),
		ALegID: intent.ALegID, BLegID: intent.BLegID, OperationKind: intent.OperationKind,
		CorrectionGroupID: intent.CorrectionGroupID, ReversalOf: intent.ReversalOf,
		CorrectsTransactionID: intent.CorrectsTransactionID,
		BalanceBefore:         before.BalanceNano, BalanceAfter: before.BalanceNano,
		SpendableBefore: before.SpendableNano, SpendableAfter: before.SpendableNano,
		CreditFloor: before.CreditFloorNano, CreditLimit: before.CreditLimitNano,
		Mode: string(before.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: before.Version,
		Entries: append([]billing.JournalEntry(nil), intent.Entries...),
	}
	posted, replayed, err := s.postJournalInTx(ctx, tx, journal)
	if err != nil {
		return billing.Posting{}, err
	}
	return billing.Posting{OperationKey: intent.OperationKey, Transaction: posted, Replayed: replayed}, nil
}

func insertSelectedCostAdjustmentInTx(ctx context.Context, tx bun.Tx, storeID string, input billing.SelectedCostAdjustmentInput, plan billing.SelectedCostHeadTransition, linkKey, transactionID string) error {
	if plan.Link == nil || plan.Delta == nil {
		return fmt.Errorf("%w: selected cost adjustment link/delta is missing", billing.ErrSelectedCostAdjustmentInvalid)
	}
	deltaJSON, err := json.Marshal(plan.Delta)
	if err != nil {
		return fmt.Errorf("billingstore: encode selected cost delta: %w", err)
	}
	fxJSON := ""
	if plan.Link.FX != nil {
		payload, err := json.Marshal(plan.Link.FX)
		if err != nil {
			return fmt.Errorf("billingstore: encode selected cost frozen FX: %w", err)
		}
		fxJSON = string(payload)
	}
	previousValuationID, previousRevision, previousInputSetHash := "", int64(0), ""
	if plan.Link.Previous != nil {
		previousValuationID = plan.Link.Previous.ValuationID
		if plan.Link.Previous.Revision > 0 {
			previousRevision = int64(plan.Link.Previous.Revision)
		}
		previousInputSetHash = plan.Link.Previous.InputSetHash
	}
	_, err = tx.NewRaw(`INSERT INTO billing_selected_cost_adjustments(
			store_id, account_id, call_id, head_key, operation_key, link_key, fingerprint, status, comparison, posting,
			previous_valuation_id, previous_revision, previous_input_set_hash,
			current_valuation_id, current_revision, current_input_set_hash,
			currency, fx_json, adjustment_revision, delta_json, journal_transaction_id, created_at_unix
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		storeID, input.AccountID, input.CallID.String(), input.HeadKey, plan.OperationKey, linkKey, plan.Fingerprint,
		string(plan.Status), string(plan.Comparison), string(plan.Posting),
		previousValuationID, previousRevision, previousInputSetHash,
		plan.Link.Current.ValuationID, int64(plan.Link.Current.Revision), plan.Link.Current.InputSetHash,
		plan.Link.Currency, fxJSON, int64(plan.Link.AdjustmentRevision), string(deltaJSON), transactionID,
		time.Now().UTC().UnixNano()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert selected cost adjustment: %w", err)
	}
	return nil
}

// persistSelectedCostHeadInTx compares-and-swaps the expected head version and
// stores the exact successor identity. The head row is the only mutable
// selected-cost state; the immutable adjustment/link and journal rows are the
// audit history.
func (s *DurableStore) persistSelectedCostHeadInTx(ctx context.Context, tx bun.Tx, input billing.SelectedCostAdjustmentInput, row providerCostHeadRow, found bool, plan billing.SelectedCostHeadTransition, transactionID string) error {
	if plan.NextHead == nil || plan.NextHead.Selected == nil {
		return fmt.Errorf("%w: selected cost head transition is missing its successor", billing.ErrSelectedCostAdjustmentInvalid)
	}
	selected := *plan.NextHead.Selected
	amountNano, err := selectedCostAdjustmentExactNanos(selected.Amount)
	if err != nil {
		return err
	}
	if amountNano < 0 {
		return fmt.Errorf("%w: selected cost amount cannot be negative", billing.ErrSelectedCostAdjustmentInvalid)
	}
	subjectJSON, err := json.Marshal(input.Subject)
	if err != nil {
		return fmt.Errorf("billingstore: encode selected cost subject: %w", err)
	}
	postedJSON, err := json.Marshal(selected.Amount)
	if err != nil {
		return fmt.Errorf("billingstore: encode selected cost posted amount: %w", err)
	}
	nativeJSON := ""
	if selected.NativeAmount != nil {
		payload, err := json.Marshal(selected.NativeAmount)
		if err != nil {
			return fmt.Errorf("billingstore: encode selected cost native amount: %w", err)
		}
		nativeJSON = string(payload)
	}
	fxJSON := ""
	if selected.FX != nil {
		payload, err := json.Marshal(selected.FX)
		if err != nil {
			return fmt.Errorf("billingstore: encode selected cost frozen FX: %w", err)
		}
		fxJSON = string(payload)
	}
	postingState := string(billing.SelectedCostPostingApplied)
	now := time.Now().UTC().UnixNano()
	if !found {
		if _, err := tx.NewRaw(`INSERT INTO billing_provider_cost_heads(
				store_id, account_id, call_id, head_key, subject_kind, subject_id, subject_json,
				evidence_revision, input_set_hash, valuation_id, amount_nano, currency, head_version, fence,
				last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix,
				valuation_revision, selection_status, selection_reason, selection_basis, selection_provenance,
				posted_amount_json, native_amount_json, fx_json, posting_state
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			s.storeID, input.AccountID, input.CallID.String(), input.HeadKey, string(input.Subject.Kind), subjectIDForEconomics(input.Subject), string(subjectJSON),
			selected.Ref.Revision, selected.Ref.InputSetHash, selected.Ref.ValuationID, amountNano, selected.Currency, plan.NextHead.Version, 1,
			plan.OperationKey, transactionID, transactionID, now, now,
			selected.Ref.Revision, string(selected.Status), string(selected.Reason), string(selected.Basis), string(selected.Provenance),
			string(postedJSON), nativeJSON, fxJSON, postingState).Exec(ctx); err != nil {
			return fmt.Errorf("billingstore: insert selected cost head: %w", err)
		}
		return nil
	}
	lastTransactionID := transactionID
	if lastTransactionID == "" {
		lastTransactionID = row.LastTransactionID
	}
	result, err := tx.NewRaw(`UPDATE billing_provider_cost_heads SET
			subject_kind = ?, subject_id = ?, subject_json = ?, evidence_revision = ?, input_set_hash = ?, valuation_id = ?,
			amount_nano = ?, currency = ?, head_version = head_version + 1, fence = fence + 1,
			last_operation_key = ?, original_transaction_id = CASE WHEN original_transaction_id <> '' THEN original_transaction_id WHEN last_transaction_id <> '' THEN last_transaction_id ELSE ? END,
			last_transaction_id = ?, updated_at_unix = ?,
			valuation_revision = ?, selection_status = ?, selection_reason = ?, selection_basis = ?, selection_provenance = ?,
			posted_amount_json = ?, native_amount_json = ?, fx_json = ?, posting_state = ?
			WHERE id = ? AND head_version = ? AND fence = ?`,
		string(input.Subject.Kind), subjectIDForEconomics(input.Subject), string(subjectJSON),
		selected.Ref.Revision, selected.Ref.InputSetHash, selected.Ref.ValuationID,
		amountNano, selected.Currency, plan.OperationKey, transactionID, lastTransactionID, now,
		selected.Ref.Revision, string(selected.Status), string(selected.Reason), string(selected.Basis), string(selected.Provenance),
		string(postedJSON), nativeJSON, fxJSON, postingState,
		row.ID, row.HeadVersion, row.Fence).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: advance selected cost head: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("billingstore: selected cost head rows affected: %w", affectedErr)
	} else if affected != 1 {
		return fmt.Errorf("%w: selected cost head %s", billing.ErrProviderCostRevisionFence, input.HeadKey)
	}
	return nil
}

func loadLatestSelectedCostAdjustment(ctx context.Context, q bun.IDB, storeID, accountID string, callID billing.BillingCallID, headKey string) (selectedCostAdjustmentRow, bool, error) {
	var row selectedCostAdjustmentRow
	err := q.NewRaw(selectedCostAdjustmentSelect+` WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ? ORDER BY id DESC LIMIT 1`,
		storeID, accountID, callID.String(), headKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return selectedCostAdjustmentRow{}, false, nil
	}
	if err != nil {
		return selectedCostAdjustmentRow{}, false, fmt.Errorf("billingstore: selected cost adjustment lookup: %w", err)
	}
	return row, true, nil
}

func selectedCostAdjustmentStoredMatches(stored selectedCostAdjustmentRow, ref billing.SelectedCostValuationRef) bool {
	return stored.CurrentValuationID == ref.ValuationID &&
		stored.CurrentInputSetHash == ref.InputSetHash &&
		stored.CurrentRevision == int64(ref.Revision)
}

func selectedCostAdjustmentResultFromPlan(input billing.SelectedCostAdjustmentInput, current billing.SelectedCostHead, plan billing.SelectedCostHeadTransition) billing.SelectedCostAdjustmentResult {
	result := billing.SelectedCostAdjustmentResult{
		AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey,
		Status: plan.Status, Reason: plan.Reason, Comparison: plan.Comparison, Posting: plan.Posting,
		SelectionStatus: plan.SelectionStatus, SelectionReason: plan.SelectionReason,
		Current: input.Selected.Ref, OperationKey: plan.OperationKey, Fingerprint: plan.Fingerprint,
		HeadVersion: current.Version,
	}
	if plan.Previous != nil {
		previous := plan.Previous.Ref
		result.Previous = &previous
	} else if plan.Link != nil && plan.Link.Previous != nil {
		previous := *plan.Link.Previous
		result.Previous = &previous
	}
	if plan.Delta != nil {
		delta := *plan.Delta
		result.Delta = &delta
	}
	if plan.NextHead != nil {
		result.HeadVersion = plan.NextHead.Version
	}
	return result
}

func selectedCostAdjustmentReplayResult(stored selectedCostAdjustmentRow, input billing.SelectedCostAdjustmentInput, current billing.SelectedCostHead) (billing.SelectedCostAdjustmentResult, error) {
	result := billing.SelectedCostAdjustmentResult{
		AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey,
		Status: billing.SelectedCostTransitionReplay, Reason: billing.SelectedCostReasonAlreadyApplied,
		Posting:         billing.SelectedCostPostingReplayed,
		SelectionStatus: input.Selected.Status, SelectionReason: input.Selected.Reason,
		Current: input.Selected.Ref, OperationKey: stored.OperationKey, LinkKey: stored.LinkKey,
		Fingerprint: stored.Fingerprint, HeadVersion: current.Version, TransactionID: stored.JournalTransactionID,
	}
	comparison := billing.SelectedCostComparisonStatus(stored.Comparison)
	if comparison.IsKnown() {
		result.Comparison = comparison
	} else {
		result.Comparison = billing.SelectedCostComparisonNotEvaluated
	}
	if strings.TrimSpace(stored.DeltaJSON) != "" {
		var delta billing.MonetaryExactAmount
		if err := json.Unmarshal([]byte(stored.DeltaJSON), &delta); err != nil {
			return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: retained delta decode", ErrSelectedCostAdjustmentMismatch)
		}
		if err := delta.Validate(); err != nil {
			return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: retained delta: %v", ErrSelectedCostAdjustmentMismatch, err)
		}
		result.Delta = &delta
	}
	if stored.PreviousValuationID != "" || stored.PreviousRevision != 0 || stored.PreviousInputSetHash != "" {
		previous := billing.SelectedCostValuationRef{
			ValuationID: stored.PreviousValuationID, Revision: uint64(stored.PreviousRevision),
			InputSetHash: stored.PreviousInputSetHash,
		}
		if err := previous.Validate(); err != nil {
			return billing.SelectedCostAdjustmentResult{}, fmt.Errorf("%w: retained previous valuation: %v", ErrSelectedCostAdjustmentMismatch, err)
		}
		result.Previous = &previous
	}
	return result, nil
}
