package billing

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrAccountInvalid        = errors.New("billing: invalid account")
	ErrAccountNotReady       = errors.New("billing: account is not ready")
	ErrInsufficientSpendable = errors.New("billing: insufficient spendable balance")
)

type AccountMode string

const (
	AccountPrepaid  AccountMode = "prepaid"
	AccountPostpaid AccountMode = "postpaid"
)

type AccountState string

const (
	AccountReady             AccountState = "ready"
	AccountReconcileRequired AccountState = "reconcile_required"
)

// Account is the materialized admission state reconstructed from the journal.
// BalanceNano is signed customer-account credits minus debits. ReservedNano is
// authorization exposure and never part of posted financial balance.
type Account struct {
	ID          string
	Currency    string
	Mode        AccountMode
	CreditLimit int64

	BalanceNano  int64
	ReservedNano int64
	Version      uint64
	State        AccountState
}

func (a Account) Validate() error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.Currency) == "" {
		return fmt.Errorf("%w: account id and currency are required", ErrAccountInvalid)
	}
	if a.Mode != AccountPrepaid && a.Mode != AccountPostpaid {
		return fmt.Errorf("%w: unsupported mode %q", ErrAccountInvalid, a.Mode)
	}
	if a.CreditLimit < 0 || (a.Mode == AccountPrepaid && a.CreditLimit != 0) {
		return fmt.Errorf("%w: invalid credit limit %d", ErrAccountInvalid, a.CreditLimit)
	}
	if a.ReservedNano < 0 {
		return fmt.Errorf("%w: reserved amount cannot be negative", ErrAccountInvalid)
	}
	if a.State != AccountReady && a.State != AccountReconcileRequired {
		return fmt.Errorf("%w: unsupported state %q", ErrAccountInvalid, a.State)
	}
	return nil
}

func (a Account) CreditFloorNano() int64 {
	if a.Mode == AccountPostpaid {
		return -a.CreditLimit
	}
	return 0
}

// SpendableNano applies Balance - CreditFloor - Reserved using checked math.
func (a Account) SpendableNano() (int64, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	floor := a.CreditFloorNano()
	available, err := checkedSub(a.BalanceNano, floor)
	if err != nil {
		return 0, err
	}
	return checkedSub(available, a.ReservedNano)
}

func (a Account) Spendable() (Money, error) {
	nano, err := a.SpendableNano()
	if err != nil {
		return Money{}, err
	}
	return Money{Nano: nano, Currency: a.Currency}, nil
}

// Authorize returns a detached account with an additional hold. It never
// mutates the receiver and rejects reconcile-required accounts before money is
// reserved.
func (a Account) Authorize(amount Money) (Account, error) {
	if err := a.Validate(); err != nil {
		return Account{}, err
	}
	if a.State != AccountReady {
		return Account{}, ErrAccountNotReady
	}
	if err := amount.Validate(); err != nil {
		return Account{}, err
	}
	if amount.Currency != a.Currency {
		return Account{}, ErrMoneyCurrencyMismatch
	}
	if amount.Nano < 0 {
		return Account{}, fmt.Errorf("%w: authorization amount must be non-negative", ErrAccountInvalid)
	}
	spendable, err := a.SpendableNano()
	if err != nil {
		return Account{}, err
	}
	if spendable < amount.Nano {
		return Account{}, ErrInsufficientSpendable
	}
	reserved, err := checkedAdd(a.ReservedNano, amount.Nano)
	if err != nil {
		return Account{}, err
	}
	out := a
	out.ReservedNano = reserved
	return out, nil
}

func (a Account) Release(amount Money) (Account, error) {
	if err := a.Validate(); err != nil {
		return Account{}, err
	}
	if amount.Currency != a.Currency {
		return Account{}, ErrMoneyCurrencyMismatch
	}
	if amount.Nano < 0 || amount.Nano > a.ReservedNano {
		return Account{}, fmt.Errorf("%w: release amount exceeds reserved exposure", ErrAccountInvalid)
	}
	out := a
	out.ReservedNano -= amount.Nano
	return out, nil
}

func (a Account) ApplyBalanceDelta(delta Money) (Account, error) {
	if err := a.Validate(); err != nil {
		return Account{}, err
	}
	if delta.Currency != a.Currency {
		return Account{}, ErrMoneyCurrencyMismatch
	}
	balance, err := checkedAdd(a.BalanceNano, delta.Nano)
	if err != nil {
		return Account{}, err
	}
	floor := a.CreditFloorNano()
	if balance < floor {
		return Account{}, fmt.Errorf("%w: balance %d below credit floor %d", ErrInsufficientSpendable, balance, floor)
	}
	out := a
	out.BalanceNano = balance
	return out, nil
}
