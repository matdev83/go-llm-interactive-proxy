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

type authorizationHoldRow struct {
	HoldKey           string     `bun:"hold_key,pk"`
	AuthorizationID   string     `bun:"authorization_id,notnull"`
	AccountID         string     `bun:"account_id,notnull"`
	TURKey            string     `bun:"tur_key,notnull"`
	Currency          string     `bun:"currency,notnull"`
	AmountNano        int64      `bun:"amount_nano,notnull"`
	Status            string     `bun:"status,notnull"`
	SourceKey         string     `bun:"source_key,notnull"`
	Fingerprint       string     `bun:"fingerprint,notnull"`
	PricingRef        string     `bun:"pricing_ref,notnull"`
	ChargePolicyRef   string     `bun:"charge_policy_ref,notnull"`
	Mode              string     `bun:"mode,notnull"`
	BalanceBefore     int64      `bun:"balance_before_nano,notnull"`
	BalanceAfter      int64      `bun:"balance_after_nano,notnull"`
	ReservedBefore    int64      `bun:"reserved_before_nano,notnull"`
	ReservedAfter     int64      `bun:"reserved_after_nano,notnull"`
	SpendableBefore   int64      `bun:"spendable_before_nano,notnull"`
	SpendableAfter    int64      `bun:"spendable_after_nano,notnull"`
	CreditFloor       int64      `bun:"credit_floor_nano,notnull"`
	CreditLimit       int64      `bun:"credit_limit_nano,notnull"`
	VersionBefore     uint64     `bun:"version_before,notnull"`
	VersionAfter      uint64     `bun:"version_after,notnull"`
	ClosedReason      string     `bun:"closed_reason,notnull"`
	ReleasedAmount    int64      `bun:"released_amount_nano,notnull"`
	ClosedSourceKey   string     `bun:"closed_source_key,notnull"`
	ClosedFingerprint string     `bun:"closed_fingerprint,notnull"`
	ClosedAmount      int64      `bun:"closed_amount_nano,notnull"`
	ExpiresAt         time.Time  `bun:"expires_at,notnull"`
	CreatedAt         time.Time  `bun:"created_at,notnull"`
	ClosedAt          *time.Time `bun:"closed_at,nullzero"`
}

type turnUsageRecordRow struct {
	TURKey             string    `bun:"tur_key,pk"`
	Fingerprint        string    `bun:"fingerprint,notnull"`
	SchemaVersion      int       `bun:"schema_version,notnull"`
	AccountID          string    `bun:"account_id,notnull"`
	TurnID             string    `bun:"turn_id,notnull"`
	ALegID             string    `bun:"a_leg_id,notnull"`
	AuthorizationID    string    `bun:"authorization_id,notnull"`
	SessionID          string    `bun:"session_id,notnull"`
	StartedAt          time.Time `bun:"started_at,notnull"`
	FinishedAt         time.Time `bun:"finished_at,notnull"`
	Outcome            string    `bun:"outcome,notnull"`
	CustomerPricingRef string    `bun:"customer_pricing_ref,notnull"`
	ChargePolicyRef    string    `bun:"charge_policy_ref,notnull"`
	PayloadJSON        string    `bun:"payload_json,notnull"`
	SealedAt           time.Time `bun:"sealed_at,notnull"`
}

type legUsageRecordRow struct {
	LURKey      string    `bun:"lur_key,pk"`
	TURKey      string    `bun:"tur_key,notnull"`
	Fingerprint string    `bun:"fingerprint,notnull"`
	ALegID      string    `bun:"a_leg_id,notnull"`
	BLegID      string    `bun:"b_leg_id,notnull"`
	Sequence    int       `bun:"sequence,notnull"`
	BackendID   string    `bun:"backend_id,notnull"`
	ProviderID  string    `bun:"provider_id,notnull"`
	ModelID     string    `bun:"model_id,notnull"`
	Outcome     string    `bun:"outcome,notnull"`
	Surfaced    string    `bun:"surfaced,notnull"`
	PayloadJSON string    `bun:"payload_json,notnull"`
	SealedAt    time.Time `bun:"sealed_at,notnull"`
}

type usageRecordProcessingRow struct {
	TURKey         string    `bun:"tur_key,pk"`
	TURFingerprint string    `bun:"tur_fingerprint,notnull"`
	Status         string    `bun:"status,notnull"`
	LeaseOwner     string    `bun:"lease_owner,notnull"`
	LeaseUntil     time.Time `bun:"lease_until,nullzero"`
	RetryCount     int       `bun:"retry_count,notnull"`
	SafeErrorCode  string    `bun:"safe_error_code,notnull"`
	ResultRef      string    `bun:"result_ref,notnull"`
	UpdatedAt      time.Time `bun:"updated_at,notnull"`
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
