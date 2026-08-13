package billingstore

import (
	"context"
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

const authorizationHoldLookupSQL = `SELECT hold_key, authorization_id, account_id, tur_key, currency, amount_nano, status, source_key, fingerprint, pricing_ref, charge_policy_ref, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, closed_reason, expires_at, created_at, closed_at FROM authorization_holds`

// Authorize atomically locks the account, validates spendable capacity, creates
// one deterministic hold, posts its balanced authorization-book transaction,
// updates Reserved/version, and commits all evidence together.
func (s *DurableStore) Authorize(ctx context.Context, input billing.AuthorizeInput) (billing.Authorization, error) {
	if s == nil || s.db == nil {
		return billing.Authorization{}, fmt.Errorf("billingstore: nil store")
	}
	sealed, err := input.Seal()
	if err != nil {
		return billing.Authorization{}, err
	}
	return withAccountTx(ctx, accountTxRetry{
		Attempts:  40,
		Delay:     3 * time.Millisecond,
		Classify:  classifyAuthorizationStoreError,
		Exhausted: fmt.Errorf("%w: lock/replay retry budget exhausted", billing.ErrAuthorizationUnavailable),
	}, func() (billing.Authorization, error) {
		return s.authorizeAttempt(ctx, input, sealed)
	})
}

func (s *DurableStore) authorizeAttempt(ctx context.Context, input billing.AuthorizeInput, sealed billing.Authorization) (billing.Authorization, error) {
	holdKey, err := billing.AuthorizationHoldKey(sealed.AccountID, sealed.TURKey)
	if err != nil {
		return billing.Authorization{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.Authorization{}, fmt.Errorf("billingstore: begin authorization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), sealed.AccountID); err != nil {
		return billing.Authorization{}, err
	}
	existing, err := scanAuthorizationHold(ctx, tx, sealed.AccountID, sealed.TURKey)
	if err == nil {
		out, decodeErr := authorizationFromRow(existing)
		if decodeErr != nil {
			return billing.Authorization{}, decodeErr
		}
		account, accountErr := getAccountTx(ctx, tx, sealed.AccountID)
		if accountErr != nil {
			return billing.Authorization{}, accountErr
		}
		if account.State != billing.AccountReady {
			return billing.Authorization{}, billing.ErrAccountNotReady
		}
		now := time.Now().UTC()
		if admitErr := out.AssertOpenForAdmission(now); admitErr != nil {
			return billing.Authorization{}, admitErr
		}
		if replayErr := billing.CheckAuthorizationReplay(out, input); replayErr != nil {
			return billing.Authorization{}, replayErr
		}
		// Read-only replay still commits so PostgreSQL releases FOR UPDATE promptly
		// under high-QPS short-session admission storms.
		if err := tx.Commit(); err != nil {
			return billing.Authorization{}, fmt.Errorf("billingstore: commit authorization replay: %w", err)
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return billing.Authorization{}, fmt.Errorf("billingstore: lookup authorization hold: %w", err)
	}

	account, err := getAccountTx(ctx, tx, sealed.AccountID)
	if err != nil {
		return billing.Authorization{}, err
	}
	if account.State != billing.AccountReady {
		return billing.Authorization{}, billing.ErrAccountNotReady
	}
	if sealed.Amount.Currency != account.Currency {
		return billing.Authorization{}, billing.ErrMoneyCurrencyMismatch
	}
	now := time.Now().UTC()
	expiresAt := sealed.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(15 * time.Minute)
	}
	if !expiresAt.After(now) {
		return billing.Authorization{}, fmt.Errorf("%w: expires_at must be in the future", billing.ErrAuthorizationExpired)
	}
	spendableBefore, err := account.SpendableNano()
	if err != nil {
		return billing.Authorization{}, err
	}
	if spendableBefore < sealed.Amount.Nano {
		return billing.Authorization{}, billing.ErrInsufficientSpendable
	}
	reservedAfter, err := checkedAddStore(account.ReservedNano, sealed.Amount.Nano)
	if err != nil {
		return billing.Authorization{}, err
	}
	spendableAfter := spendableBefore - sealed.Amount.Nano
	if spendableAfter < 0 {
		return billing.Authorization{}, billing.ErrInsufficientSpendable
	}
	beforeSpendable := spendableBefore
	before := billing.AccountSnapshot{
		BalanceNano: account.BalanceNano, ReservedNano: account.ReservedNano,
		SpendableNano: beforeSpendable, CreditFloorNano: account.CreditFloorNano(),
		CreditLimitNano: account.CreditLimit, Mode: account.Mode, Currency: account.Currency, Version: account.Version,
	}
	if account.Version >= uint64(math.MaxInt64) {
		return billing.Authorization{}, fmt.Errorf("%w: account version overflow", billing.ErrAuthorizationInvalid)
	}
	versionAfter := account.Version + 1
	after := billing.AccountSnapshot{
		BalanceNano: account.BalanceNano, ReservedNano: reservedAfter,
		SpendableNano: spendableAfter, CreditFloorNano: account.CreditFloorNano(),
		CreditLimitNano: account.CreditLimit, Mode: account.Mode, Currency: account.Currency, Version: versionAfter,
	}
	pricingRef, _ := json.Marshal(sealed.PricingRef)
	policyRef, _ := json.Marshal(sealed.ChargePolicyRef)
	if _, err := tx.NewRaw(`INSERT INTO authorization_holds(hold_key, authorization_id, account_id, tur_key, currency, amount_nano, status, source_key, fingerprint, pricing_ref, charge_policy_ref, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, closed_reason, expires_at, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		holdKey, sealed.ID, sealed.AccountID, sealed.TURKey, sealed.Amount.Currency, sealed.Amount.Nano, string(billing.HoldStatusOpen), authorizationSourceKey(holdKey), sealed.Fingerprint, string(pricingRef), string(policyRef), string(account.Mode), before.BalanceNano, after.BalanceNano, before.ReservedNano, after.ReservedNano, before.SpendableNano, after.SpendableNano, before.CreditFloorNano, before.CreditLimitNano, before.Version, after.Version, "", expiresAt, now).Exec(ctx); err != nil {
		return billing.Authorization{}, fmt.Errorf("billingstore: insert authorization hold: %w", err)
	}

	journal := billing.JournalTransaction{
		ID:   authorizationTransactionID(holdKey),
		Book: billing.JournalBookAuthorization, Currency: sealed.Amount.Currency,
		SourceKey: authorizationSourceKey(holdKey), SemanticFingerprint: "",
		AccountID: sealed.AccountID, TurnID: sealed.TURKey, ALegID: sealed.TURKey,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_reserved_exposure", Side: billing.JournalDebit, Amount: sealed.Amount},
			{LedgerAccount: "authorization_contra", Side: billing.JournalCredit, Amount: sealed.Amount},
		},
	}
	var postedSeq uint64
	if sealed.Amount.Nano > 0 {
		journal.OperationKind = "authorization"
		journal.BalanceBefore, journal.BalanceAfter = before.BalanceNano, after.BalanceNano
		journal.ReservedBefore, journal.ReservedAfter = before.ReservedNano, after.ReservedNano
		journal.SpendableBefore, journal.SpendableAfter = before.SpendableNano, after.SpendableNano
		journal.CreditFloor, journal.CreditLimit, journal.Mode = before.CreditFloorNano, before.CreditLimitNano, string(before.Mode)
		journal.SnapshotVersionBefore, journal.SnapshotVersionAfter = before.Version, after.Version
		posted, existing, postErr := s.postJournalInTx(ctx, tx, journal)
		if postErr != nil {
			return billing.Authorization{}, postErr
		}
		if existing {
			return billing.Authorization{}, billing.ErrAuthorizationConflict
		}
		postedSeq = posted.AccountSequence
	}
	accountResult, err := tx.NewRaw(`UPDATE billing_accounts SET reserved_nano = ?, version = ?, updated_at = ? WHERE account_id = ? AND version = ?`, reservedAfter, versionAfter, now, sealed.AccountID, account.Version).Exec(ctx)
	if err != nil {
		return billing.Authorization{}, fmt.Errorf("billingstore: update authorized account: %w", err)
	}
	if err := requireRowsAffected(accountResult, 1, "authorized account version"); err != nil {
		return billing.Authorization{}, fmt.Errorf("%w: %v", billing.ErrAuthorizationUnavailable, err)
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
		OperationKey: authorizationSourceKey(holdKey), AccountID: sealed.AccountID, OperationKind: "authorization",
		SourceKey: authorizationSourceKey(holdKey), Fingerprint: sealed.Fingerprint, Before: before, After: after,
		SequenceStart: postedSeq, SequenceEnd: postedSeq,
	}); err != nil {
		return billing.Authorization{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.Authorization{}, fmt.Errorf("billingstore: commit authorization: %w", err)
	}
	sealed.ExpiresAt = expiresAt
	sealed.Status = billing.HoldStatusOpen
	sealed.Before = before
	sealed.After = after
	return sealed, nil
}

// GetAuthorization returns the existing durable hold for one account and TUR
// key. It does not create, extend, or release a hold. A missing row fails
// closed; lookup never invents an authorization from TUR refs alone.
func (s *DurableStore) GetAuthorization(ctx context.Context, accountID, turKey string) (billing.Authorization, error) {
	if s == nil || s.db == nil {
		return billing.Authorization{}, fmt.Errorf("%w: nil store", billing.ErrAuthorizationUnavailable)
	}
	row, err := scanAuthorizationHold(ctx, s.db, accountID, turKey)
	if err != nil {
		if errors.Is(err, billing.ErrAuthorizationInvalid) {
			return billing.Authorization{}, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			return billing.Authorization{}, fmt.Errorf("%w: %w", billing.ErrAuthorizationNotFound, ErrAuthorizationHoldNotFound)
		}
		return billing.Authorization{}, fmt.Errorf("%w: lookup authorization hold: %w", billing.ErrAuthorizationUnavailable, err)
	}
	return authorizationFromRow(row)
}

func scanAuthorizationHold(ctx context.Context, q bun.IDB, accountID, turKey string) (authorizationHoldRow, error) {
	holdKey, err := billing.AuthorizationHoldKey(accountID, turKey)
	if err != nil {
		return authorizationHoldRow{}, err
	}
	accountID = strings.TrimSpace(accountID)
	turKey = strings.TrimSpace(turKey)
	var row authorizationHoldRow
	err = q.NewRaw(authorizationHoldLookupSQL+` WHERE hold_key = ?`, holdKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		// Keep upgrades from the pre-length-prefixed key format replay-safe.
		err = q.NewRaw(authorizationHoldLookupSQL+` WHERE account_id = ? AND tur_key = ?`, accountID, turKey).Scan(ctx, &row)
	}
	if err != nil {
		return authorizationHoldRow{}, err
	}
	return row, nil
}

func getAccountTx(ctx context.Context, q bun.IDB, accountID string) (billing.Account, error) {
	var row accountRow
	if err := q.NewRaw(`SELECT account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, reserved_nano, version, state FROM billing_accounts WHERE account_id = ?`, accountID).Scan(ctx, &row); errors.Is(err, sql.ErrNoRows) {
		return billing.Account{}, ErrAccountNotFound
	} else if err != nil {
		return billing.Account{}, fmt.Errorf("billingstore: read account: %w", err)
	}
	account := billing.Account{ID: row.ID, Currency: row.Currency, Mode: billing.AccountMode(row.Mode), CreditLimit: row.CreditLimit, BalanceNano: row.Balance, ReservedNano: row.Reserved, Version: row.Version, State: billing.AccountState(row.State)}
	if err := account.Validate(); err != nil {
		return billing.Account{}, err
	}
	return account, nil
}

func classifyAuthorizationStoreError(err error) error {
	if err == nil || isAuthorizationDomainError(err) {
		return err
	}
	return fmt.Errorf("%w: %w", billing.ErrAuthorizationUnavailable, err)
}

func isAuthorizationDomainError(err error) bool {
	return errors.Is(err, billing.ErrInsufficientSpendable) ||
		errors.Is(err, billing.ErrAccountNotReady) ||
		errors.Is(err, billing.ErrAccountInvalid) ||
		errors.Is(err, billing.ErrAuthorizationInvalid) ||
		errors.Is(err, billing.ErrAuthorizationExpired) ||
		errors.Is(err, billing.ErrAuthorizationClosed) ||
		errors.Is(err, billing.ErrAuthorizationConflict) ||
		errors.Is(err, billing.ErrAuthorizationUnavailable) ||
		errors.Is(err, billing.ErrAuthorizationNotFound) ||
		errors.Is(err, billing.ErrMoneyCurrencyMismatch) ||
		errors.Is(err, billing.ErrLegacyAuthorization) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func authorizationSourceKey(holdKey string) string { return "authorization-hold:v1:" + holdKey }

func authorizationTransactionID(holdKey string) string { return "authorization-hold:v1:" + holdKey }

func authorizationFromRow(row authorizationHoldRow) (billing.Authorization, error) {
	// Rows created before the authorization snapshot schema cannot be replayed
	// safely: their pricing/policy identity and account snapshots are unknown.
	// Fail closed instead of presenting a fabricated replayable authorization.
	if row.PricingRef == "" || row.ChargePolicyRef == "" {
		return billing.Authorization{}, fmt.Errorf("%w: %w: immutable snapshot identity is missing", billing.ErrAuthorizationInvalid, billing.ErrLegacyAuthorization)
	}
	var pricing, policy billing.VersionRef
	if err := json.Unmarshal([]byte(row.PricingRef), &pricing); err != nil && row.PricingRef != "" {
		return billing.Authorization{}, fmt.Errorf("billingstore: decode authorization pricing ref: %w", err)
	}
	if err := json.Unmarshal([]byte(row.ChargePolicyRef), &policy); err != nil && row.ChargePolicyRef != "" {
		return billing.Authorization{}, fmt.Errorf("billingstore: decode authorization policy ref: %w", err)
	}
	return billing.Authorization{
		ID: row.AuthorizationID, AccountID: row.AccountID, TURKey: row.TURKey,
		Amount: billing.Money{Nano: row.AmountNano, Currency: row.Currency}, PricingRef: pricing, ChargePolicyRef: policy,
		Fingerprint: row.Fingerprint, ExpiresAt: row.ExpiresAt, Status: billing.HoldStatus(row.Status),
		Before: billing.AccountSnapshot{BalanceNano: row.BalanceBefore, ReservedNano: row.ReservedBefore, SpendableNano: row.SpendableBefore, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionBefore},
		After:  billing.AccountSnapshot{BalanceNano: row.BalanceAfter, ReservedNano: row.ReservedAfter, SpendableNano: row.SpendableAfter, CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionAfter},
	}, nil
}

func checkedAddStore(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, fmt.Errorf("%w: reserved amount overflow", billing.ErrAuthorizationInvalid)
	}
	return a + b, nil
}
