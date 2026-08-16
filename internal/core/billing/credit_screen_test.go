package billing

import (
	"context"
	"errors"
	"testing"
)

type creditScreenAccountStore struct {
	account Account
	err     error
	calls   int
}

func (s *creditScreenAccountStore) GetAccount(context.Context, string) (Account, error) {
	s.calls++
	return s.account, s.err
}

func TestCheapCreditScreenAllowsConfiguredHeadroom(t *testing.T) {
	store := &creditScreenAccountStore{account: Account{
		ID: "acct", Currency: "USD", Mode: AccountPrepaid, BalanceNano: 100,
		State: AccountReady,
	}}
	gate := CheapCreditScreen{Store: store, Currency: "USD", MinPreRouteHeadroomNano: 50}
	if err := gate.Check(context.Background(), "acct"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("account reads = %d, want 1", store.calls)
	}
}

func TestCheapCreditScreenDeniesBelowMinimumBeforeRouting(t *testing.T) {
	store := &creditScreenAccountStore{account: Account{
		ID: "acct", Currency: "USD", Mode: AccountPostpaid, CreditLimit: 100,
		BalanceNano: -60, State: AccountReady,
	}}
	gate := CheapCreditScreen{Store: store, Currency: "USD", MinPreRouteHeadroomNano: 50}
	if err := gate.Check(context.Background(), "acct"); !errors.Is(err, ErrCreditScreenDenied) {
		t.Fatalf("Check = %v, want ErrCreditScreenDenied", err)
	}
}

func TestCheapCreditScreenZeroMinimumAllowsZeroHeadroom(t *testing.T) {
	store := &creditScreenAccountStore{account: Account{
		ID: "acct", Currency: "USD", Mode: AccountPrepaid, BalanceNano: 0,
		State: AccountReady,
	}}
	gate := CheapCreditScreen{Store: store, Currency: "USD"}
	if err := gate.Check(context.Background(), "acct"); err != nil {
		t.Fatalf("zero-headroom Check: %v", err)
	}
}

func TestCheapCreditScreenFailsClosedForUnavailableInvalidOrWrongCurrency(t *testing.T) {
	tests := []struct {
		name  string
		store *creditScreenAccountStore
		want  error
	}{
		{name: "store unavailable", store: &creditScreenAccountStore{err: errors.New("down")}, want: ErrCreditScreenUnavailable},
		{name: "not ready", store: &creditScreenAccountStore{account: Account{ID: "acct", Currency: "USD", Mode: AccountPrepaid, State: AccountReconcileRequired}}, want: ErrCreditScreenDenied},
		{name: "currency mismatch", store: &creditScreenAccountStore{account: Account{ID: "acct", Currency: "EUR", Mode: AccountPrepaid, State: AccountReady}}, want: ErrCreditScreenInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := CheapCreditScreen{Store: tt.store, Currency: "USD"}
			if err := gate.Check(context.Background(), "acct"); !errors.Is(err, tt.want) {
				t.Fatalf("Check = %v, want %v", err, tt.want)
			}
		})
	}
}
