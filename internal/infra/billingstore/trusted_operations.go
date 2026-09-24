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
	}, "", nil)
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
	}, "", nil)
}

func (s *DurableStore) PostAdjustment(ctx context.Context, input billing.AdjustmentInput) (billing.Posting, error) {
	if err := input.Validate(); err != nil {
		return billing.Posting{}, err
	}
	fp, err := input.Fingerprint()
	if err != nil {
		return billing.Posting{}, err
	}
	if _, err := billing.ResolveDirectAdjustmentOwner(input); err != nil {
		return billing.Posting{}, err
	}
	if input.Claim != nil {
		// Store scope is required for canonical direct pin identity; use the
		// durable store boundary, not caller strings.
		if s == nil {
			return billing.Posting{}, fmt.Errorf("billingstore: nil store")
		}
		if err := billing.ValidateDirectAdjustmentClaim(*input.Claim, s.storeID, strings.TrimSpace(input.AccountID), strings.TrimSpace(input.SourceKey)); err != nil {
			return billing.Posting{}, err
		}
	}
	delta := int64(1)
	if input.Direction == billing.AdjustmentDebit {
		delta = -1
	}
	owner, _ := billing.ResolveDirectAdjustmentOwner(input)
	return s.postFinancialCommand(ctx, "adjustment", input.AccountID, input.SourceKey, fp, input.Amount, delta, func() (billing.JournalTransaction, error) {
		return billing.AdjustmentJournalIntent(input)
	}, owner, input.Claim)
}

func (s *DurableStore) postFinancialCommand(ctx context.Context, kind, accountID, sourceKey, fingerprint string, amount billing.Money, balanceDelta int64, intent func() (billing.JournalTransaction, error), owner string, claim *billing.CutoverClaimMetadata) (billing.Posting, error) {
	if s == nil || s.db == nil {
		return billing.Posting{}, fmt.Errorf("billingstore: nil store")
	}
	operationKey := billing.ScopedOperationKey(kind, strings.TrimSpace(accountID), strings.TrimSpace(sourceKey))
	return withAccountTx(ctx, accountTxRetry{
		Attempts:  30,
		Delay:     3 * time.Millisecond,
		Exhausted: fmt.Errorf("%w: retry budget exhausted", billing.ErrBillingStoreUnavailable),
	}, func() (billing.Posting, error) {
		return s.postFinancialAttempt(ctx, kind, operationKey, strings.TrimSpace(accountID), strings.TrimSpace(sourceKey), fingerprint, amount, balanceDelta, intent, owner, claim)
	})
}

