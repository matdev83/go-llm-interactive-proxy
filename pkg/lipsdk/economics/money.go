package economics

import (
	"fmt"
	"math"
	"math/bits"
	"strings"
)

const tokensPerMillion = 1_000_000

// Money is a currency-tagged amount in nano-units of the major currency unit.
// Present distinguishes authoritative zero from an absent amount (requirement 6.2).
type Money struct {
	NanoUnits int64  `json:"nano_units"`
	Currency  string `json:"currency,omitempty"`
	Present   bool   `json:"present"`
}

// Add returns the checked sum of two present amounts in the same currency.
func (m Money) Add(other Money) (Money, error) {
	if err := requirePresentSameCurrency(m, other); err != nil {
		return Money{}, err
	}
	sum, ok := addMoneyChecked(m.NanoUnits, other.NanoUnits)
	if !ok {
		return Money{}, fmt.Errorf("economics: money add overflow")
	}
	return Money{NanoUnits: sum, Currency: m.Currency, Present: true}, nil
}

// Sub returns the checked difference m - other for non-negative present amounts.
func (m Money) Sub(other Money) (Money, error) {
	if err := requirePresentSameCurrency(m, other); err != nil {
		return Money{}, err
	}
	diff, ok := subMoneyChecked(m.NanoUnits, other.NanoUnits)
	if !ok {
		return Money{}, fmt.Errorf("economics: money sub underflow")
	}
	return Money{NanoUnits: diff, Currency: m.Currency, Present: true}, nil
}

// MulTokensByRatePer1M multiplies token count by a per-1M nano rate with overflow
// detection. A successful result is always Present, including authoritative zero
// when tokens or rate are non-positive (mirrors Phase 2 checked multiply semantics).
func MulTokensByRatePer1M(tokens, pricePer1MNano int64) (Money, error) {
	if tokens < 0 || pricePer1MNano < 0 {
		return Money{}, fmt.Errorf("economics: tokens and rate must be non-negative")
	}
	if tokens == 0 || pricePer1MNano == 0 {
		return Money{NanoUnits: 0, Present: true}, nil
	}
	hi, lo := bits.Mul64(uint64(tokens), uint64(pricePer1MNano))
	div := uint64(tokensPerMillion)
	if hi >= div {
		return Money{}, fmt.Errorf("economics: token*rate overflow")
	}
	q, _ := bits.Div64(hi, lo, div)
	if q > math.MaxInt64 {
		return Money{}, fmt.Errorf("economics: token*rate overflow")
	}
	return Money{NanoUnits: int64(q), Present: true}, nil
}

func requirePresentSameCurrency(a, b Money) error {
	if !a.Present || !b.Present {
		return fmt.Errorf("economics: both money operands must be present")
	}
	ac := strings.TrimSpace(a.Currency)
	bc := strings.TrimSpace(b.Currency)
	if ac == "" || bc == "" {
		return fmt.Errorf("economics: currency required")
	}
	if ac != bc {
		return fmt.Errorf("economics: currency mismatch %q vs %q", ac, bc)
	}
	return nil
}

func addMoneyChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	sum, carry := bits.Add64(uint64(a), uint64(b), 0)
	if carry != 0 || sum > math.MaxInt64 {
		return 0, false
	}
	return int64(sum), true
}

func subMoneyChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a < b {
		return 0, false
	}
	return a - b, true
}
