package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

const settlementReleaseSourcePrefix = "settled:"

// ApplyBillingResult is the sole authoritative monetary settlement boundary for
// a sealed TUR. Every mutation below is committed in one account transaction.
func (s *DurableStore) ApplyBillingResult(ctx context.Context, input billing.ApplyBillingInput) (billing.Settlement, error) {
	if s == nil || s.db == nil {
		return billing.Settlement{}, fmt.Errorf("billingstore: nil store")
	}
	if err := input.Validate(); err != nil {
		return billing.Settlement{}, err
	}
	sealed, err := input.Record.Seal()
	if err != nil {
		return billing.Settlement{}, err
	}
	resultFingerprint, err := input.Result.SemanticFingerprint()
	if err != nil {
		return billing.Settlement{}, err
	}
	for attempt := range 40 {
		out, callErr := s.applyBillingAttempt(ctx, sealed, input.Authorization, input.Result, resultFingerprint)
		if callErr == nil {
			return out, nil
		}
		if !isSQLiteBusy(callErr) && !isUniqueViolation(callErr) {
			return billing.Settlement{}, callErr
		}
		if waitErr := waitContention(ctx, time.Duration(attempt+1)*3*time.Millisecond); waitErr != nil {
			return billing.Settlement{}, waitErr
		}
	}
	return billing.Settlement{}, fmt.Errorf("%w: settlement retry budget exhausted", billing.ErrAuthorizationUnavailable)
}

