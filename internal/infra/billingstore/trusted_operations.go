package billingstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

var ErrOperationConflict = errors.New("billingstore: trusted operation replay conflict")

func (s *DurableStore) PostFunding(ctx context.Context, input billing.FundingInput) (billing.Posting, error) {
	if err := input.Validate(); err != nil {
		return billing.Posting{}, err
	}
	fp, err := input.Fingerprint()
	if err != nil {
		return billing.Posting{}, err
	}
	posting, err := s.postFinancialCommand(ctx, "funding", input.AccountID, input.SourceKey, fp, input.Amount, 1, func() (billing.JournalTransaction, error) {
		return billing.FundingJournalIntent(input)
	})
	return posting, wrapAccountProvisionerError(err)
}

func (s *DurableStore) PostPayment(ctx context.Context, input billing.PaymentInput) (billing.Posting, error) {
	if err := input.Validate(); err != nil {
		return billing.Posting{}, err
	}
	fp, err := input.Fingerprint()
	if err != nil {
		return billing.Posting{}, err
	}
	return s.postFinancialCommand(ctx, "payment", input.AccountID, input.SourceKey, fp, input.Amount, 1, func() (billing.JournalTransaction, error) {
		return billing.PaymentJournalIntent(input)
	})
}

func (s *DurableStore) PostAdjustment(ctx context.Context, input billing.AdjustmentInput) (billing.Posting, error) {
	if err := input.Validate(); err != nil {
		return billing.Posting{}, err
	}
	fp, err := input.Fingerprint()
	if err != nil {
		return billing.Posting{}, err
	}
	delta := int64(1)
	if input.Direction == billing.AdjustmentDebit {
		delta = -1
	}
	return s.postFinancialCommand(ctx, "adjustment", input.AccountID, input.SourceKey, fp, input.Amount, delta, func() (billing.JournalTransaction, error) {
		return billing.AdjustmentJournalIntent(input)
	})
}

func (s *DurableStore) postFinancialCommand(ctx context.Context, kind, accountID, sourceKey, fingerprint string, amount billing.Money, balanceDelta int64, intent func() (billing.JournalTransaction, error)) (billing.Posting, error) {
	if s == nil || s.db == nil {
		return billing.Posting{}, fmt.Errorf("billingstore: nil store")
	}
	operationKey := billing.ScopedOperationKey(kind, strings.TrimSpace(accountID), strings.TrimSpace(sourceKey))
	return withAccountTx(ctx, accountTxRetry{
		Attempts:  30,
		Delay:     3 * time.Millisecond,
		Exhausted: fmt.Errorf("%w: retry budget exhausted", billing.ErrBillingStoreUnavailable),
	}, func() (billing.Posting, error) {
		return s.postFinancialAttempt(ctx, kind, operationKey, strings.TrimSpace(accountID), strings.TrimSpace(sourceKey), fingerprint, amount, balanceDelta, intent)
	})
}

func (s *DurableStore) postFinancialAttempt(ctx context.Context, kind, operationKey, accountID, sourceKey, fingerprint string, amount billing.Money, balanceDelta int64, intent func() (billing.JournalTransaction, error)) (billing.Posting, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.Posting{}, fmt.Errorf("billingstore: begin %s: %w", kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), accountID); err != nil {
		return billing.Posting{}, err
	}
	if existing, found, err := loadOperationSnapshot(ctx, tx, accountID, kind, sourceKey); err != nil {
		return billing.Posting{}, err
	} else if found {
		if existing.Fingerprint != fingerprint {
			return billing.Posting{}, ErrOperationConflict
		}
		journal, err := requireJournalBySource(ctx, tx, accountID, billing.JournalBookFinancial, billing.ScopedOperationKey(kind, accountID, sourceKey))
		if err != nil {
			return billing.Posting{}, err
		}
		return postingFromSnapshot(existing, journal, true), nil
	}
	account, err := getAccountTx(ctx, tx, accountID)
	if err != nil {
		return billing.Posting{}, err
	}
	if account.State != billing.AccountReady {
		return billing.Posting{}, billing.ErrAccountNotReady
	}
	if amount.Currency != account.Currency {
		return billing.Posting{}, billing.ErrMoneyCurrencyMismatch
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.Posting{}, err
	}
	balanceAmount := amount.Nano
	if balanceDelta < 0 {
		balanceAmount = -balanceAmount
	}
	after, err := account.ApplyBalanceDelta(billing.Money{Nano: balanceAmount, Currency: amount.Currency})
	if err != nil {
		return billing.Posting{}, err
	}
	if account.Version >= math.MaxInt64 {
		return billing.Posting{}, fmt.Errorf("%w: account version overflow", billing.ErrTrustedCommandInvalid)
	}
	after.Version = account.Version + 1
	afterSnapshot, err := snapshotForAccount(after)
	if err != nil {
		return billing.Posting{}, err
	}
	journal, err := intent()
	if err != nil {
		return billing.Posting{}, err
	}
	journal.OperationKind = kind
	journal.TurnID = operationKey
	journal.BalanceBefore, journal.BalanceAfter = before.BalanceNano, afterSnapshot.BalanceNano
	journal.SpendableBefore, journal.SpendableAfter = before.SpendableNano, afterSnapshot.SpendableNano
	journal.CreditFloor, journal.CreditLimit, journal.Mode = before.CreditFloorNano, before.CreditLimitNano, string(before.Mode)
	journal.SnapshotVersionBefore, journal.SnapshotVersionAfter = before.Version, afterSnapshot.Version
	posted, existing, err := s.postJournalInTx(ctx, tx, journal)
	if err != nil {
		return billing.Posting{}, err
	}
	if existing {
		return billing.Posting{}, ErrOperationConflict
	}
	accountResult, err := tx.NewRaw(`UPDATE billing_accounts SET balance_nano = ?, version = ?, updated_at = ? WHERE account_id = ? AND version = ?`, afterSnapshot.BalanceNano, afterSnapshot.Version, time.Now().UTC(), accountID, before.Version).Exec(ctx)
	if err != nil {
		return billing.Posting{}, fmt.Errorf("billingstore: update %s account: %w", kind, err)
	}
	if err := requireRowsAffected(accountResult, 1, kind+" account version"); err != nil {
		return billing.Posting{}, fmt.Errorf("%w: %v", billing.ErrBillingStoreUnavailable, err)
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: operationKey, AccountID: accountID, OperationKind: kind, SourceKey: sourceKey, Fingerprint: fingerprint, Before: before, After: afterSnapshot, SequenceStart: posted.AccountSequence, SequenceEnd: posted.AccountSequence}); err != nil {
		return billing.Posting{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.Posting{}, fmt.Errorf("billingstore: commit %s: %w", kind, err)
	}
	return billing.Posting{OperationKey: operationKey, Transaction: posted, Before: before, After: afterSnapshot}, nil
}

