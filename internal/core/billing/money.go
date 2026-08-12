package billing

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrMoneyCurrencyMismatch = errors.New("billing: money currency mismatch")
	ErrMoneyOverflow         = errors.New("billing: money arithmetic overflow")
	ErrMoneyInvalid          = errors.New("billing: invalid money")
)

// Money is the authoritative fixed-point monetary value. Nano is signed and
// Currency is an immutable ISO-like currency identifier; floating point is
// deliberately not part of the billing domain.
type Money struct {
	Nano     int64
	Currency string
}

func NewMoney(nano int64, currency string) (Money, error) {
	currency = strings.TrimSpace(currency)
	if currency == "" {
		return Money{}, fmt.Errorf("%w: currency is required", ErrMoneyInvalid)
	}
	return Money{Nano: nano, Currency: currency}, nil
}

func (m Money) Validate() error {
	if strings.TrimSpace(m.Currency) == "" {
		return fmt.Errorf("%w: currency is required", ErrMoneyInvalid)
	}
	return nil
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	nano, err := checkedAdd(m.Nano, other.Nano)
	if err != nil {
		return Money{}, err
	}
	return Money{Nano: nano, Currency: m.Currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	if other.Nano == math.MinInt64 {
		return Money{}, ErrMoneyOverflow
	}
	nano, err := checkedAdd(m.Nano, -other.Nano)
	if err != nil {
		return Money{}, err
	}
	return Money{Nano: nano, Currency: m.Currency}, nil
}

func (m Money) Neg() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if m.Nano == math.MinInt64 {
		return Money{}, ErrMoneyOverflow
	}
	return Money{Nano: -m.Nano, Currency: m.Currency}, nil
}

func (m Money) IsNonNegative() bool { return m.Nano >= 0 }

func (m Money) sameCurrency(other Money) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := other.Validate(); err != nil {
		return err
	}
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: %q versus %q", ErrMoneyCurrencyMismatch, m.Currency, other.Currency)
	}
	return nil
}

func checkedAdd(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrMoneyOverflow
	}
	return a + b, nil
}

func checkedSub(a, b int64) (int64, error) {
	if b == math.MinInt64 {
		return 0, ErrMoneyOverflow
	}
	return checkedAdd(a, -b)
}
