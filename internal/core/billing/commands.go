package billing

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrTrustedCommandInvalid      = errors.New("billing: invalid trusted command")
	ErrUnsafeCreditLimitReduction = errors.New("billing: unsafe credit-limit reduction")
	ErrReleaseNotEligible         = errors.New("billing: authorization release is not safely eligible")
	ErrReleaseReasonInvalid       = errors.New("billing: invalid authorization release reason")
	ErrHoldReleaseBlocked         = errors.New("billing: hold release blocked while usage settlement is pending")
)

// ReleaseReason is a closed set of auditable authorization lifecycle reasons.
type ReleaseReason string

const (
	ReleaseExecutionNotStarted ReleaseReason = "execution_not_started"
	ReleaseSettled             ReleaseReason = "settled"
	ReleaseZeroCharge          ReleaseReason = "zero_charge"
	ReleaseStaleSafe           ReleaseReason = "stale_safe_release"
	ReleaseOperator            ReleaseReason = "operator_release"
)

func (r ReleaseReason) Valid() bool {
	switch r {
	case ReleaseExecutionNotStarted, ReleaseSettled, ReleaseZeroCharge, ReleaseStaleSafe, ReleaseOperator:
		return true
	default:
		return false
	}
}

// AdjustmentDirection selects one of the two trusted customer-account
// adjustment semantics. Callers cannot select arbitrary ledger accounts.
type AdjustmentDirection string

const (
	AdjustmentCredit AdjustmentDirection = "credit"
	AdjustmentDebit  AdjustmentDirection = "debit"
)

func (d AdjustmentDirection) Valid() bool {
	return d == AdjustmentCredit || d == AdjustmentDebit
}

// FundingInput is a trusted top-up command. It always increases the customer
// financial account and uses the fixed cash/payment-clearing accounts.
type FundingInput struct {
	AccountID string
	Amount    Money
	SourceKey string
	Reason    string
}

// PaymentInput is a trusted payment command. It has the same balanced
// financial semantics as funding but retains a distinct operation identity.
type PaymentInput struct {
	AccountID string
	Amount    Money
	SourceKey string
	Reason    string
}

// AdjustmentInput is an audited operator adjustment with closed direction
// semantics. It deliberately has no ledger-account fields.
type AdjustmentInput struct {
	AccountID string
	Amount    Money
	Direction AdjustmentDirection
	SourceKey string
	Reason    string
}

// CreditPolicyInput changes the durable postpaid credit policy without creating
// fake money. The account policy event is immutable and audited.
type CreditPolicyInput struct {
	AccountID   string
	Mode        AccountMode
	Currency    string
	CreditLimit int64
	SourceKey   string
	Reason      string
	EffectiveAt time.Time
}

// ReleaseAuthorizationInput requests a deterministic full or partial hold
// release. A zero Amount with FullClose closes the remaining exposure.
type ReleaseAuthorizationInput struct {
	AccountID            string
	AuthorizationID      string
	TURKey               string
	Amount               Money
	FullClose            bool
	SourceKey            string
	Reason               ReleaseReason
	AlegInactiveAt       time.Time
	Now                  time.Time
	MaximumExecutionLife time.Duration
	SafetyGrace          time.Duration
}

// Posting is the common result of a trusted monetary operation. Snapshots are
// redundant evidence; the journal transaction remains financial truth.
type Posting struct {
	OperationKey string
	Transaction  JournalTransaction
	Before       AccountSnapshot
	After        AccountSnapshot
	Replayed     bool
}

// PolicyChange is the result of an audited policy mutation. It has no journal
// transaction because changing a limit is not money movement.
type PolicyChange struct {
	OperationKey string
	Before       AccountSnapshot
	After        AccountSnapshot
	Replayed     bool
}

func (in FundingInput) normalized(kind string) (FundingInput, error) {
	out := in
	out.AccountID = strings.TrimSpace(out.AccountID)
	out.SourceKey = strings.TrimSpace(out.SourceKey)
	out.Reason = strings.TrimSpace(out.Reason)
	if out.AccountID == "" || out.SourceKey == "" || out.Reason == "" {
		return FundingInput{}, fmt.Errorf("%w: %s account, source key, and reason are required", ErrTrustedCommandInvalid, kind)
	}
	if err := out.Amount.Validate(); err != nil {
		return FundingInput{}, err
	}
	if out.Amount.Nano <= 0 {
		return FundingInput{}, fmt.Errorf("%w: %s amount must be positive", ErrTrustedCommandInvalid, kind)
	}
	return out, nil
}