func (s *DurableStore) postFinancialAttempt(ctx context.Context, kind, operationKey, accountID, sourceKey, fingerprint string, amount billing.Money, balanceDelta int64, intent func() (billing.JournalTransaction, error), owner string, claim *billing.CutoverClaimMetadata) (billing.Posting, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.Posting{}, fmt.Errorf("billingstore: begin %s: %w", kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	// B2b4 posting-time ownership fence for direct adjustments
	// (financial_adjustment only, per-source pin). Funding/payment/policy keep
	// existing behavior and are not cutover adjustment writers (see inventory
	// disposition). Pin + money commit atomically in this transaction.
	var pinKey string
	var pin billing.PostingPin
	var pinFound bool
	var curState billing.AccountingCutoverState
	var curVersion, curEpoch uint64
	var curGeneration int
	isAdjustment := kind == "adjustment"
	if isAdjustment {
		if strings.TrimSpace(owner) == "" {
			owner = billing.PostingOwnerV1
		}
		if owner != billing.PostingOwnerV1 && owner != billing.PostingOwnerV2 {
			return billing.Posting{}, fmt.Errorf("%w: %w: unknown direct adjustment owner %q", billing.ErrPostingOwnershipInvalid, billing.ErrInvalidRecord, owner)
		}
		if claim != nil && strings.TrimSpace(claim.Owner) != "" && claim.Owner != owner {
			return billing.Posting{}, fmt.Errorf("%w: claim owner %q differs from posting owner %q", billing.ErrPostingOwnershipConflict, claim.Owner, owner)
		}
		if err := s.b2b4Fault("b2b4-enter"); err != nil {
			return billing.Posting{}, err
		}
		// F1: lock/create the per-store marker before pin/account effects.
		lockedMarker, merr := s.ensureAndLockAccountingCutoverTx(ctx, tx)
		if merr != nil {
			return billing.Posting{}, merr
		}
		curState = lockedMarker.State
		curVersion = lockedMarker.Version
		curEpoch = lockedMarker.Epoch
		curGeneration = lockedMarker.Generation
		var kerr error
		pinKey, kerr = billing.DirectFinancialAdjustmentPostingOperationKey(s.storeID, accountID, sourceKey)
		if kerr != nil {
			return billing.Posting{}, kerr
		}
		if claim != nil {
			if err := billing.ValidateDirectAdjustmentClaim(*claim, s.storeID, accountID, sourceKey); err != nil {
				return billing.Posting{}, err
			}
			if claim.OperationKey != pinKey {
				return billing.Posting{}, fmt.Errorf("%w: claim key %q != canonical %q", billing.ErrPostingOwnershipConflict, claim.OperationKey, pinKey)
			}
			if claim.Owner != owner {
				return billing.Posting{}, fmt.Errorf("%w: claim owner %q != posting owner %q", billing.ErrPostingOwnershipConflict, claim.Owner, owner)
			}
		}
		prow, pfound, perr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationFinancialAdjustment, pinKey)
		if perr != nil {
			return billing.Posting{}, perr
		}
		pinFound = pfound
		if pfound {
			pin, err = postingOwnershipRowToPin(prow)
			if err != nil {
				return billing.Posting{}, err
			}
			if pin.Kind != billing.PostingOperationFinancialAdjustment || pin.OperationKey != pinKey || pin.AccountID != accountID || strings.TrimSpace(pin.HeadKey) != strings.TrimSpace(sourceKey) {
				return billing.Posting{}, fmt.Errorf("%w: direct pin identity mismatch for %q", billing.ErrPostingOwnershipConflict, pinKey)
			}
			if pin.Owner != owner {
				return billing.Posting{}, fmt.Errorf("%w: direct pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, pin.Owner, owner)
			}
		}
	}
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
		if isAdjustment {
			if err := s.b2b4Fault("b2b4-replay-pin"); err != nil {
				return billing.Posting{}, err
			}
			replayed := postingFromSnapshot(existing, journal, true)
			completionTxID := journal.ID
			if err := s.b2b4EnsureDirectReplayPin(ctx, tx, accountID, sourceKey, pinKey, owner, curVersion, curEpoch, curGeneration, curState, pin, pinFound, operationKey, completionTxID); err != nil {
				return billing.Posting{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.Posting{}, fmt.Errorf("billingstore: commit %s replay: %w", kind, err)
			}
			return replayed, nil
		}
		return postingFromSnapshot(existing, journal, true), nil
	}
	// B2b4 synchronous ownership fence for new direct adjustments: ordinary
	// new adjustments are fenced in draining/v2_active. Exact replay above
	// remains allowed per B1. Funding, payment and policy commands are not
	// financial adjustments and keep their existing behavior. Pin owner stable;
	// only token==current required (F6, sync uses marker lock directly).
	if isAdjustment {
		if claim != nil {
			if claim.MarkerVersion != curVersion || claim.MarkerEpoch != curEpoch || claim.MarkerState != curState {
				return billing.Posting{}, fmt.Errorf("%w: direct claim %d/%d/%q != current %d/%d/%q",
					billing.ErrPostingOwnershipFence, claim.MarkerVersion, claim.MarkerEpoch, string(claim.MarkerState), curVersion, curEpoch, string(curState))
			}
		}
		if !pinFound {
			if !billing.IsPostingOwnerAllowedForNew(curState, owner) {
				return billing.Posting{}, fmt.Errorf("%w: direct owner %q not allowed for new in %q", billing.ErrPostingOwnershipFence, owner, string(curState))
			}
			if claim != nil {
				return billing.Posting{}, fmt.Errorf("%w: direct claim without classified pin for %q", billing.ErrPostingOwnershipFence, pinKey)
			}
			if curState == billing.AccountingCutoverV1Draining {
				return billing.Posting{}, fmt.Errorf("%w: draining forbids new direct adjustment for %q", billing.ErrPostingOwnershipFence, pinKey)
			}
			if owner == billing.PostingOwnerV2 && !billing.IsV2NewWorkAuthorized(curState) {
				return billing.Posting{}, fmt.Errorf("%w: V2 direct adjustment requires v2_active", billing.ErrCutoverV2NotAuthorized)
			}
			if err := s.b2b4Fault("b2b4-pin-acquire"); err != nil {
				return billing.Posting{}, err
			}
			nowUnix := nowUnixNano()
			acquired, err := b2b4InsertDirectPinTx(ctx, tx, s, accountID, sourceKey, pinKey, owner, curVersion, curEpoch, curGeneration, curState, nowUnix)
			if err != nil {
				return billing.Posting{}, err
			}
			pin = acquired
			pinFound = true
		} else {
			if pin.Status == billing.PostingPinCompleted {
				return billing.Posting{}, fmt.Errorf("%w: direct pin already completed for %q", billing.ErrPostingOwnershipConflict, pinKey)
			}
			if !billing.IsPostingPinAcquireReplayAllowed(curState, pin) {
				return billing.Posting{}, fmt.Errorf("%w: direct pin replay not allowed in %q", billing.ErrPostingOwnershipFence, string(curState))
			}
			if !billing.IsPostingPinCompleteAllowed(curState, pin) {
				return billing.Posting{}, fmt.Errorf("%w: direct pin completion not allowed in %q", billing.ErrPostingOwnershipFence, string(curState))
			}
			if curState == billing.AccountingCutoverV1Draining {
				return billing.Posting{}, fmt.Errorf("%w: draining forbids new direct adjustment for %q", billing.ErrPostingOwnershipFence, pinKey)
			}
		}
		if err := s.b2b4Fault("b2b4-before-effects"); err != nil {
			return billing.Posting{}, err
		}
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
	if isAdjustment {
		if err := s.b2b4Fault("b2b4-before-journal"); err != nil {
			return billing.Posting{}, err
		}
	}
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
	if isAdjustment {
		// B2b4 atomic completion: per-source pin + balance/journal commit in
		// the same transaction. Exact replay above already completed the pin;
		// conflicting replay fails before effects.
		if err := s.b2b4Fault("b2b4-before-pin-complete"); err != nil {
			return billing.Posting{}, err
		}
		nowUnix := nowUnixNano()
		if pinFound && nowUnix < pin.CreatedAtUnix {
			nowUnix = pin.CreatedAtUnix
		}
		if !pinFound {
			return billing.Posting{}, fmt.Errorf("%w: direct pin missing for completion", billing.ErrPostingOwnershipNotFound)
		}
		if pin.Status != billing.PostingPinPinned {
			return billing.Posting{}, fmt.Errorf("%w: direct pin already completed for %q", billing.ErrPostingOwnershipConflict, pinKey)
		}
		if err := b2b4CompleteDirectPinTx(ctx, tx, s.storeID, pinKey, operationKey, posted.ID, nowUnix); err != nil {
			return billing.Posting{}, err
		}
		if err := s.b2b4Fault("b2b4-before-commit"); err != nil {
			return billing.Posting{}, err
		}
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
