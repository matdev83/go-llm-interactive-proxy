package billing

import "errors"

var ErrBillingStoreUnavailable = errors.New("billing: store unavailable")

type AccountSnapshot struct {
	BalanceNano     int64
	SpendableNano   int64
	CreditFloorNano int64
	CreditLimitNano int64
	Mode            AccountMode
	Currency        string
	Version         uint64
}
