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
	return s.postFinancialCommand(ctx, "funding", input.AccountID, input.SourceKey, fp, input.Amount, 1, func() (billing.JournalTransaction, error) {
		return billing.FundingJournalIntent(input)
	})
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
	for attempt := range 30 {
		out, err := s.postFinancialAttempt(ctx, kind, operationKey, strings.TrimSpace(accountID), strings.TrimSpace(sourceKey), fingerprint, amount, balanceDelta, intent)
		if err == nil {
			return out, nil
		}
		if !isSQLiteBusy(err) && !isUniqueViolation(err) {
			return billing.Posting{}, err
		}
		if waitErr := waitContention(ctx, time.Duration(attempt+1)*3*time.Millisecond); waitErr != nil {
			return billing.Posting{}, waitErr
		}
	}
	return billing.Posting{}, fmt.Errorf("%w: retry budget exhausted", billing.ErrAuthorizationUnavailable)
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
	// Debits must not leave spendable negative while authorization holds remain.
	// Floor-only checks ignore reserved exposure and can strand later settlement.
	if spendable, spendErr := after.SpendableNano(); spendErr != nil {
		return billing.Posting{}, spendErr
	} else if spendable < 0 {
		return billing.Posting{}, billing.ErrInsufficientSpendable
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
	journal.ReservedBefore, journal.ReservedAfter = before.ReservedNano, afterSnapshot.ReservedNano
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
		return billing.Posting{}, fmt.Errorf("%w: %v", billing.ErrAuthorizationUnavailable, err)
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
	for attempt := range 30 {
		out, callErr := s.changeCreditPolicyAttempt(ctx, input, fp)
		if callErr == nil {
			return out, nil
		}
		if !isSQLiteBusy(callErr) && !isUniqueViolation(callErr) {
			return billing.PolicyChange{}, callErr
		}
		if waitErr := waitContention(ctx, time.Duration(attempt+1)*3*time.Millisecond); waitErr != nil {
			return billing.PolicyChange{}, waitErr
		}
	}
	return billing.PolicyChange{}, fmt.Errorf("%w: credit policy retry budget exhausted", billing.ErrAuthorizationUnavailable)
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
		return billing.PolicyChange{}, fmt.Errorf("%w: %v", billing.ErrAuthorizationUnavailable, err)
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: operationKey, AccountID: input.AccountID, OperationKind: kind, SourceKey: source, Fingerprint: fp, Before: before, After: after}); err != nil {
		return billing.PolicyChange{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.PolicyChange{}, fmt.Errorf("billingstore: commit policy: %w", err)
	}
	return billing.PolicyChange{OperationKey: operationKey, Before: before, After: after}, nil
}

func (s *DurableStore) ReleaseAuthorization(ctx context.Context, input billing.ReleaseAuthorizationInput) (billing.Posting, error) {
	if err := input.Validate(); err != nil {
		return billing.Posting{}, err
	}
	if s == nil || s.db == nil {
		return billing.Posting{}, fmt.Errorf("billingstore: nil store")
	}
	for attempt := range 30 {
		out, callErr := s.releaseAuthorizationAttempt(ctx, input)
		if callErr == nil {
			return out, nil
		}
		if !isSQLiteBusy(callErr) && !isUniqueViolation(callErr) {
			return billing.Posting{}, callErr
		}
		if waitErr := waitContention(ctx, time.Duration(attempt+1)*3*time.Millisecond); waitErr != nil {
			return billing.Posting{}, waitErr
		}
	}
	return billing.Posting{}, fmt.Errorf("%w: authorization release retry budget exhausted", billing.ErrAuthorizationUnavailable)
}