func (in FundingInput) Validate() error                   { _, err := in.normalized("funding"); return err }
func (in FundingInput) Fingerprint() (string, error)      { return in.fingerprint("funding") }
func (in PaymentInput) Fingerprint() (string, error)      { return in.fingerprint() }
func (in AdjustmentInput) Fingerprint() (string, error)   { return in.fingerprint() }
func (in CreditPolicyInput) Fingerprint() (string, error) { return in.fingerprint() }
func (in ReleaseAuthorizationInput) Fingerprint(amount int64) (string, error) {
	return in.fingerprint(amount)
}

func (in PaymentInput) Validate() error {
	_, err := FundingInput(in).normalized("payment")
	return err
}

func (in AdjustmentInput) Validate() error {
	if strings.TrimSpace(in.AccountID) == "" || strings.TrimSpace(in.SourceKey) == "" || strings.TrimSpace(in.Reason) == "" || !in.Direction.Valid() {
		return fmt.Errorf("%w: adjustment identity, direction, and reason are required", ErrTrustedCommandInvalid)
	}
	if err := in.Amount.Validate(); err != nil {
		return err
	}
	if in.Amount.Nano <= 0 {
		return fmt.Errorf("%w: adjustment amount must be positive", ErrTrustedCommandInvalid)
	}
	return nil
}

func (in CreditPolicyInput) Validate() error {
	if strings.TrimSpace(in.AccountID) == "" || strings.TrimSpace(in.SourceKey) == "" || strings.TrimSpace(in.Reason) == "" {
		return fmt.Errorf("%w: policy account, source key, and reason are required", ErrTrustedCommandInvalid)
	}
	if in.Mode != AccountPrepaid && in.Mode != AccountPostpaid {
		return fmt.Errorf("%w: unsupported policy mode", ErrTrustedCommandInvalid)
	}
	if strings.TrimSpace(in.Currency) == "" || in.CreditLimit < 0 || (in.Mode == AccountPrepaid && in.CreditLimit != 0) {
		return fmt.Errorf("%w: invalid policy currency/limit", ErrTrustedCommandInvalid)
	}
	return nil
}

func (in ReleaseAuthorizationInput) Validate() error {
	if strings.TrimSpace(in.AccountID) == "" || strings.TrimSpace(in.AuthorizationID) == "" || strings.TrimSpace(in.TURKey) == "" {
		return fmt.Errorf("%w: release identities are required", ErrTrustedCommandInvalid)
	}
	if !in.Reason.Valid() {
		return ErrReleaseReasonInvalid
	}
	if in.Amount.Nano < 0 || (in.Amount.Nano > 0 && strings.TrimSpace(in.Amount.Currency) == "") {
		return fmt.Errorf("%w: invalid release amount", ErrTrustedCommandInvalid)
	}
	// Zero amount is only meaningful as "close remaining exposure" via FullClose
	// or the closed zero_charge reason. A bare zero amount would burn the source
	// key as a no-op posting.
	if in.Amount.Nano == 0 && !in.FullClose && in.Reason != ReleaseZeroCharge {
		return fmt.Errorf("%w: zero release amount requires FullClose or zero_charge reason", ErrTrustedCommandInvalid)
	}
	if in.Reason == ReleaseStaleSafe {
		if in.AlegInactiveAt.IsZero() || in.Now.IsZero() || in.MaximumExecutionLife <= 0 || in.SafetyGrace < 0 || in.Now.Before(in.AlegInactiveAt) {
			return ErrReleaseNotEligible
		}
	}
	return nil
}

func trustedFingerprint(kind string, value any) (string, error) {
	payload, err := json.Marshal(struct {
		Version string `json:"v"`
		Kind    string `json:"kind"`
		Value   any    `json:"value"`
	}{"trusted-command:v1", kind, value})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("trusted-command:v1:%x", sum[:]), nil
}

func (in FundingInput) fingerprint(kind string) (string, error) {
	n := struct {
		AccountID, AmountCurrency, SourceKey, Reason string
		Amount                                       int64
	}{strings.TrimSpace(in.AccountID), in.Amount.Currency, strings.TrimSpace(in.SourceKey), strings.TrimSpace(in.Reason), in.Amount.Nano}
	return trustedFingerprint(kind, n)
}

func (in PaymentInput) fingerprint() (string, error) {
	return FundingInput(in).fingerprint("payment")
}