func (s *DurableStore) applyBillingAttempt(ctx context.Context, record billing.TurnUsageRecord, authorization billing.Authorization, result billing.BillingResult, resultFingerprint string) (billing.Settlement, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.Settlement{}, fmt.Errorf("billingstore: begin settlement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), record.AccountID); err != nil {
		return billing.Settlement{}, err
	}
	if err := lockProcessingForSettlement(ctx, tx, s.db.Dialect().Name(), record.Key); err != nil {
		return billing.Settlement{}, err
	}
	storedRecord, err := loadUsageRecordTx(ctx, tx, record.Key)
	if err != nil {
		return billing.Settlement{}, err
	}
	if err := billing.CheckReplay(storedRecord, record); err != nil {
		return billing.Settlement{}, fmt.Errorf("%w: stored TUR differs: %v", billing.ErrSettlementInvalid, err)
	}

	hold, err := loadSettlementHold(ctx, tx, record.AccountID, authorization.ID, record.Key)
	if err != nil {
		return billing.Settlement{}, err
	}
	if hold.Fingerprint != authorization.Fingerprint || hold.AmountNano != authorization.Amount.Nano || hold.Currency != authorization.Amount.Currency {
		return billing.Settlement{}, billing.ErrSettlementConflict
	}
	customerSource, _ := billing.CustomerSettlementSourceKey(record.Key)
	if existing, found, lookupErr := loadOperationSnapshot(ctx, tx, record.AccountID, "customer_settlement", record.Key); lookupErr != nil {
		return billing.Settlement{}, lookupErr
	} else if found {
		if existing.Fingerprint != resultFingerprint {
			return billing.Settlement{}, ErrOperationConflict
		}
		settlement, err := s.loadSettlementReplay(ctx, tx, record, result)
		if err != nil {
			return billing.Settlement{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Settlement{}, err
		}
		settlement.Replayed = true
		return settlement, nil
	}

	if hold.Status == "closed" {
		return billing.Settlement{}, billing.ErrSettlementConflict
	}
	remaining := hold.AmountNano - hold.ReleasedAmount
	if remaining < 0 || result.CustomerCharge.Nano > remaining {
		return billing.Settlement{}, billing.ErrSettlementInvalid
	}
	account, err := getAccountTx(ctx, tx, record.AccountID)
	if err != nil {
		return billing.Settlement{}, err
	}
	if account.State != billing.AccountReady {
		return billing.Settlement{}, billing.ErrAccountNotReady
	}
	if account.Currency != result.CustomerCharge.Currency {
		return billing.Settlement{}, billing.ErrMoneyCurrencyMismatch
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.Settlement{}, err
	}
	after, err := account.ApplyBalanceDelta(billing.Money{Nano: -result.CustomerCharge.Nano, Currency: account.Currency})
	if err != nil {
		return billing.Settlement{}, err
	}
	if after.BalanceNano < after.CreditFloorNano() {
		return billing.Settlement{}, billing.ErrInsufficientSpendable
	}
	after, err = after.Release(billing.Money{Nano: remaining, Currency: account.Currency})
	if err != nil {
		return billing.Settlement{}, err
	}
	if account.Version >= uint64(^uint64(0)>>1) {
		return billing.Settlement{}, fmt.Errorf("%w: account version overflow", billing.ErrSettlementInvalid)
	}
	after.Version = account.Version + 1
	afterSnapshot, err := snapshotForAccount(after)
	if err != nil {
		return billing.Settlement{}, err
	}

	base := func(kind, turnID, aLegID, bLegID string) billing.JournalTransaction {
		return billing.JournalTransaction{
			AccountID: record.AccountID, TurnID: turnID, ALegID: aLegID, BLegID: bLegID,
			OperationKind: kind, Currency: account.Currency,
			BalanceBefore: before.BalanceNano, BalanceAfter: afterSnapshot.BalanceNano,
			ReservedBefore: before.ReservedNano, ReservedAfter: afterSnapshot.ReservedNano,
			SpendableBefore: before.SpendableNano, SpendableAfter: afterSnapshot.SpendableNano,
			CreditFloor: afterSnapshot.CreditFloorNano, CreditLimit: afterSnapshot.CreditLimitNano,
			Mode: string(afterSnapshot.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: afterSnapshot.Version,
		}
	}
	settlement := billing.Settlement{TURKey: record.Key}
	firstSequence, lastSequence := uint64(0), uint64(0)
	rememberSequence := func(sequence uint64) {
		if sequence == 0 {
			return
		}
		if firstSequence == 0 {
			firstSequence = sequence
		}
		lastSequence = sequence
	}

	if result.CustomerCharge.Nano > 0 {
		journal := base("customer_settlement", record.TurnID, record.ALegID, "")
		journal.ID, _ = billing.CustomerSettlementSourceKey(record.Key)
		journal.SourceKey = journal.ID
		journal.Book = billing.JournalBookFinancial
		journal.Entries = []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: result.CustomerCharge},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: result.CustomerCharge},
		}
		posted, replayed, postErr := s.postJournalInTx(ctx, tx, journal)
		if postErr != nil || replayed {
			if postErr != nil {
				return billing.Settlement{}, postErr
			}
			// Fail closed: a journal without its settlement snapshot needs operator
			// repair. Adopting here could double-apply materialized balance.
			return billing.Settlement{}, ErrOperationConflict
		}
		rememberSequence(posted.AccountSequence)
		settlement.Customer = billing.Posting{OperationKey: customerSource, Transaction: posted, Before: before, After: afterSnapshot}
	} else {
		settlement.Customer = billing.Posting{OperationKey: customerSource, Before: before, After: afterSnapshot}
	}
	if err := s.settlementFault("after_customer_journal"); err != nil {
		return billing.Settlement{}, err
	}

	costByLUR := make(map[string]billing.OperatorCostResult, len(result.OperatorCosts))
	for _, cost := range result.OperatorCosts {
		costByLUR[cost.LURKey] = cost
	}
	type zeroCostSnapshot struct {
		legKey, providerSource, fingerprint string
	}
	var deferredZeroCosts []zeroCostSnapshot
	for _, leg := range record.Legs {
		cost := costByLUR[leg.Key]
		providerSource, _ := billing.ProviderCostSourceKey(leg.Key)
		if cost.Amount.Nano == 0 {
			costFingerprint, fpErr := cost.SemanticFingerprint()
			if fpErr != nil {
				return billing.Settlement{}, fpErr
			}
			deferredZeroCosts = append(deferredZeroCosts, zeroCostSnapshot{legKey: leg.Key, providerSource: providerSource, fingerprint: costFingerprint})
			settlement.ProviderCosts = append(settlement.ProviderCosts, billing.Posting{OperationKey: providerSource, Before: before, After: afterSnapshot})
			continue
		}
		journal := base("provider_cogs", record.TurnID, record.ALegID, leg.BLegID)
		journal.ID, journal.SourceKey = providerSource, providerSource
		journal.Book = billing.JournalBookFinancial
		journal.Entries = []billing.JournalEntry{
			{LedgerAccount: "inference_provider_cogs", Side: billing.JournalDebit, Amount: cost.Amount},
			{LedgerAccount: "provider_payable_clearing", Side: billing.JournalCredit, Amount: cost.Amount},
		}
		posted, replayed, postErr := s.postJournalInTx(ctx, tx, journal)
		if postErr != nil || replayed {
			if postErr != nil {
				return billing.Settlement{}, postErr
			}
			return billing.Settlement{}, ErrOperationConflict
		}
		rememberSequence(posted.AccountSequence)
		settlement.ProviderCosts = append(settlement.ProviderCosts, billing.Posting{OperationKey: providerSource, Transaction: posted, Before: before, After: afterSnapshot})
		fp, fpErr := posted.CanonicalFingerprint()
		if fpErr != nil {
			return billing.Settlement{}, fpErr
		}
		if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: providerSource, AccountID: record.AccountID, OperationKind: "provider_cogs", SourceKey: leg.Key, Fingerprint: fp, Before: before, After: afterSnapshot, SequenceStart: posted.AccountSequence, SequenceEnd: posted.AccountSequence}); err != nil {
			return billing.Settlement{}, err
		}
		if err := s.settlementFault("after_provider_journal"); err != nil {
			return billing.Settlement{}, err
		}
	}

	releaseSource := settlementReleaseSourceKey(record.Key)
	if remaining > 0 {
		journal := base("authorization_release", record.TurnID, record.ALegID, "")
		journal.ID, journal.SourceKey = releaseSource, releaseSource
		journal.Book = billing.JournalBookAuthorization
		amount := billing.Money{Nano: remaining, Currency: account.Currency}
		journal.Entries = []billing.JournalEntry{
			{LedgerAccount: "authorization_contra", Side: billing.JournalDebit, Amount: amount},
			{LedgerAccount: "customer_reserved_exposure", Side: billing.JournalCredit, Amount: amount},
		}
		posted, replayed, postErr := s.postJournalInTx(ctx, tx, journal)
		if postErr != nil || replayed {
			if postErr != nil {
				return billing.Settlement{}, postErr
			}
			return billing.Settlement{}, ErrOperationConflict
		}
		rememberSequence(posted.AccountSequence)
		settlement.AuthorizationRelease = billing.Posting{OperationKey: releaseSource, Transaction: posted, Before: before, After: afterSnapshot}
	} else {
		settlement.AuthorizationRelease = billing.Posting{OperationKey: releaseSource, Before: before, After: afterSnapshot}
	}
	if err := s.settlementFault("after_release_journal"); err != nil {
		return billing.Settlement{}, err
	}

	now := time.Now().UTC()
	holdResult, err := tx.NewRaw(`UPDATE authorization_holds SET status = 'closed', released_amount_nano = ?, closed_reason = ?, closed_source_key = ?, closed_fingerprint = ?, closed_amount_nano = ?, closed_at = ? WHERE hold_key = ? AND status = 'open'`, hold.AmountNano, string(billing.ReleaseSettled), releaseSource, resultFingerprint, remaining, now, hold.HoldKey).Exec(ctx)
	if err != nil {
		return billing.Settlement{}, fmt.Errorf("billingstore: close settlement hold: %w", err)
	}
	if err := requireRowsAffected(holdResult, 1, "close settlement hold"); err != nil {
		return billing.Settlement{}, fmt.Errorf("%w: %v", billing.ErrSettlementConflict, err)
	}
	if err := s.settlementFault("after_hold_close"); err != nil {
		return billing.Settlement{}, err
	}
	accountResult, err := tx.NewRaw(`UPDATE billing_accounts SET balance_nano = ?, reserved_nano = ?, version = ?, updated_at = ? WHERE account_id = ? AND version = ?`, afterSnapshot.BalanceNano, afterSnapshot.ReservedNano, afterSnapshot.Version, now, record.AccountID, before.Version).Exec(ctx)
	if err != nil {
		return billing.Settlement{}, fmt.Errorf("billingstore: update settled account: %w", err)
	}
	if err := requireRowsAffected(accountResult, 1, "settled account version"); err != nil {
		return billing.Settlement{}, fmt.Errorf("%w: %v", billing.ErrSettlementConflict, err)
	}
	if err := s.settlementFault("after_account_update"); err != nil {
		return billing.Settlement{}, err
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: customerSource, AccountID: record.AccountID, OperationKind: "customer_settlement", SourceKey: record.Key, Fingerprint: resultFingerprint, Before: before, After: afterSnapshot, SequenceStart: firstSequence, SequenceEnd: lastSequence}); err != nil {
		return billing.Settlement{}, err
	}
	for _, zero := range deferredZeroCosts {
		if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: zero.providerSource, AccountID: record.AccountID, OperationKind: "provider_cogs", SourceKey: zero.legKey, Fingerprint: zero.fingerprint, Before: before, After: afterSnapshot, SequenceStart: firstSequence, SequenceEnd: lastSequence}); err != nil {
			return billing.Settlement{}, err
		}
	}
	if remaining > 0 {
		fp, fpErr := result.SemanticFingerprint()
		if fpErr != nil {
			return billing.Settlement{}, fpErr
		}
		if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: releaseSource, AccountID: record.AccountID, OperationKind: "authorization_release", SourceKey: settlementReleaseSourcePrefix + record.Key, Fingerprint: fp, Before: before, After: afterSnapshot, SequenceStart: lastSequence, SequenceEnd: lastSequence}); err != nil {
			return billing.Settlement{}, err
		}
	} else {
		if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{OperationKey: releaseSource, AccountID: record.AccountID, OperationKind: "authorization_release", SourceKey: settlementReleaseSourcePrefix + record.Key, Fingerprint: resultFingerprint, Before: before, After: afterSnapshot, SequenceStart: firstSequence, SequenceEnd: lastSequence}); err != nil {
			return billing.Settlement{}, err
		}
	}
	if err := s.settlementFault("after_snapshot_write"); err != nil {
		return billing.Settlement{}, err
	}
	resultRef := customerSource
	// Claim owners may settle; unclaimed pending/retryable rows keep empty lease_owner
	// and remain settleable. A foreign lease_owner must fail closed so a stale worker
	// cannot post money after another replica reclaimed the TUR.
	processingResult, err := tx.NewRaw(`UPDATE usage_record_processing SET status = 'processed', lease_owner = '', lease_until = NULL, safe_error_code = '', result_ref = ?, updated_at = ? WHERE tur_key = ? AND tur_fingerprint = ? AND status IN ('pending','processing','retryable') AND (lease_owner = '' OR lease_owner = ?)`, resultRef, now, record.Key, record.Fingerprint, s.storeID).Exec(ctx)
	if err != nil {
		return billing.Settlement{}, fmt.Errorf("billingstore: mark settlement processed: %w", err)
	}
	if count, err := processingResult.RowsAffected(); err != nil {
		return billing.Settlement{}, err
	} else if count == 0 {
		var existing usageRecordProcessingRow
		if scanErr := tx.NewRaw(`SELECT tur_key, tur_fingerprint, status, lease_owner, lease_until, retry_count, safe_error_code, result_ref, updated_at FROM usage_record_processing WHERE tur_key = ? AND tur_fingerprint = ?`, record.Key, record.Fingerprint).Scan(ctx, &existing); scanErr != nil {
			if errors.Is(scanErr, sql.ErrNoRows) {
				return billing.Settlement{}, ErrProcessingNotFound
			}
			return billing.Settlement{}, scanErr
		}
		if existing.LeaseOwner != "" && existing.LeaseOwner != s.storeID {
			return billing.Settlement{}, fmt.Errorf("%w: processing lease owned by %q", billing.ErrSettlementConflict, existing.LeaseOwner)
		}
		// Never treat bare processed (or any other status) as license to commit money
		// when the settlement snapshot path did not already replay above.
		return billing.Settlement{}, fmt.Errorf("%w: processing status %s", billing.ErrSettlementConflict, existing.Status)
	}
	if err := s.settlementFault("after_processing_update"); err != nil {
		return billing.Settlement{}, err
	}
	if err := s.settlementFault("before_commit"); err != nil {
		return billing.Settlement{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.Settlement{}, fmt.Errorf("billingstore: commit settlement: %w", err)
	}
	return settlement, nil
}