func (s *DurableStore) releaseAuthorizationAttempt(ctx context.Context, input billing.ReleaseAuthorizationInput) (billing.Posting, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.Posting{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), input.AccountID); err != nil {
		return billing.Posting{}, err
	}
	var hold authorizationHoldRow
	err = tx.NewRaw(`SELECT hold_key, authorization_id, account_id, tur_key, currency, amount_nano, status, source_key, fingerprint, pricing_ref, charge_policy_ref, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, closed_reason, released_amount_nano, closed_source_key, closed_fingerprint, closed_amount_nano, expires_at, created_at, closed_at FROM authorization_holds WHERE account_id = ? AND authorization_id = ? AND tur_key = ?`, input.AccountID, input.AuthorizationID, input.TURKey).Scan(ctx, &hold)
	if errors.Is(err, sql.ErrNoRows) {
		return billing.Posting{}, ErrAuthorizationHoldNotFound
	}
	if err != nil {
		return billing.Posting{}, err
	}
	remaining := hold.AmountNano - hold.ReleasedAmount
	if remaining < 0 {
		return billing.Posting{}, fmt.Errorf("%w: hold released amount invalid", billing.ErrAuthorizationInvalid)
	}
	amount := input.Amount.Nano
	if hold.Status == "closed" && hold.ClosedAmount > 0 {
		amount = hold.ClosedAmount
	} else if input.FullClose || (amount == 0 && input.Reason == billing.ReleaseZeroCharge) {
		amount = remaining
	}
	if amount < 0 || (hold.Status != "closed" && amount > remaining) {
		return billing.Posting{}, fmt.Errorf("%w: release exceeds open exposure", billing.ErrAuthorizationInvalid)
	}
	if input.Amount.Nano > 0 && input.Amount.Currency != hold.Currency {
		return billing.Posting{}, billing.ErrMoneyCurrencyMismatch
	}
	if input.Reason == billing.ReleaseStaleSafe {
		if err := (billing.SafeReleaseEligibility{AlegInactiveAt: input.AlegInactiveAt, AuthorizationCreated: hold.CreatedAt, Now: input.Now, MaximumExecutionLife: input.MaximumExecutionLife, SafetyGrace: input.SafetyGrace}).Validate(); err != nil {
			return billing.Posting{}, err
		}
	}
	source := strings.TrimSpace(input.SourceKey)
	if source == "" {
		source = input.AuthorizationID + ":" + string(input.Reason)
	}
	closedSource := billing.ScopedOperationKey("authorization-release", input.AccountID, source)
	fpInput := input
	fpInput.SourceKey = source
	fp, err := fpInput.Fingerprint(amount)
	if err != nil {
		return billing.Posting{}, err
	}
	if existing, found, err := loadOperationSnapshot(ctx, tx, input.AccountID, "authorization_release", source); err != nil {
		return billing.Posting{}, err
	} else if found {
		if existing.Fingerprint != fp {
			return billing.Posting{}, ErrOperationConflict
		}
		if existing.SequenceStart == 0 && existing.SequenceEnd == 0 {
			// Zero-amount releases persist a snapshot without a journal row.
			return postingFromSnapshot(existing, billing.JournalTransaction{}, true), nil
		}
		journal, err := requireJournalBySource(ctx, tx, input.AccountID, billing.JournalBookAuthorization, closedSource)
		if err != nil {
			return billing.Posting{}, err
		}
		return postingFromSnapshot(existing, journal, true), nil
	}
	if hold.Status == "closed" || remaining == 0 {
		if hold.ClosedSourceKey != closedSource || hold.ClosedFingerprint != fp {
			return billing.Posting{}, ErrOperationConflict
		}
		if hold.ClosedAmount == 0 {
			return billing.Posting{OperationKey: closedSource, Replayed: true}, nil
		}
		journal, err := requireJournalBySource(ctx, tx, input.AccountID, billing.JournalBookAuthorization, closedSource)
		if err != nil {
			return billing.Posting{}, err
		}
		return billing.Posting{OperationKey: closedSource, Transaction: journal, Replayed: true}, nil
	}
	var processingStatus string
	procErr := tx.NewRaw(`SELECT status FROM usage_record_processing WHERE tur_key = ?`, hold.TURKey).Scan(ctx, &processingStatus)
	if procErr == nil {
		if billing.ProcessingStatus(processingStatus).BlocksHoldRelease() {
			return billing.Posting{}, billing.ErrHoldReleaseBlocked
		}
	} else if !errors.Is(procErr, sql.ErrNoRows) {
		return billing.Posting{}, procErr
	}
	account, err := getAccountTx(ctx, tx, input.AccountID)
	if err != nil {
		return billing.Posting{}, err
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.Posting{}, err
	}
	after, err := account.Release(billing.Money{Nano: amount, Currency: hold.Currency})
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
	var posted billing.JournalTransaction
	var existing bool
	if amount > 0 {
		posted = billing.JournalTransaction{ID: closedSource, Book: billing.JournalBookAuthorization, Currency: hold.Currency, SourceKey: closedSource, AccountID: input.AccountID, TurnID: input.TURKey, ALegID: input.TURKey, OperationKind: "authorization_release", BalanceBefore: before.BalanceNano, BalanceAfter: afterSnapshot.BalanceNano, ReservedBefore: before.ReservedNano, ReservedAfter: afterSnapshot.ReservedNano, SpendableBefore: before.SpendableNano, SpendableAfter: afterSnapshot.SpendableNano, CreditFloor: before.CreditFloorNano, CreditLimit: before.CreditLimitNano, Mode: string(before.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: afterSnapshot.Version, Entries: []billing.JournalEntry{{LedgerAccount: "authorization_contra", Side: billing.JournalDebit, Amount: billing.Money{Nano: amount, Currency: hold.Currency}}, {LedgerAccount: "customer_reserved_exposure", Side: billing.JournalCredit, Amount: billing.Money{Nano: amount, Currency: hold.Currency}}}}
		posted, existing, err = s.postJournalInTx(ctx, tx, posted)
		if err != nil {
			return billing.Posting{}, err
		}
		if existing {
			return billing.Posting{}, ErrOperationConflict
		}
	}
	newReleased := hold.ReleasedAmount + amount
	if amount == remaining {
		closedAt := time.Now().UTC().Format(time.RFC3339Nano)
		holdResult, err := tx.NewRaw(`UPDATE authorization_holds SET status = 'closed', released_amount_nano = ?, closed_reason = ?, closed_source_key = ?, closed_fingerprint = ?, closed_amount_nano = ?, closed_at = ? WHERE hold_key = ? AND status = 'open'`, newReleased, string(input.Reason), closedSource, fp, amount, closedAt, hold.HoldKey).Exec(ctx)
		if err != nil {
			return billing.Posting{}, err
		}
		if err := requireRowsAffected(holdResult, 1, "close authorization hold"); err != nil {
			return billing.Posting{}, fmt.Errorf("%w: %v", billing.ErrAuthorizationUnavailable, err)
		}
	} else {
		// Partial release must not stamp closed_* identity fields while the hold
		// remains open; those fields are reserved for the final close replay key.
		holdResult, err := tx.NewRaw(`UPDATE authorization_holds SET released_amount_nano = ? WHERE hold_key = ? AND status = 'open' AND released_amount_nano = ?`, newReleased, hold.HoldKey, hold.ReleasedAmount).Exec(ctx)
		if err != nil {
			return billing.Posting{}, err
		}
		if err := requireRowsAffected(holdResult, 1, "partial authorization release"); err != nil {
			return billing.Posting{}, fmt.Errorf("%w: %v", billing.ErrAuthorizationUnavailable, err)
		}
	}
	accountResult, err := tx.NewRaw(`UPDATE billing_accounts SET reserved_nano = ?, version = ?, updated_at = ? WHERE account_id = ? AND version = ?`, afterSnapshot.ReservedNano, afterSnapshot.Version, time.Now().UTC(), input.AccountID, account.Version).Exec(ctx)
	if err != nil {
		return billing.Posting{}, err
	}
	if err := requireRowsAffected(accountResult, 1, "release account version"); err != nil {
		return billing.Posting{}, fmt.Errorf("%w: %v", billing.ErrAuthorizationUnavailable, err)
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: closedSource, AccountID: input.AccountID, OperationKind: "authorization_release", SourceKey: source, Fingerprint: fp, Before: before, After: afterSnapshot, SequenceStart: posted.AccountSequence, SequenceEnd: posted.AccountSequence}); err != nil {
		return billing.Posting{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.Posting{}, err
	}
	return billing.Posting{OperationKey: closedSource, Transaction: posted, Before: before, After: afterSnapshot}, nil
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
	return billing.AccountSnapshot{BalanceNano: a.BalanceNano, ReservedNano: a.ReservedNano, SpendableNano: spendable, CreditFloorNano: a.CreditFloorNano(), CreditLimitNano: a.CreditLimit, Mode: a.Mode, Currency: a.Currency, Version: a.Version}, nil
}

