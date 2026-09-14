package billingstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

var _ billing.CostPassThroughSettlementStore = (*DurableStore)(nil)

const costPassThroughHeadVersionInitial uint64 = 1

const CostPassThroughAdjustmentOperationKind = billing.CostPassThroughAdjustmentOperationKind

type costPassThroughHeadRow struct {
	HeadKey                string    `bun:"head_key"`
	AccountID              string    `bun:"account_id"`
	CallID                 string    `bun:"call_id"`
	SettlementOperationKey string    `bun:"settlement_operation_key"`
	ALegID                 string    `bun:"a_leg_id"`
	OriginalTransactionID  string    `bun:"original_transaction_id"`
	PolicyID               string    `bun:"policy_id"`
	PolicyVersion          string    `bun:"policy_version"`
	MissingCost            string    `bun:"missing_cost"`
	SafeBoundNano          int64     `bun:"safe_bound_nano"`
	Currency               string    `bun:"currency"`
	AllowLateAdjustment    bool      `bun:"allow_late_adjustment"`
	Status                 string    `bun:"status"`
	PostedAmountNano       int64     `bun:"posted_amount_nano"`
	ProviderLURKey         string    `bun:"provider_lur_key"`
	ProviderValuationID    string    `bun:"provider_valuation_id"`
	ProviderRevision       int64     `bun:"provider_revision"`
	ProviderInputHash      string    `bun:"provider_input_hash"`
	HeadVersion            uint64    `bun:"head_version"`
	Fence                  uint64    `bun:"fence"`
	CreatedAt              time.Time `bun:"created_at"`
	UpdatedAt              time.Time `bun:"updated_at"`
}

func costPassThroughHeadKey(accountID string, callID billing.BillingCallID) string {
	return "cost-pass-through-head:v1:" + strings.TrimSpace(accountID) + ":" + callID.String()
}

func costPassThroughHeadSelect() string {
	return `SELECT head_key, account_id, call_id, settlement_operation_key, original_transaction_id,
	policy_id, policy_version, missing_cost, safe_bound_nano, currency, allow_late_adjustment,
	status, posted_amount_nano, provider_lur_key, provider_valuation_id, provider_revision,
	provider_input_hash, head_version, fence, created_at, updated_at
	FROM billing_cost_pass_through_heads WHERE account_id = ? AND call_id = ?`
}