func (s *DurableStore) settlementFault(step string) error {
	if s.settlementFaultHook == nil {
		return nil
	}
	return s.settlementFaultHook(step)
}

func settlementReleaseSourceKey(turKey string) string {
	return "authorization-release:v1:" + settlementReleaseSourcePrefix + turKey
}

func loadUsageRecordTx(ctx context.Context, tx bun.Tx, turKey string) (billing.TurnUsageRecord, error) {
	var payload string
	if err := tx.NewRaw(`SELECT payload_json FROM turn_usage_records WHERE tur_key = ?`, turKey).Scan(ctx, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.TurnUsageRecord{}, ErrUsageRecordNotFound
		}
		return billing.TurnUsageRecord{}, err
	}
	var record billing.TurnUsageRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return billing.TurnUsageRecord{}, fmt.Errorf("billingstore: decode settlement TUR: %w", err)
	}
	if err := billing.CheckReplay(record, record); err != nil {
		return billing.TurnUsageRecord{}, err
	}
	return record, nil
}

func loadSettlementHold(ctx context.Context, tx bun.Tx, accountID, authorizationID, turKey string) (authorizationHoldRow, error) {
	var hold authorizationHoldRow
	err := tx.NewRaw(`SELECT hold_key, authorization_id, account_id, tur_key, currency, amount_nano, status, source_key, fingerprint, pricing_ref, charge_policy_ref, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, closed_reason, released_amount_nano, closed_source_key, closed_fingerprint, closed_amount_nano, expires_at, created_at, closed_at FROM authorization_holds WHERE account_id = ? AND authorization_id = ? AND tur_key = ?`, accountID, authorizationID, turKey).Scan(ctx, &hold)
	if errors.Is(err, sql.ErrNoRows) {
		return authorizationHoldRow{}, ErrAuthorizationHoldNotFound
	}
	return hold, err
}

