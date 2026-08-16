package billingstore

import "time"

type accountRow struct {
	ID             string    `bun:"account_id,pk"`
	Currency       string    `bun:"currency,notnull"`
	Mode           string    `bun:"mode,notnull"`
	CreditLimit    int64     `bun:"credit_limit_nano,notnull"`
	Balance        int64     `bun:"balance_nano,notnull"`
	OpeningBalance int64     `bun:"opening_balance_nano,notnull"`
	Reserved       int64     `bun:"reserved_nano,notnull"`
	Version        uint64    `bun:"version,notnull"`
	State          string    `bun:"state,notnull"`
	CreatedAt      time.Time `bun:"created_at,notnull"`
	UpdatedAt      time.Time `bun:"updated_at,notnull"`
}
type policyEventRow struct {
	ID          int64     `bun:"id,pk,autoincrement"`
	AccountID   string    `bun:"account_id,notnull"`
	EventKey    string    `bun:"event_key,notnull"`
	Mode        string    `bun:"mode,notnull"`
	Currency    string    `bun:"currency,notnull"`
	CreditLimit int64     `bun:"credit_limit_nano,notnull"`
	EffectiveAt time.Time `bun:"effective_at,notnull"`
	SourceKey   string    `bun:"source_key,notnull"`
	Fingerprint string    `bun:"fingerprint,notnull"`
	PayloadJSON string    `bun:"payload_json,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
}
type journalTransactionRow struct {
	ID                    string    `bun:"transaction_id,pk"`
	AccountID             string    `bun:"account_id,notnull"`
	Book                  string    `bun:"book,notnull"`
	Currency              string    `bun:"currency,notnull"`
	SourceKey             string    `bun:"source_key,notnull"`
	SemanticFingerprint   string    `bun:"semantic_fingerprint,notnull"`
	TurnID                string    `bun:"turn_id,notnull"`
	ALegID                string    `bun:"a_leg_id,notnull"`
	BLegID                string    `bun:"b_leg_id,notnull"`
	AccountSequence       uint64    `bun:"account_sequence,notnull"`
	ReversalOf            string    `bun:"reversal_of,notnull"`
	CorrectsTransactionID string    `bun:"corrects_transaction_id,notnull"`
	CorrectionGroupID     string    `bun:"correction_group_id,notnull"`
	OperationKind         string    `bun:"operation_kind,notnull"`
	BalanceBefore         int64     `bun:"balance_before_nano,notnull"`
	BalanceAfter          int64     `bun:"balance_after_nano,notnull"`
	ReservedBefore        int64     `bun:"reserved_before_nano,notnull"`
	ReservedAfter         int64     `bun:"reserved_after_nano,notnull"`
	SpendableBefore       int64     `bun:"spendable_before_nano,notnull"`
	SpendableAfter        int64     `bun:"spendable_after_nano,notnull"`
	CreditFloor           int64     `bun:"credit_floor_nano,notnull"`
	CreditLimit           int64     `bun:"credit_limit_nano,notnull"`
	Mode                  string    `bun:"mode,notnull"`
	SnapshotVersionBefore uint64    `bun:"snapshot_version_before,notnull"`
	SnapshotVersionAfter  uint64    `bun:"snapshot_version_after,notnull"`
	RecordedAt            time.Time `bun:"recorded_at,notnull"`
}
type operationSnapshotRow struct {
	OperationKey         string    `bun:"operation_key,pk"`
	AccountID            string    `bun:"account_id,notnull"`
	OperationKind        string    `bun:"operation_kind,notnull"`
	SourceKey            string    `bun:"source_key,notnull"`
	Fingerprint          string    `bun:"fingerprint,notnull"`
	IntegrityFingerprint string    `bun:"integrity_fingerprint,notnull"`
	Currency             string    `bun:"currency,notnull"`
	Mode                 string    `bun:"mode,notnull"`
	BalanceBefore        int64     `bun:"balance_before_nano,notnull"`
	BalanceAfter         int64     `bun:"balance_after_nano,notnull"`
	ReservedBefore       int64     `bun:"reserved_before_nano,notnull"`
	ReservedAfter        int64     `bun:"reserved_after_nano,notnull"`
	SpendableBefore      int64     `bun:"spendable_before_nano,notnull"`
	SpendableAfter       int64     `bun:"spendable_after_nano,notnull"`
	CreditFloor          int64     `bun:"credit_floor_nano,notnull"`
	CreditLimit          int64     `bun:"credit_limit_nano,notnull"`
	VersionBefore        uint64    `bun:"version_before,notnull"`
	VersionAfter         uint64    `bun:"version_after,notnull"`
	SequenceStart        uint64    `bun:"account_sequence_start,notnull"`
	SequenceEnd          uint64    `bun:"account_sequence_end,notnull"`
	CreatedAt            time.Time `bun:"created_at,notnull"`
}
type journalEntryRow struct {
	TransactionID string `bun:"transaction_id,pk"`
	Ordinal       int    `bun:"ordinal,pk"`
	LedgerAccount string `bun:"ledger_account,notnull"`
	Side          string `bun:"side,notnull"`
	Currency      string `bun:"currency,notnull"`
	AmountNano    int64  `bun:"amount_nano,notnull"`
}