func (s *DurableStore) ChangeCreditPolicy(ctx context.Context, input billing.CreditPolicyInput) (billing.PolicyChange, error) {
	if err := input.Validate(); err != nil {
		return billing.PolicyChange{}, err
	}
	fp, err := input.Fingerprint()
	if err != nil {
		return billing.PolicyChange{}, err
	}
	change, err := withAccountTx(ctx, accountTxRetry{
		Attempts:  30,
		Delay:     3 * time.Millisecond,
		Exhausted: fmt.Errorf("%w: credit policy retry budget exhausted", billing.ErrBillingStoreUnavailable),
	}, func() (billing.PolicyChange, error) {
		return s.changeCreditPolicyAttempt(ctx, input, fp)
	})
	return change, wrapAccountProvisionerError(err)
}

func (s *DurableStore) changeCreditPolicyAttempt(ctx context.Context, input billing.CreditPolicyInput, fp string) (billing.PolicyChange, error) {
	kind, source := "credit_policy", strings.TrimSpace(input.SourceKey)
	operationKey := billing.ScopedOperationKey(kind, input.AccountID, source)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.PolicyChange{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), input.AccountID); err != nil {
		return billing.PolicyChange{}, err
	}
	if existing, found, err := loadOperationSnapshot(ctx, tx, input.AccountID, kind, source); err != nil {
		return billing.PolicyChange{}, err
	} else if found {
		if existing.Fingerprint != fp {
			return billing.PolicyChange{}, ErrOperationConflict
		}
		return policyChangeFromSnapshot(existing, true), nil
	}
	account, err := getAccountTx(ctx, tx, input.AccountID)
	if err != nil {
		return billing.PolicyChange{}, err
	}
	if account.State != billing.AccountReady {
		return billing.PolicyChange{}, billing.ErrAccountNotReady
	}
	if account.Currency != input.Currency {
		return billing.PolicyChange{}, billing.ErrMoneyCurrencyMismatch
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.PolicyChange{}, err
	}
	candidate := account
	candidate.Mode, candidate.CreditLimit = input.Mode, input.CreditLimit
	if candidate.BalanceNano < candidate.CreditFloorNano() {
		return billing.PolicyChange{}, billing.ErrUnsafeCreditLimitReduction
	}
	spendable, err := candidate.SpendableNano()
	if err != nil || spendable < 0 {
		return billing.PolicyChange{}, billing.ErrUnsafeCreditLimitReduction
	}
	if account.Version >= math.MaxInt64 {
		return billing.PolicyChange{}, fmt.Errorf("%w: account version overflow", billing.ErrTrustedCommandInvalid)
	}
	candidate.Version = account.Version + 1
	after, err := snapshotForAccount(candidate)
	if err != nil {
		return billing.PolicyChange{}, err
	}
	effective := input.EffectiveAt
	if effective.IsZero() {
		effective = time.Now().UTC()
	}
	payload, _ := json.Marshal(input)
	if _, err := tx.NewRaw(`INSERT INTO billing_account_policy_events(account_id, event_key, mode, currency, credit_limit_nano, effective_at, source_key, fingerprint, payload_json, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, input.AccountID, operationKey, string(input.Mode), input.Currency, input.CreditLimit, effective, source, fp, string(payload), time.Now().UTC()).Exec(ctx); err != nil {
		return billing.PolicyChange{}, fmt.Errorf("billingstore: insert policy event: %w", err)
	}
	policyResult, err := tx.NewRaw(`UPDATE billing_accounts SET mode = ?, currency = ?, credit_limit_nano = ?, version = ?, updated_at = ? WHERE account_id = ? AND version = ?`, string(candidate.Mode), candidate.Currency, candidate.CreditLimit, candidate.Version, time.Now().UTC(), input.AccountID, account.Version).Exec(ctx)
	if err != nil {
		return billing.PolicyChange{}, fmt.Errorf("billingstore: update policy: %w", err)
	}
	if err := requireRowsAffected(policyResult, 1, "policy account version"); err != nil {
		return billing.PolicyChange{}, fmt.Errorf("%w: %v", billing.ErrBillingStoreUnavailable, err)
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: operationKey, AccountID: input.AccountID, OperationKind: kind, SourceKey: source, Fingerprint: fp, Before: before, After: after}); err != nil {
		return billing.PolicyChange{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.PolicyChange{}, fmt.Errorf("billingstore: commit policy: %w", err)
	}
	return billing.PolicyChange{OperationKey: operationKey, Before: before, After: after}, nil
}

type operationSnapshotInput struct {
	OperationKey, AccountID, OperationKind, SourceKey, Fingerprint string
	Before, After                                                  billing.AccountSnapshot
	SequenceStart, SequenceEnd                                     uint64
}

func snapshotForAccount(a billing.Account) (billing.AccountSnapshot, error) {
	spendable, err := a.SpendableNano()
	if err != nil {
		return billing.AccountSnapshot{}, err
	}
	return billing.AccountSnapshot{BalanceNano: a.BalanceNano, SpendableNano: spendable, CreditFloorNano: a.CreditFloorNano(), CreditLimitNano: a.CreditLimit, Mode: a.Mode, Currency: a.Currency, Version: a.Version}, nil
}

func insertOperationSnapshot(ctx context.Context, tx bun.Tx, in operationSnapshotInput) error {
	integrity := snapshotIntegrity(in.OperationKey, in.AccountID, in.OperationKind, in.SourceKey, in.Fingerprint, in.Before, in.After, in.SequenceStart, in.SequenceEnd)
	// Historical snapshot columns are retained for audit compatibility. The
	// current operation snapshot contract has no reserved field and always
	// writes zero into those legacy columns.
	_, err := tx.NewRaw(`INSERT INTO billing_operation_snapshots(operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.OperationKey, in.AccountID, in.OperationKind, in.SourceKey, in.Fingerprint, integrity, in.After.Currency, string(in.After.Mode), in.Before.BalanceNano, in.After.BalanceNano, 0, 0, in.Before.SpendableNano, in.After.SpendableNano, in.After.CreditFloorNano, in.After.CreditLimitNano, in.Before.Version, in.After.Version, in.SequenceStart, in.SequenceEnd, time.Now().UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert operation snapshot: %w", err)
	}
	return nil
}