func (s *DurableStore) loadSettlementReplay(ctx context.Context, tx bun.Tx, record billing.TurnUsageRecord, result billing.BillingResult) (billing.Settlement, error) {
	customerSource, _ := billing.CustomerSettlementSourceKey(record.Key)
	customerSnapshot, found, err := loadOperationSnapshot(ctx, tx, record.AccountID, "customer_settlement", record.Key)
	if err != nil || !found {
		return billing.Settlement{}, fmt.Errorf("%w: customer settlement snapshot missing", billing.ErrSettlementConflict)
	}
	settlement := billing.Settlement{TURKey: record.Key, Replayed: true}
	customerJournal, err := settlementJournalForSnapshot(ctx, tx, record.AccountID, billing.JournalBookFinancial, customerSource)
	if err != nil {
		return billing.Settlement{}, err
	}
	settlement.Customer = postingFromSnapshot(customerSnapshot, customerJournal, true)
	for _, leg := range record.Legs {
		source, _ := billing.ProviderCostSourceKey(leg.Key)
		row, found, rowErr := loadOperationSnapshot(ctx, tx, record.AccountID, "provider_cogs", leg.Key)
		if rowErr != nil {
			return billing.Settlement{}, rowErr
		}
		if !found {
			return billing.Settlement{}, fmt.Errorf("%w: provider cost snapshot missing for %q", billing.ErrSettlementConflict, leg.Key)
		}
		journal, journalErr := settlementJournalForSnapshot(ctx, tx, record.AccountID, billing.JournalBookFinancial, source)
		if journalErr != nil {
			return billing.Settlement{}, journalErr
		}
		settlement.ProviderCosts = append(settlement.ProviderCosts, postingFromSnapshot(row, journal, true))
	}
	releaseSource := settlementReleaseSourceKey(record.Key)
	releaseSnapshot, releaseFound, releaseSnapErr := loadOperationSnapshot(ctx, tx, record.AccountID, "authorization_release", settlementReleaseSourcePrefix+record.Key)
	if releaseSnapErr != nil {
		return billing.Settlement{}, releaseSnapErr
	}
	if !releaseFound {
		return billing.Settlement{}, fmt.Errorf("%w: authorization release snapshot missing", billing.ErrSettlementConflict)
	}
	releaseJournal, releaseErr := settlementJournalForSnapshot(ctx, tx, record.AccountID, billing.JournalBookAuthorization, releaseSource)
	if releaseErr != nil {
		return billing.Settlement{}, releaseErr
	}
	settlement.AuthorizationRelease = postingFromSnapshot(releaseSnapshot, releaseJournal, true)
	return settlement, nil
}

// settlementJournalForSnapshot loads a journal when present. Zero-effect settlement
// legs (zero customer charge, zero provider cost, zero remaining hold) still write
// operation snapshots that may borrow sibling sequence ranges without posting a
// journal row for that leg.
func settlementJournalForSnapshot(ctx context.Context, tx bun.Tx, accountID string, book billing.JournalBook, sourceKey string) (billing.JournalTransaction, error) {
	journal, found, err := lookupJournalBySource(ctx, tx, accountID, book, sourceKey)
	if err != nil {
		return billing.JournalTransaction{}, err
	}
	if !found {
		return billing.JournalTransaction{}, nil
	}
	return journal, nil
}