func insertOperationSnapshot(ctx context.Context, tx bun.Tx, in operationSnapshotInput) error {
	integrity := snapshotIntegrity(in.OperationKey, in.AccountID, in.OperationKind, in.SourceKey, in.Fingerprint, in.Before, in.After, in.SequenceStart, in.SequenceEnd)
	_, err := tx.NewRaw(`INSERT INTO billing_operation_snapshots(operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.OperationKey, in.AccountID, in.OperationKind, in.SourceKey, in.Fingerprint, integrity, in.After.Currency, string(in.After.Mode), in.Before.BalanceNano, in.After.BalanceNano, in.Before.ReservedNano, in.After.ReservedNano, in.Before.SpendableNano, in.After.SpendableNano, in.After.CreditFloorNano, in.After.CreditLimitNano, in.Before.Version, in.After.Version, in.SequenceStart, in.SequenceEnd, time.Now().UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert operation snapshot: %w", err)
	}
	return nil
}

func loadOperationSnapshot(ctx context.Context, q bun.IDB, accountID, kind, source string) (operationSnapshotRow, bool, error) {
	var row operationSnapshotRow
	err := q.NewRaw(`SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = ? AND source_key = ?`, accountID, kind, source).Scan(ctx, &row)
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
		return billing.AccountSnapshot{BalanceNano: row.BalanceBefore, ReservedNano: row.ReservedBefore, SpendableNano: row.SpendableBefore, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionBefore}
	}
	return billing.AccountSnapshot{BalanceNano: row.BalanceAfter, ReservedNano: row.ReservedAfter, SpendableNano: row.SpendableAfter, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionAfter}
}
