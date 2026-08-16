package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrCreditScreenDenied      = errors.New("billing: cheap credit screen denied")
	ErrCreditScreenUnavailable = errors.New("billing: cheap credit screen unavailable")
	ErrCreditScreenInvalid     = errors.New("billing: invalid cheap credit screen")
)

type CreditScreenStore interface {
	GetAccount(context.Context, string) (Account, error)
}
type CheapCreditScreen struct {
	Store                   CreditScreenStore
	Currency                string
	MinPreRouteHeadroomNano int64
}

func (s CheapCreditScreen) Check(ctx context.Context, accountID string) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrCreditScreenInvalid)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: context: %v", ErrCreditScreenUnavailable, err)
	}
	if s.Store == nil {
		return fmt.Errorf("%w: account store is required", ErrCreditScreenUnavailable)
	}
	accountID = strings.TrimSpace(accountID)
	currency := strings.TrimSpace(s.Currency)
	if accountID == "" || currency == "" || s.MinPreRouteHeadroomNano < 0 {
		return fmt.Errorf("%w: account, currency, and non-negative minimum headroom are required", ErrCreditScreenInvalid)
	}
	account, err := s.Store.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("%w: account lookup: %w", ErrCreditScreenUnavailable, err)
	}
	if err := account.Validate(); err != nil {
		return fmt.Errorf("%w: account: %w", ErrCreditScreenInvalid, err)
	}
	if account.ID != accountID || account.Currency != currency {
		return fmt.Errorf("%w: account identity or currency mismatch", ErrCreditScreenInvalid)
	}
	if account.State != AccountReady {
		return fmt.Errorf("%w: account state is %s", ErrCreditScreenDenied, account.State)
	}
	headroom, err := SettledHeadroom(account)
	if err != nil {
		return fmt.Errorf("%w: settled headroom: %w", ErrCreditScreenInvalid, err)
	}
	if headroom.Nano < s.MinPreRouteHeadroomNano {
		return fmt.Errorf("%w: settled headroom %d below minimum %d", ErrCreditScreenDenied, headroom.Nano, s.MinPreRouteHeadroomNano)
	}
	return nil
}