func loadOperationSnapshot(ctx context.Context, q bun.IDB, accountID, kind, source string) (operationSnapshotRow, bool, error) {
	var row operationSnapshotRow
	err := q.NewRaw(`SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = ? AND source_key = ?`, accountID, kind, source).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return operationSnapshotRow{}, false, nil
	}
	if err != nil {
		return operationSnapshotRow{}, false, err
	}
	return row, true, nil
}

func snapshotIntegrity(operationKey, accountID, operationKind, sourceKey, fingerprint string, before, after billing.AccountSnapshot, sequenceStart, sequenceEnd uint64) string {
	payload, _ := json.Marshal(struct {
		Version                                                        string
		OperationKey, AccountID, OperationKind, SourceKey, Fingerprint string
		Before, After                                                  billing.AccountSnapshot
		SequenceStart, SequenceEnd                                     uint64
	}{"snapshot:v1", operationKey, accountID, operationKind, sourceKey, fingerprint, before, after, sequenceStart, sequenceEnd})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("snapshot:v1:%x", digest[:])
}

func postingFromSnapshot(row operationSnapshotRow, journal billing.JournalTransaction, replayed bool) billing.Posting {
	return billing.Posting{OperationKey: row.OperationKey, Transaction: journal, Replayed: replayed, Before: snapshotFromRow(row, true), After: snapshotFromRow(row, false)}
}

func policyChangeFromSnapshot(row operationSnapshotRow, replayed bool) billing.PolicyChange {
	return billing.PolicyChange{OperationKey: row.OperationKey, Replayed: replayed, Before: snapshotFromRow(row, true), After: snapshotFromRow(row, false)}
}

func snapshotFromRow(row operationSnapshotRow, before bool) billing.AccountSnapshot {
	if before {
		return billing.AccountSnapshot{BalanceNano: row.BalanceBefore, SpendableNano: row.SpendableBefore, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionBefore}
	}
	return billing.AccountSnapshot{BalanceNano: row.BalanceAfter, SpendableNano: row.SpendableAfter, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionAfter}
}