func (in AdjustmentInput) fingerprint() (string, error) {
	n := struct {
		AccountID, AmountCurrency, SourceKey, Reason string
		Amount                                       int64
		Direction                                    AdjustmentDirection
	}{strings.TrimSpace(in.AccountID), in.Amount.Currency, strings.TrimSpace(in.SourceKey), strings.TrimSpace(in.Reason), in.Amount.Nano, in.Direction}
	return trustedFingerprint("adjustment", n)
}

func (in CreditPolicyInput) fingerprint() (string, error) {
	n := struct {
		AccountID, Currency, SourceKey, Reason string
		Mode                                   AccountMode
		Limit                                  int64
		EffectiveAt                            time.Time
	}{strings.TrimSpace(in.AccountID), strings.TrimSpace(in.Currency), strings.TrimSpace(in.SourceKey), strings.TrimSpace(in.Reason), in.Mode, in.CreditLimit, in.EffectiveAt.UTC()}
	return trustedFingerprint("credit-policy", n)
}

func (in ReleaseAuthorizationInput) fingerprint(amount int64) (string, error) {
	n := struct {
		AccountID, AuthorizationID, TURKey, SourceKey string
		Amount                                        int64
		Reason                                        ReleaseReason
		FullClose                                     bool
	}{strings.TrimSpace(in.AccountID), strings.TrimSpace(in.AuthorizationID), strings.TrimSpace(in.TURKey), strings.TrimSpace(in.SourceKey), amount, in.Reason, in.FullClose}
	return trustedFingerprint("authorization-release", n)
}

// FundingJournalIntent is the closed cash-to-customer funding posting shape.
func FundingJournalIntent(input FundingInput) (JournalTransaction, error) {
	return financialJournalIntent("funding", input.Amount, strings.TrimSpace(input.AccountID), input.SourceKey, input.Reason, "")
}

// PaymentJournalIntent is the closed payment posting shape. It uses the same
// ledger accounts as funding and a distinct operation identity.
func PaymentJournalIntent(input PaymentInput) (JournalTransaction, error) {
	return financialJournalIntent("payment", input.Amount, strings.TrimSpace(input.AccountID), input.SourceKey, input.Reason, "")
}

// AdjustmentJournalIntent is the closed operator adjustment posting shape.
func AdjustmentJournalIntent(input AdjustmentInput) (JournalTransaction, error) {
	return financialJournalIntent("adjustment", input.Amount, strings.TrimSpace(input.AccountID), input.SourceKey, input.Reason, input.Direction)
}

// ScopedOperationKey is the globally unique durable identity for one trusted
// command. Journal transaction IDs and operation-snapshot primary keys are
// process-global, so account identity is part of the key even though replay
// lookup remains account-scoped. Length-prefix each caller-controlled field so
// account/source pairs that contain ':' cannot collide.
func ScopedOperationKey(kind, accountID, sourceKey string) string {
	kind = strings.TrimSpace(kind)
	accountID = strings.TrimSpace(accountID)
	sourceKey = strings.TrimSpace(sourceKey)
	return fmt.Sprintf("%s:v1:%d:%s:%d:%s", kind, len(accountID), accountID, len(sourceKey), sourceKey)
}

func financialJournalIntent(kind string, amount Money, accountID, sourceKey, reason string, direction AdjustmentDirection) (JournalTransaction, error) {
	if amount.Nano <= 0 || strings.TrimSpace(accountID) == "" {
		return JournalTransaction{}, fmt.Errorf("%w: invalid financial intent", ErrTrustedCommandInvalid)
	}
	var source string
	switch kind {
	case "funding", "payment", "adjustment":
		source = ScopedOperationKey(kind, accountID, sourceKey)
	default:
		return JournalTransaction{}, fmt.Errorf("%w: unsupported financial intent kind", ErrTrustedCommandInvalid)
	}
	if strings.TrimSpace(sourceKey) == "" || reason == "" {
		return JournalTransaction{}, fmt.Errorf("%w: source/reason required", ErrTrustedCommandInvalid)
	}
	debit, credit := "cash_payment_clearing", "customer_financial_account"
	if kind == "adjustment" {
		if direction == AdjustmentDebit {
			debit, credit = "customer_financial_account", "customer_adjustment_clearing"
		} else {
			debit, credit = "customer_adjustment_clearing", "customer_financial_account"
		}
	}
	return JournalTransaction{ID: source, Book: JournalBookFinancial, Currency: amount.Currency, SourceKey: source, AccountID: accountID, Entries: []JournalEntry{{LedgerAccount: debit, Side: JournalDebit, Amount: amount}, {LedgerAccount: credit, Side: JournalCredit, Amount: amount}}}, nil
}
