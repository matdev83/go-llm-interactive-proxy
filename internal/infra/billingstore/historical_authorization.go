package billingstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// HistoricalAuthorizationJournal is an audit-only decode of a retired
// authorization posting. It is deliberately not billing.JournalTransaction:
// current validation, writers, reconciliation, and reports cannot post or
// replay this historical book.
type HistoricalAuthorizationJournal struct {
	ID                    string
	AccountID             string
	Currency              string
	SourceKey             string
	SemanticFingerprint   string
	TurnID                string
	ALegID                string
	BLegID                string
	AccountSequence       *int64
	ReversalOf            string
	CorrectsTransactionID string
	CorrectionGroupID     string
	OperationKind         string
	BalanceBeforeNano     int64
	BalanceAfterNano      int64
	ReservedBeforeNano    int64
	ReservedAfterNano     int64
	SpendableBeforeNano   int64
	SpendableAfterNano    int64
	CreditFloorNano       int64
	CreditLimitNano       int64
	Mode                  string
	SnapshotVersionBefore uint64
	SnapshotVersionAfter  uint64
	RecordedAt            time.Time
	Entries               []billing.JournalEntry
}

// HistoricalAuthorizationJournals reads old authorization rows for audit and
// migration tooling only. No current report calls this reader.
func (s *DurableStore) HistoricalAuthorizationJournals(ctx context.Context, accountID string) ([]HistoricalAuthorizationJournal, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	var rows []historicalAuthorizationRow
	if err := s.db.NewRaw(`
SELECT transaction_id, account_id, currency, source_key, semantic_fingerprint,
 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of,
 corrects_transaction_id, correction_group_id, operation_kind,
 balance_before_nano, balance_after_nano, reserved_before_nano,
 reserved_after_nano, spendable_before_nano, spendable_after_nano,
 credit_floor_nano, credit_limit_nano, mode, snapshot_version_before,
 snapshot_version_after, recorded_at
FROM journal_transactions WHERE account_id = ? AND book = 'authorization'
ORDER BY recorded_at, transaction_id`, accountID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: historical authorization decode: %w", err)
	}
	out := make([]HistoricalAuthorizationJournal, 0, len(rows))
	for _, row := range rows {
		entries, err := historicalJournalEntries(ctx, s, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, HistoricalAuthorizationJournal{
			ID: row.ID, AccountID: row.AccountID, Currency: row.Currency, SourceKey: row.SourceKey,
			SemanticFingerprint: row.SemanticFingerprint, TurnID: row.TurnID, ALegID: row.ALegID, BLegID: row.BLegID,
			AccountSequence: row.AccountSequence, ReversalOf: row.ReversalOf, CorrectsTransactionID: row.CorrectsTransactionID,
			CorrectionGroupID: row.CorrectionGroupID, OperationKind: row.OperationKind,
			BalanceBeforeNano: row.BalanceBefore, BalanceAfterNano: row.BalanceAfter,
			ReservedBeforeNano: row.ReservedBefore, ReservedAfterNano: row.ReservedAfter,
			SpendableBeforeNano: row.SpendableBefore, SpendableAfterNano: row.SpendableAfter,
			CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: row.Mode,
			SnapshotVersionBefore: row.VersionBefore, SnapshotVersionAfter: row.VersionAfter,
			RecordedAt: row.RecordedAt, Entries: entries,
		})
	}
	return out, nil
}

type historicalAuthorizationRow struct {
	ID                    string    `bun:"transaction_id"`
	AccountID             string    `bun:"account_id"`
	Currency              string    `bun:"currency"`
	SourceKey             string    `bun:"source_key"`
	SemanticFingerprint   string    `bun:"semantic_fingerprint"`
	TurnID                string    `bun:"turn_id"`
	ALegID                string    `bun:"a_leg_id"`
	BLegID                string    `bun:"b_leg_id"`
	AccountSequence       *int64    `bun:"account_sequence"`
	ReversalOf            string    `bun:"reversal_of"`
	CorrectsTransactionID string    `bun:"corrects_transaction_id"`
	CorrectionGroupID     string    `bun:"correction_group_id"`
	OperationKind         string    `bun:"operation_kind"`
	BalanceBefore         int64     `bun:"balance_before_nano"`
	BalanceAfter          int64     `bun:"balance_after_nano"`
	ReservedBefore        int64     `bun:"reserved_before_nano"`
	ReservedAfter         int64     `bun:"reserved_after_nano"`
	SpendableBefore       int64     `bun:"spendable_before_nano"`
	SpendableAfter        int64     `bun:"spendable_after_nano"`
	CreditFloor           int64     `bun:"credit_floor_nano"`
	CreditLimit           int64     `bun:"credit_limit_nano"`
	Mode                  string    `bun:"mode"`
	VersionBefore         uint64    `bun:"snapshot_version_before"`
	VersionAfter          uint64    `bun:"snapshot_version_after"`
	RecordedAt            time.Time `bun:"recorded_at"`
}

func historicalJournalEntries(ctx context.Context, s *DurableStore, id string) ([]billing.JournalEntry, error) {
	var rows []journalEntryRow
	if err := s.db.NewRaw(`SELECT transaction_id, ordinal, ledger_account, side, currency, amount_nano FROM journal_entries WHERE transaction_id = ? ORDER BY ordinal`, id).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: historical authorization entries: %w", err)
	}
	entries := make([]billing.JournalEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, billing.JournalEntry{LedgerAccount: row.LedgerAccount, Side: billing.JournalSide(row.Side), Amount: billing.Money{Nano: row.AmountNano, Currency: row.Currency}})
	}
	return entries, nil
}

// legacySnapshotIntegrityZero verifies snapshots written before the current
// snapshot DTO removed its reserved field. Those historical rows remain
// auditable without reintroducing the field into current billing types.
func legacySnapshotIntegrityZero(operationKey, accountID, operationKind, sourceKey, fingerprint string, before, after billing.AccountSnapshot, sequenceStart, sequenceEnd uint64) string {
	type legacyAccountSnapshot struct {
		BalanceNano     int64
		LegacyHeldNano  int64 `json:"ReservedNano"`
		SpendableNano   int64
		CreditFloorNano int64
		CreditLimitNano int64
		Mode            billing.AccountMode
		Currency        string
		Version         uint64
	}
	type legacySnapshot struct {
		Version                                                        string
		OperationKey, AccountID, OperationKind, SourceKey, Fingerprint string
		Before, After                                                  legacyAccountSnapshot
		SequenceStart, SequenceEnd                                     uint64
	}
	payload, _ := json.Marshal(legacySnapshot{
		Version: "snapshot:v1", OperationKey: operationKey, AccountID: accountID, OperationKind: operationKind, SourceKey: sourceKey, Fingerprint: fingerprint,
		Before:        legacyAccountSnapshot{BalanceNano: before.BalanceNano, LegacyHeldNano: 0, SpendableNano: before.SpendableNano, CreditFloorNano: before.CreditFloorNano, CreditLimitNano: before.CreditLimitNano, Mode: before.Mode, Currency: before.Currency, Version: before.Version},
		After:         legacyAccountSnapshot{BalanceNano: after.BalanceNano, LegacyHeldNano: 0, SpendableNano: after.SpendableNano, CreditFloorNano: after.CreditFloorNano, CreditLimitNano: after.CreditLimitNano, Mode: after.Mode, Currency: after.Currency, Version: after.Version},
		SequenceStart: sequenceStart, SequenceEnd: sequenceEnd,
	})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("snapshot:v1:%x", digest[:])
}
