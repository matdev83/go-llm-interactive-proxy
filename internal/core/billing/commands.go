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
)

type (
	ReleaseReason       string
	AdjustmentDirection string
)

const (
	AdjustmentCredit AdjustmentDirection = "credit"
	AdjustmentDebit  AdjustmentDirection = "debit"
)

func (d AdjustmentDirection) Valid() bool {
	return d == AdjustmentCredit || d == AdjustmentDebit
}

type FundingInput struct {
	AccountID string
	Amount    Money
	SourceKey string
	Reason    string
}
type PaymentInput struct {
	AccountID string
	Amount    Money
	SourceKey string
	Reason    string
}
type AdjustmentInput struct {
	AccountID string
	Amount    Money
	Direction AdjustmentDirection
	SourceKey string
	Reason    string
}
type CreditPolicyInput struct {
	AccountID   string
	Mode        AccountMode
	Currency    string
	CreditLimit int64
	SourceKey   string
	Reason      string
	EffectiveAt time.Time
}
type Posting struct {
	OperationKey string
	Transaction  JournalTransaction
	Before       AccountSnapshot
	After        AccountSnapshot
	Replayed     bool
}
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

func FundingJournalIntent(input FundingInput) (JournalTransaction, error) {
	return financialJournalIntent("funding", input.Amount, strings.TrimSpace(input.AccountID), input.SourceKey, input.Reason, "")
}

func PaymentJournalIntent(input PaymentInput) (JournalTransaction, error) {
	return financialJournalIntent("payment", input.Amount, strings.TrimSpace(input.AccountID), input.SourceKey, input.Reason, "")
}

func AdjustmentJournalIntent(input AdjustmentInput) (JournalTransaction, error) {
	return financialJournalIntent("adjustment", input.Amount, strings.TrimSpace(input.AccountID), input.SourceKey, input.Reason, input.Direction)
}

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