func persistCostPassThroughHeadInTx(ctx context.Context, tx bun.Tx, call billing.CallUsageRecord, settlementOperationKey string, state billing.CostPassThroughSettlement, settlementFingerprint string) error {
	if err := state.Validate(state.SafeBound.Currency); err != nil {
		return err
	}
	if state.PolicyRef != call.ChargePolicyRef {
		return fmt.Errorf("%w: policy reference differs from call", billing.ErrCostPassThroughSettlementInvalid)
	}
	providerLURKey, providerValuationID, providerRevision, providerInputHash := "", "", uint64(0), ""
	if state.ProviderCost != nil {
		providerLURKey = strings.TrimSpace(state.ProviderCost.LURKey)
		providerValuationID = strings.TrimSpace(state.ProviderCost.ValuationID)
		providerRevision = state.ProviderCost.Revision
		providerInputHash = strings.ToLower(state.ProviderCost.InputHash)
	}
	if state.OriginalTransactionID == "" && state.PostedAmount.Nano > 0 {
		return fmt.Errorf("%w: posted pass-through settlement needs original transaction", billing.ErrCostPassThroughSettlementInvalid)
	}
	now := time.Now().UTC()
	_, err := tx.ExecContext(ctx, `INSERT INTO billing_cost_pass_through_heads(
	head_key, account_id, call_id, settlement_operation_key, original_transaction_id, a_leg_id,
	policy_id, policy_version, missing_cost, safe_bound_nano, currency, allow_late_adjustment,
	status, posted_amount_nano, provider_lur_key, provider_valuation_id, provider_revision,
	provider_input_hash, settlement_fingerprint, head_version, fence, created_at, updated_at)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		costPassThroughHeadKey(call.AccountID, call.CallID), call.AccountID, call.CallID.String(), settlementOperationKey, state.OriginalTransactionID, call.ALegID,
		state.PolicyRef.ID, state.PolicyRef.Version, string(state.Policy.MissingCost), state.SafeBound.Nano, state.SafeBound.Currency, boolProjection(tx, boolInt(state.Policy.AllowLateAdjustment)),
		string(state.Status), state.PostedAmount.Nano, providerLURKey, providerValuationID, providerRevision,
		providerInputHash, settlementFingerprint, costPassThroughHeadVersionInitial, costPassThroughHeadVersionInitial, now, now)
	if err != nil {
		return fmt.Errorf("billingstore: insert cost pass-through head: %w", err)
	}
	return nil
}

func loadCostPassThroughHead(ctx context.Context, q bun.IDB, accountID string, callID billing.BillingCallID) (costPassThroughHeadRow, error) {
	var row costPassThroughHeadRow
	err := q.NewRaw(costPassThroughHeadSelect(), accountID, callID.String()).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return costPassThroughHeadRow{}, billing.ErrCostPassThroughHeadNotFound
	}
	if err != nil {
		return costPassThroughHeadRow{}, fmt.Errorf("billingstore: read cost pass-through head: %w", err)
	}
	return row, nil
}

func (row costPassThroughHeadRow) settlement() (billing.CostPassThroughSettlement, error) {
	if row.ProviderRevision < 0 {
		return billing.CostPassThroughSettlement{}, fmt.Errorf("%w: negative provider revision", billing.ErrCostPassThroughSettlementInvalid)
	}
	bound := billing.Money{Nano: row.SafeBoundNano, Currency: row.Currency}
	state := billing.CostPassThroughSettlement{
		PolicyRef: billing.VersionRef{ID: row.PolicyID, Version: row.PolicyVersion},
		Policy: billing.CostPassThroughPolicy{
			MissingCost: billing.CostPassThroughMissingCostPolicy(row.MissingCost),
			SafeBound:   &bound, AllowLateAdjustment: row.AllowLateAdjustment,
		},
		Status:                billing.CostPassThroughSettlementStatus(row.Status),
		SafeBound:             bound,
		PostedAmount:          billing.Money{Nano: row.PostedAmountNano, Currency: row.Currency},
		OriginalTransactionID: row.OriginalTransactionID,
	}
	if row.ProviderRevision > 0 {
		provider := billing.CostPassThroughProviderCost{
			LURKey: row.ProviderLURKey, ValuationID: row.ProviderValuationID, Revision: uint64(row.ProviderRevision),
			InputHash: row.ProviderInputHash, Amount: state.PostedAmount, AmountPresent: true, Reconciled: true, Authoritative: true,
		}
		state.ProviderCost = &provider
	}
	if err := state.Validate(row.Currency); err != nil {
		return billing.CostPassThroughSettlement{}, err
	}
	return state, nil
}

func costPassThroughAdjustmentFingerprint(accountID string, callID billing.BillingCallID, before billing.CostPassThroughSettlement, provider billing.CostPassThroughProviderCost) (string, error) {
	beforeFingerprint, err := before.SemanticFingerprint()
	if err != nil {
		return "", err
	}
	providerFingerprint, err := provider.SemanticFingerprint()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Version             string
		AccountID           string
		CallID              billing.BillingCallID
		BeforeFingerprint   string
		ProviderFingerprint string
	}{
		Version: "cost-pass-through-adjustment:v1", AccountID: accountID, CallID: callID,
		BeforeFingerprint: beforeFingerprint, ProviderFingerprint: providerFingerprint,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "cost-pass-through-adjustment:v1:" + hex.EncodeToString(digest[:]), nil
}

// ApplyCostPassThroughRevision atomically advances one explicit customer
// pass-through head and posts only the signed difference from its current
// posted amount. Validation of the provider payload occurs before the account
// transaction, so supplier waits never hold customer admission locks.
func (s *DurableStore) ApplyCostPassThroughRevision(ctx context.Context, input billing.CostPassThroughRevisionInput) (billing.CostPassThroughRevisionResult, error) {
	if s == nil || s.db == nil {
		return billing.CostPassThroughRevisionResult{}, fmt.Errorf("billingstore: nil store")
	}
	if ctx == nil {
		return billing.CostPassThroughRevisionResult{}, fmt.Errorf("%w: nil context", billing.ErrCostPassThroughSettlementInvalid)
	}
	if err := ctx.Err(); err != nil {
		return billing.CostPassThroughRevisionResult{}, fmt.Errorf("%w: %w", billing.ErrCostPassThroughSettlementInvalid, err)
	}
	if err := input.Validate(); err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.ProviderCost.LURKey = strings.TrimSpace(input.ProviderCost.LURKey)
	input.ProviderCost.ValuationID = strings.TrimSpace(input.ProviderCost.ValuationID)
	return withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (billing.CostPassThroughRevisionResult, error) {
		return s.applyCostPassThroughRevisionAttempt(ctx, input)
	})
}

// ApplyCostPassThroughAdjustment is a descriptive compatibility spelling for
// callers that treat each accepted revision as an adjustment operation.
func (s *DurableStore) ApplyCostPassThroughAdjustment(ctx context.Context, input billing.CostPassThroughRevisionInput) (billing.CostPassThroughRevisionResult, error) {
	return s.ApplyCostPassThroughRevision(ctx, input)
}

func (s *DurableStore) applyCostPassThroughRevisionAttempt(ctx context.Context, input billing.CostPassThroughRevisionInput) (billing.CostPassThroughRevisionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, fmt.Errorf("billingstore: begin cost pass-through adjustment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), input.AccountID); err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	row, err := loadCostPassThroughHead(ctx, tx, input.AccountID, input.CallID)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	state, err := row.settlement()
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	if err := input.ProviderCost.Validate(row.Currency); err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	if input.ProviderCost.Amount.Nano > state.SafeBound.Nano {
		return billing.CostPassThroughRevisionResult{}, fmt.Errorf("%w: %d is greater than %d", billing.ErrCostPassThroughBoundExceeded, input.ProviderCost.Amount.Nano, state.SafeBound.Nano)
	}
	previous := state.PostedAmount
	zeroDelta := billing.Money{Nano: 0, Currency: row.Currency}
	baseResult := billing.CostPassThroughRevisionResult{
		CallID: input.CallID, Status: state.Status, PreviousAmount: previous, CurrentAmount: previous,
		Delta: zeroDelta, ProviderCost: input.ProviderCost,
	}
	if !state.Policy.AllowLateAdjustment {
		if err := tx.Commit(); err != nil {
			return billing.CostPassThroughRevisionResult{}, err
		}
		baseResult.Ignored = true
		return baseResult, nil
	}
	if state.ProviderCost != nil && input.ProviderCost.LURKey != state.ProviderCost.LURKey {
		return billing.CostPassThroughRevisionResult{}, billing.ErrCostPassThroughRevisionConflict
	}
	currentRevision := uint64(0)
	if row.ProviderRevision > 0 {
		currentRevision = uint64(row.ProviderRevision)
	}
	if currentRevision > 0 && input.ProviderCost.Revision == currentRevision {
		if row.ProviderLURKey != input.ProviderCost.LURKey || row.ProviderValuationID != input.ProviderCost.ValuationID || row.ProviderInputHash != strings.ToLower(input.ProviderCost.InputHash) || previous != input.ProviderCost.Amount {
			return billing.CostPassThroughRevisionResult{}, billing.ErrCostPassThroughRevisionConflict
		}
		sourceKey, sourceErr := billing.CostPassThroughAdjustmentSourceKey(input.AccountID, input.CallID, input.ProviderCost)
		if sourceErr != nil {
			return billing.CostPassThroughRevisionResult{}, sourceErr
		}
		if _, _, lookupErr := loadOperationSnapshot(ctx, tx, input.AccountID, billing.CostPassThroughAdjustmentOperationKind, sourceKey); lookupErr != nil {
			return billing.CostPassThroughRevisionResult{}, lookupErr
		}
		if err := tx.Commit(); err != nil {
			return billing.CostPassThroughRevisionResult{}, err
		}
		baseResult.Replayed = true
		return baseResult, nil
	}
	if currentRevision > 0 && input.ProviderCost.Revision < currentRevision {
		if err := tx.Commit(); err != nil {
			return billing.CostPassThroughRevisionResult{}, err
		}
		baseResult.Stale = true
		return baseResult, nil
	}
	delta, err := input.ProviderCost.Amount.Sub(previous)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	sourceKey, err := billing.CostPassThroughAdjustmentSourceKey(input.AccountID, input.CallID, input.ProviderCost)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	fingerprint, err := costPassThroughAdjustmentFingerprint(input.AccountID, input.CallID, state, input.ProviderCost)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	if existing, found, lookupErr := loadOperationSnapshot(ctx, tx, input.AccountID, billing.CostPassThroughAdjustmentOperationKind, sourceKey); lookupErr != nil {
		return billing.CostPassThroughRevisionResult{}, lookupErr
	} else if found {
		if existing.Fingerprint != fingerprint {
			return billing.CostPassThroughRevisionResult{}, ErrOperationConflict
		}
		if err := tx.Commit(); err != nil {
			return billing.CostPassThroughRevisionResult{}, err
		}
		baseResult.Replayed = true
		return baseResult, nil
	}
	account, err := getAccountTx(ctx, tx, input.AccountID)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	if account.State != billing.AccountReady {
		return billing.CostPassThroughRevisionResult{}, billing.ErrAccountNotReady
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	after := account
	if delta.Nano != 0 {
		after, err = account.ApplyBalanceDelta(billing.Money{Nano: -delta.Nano, Currency: account.Currency})
		if err != nil {
			if errors.Is(err, billing.ErrInsufficientSpendable) {
				if markErr := setReconcileRequiredTx(ctx, tx, input.AccountID); markErr != nil {
					return billing.CostPassThroughRevisionResult{}, markErr
				}
				if commitErr := tx.Commit(); commitErr != nil {
					return billing.CostPassThroughRevisionResult{}, commitErr
				}
				return billing.CostPassThroughRevisionResult{}, fmt.Errorf("%w: %w", billing.ErrSettlementReconcileRequired, err)
			}
			return billing.CostPassThroughRevisionResult{}, err
		}
		if account.Version == math.MaxUint64 {
			return billing.CostPassThroughRevisionResult{}, billing.ErrCostPassThroughSettlementInvalid
		}
		after.Version = account.Version + 1
	}
	afterSnapshot, err := snapshotForAccount(after)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	var posting billing.Posting
	var accountSequence uint64
	if delta.Nano != 0 {
		amount := delta
		debit, credit := "customer_financial_account", "customer_adjustment_clearing"
		if delta.Nano < 0 {
			amount.Nano = -amount.Nano
			debit, credit = credit, debit
		}
		correctionGroup := row.OriginalTransactionID
		if correctionGroup == "" {
			correctionGroup = row.SettlementOperationKey
		}
		journal := billing.JournalTransaction{
			ID: sourceKey, Book: billing.JournalBookFinancial, Currency: account.Currency, SourceKey: sourceKey,
			AccountID: input.AccountID, TurnID: input.CallID.String(), ALegID: row.ALegID,
			CorrectionGroupID: correctionGroup,
			OperationKind:     billing.CostPassThroughAdjustmentOperationKind,
			BalanceBefore:     before.BalanceNano, BalanceAfter: afterSnapshot.BalanceNano,
			SpendableBefore: before.SpendableNano, SpendableAfter: afterSnapshot.SpendableNano,
			CreditFloor: afterSnapshot.CreditFloorNano, CreditLimit: afterSnapshot.CreditLimitNano,
			Mode: string(afterSnapshot.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: afterSnapshot.Version,
			Entries: []billing.JournalEntry{{LedgerAccount: debit, Side: billing.JournalDebit, Amount: amount}, {LedgerAccount: credit, Side: billing.JournalCredit, Amount: amount}},
		}
		posted, replayed, postErr := s.postJournalInTx(ctx, tx, journal)
		if postErr != nil {
			return billing.CostPassThroughRevisionResult{}, postErr
		}
		if replayed {
			return billing.CostPassThroughRevisionResult{}, ErrOperationConflict
		}
		posting = billing.Posting{OperationKey: sourceKey, Transaction: posted, Before: before, After: afterSnapshot}
		accountSequence = posted.AccountSequence
	} else {
		posting = billing.Posting{OperationKey: sourceKey, Before: before, After: afterSnapshot}
	}
	if delta.Nano != 0 {
		accountResult, updateErr := tx.NewRaw(`UPDATE billing_accounts SET balance_nano = ?, version = ?, updated_at = ? WHERE account_id = ? AND version = ?`, afterSnapshot.BalanceNano, afterSnapshot.Version, time.Now().UTC(), input.AccountID, before.Version).Exec(ctx)
		if updateErr != nil {
			return billing.CostPassThroughRevisionResult{}, updateErr
		}
		if count, affectedErr := accountResult.RowsAffected(); affectedErr != nil || count != 1 {
			if affectedErr != nil {
				return billing.CostPassThroughRevisionResult{}, affectedErr
			}
			return billing.CostPassThroughRevisionResult{}, billing.ErrSettlementConflict
		}
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: sourceKey + ":snapshot", AccountID: input.AccountID, OperationKind: billing.CostPassThroughAdjustmentOperationKind, SourceKey: sourceKey, Fingerprint: fingerprint, Before: before, After: afterSnapshot, SequenceStart: accountSequence, SequenceEnd: accountSequence}); err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	now := time.Now().UTC()
	provider := input.ProviderCost
	updateResult, err := tx.NewRaw(`UPDATE billing_cost_pass_through_heads SET status = ?, posted_amount_nano = ?, provider_lur_key = ?, provider_valuation_id = ?, provider_revision = ?, provider_input_hash = ?, head_version = head_version + 1, fence = fence + 1, updated_at = ? WHERE account_id = ? AND call_id = ? AND head_version = ? AND fence = ?`,
		string(billing.CostPassThroughSettlementFinal), provider.Amount.Nano, provider.LURKey, provider.ValuationID, provider.Revision, strings.ToLower(provider.InputHash), now, input.AccountID, input.CallID.String(), row.HeadVersion, row.Fence).Exec(ctx)
	if err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	if count, affectedErr := updateResult.RowsAffected(); affectedErr != nil || count != 1 {
		if affectedErr != nil {
			return billing.CostPassThroughRevisionResult{}, affectedErr
		}
		return billing.CostPassThroughRevisionResult{}, billing.ErrCostPassThroughRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return billing.CostPassThroughRevisionResult{}, err
	}
	baseResult.Status = billing.CostPassThroughSettlementFinal
	baseResult.CurrentAmount = provider.Amount
	baseResult.Delta = delta
	baseResult.Posting = posting
	baseResult.Applied = true
	return baseResult, nil
}
